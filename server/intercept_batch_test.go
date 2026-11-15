package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/intercept"
)

func TestInterceptBatchDecideRiskAndAlwaysAllow(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	m := &Manager{pg: d, interceptor: intercept.New(d)}
	s := &Server{m: m}
	s.auth.signKey.set([]byte("approval-batch-test-signing-key"))
	h := s.Handler()
	token, err := signJWT(s.auth.signKey.get())
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	mk := func(tool, input string) int64 {
		id, err := m.pg.CreateInterceptPending(0, 0, "approval-batch", "worker", tool, []byte(input), "ask", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id) })
		return id
	}
	low := mk("Bash", `{"command":"ls -la"}`)
	high := mk("Bash", `{"command":"sudo rm -rf /"}`)
	allowID := mk("Bash", `{"command":"git push origin main"}`)

	// Pending DTO carries a deterministic risk and always-allow prefix.
	r := do(http.MethodGet, "/api/intercept/pending", "")
	if r.Code != 200 {
		t.Fatalf("pending: %d %s", r.Code, r.Body)
	}
	var list struct {
		Pending []struct {
			ID     int64                `json:"id"`
			Risk   intercept.RiskReport `json:"risk"`
			Prefix string               `json:"always_allow_prefix"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	byID := map[int64]struct {
		Risk   intercept.RiskReport
		Prefix string
	}{}
	for _, p := range list.Pending {
		byID[p.ID] = struct {
			Risk   intercept.RiskReport
			Prefix string
		}{p.Risk, p.Prefix}
	}
	if byID[low].Risk.Level != "low" || byID[high].Risk.Level != "high" {
		t.Fatalf("risk levels: low=%+v high=%+v", byID[low].Risk, byID[high].Risk)
	}
	if byID[allowID].Prefix != "git push" {
		t.Fatalf("always-allow prefix=%q", byID[allowID].Prefix)
	}

	// Batch decide is per-id and partial-safe.
	r = do(http.MethodPost, "/api/intercept/pending/batch-decide",
		fmt.Sprintf(`{"ids":[%d,%d],"decision":"denied"}`, low, high))
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"decided":2`) {
		t.Fatalf("batch: %d %s", r.Code, r.Body.String())
	}
	for _, id := range []int64{low, high} {
		p, _ := m.pg.GetInterceptPending(id)
		if p == nil || p.Status != "denied" {
			t.Fatalf("pending %d status=%+v", id, p)
		}
	}

	// Always-allow creates the argv rule and leaves the pending request pending.
	r = do(http.MethodPost, fmt.Sprintf("/api/intercept/pending/%d/always-allow", allowID), "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"created":true`) {
		t.Fatalf("always-allow: %d %s", r.Code, r.Body.String())
	}
	var created struct {
		Rule db.InterceptRule `json:"rule"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteInterceptRule(created.Rule.ID) })
	if created.Rule.MatchType != "argv" || created.Rule.Pattern != `["git","push"]` || created.Rule.Action != "allow" {
		t.Fatalf("rule=%+v", created.Rule)
	}
	if p, _ := m.pg.GetInterceptPending(allowID); p == nil || p.Status != "pending" {
		t.Fatalf("always-allow must not decide the request: %+v", p)
	}
}
