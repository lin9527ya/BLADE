package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPITokenAuthenticatesAndSetsActor(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	token, plain, err := m.PG().CreateAPIToken("prometheus test")
	if err != nil {
		t.Fatal(err)
	}
	defer m.PG().RevokeAPIToken(token.ID) //nolint:errcheck

	s := &Server{m: m, engine: &Engine{}}
	handler := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actorFrom(r) != "token:prometheus test" {
			t.Errorf("actor = %q", actorFrom(r))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/metrics.prometheus", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRevokeAPITokenPurgeRemovesRow(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	token, _, err := m.PG().CreateAPIToken("purge test")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, engine: &Engine{}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/api-tokens/1?purge=1", nil)
	req.SetPathValue("id", fmt.Sprint(token.ID))
	s.revokeAPIToken(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"purged":true`) {
		t.Fatalf("purge status=%d body=%s", rec.Code, rec.Body.String())
	}
	if n, err := m.PG().PurgeAPIToken(token.ID); err != nil || n != 0 {
		t.Fatalf("row still present after purge: n=%d err=%v", n, err)
	}

	// 不带 purge 的 DELETE 仍然是撤销而不是删除。
	second, _, err := m.PG().CreateAPIToken("revoke test")
	if err != nil {
		t.Fatal(err)
	}
	defer m.PG().PurgeAPIToken(second.ID) //nolint:errcheck
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/api-tokens/2", nil)
	req.SetPathValue("id", fmt.Sprint(second.ID))
	s.revokeAPIToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	items, err := m.PG().ListAPITokens()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.ID == second.ID && it.RevokedAt != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("revoked token row missing")
	}
}

func TestReadyzAndPrometheusText(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	s := &Server{m: m, engine: &Engine{}, ctx: context.Background()}

	rec := httptest.NewRecorder()
	s.readyz(rec, httptest.NewRequest(http.MethodGet, "/api/readyz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"database":"ok"`) {
		t.Fatalf("readyz status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.metricsPrometheus(rec, httptest.NewRequest(http.MethodGet, "/api/metrics.prometheus", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"blade_build_info", "blade_tasks", "blade_approvals_pending"} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestReadOnlyAPITokenCannotWrite(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer m.Close()
	token, plain, err := m.PG().CreateAPIToken("readonly test", "read")
	if err != nil {
		t.Fatal(err)
	}
	defer m.PG().RevokeAPIToken(token.ID) //nolint:errcheck

	s := &Server{m: m, engine: &Engine{}}
	handler := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/assets", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("read GET status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/assets", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read POST status = %d body=%s", rec.Code, rec.Body.String())
	}
}
