package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestRenderSystemOverrideAndFallback(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil })

	// no override → built-in default
	PromptOverride = nil
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "g"}); got != "DEFAULT" {
		t.Fatalf("no override should give default, got %q", got)
	}

	// override → rendered with vars
	PromptOverride = func(k string) (string, bool) {
		if k == "planner" {
			return "目标:{{.Goal}} 范围:{{.Scope}}", true
		}
		return "", false
	}
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "拿下X", Scope: "*.x.com"}); got != "目标:拿下X 范围:*.x.com" {
		t.Fatalf("override render: %q", got)
	}

	// override referencing a non-catalog var → execution error → fallback to default
	PromptOverride = func(k string) (string, bool) { return "{{.NotInCatalog}}", true }
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "x"}); got != "DEFAULT" {
		t.Fatalf("bad var should fall back to default, got %q", got)
	}

	// full plannerSystem path: DB body [A] is honored, then the code-owned tail
	// [C] (中间产物输出规约) is ALWAYS appended — editing the body can't drop it.
	PromptOverride = func(k string) (string, bool) { return "PLANNER {{.Goal}}", true }
	got := plannerSystem("拿下X", "/data", "/data", "")
	if !strings.HasPrefix(got, "PLANNER 拿下X") {
		t.Fatalf("plannerSystem body not honored: %q", got)
	}
	if !strings.Contains(got, "中间产物输出规约") || !strings.Contains(got, "/data") {
		t.Fatalf("plannerSystem missing code-owned artifact tail: %q", got)
	}

	// worker dual-text via {{if .ProxyAddr}} in a user template, plus the code tail:
	// [B] trafficTool present only when RECORDING (caCert set — the MITM is on, so
	// the traffic_* tools exist), [C] artifact spec always present. The trafficTool
	// block is gated on the CA (arg 2), NOT on ProxyAddr — a global egress proxy
	// with capture off routes traffic but records nothing.
	PromptOverride = func(k string) (string, bool) {
		return "{{if .ProxyAddr}}走代理 {{.ProxyAddr}}{{else}}手动{{end}}", true
	}
	recording := workerSystem("127.0.0.1:8080", "/ca.pem", "/data")
	if !strings.HasPrefix(recording, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker proxy branch body: %q", recording)
	}
	if !strings.Contains(recording, "traffic_search") {
		t.Fatalf("worker while recording should inject trafficTool: %q", recording)
	}
	if strings.Contains(recording, "traffic_refs") {
		t.Fatalf("worker bypassed shared optional evidence policy: %q", recording)
	}
	// Per-intent runDir/artifact contract must NOT be in the reused system prompt;
	// it belongs to the per-run user turn so the cached prefix stays stable.
	if strings.Contains(recording, "中间产物输出规约") || strings.Contains(recording, "BLADE_WORKER_INTENT") {
		t.Fatalf("worker system must stay intent-free: %q", recording)
	}
	// Egress proxy set but capture OFF (no CA): the ProxyAddr template branch still
	// renders, but the trafficTool block must NOT — those tools are not registered.
	egressOnly := workerSystem("127.0.0.1:8080", "", "/data")
	if !strings.HasPrefix(egressOnly, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker egress-only branch body: %q", egressOnly)
	}
	if strings.Contains(egressOnly, "traffic_search") {
		t.Fatalf("worker without recording must NOT inject trafficTool: %q", egressOnly)
	}
	noProxy := workerSystem("", "", "/data")
	if !strings.HasPrefix(noProxy, "手动") {
		t.Fatalf("worker no-proxy branch body: %q", noProxy)
	}
	if strings.Contains(noProxy, "traffic_search") {
		t.Fatalf("worker without proxy must NOT inject trafficTool: %q", noProxy)
	}

	// The intent turn carries the per-run anchors: marker, payload/id, assets,
	// runDir/artifact spec and slot — and nothing per-intent leaks into system.
	turn := renderWorkerIntentTurn(&db.Node{ID: 9, Payload: json.RawMessage(`{"task":"probe-api"}`)}, `[{"id":1,"url":"http://t.example"}]`, "/data/tasks/1/i9", 2)
	for _, want := range []string{workerIntentMarker(9), "probe-api", "意图 id: 9", "http://t.example", "/data/tasks/1/i9", "中间产物输出规约", "Worker #2", "仅作背景参考"} {
		if !strings.Contains(turn, want) {
			t.Fatalf("intent turn missing %q: %s", want, turn)
		}
	}
	if strings.Contains(recording, "/data/tasks/1/i9") {
		t.Fatalf("system leaked runDir: %q", recording)
	}
}

// TestPromptAppendAppliesOnBuiltinAndOverride 钉住追加段的两条语义：
//   - 正文为空（走内置默认）时追加段**仍然生效**——benchmark 依赖这一点，
//     它只写场景说明，不复制整份内置提示词；
//   - 追加段落在正文之后。
func TestPromptAppendAppliesOnBuiltinAndOverride(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil; PromptAppend = nil })

	PromptOverride = nil
	PromptAppend = func(k string) (string, bool) {
		if k == "worker" {
			return "SCENE", true
		}
		return "", false
	}
	if got := renderSystem("worker", "BUILTIN", WorkerVars{}); got != "BUILTIN\n\nSCENE" {
		t.Fatalf("追加段应作用在内置正文上，实际 %q", got)
	}

	PromptOverride = func(k string) (string, bool) { return "CUSTOM", true }
	if got := renderSystem("worker", "BUILTIN", WorkerVars{}); got != "CUSTOM\n\nSCENE" {
		t.Fatalf("追加段应作用在自定义正文上，实际 %q", got)
	}

	PromptAppend = nil
	if got := renderSystem("worker", "BUILTIN", WorkerVars{}); got != "CUSTOM" {
		t.Fatalf("没有追加段时不该多出内容，实际 %q", got)
	}
}
