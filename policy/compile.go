package policy

import (
	"fmt"
	"regexp"
	"strings"
)

// RuleDraft 是一条由策略编译出来的拦截规则草案（落库前）。
type RuleDraft struct {
	Name        string `json:"name"`
	MatchTarget string `json:"match_target"` // tool_input
	MatchType   string `json:"match_type"`   // regex
	Pattern     string `json:"pattern"`
	Action      string `json:"action"` // deny
	Message     string `json:"message"`
	Priority    int    `json:"priority"`
	ItemID      string `json:"item_id"`
	ItemLabel   string `json:"item_label"`
}

// 上限：与 P18 的单约束 20 条同口径；本策略每项 1 条（含自定义条目），正常远低于上限。
const (
	MaxPolicyRules      = 32
	maxPolicyPatternLen = 4000
	// MaxCustomRules 是自定义禁止条目的条数上限（Validate 拒绝超限，不静默截断）。
	MaxCustomRules = 16
	// MaxCustomPatternLen 是单条自定义条目的长度上限（字符）。
	MaxCustomPatternLen = 200
	policyRulePriority  = 50 // 低于内置规则(80/100)：两者都命中时以内置文案为准
)

// CompileRules 把策略编译成任务级拦截规则草案：手段 + 写操作各 1 条 regex 规则。
//
// 为什么用 regex 而不是字符串包含：写操作的语义只有形状化正则表达得准——
// 要能区分 `DELETE FROM users` 与 `grep DELETE FROM readme.md`，也要让
// `hydra.example.com`（域名）不命中 `hydra`（工具）。
func (e Effective) CompileRules() []RuleDraft {
	var out []RuleDraft
	appendRule := func(id, label, pattern, message string) {
		if strings.TrimSpace(pattern) == "" {
			return
		}
		if len(pattern) > maxPolicyPatternLen {
			pattern = pattern[:maxPolicyPatternLen]
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return // 词表自身写错时不落一条坏规则（单测会兜住）
		}
		out = append(out, RuleDraft{
			Name:        fmt.Sprintf("任务探索策略·%s", label),
			MatchTarget: "tool_input", MatchType: "regex", Pattern: pattern,
			Action: "deny", Message: message, Priority: policyRulePriority,
			ItemID: id, ItemLabel: label,
		})
	}
	for _, id := range e.Techniques {
		t, ok := techniqueByID[id]
		if !ok {
			continue
		}
		appendRule(id, t.Label, alternation(t.Words, nil),
			fmt.Sprintf("任务探索策略禁止「%s」（%s）。这是平台的策略决定，不是目标侧的防御：换载荷/换编码/换措辞重试无效，请改方向。", t.Label, t.ID))
	}
	for _, id := range e.ForbiddenWrites {
		w, ok := writeItemByID[id]
		if !ok {
			continue
		}
		appendRule(id, w.Label, alternation(w.Words, w.Patterns),
			fmt.Sprintf("任务探索策略禁止「%s」（%s）：本任务声明为只读验证，不对目标发起写操作。请改用只读方式取证（是否被拒/是否可读/有无差异）。", w.Label, w.ID))
	}
	// 自定义条目（操作者手写）：关键词走与词表同款的词边界/子串形状，正则原样并入。
	// 排在词表之后，便于阅读时"先看标准项、再看自定义项"。
	for _, r := range e.Custom {
		var words, patterns []string
		if r.Regex {
			patterns = []string{r.Pattern}
		} else {
			words = []string{r.Pattern}
		}
		appendRule("custom", "自定义·"+r.Display(), alternation(words, patterns),
			fmt.Sprintf("任务探索策略禁止「%s」（自定义条目，由操作者设定）。这是平台的策略决定，不是目标侧的防御：换载荷/换编码/换措辞重试无效，请改方向。", r.Display()))
	}
	if len(out) > MaxPolicyRules {
		out = out[:MaxPolicyRules]
	}
	return out
}

// alternation 把关键词与正则片段拼成一条 `(?i)(?:…|…)`。
// ASCII 关键词套词边界（含 `.` 作词内字符），中文关键词直接子串，正则片段原样并入
// （片段里的行首 `(?i)` 会被剥掉，统一由外层设置大小写不敏感）。
func alternation(words, patterns []string) string {
	parts := make([]string, 0, len(words)+len(patterns))
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if isASCII(w) {
			parts = append(parts, `(?:^|[^a-z0-9_.-])`+regexp.QuoteMeta(w)+`(?:[^a-z0-9_.-]|$)`)
			continue
		}
		parts = append(parts, regexp.QuoteMeta(w))
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		p = strings.TrimPrefix(p, "(?i)")
		parts = append(parts, "(?:"+p+")")
	}
	if len(parts) == 0 {
		return ""
	}
	return "(?i)(?:" + strings.Join(parts, "|") + ")"
}
