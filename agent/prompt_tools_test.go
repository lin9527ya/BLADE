package agent

import "testing"

// 提示词里点名让模型调用的工具，必须在工具面上真的存在。
//
// 反例就在 asset_neighbors：提示词与 node_detail 的报错教了模型好几轮去调它，工具却
// 从来没实现——模型每调一次浪费一轮，还会开始怀疑提示词里的其它指引。这份清单是
// 提示词里**明确出现**的工具名，改动提示词时顺手补；漏注册会在这里当场红。
func TestPlannerPromptToolReferencesExist(t *testing.T) {
	have := map[string]bool{}
	for _, tl := range NewToolSet(nil, "").PlannerTools() {
		have[tl.Name()] = true
	}
	for _, name := range []string{
		"list_facts", "list_findings", "node_detail", "list_assets", "asset_neighbors",
		"get_worker_output", "get_worker_trace", "lookup_playbooks", "record_playbook_feedback",
		"add_intent", "unblock_intent", "prove_goal", "goal_met", "coverage_matrix",
		"run_execution", "wait_execution", "get_execution", "grep_execution", "cancel_execution",
		"kill_work", "steer_work", "accept_artifact_risk", "tunnel_open", "loot_upload", "loot_download",
	} {
		if !have[name] {
			t.Fatalf("提示词让模型调 %s，但它不在 PlannerTools 里——模型会拿到「未知工具」并浪费一轮", name)
		}
	}
}
