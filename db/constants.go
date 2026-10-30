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
	// 遗留物（P43）：我们**留在目标上**的东西——webshell、上传的工具、建的账号、计划任务、
	// 改过的配置。
	//
	// 它跟其它战果是反的：据点与凭据是"我们拿到了什么"，遗留物是"我们给客户留下了什么"。
	// 没清掉的 webshell 就是留在客户系统上的后门，而在此之前平台没有任何地方记过这件事，
	// 交付时也说不清动过什么。这个 kind 就是那本台账。
	KindArtifact = "artifact"
	// 目标侧痕迹（footprint）：测试动作可能在目标上留下的可检出信号（日志/EDR/网络/应用/
	// 本机痕迹）。与遗留物的区别：遗留物是**确实留在目标上的具体对象**，痕迹是**可能被
	// 看见的信号**（包括已经清理、仍留在日志里的记录）。由命令历史 + opsec 知识库自动归纳，
	// 也可由 worker 实测补录；状态含“接受风险”，因为有些痕迹无法/不应清理。
	KindFootprint = "footprint"
	// 检测验证（purple team）：一次进攻动作做完之后，这一手**有没有被检出**。
	//
	// 它是「平台」与「扫描器」的分水岭：只会打的是扫描器，能回答「这套防御拦不拦得住」的才是平台。
	// 而且它恰恰是本项目最适合做的一件事——它需要攻防两侧都可控，而实验室正是唯一同时具备两侧的地方。
	KindDetection = "detection"
	// AD 层（P32）：BloodHound/SharpHound 采集回来的域内对象（用户/组/机器/域/OU/GPO）。
	//
	// 只占**一个** kind，靠 payload.category 分类：六个 kind 意味着 ck_node_kind 与
	// ck_node_state 两处列表都得跟着长，而 AD 对象的状态语义完全一致（都是「采集到的事实」），
	// 拆开只有维护成本、没有表达力。P27 的 foothold/credential 已经是这个形状。
	KindADObject = "ad_object"
	// 云对象（P39，G 项）：云上枚举回来的身份与授权（账号/用户/组/角色/策略、K8s 的
	// ServiceAccount/Role/ClusterRole 与绑定关系）。
	//
	// 与 ad_object 分开成两个 kind，而不是合成"身份对象"：两者的**枚举来源、可读字段、
	// 路径语义**都不同（域内讲"谁能到域管"，云上讲"谁能拿到 admin 策略/集群管理员"），
	// 合成一个只会让 payload 变成一堆互不相干的字段。它们共用同一套边与同一条路径算法
	// （member_of / can 顺势走），所以图上仍是一条路。
	KindCloudObject = "cloud_object"
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

// AD 对象只有一个状态：active（这次采集里它在）。
//
// 刻意**不做**「上次采集有、这次没有 → stale」：采集是分文件的（用户可能只导 users.json），
// 按缺席判 stale 会把整片 computers 误标成过期。缺席是「这次没采」，不是「没了」——
// 本仓一贯不把「没测到」写成结论（同 P24/P31）。
const StateADActive = "active"

// 云对象同样只有一态：active（这次枚举里它在）。理由与 AD 相同——枚举是分文件的
// （可能只导了 K8s、没导 IAM），按缺席判过期会把整片未导的部分误标成没了。
const StateCloudActive = "active"

// 战果节点（kind='foothold'/'credential'）的状态：只有两态——在手 / 掉了。
// 战果的全部价值就在「现在还能不能用」，再多的状态只是要人维护、且没人会维护的字段；
// 「曾经用过但已失效」由 report 的说明承载，不需要一个状态位。
const (
	StateLootActive = "active" // 据点还能连、凭据还能用
	StateLootLost   = "lost"   // 掉了：据点断了、凭据失效（复测/复用失败后如实标记）
)

// 遗留物节点（kind='artifact'）的状态：**还留在目标上 / 已清理 / 不知道**。
//
// 三态是必要的，别压成两态：`unknown`（没去确认过还在不在）跟 `active`（确认还在）在
// 交付物里是完全不同的两句话——"我们没清"与"我们不知道清没清"必须分开说。
// 这与检测三态是同一条纪律：unknown 不许并进任何一档。
const (
	StateArtifactActive  = "active"  // 还留在目标上（未清理）
	StateArtifactRemoved = "removed" // 已清理（清理时的那行输出为证）
	StateArtifactUnknown = "unknown" // 没去确认还在不在——不是"已清理"
)

// 目标侧痕迹（kind='footprint'）的状态。
//
// cleaned 与 accepted 必须分开：前者是“已经清理并留下证据”，后者是“决定不清理/无法清理，
// 接受风险并写明理由”。把两者合成一个“已处理”会让报告读不出实际暴露面。
const (
	StateFootprintOpen     = "open"      // 未处置：可能还留着
	StateFootprintUnknown  = "unknown"   // 未确认是否存在
	StateFootprintAccepted = "accepted"  // 接受风险（理由必填）
	StateFootprintCleaned  = "cleaned"   // 已清理（清理证据必填）
	StateFootprintNotFound = "not_found" // 核实后确认不存在（核实证据必填）
)

// 提示节点（kind='hint'）的状态：待采纳 / 已采纳。
//
// 「已采纳」由 planner 调用 consume_hint 显式标记，不是系统猜的：自动判断"哪条意图对应
// 哪条提示"并不可靠，贸然置 consumed 会让操作者以为方向已被处理。未被采纳的提示保持
// active（每轮仍出现在态势里），操作者可以随时删除。
const (
	StateHintActive   = "active"
	StateHintConsumed = "consumed"
)

