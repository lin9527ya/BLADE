package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMiniappAuditComposesCommandAndBoundary(t *testing.T) {
	taskRoot := t.TempDir()
	runDir := filepath.Join(taskRoot, "i1")
	src := filepath.Join(taskRoot, "miniapp", "wx1234567890abcdef", "source")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}

	ts := NewToolSet(nil, "work#1")
	ts.SetRunDir(runDir)
	ts.SetTaskRoot(taskRoot)
	tool := ts.miniappAudit()

	// Relative traversal within taskRoot works (../miniapp/...)
	in, _ := json.Marshal(map[string]any{
		"dir":   "../miniapp/wx1234567890abcdef/source",
		"appid": "wx1234567890abcdef",
	})
	res, err := tool.Call(context.Background(), in, nil)
	if err != nil || res.IsError {
		t.Fatalf("miniapp_audit call failed: res=%+v err=%v", res, err)
	}
	var out struct {
		RunWithBash string `json:"run_with_bash"`
		Out         string `json:"out"`
	}
	txt := extractResultText(res)
	if err := json.Unmarshal([]byte(txt), &out); err != nil {
		t.Fatalf("decode miniapp_audit json: %v (%s)", err, txt)
	}
	if !strings.Contains(out.RunWithBash, "--dir") || !strings.Contains(out.RunWithBash, "--appid 'wx1234567890abcdef'") {
		t.Fatalf("unexpected command: %q", out.RunWithBash)
	}

	// Outside taskRoot must be rejected
	outside := t.TempDir()
	badIn, _ := json.Marshal(map[string]any{"dir": outside})
	badRes, err := tool.Call(context.Background(), badIn, nil)
	if err != nil {
		t.Fatalf("Call outside err: %v", err)
	}
	if !badRes.IsError || !strings.Contains(extractResultText(badRes), "超出任务目录范围") {
		t.Fatalf("expected boundary error, got: %+v", badRes)
	}
}

func TestMiniappAuditDirScanPythonExecution(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	taskRoot := t.TempDir()
	runDir := filepath.Join(taskRoot, "i2")
	src := filepath.Join(taskRoot, "miniapp", "wxabcdef1234567890", "source")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "pages", "index"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "app.json"), []byte(`{"pages":["pages/index/index"],"subPackages":[{"root":"pkgA","pages":["p/d"]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	js := `
wx.request({url:"https://api.example.com/api/v1/user/info"});
wx.cloud.init({env:"prod-1a2b3c"});
wx.cloud.callFunction({name:"adminDeleteUser"});
wx.cloud.database().collection("orders").get();
var appSecret = "0123456789abcdef0123456789abcdef";
var jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnopqrstuvwxyz";
`
	if err := os.WriteFile(filepath.Join(src, "app-service.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}

	ts := NewToolSet(nil, "work#1")
	ts.SetRunDir(runDir)
	ts.SetTaskRoot(taskRoot)
	res, err := ts.miniappAudit().Call(context.Background(), json.RawMessage(`{"dir":"../miniapp/wxabcdef1234567890/source","appid":"wxabcdef1234567890"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("miniappAudit err=%v res=%+v", err, res)
	}
	outDir := filepath.Join(runDir, "miniapp-audit")
	cmd := exec.Command(py, filepath.Join(runDir, "blade-jsaudit.py"), "--dir", src, "--out", outDir, "--appid", "wxabcdef1234567890", "--no-osv")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("python dir scan failed: %v\n%s", err, raw)
	}
	repRaw, err := os.ReadFile(filepath.Join(outDir, "report.json"))
	if err != nil {
		t.Fatalf("read report.json: %v", err)
	}
	var rep struct {
		Routes    []string `json:"routes"`
		Endpoints []struct {
			URL string `json:"url"`
		} `json:"endpoints"`
		Secrets []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"secrets"`
		Cloud struct {
			Env       []string `json:"env"`
			Functions []string `json:"functions"`
		} `json:"cloud"`
	}
	if err := json.Unmarshal(repRaw, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Routes) != 2 || len(rep.Cloud.Env) == 0 || len(rep.Cloud.Functions) == 0 || len(rep.Endpoints) == 0 || len(rep.Secrets) == 0 {
		t.Fatalf("unexpected python miniapp audit report: %+v", rep)
	}
	for _, s := range rep.Secrets {
		if strings.Contains(s.Value, "0123456789abcdef0123456789abcdef") {
			t.Fatalf("unmasked secret leaked: %s", s.Value)
		}
	}
}

func TestMiniProgramScriptsEndToEndFixtureUnpackAndAudit(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	scriptsDir, err := filepath.Abs(filepath.Join("..", "skills", "mini-program", "scripts"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(scriptsDir, "miniapp_unpack.sh")); err != nil {
		t.Skip("skills/mini-program/scripts not present")
	}

	root := t.TempDir()
	runDir := filepath.Join(root, "i1")
	uploads := filepath.Join(root, "uploads")
	_ = os.MkdirAll(runDir, 0o755)
	_ = os.MkdirAll(uploads, 0o755)

	pkg := filepath.Join(uploads, "__APP__.wxapkg")
	const appID = "wx1234567890abcdef"
	fixCmd := exec.Command(py, filepath.Join(scriptsDir, "wxapkg_fixture.py"), "--out", pkg, "--sample", "--appid", appID)
	if raw, ferr := fixCmd.CombinedOutput(); ferr != nil {
		t.Fatalf("wxapkg_fixture failed: %v\n%s", ferr, raw)
	}

	unpackCmd := exec.Command(bash, filepath.Join(scriptsDir, "miniapp_unpack.sh"),
		"--in", uploads,
		"--out", filepath.Join(root, "miniapp"),
		"--appid", appID,
		"--quality", "fast")
	unpackCmd.Dir = runDir
	if raw, uerr := unpackCmd.CombinedOutput(); uerr != nil {
		t.Fatalf("miniapp_unpack.sh failed: %v\n%s", uerr, raw)
	}

	sourceDir, err := filepath.EvalSymlinks(filepath.Join(root, "miniapp", appID, "source"))
	if err != nil {
		t.Fatalf("source symlink missing: %v", err)
	}
	ts := NewToolSet(nil, "worker")
	ts.SetRunDir(runDir)
	ts.SetTaskRoot(root)
	res, err := ts.miniappAudit().Call(context.Background(), json.RawMessage(`{"dir":"../miniapp/`+appID+`/source","appid":"`+appID+`"}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("miniapp_audit call err=%v res=%+v", err, res)
	}
	outDir := filepath.Join(runDir, "miniapp-audit")
	scanCmd := exec.Command(py, filepath.Join(runDir, "blade-jsaudit.py"), "--dir", sourceDir, "--out", outDir, "--appid", appID, "--no-osv")
	if raw, serr := scanCmd.CombinedOutput(); serr != nil {
		t.Fatalf("python audit failed: %v\n%s", serr, raw)
	}
	if _, err := os.Stat(filepath.Join(outDir, "report.json")); err != nil {
		t.Fatalf("report.json missing: %v", err)
	}
}
