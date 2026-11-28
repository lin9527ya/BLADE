package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lin9527ya/Norma/permission"
	"github.com/lin9527ya/Norma/skill"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/mcphttp"
	"github.com/lin9527ya/blade/toolpolicy"
	"github.com/lin9527ya/blade/traffic"
)

type cachedMCPToolDef struct {
	name        string
	description string
	schema      map[string]any
}

const mcpSchemaCacheTTL = 5 * time.Minute

var mcpConfigEpoch atomic.Int64

func bumpMCPConfigEpoch() int64 {
	return mcpConfigEpoch.Add(1)
}

// InvalidateMCPSchemaCache evicts in-memory cached MCP schemas for serverID.
func InvalidateMCPSchemaCache(serverID int64) {
	prefix := fmt.Sprintf("%d|", serverID)
	mcpSchemaCache.mu.Lock()
	for k := range mcpSchemaCache.entries {
		if serverID <= 0 || strings.HasPrefix(k, prefix) {
			delete(mcpSchemaCache.entries, k)
		}
	}
	mcpSchemaCache.mu.Unlock()
}

var mcpSchemaCache = struct {
	mu      sync.Mutex
	entries map[string]mcpSchemaCacheEntry
}{entries: map[string]mcpSchemaCacheEntry{}}

type mcpSchemaCacheEntry struct {
	expires time.Time
	defs    []cachedMCPToolDef
}

func mcpServerCacheKey(m *db.MCPServer) string {
	return fmt.Sprintf("%d|%s|%s|%s|%s|%s|%t|%s",
		m.ID, m.Transport, m.Command, string(m.Args), string(m.Env), m.URL, m.Insecure, strings.Join(m.Tools, ","))
}

// lazyMCPServerSession defers dialing an external MCP server until a run
// actually invokes one of its tools, letting schema assembly stay zero-spawn
// and deterministic when schemas are already cached in DB or memory.
type lazyMCPServerSession struct {
	runCtx context.Context
	srv    *db.MCPServer
	dialMu sync.Mutex
	mu     sync.Mutex
	client mcpClient
	byName map[string]actool.CoreTool
}

func (s *lazyMCPServerSession) currentTarget(toolName string) (mcpClient, actool.CoreTool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil || s.byName == nil {
		return nil, nil
	}
	if ac, ok := s.client.(mcpAliveChecker); ok && !ac.Alive() {
		return nil, nil
	}
	return s.client, s.byName[toolName]
}

func (s *lazyMCPServerSession) resetIfSame(old mcpClient) {
	s.mu.Lock()
	if old == nil || s.client == old {
		prev := s.client
		s.client = nil
		s.byName = nil
		s.mu.Unlock()
		if prev != nil {
			_ = prev.Close()
		}
		return
	}
	s.mu.Unlock()
}

func (s *lazyMCPServerSession) ensureTarget(ctx context.Context, toolName string) (mcpClient, actool.CoreTool, error) {
	if cl, tgt := s.currentTarget(toolName); cl != nil {
		return cl, tgt, nil
	}
	s.dialMu.Lock()
	defer s.dialMu.Unlock()
	if cl, tgt := s.currentTarget(toolName); cl != nil {
		return cl, tgt, nil
	}
	// Evict any dead client before opening a fresh connection.
	s.mu.Lock()
	dead := s.client
	s.client = nil
	s.byName = nil
	s.mu.Unlock()
	if dead != nil {
		_ = dead.Close()
	}

	dialCtx := ctx
	if dialCtx == nil || dialCtx.Err() != nil {
		dialCtx = s.runCtx
	}
	if dialCtx == nil {
		dialCtx = context.Background()
	}
	boundedCtx, cancel := withBoundedDialTimeout(dialCtx, stdioDialTimeout)
	defer cancel()

	cl, err := connectMCP(boundedCtx, s.srv)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp %s 连接失败: %w", s.srv.Name, err)
	}
	ts, err := cl.Tools(boundedCtx)
	if err != nil {
		_ = cl.Close()
		return nil, nil, fmt.Errorf("mcp %s tools/list 失败: %w", s.srv.Name, err)
	}
	byName := make(map[string]actool.CoreTool, len(ts))
	for _, t := range ts {
		byName[t.Name()] = t
	}
	s.mu.Lock()
	s.client = cl
	s.byName = byName
	tgt := byName[toolName]
	s.mu.Unlock()
	return cl, tgt, nil
}

