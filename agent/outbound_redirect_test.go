package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestHTTPValidateRedirectTargetsNoDB(t *testing.T) {
	ts := NewToolSet(nil, "worker")
	initURL, _ := url.Parse("http://198.51.100.10/login")

	cases := []struct {
		name      string
		target    string
		wantAllow bool
	}{
		{"same_origin", "http://198.51.100.10/callback", true},
		{"compliant_external", "https://203.0.113.50/cdn/app.js", true},
		{"file_scheme", "file:///etc/passwd", false},
		{"ftp_scheme", "ftp://203.0.113.50/pub", false},
		{"loopback_v4", "http://127.0.0.1:9898/api/health", false},
		{"loopback_v6", "http://[::1]:9898/api/health", false},
		{"ipv4_mapped_loopback", "http://[::ffff:127.0.0.1]:9898/api/health", false},
		{"metadata_ip", "http://169.254.169.254/latest/meta-data/", false},
		{"metadata_aws_v6", "http://[fd00:ec2::254]/latest/meta-data/", false},
		{"metadata_host", "http://metadata.google.internal/computeMetadata/v1/", false},
		{"private_10", "http://10.0.0.5/internal", false},
		{"private_192", "http://192.168.1.50/admin", false},
		{"cgnat_100_64", "http://100.64.1.2/internal", false},
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.target)
		if err != nil {
			t.Fatalf("%s parse: %v", tc.name, err)
		}
		err = ts.validateRedirectTarget(context.Background(), initURL, u)
		if tc.wantAllow && err != nil {
			t.Errorf("%s expected allow, got %v", tc.name, err)
		}
		if !tc.wantAllow && err == nil {
			t.Errorf("%s expected block, got nil", tc.name)
		}
	}
}

