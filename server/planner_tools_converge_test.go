package server

import "testing"

// 老库的 planner 默认绑定里混着执行/写漏洞/扩面工具；迁移必须把它们剥掉，
// 同时不能动 worker 对同一工具的绑定（worker 仍然要执行）。
func TestSeedPlannerToolsConverge(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	const flag = "planner_tools_converge_v1"
	if err := pg.SetSetting(flag, "false"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.SetSetting(flag, "true") })
	execKeys := []string{"run_execution", "loot_exec", "cve_exploit"}
	exploreKeys := []string{"report_finding", "add_task_scope"}
	keys := append(append([]string{}, execKeys...), exploreKeys...)
	if err := pg.AddAgentToToolBinding("planner", keys); err != nil {
		t.Fatal(err)
	}
	s.seedPlannerToolsConverge()
	contains := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	for _, key := range execKeys {
		tool, err := pg.GetTool(key)
		if err != nil || tool == nil {
			t.Fatalf("get tool %s: %+v %v", key, tool, err)
		}
		if contains(tool.Agents, "planner") {
			t.Fatalf("%s still bound to planner after converge: %v", key, tool.Agents)
		}
	}
	for _, key := range exploreKeys {
		tool, err := pg.GetTool(key)
		if err != nil || tool == nil || !contains(tool.Agents, "planner") {
			t.Fatalf("ARTEX exploration tool %s must remain bound to planner: %+v %v", key, tool, err)
		}
	}
	tool, _ := pg.GetTool("run_execution")
	if tool == nil || !contains(tool.Agents, "worker") {
		t.Fatalf("converge must not strip worker's execution tools: %+v", tool)
	}
}

// planner 只保留规划/查询工具：写回与动作类工具从 planner 解绑，但实现与
// 其他 agent 的绑定不动；只读知识查询（打法/CVE/AD/opsec）继续保留。
func TestSeedPlannerToolsSlim(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	const flag = "planner_tools_slim_v1"
	if err := pg.SetSetting(flag, "false"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.SetSetting(flag, "true") })
	keys := []string{
		"record_playbook", "promote_knowledge", "merge_findings",
		"rotate_egress", "list_worker_traces",
	}
	if err := pg.AddAgentToToolBinding("planner", keys); err != nil {
		t.Fatal(err)
	}
	s.seedPlannerToolsSlim()
	contains := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	for _, key := range keys {
		tool, err := pg.GetTool(key)
		if err != nil || tool == nil {
			t.Fatalf("get tool %s: %+v %v", key, tool, err)
		}
		if contains(tool.Agents, "planner") {
			t.Fatalf("%s still bound to planner after slim: %v", key, tool.Agents)
		}
	}
	for _, key := range []string{"lookup_cves", "lookup_playbooks", "ad_paths", "opsec_notes", "record_playbook_feedback"} {
		tool, err := pg.GetTool(key)
		if err != nil || tool == nil || !contains(tool.Agents, "planner") {
			t.Fatalf("read-only planning tool %s must stay on planner: %+v %v", key, tool, err)
		}
	}
	tool, _ := pg.GetTool("rotate_egress")
	if tool == nil || (!contains(tool.Agents, "worker") && !contains(tool.Agents, "mainagent")) {
		t.Fatalf("slim must not strip worker/mainagent action tools: %+v", tool)
	}
}
