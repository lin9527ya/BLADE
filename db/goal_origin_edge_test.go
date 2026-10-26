package db

import "testing"

// 起点→目标的初始边必须被清理，但目标→意图这类真实推进边要保留。
// 迁移写在 schema.sql 里，所以直接重放 schemaSQL 来验证它对旧库的效果。
func TestSchemaDropsInitialOriginGoalEdge(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	task, err := d.CreateTaskWithOptions("goal-edge migration", "验证目标初始不连线", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)

	es := d.Exploration(task.ExplorationID)
	origin, err := es.OriginFactID()
	if err != nil || origin == 0 {
		t.Fatalf("origin: %d %v", origin, err)
	}
	goal, err := es.AddGoal(map[string]any{"text": "拿下后台"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := es.AddIntent(map[string]any{"summary": "验证目标分解轴"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := es.LinkIntentToGoal(goal, intent); err != nil {
		t.Fatal(err)
	}
	// 模拟旧版本：目标创建时直接挂到任务根。
	if err := es.Link(origin, RelSpawns, goal); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='spawns' AND dst_id=$3`,
		task.ExplorationID, origin, goal).Scan(&n); err != nil || n != 1 {
		t.Fatalf("seed edge: n=%d err=%v", n, err)
	}

	if _, err := d.Exec(schemaSQL); err != nil {
		t.Fatalf("reapply schema: %v", err)
	}

	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='spawns' AND dst_id=$3`,
		task.ExplorationID, origin, goal).Scan(&n); err != nil || n != 0 {
		t.Fatalf("origin→goal edge must be gone: n=%d err=%v", n, err)
	}
	// 目标→意图是真实推进关系，不能被迁移误删。
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND src_id=$2 AND rel='spawns' AND dst_id=$3`,
		task.ExplorationID, goal, intent).Scan(&n); err != nil || n != 1 {
		t.Fatalf("goal→intent edge must survive: n=%d err=%v", n, err)
	}
}
