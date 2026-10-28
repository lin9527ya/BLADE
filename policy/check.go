package policy

import (
	"fmt"
	"regexp"
	"strings"
)

// IntentInput 是一次意图准入判定的输入。
type IntentInput struct {
	Summary   string
	RiskArea  string
	OpenCount int // 本任务当前待领(open)意图数
	RoundUsed int // 本规划轮已成功创建的意图数
	TotalUsed int // 本任务累计已创建意图数（含种子/人直投）
}

// Verdict 是判定结果。Allow=false 时其余字段说明"被哪条策略拦、命中了什么、怎么改"。
type Verdict struct {
	Allow     bool
	ItemID    string // 命中的词表条目 id，或 "pace:round|open|total"
	ItemLabel string
	Where     string // summary | pace_round | pace_open | pace_total
	Matched   string // 命中的原文片段（pace 类为空）
	Hint      string // 合规改写建议
	// Benign：命中词表但处于否定语境（如"检查登录是否有爆破防护"）——放行，但记审计，
	// 便于事后看"策略是不是太糙"。
	Benign bool
}

// CheckIntent 是意图层策略门（纯函数）。判定顺序：节奏面 → 内容面——
// 额度用尽时不该再为一条注定被拒的意图去扫词表，模型也该先听到"别再派了"。
func (e Effective) CheckIntent(in IntentInput) Verdict {
	var benign Verdict // first negated hit: allow only if nothing else rejects
	if e.MaxIntentsPerRound > 0 && in.RoundUsed >= e.MaxIntentsPerRound {
		return Verdict{ItemID: "pace:round", ItemLabel: "本轮意图配额", Where: "pace_round",
			Hint: fmt.Sprintf("本轮已提出 %d 条意图（上限 %d）：把本轮剩下的判断留到下一轮，或先让在跑的方向出结果", in.RoundUsed, e.MaxIntentsPerRound)}
	}
	if e.MaxOpenIntents > 0 && in.OpenCount >= e.MaxOpenIntents {
		return Verdict{ItemID: "pace:open", ItemLabel: "在途意图上限", Where: "pace_open",
			Hint: fmt.Sprintf("当前已有 %d 条待领意图（上限 %d）：先让它们跑起来、出结果，再补新方向", in.OpenCount, e.MaxOpenIntents)}
	}
	if e.MaxIntentsTotal > 0 && in.TotalUsed >= e.MaxIntentsTotal {
		return Verdict{ItemID: "pace:total", ItemLabel: "意图总预算", Where: "pace_total",
			Hint: fmt.Sprintf("本任务累计已创建 %d 条意图（上限 %d）：不要再派新方向，改为判定目标是否达成、整理已得结论", in.TotalUsed, e.MaxIntentsTotal)}
	}
	for _, id := range e.Techniques {
		t, ok := techniqueByID[id]
		if !ok {
			continue
		}
		if hit, ok := MatchWords(in.Summary, t.Words); ok {
			if Negated(in.Summary, hit.Where) {
				// 否定语境跳过这一项，继续检查其它手段/禁止写入。此前直接 Allow，
				// "检查爆破防护，然后删除订单"会因为第一个词否定而整条放行。
				if benign.ItemID == "" {
					benign = Verdict{Allow: true, ItemID: t.ID, ItemLabel: t.Label, Where: "summary", Matched: hit.Matched, Benign: true}
				}
				continue
			}
			return Verdict{ItemID: t.ID, ItemLabel: t.Label, Where: "summary", Matched: hit.Matched, Hint: t.Hint}
		}
	}
	for _, id := range e.ForbiddenWrites {
		w, ok := writeItemByID[id]
		if !ok {
			continue
		}
		if hit, ok := MatchWords(in.Summary, w.Words); ok {
			if Negated(in.Summary, hit.Where) {
				if benign.ItemID == "" {
					benign = Verdict{Allow: true, ItemID: w.ID, ItemLabel: w.Label, Where: "summary", Matched: hit.Matched, Benign: true}
				}
				continue
			}
			return Verdict{ItemID: w.ID, ItemLabel: w.Label, Where: "summary", Matched: hit.Matched, Hint: w.Hint}
		}
	}
	// 自定义条目（操作者手写）最后判：词表是通用底线，自定义是"这家/这单特有的边界"。
	for _, r := range e.Custom {
		if r.IsCommandOnly() {
			continue // 只作用于命令层：意图措辞不判
		}
		label := "自定义·" + r.Display()
		hint := "这是操作者为本任务设定的自定义边界：换措辞同样会被拒；若确需调整，请让操作者在任务页/设置页改这条策略。"
		if r.Regex {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				continue // 非法正则不该让整次判定失败（写入侧 Validate 已拦）
			}
			if loc := re.FindStringIndex(in.Summary); loc != nil {
				return Verdict{ItemID: "custom", ItemLabel: label, Where: "summary",
					Matched: in.Summary[loc[0]:loc[1]], Hint: hint}
			}
			continue
		}
		if hit, ok := MatchWords(in.Summary, []string{r.Pattern}); ok {
			if Negated(in.Summary, hit.Where) {
				if benign.ItemID == "" {
					benign = Verdict{Allow: true, ItemID: "custom", ItemLabel: label, Where: "summary", Matched: hit.Matched, Benign: true}
				}
				continue
			}
			return Verdict{ItemID: "custom", ItemLabel: label, Where: "summary", Matched: hit.Matched, Hint: hint}
		}
	}
	if benign.ItemID != "" {
		return benign
	}
	return Verdict{Allow: true}
}

// RejectText 渲染给模型看的三段式拒绝理由。
//
// 必须让模型明白三件事：① 这条没进探索图（不是"排队中"）；② 命中的是**平台的策略**，
// 不是目标侧的防御（否则它会本能地换编码/换措辞再试）；③ 往哪改是合规的。
func RejectText(v Verdict, summary string) string {
	var b strings.Builder
	b.WriteString("【BLADE 探索策略·硬拒绝】本条意图未写入探索图（不计入待领方向，也不会被执行）。\n")
	if v.Matched != "" {
		fmt.Fprintf(&b, "命中条目：%s（%s）——命中词「%s」位于意图描述。\n", v.ItemLabel, v.ItemID, v.Matched)
	} else {
		fmt.Fprintf(&b, "命中条目：%s（%s）。\n", v.ItemLabel, v.ItemID)
	}
	if v.Hint != "" {
		b.WriteString("合规改写建议：" + v.Hint + "\n")
	}
	b.WriteString("不要用近义措辞重提同一方向：策略按语义面判定，重试只会再次被拒；策略内确无可行方向时，请把该结论写成一条 fact。")
	return b.String()
}
