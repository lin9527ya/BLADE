package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

func srcCall(t *testing.T, ts *ToolSet, name, body string) string {
	t.Helper()
	for _, tool := range ts.WorkerTools() {
		if tool.Name() != name {
			continue
		}
		res, err := tool.Call(context.Background(), json.RawMessage(body), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return res.Flatten()
	}
	t.Fatalf("tool %q not registered", name)
	return ""
}

func TestSRCToolsStageCommands(t *testing.T) {
	root := t.TempDir()
	runDir := filepath.Join(root, "tasks", "1", "i1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(nil, "work#1")
	ts.SetRunDir(runDir)

	probe := srcCall(t, ts, "http_probe", `{"urls":["https://example.test/a"]}`)
	if !strings.Contains(probe, "run_with_bash") {
		t.Fatalf("http_probe output missing command: %s", probe)
	}
	if _, err := os.Stat(filepath.Join(runDir, "blade-probe.sh")); err != nil {
		t.Fatalf("probe script not staged: %v", err)
	}

	contract := srcCall(t, ts, "api_contract", `{"base_url":"https://example.test"}`)
	if !strings.Contains(contract, "run_with_bash") {
		t.Fatalf("api_contract output missing command: %s", contract)
	}

	js := srcCall(t, ts, "js_audit", `{"urls":["https://example.test/"]}`)
	if !strings.Contains(js, "run_with_bash") {
		t.Fatalf("js_audit output missing command: %s", js)
	}
	if _, err := os.Stat(filepath.Join(runDir, "blade-jsaudit.py")); err != nil {
		t.Fatalf("js audit script not staged: %v", err)
	}

	pa := filepath.Join(root, "a.json")
	pb := filepath.Join(root, "b.json")
	if err := os.WriteFile(pa, []byte(`{"id":1,"name":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pb, []byte(`{"id":1,"name":"a","secret":"b"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	diff := srcCall(t, ts, "diff_responses", `{"file_a":`+quoteJSON(pa)+`,"file_b":`+quoteJSON(pb)+`}`)
	if !strings.Contains(diff, "secret") {
		t.Fatalf("diff output missing key difference: %s", diff)
	}

	poc := srcCall(t, ts, "poc_scaffold", `{"vulnclass":"sqli","target":"https://example.test/?FUZZ=1","param":"id"}`)
	if !strings.Contains(poc, "run_with_bash") {
		t.Fatalf("poc_scaffold output missing command: %s", poc)
	}
	if !strings.Contains(poc, "path") {
		t.Fatalf("poc_scaffold output missing path: %s", poc)
	}

	if out := srcCall(t, ts, "auth_request", `{"asset_id":1,"url":"https://example.test"}`); !strings.Contains(out, "AssetStore") {
		t.Fatalf("auth_request without asset store should explain unavailability: %s", out)
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var _ actool.CoreTool

func TestNativeHTTPAndSessionJar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_ = r.ParseForm()
			if r.Form.Get("username") != "alice" || r.Form.Get("password") != "pw" {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "ok", Path: "/"})
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/me":
			c, err := r.Cookie("sid")
			if err != nil || c.Value != "ok" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"user":"alice"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	runDir := filepath.Join(root, "tasks", "1", "i1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(nil, "work#1")
	ts.SetRunDir(runDir)

	login := srcCall(t, ts, "session_login", `{"session":"alice","url":`+quoteJSON(srv.URL+"/login")+`,"username":"alice","password":"pw"}`)
	if !strings.Contains(login, `"cookie_names":["sid"]`) {
		t.Fatalf("session_login did not succeed: %s", login)
	}
	me := srcCall(t, ts, "http_request", `{"session":"alice","url":`+quoteJSON(srv.URL+"/me")+`}`)
	if !strings.Contains(me, `"status":200`) || !strings.Contains(me, `alice`) {
		t.Fatalf("http_request did not reuse the session: %s", me)
	}
}
