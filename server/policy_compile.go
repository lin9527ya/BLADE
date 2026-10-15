package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/artex/db"
	"github.com/lin9527ya/artex/intercept"
	"github.com/lin9527ya/artex/policy"
)

// P20 探索策略的服务端面：
//
//	GET/PATCH/DELETE /api/tasks/{id}/policy    读/改/清空任务策略（改完即重建命令门）
//	POST  /api/tasks/{id}/policy/preview       编译预览（只读，拿本任务真实调用当样本）
//	POST  /api/tasks/{id}/policy/prune         按新策略清理"未领取"的意图
//	PUT   /api/settings/exploration_policy     全局默认（新任务继承）
//
// 三层执行点（意图门 / 提示词段 / 命令门）共用同一个 policy.Effective；任务落库时就把
// 「全局默认 + 任务覆盖」解析成快照写进 tasks.exploration_policy —— 执行点永不回头读
// 全局设置，于是"改全局默认"与"运行中的任务"之间不存在竞态。

// settingExplorationPolicy 是全局默认策略的 settings key。
const settingExplorationPolicy = "exploration_policy"

// 策略停摆判定（反空转）：窗口内被拒次数达到阈值且期间没有任何成功创建的意图 →
// 认定"策略内已无可行方向"，让任务体面收尾（复用超时收尾时序 + policy_blocked 终态）。
// 参数用 var 而非 const：验收要在测试里把窗口调小，驱动与生产完全同一条判定路径
// （直接测一个"只有测试才走"的副本等于没测）。
var (
	policyStallWindow     = 10 * time.Minute
	policyStallMinRejects = 3
	policyStallGrace      = 2 * time.Minute
	// 判定节流：结论依赖的是分钟级窗口，没必要跟着 2s 的截止时间轮询每次都查库
	// （无策略的任务只需要这一次 GetTask 就返回 false）。
	policyStallCheckInterval = 60 * time.Second
)

// globalPolicy 读全局默认（含版本号）；解析失败回退出厂默认。
func (s *Server) globalPolicy() (policy.Effective, int) {
	v, _, _ := s.m.pg.GetSetting(settingExplorationPolicy)
	return policy.ParseGlobalDefault(v)
}

// taskPolicyByID 读一个任务的生效策略（nil = 无策略）。命令门编译与执行点都用它。
func (s *Server) taskPolicyByID(taskID int64) *policy.Effective {
	t, err := s.m.pg.GetTask(taskID)
	if err != nil || t == nil {
		return nil
	}
	doc := policy.ParseDoc(t.ExplorationPolicy)
	if doc == nil {
		return nil
	}
	eff := doc.Effective
	return &eff
}

// newTaskPolicy 在任务创建时解析策略快照：nil override = 完全采纳全局默认（含出厂
// 默认；若全局默认被显式清空则无策略）；给了 override 就按 Merge 语义覆盖（显式空数组 =
// 本任务不禁该项）。解析不出来（未知条目）返回错误，创建任务随之 400。
func (s *Server) newTaskPolicy(override *policy.Override) (json.RawMessage, error) {
	global, gv := s.globalPolicy()
	ov := policy.Override{}
	if override != nil {
		ov = *override
	}
	eff := policy.Merge(global, ov)
	if verr := policy.Validate(eff); verr != nil {
		return nil, fmt.Errorf("探索策略无效：%w", verr)
	}
	if eff.IsZero() {
		return nil, nil
	}
	if ov.IsZero() {
		return policy.MarshalSnapshot(gv, eff) // 纯继承：UI 显示"继承自全局默认"
	}
	return policy.MarshalDoc(gv, ov, eff)
}