func TestHTTPValidateRedirectTargets(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("redirect test", "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)

	as := d.Assets()
	rule, err := d.CreateAssetInterceptRule("exact_domain", "blocked-partner.example", "test block", true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteAssetInterceptRule(rule.ID)

	if _, err := as.AddAgentScope(task.ID, "cidr", "10.20.0.0/16", "test intranet", "manual"); err != nil {
		t.Fatal(err)
	}

	ts := NewToolSet(d.Exploration(task.ExplorationID), "worker")
	ts.SetAssetStore(as, d.Companies())
	ts.SetTaskID(task.ID)
	initURL, _ := url.Parse("http://198.51.100.10/login")

	cases := []struct {
		name      string
		target    string
		wantAllow bool
	}{
		{"same_origin", "http://198.51.100.10/callback", true},
		{"compliant_external", "https://203.0.113.50/cdn/app.js", true},
		{"file_scheme", "file:///etc/passwd", false},
		{"loopback", "http://127.0.0.1:9898/api/health", false},
		{"ipv4_mapped_loopback", "http://[::ffff:127.0.0.1]:9898/api/health", false},
		{"metadata_ip", "http://169.254.169.254/latest/meta-data/", false},
		{"metadata_host", "http://metadata.google.internal/computeMetadata/v1/", false},
		{"unscoped_private_ip", "http://192.168.10.5/admin", false},
		{"scoped_private_ip", "http://10.20.1.5/intranet", true},
		{"blacklisted_domain", "https://blocked-partner.example/x", false},
	}

	for _, tc := range cases {
		u, err := url.Parse(tc.target)
		if err != nil {
			t.Fatalf("%s parse: %v", tc.name, err)
		}
		err = ts.validateRedirectTarget(context.Background(), initURL, u)
		if tc.wantAllow && err != nil {
			t.Errorf("%s expected allow, got %v", tc.name, err)
		}
		if !tc.wantAllow && err == nil {
			t.Errorf("%s expected block, got nil", tc.name)
		}
	}
}

type mockRedirectRT struct {
	sawTargetReq *http.Request
}

func (m *mockRedirectRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "198.51.100.10" {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": {"https://203.0.113.20/external"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	}
	clone := req.Clone(req.Context())
	clone.Host = req.Host
	m.sawTargetReq = clone
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    req,
	}, nil
}

func TestHTTPCrossOriginRedirectStripsSensitiveHeaders(t *testing.T) {
	ts := NewToolSet(nil, "worker")
	c, err := ts.buildHTTPClient(nil, true, true, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rt := &mockRedirectRT{}
	c.Transport = rt
	initURL, _ := url.Parse("http://198.51.100.10/start")
	custom := map[string]bool{
		"x-api-key":      true,
		"x-custom-token": true,
		"x-trace-id":     true,
	}
	c.CheckRedirect = ts.buildRedirectCheck(context.Background(), true, initURL, custom)

	req, _ := http.NewRequest(http.MethodGet, initURL.String(), nil)
	req.Host = "internal-vhost.local"
	req.Header.Set("Authorization", "Bearer top-secret")
	req.Header.Set("Cookie", "sid=supersecret")
	req.Header.Set("X-API-Key", "api-key-123")
	req.Header.Set("X-Custom-Token", "tok-456")
	req.Header.Set("Accept-Language", "zh-CN")

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("compliant external redirect failed: %v", err)
	}
	resp.Body.Close()
	if rt.sawTargetReq == nil {
		t.Fatal("target request did not run")
	}
	if rt.sawTargetReq.Host != "" && rt.sawTargetReq.Host != "203.0.113.20" {
		t.Fatalf("sticky custom Host leaked to external redirect: %q", rt.sawTargetReq.Host)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-API-Key", "X-Custom-Token"} {
		if got := rt.sawTargetReq.Header.Get(h); got != "" {
			t.Fatalf("sensitive header %s leaked across origin: %q", h, got)
		}
	}
	if got := rt.sawTargetReq.Header.Get("Accept-Language"); got != "zh-CN" {
		t.Fatalf("non-sensitive header unexpectedly stripped: %q", got)
	}
}

func TestSessionLoginInjectsCredentialIDWithoutEchoingSecret(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTaskWithOptions("session cred", "goal", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)

	runDir := filepath.Join(t.TempDir(), "tasks", fmt.Sprint(task.ID), "i1")
	ts := NewToolSet(d.Exploration(task.ExplorationID), "worker")
	ts.SetAssetStore(d.Assets(), d.Companies())
	ts.SetTaskID(task.ID)
	ts.SetRunDir(runDir)
	defer ts.CloseHTTP()

	intent, err := ts.ts.AddIntent(map[string]any{"summary": "login verify"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	ts.SetOwnerIntent(intent)

	var gotUser, gotPass, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/form":
			_ = r.ParseForm()
			gotUser = r.FormValue("username")
			gotPass = r.FormValue("password")
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "ok1"})
			w.Write([]byte("logged in"))
		case "/bearer":
			gotAuth = r.Header.Get("Authorization")
			w.Write([]byte("ok bearer"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	pwdOut := callTool(t, ts.WorkerTools(), "report_credential", fmt.Sprintf(
		`{"risk_area":"auth","summary":"portal admin","kind":"password","username":"alice","secret":"S3cr3t!Pass","evidence":"found config","confidence":"high","intent_id":%d}`, intent))
	pwdID := lootNodeID(t, pwdOut)
	tokOut := callTool(t, ts.WorkerTools(), "report_credential", fmt.Sprintf(
		`{"risk_area":"auth","summary":"api jwt","kind":"token","username":"svc","secret":"jwt-xyz-999","evidence":"found env","confidence":"high","intent_id":%d}`, intent))
	tokID := lootNodeID(t, tokOut)

	out1 := callTool(t, ts.WorkerTools(), "session_login", fmt.Sprintf(
		`{"session":"s1","url":"%s/form","credential_id":%d}`, srv.URL, pwdID))
	if gotUser != "alice" || gotPass != "S3cr3t!Pass" {
		t.Fatalf("password credential not injected: user=%q pass=%q out=%s", gotUser, gotPass, out1)
	}
	if strings.Contains(out1, "S3cr3t!Pass") {
		t.Fatalf("tool output leaked credential secret: %s", out1)
	}

	out2 := callTool(t, ts.WorkerTools(), "session_login", fmt.Sprintf(
		`{"session":"s2","url":"%s/bearer","credential_id":%d}`, srv.URL, tokID))
	if gotAuth != "Bearer jwt-xyz-999" {
		t.Fatalf("bearer credential not injected: auth=%q out=%s", gotAuth, out2)
	}
	if strings.Contains(out2, "jwt-xyz-999") {
		t.Fatalf("tool output leaked token secret: %s", out2)
	}
}

func TestJSAuditScriptBlocksFileSSRFAndBlacklist(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	var hitInternal atomic.Int32
	forbiddenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitInternal.Add(1)
		w.Write([]byte("should-never-be-fetched"))
	}))
	defer forbiddenSrv.Close()

	var targetSrv *httptest.Server
	targetSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.html":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html>
<script src="file:///etc/passwd"></script>
<script src="%s/secret.js"></script>
<script src="http://blocked.evil.test/track.js"></script>
<script src="/app.js"></script>
</html>`, forbiddenSrv.URL)
		case "/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprintf(w, "console.log('ok');\n//# sourceMappingURL=http://169.254.169.254/latest/meta-data/\n")
		case "/app.js.map":
			http.Redirect(w, r, forbiddenSrv.URL+"/smuggled", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer targetSrv.Close()

	runDir := t.TempDir()
	ts := NewToolSet(nil, "worker")
	ts.SetRunDir(runDir)
	script := ts.ensureJsAuditScript()
	if script == "" {
		t.Fatal("ensureJsAuditScript returned empty")
	}
	bf := filepath.Join(runDir, "block.json")
	rawRules, _ := json.Marshal([]map[string]string{{"kind": "exact_domain", "pattern": "blocked.evil.test"}})
	if err := os.WriteFile(bf, rawRules, 0o644); err != nil {
		t.Fatal(err)
	}

	outDir := filepath.Join(runDir, "jsmap")
	cmd := exec.Command("python3", script, "--out", outDir, "--block-file", bf, targetSrv.URL+"/index.html")
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python3 blade-jsaudit failed: %v\n%s", err, string(outBytes))
	}
	if hitInternal.Load() != 0 {
		t.Fatalf("js_audit accessed forbidden cross-origin loopback server (%d hits)", hitInternal.Load())
	}
	outText := string(outBytes)
	for _, wantBlocked := range []string{"file:///etc/passwd", "169.254.169.254", "blocked.evil.test", forbiddenSrv.URL} {
		if !strings.Contains(outText, wantBlocked) {
			t.Fatalf("expected audit output to report blocked target %q, got:\n%s", wantBlocked, outText)
		}
	}
}
