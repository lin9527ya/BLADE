package agent

import (
	"strconv"
	"strings"
	"testing"
)

// 目标达成但还有未推进的通路：block 模式拒收 prove_goal；显式豁免后放行。
func TestCompletionGateBlocksUntilWaived(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	tools.SetCompletionGate(func() string { return "block" })

	goal, err := ts.AddGoal(map[string]any{"text": "拿到管理员权限"}, "planner")
	if err != nil {
		t.Fatal(err)
	}
	factOut := callTool(t, tools.WorkerTools(), "record_fact",
		`{"summary":"读到了管理员配置","detail":"x","evidence":"cat config","confidence":"observed"}`)
	factID := parseRecordedID(t, factOut)
	// 一条从没被任何意图动用的据点 = 未闭合通路。
	if out := callTool(t, tools.WorkerTools(), "report_foothold",
		`{"risk_area":"other","summary":"web01 root shell","access":"shell","privilege":"root",`+
			`"evidence":"nc -lvnp 4444 -> connect","confidence":"high"}`); !strings.HasPrefix(out, "foothold recorded: ") {
		t.Fatalf("据点没记上：%s", out)
	}

	reviewRequested := 0
	tools.SetForceReview(func() { reviewRequested++ })
	blocked := callTool(t, tools.PlannerTools(), "prove_goal",
		`{"goal_id":`+strconv.FormatInt(goal, 10)+`,"evidence_id":`+strconv.FormatInt(factID, 10)+`,"reason":"证据充分"}`)
	if !strings.Contains(blocked, "收官审计") || !strings.Contains(blocked, "chain") {
		t.Fatalf("block 模式应拒收并列出通路缺口：%s", blocked)
	}
	if reviewRequested != 1 {
		t.Fatalf("拒收后应请求下一轮复盘，实际 %d 次", reviewRequested)
	}

	w := callTool(t, tools.PlannerTools(), "accept_completion_risk",
		`{"scope":"chains","reason":"这条据点属于历史遗留，客户要求不再触碰"}`)
	if !strings.Contains(w, "completion waiver recorded") {
		t.Fatalf("豁免没写进去：%s", w)
	}
	ok := callTool(t, tools.PlannerTools(), "prove_goal",
		`{"goal_id":`+strconv.FormatInt(goal, 10)+`,"evidence_id":`+strconv.FormatInt(factID, 10)+`,"reason":"证据充分"}`)
	if !strings.Contains(ok, "marked met") {
		t.Fatalf("豁免后应放行：%s", ok)
	}
}

// notice 模式放行但在回执里带警告；off 模式完全安静（旧行为）。
func TestCompletionGateNoticeAndOff(t *testing.T) {
	for _, mode := range []string{"notice", "off"} {
		tools, ts, _ := newToolWorker(t)
		tools.SetCompletionGate(func() string { return mode })
		goal, err := ts.AddGoal(map[string]any{"text": "目标"}, "planner")
		if err != nil {
			t.Fatal(err)
		}
		factID := parseRecordedID(t, callTool(t, tools.WorkerTools(), "record_fact",
			`{"summary":"证据","detail":"x","evidence":"cmd","confidence":"observed"}`))
		// 制造一个未闭合通路，确保审计有东西可报。
		callTool(t, tools.WorkerTools(), "report_foothold",
			`{"risk_area":"other","summary":"未动用的 shell","access":"shell","privilege":"user",`+
				`"evidence":"id","confidence":"high"}`)
		out := callTool(t, tools.PlannerTools(), "prove_goal",
			`{"goal_id":`+strconv.FormatInt(goal, 10)+`,"evidence_id":`+strconv.FormatInt(factID, 10)+`,"reason":"ok"}`)
		if !strings.Contains(out, "marked met") {
			t.Fatalf("%s 模式应放行：%s", mode, out)
		}
		if mode == "notice" && !strings.Contains(out, "⚠") {
			t.Fatalf("notice 模式应带警告：%s", out)
		}
		if mode == "off" && strings.Contains(out, "⚠") {
			t.Fatalf("off 模式不该有警告：%s", out)
		}
	}
}

func parseRecordedID(t *testing.T, out string) int64 {
	t.Helper()
	for _, prefix := range []string{"fact recorded: ", "finding recorded: "} {
		if strings.HasPrefix(out, prefix) {
			line := strings.SplitN(strings.TrimPrefix(out, prefix), "\n", 2)[0]
			id, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
			if err != nil {
				t.Fatalf("解析 id 失败：%q", out)
			}
			return id
		}
	}
	t.Fatalf("回执里没有 id：%s", out)
	return 0
}
