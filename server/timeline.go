package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/report"
)

// 作业时间线（P37）：把几路**已经记着**的东西按时间串成一条线。
//
// 它是**交付物**，不是又一个调试视图：客户与蓝队要看的不是平台内部的活动流水，而是"什么时候对
// 什么做了什么、依据是什么、结论是什么"。所以这条线上的每一格都指得回原始存储（节点 / 台账 /
// 审批记录 / 流量证据），而不是把 activity 表原样倒出来。
//
// 数据全部来自已有存储，**不新增表**：时间线是读模型，存一份就等于多一个会与源数据漂移的副本
// （本仓在 TechniqueTally 上已经吃过一次"两处各写一份、迟早报出两个数字"的亏）。
//
// 默认只取**里程碑**（执行/审批/发现/战果/检测验证/系统留言）——一条线上全是"第 N 轮工具调用"
// 的话，客户看不到重点；需要看全部步骤时用 kinds=all（那部分来自 activity 的原始流水）。
const (
	tlKindExecution = "execution" // 长任务执行台账（run_execution）
	tlKindApproval  = "approval"  // 三层拦截的审批决定（规则 / 模型 / 人工）
	tlKindFinding   = "finding"   // 确认漏洞（含已撤回）
	tlKindLoot      = "loot"      // 据点 / 凭据
	tlKindArtifact  = "artifact"  // 遗留物（我们留在目标上的东西）
	tlKindDetection = "detection" // 检测验证（三态）
	tlKindNote      = "note"      // 系统留在时间线上的话（策略拒绝、停机、目标变更…）
	tlKindStep      = "step"      // 原始步骤流水（activity 的 tool_use/round…），只随 kinds=all 出
)

// timelineMilestoneKinds 是默认集合。顺序即文档顺序（导出时按时间重排，与这里无关）。
var timelineMilestoneKinds = []string{
	tlKindExecution, tlKindApproval, tlKindFinding, tlKindLoot, tlKindArtifact, tlKindDetection, tlKindNote,
}

// TimelineEvent 是时间线上的一格。
type TimelineEvent struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// State 是这一格的收场（execution：completed/failed/cancelled…；approval：allowed/denied；
	// detection：triggered/not_triggered/unknown）。空 = 这一格没有收场可言（比如一条笔记）。
	State string `json:"state,omitempty"`
	// StateLabel 是 State 的人话（已检出 / 已撤回 / 已取消…）。**与 State 一起下发**：
	// 交付件与界面都不该各自再翻译一遍——同一种状态在三处叫三个名字，读者会以为是三件事
	// （本仓在 ADRelLabel 上已经立过这个规矩）。
	StateLabel string `json:"state_label,omitempty"`
	// RefType/RefID 指回原始存储，供界面"点进去看证据"。
	RefType string `json:"ref_type,omitempty"` // node | execution | intercept
	RefID   int64  `json:"ref_id,omitempty"`
}

// timelineStateLabel 把一格的状态翻成人话。未收录的状态原样返回（不猜）。
//
// 注意 detection 的 unknown 与 not_triggered **必须各说各的**：把"无从判定"说成"未检出"
// 就是把"日志没留"读成"防御没拦住"，那比不说更危险（同 P31 的口径）。
func timelineStateLabel(kind, state string) string {
	if state == "" {
		return ""
	}
	switch kind {
	case tlKindExecution:
		switch state {
		case "completed":
			return "完成"
		case "failed":
			return "失败"
		case "cancelled":
			return "已取消"
		case "hard_timeout":
			return "硬超时"
		case "orphaned":
			return "无人接管"
		case "running":
			return "进行中"
		case "queued":
			return "排队中"
		}
	case tlKindApproval:
		switch state {
		case "allowed":
			return "放行"
		case "denied":
			return "拒绝"
		case "pending":
			return "待批"
		case "timeout":
			return "超时未决"
		}
	case tlKindFinding:
		switch state {
		case db.FindingPending:
			return "待处理"
		case db.FindingConfirmed:
			return "已确认"
		case db.FindingRetracted:
			return "已撤回"
		case db.FindingRuledOut:
			return "已排除"
		case db.FindingOpenProofGap:
			return "存疑待补证"
		}
	case tlKindLoot:
		switch state {
		case db.StateLootActive:
			return "在手"
		case db.StateLootLost:
			return "已失效"
		}
	case tlKindArtifact:
		// 遗留物的三态**各自成词**：unknown 不能并进 active，那会把"我们没去确认"
		// 说成"我们确认它还留着"（同检测三态的纪律）。
		switch state {
		case db.StateArtifactActive:
			return "未清理"
		case db.StateArtifactRemoved:
			return "已清理"
		case db.StateArtifactUnknown:
			return "未确认"
		}
	case tlKindDetection:
		switch state {
		case db.StateDetectionTriggered:
			return "已检出"
		case db.StateDetectionNotTriggered:
			return "未检出"
		case db.StateDetectionUnknown:
			return "无法判定"
		}
	}
	return state
}

