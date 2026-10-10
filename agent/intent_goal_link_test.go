package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// TestAddIntentGoalAxis drives the 目标分解 axis through the real tool: a
// top-level intent with no goal falls back to the origin fact; once a single open
// goal exists it attaches automatically (goal --spawns--> intent, no origin edge);
// several open goals are never guessed (a note asks for goal_id); an explicit
// goal_id wins; a settled goal is refused.
func TestAddIntentGoalAxis(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("goal axis", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(nil, "")
	ts.ts, ts.taskID = d.Exploration(task.ExplorationID), task.ID
	origin, err := ts.ts.OriginFactID()
	if err != nil || origin <= 0 {
		t.Fatalf("origin fact: %d err=%v", origin, err)
	}
	spawnsFrom := func(intentID int64) []int64 {
		rows, err := d.Query(`SELECT src_id FROM exploration_edges WHERE exploration_id=$1 AND dst_id=$2 AND rel='spawns'`, task.ExplorationID, intentID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	derivedFrom := func(intentID int64) bool {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE exploration_id=$1 AND dst_id=$2 AND rel='derived_from' AND src_id=$3`, task.ExplorationID, intentID, origin).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	call := func(tool string, payload string) string {
		res, err := ts.addIntent().Call(context.Background(), json.RawMessage(payload), nil)
		if err != nil {
			t.Fatalf("%s transport error: %v", tool, err)
		}
		return res.Flatten()
	}
	intentID := func(out string) int64 {
		first := strings.SplitN(out, "\n", 2)[0]
		id, err := strconv.ParseInt(strings.TrimPrefix(first, "intent created: "), 10, 64)
		if err != nil {
			t.Fatalf("bad tool output %q: %v", out, err)
		}
		return id
	}

	// 1) 无目标（goalless）：顶层意图回退 origin，不挂目标。
	out := call("add_intent", `{"summary":"开局侦察"}`)
	lone := intentID(out)
	if len(spawnsFrom(lone)) != 0 || !derivedFrom(lone) {
		t.Fatalf("goalless top-level intent must fall back to origin: spawns=%v origin=%v", spawnsFrom(lone), derivedFrom(lone))
	}

	// 2) 单个 open 目标：自动挂，且不再写 origin 兜底边。
	if res, err := ts.setGoals().Call(context.Background(), json.RawMessage(`{"text":"拿到站点 flag"}`), nil); err != nil {
		t.Fatal(err)
	} else if res.IsError {
		t.Fatalf("set_goals failed: %s", res.Flatten())
	}
	goals, err := ts.ts.ListByKind("goal", 10)
	if err != nil || len(goals) != 1 {
		t.Fatalf("goals: %+v err=%v", goals, err)
	}
	auto := intentID(call("add_intent", `{"summary":"挂单目标"}`))
	if from := spawnsFrom(auto); len(from) != 1 || from[0] != goals[0].ID {
		t.Fatalf("single open goal must auto-attach: %v want %d", from, goals[0].ID)
	}
	if derivedFrom(auto) {
		t.Fatal("goal-attached intent must not also write the origin fallback edge")
	}

	// 3) 目标已 met：显式指名被拒收。
	if err := ts.ts.SetNodeState(goals[0].ID, "met"); err != nil {
		t.Fatal(err)
	}
	if out := call("add_intent", `{"summary":"挂已达成目标","goal_id":`+strconv.FormatInt(goals[0].ID, 10)+`}`); !strings.Contains(out, "open") {
		t.Fatalf("settled goal must be refused: %s", out)
	}

	// 4) 两个 open 目标：省略 goal_id 不猜、回执提示；显式指名挂对的。
	g1, err := ts.ts.AddNode("goal", map[string]any{"text": "目标一"}, 0, "open", "goals", nil)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := ts.ts.AddNode("goal", map[string]any{"text": "目标二"}, 0, "open", "goals", nil)
	if err != nil {
		t.Fatal(err)
	}
	out = call("add_intent", `{"summary":"多目标未指名"}`)
	ambiguous := intentID(out)
	if len(spawnsFrom(ambiguous)) != 0 || !strings.Contains(out, "goal_id") {
		t.Fatalf("ambiguous goals must not be guessed: spawns=%v out=%s", spawnsFrom(ambiguous), out)
	}
	named := intentID(call("add_intent", `{"summary":"指名目标二","goal_id":`+strconv.FormatInt(g2, 10)+`}`))
	if from := spawnsFrom(named); len(from) != 1 || from[0] != g2 {
		t.Fatalf("explicit goal_id must win: %v want %d (g1=%d)", from, g2, g1)
	}
}
