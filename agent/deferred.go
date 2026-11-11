package agent

import (
	"encoding/json"
	"strings"

	"github.com/lin9527ya/Norma/llm"
	actool "github.com/lin9527ya/Norma/tool"
)

// deferredSystem builds an agent's system-prompt segments and cache boundary from
// its DeferredInfo. When globally-available MCP tools are present, their names + a
// "prefer core tools" instruction render into a <available-deferred-tools> block
// placed as the LAST system-prompt segment, with DynamicBoundary set so the whole
// (session-fixed) system prompt — including the block — is cached (design doc
// §2.1 / C1). Skill-gated MCP names are NOT in this block; they surface when their
// skill loads. When there is no global block to add, the single plain segment is
// still entirely static (callers must keep per-run data out of sysText), so the
// boundary covers it — otherwise the provider gets no cache breakpoint at all and
// a reused session re-pays the full prefix every intent.
// plannerDeferredTools 是**规划层**按需才用的内置工具：它们的 schema 不进每轮工具列表，
// 只在系统提示里列名字，需要时由模型用 SearchExtraTools 取 schema、ExecuteExtraTool 调用。
//
// 为什么挑这几个：planner 37 个工具的 schema 合计约 3.3 万字符（≈1.1 万 token），每轮都
// 随缓存前缀发送；而这几项在绝大多数轮次里根本不会被调用（沉淀知识、合并重复、打标签法、
// 扩测范围、给公司范围补域名）。把它们降为 deferred，每轮固定省下约 6k 字符，且不改变
// 任何工具的实际能力——只是多一次「先查 schema」的往返。
var plannerDeferredTools = []string{
	"record_playbook",          // 2612 字符：只在规划层确认了一条可复用打法时用
	"record_playbook_feedback", // 727
	"promote_knowledge",        // 1542
	"merge_findings",           // 817
	"add_task_scope",           // 范围扩张是低频决策（当前已不在 planner 面上，留作兜底）
	"add_company_scope",
	"list_untested_assets", // 691：按需翻页是低频动作
	"list_companies",       // 443
}

// mergeDeferred 把内置 deferred 工具并入本轮 DeferredInfo：既要藏 schema、列进
// system 的 deferred 块，也要加进会话的 UnlockSet。后者容易漏：有 MCP/技能时
// def.Unlock 非 nil 且只含 MCP 名字，此时只 append 名字、不 Add 到 unlock，
// ExecuteExtraTool 会报 "not unlocked in this session"——内置工具没有技能可加载，
// 于是永远解锁不了。
func mergeDeferred(def *DeferredInfo, names []string) {
	if len(names) == 0 {
		return
	}
	def.Deferred = append(def.Deferred, names...)
	def.GlobalNames = append(def.GlobalNames, names...)
	if def.Unlock != nil {
		def.Unlock.Add(names...)
	}
}

// deferToolsNamed 返回 names 里**实际存在**于 base 的工具名。
// 工具本身仍留在工具面（SDK 负责隐藏 schema、经 SearchExtraTools/ExecuteExtraTool
// 调用）；只标记确实存在的名字，避免把不存在的工具写进提示词（模型会去查一个查不到的名字）。
func deferToolsNamed(base []actool.CoreTool, names []string) []string {
	have := make(map[string]bool, len(base))
	for _, tl := range base {
		have[tl.Name()] = true
	}
	deferred := make([]string, 0, len(names))
	for _, name := range names {
		if have[name] {
			deferred = append(deferred, name)
		}
	}
	return deferred
}

func deferPlannerTools(base []actool.CoreTool) []string {
	return deferToolsNamed(base, plannerDeferredTools)
}

