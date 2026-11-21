package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

func TestManagementToolsRequireTrustedPrincipal(t *testing.T) {
	s := &Server{skillDir: t.TempDir()}
	in := json.RawMessage(`{"name":"scope-test","description":"fixture"}`)
	for _, p := range []runPrincipal{{}, {Role: "read"}, {Role: "operator"}, {Actor: "admin"}} {
		ctx := withPrincipal(t.Context(), p)
		tool := s.toolCreateSkill()
		if got := restrictManagementTools(ctx, []actool.CoreTool{tool}); len(got) != 0 {
			t.Fatalf("management exposed to %+v", p)
		}
		res, err := tool.Call(ctx, in, nil)
		if err != nil || !res.IsError {
			t.Fatalf("management allowed %+v: %+v %v", p, res, err)
		}
		if _, err := os.Stat(filepath.Join(s.skillDir, "scope-test")); !os.IsNotExist(err) {
			t.Fatalf("rejected tool wrote file: %v", err)
		}
	}
	res, err := s.toolCreateSkill().Call(withPrincipal(context.Background(), runPrincipal{Role: "admin"}), in, nil)
	if err != nil || res.IsError {
		t.Fatalf("admin denied: %+v %v", res, err)
	}
	if !canManagePlatform(withPrincipal(t.Context(), systemPrincipal())) {
		t.Fatal("explicit system delegation denied")
	}
}

func TestOperatorAliasesAndPrincipal(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	tok, plain, err := s.m.pg.CreateAPIToken("principal fixture", "operator")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.m.pg.PurgeAPIToken(tok.ID) })
	for _, path := range []string{"/api/visibility/toggle", "/api/visibility/skill/toggle", "/api/triggers/1"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		if path == "/api/triggers/1" {
			req.Method = http.MethodPatch
		}
		req.Header.Set("Authorization", "Bearer "+plain)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d", path, w.Code)
		}
	}
	called := false
	h := s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		p := principalFrom(r.Context())
		if p.Role != "operator" || p.Actor != "token:principal fixture" {
			t.Errorf("lost principal: %+v", p)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !called || w.Code != http.StatusNoContent {
		t.Fatal("operator lost task operations")
	}
}

func TestTriggerQueueNeverMergesDifferentPrincipals(t *testing.T) {
	for _, mode := range []string{"all", "by_task"} {
		q := newTriggerQueue()
		cfg := triggerBehavior{runMode: "serial", mergeMode: mode}
		admin := triggeredRun{agentKey: "auto", taskID: 1, message: "admin", mergeable: true, principal: runPrincipal{Role: "admin"}}
		op := admin
		op.message = "operator"
		op.principal = runPrincipal{Role: "operator"}
		q.Enqueue("auto", admin, cfg, false)
		q.Enqueue("auto", op, cfg, false)
		q.Enqueue("auto", admin, cfg, false)
		q.mu.Lock()
		first := q.nextLocked("auto", cfg)
		next := q.nextLocked("auto", cfg)
		q.mu.Unlock()
		if first.principal.Role != "admin" || next.principal.Role != "operator" || next.message != "operator" {
			t.Fatalf("%s elevated merged principal: %+v %+v", mode, first, next)
		}
	}
}
