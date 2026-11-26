package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/llmpool"
)

// The status list drives the "轮询顺序" strip in the UI, so it has to match the
// order PoolProfiles actually runs: active first, then priority DESC, then id ASC
// (input arrives id-ordered, so equal priorities must keep their relative order).
func TestSortPoolStatusMatchesChainOrder(t *testing.T) {
	in := []LLMPoolMemberStatus{
		{ProfileID: "1", Name: "low", Priority: 0},
		{ProfileID: "2", Name: "active", Priority: 0, Active: true},
		{ProfileID: "3", Name: "high", Priority: 10},
		{ProfileID: "4", Name: "mid-a", Priority: 5},
		{ProfileID: "5", Name: "mid-b", Priority: 5},
	}
	sortPoolStatus(in)

	want := []string{"active", "high", "mid-a", "mid-b", "low"}
	for i, w := range want {
		if in[i].Name != w {
			got := make([]string, len(in))
			for j, m := range in {
				got[j] = m.Name
			}
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// The active profile heads the chain no matter how low its own priority is —
// that's the documented precedence, and the UI must not imply otherwise.
func TestActiveProfileHeadsStatusList(t *testing.T) {
	in := []LLMPoolMemberStatus{
		{ProfileID: "1", Name: "loud", Priority: 999},
		{ProfileID: "2", Name: "active", Priority: -5, Active: true},
	}
	sortPoolStatus(in)
	if in[0].Name != "active" {
		t.Fatalf("head = %q, want the active profile regardless of priority", in[0].Name)
	}
}

func TestLLMPoolStatusIncludesPolicyHalfOpenAndStickyCounts(t *testing.T) {
	reg := llmpool.NewRegistry(nil, nil)
	reg.SetPolicy(4, 2*time.Minute)
	reg.Restore(1, llmpool.State{
		Trips:     1,
		OpenUntil: time.Now().Add(-time.Second),
		LastError: "timeout",
	})
	m1 := &llmpool.Member{ID: 1, Name: "a", Model: "m-a", Rank: llmpool.RankActive}
	pool := llmpool.New([]*llmpool.Member{m1}, reg)

	s := &Server{
		llm:       llmState{prov: pool},
		providers: providerState{health: reg},
	}
	status := s.llmPoolStatus()
	pol, ok := status["policy"].(map[string]any)
	if !ok {
		t.Fatalf("missing policy in status: %+v", status)
	}
	if pol["soft_trip"] != 4 || pol["fixed_cooldown"] != true {
		t.Fatalf("unexpected policy snapshot: %+v", pol)
	}
}

func TestPGLLMPoolUpdateMembersAndTestChainEndpoints(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	id1, err := m.pg.SaveProfile(&db.LLMProfile{Name: "pool-u-1", Format: "openai", Model: "m1", APIKey: "k1", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(id1) })
	id2, err := m.pg.SaveProfile(&db.LLMProfile{Name: "pool-u-2", Format: "openai", Model: "m2", APIKey: "k2", Priority: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(id2) })

	s := newAdmissionTestServer(m, nil)
	s.providers.health = llmpool.NewRegistry(nil, nil)
	body, _ := json.Marshal(map[string]any{
		"members": []map[string]any{
			{"profile_id": i64s(id1), "priority": 20, "pool_exclude": true},
			{"profile_id": i64s(id2), "priority": 50, "pool_exclude": false},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/llm/pool/members", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.pgLLMPoolUpdateMembers(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pgLLMPoolUpdateMembers status=%d body=%s", rec.Code, rec.Body.String())
	}
	p1, _ := m.pg.ProfileByID(id1)
	p2, _ := m.pg.ProfileByID(id2)
	if p1 == nil || p1.Priority != 20 || !p1.PoolExclude {
		t.Fatalf("profile 1 not updated: %+v", p1)
	}
	if p2 == nil || p2.Priority != 50 || p2.PoolExclude {
		t.Fatalf("profile 2 not updated: %+v", p2)
	}
}

func TestPGLLMPoolTestChainEndpoint(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
	}))
	defer okServer.Close()

	badServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key"}}`))
	}))
	defer badServer.Close()

	id1, err := m.pg.SaveProfile(&db.LLMProfile{Name: "chain-ok", Format: "openai", BaseURL: okServer.URL, Model: "m-ok", APIKey: "k1", Streaming: false, Priority: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(id1) })
	id2, err := m.pg.SaveProfile(&db.LLMProfile{Name: "chain-bad", Format: "openai", BaseURL: badServer.URL, Model: "m-bad", APIKey: "k2", Streaming: false, Priority: 10})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteProfile(id2) })

	s := newAdmissionTestServer(m, nil)
	s.providers.health = llmpool.NewRegistry(nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/llm/pool/test-chain", nil)
	rec := httptest.NewRecorder()
	s.pgLLMPoolTestChain(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pgLLMPoolTestChain status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Results []poolChainTestResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	foundOK, foundBad := false, false
	for _, item := range payload.Results {
		if item.ProfileID == i64s(id1) && item.OK && item.Reply == "OK" {
			foundOK = true
		}
		if item.ProfileID == i64s(id2) && !item.OK && item.Error != "" {
			foundBad = true
		}
	}
	if !foundOK || !foundBad {
		t.Fatalf("unexpected test-chain results: %+v", payload.Results)
	}
}
