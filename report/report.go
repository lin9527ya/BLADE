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

	"github.com/lin9527ya/blade/db"
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
	// Closed lists findings triaged as false_positive / duplicate / ignored / fixed /
	// resolved: 不再作为当前风险，不进正文与「确认发现」计数，但留档备查。
	Closed []ClosedNote
	// FindingReports 是「报告 Agent」为每条发现写的详细分析（Markdown），按 finding 节点 id。
	// 那是整条链路里最值钱的一段（利用过程/影响/根因/修复建议），此前只存在详情页里，
	// 总报告完全没用上。空 = 该发现还没有详细报告。
	FindingReports map[int64]string
	// Scope 是「测了什么 / 没测什么」的账目；nil = 没有可核对的范围信息。
	Scope *ScopeSummary
	// Truncated 列出「本次报告因上限被截断」的清单。截断必须被声明——让读者以为
	// 看到的是全部，是比少列几条更严重的问题。
	Truncated []string
	// Duplicates 把「同一漏洞的多次登记」挂到代表项上（节点 id → 重复项 id）。
	// 指纹 = 漏洞类型 + 端点集合；重复项不进正文、也不计入确认发现数——同一件事数两遍
	// 会让客户以为洞很多，也让严重度合计虚高。
	Duplicates map[int64][]int64
	// Footholds / Credentials 是战果层（利用成功之后的产物）：据点是已攻陷且当前可用的
	// 通路，凭据是真实读到的口令/哈希/密钥/令牌。
	//
	// **凭据本体不进报告**：报告是要交出去的文档（还能被打进 P19 的批量交付包），在里面印
	// 可用的凭据，等于把战果随交付物一起发走。报告只印身份与来源——类型、账号、取自哪里、
	// 怎么读到的；要看原文在任务记录里用节点详情。据点则连细节一起印（那是"怎么进去的"，
	// 属于报告该讲清楚的事）。
	Footholds   []*db.Node
	Credentials []*db.Node
	// Artifacts 是**我们留在目标上**的东西（P43）：webshell、上传的文件、建的账号、计划任务、
	// 改过的配置。它跟 Footholds/Credentials 是反的——那两个记"我们拿到了什么"，
	// 这个记"我们给客户留下了什么"。没清掉的 webshell 就是留在客户系统上的后门，
	// 交付时也必须能说清我们动过什么。
	Artifacts []*db.Node
	// LootUsedBy 记录每条战果被哪些意图动用过（战果节点 id → 用它的意图）。
	//
	// 这是交付物里最接近「攻击链」的东西：只写「拿到了这个据点」是资产清单，写清「拿它走到了
	// 哪一步」才是作业叙述。空 = 到手但还没被真正用过（见 agent 侧的 lootConsumingState：
	// 中途 blocked/exhausted 收场的意图不算动用，否则到手的能力会在报告里假装已被处理）。
	LootUsedBy map[int64][]LootUse
	// Chains 是重建出来的攻击路径（见 AttackChain）。空 = 本次没有多步链，报告里不出这一节。
	Chains []AttackChain
	// ADPaths 是域内攻击路径（见 ADPath）：从手头账号到高价值目标的最短利用链。
	// 空 = 这次没有导过 AD 采集结果（或图里没有可达的高价值目标），报告里不出这一节。
	ADPaths []ADPath
	// TechniqueTally 是本次作业用到的 ATT&CK 技术（技术 ID → 意图数）。
	//
	// 红队的交付物本来就是按技术组织的——客户要的是「你打了哪些 TTP」，不是「你试过哪些方向」。
	// 只列 ID、不翻译成技术名：那需要内置一份会过期的 ATT&CK 表，而 ID 是行内通用语。
	TechniqueTally map[string]int
	// Detections 是检测验证记录（这一手有没有被检出）。三态各自成词，**绝不合成「检出率」**——
	// 把 unknown 混进分母就是把「测不到」读成「没响」。
	Detections []*db.Node
	// CredentialAttempts 是凭据尝试台账（P43）：本次作业对**哪个目标的哪个账号**试了几次凭据。
	//
	// 它跟 Credentials 是两个方向：Credentials 说"拿到了什么"，这里说"动过谁"。
	// 交付物里后者同样要交代——客户复盘时最想知道的就是自己的账号有没有被反复试过。
	CredentialAttempts []db.CredentialAttempt
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

