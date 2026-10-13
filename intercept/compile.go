package intercept

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// P18 约束可执行化：把「任务操作约束」从提示词里的建议，编译成规则层可确定性执行的
// 拦截规则 / 授权范围条目。
//
// 这一层是**确定性**的：只从约束原文里抽取可核实的目标（域名/IP/URL，复用 RoE 那套
// 抽取器）与已知的命令行工具名，不做任何"猜测意图"的推断。抽不出东西的约束就如实
// 说明"这条没法确定性编译"，由人手工写规则或改用模型起草——宁可少编译，不可乱编译。

// ConstraintRuleDraft 是一条编译出来的拦截规则草案（尚未落库）。
type ConstraintRuleDraft struct {
	Name        string `json:"name"`
	MatchTarget string `json:"match_target"` // tool_input
	MatchType   string `json:"match_type"`   // string
	Pattern     string `json:"pattern"`
	Action      string `json:"action"` // deny
	Message     string `json:"message"`
	Priority    int    `json:"priority"`
	// Surface 说明这条 pattern 是从约束里的哪一段抽出来的（原文片段），供人核对。
	Surface string `json:"surface"`
}

// ConstraintScopeDraft 是一条编译出来的授权范围草案（allow 约束 → RoE 允许面）。
type ConstraintScopeDraft struct {
	Kind  string `json:"kind"` // root_domain | ip | cidr ...
	Value string `json:"value"`
	Note  string `json:"note"`
}

// ConstraintDraft 是一次编译的完整结果。
type ConstraintDraft struct {
	ConstraintID int64                  `json:"constraint_id"`
	Kind         string                 `json:"kind"` // allow | deny
	Text         string                 `json:"text"`
	Rules        []ConstraintRuleDraft  `json:"rules"`
	Scope        []ConstraintScopeDraft `json:"scope"`
	// Note 说明这次编译做了什么、哪些部分没编译（人看的核对说明）。
	Note string `json:"note"`
}

// cliToolLexicon 是"约束里点名了某工具"时能确定性识别的一小组命令行工具。刻意保持
// 小而明确：命中即生成一条 tool_input 字符串规则（不区分大小写由规则引擎负责，
// 这里只提供 pattern）。扩充它 = 扩充可确定性编译的约束面。
var cliToolLexicon = []string{
	"sqlmap", "nmap", "masscan", "hydra", "medusa", "ffuf", "feroxbuster", "gobuster",
	"dirsearch", "nuclei", "nikto", "wpscan", "john", "hashcat", "msfconsole",
	"metasploit", "commix", "dalfox", "subfinder", "amass", "httpx", "curl", "wget",
	"mysql", "psql", "redis-cli", "mongosh", "mongo", "nc", "netcat", "ssh",
}

// CompileConstraint 把一条约束编译成草案。kind ∈ allow|deny；allow 产出授权范围
// 条目（RoE 允许面），deny 产出拦截规则。抽不出目标/工具名的约束返回空草案 + Note。
func CompileConstraint(constraintID int64, kind, text string) ConstraintDraft {
	draft := ConstraintDraft{ConstraintID: constraintID, Kind: kind, Text: strings.TrimSpace(text)}
	body := strings.TrimSpace(text)
	if body == "" {
		draft.Note = "约束内容为空，无法编译。"
		return draft
	}
	fmtCtx := func(what string) string {
		return fmt.Sprintf("由任务操作约束 #%d 编译（%s）：%s", constraintID, what, clipRunes(body, 120))
	}

	targets := ExtractTargetsDetailed(body)
	seen := map[string]bool{}
	var targetVals []string
	for _, t := range targets {
		if seen[t.Value] {
			continue
		}
		seen[t.Value] = true
		targetVals = append(targetVals, t.Value)
	}
	tools := detectToolMentions(body)

	if kind == "allow" {
		for _, v := range targetVals {
			draft.Scope = append(draft.Scope, ConstraintScopeDraft{
				Kind:  scopeKindOf(v),
				Value: v,
				Note:  fmtCtx("允许范围"),
			})
		}
		switch {
		case len(draft.Scope) > 0:
			draft.Note = fmt.Sprintf("抽到 %d 个目标，将把它们加入本任务的授权范围（allow 约束进 RoE 允许面，不生成拦截规则）。", len(draft.Scope))
		default:
			draft.Note = "这条允许类约束没有可抽取的目标（域名/IP/URL），无法确定性编译进授权范围；如果它表达的是「允许某种操作」，请写成拦截规则（allow 规则）或在提示词层面保留。"
		}
		return draft
	}

	// deny：目标 → 命中即拦；点名工具 → tool_input 字符串命中即拦。
	for _, v := range targetVals {
		draft.Rules = append(draft.Rules, ConstraintRuleDraft{
			Name:        fmt.Sprintf("任务约束#%d·禁止目标 %s", constraintID, clipRunes(v, 60)),
			MatchTarget: "tool_input",
			MatchType:   "string",
			Pattern:     v,
			Action:      "deny",
			Message:     fmtCtx("禁止访问该目标"),
			Priority:    50,
			Surface:     v,
		})
	}
	for _, tool := range tools {
		if seen[tool] {
			continue // 目标里已经有同名串，避免重复规则
		}
		seen[tool] = true
		draft.Rules = append(draft.Rules, ConstraintRuleDraft{
			Name:        fmt.Sprintf("任务约束#%d·禁用工具 %s", constraintID, tool),
			MatchTarget: "tool_input",
			MatchType:   "string",
			Pattern:     tool,
			Action:      "deny",
			Message:     fmtCtx("禁止使用该工具"),
			Priority:    50,
			Surface:     tool,
		})
	}
	switch {
	case len(draft.Rules) > 0:
		draft.Note = fmt.Sprintf("抽到 %d 项（目标 %d / 工具 %d），将编译为 %d 条 deny 规则；它们只对本任务生效。",
			len(draft.Rules), len(targetVals), len(tools), len(draft.Rules))
	default:
		draft.Note = "这条禁止类约束没有可抽取的目标（域名/IP/URL）或已知工具名，无法确定性编译成规则——它仍会作为提示词约束注入 planner/worker；要让它硬生效，可手工在拦截页写规则，或把约束改写得更具体（点名目标/工具）。"
	}
	return draft
}

// detectToolMentions 找出约束文本里点名的命令行工具（词边界匹配，避免 "nc" 命中
// "since" 这类子串）。
func detectToolMentions(text string) []string {
	lower := strings.ToLower(text)
	var out []string
	for _, tool := range cliToolLexicon {
		re := regexp.MustCompile(`(^|[^a-z0-9_-])` + regexp.QuoteMeta(tool) + `([^a-z0-9_-]|$)`)
		if re.MatchString(lower) {
			out = append(out, tool)
		}
	}
	sort.Strings(out)
	return out
}

// scopeKindOf 把抽到的目标映射成 task_scope 接受的 kind：抽取器给的是主机名/IP
// （URL 已剥成 host），所以只可能是 cidr / ip / root_domain 三种。
func scopeKindOf(value string) string {
	switch {
	case strings.Contains(value, "/") && netIPRe.MatchString(value):
		return "cidr"
	case netIPRe.MatchString(value):
		return "ip"
	default:
		return "root_domain"
	}
}

// netIPRe 与 RoE 抽取器同一个 IP/CIDR 口径。
var netIPRe = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}(/\d{1,2})?$`)