type timelineResponse struct {
	TaskID string          `json:"task_id"`
	From   string          `json:"from,omitempty"`
	To     string          `json:"to,omitempty"`
	Kinds  []string        `json:"kinds"`
	Events []TimelineEvent `json:"events"`
	// Truncated 如实报出被截断的条数——"只显示了一部分"必须让人看得见（同报告里的窗口计数）。
	Truncated int            `json:"truncated,omitempty"`
	Counts    map[string]int `json:"counts"`
	// 分页（界面用；导出仍取完整时间线）。
	Page    int    `json:"page,omitempty"`
	Size    int    `json:"size,omitempty"`
	Total   int    `json:"total,omitempty"`
	Order   string `json:"order,omitempty"`
	HasMore bool   `json:"has_more,omitempty"`
}

// parseTimelineKinds 解析 kinds 参数：空 = 里程碑；all = 里程碑 + 原始步骤。
func parseTimelineKinds(raw string) ([]string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]string{}, timelineMilestoneKinds...), false
	}
	if raw == "all" {
		return append(append([]string{}, timelineMilestoneKinds...), tlKindStep), true
	}
	valid := map[string]bool{}
	for _, k := range timelineMilestoneKinds {
		valid[k] = true
	}
	valid[tlKindStep] = true
	var out []string
	withSteps := false
	for _, part := range strings.Split(raw, ",") {
		k := strings.TrimSpace(part)
		if !valid[k] {
			continue
		}
		if k == tlKindStep {
			withSteps = true
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return append([]string{}, timelineMilestoneKinds...), false
	}
	return out, withSteps
}

