package server

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"path/filepath"
	"strings"

	"github.com/lin9527ya/Norma/skill"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/traffic"
)

// wireAgentAugment connects the PG agent_visibility table into the agent runtime:
// an agent's visible skills are loaded from the filesystem and packed into one
// Skill meta-tool; its visible stdio MCP servers are spawned and expanded to
// mcp__server__tool. skillDir is the root directory of all skill subdirectories.
// hostTools, if set, returns runtime host tools (currently the traffic tools when
// capture is on) to add to EVERY agent's base list — the DB tools table then
// filters them per-agent binding. Empty/nil → no host tools this run (capture off).
func wireAgentAugment(pg *db.DB, skillDir string, hostTools func() ([]actool.CoreTool, map[string][]string)) {
	augment := func(ctx context.Context, agentKey string) ([]actool.CoreTool, agent.DeferredInfo, func()) {
		a, err := pg.GetAgentByKey(agentKey)
		if err != nil || a == nil {
			return nil, agent.DeferredInfo{}, nil
		}
		var extra []actool.CoreTool

		// --- skills: load visible skills into reg (used for the Skill meta-tool
		// and to know which MCP servers are skill-gated). ---
		var reg *skill.Registry
		if names, _ := pg.AgentSkillNames(a.ID); len(names) > 0 {
			nameSet := make(map[string]bool, len(names))
			for _, n := range names {
				nameSet[n] = true
			}
			if allReg, err := skill.LoadDir(skillDir); err == nil && allReg != nil {
				reg = skill.NewRegistry()
				for _, s := range allReg.List() {
					// match by directory name (Base of Dir), not by skill display Name
					if s.Dir != "" && nameSet[filepath.Base(s.Dir)] {
						reg.Add(s)
					}
				}
				if len(reg.List()) == 0 {
					reg = nil
				}
			}
		}
		// --- 常驻技能（分层注入的常量层）：正文整段预渲染，随 DeferredInfo 注入 system
		// 稳定段（在 deferred 工具块之前、同属缓存前缀）——「规则一律常驻」，不依赖模型
		// 想起调用 Skill 工具。上限在写入侧强制（db.MaxAlwaysSkills）。 ---
		alwaysText := ""
		if reg != nil {
			if alwaysNames, _ := pg.AgentAlwaysSkillNames(a.ID); len(alwaysNames) > 0 {
				byDir := make(map[string]skill.Skill, len(alwaysNames))
				for _, s := range reg.List() {
					if s.Dir != "" {
						byDir[filepath.Base(s.Dir)] = s
					}
				}
				var b strings.Builder
				for _, name := range alwaysNames { // ORDER BY skill_name → 输出稳定（缓存友好）
					s, ok := byDir[name]
					if !ok {
						continue
					}
					if b.Len() == 0 {
						b.WriteString("## 【常驻技能 · 始终遵守】\n以下技能已随本 agent 常驻加载（无需再调用 Skill 工具）；把它们当作硬性作业规范执行。\n")
					}
					b.WriteString("\n### " + s.Name + "\n" + strings.TrimSpace(s.Instructions) + "\n")
				}
				alwaysText = b.String()
			}
		}

		// A server named by any visible skill's `mcps:` is skill-gated: its tools
		// are deferred + locked (not in the global block) until that skill loads.
		gated := map[string]bool{}
		if reg != nil {
			for _, s := range reg.List() {
				for _, srv := range s.MCPs {
					gated[srv] = true
				}
			}
		}

		// --- mcp: connect enabled servers that are directly visible to this agent
		// OR skill-gated (named in a visible skill's mcps field). Directly-visible
		// and NOT gated → global (unlocked from session start). Skill-gated →
		// deferred until that skill is invoked (regardless of direct visibility).
		var closers []io.Closer
		serverTools := map[string][]string{} // server name → its tool names
		var allNames, globalNames []string
		globalSet := map[string]bool{}
		{
			mcpIDs, _ := pg.AgentVisible(a.ID, "mcp")
			want := idSet(mcpIDs)
			all, _ := pg.ListMCP()
			for _, m := range all {
				if !m.Enabled {
					continue
				}
				directVisible := want[m.ID]
				skillGated := gated[m.Name]
				if !directVisible && !skillGated {
					continue // neither directly visible nor referenced by a visible skill
				}
				cl, err := connectMCP(ctx, m)
				if err != nil {
					log.Printf("[mcp] %s 连接失败: %v", m.Name, err)
					continue
				}
				closers = append(closers, cl)
				ts, err := cl.Tools(ctx)
				if err != nil {
					log.Printf("[mcp] %s tools/list 失败: %v", m.Name, err)
					continue
				}
				// 工具级开关（全局）：关掉的工具在这里就被丢掉——收集 allNames /
				// globalNames / serverTools 之前过滤一次，于是「直接可见 → 全局块」
				// 与「skill-gated → 解锁」两条注入路径同时拿不到它。
				off, _ := pg.MCPDisabledTools(m.ID)
				for _, t := range ts {
					if off[t.Name()] {
						continue
					}
					extra = append(extra, t)
					allNames = append(allNames, t.Name())
					serverTools[m.Name] = append(serverTools[m.Name], t.Name())
					if directVisible && !skillGated {
						// directly visible and not gated → available from session start
						globalNames = append(globalNames, t.Name())
						globalSet[t.Name()] = true
					}
					// skill-gated tools stay out of globalNames; unlocked via unlockSkill()
				}
			}
		}

		// Shared call-gate: global MCP tools are unlocked from the start; skill-gated
		// ones are unlocked when their skill loads (or replayed from history — C2).
		unlock := actool.NewUnlockSet(globalNames...)
		unlockSkill := func(skillName string) {
			if reg == nil {
				return
			}
			if s, ok := reg.Get(skillName); ok {
				for _, srv := range s.MCPs {
					unlock.Add(serverTools[srv]...)
				}
			}
		}

		// Skill meta-tool: on load, unlock the skill's MCPs and reveal their names
		// (the ones not already in the global block).
		if reg != nil {
			reg.OnInvoke = func(s skill.Skill) string {
				unlockSkill(s.Name)
				var reveal []string
				for _, srv := range s.MCPs {
					for _, n := range serverTools[srv] {
						if !globalSet[n] {
							reveal = append(reveal, n)
						}
					}
				}
				return actool.RenderDeferredToolsBlock(reveal)
			}
			// Attribution for the usage ledger: ToolAugment only gets (ctx, agentKey),
			// so the run's task/session ids ride in on the ctx (agent.RunInfo). Read it
			// once here — this closure is rebuilt per run, so the captured value always
			// belongs to this run.
			extra = append(extra, meterSkillTool(reg.Tool(), pg, reg, a.Key, agent.RunInfoFrom(ctx)))
		}

		// host tools (traffic / orchestration / custom) — added to every agent's base;
		// ToolResolve then keeps them only for agents the tool is bound to. Custom
		// tools flagged deferred contribute their names to the deferred wiring so
		// their schema is withheld (SearchExtraTools/ExecuteExtraTool), same as MCP.
		if hostTools != nil {
			ht, deferredBinds := hostTools()
			extra = append(extra, ht...)
			for name, boundAgents := range deferredBinds {
				if !contains(boundAgents, a.Key) {
					continue // only defer names this agent is actually bound to
				}
				allNames = append(allNames, name)       // schema withheld from the prompt
				globalNames = append(globalNames, name) // advertised in the deferred block
				globalSet[name] = true
				if unlock != nil {
					unlock.Add(name) // global deferred → callable from the start
				}
			}
		}

		cleanup := func() {
			for _, c := range closers {
				_ = c.Close()
			}
		}
		def := agent.DeferredInfo{
			Deferred:     allNames,
			GlobalNames:  globalNames,
			Unlock:       unlock,
			UnlockSkill:  unlockSkill,
			AlwaysSkills: alwaysText,
		}
		return extra, def, cleanup
	}
	o := agent.CurrentOverrides()
	o.ToolAugment = augment
	agent.SetOverrides(o)
}

