package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

func extractResultText(res actool.Result) string {
	var b strings.Builder
	for _, c := range res.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestMobileDecompileAuditAndUIExploreScreenshotTool(t *testing.T) {
	stub := &stubMobileBridge{}
	origMob, origInfo := Mobile, TaskMobileInfo
	Mobile = stub
	TaskMobileInfo = func(taskID int64) (string, bool, bool) {
		return "app=com.demo.pay sha256=deadbeef1234", true, true
	}
	defer func() {
		Mobile = origMob
		TaskMobileInfo = origInfo
	}()

	ts := &ToolSet{}
	ctx := WithRunInfo(context.Background(), RunInfo{TaskID: 99})

	// 1. mobile_decompile_audit delegates to MobileBridge.Audit("decompile_audit")
	res, err := ts.mobileDecompileAudit().Call(ctx, json.RawMessage(`{"mode":"fast","skip_apktool":true}`), nil)
	if err != nil {
		t.Fatalf("mobile_decompile_audit err: %v", err)
	}
	if res.IsError || !strings.Contains(extractResultText(res), "audit:decompile_audit") {
		t.Fatalf("unexpected mobile_decompile_audit output: %+v", res)
	}

	// 2. mobile_ui_explore supports screenshot action via UIExplore bridge
	res, err = ts.mobileUIExplore().Call(ctx, json.RawMessage(`{"action":"screenshot","save_name":"login.png"}`), nil)
	if err != nil {
		t.Fatalf("mobile_ui_explore screenshot err: %v", err)
	}
	if res.IsError || !strings.Contains(extractResultText(res), "ui_ok") {
		t.Fatalf("unexpected mobile_ui_explore screenshot output: %+v", res)
	}

	// 3. Planner & Worker context blocks guide decompile_audit & evidence_check in both no-device & device modes
	noDevPlanner := mobilePlannerContextBlock("pkg=com.demo.pay", false)
	if !strings.Contains(noDevPlanner, "mobile_decompile_audit") || !strings.Contains(noDevPlanner, "mobile_evidence_check") {
		t.Fatalf("no-device planner block missing mobile audit guidance: %s", noDevPlanner)
	}
	devWorker := mobileWorkerContextBlock("pkg=com.demo.pay", true)
	if !strings.Contains(devWorker, "screenshot") || !strings.Contains(devWorker, "mobile_evidence_check") {
		t.Fatalf("device worker block missing screenshot/evidence guidance: %s", devWorker)
	}
}

func TestMobileEvidenceCheckRules(t *testing.T) {
	origMob, origInfo := Mobile, TaskMobileInfo
	TaskMobileInfo = func(taskID int64) (string, bool, bool) {
		return "package=com.demo.bank sha256=abcd1234abcd1234", true, false
	}
	defer func() {
		Mobile = origMob
		TaskMobileInfo = origInfo
	}()

	tmp := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp, "mobile_audit"), 0o755)
	_ = os.WriteFile(filepath.Join(tmp, "mobile_audit", "manifest.json"),
		[]byte(`{"package_name":"com.demo.bank","sha256":"abcd1234abcd1234"}`), 0o600)

	ts := &ToolSet{taskRoot: tmp}
	ctx := WithRunInfo(context.Background(), RunInfo{TaskID: 12})
	tool := ts.mobileEvidenceCheck()

	// Case 1: Complete masked API+APP evidence passes src_ready=true score=100
	res, err := tool.Call(ctx, json.RawMessage(`{
		"title":"订单越权查询 (IDOR)",
		"vuln_type":"api_idor",
		"severity":"high",
		"source_ref":"jadx: com.demo.bank.net.OrderApi#getOrderDetail",
		"evidence_text":"通过 UserB Token(eyJ***masked) 查询 UserA 订单返回敏感姓名",
		"http_proof":"GET /api/v1/order?id=10001 -> HTTP/1.1 200 OK"
	}`), nil)
	if err != nil {
		t.Fatalf("mobile_evidence_check valid err: %v", err)
	}
	var passed struct {
		SrcReady bool     `json:"src_ready"`
		Score    int      `json:"score"`
		Missing  []string `json:"missing"`
	}
	if err := json.Unmarshal([]byte(extractResultText(res)), &passed); err != nil {
		t.Fatalf("decode result: %v (%s)", err, extractResultText(res))
	}
	if !passed.SrcReady || passed.Score != 100 || len(passed.Missing) != 0 {
		t.Fatalf("expected src_ready=true score=100, got %+v", passed)
	}

	// Case 2: Client-only marked high AND unmasked Bearer/AKIA secret flagged
	res, err = tool.Call(ctx, json.RawMessage(`{
		"title":"allowBackup 开启且含未脱敏 AK",
		"vuln_type":"config_hardening",
		"severity":"high",
		"client_only":true,
		"source_ref":"AndroidManifest.xml allowBackup=true",
		"evidence_text":"AKIA1234567890ABCDEF and Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VyIjoiYSJ9.sign"
	}`), nil)
	if err != nil {
		t.Fatalf("mobile_evidence_check bad err: %v", err)
	}
	var failed struct {
		SrcReady bool     `json:"src_ready"`
		Score    int      `json:"score"`
		Missing  []string `json:"missing"`
	}
	if err := json.Unmarshal([]byte(extractResultText(res)), &failed); err != nil {
		t.Fatalf("decode failed result: %v", err)
	}
	if failed.SrcReady {
		t.Fatalf("expected src_ready=false for client-only high & unmasked secret, got %+v", failed)
	}
	joined := strings.Join(failed.Missing, ",")
	if !strings.Contains(joined, "client_only_severity_mismatch") || !strings.Contains(joined, "unmasked_secret_in_evidence") {
		t.Fatalf("expected client_only_severity_mismatch & unmasked_secret_in_evidence, got %v", failed.Missing)
	}
}
