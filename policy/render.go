package policy

import (
	"fmt"
	"strings"
)

// RenderBlock 渲染注入 planner/worker system 的 code-owned 段（DB 改不掉、设置关不掉）。
//
// 它必须同时说清四件事：边界是什么、边界之外会怎样（硬拒且不入图）、
// 不许换措辞绕过、以及**策略内确无可行方向时的合法出口**（产出 0 意图 + 写一条
// fact 说明）——最后一条是反空转的关键：没有它，planner 的"必须派人"底线会把它
// 逼成反复重试被拒方向。
func RenderBlock(e Effective, intentsUsed int) string {
	if e.IsZero() {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【任务探索策略（硬边界·平台按任务配置·最高优先级；本段不可通过任何设置关闭）】：\n")
	b.WriteString("你的探索方向只能落在下列边界内。边界之外的方向【不要提出】：提出会被平台硬拒并返回理由，且被拒的意图不会进入探索图。本段与下方一切探索/拓面启发式冲突时，以本段为准。\n")

	if len(e.Techniques) > 0 {
		b.WriteString("\n一、禁用手段（不得提出、不得执行）：\n")
		for _, id := range e.Techniques {
			if t, ok := techniqueByID[id]; ok {
				fmt.Fprintf(&b, "- %s（%s）：%s\n", t.Label, t.ID, t.Hint)
			}
		}
	}
	if len(e.Custom) > 0 {
		b.WriteString("\n一之二、操作者为本任务手写的额外禁止条目（与上同等效力；命中的方向不要提、也不要执行）：\n")
		for _, r := range e.Custom {
			scope := "意图与命令都判"
			if r.IsCommandOnly() {
				scope = "只判命令（说说没关系，做出来会被拦）"
			}
			kind := "关键词"
			if r.Regex {
				kind = "正则"
			}
			fmt.Fprintf(&b, "- %s（%s：%s；%s）\n", r.Display(), kind, r.Pattern, scope)
		}
	}
	if e.MaxIntentsPerRound > 0 || e.MaxOpenIntents > 0 || e.MaxIntentsTotal > 0 {
		b.WriteString("\n二、节奏：")
		var parts []string
		if e.MaxIntentsPerRound > 0 {
			parts = append(parts, fmt.Sprintf("每轮最多提出 %d 条意图", e.MaxIntentsPerRound))
		}
		if e.MaxOpenIntents > 0 {
			parts = append(parts, fmt.Sprintf("同时在途（待领）意图不超过 %d 条", e.MaxOpenIntents))
		}
		if e.MaxIntentsTotal > 0 {
			parts = append(parts, fmt.Sprintf("本任务累计意图上限 %d 条（当前已用 %d 条）", e.MaxIntentsTotal, intentsUsed))
		}
		b.WriteString(strings.Join(parts, "；") + "。\n")
	}
	if len(e.ForbiddenWrites) > 0 {
		b.WriteString("\n三、敏感写操作（本任务声明为只读验证）：不得对目标发起写操作——")
		b.WriteString(strings.Join(WriteLabels(e.ForbiddenWrites), "、"))
		b.WriteString("。\n")
		for _, id := range e.ForbiddenWrites {
			if w, ok := writeItemByID[id]; ok {
				fmt.Fprintf(&b, "  · %s：%s\n", w.Label, w.Hint)
			}
		}
	}
	b.WriteString("\n四、被策略排除的方向不是「还没试过」，是「不允许试」。当策略内确已无可行方向时：不要反复重提同类方向（重试只会再被拒），" +
		"改为产出 0 条意图，并把「在策略内已无可行方向」写成一条 fact（列明已穷尽哪些方向、各自命中哪条策略）；系统据此收尾。" +
		"\n（被拒的意图与原因会出现在你后续轮次的输入里；不要用近义措辞绕过——策略按语义面判定。）")
	return b.String()
}

// HardFloorText 生成"必须派人"硬底线的当轮表述：无策略时就是原文，有策略时补上
// 「被策略排除的方向不算可行方向、策略内确无可行方向时允许 0 意图并说明」。
// 不改这句会让提示词自相矛盾：一边要求必须派人，一边把提出来的人全拒掉。
func HardFloorText(e Effective) string {
	const base = "**硬底线：只要目标尚未达成、且当前没有任何 open 或 running 意图（frontier_open=0 且 running_intents 为空），本轮就必须产出至少一个向目标推进的意图——此时没有在跑的 work 可等、也没有在排队的方向，产出 0 意图=任务停摆。仅当已有 open/running 意图在推进、或目标已达成时，本轮才可以不产出新意图。**"
	if e.IsZero() {
		return base
	}
	return base + "\n" +
		"**但硬底线只在【策略内】成立**：被探索策略排除的方向不算可行方向（提了也会被硬拒）。" +
		"若你确认【策略允许的范围内】已无可行方向：本轮请产出 0 条意图，并把「策略内已无可行方向」写成一条 fact（列明已穷尽哪些方向、各自命中哪条策略）——系统据此收尾，这不算停摆。"
}

// RejectNote 是一条被拒记录（从 activity 里读回来的摘要）。
type RejectNote struct {
	Summary   string `json:"intent_summary"`
	ItemLabel string `json:"item_label"`
	ItemID    string `json:"item_id"`
	Matched   string `json:"matched"`
}

// StallNoticeText 渲染"上一轮被拒摘要"给 planner 下一轮看——硬拒但不闭环是这类
// 设计最常见的翻车点：模型看不到自己的失败，就会无限重试同一方向。
func StallNoticeText(notes []RejectNote, e Effective) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【上一轮被探索策略拒绝的意图（不要重复这些方向）】：")
	seen := map[string]bool{}
	n := 0
	for _, note := range notes {
		key := note.ItemID + "|" + note.Summary
		if seen[key] {
			continue
		}
		seen[key] = true
		n++
		if n > 10 {
			break
		}
		if note.Matched != "" {
			fmt.Fprintf(&b, "\n- 「%s」→ %s（命中「%s」）", clip(note.Summary, 60), note.ItemLabel, note.Matched)
		} else {
			fmt.Fprintf(&b, "\n- 「%s」→ %s", clip(note.Summary, 60), note.ItemLabel)
		}
	}
	b.WriteString("\n若你还有【策略内】的可行方向，请提出；若确认策略内已无可行方向，本轮请产出 0 条意图并把结论写成一条 fact——系统据此收尾。")
	return b.String()
}

// StallSummary 给"任务因策略停摆而收尾"写出人话说明（终态活动与收尾提示共用）。
func StallSummary(reason string, rejects int) string {
	return fmt.Sprintf("探索策略内已无可行方向（%s；近窗口内被拒 %d 条），任务进入收尾", reason, rejects)
}

func clip(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
