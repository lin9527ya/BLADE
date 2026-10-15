package db

import (
	"encoding/json"
	"regexp"
	"strings"
)

// RiskAreas is the closed risk-class vocabulary an intent payload may carry
// (payload.risk_area). The coverage matrix is keyed by it: 注入类归 sqli，
// 命令执行与反序列化归 rce，对象级越权 idor、功能级越权 authz，不贴合时用 other。
var RiskAreas = []string{
	"sqli", "xss", "rce", "ssrf", "ssti", "xxe",
	"idor", "authz", "auth", "logic", "upload", "traversal", "disclosure", "other",
}

// NormalizeRiskArea maps a planner-supplied token onto the closed vocabulary
// (unknown/empty → "other").
func NormalizeRiskArea(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, area := range RiskAreas {
		if v == area {
			return v
		}
	}
	return "other"
}

// maxIntentTechniques 是单条意图允许携带的 ATT&CK 技术数上限。
const maxIntentTechniques = 8

// reAttackTechnique 是 MITRE ATT&CK 技术 ID 的**形状**：T1190 / T1552.001。
//
// 只查形状，不查它是否真实存在——那需要内置一份 ATT&CK 表，而表会过期、会需要维护。
// 取舍是：形状合法但其实是错的 ID，读者一眼能看穿（ATT&CK ID 是行内通用语）；
// 而放任自由文本，连"这是个 ID"都保证不了。
var reAttackTechnique = regexp.MustCompile(`^T\d{4}(\.\d{3})?$`)

// NormalizeTechniques 归一化一组 ATT&CK 技术 ID：大写、去空白、按形状过滤、去重、限长。
// 保留书写顺序。**非法项直接丢弃而不是拒绝整条意图**——标错一个 ID 不该让整条方向作废，
// 它是标注，不是约束。
func NormalizeTechniques(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		t := strings.ToUpper(strings.TrimSpace(raw))
		if t == "" || seen[t] || !reAttackTechnique.MatchString(t) {
			continue
		}
		seen[t] = true
		out = append(out, t)
		if len(out) >= maxIntentTechniques {
			break
		}
	}
	return out
}

// Exploration node kinds (exploration_nodes.kind).
const (
	KindBegin   = "begin"   // DEPRECATED: legacy task root; new tasks seed an origin fact (KindFact + StateOrigin) instead
	KindGoal    = "goal"    // a task objective
	KindIntent  = "intent"  // an exploration direction (planner-generated)
	KindFact    = "fact"    // a worker's exploration result/conclusion (incl. negative results), tied to its intent
	KindFinding = "finding" // a confirmed vulnerability (report_finding), distinct from a fact
	KindHint    = "hint"
	KindDigest  = "digest" // a compressed fold of cold intents/facts (cold-digest-spec §1); lossless — members kept, restorable by id
	// 战果层：利用成功之后的产物。此前 worker 只能把「拿到了 shell」压成一段 fact 文本，
	// 于是规划层看不到「我有哪些据点、能横到哪、握着哪些凭据」——每轮都从外面重新打一遍，
	// 交付报告里的「提权/横向」也只能靠文案。这两个 kind 把战果变成可以据此规划的一等公民。
	KindFoothold   = "foothold"   // 一个在手的据点（反弹 shell / SSH / 会话），挂在被攻陷的资产上
	KindCredential = "credential" // 从据点上读到的一条凭据（口令 / 哈希 / 密钥 / 令牌）
	// 检测验证（purple team）：一次进攻动作做完之后，这一手**有没有被检出**。
	//
	// 它是「平台」与「扫描器」的分水岭：只会打的是扫描器，能回答「这套防御拦不拦得住」的才是平台。
	// 而且它恰恰是本项目最适合做的一件事——它需要攻防两侧都可控，而实验室正是唯一同时具备两侧的地方。
	KindDetection = "detection"
)

// 检测验证节点的状态：三态，且 **unknown 必须独立存在**。
//
// 「测不到」和「没响」是两件完全不同的事，合成一档会把"日志没留/没权限看"读成"防御没拦住"——
// 那正是本仓证据门一贯要防的那种错（同 P24 的「没限住就要如实说」）。
const (
	StateDetectionTriggered    = "triggered"     // 防御侧确实响了（有日志/告警为证）
	StateDetectionNotTriggered = "not_triggered" // 确实没响（证据：查询过、该时段无记录）
	StateDetectionUnknown      = "unknown"       // 无从判定（没日志源、没权限、时段对不上…）——不是"没响"
)

