// Package report renders a deliverable Markdown report from the exploration
// graph: confirmed findings (with severity/evidence) (docs §15 P5).
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lin9527ya/artex/db"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
	// Retracted lists findings retracted after retesting disproved them; they
	// stay on file (墓碑) but leave the findings body.
	Retracted []Retracted
	// NotProbed lists intents that ended blocked/exhausted — 没测成不得当作已测干净。
	NotProbed []NotProbed
	// NoEvidence lists findings with no bound traffic evidence — HTTP 类漏洞的证据
	// 需要人工核对（静态/非 HTTP 结论可忽略本段）。
	NoEvidence []*db.Node
	// Coverage is the in-scope asset × risk-area matrix (未触及 classes are the
	// complement of each asset's cells against db.RiskAreas).
	Coverage []db.CoverageMatrixAsset
	// Retests lists findings whose latest completed retest verdict is not
	// "reproduced" — 复核存疑（inconclusive）或确认修复（fixed），值得点名。
	Retests []RetestNote
	// ConfidenceByNode carries the honest-calibration label (high/medium/low) for
	// in-body findings keyed by finding node id; "" = 未标定 (named in the report).
	ConfidenceByNode map[int64]string
	// RuledOut lists candidates closed with counterevidence (status ruled_out):
	// 不进正文，但留档——"没找到控制点 ≠ 安全" 的对账面。
	RuledOut []CandidateNote
	// ProofGaps lists candidates closed as open_proof_gap: 存疑待补证，不得视为安全。
	ProofGaps []CandidateNote
	// Footholds / Credentials 是战果层（利用成功之后的产物）：据点是已攻陷且当前可用的
	// 通路，凭据是真实读到的口令/哈希/密钥/令牌。
	//
	// **凭据本体不进报告**：报告是要交出去的文档（还能被打进 P19 的批量交付包），在里面印
	// 可用的凭据，等于把战果随交付物一起发走。报告只印身份与来源——类型、账号、取自哪里、
	// 怎么读到的；要看原文在任务记录里用节点详情。据点则连细节一起印（那是"怎么进去的"，
	// 属于报告该讲清楚的事）。
	Footholds   []*db.Node
	Credentials []*db.Node
	// LootUsedBy 记录每条战果被哪些意图动用过（战果节点 id → 用它的意图）。
	//
	// 这是交付物里最接近「攻击链」的东西：只写「拿到了这个据点」是资产清单，写清「拿它走到了
	// 哪一步」才是作业叙述。空 = 到手但还没被真正用过（见 agent 侧的 lootConsumingState：
	// 中途 blocked/exhausted 收场的意图不算动用，否则到手的能力会在报告里假装已被处理）。
	LootUsedBy map[int64][]LootUse
	// Chains 是重建出来的攻击路径（见 AttackChain）。空 = 本次没有多步链，报告里不出这一节。
	Chains []AttackChain
	// TechniqueTally 是本次作业用到的 ATT&CK 技术（技术 ID → 意图数）。
	//
	// 红队的交付物本来就是按技术组织的——客户要的是「你打了哪些 TTP」，不是「你试过哪些方向」。
	// 只列 ID、不翻译成技术名：那需要内置一份会过期的 ATT&CK 表，而 ID 是行内通用语。
	TechniqueTally map[string]int
	// Detections 是检测验证记录（这一手有没有被检出）。三态各自成词，**绝不合成「检出率」**——
	// 把 unknown 混进分母就是把「测不到」读成「没响」。
	Detections []*db.Node
}

// detectionView 是检测验证节点渲染所需的最小字段集。
type detectionView struct {
	Technique string
	Summary   string
	Evidence  string
}