func idSet(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// seedPrompts writes each built-in agent's code-default prompt body into
// agent_prompts on startup — first-insert only (SeedPromptIfEmpty is a no-op once
// any version exists), so the DB becomes the authoritative editable source while
// user edits survive restarts. Runs after seedBuiltins has created the agent rows.
func seedPrompts(pg *db.DB) {
	for key, tmpl := range agent.BuiltinPromptSeeds() {
		a, err := pg.GetAgentByKey(key)
		if err != nil || a == nil {
			log.Printf("[prompts] seed %s 跳过: agent 不存在 (%v)", key, err)
			continue
		}
		if err := pg.SeedPromptIfEmpty(a.ID, tmpl); err != nil {
			log.Printf("[prompts] seed %s 失败: %v", key, err)
		}
	}
	seedPlannerDomainTemplates(pg)
}

// seedPlannerDomainTemplates 播种内置 planner 领域模板，并把同名场景分类的默认模板
// 指过去。两步都幂等：模板按 nkey 首插入（改过/删掉不复活），分类按 nkey 首插入
// （已存在则保持用户设置）。
//
// 分类预设只在**分类是本次新建**时写 default_planner_template_id——已存在的分类
// 哪怕默认模板是 0（用户清空过）也不覆盖。
func seedPlannerDomainTemplates(pg *db.DB) {
	for name, body := range agent.PlannerDomainSeeds() {
		inserted, err := pg.SeedPlannerTemplateIfEmpty(name, body, false)
		if err != nil {
			log.Printf("[prompts] seed planner 领域模板 %s 失败: %v", name, err)
			continue
		}
		if !inserted {
			continue // 模板已存在（内置行只刷新正文）：分类只在首次播种时补建，删掉不再复活
		}
		tpl, err := pg.GetPlannerTemplateByNKey(name)
		if err != nil || tpl == nil {
			log.Printf("[prompts] seed planner 领域模板 %s 读不回 id: %v", name, err)
			continue
		}
		if _, err := pg.SeedTaskCategoryWithPresetIfEmpty(name, db.TaskCategoryPreset{
			DefaultCoverageEnabled:   true,
			DefaultPlannerTemplateID: tpl.ID,
		}); err != nil {
			log.Printf("[prompts] seed 场景分类 %s 失败: %v", name, err)
		}
	}
}

// wireTools seeds the built-in tool catalog (idempotent, first-insert only so page
// edits survive restart) and wires the DB tools table into the agent runtime: at
// tool-assembly time each built-in tool is filtered by its agent binding / enabled
// flag and, if kept, wrapped so the model sees the DB-overridden description/schema
// and缺省入参 get injected. MCP/skill/host tools have no row and pass through.
func wireTools(pg *db.DB, domainReg map[string]actool.CoreTool) {
	bindingEnabled := func() bool { return pg.GetBool(settingAgentTrafficBinding, false) }
	lootExecEnabled := func() bool { return pg.GetBool(settingLootExec, false) }
	// OAST：总开关 + token 都在设置里；token 为空等于没配。
	oastConfig := func() (string, string, bool) {
		if !pg.GetBool(settingOASTEnabled, false) {
			return "", "", false
		}
		token, ok, _ := pg.GetSetting(settingOASTToken)
		if !ok || strings.TrimSpace(token) == "" {
			return "", "", false
		}
		domain, ok, _ := pg.GetSetting(settingOASTDomain)
		if !ok || strings.TrimSpace(domain) == "" {
			domain = "requestrepo.com"
		}
		return strings.TrimSpace(token), strings.TrimSpace(domain), true
	}
	// Seed the built-in domain tools (first-insert only; DO NOTHING preserves edits).
	// No startup prune: rows we didn't seed are left alone so future user-defined
	// custom tools (system=false, added via the UI) survive restarts.
	for _, s := range agent.BuiltinToolSeeds() {
		schema, _ := json.Marshal(s.Schema)
		agents, _ := json.Marshal(s.Agents)
		if err := pg.SeedTool(s.Key, s.Desc, schema, agents); err != nil {
			log.Printf("[tools] seed %s 失败: %v", s.Key, err)
		}
	}
	// Seed the traffic host tools so they're bindable per-agent like built-ins.
	// Default binding = worker (preserves prior behavior). Their runtime availability
	// is still gated by the global capture switch (hostTools() returns them only when
	// capture is on), so an off-capture binding simply never surfaces the tool.
	trafficAgents, _ := json.Marshal([]string{"worker"})
	for _, t := range traffic.SeedToolMetas() {
		schema, _ := json.Marshal(t.InputSchema())
		if err := pg.SeedTool(t.Name(), t.Description(), schema, trafficAgents); err != nil {
			log.Printf("[tools] seed %s 失败: %v", t.Name(), err)
		}
	}
	// bashInteractiveShellNote is appended to Bash's description ONLY for agents whose
	// interactive_shell is on, so Bash points at shell_open for interactive programs
	// without ever referencing a tool that isn't injected (§14.1/§14.2).
	const bashInteractiveShellNote = "\n\n需要【交互输入】的程序（msfconsole / ssh 交互登录 / mysql、psql、python 等 REPL / 密码或 yes/no 提示 / nc 反弹 shell）不要用 Bash（它没有 stdin、会卡住），改用 shell_open 开交互会话（用完 shell_close）。一次性、非交互命令仍用 Bash。"
	resolveTools := func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool {
		rows, err := pg.ListTools()
		if err != nil {
			log.Printf("[tools] 读取工具表失败，按代码默认放行: %v", err)
			return tools
		}
		byKey := make(map[string]*db.Tool, len(rows))
		for _, t := range rows {
			byKey[t.Key] = t
		}
		runInfo := agent.RunInfoFrom(ctx)
		resolve := func(t actool.CoreTool, row *db.Tool) actool.CoreTool {
			var schema map[string]any
			if len(row.Schema) > 0 {
				_ = json.Unmarshal(row.Schema, &schema)
			}
			return meterTool(agent.DecorateTool(t, row.Description, schema), pg, row.Key, agentKey, runInfo)
		}
		out := tools[:0:0]
		for _, t := range tools {
			row, known := byKey[t.Name()]
			if !known { // MCP/skill/host tool: no row → untouched
				out = append(out, t)
				continue
			}
			if !row.Enabled || !contains(row.Agents, agentKey) {
				continue // disabled globally or not bound to this agent → drop
			}
			out = append(out, resolve(t, row))
		}
		// inject: domain tools bound to this agent in the DB but absent from the
		// incoming list. Covers agents (Auto, custom) whose base only has DefaultTools
		// and therefore never includes ToolSet-backed domain tools. Per-task instances
		// in the base always win: inList is built from the original incoming list so a
		// worker's own upsert_asset is never shadowed by the server-level registry copy.
		// Prefer a per-task registry when the run carries a task: the server-level
		// copy has a nil exploration store, so record_fact/report_finding would
		// answer "需要任务上下文" for triggered reporter/retester runs.
		if ri := agent.RunInfoFrom(ctx); ri.TaskID > 0 && ri.ExplorationID > 0 {
			if taskReg := buildTaskDomainReg(pg, ri.ExplorationID, ri.TaskID); len(taskReg) > 0 {
				domainReg = taskReg
			}
		}
		if len(domainReg) > 0 {
			inList := make(map[string]bool, len(tools))
			for _, t := range tools {
				inList[t.Name()] = true
			}
			for _, row := range rows {
				if row.Kind == "shell" || !row.Enabled || !contains(row.Agents, agentKey) || inList[row.Key] {
					continue
				}
				inst, ok := domainReg[row.Key]
				if !ok {
					continue // not a domain tool; custom/host tools are injected via hostTools()
				}
				out = append(out, resolve(inst, row))
			}
		}
		// shell hints: user-defined kind="shell" tools are not callable — they are
		// environment declarations that tell the model which command-line tools are
		// installed. Collect the ones bound to this agent and append to Bash's description.
		var shellHints []string
		for _, row := range rows {
			if row.Kind == "shell" && row.Enabled && contains(row.Agents, agentKey) {
				shellHints = append(shellHints, "- "+row.Key+": "+row.Description)
			}
		}
		if len(shellHints) > 0 {
			note := "\n\n以下工具已安装在此 bash 环境中，可直接通过 Bash 调用：\n" + strings.Join(shellHints, "\n")
			for i, t := range out {
				if t.Name() == "Bash" {
					out[i] = agent.DecorateTool(t, t.Description()+note, t.InputSchema())
					break
				}
			}
		}
		// interactive shell: gated purely by the agent's interactive_shell flag (like
		// web_search), NOT by tools-table binding. When on, inject the 5 shell_* tools
		// and COUPLE the Bash description addendum so it points at shell_open — and never
		// dangles when off. See docs/README.md「交互式 shell」.
		if !actool.InteractiveShellDisabled() {
			if a, err := pg.GetAgentByKey(agentKey); err == nil && a != nil && a.InteractiveShell {
				out = append(out, actool.ShellSessionTools()...)
				for i, t := range out {
					if t.Name() == "Bash" {
						out[i] = agent.DecorateTool(t, t.Description()+bashInteractiveShellNote, t.InputSchema())
						break
					}
				}
			}
		}
		// loot_exec 默认关：未启用时**从工具面移除**，而不是让模型看见一个永远拒绝的工具——
		// 那样每轮都会浪费一次调用去确认它能不能用。操作者打开设置后，下一次装配即出现。
		if !lootExecEnabled() {
			out = dropToolByName(out, "loot_exec", "loot_upload", "loot_download", "foothold_exec", "tunnel_open")
		}
		// OAST 未配置时同样从工具面移除：让模型看见一个永远拒绝的工具只会浪费调用。
		if _, _, on := oastConfig(); !on {
			out = dropToolByName(out, "oast_mint", "oast_poll")
		}
		// 失败提示：包在最后，覆盖上面所有 DecorateTool 的结果。模型此前只看到
		// `[exit code N]`，没有「下一步该换什么」的线索。
		for i, t := range out {
			if t.Name() == "Bash" {
				out[i] = agent.WithFailureHint(t)
				break
			}
		}
		return out
	}
	o := agent.CurrentOverrides()
	o.FindingTrafficBindingEnabled = bindingEnabled
	o.LootExecEnabled = lootExecEnabled
	o.OASTConfig = oastConfig
	// 噪声档位：每轮现读，改完下一轮生效。只认 normal|quiet，其它值按 normal。
	o.NoiseLevel = func() string {
		v, ok, _ := pg.GetSetting(settingOpsecNoise)
		if ok && strings.EqualFold(strings.TrimSpace(v), "quiet") {
			return "quiet"
		}
		return "normal"
	}
	o.ToolResolve = resolveTools
	agent.SetOverrides(o)
}

// dropToolByName 把工具从工具面移除（原地复用底层数组，不额外分配）。可变参数：
// 同一个开关控制的工具要一起消失，分两次调用既啰嗦又容易漏。
func dropToolByName(tools []actool.CoreTool, names ...string) []actool.CoreTool {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	kept := tools[:0]
	for _, t := range tools {
		if !drop[t.Name()] {
			kept = append(kept, t)
		}
	}
	return kept
}

func contains(ss []string, v string) bool {
	for _, s := range ss {
		if s == v {
			return true
		}
	}
	return false
}

// buildDomainReg builds a name→CoreTool registry from a server-level ToolSet
// (real AssetStore, nil ExplorationStore, taskID=0). Used by ToolResolve to inject
// domain tools into agents (Auto, custom) that don't own a per-task ToolSet.
// nil as → returns nil (no injection, graceful degradation).
//
// The nil ExplorationStore is deliberate — these instances are task-less by
// construction — so every tool here must tolerate it. Asset/company tools do
// (they only need the AssetStore); the exploration-graph tools refuse with a
// clear message via ToolSet.needExploration. Binding one of them to a task-less
// agent in the tools table is therefore a useless tool, not a crash.
// buildTaskDomainReg builds the domain registry bound to one task's exploration
// store, so triggered agent runs write back into that task's graph.
func buildTaskDomainReg(pg *db.DB, explorationID, taskID int64) map[string]actool.CoreTool {
	ts := agent.NewToolSet(pg.Exploration(explorationID), "")
	ts.SetTaskID(taskID)
	ts.SetAssetStore(pg.Assets(), pg.Assets().Companies())
	reg := make(map[string]actool.CoreTool)
	for _, t := range ts.AllDomainTools() {
		reg[t.Name()] = t
	}
	return reg
}

func buildDomainReg(as *db.AssetStore) map[string]actool.CoreTool {
	if as == nil {
		return nil
	}
	serverTS := agent.NewToolSet(nil, "")
	serverTS.SetAssetStore(as, as.Companies())
	reg := make(map[string]actool.CoreTool)
	for _, t := range serverTS.AllDomainTools() {
		reg[t.Name()] = t
	}
	return reg
}

func jsonStrSlice(raw json.RawMessage) []string {
	var out []string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func jsonStrMap(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}