// workerDeferredTools 是 worker 按需才用的内置工具：schema 不进每轮工具列表，
// 只把名字列进 system 的 <available-deferred-tools>，模型需要时用 SearchExtraTools
// 取 schema。挑的是**低频但 schema 大**的项：查询类（CVE/打法/AD/opsec/足迹）、
// 清理销账类、执行台账的检索/取消、出口切换。高频写回（record_fact / report_finding /
// report_credential / report_foothold / insert_assets）与长任务主路径（run/wait/get）
// 保持 schema 常驻，避免每轮多一次「先查 schema」的往返。
var workerDeferredTools = []string{
	"report_footprint",
	"mark_footprint_cleaned",
	"mark_footprint_not_found",
	"mark_artifact_cleared",
	"lookup_cves",
	"cve_exploit",
	"verify_credential",
	"lookup_playbooks",
	"record_playbook_feedback",
	"ad_paths",
	"opsec_notes",
	"list_footprints",
	"asset_neighbors",
	"rotate_egress",
	"grep_execution",
	"cancel_execution",
	"report_detection_check",
	"close_candidate",
}

func deferWorkerTools(base []actool.CoreTool) []string {
	return deferToolsNamed(base, workerDeferredTools)
}

// mainDeferredTools 是主 agent 的低频工具清单。主 agent 是对话入口，用户随时可能
// 让它补记战果/销账，但这些动作在绝大多数轮次里不会发生；把大 schema 藏起来，
// 需要时由模型 SearchExtraTools 取。报告相关的核心写回（report_finding /
// record_fact / report_foothold / report_credential / insert_assets）保持常驻。
var mainDeferredTools = []string{
	"report_footprint",
	"report_artifact",
	"report_detection_check",
	"close_candidate",
	"lookup_cves",
	"lookup_playbooks",
	"verify_credential",
	"promote_knowledge",
	"merge_findings",
	"mark_artifact_cleared",
	"mark_footprint_cleaned",
	"mark_footprint_not_found",
	"accept_footprint_risk",
	"link_intent_source",
	"abandon_goal",
	"list_untested_assets",
	"list_footprints",
	"asset_neighbors",
	"ad_paths",
	"opsec_notes",
	"rotate_egress",
	"expand_digest",
}

func deferMainTools(base []actool.CoreTool) []string {
	return deferToolsNamed(base, mainDeferredTools)
}

func deferredSystem(sysText string, def DeferredInfo) (system []string, boundary int) {
	sysText += def.FindingGuidance
	// 出站产物语言（P41）：放在这里而不是各 agent 的拼装点——四个 agent（planner/worker/
	// mainagent/chat）都从这里出 system 段，一处覆盖全部。与常驻技能同层：会话固定、随整段缓存。
	// zh（默认）时它返回空串，一个字符都不加。
	sysText += artifactLanguageBlock()
	// 目标侧痕迹中性化：同样是 code-owned 常驻规约，四个 agent 的统一入口在这里。
	sysText += traceNeutralityBlock()
	block := actool.RenderDeferredToolsBlock(def.GlobalNames)
	always := strings.TrimSpace(def.AlwaysSkills)
	if always == "" && block == "" {
		return []string{sysText}, 1
	}
	// 分层注入：常量层（常驻技能正文）在前、deferred 工具块在后，二者都是会话固定的
	// 稳定段，随整段 system 一起被缓存（boundary 覆盖全部）。
	system = []string{sysText}
	if always != "" {
		system = append(system, always)
	}
	if block != "" {
		system = append(system, block)
	}
	boundary = len(system) // b >= len → whole system prompt cached (SDK guard)
	return system, boundary
}

// seedUnlockFromHistory replays prior Skill() invocations in the conversation so
// their skill-gated MCPs are re-unlocked on a resumed session (design doc C2). The
// main agent builds a fresh session each turn; its in-memory unlock set would
// otherwise reset, leaving the model able to see a skill-revealed tool name yet
// unable to call it. No-op when unlockSkill is nil (no deferred tools).
func seedUnlockFromHistory(msgs []llm.Message, unlockSkill func(string)) {
	if unlockSkill == nil {
		return
	}
	for _, m := range msgs {
		for _, b := range m.ToolUses() {
			if b.Name != "Skill" {
				continue
			}
			var in struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(b.Input, &in) == nil && in.Name != "" {
				unlockSkill(in.Name)
			}
		}
	}
}