func parseDetection(n *db.Node) detectionView {
	var p struct {
		Technique string `json:"technique"`
		Summary   string `json:"summary"`
		Evidence  string `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return detectionView{p.Technique, p.Summary, p.Evidence}
}

// detectionResultLabel 渲染检测三态。三态**各自成词**——尤其 unknown 不能并进 not_triggered，
// 那会把「我们没看到」说成「它没拦住」，给出一个比不给更危险的结论。
func detectionResultLabel(state string) string {
	switch state {
	case db.StateDetectionTriggered:
		return "已检出"
	case db.StateDetectionNotTriggered:
		return "未检出"
	case db.StateDetectionUnknown:
		return "无法判定"
	}
	return state
}

// LootUse 是一条战果被哪条意图动用过。
type LootUse struct {
	IntentID int64
	Summary  string
}

// AttackChain 是重建出来的一条攻击路径：从一次最初的落脚开始，之后每一步动用了什么、又拿到了什么。
//
// 它**不引入新模型**——只是把已经记在图里的两种边接起来读：
//
//	intent --uses-->   战果（这一步动用了哪次已得的访问）
//	intent --yields--> 战果（这一步产出了什么）
//
// 红队作业的单位从来不是「一个漏洞」，而是「一条走得通的路径」。报告里只写「拿到了什么」是
// 资产清单，把路径连起来才是作业叙述——这一段就是用来兑现这件事的。
type AttackChain struct {
	Head  string      // 起点战果的可读标签
	Steps []ChainStep // 从起点往外走的顺序
}

// ChainStep 是攻击路径上的一跳。
type ChainStep struct {
	IntentID int64
	Summary  string
	// Techniques 是这一步用到的 ATT&CK 技术 ID（可为空）。标在跳上而不是汇总在别处，
	// 是因为「用 T1552.001 读了凭据、拿到 wp_user」才是一句完整的话。
	Techniques []string
	Produced   []string // 这一步产出的战果标签（可能多条）
}

// CandidateNote is one closed candidate (ruled_out / open_proof_gap) with its
// note and confidence label, sourced from the findings row.
type CandidateNote struct {
	Node            *db.Node
	Counterevidence string
	ProofGap        string
	Confidence      string
}

// confidenceLabel renders the honest-calibration label; "" is named explicitly
// instead of guessed.
func confidenceLabel(c string) string {
	switch c {
	case db.ConfidenceHigh:
		return "高（可复现触发链）"
	case db.ConfidenceMedium:
		return "中（观察到强异常，未构造完整对照）"
	case db.ConfidenceLow:
		return "低（据指纹/版本/现象推断）"
	}
	return "未标定"
}

// RetestNote is one finding with a latest retest verdict worth surfacing.
type RetestNote struct {
	Node    *db.Node
	Verdict string
}

// retestVerdictLabel renders one retest verdict for the report.
func retestVerdictLabel(v string) string {
	switch v {
	case "reproduced":
		return "仍可复现"
	case "fixed":
		return "已修复（复测确认）"
	case "inconclusive":
		return "未复现（存疑，需重新审视或重派）"
	}
	return v
}

// Retracted is one finding retracted after retesting disproved it.
type Retracted struct {
	Node   *db.Node
	Reason string
}

// NotProbed is one intent that ended without a real probe (blocked/exhausted).
type NotProbed struct {
	Node *db.Node
}

// nodeLabel extracts a human-readable label from a node payload (intent uses
// title/summary; falls back through common keys).
func nodeLabel(n *db.Node) string {
	var m map[string]any
	if err := json.Unmarshal(n.Payload, &m); err != nil {
		return ""
	}
	for _, k := range []string{"title", "summary", "name", "description", "goal"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
}

func parseFinding(n *db.Node) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return findingView{p.VulnClass, p.Name, p.Severity, p.Summary, p.Evidence.PoC}
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4}

// lootView 是战果节点渲染所需的最小字段集。Secret 只用于判断"这条凭据有没有本体"
// ——**它不参与渲染**，见 Input.Footholds 处的说明。
type lootView struct {
	Summary   string
	Detail    string
	Evidence  string
	Access    string
	Privilege string
	Kind      string
	Username  string
	Secret    string
}

func parseLoot(n *db.Node) lootView {
	var p struct {
		Summary   string `json:"summary"`
		Detail    string `json:"detail"`
		Evidence  string `json:"evidence"`
		Access    string `json:"access"`
		Privilege string `json:"privilege"`
		Kind      string `json:"kind"`
		Username  string `json:"username"`
		Secret    string `json:"secret"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return lootView{p.Summary, p.Detail, p.Evidence, p.Access, p.Privilege, p.Kind, p.Username, p.Secret}
}

// prefixIf 渲染「标签：值」；值为空时返回空串（交给 writeLootMeta 略去）。
func prefixIf(label, value string) string {
	if value == "" {
		return ""
	}
	return label + "：" + value
}

// credentialBodyNote 是报告对凭据本体的处理：只说有没有，不印内容。
// 印出来等于把可用凭据随交付物一起发走——报告是会被提交、被转发的文档。
func credentialBodyNote(secret string) string {
	if secret == "" {
		return ""
	}
	return "已取得（原文不写入报告）"
}

// lootStateLabel 渲染战果状态。未标定/未知的状态原样输出，不猜。
func lootStateLabel(s string) string {
	switch s {
	case db.StateLootActive:
		return "当前可用"
	case db.StateLootLost:
		return "已失效"
	}
	return s
}

// clipRunes 按 rune 截断（不切出半个汉字），用于把意图摘要压进一行。
func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// writeLootUses 渲染「这条战果被用在了哪一步」——交付物里的作业叙述靠它串起来。
// 已失效又没用过的什么都不写：它已经不是一个机会了，占一行只是噪声。
func writeLootUses(b *strings.Builder, uses []LootUse, active bool) {
	switch {
	case len(uses) > 0:
		parts := make([]string, 0, len(uses))
		for _, u := range uses {
			parts = append(parts, fmt.Sprintf("intent-%d「%s」", u.IntentID, clipRunes(u.Summary, 60)))
		}
		fmt.Fprintf(b, "- 动用于：%s\n", strings.Join(parts, "；"))
	case active:
		b.WriteString("- 尚未动用\n")
	}
}

// writeLootMeta 渲染战果的「一行元信息」（类型/通路/权限/状态），空项自动略去。
func writeLootMeta(b *strings.Builder, parts []string) {
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) > 0 {
		fmt.Fprintf(b, "- %s\n", strings.Join(kept, " · "))
	}
}

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 渗透测试报告 — %s\n\n", nz(in.Title, "未命名任务"))
	fmt.Fprintf(&b, "- **任务目标**：%s\n", nz(in.Goal, "（未指定）"))
	fmt.Fprintf(&b, "- **生成时间**：%s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	// summary
	fmt.Fprintf(&b, "## 摘要\n\n")
	fmt.Fprintf(&b, "- 确认发现：**%d** 个\n", len(in.Findings))
	fmt.Fprintf(&b, "- 已排除候选：**%d** 个（有反证，不进正文）\n", len(in.RuledOut))
	fmt.Fprintf(&b, "- 存疑待补证：**%d** 个（不得视为安全）\n", len(in.ProofGaps))
	fmt.Fprintf(&b, "- 战果：据点 **%d** 个、凭据 **%d** 条\n", len(in.Footholds), len(in.Credentials))
	fmt.Fprintf(&b, "- 资产：")
	var types []string
	for t := range in.AssetCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for i, t := range types {
		if i > 0 {
			b.WriteString("、")
		}
		fmt.Fprintf(&b, "%s %d", t, in.AssetCounts[t])
	}
	b.WriteString("\n\n")

	// 战果（利用成功之后的产物）。排在「发现」之前：它是"拿到了什么"的直接交代——
	// 发现说的是"哪里有洞"，战果说的是"这些洞已经打通到什么程度"。
	fmt.Fprintf(&b, "## 战果（已攻陷的通路与取得的凭据）\n\n")
	if len(in.Footholds) == 0 && len(in.Credentials) == 0 {
		b.WriteString("_本次未取得据点或凭据。_\n\n")
	} else {
		if len(in.Footholds) > 0 {
			fmt.Fprintf(&b, "### 据点（%d）\n\n", len(in.Footholds))
			for i, n := range in.Footholds {
				v := parseLoot(n)
				fmt.Fprintf(&b, "**%d. %s**\n\n", i+1, nz(v.Summary, "（未填摘要）"))
				writeLootMeta(&b, []string{
					prefixIf("通路", v.Access),
					prefixIf("权限", v.Privilege),
					lootStateLabel(n.State),
				})
				writeLootUses(&b, in.LootUsedBy[n.ID], n.State == db.StateLootActive)
				if v.Detail != "" {
					fmt.Fprintf(&b, "- 细节：%s\n", v.Detail)
				}
				if v.Evidence != "" {
					fmt.Fprintf(&b, "- 证据：`%s`\n", v.Evidence)
				}
				b.WriteString("\n")
			}
		}
		if len(in.Credentials) > 0 {
			fmt.Fprintf(&b, "### 凭据（%d）\n\n", len(in.Credentials))
			b.WriteString("_仅列身份与来源；**凭据本体不写入报告**（报告会随交付物外流），需要时在任务记录内查看。_\n\n")
			for i, n := range in.Credentials {
				v := parseLoot(n)
				fmt.Fprintf(&b, "**%d. %s**\n\n", i+1, nz(v.Summary, "（未填摘要）"))
				writeLootMeta(&b, []string{
					prefixIf("类型", v.Kind),
					prefixIf("账号", v.Username),
					prefixIf("本体", credentialBodyNote(v.Secret)),
					lootStateLabel(n.State),
				})
				writeLootUses(&b, in.LootUsedBy[n.ID], n.State == db.StateLootActive)
				if v.Evidence != "" {
					fmt.Fprintf(&b, "- 证据：`%s`\n", v.Evidence)
				}
				b.WriteString("\n")
			}
		}
	}

	// 攻击路径：把 uses / yields 两种边接起来读——「拿到了什么」之后的「用它走到了哪一步」。
	// 只有真有多步链时才出这一节：一条战果被用一用是常态，那不叫路径。
	if len(in.Chains) > 0 {
		fmt.Fprintf(&b, "## 攻击路径（从已得的访问展开）\n\n")
		for i, c := range in.Chains {
			fmt.Fprintf(&b, "**路径 %d**：起点 %s\n\n", i+1, c.Head)
			for _, s := range c.Steps {
				fmt.Fprintf(&b, "- intent-%d「%s」", s.IntentID, clipRunes(s.Summary, 80))
				if len(s.Techniques) > 0 {
					fmt.Fprintf(&b, " [%s]", strings.Join(s.Techniques, " "))
				}
				if len(s.Produced) > 0 {
					fmt.Fprintf(&b, " → 得到：%s", strings.Join(s.Produced, "、"))
				}
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	}

	// 技术覆盖（ATT&CK）：整场作业用过的技术汇总。红队的交付物本来就是按技术组织的——
	// 客户要的是「你打了哪些 TTP」，不是「你试过哪些方向」。只列 ID 不翻译技术名：
	// 那需要内置一份会过期的 ATT&CK 表，而 ID 是行内通用语。
	if len(in.TechniqueTally) > 0 {
		fmt.Fprintf(&b, "## 技术覆盖（ATT&CK）\n\n")
		ids := make([]string, 0, len(in.TechniqueTally))
		for id := range in.TechniqueTally {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(&b, "- %s —— %d 条意图\n", id, in.TechniqueTally[id])
		}
		b.WriteString("\n")
	}

	// 检测验证：这一手有没有被检出。排在技术覆盖之后——它按技术组织，是同一份数据的另一面。
	if len(in.Detections) > 0 {
		fmt.Fprintf(&b, "## 检测验证（这一手有没有被检出）\n\n")
		for _, n := range in.Detections {
			v := parseDetection(n)
			fmt.Fprintf(&b, "- %s —— %s", nz(v.Technique, "（未标技术）"), detectionResultLabel(n.State))
			if v.Summary != "" {
				fmt.Fprintf(&b, "：%s", v.Summary)
			}
			b.WriteString("\n")
			if v.Evidence != "" {
				fmt.Fprintf(&b, "  - 证据：`%s`\n", v.Evidence)
			}
		}
		b.WriteString("\n")
		// 这条脚注就是这一节存在的理由：把 unknown 读成「未检出」会得出比不给更危险的结论。
		b.WriteString("_「无法判定」不等于「未被检出」：它说的是本工具查不到证据（没有日志源、没权限、时段对不上），**不能读成防御没拦住**。_\n\n")
	}

	// findings
	fmt.Fprintf(&b, "## 发现\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("_本次未确认漏洞。_\n\n")
	} else {
		type findingRow struct {
			node *db.Node
			view findingView
		}
		rows := make([]findingRow, 0, len(in.Findings))
		for _, n := range in.Findings {
			rows = append(rows, findingRow{n, parseFinding(n)})
		}
		sort.SliceStable(rows, func(i, j int) bool { return sevRank[rows[i].view.Severity] < sevRank[rows[j].view.Severity] })
		for i, r := range rows {
			f := r.view
			fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")))
			fmt.Fprintf(&b, "**置信度**：%s\n\n", confidenceLabel(in.ConfidenceByNode[r.node.ID]))
			fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
			if f.PoC != "" {
				fmt.Fprintf(&b, "**PoC / 证据：**\n\n```\n%s\n```\n\n", f.PoC)
			}
		}
	}

	// 无流量证据的发现：HTTP 类漏洞缺绑定流量时单独列出，供人工核对。
	fmt.Fprintf(&b, "## 无流量证据的发现（HTTP 类需人工核对）\n\n")
	if len(in.NoEvidence) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, n := range in.NoEvidence {
			f := parseFinding(n)
			fmt.Fprintf(&b, "- #%d [%s] %s\n", n.ID, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")))
		}
		b.WriteString("\n")
	}

	// 已排除候选（有反证）：不进正文，但留档——"没找到控制点 ≠ 安全" 的对账面。
	fmt.Fprintf(&b, "## 已排除候选（有反证；不进正文，保留在案）\n\n")
	if len(in.RuledOut) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, c := range in.RuledOut {
			f := parseFinding(c.Node)
			fmt.Fprintf(&b, "- #%d [%s] %s — 反证：%s（置信度：%s）\n", c.Node.ID, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")), nz(c.Counterevidence, "（未记录）"), confidenceLabel(c.Confidence))
		}
		b.WriteString("\n")
	}

	// 存疑待补证：闭合不了的候选明写缺口（"缺信息 ≠ 安全"），不得当作已覆盖。
	fmt.Fprintf(&b, "## 存疑待补证（缺口未闭合，不得视为安全）\n\n")
	if len(in.ProofGaps) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, c := range in.ProofGaps {
			f := parseFinding(c.Node)
			fmt.Fprintf(&b, "- #%d [%s] %s — 缺口：%s（置信度：%s）\n", c.Node.ID, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")), nz(c.ProofGap, "（未记录）"), confidenceLabel(c.Confidence))
		}
		b.WriteString("\n")
	}

	// 覆盖矩阵（范围内资产 × 风险类）：已测/未测成/进行中/范围外 + 未触及类计数。
	fmt.Fprintf(&b, "## 覆盖矩阵（范围内资产 × 风险类）\n\n")
	if len(in.Coverage) == 0 {
		b.WriteString("_（无范围内资产）_\n\n")
	} else {
		for _, a := range in.Coverage {
			fmt.Fprintf(&b, "- [%s] %s（%d 事实）\n", a.Type, a.Label, a.Facts)
			if len(a.Cells) == 0 {
				b.WriteString("  - （无风险类记录）\n")
			}
			for _, c := range a.Cells {
				parts := make([]string, 0, 4)
				if c.Tested > 0 {
					parts = append(parts, fmt.Sprintf("已测 %d", c.Tested))
				}
				if c.Failed > 0 {
					parts = append(parts, fmt.Sprintf("未测成 %d", c.Failed))
				}
				if c.InFlight > 0 {
					parts = append(parts, fmt.Sprintf("进行中 %d", c.InFlight))
				}
				if c.OutOfScope > 0 {
					parts = append(parts, fmt.Sprintf("范围外 %d", c.OutOfScope))
				}
				fmt.Fprintf(&b, "  - %s：%s（%s）\n", c.RiskArea, strings.Join(parts, "、"), strings.Join(c.Intents, ", "))
			}
			fmt.Fprintf(&b, "  - 未触及类：%d/%d\n", len(db.RiskAreas)-len(a.Cells), len(db.RiskAreas))
		}
		b.WriteString("\n")
	}

	// 复核结论（最新复测非"复现"）。
	fmt.Fprintf(&b, "## 复核结论（最新复测非“复现”）\n\n")
	if len(in.Retests) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, rn := range in.Retests {
			f := parseFinding(rn.Node)
			fmt.Fprintf(&b, "- #%d [%s] %s — 复测结论：%s\n", rn.Node.ID, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")), retestVerdictLabel(rn.Verdict))
		}
		b.WriteString("\n")
	}

	// 未测成（blocked/exhausted）：不得当作已测干净。
	fmt.Fprintf(&b, "## 未测成（blocked/exhausted，不得当作已测干净）\n\n")
	if len(in.NotProbed) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, np := range in.NotProbed {
			reason := ""
			if np.Node.BlockedReason != "" {
				reason = " — " + np.Node.BlockedReason
			}
			label := nodeLabel(np.Node)
			if label == "" {
				label = "（无标题）"
			}
			fmt.Fprintf(&b, "- #%d [%s] %s%s\n", np.Node.ID, np.Node.State, label, reason)
		}
		b.WriteString("\n")
	}

	// 已撤回（复测推翻；不进正文，保留在案）。
	fmt.Fprintf(&b, "## 已撤回（复测推翻；不进正文，保留在案）\n\n")
	if len(in.Retracted) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, r := range in.Retracted {
			f := parseFinding(r.Node)
			fmt.Fprintf(&b, "- #%d [%s] %s — 原因：%s\n", r.Node.ID, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")), nz(r.Reason, "（未记录）"))
		}
		b.WriteString("\n")
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