// ADPath / ADHop 是域内攻击路径（P32）。定义在 db（db/ad_paths.go）——页面、报告、
// agent 工具三处读同一份实现，别名在这里只是让 report 的调用点读起来自然。
//
// 与 AttackChain 分开：那一条讲「这次作业里哪一步动用了哪次访问」（作业叙述），
// 这一条讲「域里本来存在哪条路」（拓扑）。两者的边与节点都不同，合成一条只会两边都读不清。
type ADPath = db.ADPath
type ADHop = db.ADHop

// ADRelLabel 把边类型翻成报告里的中文（同 db.ADRelLabel：报告与页面必须是同一套词）。
func ADRelLabel(rel string) string { return db.ADRelLabel(rel) }

// CandidateNote is one closed candidate (ruled_out / open_proof_gap) with its
// note and confidence label, sourced from the findings row.
type CandidateNote struct {
	Node            *db.Node
	Counterevidence string
	ProofGap        string
	Confidence      string
}

// ScopeSummary 是「这次到底测了多少、哪些没测」的账目。
//
// 覆盖矩阵回答"资产 × 风险类到了哪一格"，这一节回答"范围内的资产里还有多少一次都没碰过"——
// 交付物必须能诚实回答"什么没测"，否则"没发现漏洞"和"没测"会被读成一回事。
type ScopeSummary struct {
	ScopeRows     int      // 授权范围条目数
	Denominator   int      // 范围内资产数
	Tested        int      // 至少被一条意图/事实碰过的资产数
	Pct           *float64 // Tested/Denominator
	Untested      []string // 未测资产（最多展示 20 条）
	UntestedTotal int      // 未测资产总数（可能大于上面展示的条数）
	WindowFrom    string   // 作业开始
	WindowTo      string   // 作业结束/截止
}

// ClosedNote 是一条已被 triage 关闭的发现（误报 / 重复 / 已忽略 / 已修复 / 已处理）。
// 它不进正文与「确认发现」计数——已经不是当前风险；但也不能静默删掉：交付物要能回答
// "这条后来怎么了"，否则客户在页面上见过的编号会在报告里凭空消失。
type ClosedNote struct {
	Node       *db.Node
	Status     string // db.FindingFalsePositive / Duplicate / Ignored / Fixed / Resolved
	Confidence string
}

// closedStatusLabel 把 triage 状态翻成报告里的中文标签。未知状态原样显示（向前兼容）。
func closedStatusLabel(status string) string {
	switch status {
	case db.FindingFalsePositive:
		return "误报"
	case db.FindingDuplicate:
		return "重复"
	case db.FindingIgnored:
		return "已忽略"
	case db.FindingFixed:
		return "已修复"
	case db.FindingResolved:
		return "已处理"
	}
	return status
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

// sevOrder 给严重度排序：越小越严重。未知值一律排到最后。
//
// 此前用 `sevRank[sev]` 直接查表：查不到的键返回零值 0，于是 "High"、"严重"、拼错的
// 词都被排成比 critical 还靠前——报告的头条会是一条根本不知道严重度是什么的发现。
func sevOrder(sev string) int {
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	}
	return 4
}

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
	// Targets 是凭据能登哪（P36）：报告要能写出「这条口令能登 db01:3306（已验证）」，
	// 而不是只写「拿到一条凭据」。
	Targets []db.CredentialTarget
}

// Fingerprint 是凭据的短指纹（脱敏展示：报告里指认一条凭据靠它，不靠明文）。
func (v lootView) Fingerprint() string {
	return db.CredentialFingerprint(v.Kind, v.Username, v.Secret)
}