func resultErrorText(res actool.Result, err error) string {
	if err != nil {
		return err.Error()
	}
	if !res.IsError {
		return ""
	}
	var b strings.Builder
	for _, blk := range res.Content {
		if blk.Text != "" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

func isMCPTransportBroken(ctx context.Context, res actool.Result, err error) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if err != nil && (errors.Is(err, mcphttp.ErrSessionExpired) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		return true
	}
	msg := resultErrorText(res, err)
	if msg == "" || strings.Contains(msg, "熔断冷却") || strings.Contains(msg, "mcp rpc error") {
		return false
	}
	low := strings.ToLower(msg)
	for _, needle := range []string{
		strings.ToLower(mcphttp.ErrSessionExpired.Error()),
		"connection closed",
		"mcp sse stream",
		"broken pipe",
		"process already finished",
		"no matching response in event stream",
		"use of closed network connection",
		": eof",
	} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

func (s *lazyMCPServerSession) call(ctx context.Context, toolName string, args json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	for attempt := 0; attempt < 2; attempt++ {
		cl, target, err := s.ensureTarget(ctx, toolName)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		if target == nil {
			return actool.Errorf(fmt.Sprintf("mcp tool %s not found on %s", toolName, s.srv.Name)), nil
		}
		res, callErr := target.Call(ctx, args, tc)
		if attempt == 0 && isMCPTransportBroken(ctx, res, callErr) {
			s.resetIfSame(cl)
			continue
		}
		return res, callErr
	}
	return actool.Errorf(fmt.Sprintf("mcp %s 重连后仍失败", s.srv.Name)), nil
}

func (s *lazyMCPServerSession) Close() error {
	s.mu.Lock()
	cl := s.client
	s.client = nil
	s.byName = nil
	s.mu.Unlock()
	if cl != nil {
		return cl.Close()
	}
	return nil
}

func newLazyMCPTool(def cachedMCPToolDef, session *lazyMCPServerSession) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name:        def.name,
		Description: def.description,
		Schema:      def.schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.AskUser("call MCP tool " + def.name + "?")
		},
		Run: func(ctx context.Context, args json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			return session.call(ctx, def.name, args, tc)
		},
	})
}

func cachedMCPDefsFromDB(pg *db.DB, serverID int64) ([]cachedMCPToolDef, bool) {
	rows, err := pg.MCPToolsDetailed(serverID)
	if err != nil || len(rows) == 0 {
		return nil, false
	}
	defs := make([]cachedMCPToolDef, 0, len(rows))
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		if len(r.Schema) == 0 {
			return nil, false
		}
		var sc map[string]any
		if err := json.Unmarshal(r.Schema, &sc); err != nil || sc == nil {
			return nil, false
		}
		defs = append(defs, cachedMCPToolDef{name: r.Name, description: r.Description, schema: sc})
	}
	if len(defs) == 0 {
		// All cached tools explicitly disabled by the user -> zero enabled tools, no live connect needed.
		return nil, true
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].name < defs[j].name })
	return defs, true
}

