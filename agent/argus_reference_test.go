package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestArgusInspiredPromptContracts(t *testing.T) {
	planner := plannerSystem("目标", "/data", "/work")
	for _, want := range []string{
		"广度查漏与深度裂变",
		"盲态建预言机（Oracle）",
		"防误删红线（四维等价判定）",
	} {
		if !strings.Contains(planner, want) {
			t.Fatalf("planner system missing %q", want)
		}
	}

	worker := workerSystem("", "", "/work")
	for _, want := range []string{
		"基线指纹",
		"捕获六维弱信号",
		"签名聚合统计",
		"summary 仅写客观观测与基线差分",
	} {
		if !strings.Contains(worker, want) {
			t.Fatalf("worker system missing %q", want)
		}
	}

	for _, want := range []string{
		"前置依赖",
		"Burp Repeater",
		"绝不臆造",
	} {
		if !strings.Contains(ReporterDefaultPrompt, want) {
			t.Fatalf("reporter prompt missing %q", want)
		}
	}

	seeds := map[string]string{}
	for _, seed := range BuiltinToolSeeds() {
		seeds[seed.Key] = seed.Desc
	}
	if !strings.Contains(seeds["drop_intent"], "四维等价") {
		t.Fatalf("drop_intent description missing 4D equivalence guardrail: %s", seeds["drop_intent"])
	}
	if !strings.Contains(seeds["record_fact"], "基线指纹") {
		t.Fatalf("record_fact description missing baseline fingerprint guidance: %s", seeds["record_fact"])
	}
}

func TestFactSummaryAdviceWarningUnit(t *testing.T) {
	if w := factSummaryAdviceWarning("测 POST /articles 128 次，124 次与基线一致 [200, 3420B, 42ms]；4 次返回 500"); w != "" {
		t.Fatalf("clean observational summary should not trigger warning, got %q", w)
	}
	for _, bad := range []string{
		"建议进一步测试 /admin 的越权访问",
		"下一步换 POST 重放测试",
		"我的思路是先探测子域名",
		"或许后端未检验 token",
	} {
		if w := factSummaryAdviceWarning(bad); w == "" {
			t.Fatalf("expected warning for advice/speculative summary %q", bad)
		}
	}
}

func TestRecordFactSummaryWarningDoesNotBlockOrPolluteRawDetail(t *testing.T) {
	tools, _, intent := newToolWorker(t)

	// 1. detail 与 evidence 含目标原始返回文本（"建议" / "下一步"），不得误触发警告且正常写入。
	cleanOut := callTool(t, tools.WorkerTools(), "record_fact", fmt.Sprintf(`{
		"intent_id": %d,
		"summary": "POST /wizard 接口返回提示页面，基线 200/412B/18ms",
		"detail": "响应 HTML 原文：<button>下一步</button><p>系统建议您先绑定手机号</p>",
		"evidence": "POST /wizard -> 200 {\"msg\":\"请点击下一步并按系统建议操作\"}",
		"confidence": "observed"
	}`, intent))
	if !strings.HasPrefix(cleanOut, "fact recorded: ") {
		t.Fatalf("unexpected clean record_fact output: %s", cleanOut)
	}
	if strings.Contains(cleanOut, "warning:") {
		t.Fatalf("raw target detail/evidence triggered false-positive warning: %s", cleanOut)
	}

	// 2. summary 含建议词时（方案 A）：不阻断入库，附加 warning 引导改用 propose_intents / leads。
	warnOut := callTool(t, tools.WorkerTools(), "record_fact", fmt.Sprintf(`{
		"intent_id": %d,
		"summary": "发现 /upload 接口，建议下一步测试文件名穿越",
		"detail": "返回 200 OK",
		"evidence": "POST /upload -> 200",
		"confidence": "observed"
	}`, intent))
	if !strings.HasPrefix(warnOut, "fact recorded: ") || !strings.Contains(warnOut, "warning:") || !strings.Contains(warnOut, "propose_intents") {
		t.Fatalf("expected non-blocking summary warning, got: %s", warnOut)
	}

	// 3. 批量模式：仅污染 summary 的那条输出 warnings[idx]，原始文本那条无 warning。
	batchOut := callTool(t, tools.WorkerTools(), "record_fact", fmt.Sprintf(`{"facts":[
		{"intent_id":%d,"summary":"接口 /step1 返回 JSON 基线 200","detail":"{\"btn\":\"下一步\",\"tip\":\"建议填写\"}","evidence":"下一步 / 建议","confidence":"observed"},
		{"intent_id":%d,"summary":"下一步需要继续枚举 /api/v2","detail":"仅记录","evidence":"GET /api/v2 -> 403","confidence":"observed"}
	]}`, intent, intent))
	var resp struct {
		IDs      []int64           `json:"ids"`
		Warnings map[string]string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(batchOut), &resp); err != nil {
		t.Fatalf("unmarshal batch record_fact: %v (%s)", err, batchOut)
	}
	if len(resp.IDs) != 2 || resp.IDs[0] <= 0 || resp.IDs[1] <= 0 {
		t.Fatalf("batch record_fact blocked write on warning: %+v", resp)
	}
	if _, ok := resp.Warnings["0"]; ok {
		t.Fatalf("batch item 0 had clean summary but got warning: %+v", resp.Warnings)
	}
	if w := resp.Warnings["1"]; w == "" || !strings.Contains(w, "下一步") {
		t.Fatalf("batch item 1 missing warning for summary advice: %+v", resp.Warnings)
	}
}