// buildTaskTimeline 装配一个任务的时间线。纯读，不写任何东西。
func (s *Server) buildTaskTimeline(t *Task, kinds []string, withSteps bool, limit int) (timelineResponse, error) {
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	if limit <= 0 {
		limit = 300
	}
	var events []TimelineEvent
	tid, _ := strconv.ParseInt(t.ID, 10, 64)

	// ① 长任务台账：进程活过一轮、输出落盘的那类动作。收场带着**为什么**（cancel_note/error），
	//    那正是复盘时要看的东西。
	if want[tlKindExecution] {
		rows, err := s.m.pg.ListExecutionsByTask(t.ID, limit)
		if err != nil {
			return timelineResponse{}, err
		}
		for _, e := range rows {
			at := e.CreatedAt
			if e.StartedAt != nil {
				at = *e.StartedAt
			}
			detail := e.Description
			for _, extra := range []string{e.CancelNote, e.Error} {
				if strings.TrimSpace(extra) != "" {
					detail = strings.TrimSpace(detail + " · " + extra)
				}
			}
			events = append(events, TimelineEvent{
				At: rfc3339(at), Kind: tlKindExecution,
				Title:  "执行：" + clipRunes(strings.TrimSpace(e.Command), 120),
				Detail: detail, State: e.Status, StateLabel: timelineStateLabel(tlKindExecution, e.Status),
				RefType: "execution", RefID: e.ID,
			})
		}
	}

	// ② 审批决定：谁（规则/模型/人）在什么时候放行或拦下了哪一次调用。交付时这是**授权边界
	//    真的生效过**的证据，比任何"我们配了规则"都硬。
	if want[tlKindApproval] {
		rows, err := s.m.pg.Query(`
SELECT id, tool_name, status, COALESCE(reason,''), COALESCE(decision_source,''), created_at, decided_at
FROM intercept_pending WHERE task_id = $1 ORDER BY created_at DESC LIMIT $2`, t.ID, limit)
		if err != nil {
			return timelineResponse{}, err
		}
		for rows.Next() {
			var id int64
			var tool, status, reason, source string
			var createdAt time.Time
			var decidedAt *time.Time
			if err := rows.Scan(&id, &tool, &status, &reason, &source, &createdAt, &decidedAt); err != nil {
				rows.Close()
				return timelineResponse{}, err
			}
			at := createdAt
			if decidedAt != nil {
				at = *decidedAt
			}
			detail := reason
			if source != "" {
				detail = strings.TrimSpace("判定来源：" + source + "。" + detail)
			}
			events = append(events, TimelineEvent{
				At: rfc3339(at), Kind: tlKindApproval,
				Title: "审批：" + tool, Detail: detail, State: status,
				StateLabel: timelineStateLabel(tlKindApproval, status),
				RefType:    "intercept", RefID: id,
			})
		}
		rows.Close()
	}

	// ③ 发现 / ④ 战果 / ⑤ 检测验证：都是探索节点，一次取齐按 kind 分派。
	meta, _ := s.m.pg.FindingMetaByNodeID(tid)
	if want[tlKindFinding] {
		nodes, err := t.Store.ListByKind(db.KindFinding, 1000)
		if err != nil {
			return timelineResponse{}, err
		}
		for _, n := range nodes {
			var p struct {
				Name     string `json:"name"`
				Summary  string `json:"summary"`
				Severity string `json:"severity"`
			}
			_ = json.Unmarshal(n.Payload, &p)
			state := n.State
			var bits []string
			if m, ok := meta[n.ID]; ok {
				if m.Status != "" {
					state = m.Status
				}
				if m.RetractReason != "" {
					bits = append(bits, "撤回理由："+m.RetractReason)
				}
				if m.Confidence != "" {
					bits = append(bits, "置信："+m.Confidence)
				}
			}
			detail := strings.Join(bits, " · ")
			title := nzStr(strings.TrimSpace(p.Name), clipRunes(p.Summary, 120))
			if p.Severity != "" {
				title = "[" + p.Severity + "] " + title
			}
			events = append(events, TimelineEvent{
				At: rfc3339(n.CreatedAt), Kind: tlKindFinding,
				Title: "发现：" + title, Detail: detail, State: state,
				StateLabel: timelineStateLabel(tlKindFinding, state),
				RefType:    "node", RefID: n.ID,
			})
		}
	}
	if want[tlKindLoot] || want[tlKindArtifact] {
		// 遗留物与战果同属"图上的产物"，但**各自的 kind 分开发**：它们的收场词表不一样
		// （在手/已失效 vs 未清理/已清理/未确认），混在一格会逼出一套含糊的通用词。
		for _, kind := range []string{db.KindFoothold, db.KindCredential, db.KindArtifact} {
			tlKind := tlKindLoot
			if kind == db.KindArtifact {
				tlKind = tlKindArtifact
			}
			if !want[tlKind] {
				continue
			}
			nodes, err := t.Store.ListByKind(kind, 1000)
			if err != nil {
				return timelineResponse{}, err
			}
			for _, n := range nodes {
				var p struct {
					Summary   string `json:"summary"`
					Access    string `json:"access"`
					Privilege string `json:"privilege"`
					Kind      string `json:"kind"`
					Username  string `json:"username"`
				}
				_ = json.Unmarshal(n.Payload, &p)
				label := "据点"
				detail := ""
				switch kind {
				case db.KindCredential:
					label = "凭据"
					// 时间线上同样不给明文，指纹才是指认用的东西（见 db.CredentialFingerprint）。
					detail = strings.TrimSpace(strings.Join(nonEmpty(
						report.PrefixIf("账号", p.Username), report.PrefixIf("指纹", db.ParseCredential(n).Fingerprint())), " · "))
				case db.KindArtifact:
					label = "遗留物"
					av := db.ParseArtifact(n)
					detail = strings.TrimSpace(strings.Join(nonEmpty(
						report.PrefixIf("分类", db.ArtifactKindLabel(av.Kind)),
						report.PrefixIf("位置", av.Locator),
						report.PrefixIf("主机", av.Host)), " · "))
				default:
					detail = strings.TrimSpace(strings.Join(nonEmpty(
						report.PrefixIf("通路", p.Access), report.PrefixIf("权限", p.Privilege)), " · "))
				}
				events = append(events, TimelineEvent{
					At: rfc3339(n.CreatedAt), Kind: tlKind,
					Title: label + "：" + clipRunes(p.Summary, 120), Detail: detail, State: n.State,
					StateLabel: timelineStateLabel(tlKind, n.State),
					RefType:    "node", RefID: n.ID,
				})
			}
		}
	}
	if want[tlKindDetection] {
		nodes, err := t.Store.ListByKind(db.KindDetection, 1000)
		if err != nil {
			return timelineResponse{}, err
		}
		for _, n := range nodes {
			var p struct {
				Summary   string `json:"summary"`
				Technique string `json:"technique"`
			}
			_ = json.Unmarshal(n.Payload, &p)
			title := clipRunes(p.Summary, 120)
			if p.Technique != "" {
				title = "[" + p.Technique + "] " + title
			}
			events = append(events, TimelineEvent{
				At: rfc3339(n.CreatedAt), Kind: tlKindDetection,
				Title: "检测验证：" + title, State: n.State,
				StateLabel: timelineStateLabel(tlKindDetection, n.State),
				RefType:    "node", RefID: n.ID,
			})
		}
	}

	// ⑥ 系统留言与原始步骤：都来自 activity。留言（策略拒绝/目标变更/停机…）默认要，
	//    原始步骤（tool_use/round/result…）只在 kinds=all 时要。
	if want[tlKindNote] || withSteps {
		acts, err := t.Store.ActivityListTail(nil, limit)
		if err != nil {
			return timelineResponse{}, err
		}
		for _, a := range acts {
			kind := a.Kind
			switch {
			case timelineNoteActivity(kind):
				if !want[tlKindNote] {
					continue
				}
				events = append(events, TimelineEvent{
					At: rfc3339(a.CreatedAt), Kind: tlKindNote,
					Title: clipRunes(nzStr(a.Summary, kind), 160), Detail: clipRunes(a.Detail, 400),
				})
			case withSteps:
				events = append(events, TimelineEvent{
					At: rfc3339(a.CreatedAt), Kind: tlKindStep,
					Title: clipRunes(nzStr(a.Summary, nzStr(a.Tool, kind)), 160),
					State: map[bool]string{true: "error", false: ""}[a.IsError],
				})
			}
		}
	}

	// 截断取**最新**的一段（与全站列表口径一致：默认看最近的），截掉的是更早的那些，
	// 所以导出里的那句"另有 N 条更早的事件未列入"是真的。截完再按时间正序排——时间线
	// 读起来必须是从早到晚，而选择哪一段是另一件事。
	sort.SliceStable(events, func(i, j int) bool { return events[i].At > events[j].At })

	counts := map[string]int{}
	for _, e := range events {
		counts[e.Kind]++
	}
	// 每个源取数都有上限（activity/执行/审批取 limit，节点类取 1000）：只数取回的事件，
	// 报出去的就是**窗口长度**而不是总数。里程碑各档逐项用 COUNT(*) 覆盖。
	if want[tlKindExecution] {
		if n, err := s.m.pg.CountExecutionsByTask(t.ID); err == nil {
			counts[tlKindExecution] = n
		}
	}
	if want[tlKindApproval] {
		var n int
		if err := s.m.pg.QueryRow(`SELECT COUNT(*) FROM intercept_pending WHERE task_id=$1`, t.ID).Scan(&n); err == nil {
			counts[tlKindApproval] = n
		}
	}
	if want[tlKindFinding] {
		if n, err := t.Store.CountByKind(db.KindFinding); err == nil {
			counts[tlKindFinding] = n
		}
	}
	if want[tlKindLoot] {
		fh, e1 := t.Store.CountByKind(db.KindFoothold)
		cr, e2 := t.Store.CountByKind(db.KindCredential)
		if e1 == nil && e2 == nil {
			counts[tlKindLoot] = fh + cr
		}
	}
	if want[tlKindArtifact] {
		if n, err := t.Store.CountByKind(db.KindArtifact); err == nil {
			counts[tlKindArtifact] = n
		}
	}
	if want[tlKindDetection] {
		if n, err := t.Store.CountByKind(db.KindDetection); err == nil {
			counts[tlKindDetection] = n
		}
	}
	if want[tlKindNote] || withSteps {
		if notes, err := t.Store.CountActivityKinds(timelineNoteKinds); err == nil {
			if want[tlKindNote] {
				counts[tlKindNote] = notes
			}
			if withSteps {
				// step = activity 总数 − 留言数；两者都来自 COUNT(*)，不是窗口长度。
				if total, err := t.Store.CountActivity(); err == nil && total >= notes {
					counts[tlKindStep] = total - notes
				}
			}
		}
	}
	resp := timelineResponse{TaskID: t.ID, Kinds: kinds, Counts: counts}
	// 截断数也按**总数**算：之前用取回的事件数减 limit，源上限把更早的事件提前丢掉时
	// 就会少报"另有 N 条未列入"。
	totalEvents := 0
	for _, k := range kinds {
		totalEvents += counts[k]
	}
	listed := len(events)
	if listed > limit {
		listed = limit
	}
	if totalEvents > listed {
		resp.Truncated = totalEvents - listed
	}
	if len(events) > limit {
		events = events[:limit]
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At < events[j].At })
	if len(events) > 0 {
		resp.From, resp.To = events[0].At, events[len(events)-1].At
	}
	resp.Events = events
	return resp, nil
}

