package db

import (
	"strings"
	"testing"
)

func TestFootprintParseAndLabels(t *testing.T) {
	n := &Node{Kind: KindFootprint, State: StateFootprintOpen, Payload: []byte(`{
		"summary":"认证日志留下失败登录",
		"category":"audit","source":"predicted","exposure":"高",
		"where":["目标系统日志"],"signals":["4625"],"occurrences":3
	}`)}
	f := ParseFootprint(n)
	if f.Summary == "" || f.Category != FootprintCatLog || f.Exposure != FootprintExposureHigh || f.Occurrences != 3 {
		t.Fatalf("parse: %+v", f)
	}
	if FootprintStateLabel(StateFootprintOpen) != "未处置" || FootprintStateLabel(StateFootprintNotFound) != "未发现" {
		t.Fatal("状态词必须各自成词")
	}
	if FootprintCategoryLabel(NormalizeFootprintCategory("history")) != "本机痕迹" {
		t.Fatal("history 应归到本机痕迹")
	}
	if got := FootprintExposureLabel(""); got != "" {
		t.Fatalf("未标暴露度应留空，得到 %q", got)
	}
	if !ValidFootprintState(StateFootprintAccepted) || ValidFootprintState("active") {
		t.Fatal("状态校验错误")
	}
}

func TestFootprintStoreTransitions(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "痕迹状态机")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	add := func(summary string) int64 {
		t.Helper()
		id, err := es.AddNode(KindFootprint, map[string]any{"summary": summary, "source": FootprintSourceObserved},
			5, StateFootprintOpen, "work#1", nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	cleaned := add("清理 auth.log 中的一条记录")
	if _, err := es.MarkFootprintCleaned(cleaned, "   ", ""); err == nil || !strings.Contains(err.Error(), "证据必填") {
		t.Fatalf("清理必须要求证据，得到 %v", err)
	}
	if n, err := es.MarkFootprintCleaned(cleaned, "sed -i '/10.0.0.9/d' /var/log/auth.log → wc -l 减少 1 行", ""); err != nil || n != 1 {
		t.Fatalf("清理应成功: n=%d err=%v", n, err)
	}
	if n, _ := es.MarkFootprintCleaned(cleaned, "again", ""); n != 0 {
		t.Fatalf("重复清理应返回 0，得到 %d", n)
	}
	got, _ := es.GetNode(cleaned)
	if got == nil || got.State != StateFootprintCleaned || !strings.Contains(string(got.Payload), "removed_evidence") {
		t.Fatalf("清理后状态/证据不对: %+v", got)
	}

	accepted := add("WAF 告警无法清除")
	if _, err := es.AcceptFootprintRisk(accepted, " "); err == nil {
		t.Fatal("接受风险必须要求理由")
	}
	if n, err := es.AcceptFootprintRisk(accepted, "客户确认接受，清理会破坏证据链"); err != nil || n != 1 {
		t.Fatalf("接受风险应成功: n=%d err=%v", n, err)
	}
	got, _ = es.GetNode(accepted)
	if got == nil || got.State != StateFootprintAccepted || !strings.Contains(string(got.Payload), "accepted_reason") {
		t.Fatalf("接受风险后状态/理由不对: %+v", got)
	}
	if n, err := es.MarkFootprintNotFound(accepted, "grep 了三个时间窗，未发现对应记录", ""); err != nil || n != 1 {
		t.Fatalf("核实不存在应允许从 accepted 收口: n=%d err=%v", n, err)
	}
	got, _ = es.GetNode(accepted)
	if got == nil || got.State != StateFootprintNotFound {
		t.Fatalf("核实不存在后状态 = %+v", got)
	}
}

func TestHasActiveCleanupIntent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "清理意图门")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	if active, err := es.HasActiveCleanupIntent(); err != nil || active {
		t.Fatalf("空任务不应有清理意图: %v %v", active, err)
	}
	intent, err := es.AddIntent(map[string]any{"summary": "清理痕迹", "cleanup": true}, 9, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	if active, err := es.HasActiveCleanupIntent(); err != nil || !active {
		t.Fatalf("open 清理意图应被看到: %v %v", active, err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='done' WHERE id=$1`, intent); err != nil {
		t.Fatal(err)
	}
	if active, err := es.HasActiveCleanupIntent(); err != nil || active {
		t.Fatalf("done 清理意图不应阻断收官: %v %v", active, err)
	}
}

func TestListCommandActivitiesIncludesResult(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "命令历史")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	if _, err := es.AppendActivity(Activity{
		Worker: "work#1", Kind: "tool_use", Tool: "Bash", ToolUseID: "call-1",
		Detail: `{"command":"nmap -sV 10.0.0.1"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := es.AppendActivity(Activity{
		Worker: "work#1", Kind: "tool_result", Tool: "Bash", ToolUseID: "call-1", Detail: "22/tcp open ssh",
	}); err != nil {
		t.Fatal(err)
	}
	acts, err := es.ListCommandActivities(10)
	if err != nil || len(acts) != 1 {
		t.Fatalf("应取到 1 条 tool_use: %d err=%v", len(acts), err)
	}
	if acts[0].Tool != "Bash" || !strings.Contains(acts[0].Detail, "nmap") || acts[0].Result != "22/tcp open ssh" {
		t.Fatalf("命令/结果关联不对: %+v", acts[0])
	}
}

func TestFootprintSourceEdges(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "痕迹来源连线")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	intent, err := es.AddIntent(map[string]any{"summary": "扫端口"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := es.AddNode(KindFact, map[string]any{"summary": "确认暴露 22"}, 5, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Link(intent, RelYields, fact); err != nil {
		t.Fatal(err)
	}
	fp, err := es.AddNode(KindFootprint, map[string]any{"summary": "扫描痕迹"}, 5, StateFootprintOpen, "system", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 意图与事实都能作为痕迹来源；同一对重复连线是幂等的。
	for _, src := range []int64{intent, fact} {
		added, err := es.LinkFootprintSource(src, fp)
		if err != nil || !added {
			t.Fatalf("来源 %d 连线失败: added=%v err=%v", src, added, err)
		}
	}
	if added, err := es.LinkFootprintSource(intent, fp); err != nil || added {
		t.Fatalf("重复连线应幂等: added=%v err=%v", added, err)
	}

	sources, err := es.FootprintSources()
	if err != nil {
		t.Fatal(err)
	}
	got := sources[fp]
	if len(got) != 2 || got[0] != intent || got[1] != fact {
		t.Fatalf("来源列表 = %v，应为 [%d %d]", got, intent, fact)
	}

	// 痕迹不能当来源；非本任务的节点不能连。
	if _, err := es.LinkFootprintSource(fp, fp); err == nil {
		t.Fatal("痕迹节点不应能作为来源（自连）")
	}
	if _, err := es.LinkFootprintSource(999999, fp); err == nil {
		t.Fatal("不存在的来源应报错")
	}
}

func TestIntentIDsForActivityIDs(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "活动归属")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	intent, err := es.AddIntent(map[string]any{"summary": "扫端口"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	withIntent, err := es.AppendActivity(Activity{NodeID: &intent, Worker: "work#1", Kind: "tool_use", Tool: "Bash"})
	if err != nil {
		t.Fatal(err)
	}
	mainOnly, err := es.AppendActivity(Activity{Worker: "mainagent", Kind: "tool_use", Tool: "Bash"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := es.IntentIDsForActivityIDs([]int64{withIntent, mainOnly})
	if err != nil {
		t.Fatal(err)
	}
	if got[withIntent] != intent {
		t.Fatalf("有意图归属的活动应映射到意图：%v", got)
	}
	if _, ok := got[mainOnly]; ok {
		t.Fatalf("主 Agent 本机活动不该有意图归属：%v", got)
	}
}