// MaxActiveHints 是单任务允许同时处于 active 的提示上限。
//
// 上限与 graph_overview 的 hints 窗口同值：超过这个数，多出来的提示就挤不进 planner
// 每轮看到的态势里——留着也无法被采纳，只会让操作者误以为"已经交代过了"。HTTP 入口
// 与概览共用这一个常量，免得两边窗口各改各的。
const MaxActiveHints = 50

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

// IntentTerminalStates 是「意图已经结束」的全部状态，**只在这一处维护**。
//
// 此前这套清单被抄成十来条 SQL 字面量和若干 Go 切片：每加一个终态都要逐个改，
// 漏掉的那条会让意图在某个查询里继续显示为"进行中"——收尾门、继承读、重派判断
// 各说各话，而且没有任何测试会红。no_output（跑完了却什么都没写回）就是这次新加的
// 一个，正好用来把清单收敛掉。
var IntentTerminalStates = []string{
	"done", "negative", "out-of-scope", "blocked", "exhausted", "stopped", "no_output",
}

// IntentTerminalStatesSQL 是上面那份清单的 SQL IN 字面量，如 ('done','negative',…)。
var IntentTerminalStatesSQL = func() string {
	q := make([]string, len(IntentTerminalStates))
	for i, st := range IntentTerminalStates {
		q[i] = "'" + st + "'"
	}
	return "(" + strings.Join(q, ",") + ")"
}()

// IntentStateTerminal 报告一个意图状态是否属于「已结束」。
func IntentStateTerminal(state string) bool {
	for _, st := range IntentTerminalStates {
		if state == st {
			return true
		}
	}
	return false
}

// IntentRecoverableStates 是「没善终、可以带人工消息从 transcript 续跑」的终态。
// 与 ReopenIntent 的集合一致：拦住 blocked（模型/网络失败）、exhausted（预算耗尽）、
// stopped（被规划者/人工终止）、no_output（空跑）。done/negative/out-of-scope 是
// 有结论的收场，不能靠一条消息"接着聊"；换方向应当新建意图。
var IntentRecoverableStates = []string{"blocked", "exhausted", "stopped", "no_output"}

// IntentStateRecoverable 报告这个终态是否允许人工带消息续跑。
func IntentStateRecoverable(state string) bool {
	for _, st := range IntentRecoverableStates {
		if state == st {
			return true
		}
	}
	return false
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
	case "blocked", "exhausted", "no_output", "stopped", StateIntentDeleted:
		// no_output = 跑完了却什么都没写回：它声明过要动用这条战果，但没有任何产出证明
		// 真的用上了——按"没动用"算，否则到手的凭据会被一条空跑悄悄标成已接手。
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
	// AD 攻击路径（P32）：域内对象的四种关系，直接映射 BloodHound 的语义，不合并——
	// 「谁是谁的成员」「谁是本机管理员」「谁在这台机器上有会话」「谁对谁有什么权限」
	// 是四条不同的路，路径视图要能分别读出来。
	RelMemberOf  = "member_of"  // 子对象 --member_of--> 父组
	RelAdminTo   = "admin_to"   // principal --admin_to--> computer（本机管理员）
	RelSessionOn = "session_on" // user --session_on--> computer（在这台机器上有会话）
	// RelCan：ACE 长尾——具体权限名写在 attrs.rights 数组里（GenericAll/DCSync/CanRDP/…）。
	//
	// 收成一条 rel 而不是每种权限一条，是因为权限名是**开放集合**（BloodHound 每版都在加），
	// 每加一个就改一次列级 CHECK 是不能接受的；attrs 就是留给它的扩展点。
	RelCan = "can"
	// RelDependsOn 是**意图之间**的前置依赖：src --depends_on--> dst 表示"src 要等 dst
	// 有结论（done/negative）才该被派给 worker"。串行利用链靠它落进图里，而不是靠
	// planner 的 TodoWrite 临时记忆——后者重启就没、worker 也看不见。
	RelDependsOn = "depends_on"
	// RelReaches 是「这条战果能通到哪」：loot --reaches--> ad_object。据点/凭据与**资产**
	// 的关系走 anchor（两套 id 空间），而 AD 对象是探索节点，用这条边，
	// 图上就能读出"我手上的能力覆盖了域里的哪些机器"。
	RelReaches = "reaches"
	// RelLeaves 是「这条痕迹由谁留下」：source --leaves--> footprint。
	//
	// 痕迹本身不是「产出」：一条意图产出的是事实/漏洞/战果，痕迹是它**顺带在目标上留下的
	// 可检出信号**。所以不复用 yields（那会让痕迹混进「这条意图产出了什么」的叙述，也会被
	// 意图删除的独占级联当成产出物一起删掉——痕迹属于交付证据，不能挂在这套级联上）。
	// 边方向固定为 来源 → 痕迹，与其它关系一致（From 是上游）。
	RelLeaves = "leaves"
	// 云上的关系**不新造边**：它们本来就能被这四条表达——
	//   IAM 用户 → 组：member_of
	//   主体 → 策略（附加/内联）：can，rights 写 attached / inline
	//   谁可以代入某个角色：can，right="assume_role"（方向是 主体 --can--> 角色）
	//   K8s 主体 → Role/ClusterRole：can，right 写 "clusterrole:cluster-admin" 这类
	// 少一条边就少一份要维护的语义，路径算法（db/ad_paths.go）也就能同时走域内与云上。
)