func resolveMCPToolDefs(ctx context.Context, pg *db.DB, m *db.MCPServer) ([]actool.CoreTool, io.Closer, error) {
	session := &lazyMCPServerSession{runCtx: ctx, srv: m}
	if defs, ok := cachedMCPDefsFromDB(pg, m.ID); ok {
		tools := make([]actool.CoreTool, 0, len(defs))
		for _, d := range defs {
			tools = append(tools, newLazyMCPTool(d, session))
		}
		return tools, session, nil
	}

	key := mcpServerCacheKey(m)
	now := time.Now()
	mcpSchemaCache.mu.Lock()
	if ent, ok := mcpSchemaCache.entries[key]; ok && now.Before(ent.expires) {
		mcpSchemaCache.mu.Unlock()
		off, _ := pg.MCPDisabledTools(m.ID)
		tools := make([]actool.CoreTool, 0, len(ent.defs))
		for _, d := range ent.defs {
			if off[d.name] {
				continue
			}
			tools = append(tools, newLazyMCPTool(d, session))
		}
		return tools, session, nil
	}
	mcpSchemaCache.mu.Unlock()

	dialCtx, cancel := withBoundedDialTimeout(ctx, stdioDialTimeout)
	defer cancel()
	cl, err := connectMCP(dialCtx, m)
	if err != nil {
		return nil, nil, err
	}
	ts, err := cl.Tools(dialCtx)
	if err != nil {
		_ = cl.Close()
		return nil, nil, fmt.Errorf("tools/list: %w", err)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name() < ts[j].Name() })
	byName := make(map[string]actool.CoreTool, len(ts))
	allDefs := make([]cachedMCPToolDef, 0, len(ts))
	for _, t := range ts {
		byName[t.Name()] = t
		allDefs = append(allDefs, cachedMCPToolDef{
			name:        t.Name(),
			description: t.Description(),
			schema:      t.InputSchema(),
		})
	}
	mcpSchemaCache.mu.Lock()
	mcpSchemaCache.entries[key] = mcpSchemaCacheEntry{expires: time.Now().Add(mcpSchemaCacheTTL), defs: allDefs}
	mcpSchemaCache.mu.Unlock()

	session.client = cl
	session.byName = byName
	off, _ := pg.MCPDisabledTools(m.ID)
	tools := make([]actool.CoreTool, 0, len(allDefs))
	for _, d := range allDefs {
		if off[d.name] {
			continue
		}
		tools = append(tools, newLazyMCPTool(d, session))
	}
	return tools, session, nil
}

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
				lazyTools, cl, err := resolveMCPToolDefs(ctx, pg, m)
				if err != nil {
					log.Printf("[mcp] %s 连接失败: %v", m.Name, err)
					continue
				}
				if cl != nil {
					closers = append(closers, cl)
				}
				for _, t := range lazyTools {
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
			deferredKeys := make([]string, 0, len(deferredBinds))
			for name := range deferredBinds {
				deferredKeys = append(deferredKeys, name)
			}
			sort.Strings(deferredKeys)
			for _, name := range deferredKeys {
				boundAgents := deferredBinds[name]
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
		sort.Strings(allNames)
		sort.Strings(globalNames)

		cleanup := func() {
			for _, c := range closers {
				_ = c.Close()
			}
		}
		def := agent.DeferredInfo{
			Deferred:        allNames,
			GlobalNames:     globalNames,
			Unlock:          unlock,
			UnlockSkill:     unlockSkill,
			AlwaysSkills:    alwaysText,
			CapabilityIndex: renderCapabilityIndex(reg, serverTools),
			SurfaceSalt:     strconv.FormatInt(mcpConfigEpoch.Load(), 10),
		}
		return extra, def, cleanup
	}
	o := agent.CurrentOverrides()
	o.ToolAugment = augment
	agent.SetOverrides(o)
}