func parseLoot(n *db.Node) lootView {
	var p struct {
		Summary   string                `json:"summary"`
		Detail    string                `json:"detail"`
		Evidence  string                `json:"evidence"`
		Access    string                `json:"access"`
		Privilege string                `json:"privilege"`
		Kind      string                `json:"kind"`
		Username  string                `json:"username"`
		Secret    string                `json:"secret"`
		Targets   []db.CredentialTarget `json:"targets"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return lootView{
		Summary: p.Summary, Detail: p.Detail, Evidence: p.Evidence,
		Access: p.Access, Privilege: p.Privilege,
		Kind: p.Kind, Username: p.Username, Secret: p.Secret, Targets: p.Targets,
	}
}

// PrefixIf 渲染「标签：值」；值为空时返回空串（交给 writeLootMeta 略去）。
func PrefixIf(label, value string) string {
	if value == "" {
		return ""
	}
	return label + "：" + value
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
	fmt.Fprintf(&b, "- 已关闭（误报/重复/已忽略/已修复）：**%d** 个（不进正文）\n", len(in.Closed))
	for _, t := range in.Truncated {
		fmt.Fprintf(&b, "- ⚠ %s\n", t)
	}
	fmt.Fprintf(&b, "- 战果：据点 **%d** 个、凭据 **%d** 条\n", len(in.Footholds), len(in.Credentials))
	if len(in.Artifacts) > 0 {
		uncleaned := 0
		for _, n := range in.Artifacts {
			if n.State == db.StateArtifactActive {
				uncleaned++
			}
		}
		fmt.Fprintf(&b, "- 遗留物：**%d** 件（其中**未清理 %d 件**）\n", len(in.Artifacts), uncleaned)
	}
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
					PrefixIf("通路", v.Access),
					PrefixIf("权限", v.Privilege),
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
			b.WriteString("_仅列身份、**指纹**与来源；**凭据本体不写入报告**（报告会随交付物外流）。" +
				"指纹是本体 sha256 的前 8 位：它让同一条凭据在多份报告与界面之间可指认，却反推不出明文。" +
				"需要原文时在任务记录内按需查看（每次查看都会留痕）。_\n\n")
			for i, n := range in.Credentials {
				v := parseLoot(n)
				fmt.Fprintf(&b, "**%d. %s**\n\n", i+1, nz(v.Summary, "（未填摘要）"))
				writeLootMeta(&b, []string{
					PrefixIf("类型", v.Kind),
					PrefixIf("账号", v.Username),
					PrefixIf("指纹", v.Fingerprint()),
					lootStateLabel(n.State),
				})
				// 能登哪：**验证过的与没验证的分开写**——列在一起会让人以为"列出来的都能登"，
				// 而"待验证"恰恰是还没证实的部分。
				if len(v.Targets) > 0 {
					lines := make([]string, 0, len(v.Targets))
					for _, t := range v.Targets {
						if t.Verified {
							lines = append(lines, t.Label()+"（已验证可登）")
							continue
						}
						lines = append(lines, t.Label()+"（待验证）")
					}
					fmt.Fprintf(&b, "- 可登目标：%s\n", strings.Join(lines, "、"))
				}
				for _, t := range v.Targets {
					if t.Verified && t.Evidence != "" {
						fmt.Fprintf(&b, "- 验证证据（%s）：`%s`\n", t.Label(), t.Evidence)
					}
				}
				writeLootUses(&b, in.LootUsedBy[n.ID], n.State == db.StateLootActive)
				if v.Evidence != "" {
					fmt.Fprintf(&b, "- 证据：`%s`\n", v.Evidence)
				}
				b.WriteString("\n")
			}
		}
	}

	// 遗留物：我们留在目标上、可能还没清掉的东西。紧挨着战果——两者是同一类产物的两个方向
	// （拿到了什么 / 留下了什么），分太远会让人忘了还有这一节。
	if len(in.Artifacts) > 0 {
		writeArtifacts(&b, in.Artifacts)
	}

	// 凭据尝试台账（P43）：与「战果」是两个方向——战果说"拿到了什么"，这里说"动过谁"。
	// 交付物里后者同样要交代：客户复盘时最想知道自己的账号有没有被反复试过。
	if len(in.CredentialAttempts) > 0 {
		writeCredentialAttempts(&b, in.CredentialAttempts)
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

	// 域内/云上攻击路径（P32 / P39）：来自导入的枚举结果（SharpHound 采集、AWS 授权明细、
	// K8s RBAC）。与上面那条「攻击路径」是两件事——上面讲这次作业怎么走的，这里讲环境里
	// 本来存在哪条路。分开写，别让客户把「我们会走到这」读成「我们已经走到了这」。
	if len(in.ADPaths) > 0 {
		fmt.Fprintf(&b, "## 攻击路径（域内/云上可达，来自枚举结果）\n\n")
		fmt.Fprintf(&b, "> 以下路径由导入的枚举结果推导（域内的目录采集、云上的 IAM/RBAC 枚举），表示**存在**这条利用链，不代表已实施。\n\n")
		for i, p := range in.ADPaths {
			fmt.Fprintf(&b, "**路径 %d**：%s → %s", i+1, p.Start, p.Target)
			if len(p.TargetTags) > 0 {
				fmt.Fprintf(&b, "（%s）", strings.Join(p.TargetTags, "、"))
			}
			b.WriteString("\n\n")
			for _, s := range p.Steps {
				fmt.Fprintf(&b, "- %s --%s--> %s", s.From, ADRelLabel(s.Rel), s.To)
				if len(s.Rights) > 0 {
					fmt.Fprintf(&b, " [%s]", strings.Join(s.Rights, " "))
				}
				if s.Note != "" {
					fmt.Fprintf(&b, "：%s", s.Note)
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
	//
	// 这一节同时回答「预期与实测」那个问题：**打了多少手 vs 核了多少手**。两边的动作集合
	// 并不相同（技术覆盖数的是意图上的技术标注，检测验证数的是真去查过的手），所以只并列、
	// **绝不相除**——合成一个比值会得到一个像「检出率」却不是的东西。
	if len(in.Detections) > 0 || len(in.TechniqueTally) > 0 {
		techniqueMarks := 0
		for _, n := range in.TechniqueTally {
			techniqueMarks += n
		}
		triggered, notTriggered, unknown := 0, 0, 0
		for _, n := range in.Detections {
			switch n.State {
			case db.StateDetectionTriggered:
				triggered++
			case db.StateDetectionNotTriggered:
				notTriggered++
			case db.StateDetectionUnknown:
				unknown++
			}
		}
		fmt.Fprintf(&b, "## 检测验证（这一手有没有被检出）\n\n")
		if len(in.Detections) == 0 {
			fmt.Fprintf(&b, "本次作业的意图里共标了 **%d** 处 ATT&CK 技术，但**没有留下任何检测验证结论**"+
				"——这不代表没被检出，只是没有去核。\n\n", techniqueMarks)
		} else {
			fmt.Fprintf(&b, "本次作业的意图里共标了 **%d** 处 ATT&CK 技术；其中 **%d 手**留下了检测验证结论——"+
				"已检出 **%d**、未检出 **%d**、无法判定 **%d**。\n\n",
				techniqueMarks, len(in.Detections), triggered, notTriggered, unknown)
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
		}
		// 这两条脚注就是这一节存在的理由。
		// 第一条：把 unknown 读成「未检出」会得出比不给更危险的结论。
		b.WriteString("_「无法判定」不等于「未被检出」：它说的是本工具查不到证据（没有日志源、没权限、时段对不上），**不能读成防御没拦住**。_\n\n")
		// 第二条：上面那两个数**不是同一批动作**，相除会得到一个像「检出率」却不是的东西。
		b.WriteString("_上面「技术标注数」与「检测验证手数」只并列、**不相除**：前者数的是意图上的技术标注，" +
			"后者数的是真去查过的手，两边动作集合不同。预期侧（每条命令**预计**会留下什么）由可检出性知识库" +
			"在**审批那一刻**逐条给出，见任务记录里的审批卡与「系统设置 → 安全与策略 → 可检出性知识库」——" +
			"它按动作逐条记，不在这里汇总。_\n\n")
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
		sort.SliceStable(rows, func(i, j int) bool { return sevOrder(rows[i].view.Severity) < sevOrder(rows[j].view.Severity) })
		for i, r := range rows {
			f := r.view
			fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")))
			fmt.Fprintf(&b, "**置信度**：%s\n\n", confidenceLabel(in.ConfidenceByNode[r.node.ID]))
			fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
			if f.PoC != "" {
				fmt.Fprintf(&b, "**PoC / 证据：**\n\n```\n%s\n```\n\n", f.PoC)
			}
			if dups := in.Duplicates[r.node.ID]; len(dups) > 0 {
				ids := make([]string, 0, len(dups))
				for _, id := range dups {
					ids = append(ids, fmt.Sprintf("#%d", id))
				}
				fmt.Fprintf(&b, "**同指纹的重复登记**：%s（同一漏洞的多次登记，只在此计一次）\n\n", strings.Join(ids, "、"))
			}
			// 报告 Agent 的详细分析（利用过程/影响/根因/修复建议）。原样嵌入：它本身就是
			// Markdown，摘抄只会让"关键位置表/修复建议"这些结构化内容散架。
			if txt := strings.TrimSpace(in.FindingReports[r.node.ID]); txt != "" {
				fmt.Fprintf(&b, "**详细分析（报告 Agent）：**\n\n%s\n\n", txt)
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

	// 已关闭（误报/重复/已忽略/已修复）：不再作为当前风险，但保留在案——静默删掉会让
	// 「报告里少了三条」变成一个没人能回答的问题。
	fmt.Fprintf(&b, "## 已关闭（误报/重复/已忽略/已修复；不进正文）\n\n")
	if len(in.Closed) == 0 {
		b.WriteString("_无。_\n\n")
	} else {
		for _, c := range in.Closed {
			f := parseFinding(c.Node)
			fmt.Fprintf(&b, "- #%d [%s] %s — 状态：%s（置信度：%s）\n", c.Node.ID, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")), closedStatusLabel(c.Status), confidenceLabel(c.Confidence))
		}
		b.WriteString("\n")
	}

	// 范围与覆盖：先回答"测了多少、什么没测"，再往下才是逐格矩阵。
	// "没发现漏洞"和"没测"必须能被读者分开——这是报告诚实性的底线。
	fmt.Fprintf(&b, "## 范围与覆盖（测了什么 / 没测什么）\n\n")
	if in.Scope == nil {
		b.WriteString("_（无范围账目）_\n\n")
	} else {
		sc := in.Scope
		fmt.Fprintf(&b, "- 授权范围条目：%d 条\n", sc.ScopeRows)
		if sc.Denominator == 0 {
			b.WriteString("- 范围内资产：**0 个**（授权范围为空）——本次结论只覆盖实际测试到的目标，不能据此推断范围外安全。\n")
		} else {
			pct := ""
			if sc.Pct != nil {
				pct = fmt.Sprintf("（**%.0f%%**）", *sc.Pct*100)
			}
			fmt.Fprintf(&b, "- 范围内资产：**%d** 个，其中已测 **%d** 个%s\n", sc.Denominator, sc.Tested, pct)
			if sc.UntestedTotal > 0 {
				fmt.Fprintf(&b, "- **未测资产：%d 个**（不等于安全，只说明没碰过）：\n", sc.UntestedTotal)
				for _, u := range sc.Untested {
					fmt.Fprintf(&b, "  - %s\n", u)
				}
				if sc.UntestedTotal > len(sc.Untested) {
					fmt.Fprintf(&b, "  - …还有 %d 个（完整清单见任务页的未测资产列表）\n", sc.UntestedTotal-len(sc.Untested))
				}
			}
		}
		if sc.WindowFrom != "" {
			fmt.Fprintf(&b, "- 作业窗口：%s → %s\n", sc.WindowFrom, nz(sc.WindowTo, "（未设截止）"))
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

// writeCredentialAttempts 渲染凭据尝试台账（P43）。
//
// 按 (目标, 账号) 归并后只报次数：逐条列出每次尝试的命令原文对交付物没有价值，
// 有价值的是"对哪个账号试了几次、有没有成批喷过"。
//
// 口径必须写在正文里，否则这张表会被读成"失败了几次"——它记的是**尝试**次数，
// shell 层分不出成败，平台刻意取保守方向（宁可多问一句，也不让客户的账号被锁）。
func writeCredentialAttempts(b *strings.Builder, rows []db.CredentialAttempt) {
	type key struct{ host, account string }
	type agg struct {
		single, spray int
		tools         map[string]bool
	}
	groups := map[key]*agg{}
	var order []key
	for _, r := range rows {
		k := key{r.Host, r.Account}
		g := groups[k]
		if g == nil {
			g = &agg{tools: map[string]bool{}}
			groups[k] = g
			order = append(order, k)
		}
		if r.Spray {
			g.spray++
		} else {
			g.single++
		}
		if r.Tool != "" {
			g.tools[r.Tool] = true
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].host != order[j].host {
			return order[i].host < order[j].host
		}
		return order[i].account < order[j].account
	})

	fmt.Fprintf(b, "## 凭据尝试台账（动过哪些账号）\n\n")
	b.WriteString("_本节记的是**我们试了哪些账号**，不是试成了什么（战果见上）。口径：计的是" +
		"**尝试次数**，不是失败次数——shell 层分不出成败，平台刻意取保守方向（宁可多问一句）。" +
		"**成批尝试**（爆破器或带口令表）单独标出：一次调用可能是几百次真实尝试，不按次数计。_\n\n")

	single, spray := 0, 0
	for _, k := range order {
		g := groups[k]
		single += g.single
		spray += g.spray
		who := "**" + k.host + "**"
		if k.account == "" {
			// 抽不出账号时**明说抽不出来**，不含糊成"某账号"——读的人要能判断平台看清了多少。
			who += "（未抽出账号）"
		} else {
			who += " 的账号 `" + k.account + "`"
		}
		var parts []string
		if g.single > 0 {
			parts = append(parts, fmt.Sprintf("单次尝试 **%d** 次", g.single))
		}
		if g.spray > 0 {
			parts = append(parts, fmt.Sprintf("**成批尝试 %d 次**（爆破器/带口令表，一次调用可能是几百次真实尝试）", g.spray))
		}
		line := fmt.Sprintf("- %s：%s", who, strings.Join(parts, "；"))
		if len(g.tools) > 0 {
			var ts []string
			for t := range g.tools {
				ts = append(ts, t)
			}
			sort.Strings(ts)
			line += "（手法：" + strings.Join(ts, "、") + "）"
		}
		b.WriteString(line + "\n")
	}
	fmt.Fprintf(b, "\n合计：单次尝试 **%d** 次，成批尝试 **%d** 次，涉及 **%d** 个目标账号组合。\n\n",
		single, spray, len(order))
}

// writeArtifacts 渲染「遗留物与清理」（P43）。
//
// 排序按**未清理优先**：交付物里最要紧的一句话就是"还有东西留在客户系统上"。
// 三态各自成词——unknown（没去确认）不能并进 active（确认还在），那会把"我们不知道清没清"
// 说成"我们确认它还在"（同检测三态的纪律，方向相反、道理一样）。
func writeArtifacts(b *strings.Builder, nodes []*db.Node) {
	type row struct {
		n *db.Node
		a db.Artifact
	}
	rows := make([]row, 0, len(nodes))
	for _, n := range nodes {
		rows = append(rows, row{n, db.ParseArtifact(n)})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if ri, rj := db.ArtifactStateRank(rows[i].n.State), db.ArtifactStateRank(rows[j].n.State); ri != rj {
			return ri < rj
		}
		if rows[i].a.Host != rows[j].a.Host {
			return rows[i].a.Host < rows[j].a.Host
		}
		return rows[i].a.Locator < rows[j].a.Locator
	})

	active, unknown, removed := 0, 0, 0
	for _, r := range rows {
		switch r.n.State {
		case db.StateArtifactActive:
			active++
		case db.StateArtifactUnknown:
			unknown++
		case db.StateArtifactRemoved:
			removed++
		}
	}

	fmt.Fprintf(b, "## 遗留物与清理（我们留在目标上的东西）\n\n")
	if active > 0 {
		fmt.Fprintf(b, "> **注意：有 %d 件遗留物仍留在目标上未清理。**\n\n", active)
	}
	fmt.Fprintf(b, "合计 **%d** 件——未清理 **%d**、未确认 **%d**、已清理 **%d**。\n\n",
		len(rows), active, unknown, removed)
	for i, r := range rows {
		a := r.a
		fmt.Fprintf(b, "**%d. %s**\n\n", i+1, nz(a.Summary, "（未填摘要）"))
		writeLootMeta(b, []string{
			PrefixIf("分类", db.ArtifactKindLabel(a.Kind)),
			PrefixIf("主机", a.Host),
			PrefixIf("位置", a.Locator),
			PrefixIf("访问入口", a.URL),
			db.ArtifactStateLabel(r.n.State),
		})
		if a.Cleanup != "" {
			fmt.Fprintf(b, "- 怎么清：%s\n", a.Cleanup)
		}
		if r.n.State == db.StateArtifactRemoved && a.RemovedEvidence != "" {
			fmt.Fprintf(b, "- 清理证据：`%s`\n", a.RemovedEvidence)
		}
		if a.Detail != "" {
			fmt.Fprintf(b, "- 细节：%s\n", a.Detail)
		}
		if a.Evidence != "" {
			fmt.Fprintf(b, "- 落地证据：`%s`\n", a.Evidence)
		}
		b.WriteString("\n")
	}
}
