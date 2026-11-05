package db

import (
	"context"
	"testing"
)

// TestRepairGoalIntentLineage 验证旧图修复：目标→意图边删除，目标补回起点初始边，
// 无事实父节点的意图补回起点派生边；重复执行保持幂等。
func TestRepairGoalIntentLineage(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("lineage repair", "goal", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	expID := task.ExplorationID
	es := d.Exploration(expID)
	origin, err := es.OriginFactID()
	if err != nil || origin <= 0 {
		t.Fatalf("origin fact missing: %v", err)
	}
	goal, err := es.AddNode(KindGoal, map[string]any{"text": "goal"}, 0, "open", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := es.AddNode(KindIntent, map[string]any{"summary": "intent"}, 5, "open", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Link(goal, RelSpawns, intent); err != nil {
		t.Fatal(err)
	}
	conn, err := d.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := repairGoalIntentLineage(context.Background(), conn); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := repairGoalIntentLineage(context.Background(), conn); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()
	var goalIntent, originGoal, originIntent int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel=$3 AND dst_id=$4`, expID, goal, RelSpawns, intent).Scan(&goalIntent); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel=$3 AND dst_id=$4`, expID, origin, RelSpawns, goal).Scan(&originGoal); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel=$3 AND dst_id=$4`, expID, origin, RelDerivedFrom, intent).Scan(&originIntent); err != nil {
		t.Fatal(err)
	}
	if goalIntent != 0 || originGoal != 1 || originIntent != 1 {
		t.Fatalf("lineage repair = goalIntent %d, originGoal %d, originIntent %d; want 0,1,1", goalIntent, originGoal, originIntent)
	}
}
