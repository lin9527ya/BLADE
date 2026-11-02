package agent

import (
	"strings"
	"testing"
)

// planner 的工具 schema 每轮都随缓存前缀发送（37 个约 3.3 万字符）。低频工具降为
// deferred 后 schema 不再发送，只在系统提示里留名字——必须保证：
//  1. 被 deferred 的工具**仍然在工具面里**（否则 ExecuteExtraTool 也调不到）；
//  2. 名单只包含实际存在的工具（提示里出现查不到的名字会浪费一轮）；
//  3. 核心工具（add_intent / graph_overview 等）**不在** deferred 里。
func TestPlannerDeferredToolsAreCallableAndMinimal(t *testing.T) {
	base := NewToolSet(nil, "").PlannerTools()
	have := map[string]bool{}
	for _, tl := range base {
		have[tl.Name()] = true
	}
	own := deferPlannerTools(base)
	if len(own) == 0 {
		t.Fatal("planner 应有低频工具被 defer")
	}
	for _, name := range own {
		if !have[name] {
			t.Fatalf("deferred 名单里的 %q 不在工具面里：模型会去查一个查不到的名字", name)
		}
	}
	// 核心工具必须留在常规工具列表里：它们每轮都要用，deferred 会强迫模型多绕一次。
	for _, core := range []string{"add_intent", "graph_overview", "list_facts", "prove_goal", "drop_intent"} {
		for _, name := range own {
			if name == core {
				t.Fatalf("%q 是每轮都要用的核心工具，不应 deferred", core)
			}
		}
	}
}

// deferred 块必须出现在系统提示里，且写清「先查 schema 再调用」，否则模型不知道
// 这些工具存在（schema 已被摘掉，它看不到）。
func TestDeferredSystemListsNamesAndUsage(t *testing.T) {
	sys, boundary := deferredSystem("正文", DeferredInfo{
		Deferred:    []string{"record_playbook", "merge_findings"},
		GlobalNames: []string{"record_playbook", "merge_findings"},
	})
	joined := strings.Join(sys, "\n")
	for _, want := range []string{"record_playbook", "merge_findings", "SearchExtraTools", "ExecuteExtraTool"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("deferred 系统提示缺少 %q：\n%s", want, joined)
		}
	}
	if boundary != len(sys) {
		t.Fatalf("boundary 应覆盖全部 system 段（缓存前缀），got %d len %d", boundary, len(sys))
	}
}
