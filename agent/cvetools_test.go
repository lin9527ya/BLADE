package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

type fakeCVEService struct {
	*fakeExecService
	stagedName    string
	stagedPath    string
	stagedContent []byte
	submitTool    string
	submitCommand string
	submitDesc    string
	submitTimeout time.Duration
}

func (f *fakeCVEService) StageScript(_ context.Context, _ string, _ int64, name string, content []byte) (string, error) {
	f.stagedName = name
	f.stagedContent = append([]byte{}, content...)
	f.stagedPath = "/data/tasks/fake/cve/" + name
	return f.stagedPath, nil
}

func (f *fakeCVEService) SubmitTool(_ context.Context, _ string, _ int64, _, tool, command, desc string, timeout time.Duration) (ExecStatus, error) {
	f.submitTool = tool
	f.submitCommand = command
	f.submitDesc = desc
	f.submitTimeout = timeout
	return ExecStatus{ID: 7, Status: "running", OutputPath: "/data/tasks/fake/executions/7.log"}, nil
}

func cveToolSet(t *testing.T) (*ToolSet, *db.AssetStore, int64, int64, *fakeCVEService) {
	t.Helper()
	d := testDB(t)
	task, err := d.CreateTask("cve-tools-test", "CVE goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var expID int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, task.ID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM cve_library WHERE cve_id LIKE 'CVE-2098-%'`)
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE '%cve-test.example%' OR url LIKE '%cve-test.example%'`)
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, task.ID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
		d.Close()
	})
	as := d.Assets()
	if _, err := as.AddAgentScope(task.ID, "root_domain", "cve-test.example", "test scope", "agent"); err != nil {
		t.Fatal(err)
	}
	assetID, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{
		URL: "https://cve-test.example/", Technologies: []string{"Apache Log4j 2.14.1"},
		FaviconMMH3: "-5151", TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := NewToolSet(d.Exploration(expID), "test")
	ts.SetAssetStore(as, d.Companies())
	ts.SetTaskID(task.ID)
	fake := &fakeCVEService{fakeExecService: &fakeExecService{
		status: ExecStatus{ID: 7, Status: "completed", ExitCode: 0, Head: "head-ok", Tail: "tail-ok", OutputBytes: 12},
	}}
	ts.SetExecService(fake)
	return ts, as, task.ID, assetID, fake
}

func callCVETool(t *testing.T, tool actool.CoreTool, payload any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	res, err := tool.Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("tool call: %v", err)
	}
	return res.Flatten(), res.IsError
}

func createTestCVE(t *testing.T, as *db.AssetStore, cveID string) *db.CVE {
	t.Helper()
	c, err := as.CreateCVE(db.CVE{
		CVEID: cveID, Title: "test-cve: Log4j RCE", Severity: db.SeverityCritical, CVSSScore: 10,
		Description: "## 影响\n远程代码执行", AffectedProducts: []string{"log4j"},
		VersionConstraint: ">= 2.0.0, < 2.15.0",
		Fingerprint:       db.PlaybookFingerprint{Technologies: []string{"log4j"}},
		ProbeGuide:        "用 JNDI 回调确认", Refs: []string{"https://example.com/cve"},
		ExploitScript: "#!/bin/sh\necho SENTINEL-$BLADE_TARGET\n", ExploitLanguage: "bash",
		ExploitFilename: "cve.sh", ExploitUsage: "目标在 BLADE_TARGET；args 追加为 argv",
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLookupCVEsByAssetAndID(t *testing.T) {
	ts, as, _, assetID, _ := cveToolSet(t)
	c := createTestCVE(t, as, "CVE-2098-0100")
	lookup := ts.lookupCVEs()

	out, isErr := callCVETool(t, lookup, map[string]any{"asset_id": assetID})
	if isErr || !strings.Contains(out, "CVE-2098-0100") || !strings.Contains(out, "version_match") {
		t.Fatalf("lookup by asset: isErr=%v %s", isErr, out)
	}
	if strings.Contains(out, "SENTINEL-") {
		t.Fatalf("lookup must not return script body: %s", out)
	}
	out, isErr = callCVETool(t, lookup, map[string]any{"cve_id": c.CVEID})
	if isErr || !strings.Contains(out, "用 JNDI 回调确认") || !strings.Contains(out, "exploit_sha256") {
		t.Fatalf("lookup by id: isErr=%v %s", isErr, out)
	}
	if strings.Contains(out, "SENTINEL-") {
		t.Fatalf("lookup by id must not return script body: %s", out)
	}
}

func TestCVEExploitRefusals(t *testing.T) {
	ts, as, taskID, assetID, _ := cveToolSet(t)
	createTestCVE(t, as, "CVE-2098-0101")
	noScript, err := as.CreateCVE(db.CVE{
		CVEID: "CVE-2098-0102", Title: "no script", AffectedProducts: []string{"log4j"},
	})
	if err != nil {
		t.Fatal(err)
	}
	exploit := ts.cveExploit()

	// 任务上下文缺失。
	tsNoTask := NewToolSet(ts.ts, "test")
	tsNoTask.SetAssetStore(as, nil)
	if out, isErr := callCVETool(t, tsNoTask.cveExploit(), map[string]any{
		"cve_id": "CVE-2098-0101", "asset_id": assetID, "target": "https://cve-test.example/",
	}); !isErr || !strings.Contains(out, "任务上下文") {
		t.Fatalf("task-less exploit must refuse: isErr=%v %s", isErr, out)
	}

	// 无脚本：返回可读不能利用 + probe guide，而不是硬失败。
	if out, isErr := callCVETool(t, exploit, map[string]any{
		"cve_id": noScript.CVEID, "asset_id": assetID, "target": "https://cve-test.example/",
	}); isErr || !strings.Contains(out, `"available":false`) || !strings.Contains(out, "没有保存利用脚本") {
		t.Fatalf("no-script exploit should guide: isErr=%v %s", isErr, out)
	}

	// 版本不匹配。
	newAsset, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{
		URL: "https://new.cve-test.example/", Technologies: []string{"log4j 2.17.0"}, TaskID: taskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := callCVETool(t, exploit, map[string]any{
		"cve_id": "CVE-2098-0101", "asset_id": newAsset, "target": "https://new.cve-test.example/",
	}); isErr || !strings.Contains(out, `"available":false`) || !strings.Contains(out, "版本") {
		t.Fatalf("version mismatch should refuse: isErr=%v %s", isErr, out)
	}

	// target 不属于资产。
	if out, isErr := callCVETool(t, exploit, map[string]any{
		"cve_id": "CVE-2098-0101", "asset_id": assetID, "target": "https://evil.example/",
	}); isErr || !strings.Contains(out, `"available":false`) || !strings.Contains(out, "不属于资产") {
		t.Fatalf("target mismatch should refuse: isErr=%v %s", isErr, out)
	}

	// 资产不在任务范围。
	outside, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{
		URL: "https://outside.example/", Technologies: []string{"log4j 2.14.1"}, TaskID: taskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out, isErr := callCVETool(t, exploit, map[string]any{
		"cve_id": "CVE-2098-0101", "asset_id": outside, "target": "https://outside.example/",
	}); !isErr || !strings.Contains(out, "不在本任务范围") {
		t.Fatalf("out-of-scope asset should refuse: isErr=%v %s", isErr, out)
	}
}

func TestCVEExploitStagesAndSubmits(t *testing.T) {
	ts, as, _, assetID, fake := cveToolSet(t)
	createTestCVE(t, as, "CVE-2098-0103")
	out, isErr := callCVETool(t, ts.cveExploit(), map[string]any{
		"cve_id": "CVE-2098-0103", "asset_id": assetID, "target": "https://cve-test.example/",
		"args": []string{"--check", "a b", "x'; touch /tmp/pwned"},
	})
	if isErr || !strings.Contains(out, `"execution_id":7`) || !strings.Contains(out, `"status":"completed"`) ||
		!strings.Contains(out, "head-ok") || !strings.Contains(out, "tail-ok") {
		t.Fatalf("exploit result: isErr=%v %s", isErr, out)
	}
	if fake.submitTool != "cve_exploit" {
		t.Fatalf("ledger tool label: %s", fake.submitTool)
	}
	if !strings.Contains(fake.submitCommand, "BLADE_TARGET='https://cve-test.example/'") ||
		!strings.Contains(fake.submitCommand, "'a b'") ||
		!strings.Contains(fake.submitCommand, `'x'\''; touch /tmp/pwned'`) {
		t.Fatalf("command must quote target/args: %s", fake.submitCommand)
	}
	if !strings.Contains(string(fake.stagedContent), "SENTINEL-") || fake.stagedPath == "" {
		t.Fatalf("script must be staged: %+v", fake.stagedContent)
	}
	findings, _ := ts.ts.ListByKind(db.KindFinding, 10)
	if len(findings) != 0 {
		t.Fatalf("cve_exploit must not auto-create findings: %+v", findings)
	}
	got, _ := as.GetCVEByCVEID("CVE-2098-0103")
	if got.Executions != 1 {
		t.Fatalf("execution counter: %+v", got)
	}
}

func TestCVEContextAndPromptDiscipline(t *testing.T) {
	ts, as, _, _, _ := cveToolSet(t)
	createTestCVE(t, as, "CVE-2098-0104")
	lines := ts.cveLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "CVE-2098-0104") {
		t.Fatalf("cveLines: %+v", lines)
	}
	overview := ts.graphOverviewData()
	if _, ok := overview["matching_cves"]; !ok {
		t.Fatalf("graph overview missing matching_cves: %+v", overview)
	}
	for name, body := range map[string]string{
		"worker":  workerSystem("", "", ""),
		"planner": plannerSystem("goal", "/data", "/data", ""),
		"main":    mainAgentSystem("goal", "/data", "/data"),
	} {
		if !strings.Contains(body, "CVE") || !strings.Contains(body, "不是证据") {
			t.Fatalf("%s prompt missing CVE discipline: %s", name, body)
		}
	}
}