// getExplorationPolicyDefault 读全局默认策略（设置页要显示"当前默认禁了什么"）。
func (s *Server) getExplorationPolicyDefault(w http.ResponseWriter, r *http.Request) {
	eff, ver := s.globalPolicy()
	var ov policy.Override
	if raw, ok, err := s.m.pg.GetSetting(settingExplorationPolicy); err == nil && ok && raw != "" {
		var stored struct {
			Override policy.Override `json:"override"`
		}
		if json.Unmarshal([]byte(raw), &stored) == nil {
			ov = stored.Override
		}
	}
	writeJSON(w, 200, map[string]any{"effective": eff, "version": ver, "override": ov,
		"techniques": policy.Techniques, "writes": policy.WriteItems,
		"custom_limit": policy.MaxCustomRules, "custom_pattern_limit": policy.MaxCustomPatternLen})
}

// putExplorationPolicyDefault 保存全局默认策略（新任务继承；老任务不回溯）。
// 落库形状 {"version":N,"override":{…},"effective":{…}}，并把上一版原样备份到
// exploration_policy_prev（改坏了能对照/回滚）。
func (s *Server) putExplorationPolicyDefault(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Override policy.Override `json:"override"`
		Reset    bool            `json:"reset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	_, prevVer := s.globalPolicy()
	base := policy.Default()
	eff := policy.Merge(base, body.Override)
	if !body.Reset {
		if verr := policy.Validate(eff); verr != nil {
			writeErr(w, 400, verr.Error())
			return
		}
	} else {
		eff = base
	}
	if old, ok, err := s.m.pg.GetSetting(settingExplorationPolicy); err == nil && ok && old != "" {
		_ = s.m.pg.SetSetting(settingExplorationPolicy+"_prev", old)
	}
	eff.Version = prevVer + 1
	raw, err := json.Marshal(map[string]any{"version": eff.Version, "override": body.Override, "effective": eff})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := s.m.pg.SetSetting(settingExplorationPolicy, string(raw)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"effective": eff, "version": eff.Version,
		"note": "全局默认已更新：只影响之后新建的任务；已存在的任务各自持有快照，不会被回溯。"})
}

// policyResolver 给 agent 用的解析器（每个 run 现读 → 改策略下一轮生效）。
func (s *Server) policyResolver(taskID int64) func() *policy.Effective {
	return func() *policy.Effective { return s.taskPolicyByID(taskID) }
}

// applyTaskPolicyRules 把任务的策略编译成任务级拦截规则（命令门）。
// 事务内"删旧插新"：失败则旧规则原样保留（fail-closed），提交后才失效规则缓存。
func (s *Server) applyTaskPolicyRules(t *Task) (int, error) {
	eff := s.taskPolicyByID(parseIntDefault(t.ID))
	var rules []db.InterceptRule
	if eff != nil {
		for _, d := range eff.CompileRules() {
			rules = append(rules, db.InterceptRule{
				Name: d.Name, Enabled: true, Priority: d.Priority,
				MatchTarget: d.MatchTarget, MatchType: d.MatchType, Pattern: d.Pattern,
				Action: d.Action, Message: d.Message,
				TimeoutEnabled: false, TimeoutSeconds: 0, TimeoutAction: "deny",
				TaskID: t.ID, Source: db.RuleSourcePolicy,
			})
		}
	}
	n, err := s.m.pg.ReplaceTaskRulesBySource(t.ID, db.RuleSourcePolicy, rules)
	if err != nil {
		return 0, err
	}
	s.m.interceptor.Invalidate()
	return n, nil
}

// previewTaskPolicyRules 只读预览：策略会生成哪些规则、每条会命中最近多少条真实调用。
func (s *Server) previewTaskPolicyRules(t *Task) ([]policy.RuleDraft, []compilePreview) {
	eff := s.taskPolicyByID(parseIntDefault(t.ID))
	if eff == nil {
		return nil, nil
	}
	drafts := eff.CompileRules()
	var previews []compilePreview
	if t.Store != nil {
		samples, err := t.Store.RecentToolUseInputs(100)
		if err == nil && len(samples) > 0 {
			for _, d := range drafts {
				p := compilePreview{Pattern: d.Pattern, Total: len(samples), Samples: []string{}}
				for _, sm := range samples {
					matched, text, _, _, err := intercept.PreviewRule(sm.Tool, []byte(sm.Input),
						d.MatchTarget, d.MatchType, d.Pattern, d.Action, d.Name, d.Message)
					if err != nil || !matched {
						continue
					}
					p.Matched++
					if len(p.Samples) < 3 {
						p.Samples = append(p.Samples, clipRunesLocal(text, 120))
					}
				}
				previews = append(previews, p)
			}
		}
	}
	return drafts, previews
}

// parseIntDefault 解析任务 id（坏 id → 0，调用方按"取不到策略"处理）。
func parseIntDefault(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// policyDiffText 生成人话 diff（进审计活动）。
func policyDiffText(old, new policy.Effective) string {
	var parts []string
	if diff := stringSetDiff(old.Techniques, new.Techniques); diff != "" {
		parts = append(parts, "禁用手段 "+diff)
	}
	if diff := stringSetDiff(old.ForbiddenWrites, new.ForbiddenWrites); diff != "" {
		parts = append(parts, "敏感写 "+diff)
	}
	if diff := customRulesDiff(old.Custom, new.Custom); diff != "" {
		parts = append(parts, "自定义条目 "+diff)
	}
	if old.MaxIntentsPerRound != new.MaxIntentsPerRound {
		parts = append(parts, fmt.Sprintf("每轮意图上限 %d→%d", old.MaxIntentsPerRound, new.MaxIntentsPerRound))
	}
	if old.MaxOpenIntents != new.MaxOpenIntents {
		parts = append(parts, fmt.Sprintf("在途意图上限 %d→%d", old.MaxOpenIntents, new.MaxOpenIntents))
	}
	if old.MaxIntentsTotal != new.MaxIntentsTotal {
		parts = append(parts, fmt.Sprintf("意图总上限 %d→%d", old.MaxIntentsTotal, new.MaxIntentsTotal))
	}
	if len(parts) == 0 {
		return "（无变化）"
	}
	return strings.Join(parts, "；")
}

// customRulesDiff 生成自定义条目的人话 diff（按 显示名/layer 认条目）。
func customRulesDiff(old, new []policy.CustomRule) string {
	key := func(r policy.CustomRule) string { return r.Display() + "|" + r.Layer }
	o, n := map[string]bool{}, map[string]bool{}
	var order []string
	for _, r := range old {
		k := key(r)
		if !o[k] {
			o[k] = true
			order = append(order, k)
		}
	}
	for _, r := range new {
		k := key(r)
		if !n[k] {
			n[k] = true
			if !o[k] {
				order = append(order, k)
			}
		}
	}
	var added, removed []string
	for _, k := range order {
		switch {
		case n[k] && !o[k]:
			added = append(added, strings.SplitN(k, "|", 2)[0])
		case o[k] && !n[k]:
			removed = append(removed, strings.SplitN(k, "|", 2)[0])
		}
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "新增 "+strings.Join(added, "、"))
	}
	if len(removed) > 0 {
		parts = append(parts, "移除 "+strings.Join(removed, "、"))
	}
	return strings.Join(parts, "，")
}

func stringSetDiff(old, new []string) string {
	o, n := map[string]bool{}, map[string]bool{}
	for _, v := range old {
		o[v] = true
	}
	for _, v := range new {
		n[v] = true
	}
	var added, removed []string
	for v := range n {
		if !o[v] {
			added = append(added, v)
		}
	}
	for v := range o {
		if !n[v] {
			removed = append(removed, v)
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "新增 "+strings.Join(policy.TechniqueLabels(added), "/")+strings.Join(policy.WriteLabels(added), "/"))
	}
	if len(removed) > 0 {
		parts = append(parts, "移除 "+strings.Join(policy.TechniqueLabels(removed), "/")+strings.Join(policy.WriteLabels(removed), "/"))
	}
	return strings.Join(parts, "，")
}

// --- HTTP handlers ---

type policyDTO struct {
	// Effective 是生效策略（提示词/意图门/命令门读的就是它）。
	Effective policy.Effective `json:"effective"`
	// Override 是本任务的覆盖层（UI 编辑这一层）。
	Override policy.Override `json:"override"`
	// Global 是当前全局默认（UI 展示"继承自哪一版/哪几条"）。
	Global        policy.Effective `json:"global"`
	GlobalVersion int              `json:"global_version"`
	// Custom 表示本任务是显式配过策略的（false = 无策略/完全继承）。
	Custom bool `json:"custom"`
	// IntentsUsed / IntentsTotal 是额度可视化的分子分母。
	IntentsUsed  int `json:"intents_used"`
	IntentsTotal int `json:"intents_total"`
	// RuleCount 是命令门当前实际落库的规则数（含为 0 的自愈判断）。
	RuleCount int `json:"rule_count"`
	// CustomLimit 是自定义禁止条目的条数上限（UI 据此禁用"添加"）。
	CustomLimit int `json:"custom_limit"`
	// RecentRejects 是最近的策略拒绝摘要（供策略卡显示"模型想做什么、被拦了什么"）。
	RecentRejects []policy.RejectNote `json:"recent_rejects"`
}

func (s *Server) getTaskPolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	writeJSON(w, 200, s.buildPolicyDTO(t))
}

func (s *Server) buildPolicyDTO(t *Task) policyDTO {
	global, gv := s.globalPolicy()
	dto := policyDTO{Global: global, GlobalVersion: gv}
	// 一律以库里的行为准（Task 上的 ExplorationPolicy 只在创建时写一次；策略改动后
	// 内存副本会陈旧，而且在并发请求里读写同一个字段本身就是数据竞争）。
	if pgTask, err := s.m.pg.GetTask(parseIntDefault(t.ID)); err == nil && pgTask != nil {
		if doc := policy.ParseDoc(pgTask.ExplorationPolicy); doc != nil {
			// Custom = 本任务自己配过（覆盖层非空）。创建时抄的全局默认快照不算"自定义"，
			// 否则 UI 会把"继承"显示成"已改"，用户就不知道该去哪一层改。
			dto.Custom = !doc.Override.IsZero()
			dto.Override = doc.Override
			dto.Effective = doc.Effective
		}
	}
	if n, err := s.m.pg.CountTaskRulesBySource(t.ID, db.RuleSourcePolicy); err == nil {
		dto.RuleCount = n
	}
	if t.Store != nil {
		if used, err := t.Store.CountIntents(); err == nil {
			dto.IntentsUsed = used
		}
		if rows, err := t.Store.RecentPolicyRejects(10); err == nil {
			for _, row := range rows {
				var meta struct {
					IntentSummary string `json:"intent_summary"`
					ItemLabel     string `json:"item_label"`
					PolicyItem    string `json:"policy_item"`
					Matched       string `json:"matched"`
					Benign        bool   `json:"benign"`
				}
				if len(row.Metadata) > 0 {
					_ = json.Unmarshal(row.Metadata, &meta)
				}
				dto.RecentRejects = append(dto.RecentRejects, policy.RejectNote{
					Summary: meta.IntentSummary, ItemLabel: meta.ItemLabel, ItemID: meta.PolicyItem, Matched: meta.Matched,
				})
			}
		}
	}
	dto.IntentsTotal = dto.Effective.MaxIntentsTotal
	dto.CustomLimit = policy.MaxCustomRules
	return dto
}

// patchTaskPolicy 保存任务策略：override 层 → 解析成 effective 快照落库 → 重建命令门。
// 不回溯已存在的意图（它们已在跑/已跑完）；要清理用 /policy/prune。
func (s *Server) patchTaskPolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法修改策略")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Override policy.Override `json:"override"`
		// Reset=true 清空覆盖层（回到"无策略"）。
		Reset bool `json:"reset"`
	}
	if r.Method == http.MethodDelete {
		// DELETE /policy = 清空覆盖（无 body 的语义化写法）；坏 body 不该被当成"清空"，所以
		// DELETE 干脆不解析 body。
		body.Reset = true
	} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		// PATCH 必须带合法 JSON：把坏请求当"空覆盖"会悄悄把自定义策略改成继承全局默认。
		writeErr(w, 400, "invalid JSON")
		return
	}
	global, gv := s.globalPolicy()
	oldEff := policy.Effective{}
	if eff := s.taskPolicyByID(parseIntDefault(t.ID)); eff != nil {
		oldEff = *eff
	}
	if body.Reset {
		if err := s.m.pg.UpdateTaskExplorationPolicy(parseIntDefault(t.ID), nil); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if _, err := s.applyTaskPolicyRules(t); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		s.taskActivity(t, "policy_update", "探索策略已清空（回到无策略）", "")
		writeJSON(w, 200, s.buildPolicyDTO(t))
		return
	}
	eff := policy.Merge(global, body.Override)
	if verr := policy.Validate(eff); verr != nil {
		writeErr(w, 400, verr.Error())
		return
	}
	doc, err := policy.MarshalDoc(gv, body.Override, eff)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.m.pg.UpdateTaskExplorationPolicy(parseIntDefault(t.ID), doc); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	n, err := s.applyTaskPolicyRules(t)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.taskActivity(t, "policy_update",
		"探索策略已更新（改动下一轮规划生效；已存在的意图不回溯）",
		fmt.Sprintf("%s；命令门规则 %d 条", policyDiffText(oldEff, eff), n))
	writeJSON(w, 200, s.buildPolicyDTO(t))
}

// previewTaskPolicy 只读预览：规则草案 + 对本任务历史调用的命中预览。
func (s *Server) previewTaskPolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	drafts, previews := s.previewTaskPolicyRules(t)
	writeJSON(w, 200, map[string]any{"rules": drafts, "preview": previews})
}

// pruneTaskPolicy 按当前策略清理【未领取(open)】的意图：命中的标 stopped 并写明理由。
// 运行中的意图不动（它已经在跑，由命令门硬拦，人工可用 kill/steer 处置）。
func (s *Server) pruneTaskPolicy(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	eff := s.taskPolicyByID(parseIntDefault(t.ID))
	if eff == nil || eff.IsZero() {
		writeJSON(w, 200, map[string]any{"stopped": 0, "note": "本任务没有探索策略，无需清理"})
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法清理")
		return
	}
	defer s.engine.decInflight(t.ID)

	open, err := t.Store.ListByKind(db.KindIntent, 500)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	stopped := 0
	var details []string
	for _, n := range open {
		if n.State != "open" {
			continue
		}
		var p struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		v := eff.CheckIntent(policy.IntentInput{Summary: p.Summary})
		if v.Allow {
			continue
		}
		reason := fmt.Sprintf("新探索策略禁止：%s（%s）", v.ItemLabel, v.ItemID)
		if _, err := t.Store.SoftDeleteIntent(n.ID, reason); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		stopped++
		details = append(details, fmt.Sprintf("#%d「%s」", n.ID, clipRunesLocal(p.Summary, 40)))
	}
	if stopped > 0 {
		s.taskActivity(t, "policy_update",
			fmt.Sprintf("按新策略清理了 %d 条未领取的意图", stopped), strings.Join(details, "、"))
	}
	writeJSON(w, 200, map[string]any{"stopped": stopped})
}

// taskActivity 落一条任务级活动（策略改动/清理的审计面）。
func (s *Server) taskActivity(t *Task, kind, summary, detail string) {
	if _, err := t.Store.AppendActivity(db.Activity{Kind: kind, Worker: "user", Summary: summary, Detail: detail}); err != nil {
		// 审计写失败不该让功能失败（与预算提示同款：先落痕、失败只记日志）。
		_ = err
	}
	s.engine.touch(t.ID)
}
