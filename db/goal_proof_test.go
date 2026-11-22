package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProveGoalCommitsEdgeReasonAndStateAtomically(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "prove goal")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	goal, err := es.AddGoal(map[string]any{"text": "拿到管理员会话"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	openGoal, err := es.AddGoal(map[string]any{"text": "另一个目标"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := es.AddNode(KindFact, map[string]any{"summary": "管理员会话可直接登录后台"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	out, err := es.ProveGoal(goal, fact, "  管理员会话可直接登录后台  ")
	if err != nil {
		t.Fatal(err)
	}
	if out.AlreadyMet || out.TotalGoals != 2 || out.MetGoals != 1 || out.OpenGoals != 1 || out.AbandonedGoals != 0 {
		t.Fatalf("outcome=%+v", out)
	}
	node, err := es.GetNode(goal)
	if err != nil || node == nil || node.State != "met" {
		t.Fatalf("goal=%+v err=%v", node, err)
	}
	var proofReason string
	if err := d.QueryRow(`SELECT COALESCE(payload->>'proof_reason','') FROM exploration_nodes WHERE id=$1`, goal).Scan(&proofReason); err != nil {
		t.Fatal(err)
	}
	if proofReason != "管理员会话可直接登录后台" {
		t.Fatalf("proof_reason=%q", proofReason)
	}
	var edgeReason string
	if err := d.QueryRow(`SELECT COALESCE(attrs->>'reason','') FROM exploration_edges
WHERE exploration_id=$1 AND src_id=$2 AND rel='proves' AND dst_id=$3`, expID, fact, goal).Scan(&edgeReason); err != nil {
		t.Fatal(err)
	}
	if edgeReason != "管理员会话可直接登录后台" {
		t.Fatalf("edge reason=%q", edgeReason)
	}
	if n, err := es.GetNode(openGoal); err != nil || n == nil || n.State != "open" {
		t.Fatalf("unrelated goal must stay open: node=%+v err=%v", n, err)
	}
}

func TestProveGoalRejectsInvalidProofWithoutPartialWrite(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "prove goal rejects")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)
	goal, err := es.AddGoal(map[string]any{"text": "目标"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	otherGoal, err := es.AddGoal(map[string]any{"text": "证据？"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := es.AddNode(KindFact, map[string]any{"summary": "事实"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	strayExp, err := d.CreateExploration("test", "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, strayExp)
	strayFact, err := d.Exploration(strayExp).AddNode(KindFact, map[string]any{"summary": "别人的事实"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		ev   int64
		why  string
		want string
	}{
		{"blank reason", fact, "   ", "reason"},
		{"goal as evidence", otherGoal, "goal cannot prove goal", "事实/漏洞/据点/凭据"},
		{"unrelated exploration evidence", strayFact, "not related", "本任务或直接关联任务"},
	}
	for _, tc := range cases {
		if _, err := es.ProveGoal(goal, tc.ev, tc.why); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err=%v want contains %q", tc.name, err, tc.want)
		}
	}
	var edges int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_edges WHERE exploration_id=$1 AND rel='proves'`, expID).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if edges != 0 {
		t.Fatalf("rejected proofs must not leave edges: %d", edges)
	}
	node, err := es.GetNode(goal)
	if err != nil || node == nil || node.State != "open" {
		t.Fatalf("rejected proofs must not change goal state: node=%+v err=%v", node, err)
	}
}

func TestProveGoalDoesNotReviveAbandonedGoal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("test", "prove abandoned")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)
	goal, err := es.AddGoal(map[string]any{"text": "已下线目标"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := es.AddNode(KindFact, map[string]any{"summary": "晚到的证据"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := es.AbandonGoal(goal, "目标已下线"); err != nil || changed != 1 {
		t.Fatalf("abandon: changed=%d err=%v", changed, err)
	}
	if _, err := es.ProveGoal(goal, fact, "还是想证明"); err == nil || !strings.Contains(err.Error(), "abandoned") {
		t.Fatalf("abandoned goal must not be re-proved: %v", err)
	}
	node, err := es.GetNode(goal)
	if err != nil || node == nil || node.State != "abandoned" {
		t.Fatalf("state=%v err=%v", node, err)
	}
}

func TestProveGoalAcceptsDirectSourceEvidenceAndCountsSettlement(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	src, err := d.CreateTaskWithOptions("source", "source goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(src.ID)
	main, err := d.CreateTaskWithOptions("main", "main goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(main.ID)
	if _, err := d.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, main.ID, src.ID); err != nil {
		t.Fatal(err)
	}
	srcStore := d.Exploration(src.ExplorationID)
	fact, err := srcStore.AddNode(KindFact, map[string]any{"summary": "上游任务拿到凭据"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	es := d.Exploration(main.ExplorationID)
	goal, err := es.AddGoal(map[string]any{"text": "复用上游凭据"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	abandoned, err := es.AddGoal(map[string]any{"text": "不再需要的目标"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := es.AbandonGoal(abandoned, "客户取消"); err != nil || changed != 1 {
		t.Fatalf("abandon: changed=%d err=%v", changed, err)
	}
	out, err := es.ProveGoal(goal, fact, "上游凭据可直接复用")
	if err != nil {
		t.Fatal(err)
	}
	if out.OpenGoals != 0 || out.MetGoals != 1 || out.AbandonedGoals != 1 || out.TotalGoals != 2 {
		t.Fatalf("outcome=%+v", out)
	}
	// 边写在本任务里，源节点仍可读。
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='proves' AND dst_id=$3`,
		main.ExplorationID, fact, goal).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("proves edge in main exploration = %d", n)
	}
}

func TestProveGoalHonorsStaleWriteAuthority(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("authority", "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	es := d.Exploration(task.ExplorationID)
	intent, err := es.AddIntent(map[string]any{"summary": "run"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := es.ClaimIntent(intent, "work#1", 10*time.Minute); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	node, err := es.GetNode(intent)
	if err != nil || node == nil {
		t.Fatalf("intent read: %v", err)
	}
	authority := node.WriteAuthority(task.ID, task.ExplorationID)

	goal1, err := es.AddGoal(map[string]any{"text": "目标一"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	goal2, err := es.AddGoal(map[string]any{"text": "目标二"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	fact, err := es.AddNode(KindFact, map[string]any{"summary": "证据"}, 0, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := es.WithAuthority(authority).ProveGoal(goal1, fact, "有效权威"); err != nil {
		t.Fatalf("valid authority: %v", err)
	}
	stale := authority
	stale.Generation++
	if _, err := es.WithAuthority(stale).ProveGoal(goal2, fact, "过期权威"); !errors.Is(err, ErrStaleExecution) {
		t.Fatalf("want ErrStaleExecution, got %v", err)
	}
	if n, err := es.GetNode(goal2); err != nil || n == nil || n.State != "open" {
		t.Fatalf("stale proof must not change state: node=%+v err=%v", n, err)
	}
}
