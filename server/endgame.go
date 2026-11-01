package server

import (
	"fmt"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// 收官对账（endgame reconciliation）。
//
// sapper 的战役复盘里，最后一段不是"再抢一题"，而是**对账**：确认已经拿到的东西都落了账、
// 该关的关了、该写的写了——"收官清账比赶工值钱"。BLADE 的对应物是这几类**已经存在于库里**
// 但没人统一巡检的状态：未闭合候选、未销账遗留物、未处理痕迹、覆盖空白、仍在跑的后台执行。
//
// 清单只**告知**，不阻断收尾：拿不到数据时返回空串（对账失败不该让任务收不了官）。

// reconcileChecklist 汇总一条任务在收尾时该核对的事项。空串 = 没有可报的（或读不到）。
func (e *Engine) reconcileChecklist(t *Task) string {
	if t == nil || t.Store == nil {
		return ""
	}
	var lines []string

	if goals, err := t.Store.ListByKind(db.KindGoal, 500); err == nil {
		open := 0
		for _, g := range goals {
			if g.State == "open" {
				open++
			}
		}
		if open > 0 {
			lines = append(lines, fmt.Sprintf("未达成目标 %d 条：能完成的用 prove_goal 证明；确实达不到的用 abandon_goal 写清理由（报告会如实标「已放弃」，不会冒充达成）。", open))
		}
	}

	if states, err := t.Store.CountIntentStates(); err == nil {
		live := states["open"] + states["running"] + states["paused"]
		if live > 0 {
			lines = append(lines, fmt.Sprintf("未结算意图 %d 条（open/running/paused）：它们在下一次超时收尾时会被判 exhausted，产出留在图上；能推完的先推完。", live))
		}
	}

	if states, err := t.Store.CountKindStates(db.KindFinding); err == nil {
		if n := states["open_proof_gap"]; n > 0 {
			lines = append(lines, fmt.Sprintf("未闭合候选 %d 条（open_proof_gap）：要么补证据转 confirmed，要么 close_candidate 成 ruled_out 并写清阻断点——不能静默留着。", n))
		}
	}

	if nodes, err := t.Store.ListByKind(db.KindArtifact, 500); err == nil {
		active := 0
		for _, n := range nodes {
			if n.State == "active" || n.State == "unknown" {
				active++
			}
		}
		if active > 0 {
			lines = append(lines, fmt.Sprintf("未销账遗留物 %d 条（webshell/上传物/账号）：清理并 mark_artifact_cleared；清不掉的用 accept_artifact_risk 写明理由。这是交付红线，会阻断常规收官。", active))
		}
	}

	if nodes, err := t.Store.ListByKind(db.KindFootprint, 500); err == nil {
		pending := 0
		for _, n := range nodes {
			if n.State == "open" || n.State == "unknown" {
				pending++
			}
		}
		if pending > 0 {
			lines = append(lines, fmt.Sprintf("未处理的目标侧痕迹 %d 条：能确认清掉的 mark_footprint_cleaned，确认没留下的 mark_footprint_not_found，其余由规划者用 accept_footprint_risk 接受风险并写理由。", pending))
		}
	}

	if x := e.m.Executions(); x != nil {
		if n := len(x.ActiveForTask(t.ID)); n > 0 {
			lines = append(lines, fmt.Sprintf("仍在跑的后台执行 %d 个：收尾会取消它们；有已落盘的部分结果，先 get_execution 取回再收笔。", n))
		}
	}

	if len(lines) == 0 {
		return ""
	}
	return "【收尾对账清单（平台生成，按顺序处理；处理不了的写清原因）】\n- " + strings.Join(lines, "\n- ")
}
