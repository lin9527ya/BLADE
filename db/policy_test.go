package db

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/policy"
)

// 策略文档的读写往返（P20）：落库什么、读出来就是什么；清空 = 回到"无策略"。
func TestTaskExplorationPolicyRoundTrip(t *testing.T) {
	d, taskID, _ := coverageMatrixFixture(t)
	doc := json.RawMessage(`{"doc_version":1,"global_version":2,"override":{"techniques":["dos"]},"effective":{"version":2,"source":"task","techniques":["dos"],"forbidden_writes":["db_write"],"max_intents_per_round":4}}`)
	if err := d.UpdateTaskExplorationPolicy(taskID, doc); err != nil {
		t.Fatal(err)
	}
	task, err := d.GetTask(taskID)
	if err != nil || task == nil {
		t.Fatalf("get task: %v", err)
	}
	if len(task.ExplorationPolicy) == 0 {
		t.Fatal("策略没落库")
	}
	var got map[string]any
	if err := json.Unmarshal(task.ExplorationPolicy, &got); err != nil {
		t.Fatalf("落库的不是合法 JSON: %v", err)
	}
	if got["global_version"].(float64) != 2 {
		t.Fatalf("doc 字段丢了: %+v", got)
	}
	if err := d.UpdateTaskExplorationPolicy(taskID, nil); err != nil {
		t.Fatal(err)
	}
	// 清空后列上是 NULL；读取侧 COALESCE 成 '{}'（避免 NULL → json.RawMessage 扫描报错），
	// 所以"无策略"由 ParseDoc 归一判定：空文档 == 没有策略。
	task, err = d.GetTask(taskID)
	if err != nil || task == nil {
		t.Fatalf("get task(清空后): %v", err)
	}
	if doc := policy.ParseDoc(task.ExplorationPolicy); doc != nil {
		t.Fatalf("清空后不该还有策略: %s（解析出 %+v）", task.ExplorationPolicy, doc)
	}
}

// 规则按来源整批替换：必须是"删旧+插新"一个事务。
// 插到一半失败（这里用违反 match_target CHECK 的规则）时，旧规则必须原样还在——
// 否则边界会静默消失（fail-open），与 P12 起的失效安全纪律直接冲突。
func TestReplaceTaskRulesBySourceIsAtomic(t *testing.T) {
	d, taskID, _ := coverageMatrixFixture(t)
	task := strconv.FormatInt(taskID, 10)
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_rules WHERE task_id=$1`, task) })

	seed := []InterceptRule{
		{Name: "策略·口令爆破", Enabled: true, Priority: 50, MatchTarget: "tool_input", MatchType: "regex",
			Pattern: `(?i)hydra`, Action: "deny", Message: "m1", TimeoutAction: "deny", TaskID: task, Source: RuleSourcePolicy},
	}
	if n, err := d.ReplaceTaskRulesBySource(task, RuleSourcePolicy, seed); err != nil || n != 1 {
		t.Fatalf("首次替换: n=%d err=%v", n, err)
	}
	if n, _ := d.CountTaskRulesBySource(task, RuleSourcePolicy); n != 1 {
		t.Fatalf("策略规则数 = %d, want 1", n)
	}

	// 坏规则（match_target 违反 CHECK）→ 整笔回滚，旧规则仍在。
	bad := []InterceptRule{
		{Name: "策略·新规则", Enabled: true, Priority: 50, MatchTarget: "bogus", MatchType: "regex",
			Pattern: `x`, Action: "deny", Message: "m2", TimeoutAction: "deny", TaskID: task, Source: RuleSourcePolicy},
	}
	if _, err := d.ReplaceTaskRulesBySource(task, RuleSourcePolicy, bad); err == nil {
		t.Fatal("坏规则应让替换失败")
	}
	n, err := d.CountTaskRulesBySource(task, RuleSourcePolicy)
	if err != nil || n != 1 {
		t.Fatalf("失败后旧规则必须原样保留（fail-closed）: n=%d err=%v", n, err)
	}
	rules, err := d.ListAllInterceptRules()
	if err != nil {
		t.Fatal(err)
	}
	found := ""
	for _, r := range rules {
		if r.TaskID == task {
			found = r.Pattern
		}
	}
	if found != `(?i)hydra` {
		t.Fatalf("留下的不是旧规则: %q", found)
	}

	// 正常替换成 0 条 = 显式清空（策略不再禁任何手段）。
	if n, err := d.ReplaceTaskRulesBySource(task, RuleSourcePolicy, nil); err != nil || n != 0 {
		t.Fatalf("清空: n=%d err=%v", n, err)
	}
	if n, _ := d.CountTaskRulesBySource(task, RuleSourcePolicy); n != 0 {
		t.Fatalf("清空后还剩 %d 条", n)
	}
	// 别的来源（手工/内置）不受影响。
	if rules, _ := d.ListInterceptRules(); len(rules) == 0 {
		t.Fatal("全局/内置规则被误删")
	}
}

// 被拒记录与意图计数（P20 的 planner 反馈与额度口径）。
func TestPolicyRejectsAndIntentCount(t *testing.T) {
	d, _, expID := coverageMatrixFixture(t)
	st := d.Exploration(expID)
	before, err := st.CountIntents()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddIntent(map[string]any{"summary": "计数用意图"}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}
	if after, _ := st.CountIntents(); after != before+1 {
		t.Fatalf("意图计数没涨: %d -> %d", before, after)
	}
	if _, err := st.AppendActivity(Activity{Kind: "policy_reject", Worker: "system",
		Summary:  "探索策略拒绝：口令爆破/认证猜测",
		Metadata: json.RawMessage(`{"policy_item":"bruteforce","intent_summary":"对登录口爆破","matched":"爆破"}`)}); err != nil {
		t.Fatal(err)
	}
	rejects, err := st.RecentPolicyRejects(5)
	if err != nil || len(rejects) != 1 {
		t.Fatalf("被拒记录读取失败: %v %d", err, len(rejects))
	}
	if rejects[0].Kind != "policy_reject" || len(rejects[0].Metadata) == 0 {
		t.Fatalf("记录内容不对: %+v", rejects[0])
	}
}