// renderCapabilityIndex is a compact, cache-stable table of what this agent can
// reach: visible skills (name + description) and connected MCP servers (name +
// tool count). Full instructions still come from Skill(name); this is only a
// discoverability index so the model doesn't have to guess.
func renderCapabilityIndex(reg *skill.Registry, serverTools map[string][]string) string {
	if reg == nil && len(serverTools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 【能力速查】\n以下能力已对本 agent 可见。技能用 Skill(name) 取完整步骤；MCP 工具可直接调用。\n")
	if reg != nil {
		skills := reg.List()
		sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
		if len(skills) > 0 {
			b.WriteString("技能：\n")
			for _, s := range skills {
				desc := strings.TrimSpace(s.Description)
				if desc == "" {
					desc = strings.TrimSpace(s.WhenToUse)
				}
				if desc == "" {
					desc = "（无描述）"
				}
				b.WriteString("- " + s.Name + "：" + firstSemanticUnit(desc) + "\n")
			}
		}
	}
	if len(serverTools) > 0 {
		names := make([]string, 0, len(serverTools))
		for name := range serverTools {
			names = append(names, name)
		}
		sort.Strings(names)
		b.WriteString("MCP 服务器：\n")
		for _, name := range names {
			fmt.Fprintf(&b, "- %s（%d 个工具）\n", name, len(serverTools[name]))
		}
	}
	return b.String()
}

// firstSemanticUnit extracts the first complete semantic sentence/clause cleanly
// (up to sentence-ending punctuation or whitespace/rune limit) without chopping
// mid-token or breaking ASCII identifiers/paths (aligned with Pojun #87).
func firstSemanticUnit(s string) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if s == "" {
		return ""
	}
	const maxRunes = 220
	runes := []rune(s)
	for i, r := range runes {
		switch r {
		case '。', '！', '？', '；':
			if i > 0 {
				return strings.TrimSpace(string(runes[:i+1]))
			}
		case '.', '!', '?', ';':
			// Only end on ASCII punctuation when followed by whitespace or end-of-string
			// so version numbers (2.0), domains, or file extensions stay intact.
			if i+1 == len(runes) || runes[i+1] == ' ' {
				return strings.TrimSpace(string(runes[:i+1]))
			}
		}
		if i+1 >= maxRunes {
			// Walk back briefly to nearest whitespace if truncating an ASCII run.
			cut := maxRunes
			if runes[cut-1] < 128 {
				for j := cut - 1; j > maxRunes-32 && j > 0; j-- {
					if runes[j] == ' ' {
						cut = j
						break
					}
				}
			}
			return strings.TrimSpace(string(runes[:cut])) + "…"
		}
	}
	return s
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
}

