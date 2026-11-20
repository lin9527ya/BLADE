package db

import "testing"

// mustIntent / mustNode / mustLink are terse builders for the delete-cascade tests.
func mustIntent(t *testing.T, es *ExplorationStore, summary string) int64 {
	t.Helper()
	id, err := es.AddIntent(map[string]any{"summary": summary}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustNode(t *testing.T, es *ExplorationStore, kind, summary string) int64 {
	t.Helper()
	id, err := es.AddNode(kind, map[string]any{"summary": summary}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustLink(t *testing.T, es *ExplorationStore, from int64, rel string, to int64) {
	t.Helper()
	if err := es.Link(from, rel, to); err != nil {
		t.Fatal(err)
	}
}

func gone(t *testing.T, es *ExplorationStore, id int64) bool {
	t.Helper()
	n, err := es.GetNode(id)
	if err != nil {
		t.Fatal(err)
	}
	return n == nil
}

// TestSoftDeleteIntent 假删除置 deleted + delete_reason,保留节点。
func TestSoftDeleteIntent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("soft delete", "假删除")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	intent := mustIntent(t, es, "待删意图")
	if err := es.SetIntentState(intent, "paused"); err != nil {
		t.Fatal(err)
	}
	summary, _, err := es.SoftDeleteIntent(intent, "方向判断错误")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "待删意图" {
		t.Fatalf("summary=%q, want 待删意图", summary)
	}
	n, err := es.GetNode(intent)
	if err != nil || n == nil {
		t.Fatalf("intent removed by soft delete: n=%+v err=%v", n, err)
	}
	if n.State != StateIntentDeleted || n.DeleteReason != "方向判断错误" {
		t.Fatalf("state=%q delete_reason=%q, want deleted/方向判断错误", n.State, n.DeleteReason)
	}
	// 待领(open)意图也允许假删除。
	openIntent := mustIntent(t, es, "待领意图")
	if _, _, err := es.SoftDeleteIntent(openIntent, "方向不需要了"); err != nil {
		t.Fatalf("soft delete open intent: %v", err)
	}
	if n, err := es.GetNode(openIntent); err != nil || n == nil || n.State != StateIntentDeleted {
		t.Fatalf("open intent not soft-deleted: n=%+v err=%v", n, err)
	}

	// 已删除(deleted)等其它状态不能再假删除。
	if _, _, err := es.SoftDeleteIntent(intent, "再删"); err == nil {
		t.Fatal("soft-deleting an already-deleted intent unexpectedly succeeded")
	}
}

// TestHardDeleteCascadesExclusiveDescendants 真删除沿链路级联删除独占子孙到叶子。
func TestHardDeleteCascadesExclusiveDescendants(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("hard cascade", "级联删除")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	// intent1 --yields--> fact1 --derived_from--> intent2 --yields--> fact2(叶子)
	intent1 := mustIntent(t, es, "根意图")
	fact1 := mustNode(t, es, KindFact, "事实1")
	mustLink(t, es, intent1, RelYields, fact1)
	intent2 := mustIntent(t, es, "衍生意图")
	mustLink(t, es, fact1, RelDerivedFrom, intent2)
	fact2 := mustNode(t, es, KindFact, "事实2")
	mustLink(t, es, intent2, RelYields, fact2)

	cleanup, err := es.CancelIntent(intent1)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Intents != 2 || cleanup.Facts != 2 {
		t.Fatalf("cleanup=%+v, want 2 intents / 2 facts", cleanup)
	}
	for _, id := range []int64{intent1, fact1, intent2, fact2} {
		if !gone(t, es, id) {
			t.Fatalf("node %d survived cascade", id)
		}
	}
}

// TestHardDeletePreservesSharedAndGoal 真删除保留共享子孙(还有其它父)与目标。
func TestHardDeletePreservesSharedAndGoal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("hard preserve", "保留共享/目标")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	goal, err := es.AddGoal(map[string]any{"text": "拿下后台"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	// intent1 独占 finding(proves goal),intent1 与 intentX 共享 fact1(fact1 衍生出 intent2)。
	intent1 := mustIntent(t, es, "待删意图")
	intentX := mustIntent(t, es, "旁路意图")
	finding := mustNode(t, es, KindFinding, "漏洞")
	mustLink(t, es, intent1, RelYields, finding)
	mustLink(t, es, finding, RelProves, goal)
	shared := mustNode(t, es, KindFact, "共享事实")
	mustLink(t, es, intent1, RelYields, shared)
	mustLink(t, es, intentX, RelYields, shared)
	intent2 := mustIntent(t, es, "由共享事实衍生")
	mustLink(t, es, shared, RelDerivedFrom, intent2)

	cleanup, err := es.CancelIntent(intent1)
	if err != nil {
		t.Fatal(err)
	}
	// 只删 intent1 与其独占的 finding;shared(有 intentX 父)及其下游 intent2、goal 全保留。
	if cleanup.Intents != 1 || cleanup.Findings != 1 || cleanup.Facts != 0 {
		t.Fatalf("cleanup=%+v, want 1 intent / 1 finding / 0 fact", cleanup)
	}
	if !gone(t, es, intent1) || !gone(t, es, finding) {
		t.Fatal("intent1/finding should be removed")
	}
	for _, id := range []int64{goal, intentX, shared, intent2} {
		if gone(t, es, id) {
			t.Fatalf("node %d was wrongly cascaded", id)
		}
	}
}

// 战果与证据永不随意图删除：删掉一条意图不该把已经拿到的据点/凭据、
// 留在目标上的痕迹台账一起抹掉——它们是交付物与清理依据。
func TestHardDeletePreservesLootAndEvidence(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("hard preserve loot", "战果保护")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	intent := mustIntent(t, es, "打下据点后清理战场")
	mk := func(kind, state, summary string) int64 {
		id, err := es.AddNode(kind, map[string]any{"summary": summary}, 1, state, "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	foothold := mk(KindFoothold, "active", "web01 反弹 shell")
	credential := mk(KindCredential, "active", "wp_user 口令")
	detection := mk(KindDetection, "unknown", "目标侧命中告警")
	artifact := mk(KindArtifact, "active", "上传的 webshell")
	footprint := mk(KindFootprint, "open", "auth.log 失败登录")
	for _, id := range []int64{foothold, credential, detection, artifact} {
		mustLink(t, es, intent, RelYields, id)
	}
	mustLink(t, es, intent, RelLeaves, footprint)

	cleanup, err := es.CancelIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Intents != 1 || cleanup.Footholds != 0 || cleanup.Credentials != 0 ||
		cleanup.Detections != 0 || cleanup.Artifacts != 0 || cleanup.Footprints != 0 {
		t.Fatalf("cleanup=%+v, want only the intent removed", cleanup)
	}
	if !gone(t, es, intent) {
		t.Fatal("intent should be removed")
	}
	for _, id := range []int64{foothold, credential, detection, artifact, footprint} {
		if gone(t, es, id) {
			t.Fatalf("loot/evidence node %d must survive intent deletion", id)
		}
	}
}

func TestGoalCompletePrunesOpenNonCleanupIntents(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("goal done prune", "测试达标清理待领意图", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)

	if _, err := es.AddNode(KindGoal, map[string]any{"text": "target goal"}, 0, "met", "test", nil); err != nil {
		t.Fatal(err)
	}
	openA := mustIntent(t, es, "未执行方向A")
	openB := mustIntent(t, es, "未执行方向B")
	if err := es.Link(openB, RelDependsOn, openA); err != nil {
		t.Fatal(err)
	}
	cleanupID, err := es.AddIntent(map[string]any{"summary": "清理痕迹", "cleanup": true}, 10, nil, "user")
	if err != nil {
		t.Fatal(err)
	}
	doneID := mustIntent(t, es, "已完成方向")
	if err := es.SetIntentState(doneID, "done"); err != nil {
		t.Fatal(err)
	}

	// Guard failure (e.g., paused) must NOT prune open intents.
	if err := d.SetPaused(tk.ID, true); err != nil {
		t.Fatal(err)
	}
	if won, pruned, err := d.SetGoalsCompleteGuardedWithPruned(tk.ID); err != nil || won || len(pruned) != 0 {
		t.Fatalf("paused guard should fail without pruning: won=%v pruned=%v err=%v", won, pruned, err)
	}
	if n, _ := es.GetNode(openA); n == nil || n.State != "open" {
		t.Fatalf("openA unexpectedly modified on refused completion: %+v", n)
	}
	if err := d.SetPaused(tk.ID, false); err != nil {
		t.Fatal(err)
	}

	// Guard success atomically marks task done and soft-deletes open non-cleanup intents.
	won, pruned, err := d.SetGoalsCompleteGuardedWithPruned(tk.ID)
	if err != nil || !won {
		t.Fatalf("SetGoalsCompleteGuardedWithPruned failed: won=%v err=%v", won, err)
	}
	if len(pruned) != 2 || pruned[0] != openA || pruned[1] != openB {
		t.Fatalf("pruned=%v, want [%d %d]", pruned, openA, openB)
	}
	for _, id := range []int64{openA, openB} {
		n, err := es.GetNode(id)
		if err != nil || n == nil {
			t.Fatalf("get node %d: %+v %v", id, n, err)
		}
		if n.State != StateIntentDeleted || n.DeleteReason != GoalDoneIntentDeleteReason {
			t.Fatalf("intent %d state=%q reason=%q, want deleted/%q", id, n.State, n.DeleteReason, GoalDoneIntentDeleteReason)
		}
	}
	var edgeCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_edges WHERE exploration_id=$1 AND rel=$2 AND (dst_id=$3 OR dst_id=$4)`,
		tk.ExplorationID, RelDependsOn, openA, openB).Scan(&edgeCount); err != nil || edgeCount != 0 {
		t.Fatalf("depends_on edges should be cleaned up: count=%d err=%v", edgeCount, err)
	}
	if n, _ := es.GetNode(cleanupID); n == nil || n.State != "open" {
		t.Fatalf("cleanup intent must remain open: %+v", n)
	}
	if n, _ := es.GetNode(doneID); n == nil || n.State != "done" {
		t.Fatalf("finished intent state changed: %+v", n)
	}

	// Timeout / policy_blocked terminal statuses keep leftover open intents intact.
	tkTimeout, err := d.CreateTask("timeout keep open", "测试超时保留待领意图", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tkTimeout.ID)
	esTimeout := d.Exploration(tkTimeout.ExplorationID)
	kept := mustIntent(t, esTimeout, "超时时未领意图")
	won, pruned, err = d.SetTerminalStatusGuardedWithPruned(tkTimeout.ID, "timeout")
	if err != nil || !won || len(pruned) != 0 {
		t.Fatalf("timeout terminal should not prune: won=%v pruned=%v err=%v", won, pruned, err)
	}
	if n, _ := esTimeout.GetNode(kept); n == nil || n.State != "open" {
		t.Fatalf("open intent should remain open after timeout: %+v", n)
	}
}
