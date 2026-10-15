package db

import "strings"

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
)

// 战果节点（kind='foothold'/'credential'）的状态：只有两态——在手 / 掉了。
// 战果的全部价值就在「现在还能不能用」，再多的状态只是要人维护、且没人会维护的字段；
// 「曾经用过但已失效」由 report 的说明承载，不需要一个状态位。
const (
	StateLootActive = "active" // 据点还能连、凭据还能用
	StateLootLost   = "lost"   // 掉了：据点断了、凭据失效（复测/复用失败后如实标记）
)

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