// 战果节点（kind='foothold'/'credential'）的状态：只有两态——在手 / 掉了。
// 战果的全部价值就在「现在还能不能用」，再多的状态只是要人维护、且没人会维护的字段；
// 「曾经用过但已失效」由 report 的说明承载，不需要一个状态位。
const (
	StateLootActive = "active" // 据点还能连、凭据还能用
	StateLootLost   = "lost"   // 掉了：据点断了、凭据失效（复测/复用失败后如实标记）
)

// TechniqueTally 统计一组意图节点用到的 ATT&CK 技术（技术 ID → 意图数）。
//
// **软删除的意图不计**：用户主动把它拿掉了，不该还留在覆盖率里。其余都计——被 blocked 的
// 方向也算「试过」，覆盖率答的是「打过哪些面」，不是「哪些面打赢了」。
//
// 与 LootConsumingIntentState 同理放在 db：agent 用它渲染 graph_overview 的技术统计、
// server 用它生成报告的技术覆盖表，同一条规则写两处迟早会漂移成两套数字。
func TechniqueTally(intents []*Node) map[string]int {
	tally := map[string]int{}
	for _, n := range intents {
		if n.State == StateIntentDeleted {
			continue
		}
		for _, t := range NodeTechniques(n) {
			tally[t]++
		}
	}
	return tally
}

// NodeTechniques 读出一个意图节点携带的 ATT&CK 技术 ID（payload.techniques）。
// 空/畸形 payload 一律当作没有标注，不报错——标注是附加信息，不该阻断任何读取路径。
func NodeTechniques(n *Node) []string {
	if n == nil {
		return nil
	}
	var p struct {
		Techniques []string `json:"techniques"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.Techniques
}

// LootConsumingIntentState 报告「以这个意图状态收场的意图，算不算真的动用了它声明的那条战果」。
//
// 中途没善终的（blocked 模型/网络失败、exhausted 预算耗尽被掐断、stopped 被打断）**不算**：
// 它们没能把那次访问真的用出去。若就此把战果标成「已动用」，这条到手的能力会在图里静默烂掉
// ——而「静默失效」正是本仓一贯要防的东西（同 P24 的「没限住就要如实说」）。用户主动删掉的
// (deleted) 同样不算。其余状态都算已处理：open/running 在推进、done 跑完、negative 探针确实
// 发出且被否定、out-of-scope 已判定不做（重派反而是错的）。
//
// 放在 db 而不是 agent：agent 用它算 outstanding、server 用它渲染报告的「动用于」一行——
// 同一条判定分两处写，迟早会漂移成两套口径。
func LootConsumingIntentState(state string) bool {
	switch state {
	case "blocked", "exhausted", "stopped", StateIntentDeleted:
		return false
	}
	return true
}

// Digest node states (kind='digest'). A digest is 'active' while it renders in
// graph_overview; major compaction retires a merged-away segment to 'superseded'
// (its covers edges repointed to the new digest) — cold-digest-spec §5.1.
const (
	StateDigestActive     = "active"
	StateDigestSuperseded = "superseded"
)

// StateOrigin marks the task root fact (KindFact) seeded at task creation — the
// exploration graph's origin. Every intent traces back to it, so "an intent must
// connect to a fact" holds uniformly from the very first intent. Worker-produced
// facts use state 'confirmed', so this never collides.
const StateOrigin = "origin"

// StateIntentDeleted marks an intent the user假删除(soft delete): it drops out of
// the frontier and graph_overview like other terminal states, but keeps its node
// and full lineage. The delete reason lives in exploration_nodes.delete_reason.
const StateIntentDeleted = "deleted"

// Exploration edge relations (exploration_edges.rel).
const (
	RelSpawns      = "spawns"
	RelDerivedFrom = "derived_from"
	RelYields      = "yields"
	RelProves      = "proves"
	RelCovers      = "covers" // digest --covers--> member (cold-digest-spec §1); source of truth for "which digest folds node X"
	// RelUses：intent --uses--> 战果（据点/凭据）——「这条意图在动用那次已得的访问」。
	//
	// 为什么不复用 derived_from（语义上最接近的那条）：删除一条意图时会沿
	// yields/derived_from 向下做独占级联（db/exploration.go 的 downOf），而战果节点若
	// 被当成「下游」，删掉一条用过它的意图就可能连带把据点/凭据本身删掉——那是证据级
	// 数据，不能挂在会被级联的关系上。单独一条边把它隔离在这套级联之外。
	RelUses = "uses"
)