// timelineNoteKinds 是「系统留言」认的 activity kind 白名单。它同时用于逐条判定与
// COUNT(*) 汇总——两处共用一份清单，就不会出现「数出来 3 条、列出来 5 条」。
var timelineNoteKinds = []string{
	"text", "system", "hint", "policy_reject", "goal", "goal_edited", "goal_deleted",
	"llm_switch", "intel_query", "keyword",
}

// timelineNoteActivity 判一条 activity 该不该进"系统留言"这一档。
//
// 取的是**人话性质的记录**（系统说明、策略拒绝、目标变更、模型切换、情报查询…），
// 排除 LLM 的原始步骤（tool_use/tool_result/round/result/usage）——那些是过程，
// 要看就显式 kinds=all。判断按 kind 白名单而不是黑名单：将来新增的 activity kind
// 默认不进时间线（时间线是交付物，多一格都要有理由）。
func timelineNoteActivity(kind string) bool {
	for _, k := range timelineNoteKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func nonEmpty(items ...string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------- HTTP ----------

func (s *Server) taskTimeline(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "no active task")
		return
	}
	kinds, withSteps := parseTimelineKinds(r.URL.Query().Get("kinds"))
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	size := atoiDefault(q.Get("size"), 50)
	if q.Get("size") == "" {
		// 兼容旧调用：以前只认 limit。
		if n := atoiDefault(q.Get("limit"), 0); n > 0 {
			size = n
		}
	}
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	order := strings.ToLower(strings.TrimSpace(q.Get("order")))
	if order != "asc" {
		order = "desc" // 默认最新在前
	}
	// 各源取数上限 = 目标页之前的所有事件。page 越深取越多，封顶避免一次拉爆。
	fetchLimit := page * size
	if fetchLimit > 5000 {
		fetchLimit = 5000
	}
	resp, err := s.buildTaskTimeline(t, kinds, withSteps, fetchLimit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// buildTaskTimeline 返回的是“最新 fetchLimit 条、按时间正序”。这里按请求的
	// order 排好，再切出当前页；counts/total 来自 COUNT(*)，不是窗口长度。
	total := 0
	for _, k := range kinds {
		total += resp.Counts[k]
	}
	events := resp.Events
	if order == "desc" {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	start := (page - 1) * size
	if start > len(events) {
		start = len(events)
	}
	end := start + size
	if end > len(events) {
		end = len(events)
	}
	pageEvents := events[start:end]
	resp.Events = pageEvents
	resp.Page, resp.Size, resp.Total, resp.Order = page, size, total, order
	resp.HasMore = end < total
	if len(pageEvents) == 0 {
		resp.From, resp.To = "", ""
	} else if order == "desc" {
		resp.From, resp.To = pageEvents[len(pageEvents)-1].At, pageEvents[0].At
	} else {
		resp.From, resp.To = pageEvents[0].At, pageEvents[len(pageEvents)-1].At
	}
	if total > end {
		resp.Truncated = total - end
	} else {
		resp.Truncated = 0
	}
	writeJSON(w, 200, resp)
}

// taskTimelineExport 把同一条时间线导成交付件（markdown，直接下载）。
//
// 与页面上看到的是**同一份装配**（buildTaskTimeline）：导出与界面各算一遍，迟早会导出
// 一份和界面对不上的东西——而交付件是要发给客户的。
func (s *Server) taskTimelineExport(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeErr(w, 404, "no active task")
		return
	}
	kinds, withSteps := parseTimelineKinds(r.URL.Query().Get("kinds"))
	resp, err := s.buildTaskTimeline(t, kinds, withSteps, 2000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	md := renderTimelineMarkdown(t, resp)
	name := fmt.Sprintf("timeline-task-%s.md", t.ID)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(md))
}

func renderTimelineMarkdown(t *Task, resp timelineResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 作业时间线：%s\n\n", t.Description)
	fmt.Fprintf(&b, "目标：%s\n\n", t.Goal)
	if resp.From != "" {
		fmt.Fprintf(&b, "时间范围：%s → %s（共 %d 条）\n\n", resp.From, resp.To, len(resp.Events))
	} else {
		b.WriteString("_本次作业没有可列入时间线的事件。_\n\n")
		return b.String()
	}
	if resp.Truncated > 0 {
		fmt.Fprintf(&b, "> 另有 %d 条更早的事件未列入（导出上限）。\n\n", resp.Truncated)
	}
	b.WriteString("| 时间 | 类别 | 事件 | 收场 | 依据 |\n|---|---|---|---|---|\n")
	for _, e := range resp.Events {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			shortTime(e.At), timelineKindLabel(e.Kind), mdCell(e.Title), mdCell(e.StateLabel), mdCell(e.Detail))
	}
	b.WriteString("\n_时间线上每一格都指得回原始记录（执行台账 / 审批记录 / 探索节点）。_\n")
	return b.String()
}

func timelineKindLabel(kind string) string {
	switch kind {
	case tlKindExecution:
		return "执行"
	case tlKindApproval:
		return "审批"
	case tlKindFinding:
		return "发现"
	case tlKindLoot:
		return "战果"
	case tlKindArtifact:
		return "遗留物"
	case tlKindDetection:
		return "检测验证"
	case tlKindNote:
		return "系统"
	case tlKindStep:
		return "步骤"
	}
	return kind
}

// mdCell 让一格内容在 markdown 表格里不破格（竖线与换行都要处理，否则表格会散架）。
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func shortTime(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Format("2006-01-02 15:04:05")
	}
	return ts
}