// wireTools seeds the built-in tool catalog (idempotent, first-insert only so page
// edits survive restart) and wires the DB tools table into the agent runtime: at
// tool-assembly time each built-in tool is filtered by its agent binding / enabled
// flag and, if kept, wrapped so the model sees the DB-overridden description/schema
// and缺省入参 get injected. MCP/skill/host tools have no row and pass through.
func wireTools(pg *db.DB, domainReg map[string]actool.CoreTool) {
	bindingEnabled := func() bool { return pg.GetBool(settingAgentTrafficBinding, false) }
	lootExecEnabled := func() bool { return pg.GetBool(settingLootExec, false) }
	taskLootExecEnabled := func(ctx context.Context) bool {
		ri := agent.RunInfoFrom(ctx)
		return ri.TaskID > 0 && pg.TaskLootExecEnabled(ri.TaskID)
	}
	taskMobileBound := func(ctx context.Context) bool {
		ri := agent.RunInfoFrom(ctx)
		return ri.TaskID > 0 && pg.TaskHasMobileDevice(ri.TaskID)
	}
	taskMobileApp := func(ctx context.Context) bool {
		ri := agent.RunInfoFrom(ctx)
		return ri.TaskID > 0 && pg.TaskHasMobileApp(ri.TaskID)
	}
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
			return restrictIMSurfaceTools(ctx, restrictManagementTools(ctx, tools))
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
			return meterTool(agent.DecorateStoredTool(t, row.Description, schema), pg, row.Key, agentKey, runInfo)
		}
		out := tools[:0:0]
		imPrincipal := principalFrom(ctx)
		for _, t := range tools {
			row, known := byKey[t.Name()]
			if !known { // MCP/skill/host tool: no row → untouched
				out = append(out, t)
				continue
			}
			bound := contains(row.Agents, agentKey)
			if !bound && row.Enabled && imPrincipal.Surface == "im" && imPrincipal.Role == "operator" && imSurfaceUnboundTools[t.Name()] {
				bound = true // IM-only task-op tools; do not expand the Web Auto toolset
			}
			if !row.Enabled || !bound {
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
		// Never replace the shared baseline: task-less and concurrent runs must
		// not inherit another task's callable closures.
		runDomainReg := domainReg
		if ri := agent.RunInfoFrom(ctx); ri.TaskID > 0 && ri.ExplorationID > 0 {
			if taskReg := buildTaskDomainReg(pg, ri.ExplorationID, ri.TaskID); len(taskReg) > 0 {
				runDomainReg = taskReg
			}
		}
		if len(runDomainReg) > 0 {
			inList := make(map[string]bool, len(tools))
			for _, t := range tools {
				inList[t.Name()] = true
			}
			for _, row := range rows {
				if row.Kind == "shell" || !row.Enabled || !contains(row.Agents, agentKey) || inList[row.Key] {
					continue
				}
				inst, ok := runDomainReg[row.Key]
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
		if !lootExecEnabled() && !taskLootExecEnabled(ctx) {
			out = dropToolByName(out, toolpolicy.GatedTools("agent_loot_exec")...)
		}
		// OAST 未配置时同样从工具面移除：让模型看见一个永远拒绝的工具只会浪费调用。
		if _, _, on := oastConfig(); !on {
			out = dropToolByName(out, "oast_mint", "oast_poll")
		}
		// 移动设备工具组：仅绑定移动设备时动态开启，不污染常规 Web 任务工具面。
		if !taskMobileBound(ctx) {
			out = dropToolByName(out, toolpolicy.GatedTools("task_mobile_device")...)
		}
		// 移动样本审计与证据校验组：只要上传移动样本或绑定移动设备即放行。
		if !taskMobileApp(ctx) {
			out = dropToolByName(out, toolpolicy.GatedTools("task_mobile_app")...)
		}
		// 已下线的 BLADE 探索约束工具：DB 旧绑定也不允许重新注入任何 agent。
		out = dropRetiredConstraintTools(out)
		// 失败提示：包在最后，覆盖上面所有 DecorateTool 的结果。模型此前只看到
		// `[exit code N]`，没有「下一步该换什么」的线索。
		for i, t := range out {
			if t.Name() == "Bash" {
				out[i] = agent.WithFailureHint(t)
				break
			}
		}
		return restrictIMSurfaceTools(ctx, restrictManagementTools(ctx, out))
	}
	o := agent.CurrentOverrides()
	o.FindingTrafficBindingEnabled = bindingEnabled
	o.LootExecEnabled = lootExecEnabled
	o.LootExecTaskEnabled = func(taskID int64) bool { return pg.TaskLootExecEnabled(taskID) }
	o.OASTConfig = oastConfig
	agent.NucleiOASTServer = func() string {
		if v, ok, err := pg.GetSetting(settingOASTNucleiServer); err == nil && ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return "oast.fun"
	}
	agent.OASTInteractshServers = func() []string {
		primary := agent.NucleiOASTServer()
		out := []string{primary, "oast.fun", "oast.pro", "oast.live", "oast.site", "oast.online"}
		seen := map[string]bool{}
		uniq := out[:0]
		for _, s := range out {
			if s = strings.TrimSpace(s); s != "" && !seen[s] {
				seen[s] = true
				uniq = append(uniq, s)
			}
		}
		return uniq
	}
	// 噪声档位：每轮现读，改完下一轮生效。只认 normal|quiet，其它值按 normal。
	o.NoiseLevel = func() string {
		v, ok, _ := pg.GetSetting(settingOpsecNoise)
		if ok && strings.EqualFold(strings.TrimSpace(v), "quiet") {
			return "quiet"
		}
		return "normal"
	}
	o.EvidenceVerifyModeFn = func() agent.EvidenceVerifyMode {
		return evidenceVerifySetting(pg)
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
