package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/sched"
)

// P22 计划任务：按结构化频率到点自动建任务。
//
// 语义（写进 UI 与 docs/README.md「情报与计划任务」，避免"为什么没跑/为什么跑了两次"的歧义）：
//   - 一份计划任务 = 一份**确定的可重放规格**（task_spec，与创建任务 API 同形状的子集）
//     + 一个结构化频率。到点就按规格原样建一个普通任务，之后的推送/交付全走既有链路。
//   - **不重复触发**靠两层凭据：schedule_runs(schedule_id, scheduled_at) 唯一键认领，
//     再把 next_run_at 推到下一次（CAS）。认领、推进、建任务、记账在同一事务里，
//     多 tick/多实例不会建出两个任务，崩溃也不会留下"排期已推进、任务却没建成"的空洞。
//   - **不追补**：进程停机期间错过的次数只跑一次（开机时那一次），不会连建三个任务；
//     要补就人工点「立即运行」。
//   - 「立即运行」是**额外**一次运行，不改排期（下次仍按原计划）。

const (
	scheduleActionCreate = "create"
	scheduleActionStart  = "start"
	scheduleActionPause  = "pause"
	scheduleActionResume = "resume"
	// defaultScheduleTimezone is Beijing time, independent of the server host.
	defaultScheduleTimezone = "Asia/Shanghai"
)

func normalizeScheduleAction(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", scheduleActionCreate:
		return scheduleActionCreate, nil
	case scheduleActionStart:
		return scheduleActionStart, nil
	case scheduleActionPause:
		return scheduleActionPause, nil
	case scheduleActionResume:
		return scheduleActionResume, nil
	}
	return "", fmt.Errorf("action 必须是 create / pause / resume")
}

func scheduleActionVerb(action string) string {
	switch action {
	case scheduleActionStart:
		return "启动"
	case scheduleActionPause:
		return "暂停"
	case scheduleActionResume:
		return "继续"
	default:
		return "执行"
	}
}

func scheduleActionText(action string) string {
	switch action {
	case scheduleActionStart:
		return "启动指定任务"
	case scheduleActionPause:
		return "暂停指定任务"
	case scheduleActionResume:
		return "继续指定任务"
	default:
		return "创建并启动任务"
	}
}

// normalizeScheduleTimezone returns the IANA name and location. Empty = Beijing.
func normalizeScheduleTimezone(raw string) (string, *time.Location, error) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		tz = defaultScheduleTimezone
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", nil, fmt.Errorf("时区 %q 不合法（要 IANA 名称，如 Asia/Shanghai）", tz)
	}
	return tz, loc, nil
}

// scheduleNext computes the next occurrence in the schedule's wall-clock zone.
// A nil result means a one-time schedule has no future occurrence.
func formatScheduleTime(t *time.Time, tz string) string {
	if t == nil {
		return "—"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.FixedZone("Asia/Shanghai", 8*3600)
	}
	return t.In(loc).Format("2006-01-02 15:04 MST")
}

func scheduleNext(freq sched.Frequency, loc *time.Location, enabled bool) (*time.Time, error) {
	if !enabled {
		return nil, nil
	}
	n, err := freq.NextIn(time.Now(), loc)
	if err != nil {
		return nil, err
	}
	if n.IsZero() {
		return nil, fmt.Errorf("下一次触发不存在：一次性计划的时间已经过去")
	}
	return &n, nil
}

// scheduleSpec 是到点要建的任务规格：与 createTaskReq 同形状的子集（字段名一致，
// 前端可以直接复用建任务表单的字段名）。刻意不做隐式继承——模板改了不该影响已在跑的计划。
type scheduleSpec struct {
	Name                 string                 `json:"name,omitempty"`
	Description          string                 `json:"description"`
	Goal                 string                 `json:"goal"`
	CategoryID           *int64                 `json:"category_id,omitempty"`
	TemplateID           *int64                 `json:"template_id,omitempty"`
	LLMProfileIDs        []int64                `json:"llm_profile_ids,omitempty"`
	CompanyIDs           []int64                `json:"company_ids,omitempty"`
	TimeoutSeconds       int                    `json:"timeout_seconds,omitempty"`
	PlanHeartbeatSeconds int                    `json:"plan_heartbeat_seconds,omitempty"`
	CoverageEnabled      *bool                  `json:"coverage_enabled,omitempty"`
	SeedFirstIntent      *bool                  `json:"seed_first_intent,omitempty"`
	InterceptRules       []taskInterceptRuleReq `json:"intercept_rules,omitempty"`
}

// taskScheduleDTO 是给 UI 的计划任务视图：多给一个 frequency_text（人话），
// 省得前端再实现一遍频率渲染。
type taskScheduleDTO struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	Enabled       bool            `json:"enabled"`
	TaskSpec      scheduleSpec    `json:"task_spec"`
	Frequency     sched.Frequency `json:"frequency"`
	FrequencyText string          `json:"frequency_text"`
	Action        string          `json:"action"`
	ActionText    string          `json:"action_text"`
	TargetTaskID  string          `json:"target_task_id,omitempty"`
	Timezone      string          `json:"timezone"`
	NextRunAt     string          `json:"next_run_at,omitempty"`
	LastRunAt     string          `json:"last_run_at,omitempty"`
	LastTaskID    string          `json:"last_task_id,omitempty"`
	LastError     string          `json:"last_error,omitempty"`
	CreatedAt     string          `json:"created_at"`
}

func scheduleDTO(s *db.TaskSchedule) taskScheduleDTO {
	var spec scheduleSpec
	_ = json.Unmarshal(s.TaskSpec, &spec)
	var freq sched.Frequency
	_ = json.Unmarshal(s.Frequency, &freq)
	action := s.Action
	if action == "" {
		action = scheduleActionCreate
	}
	tz := s.Timezone
	if tz == "" {
		tz = defaultScheduleTimezone
	}
	dto := taskScheduleDTO{
		ID: s.ID, Name: s.Name, Enabled: s.Enabled, TaskSpec: spec, Frequency: freq,
		FrequencyText: freq.Describe(), Action: action, ActionText: scheduleActionText(action),
		TargetTaskID: s.TargetTaskID, Timezone: tz,
		LastTaskID: s.LastTaskID, LastError: s.LastError,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
	}
	if s.NextRunAt != nil {
		dto.NextRunAt = s.NextRunAt.Format(time.RFC3339)
	}
	if s.LastRunAt != nil {
		dto.LastRunAt = s.LastRunAt.Format(time.RFC3339)
	}
	return dto
}

// validateScheduleSpec 校验规格（写入侧就拒，不等到触发那天才发现写错）。
// 只做"能不能建成任务"的确定性检查；能力的可用性（LLM 配置是否还在等）留给触发时记录。
func (s *Server) validateScheduleSpec(spec scheduleSpec) error {
	if strings.TrimSpace(spec.Description) == "" {
		return fmt.Errorf("任务描述不能为空")
	}
	if strings.TrimSpace(spec.Goal) == "" {
		return fmt.Errorf("任务目标不能为空")
	}
	if spec.TimeoutSeconds < 0 {
		return fmt.Errorf("超时秒数不能为负")
	}
	if len(spec.LLMProfileIDs) > 0 {
		if err := s.validateTaskProfileIDs(spec.LLMProfileIDs); err != nil {
			return err
		}
	}
	if spec.CategoryID != nil {
		// GetTaskCategory 查不到时返回 (nil, nil)——必须判 nil，否则"分类不存在"会漏到触发那天。
		cat, err := s.m.pg.GetTaskCategory(*spec.CategoryID)
		if err != nil || cat == nil {
			return fmt.Errorf("任务分类不存在")
		}
	}
	if _, err := buildTaskInterceptRules(spec.InterceptRules); err != nil {
		return fmt.Errorf("任务级拦截规则无效：%w", err)
	}
	return nil
}

// scheduleTaskOptions resolves the parts of the spec that need DB lookups.
// Both the manual and scheduled paths use the same options.
func (s *Server) scheduleTaskOptions(spec scheduleSpec) (db.TaskCreateOptions, error) {
	interceptRules, err := buildTaskInterceptRules(spec.InterceptRules)
	if err != nil {
		return db.TaskCreateOptions{}, fmt.Errorf("任务级拦截规则无效：%w", err)
	}
	return db.TaskCreateOptions{
		Name: strings.TrimSpace(spec.Name), CategoryID: spec.CategoryID,
		CompanyIDs: spec.CompanyIDs, LLMProfileIDs: spec.LLMProfileIDs,
		TimeoutSeconds: spec.TimeoutSeconds, PlanHeartbeatSeconds: spec.PlanHeartbeatSeconds,
		CoverageEnabled: spec.CoverageEnabled, InterceptRules: interceptRules,
	}, nil
}

// createTaskFromSpec is the manual "run now" path (no schedule advancement).
func (s *Server) createTaskFromSpec(spec scheduleSpec) (*Task, error) {
	opts, err := s.scheduleTaskOptions(spec)
	if err != nil {
		return nil, err
	}
	t, err := s.m.CreateTaskWithOptions(spec.Description, spec.Goal, opts)
	if err != nil {
		return nil, err
	}
	s.launchTask(t, spec.Description+" "+spec.Goal, spec.SeedFirstIntent != nil && *spec.SeedFirstIntent)
	return t, nil
}

// ---- HTTP ----

func (s *Server) listTaskSchedules(w http.ResponseWriter, r *http.Request) {
	rows, err := s.m.pg.ListTaskSchedules()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]taskScheduleDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, scheduleDTO(row))
	}
	writeJSON(w, 200, map[string]any{"schedules": out})
}

type taskScheduleReq struct {
	Name         string          `json:"name"`
	Enabled      *bool           `json:"enabled"`
	TaskSpec     scheduleSpec    `json:"task_spec"`
	Frequency    sched.Frequency `json:"frequency"`
	Action       string          `json:"action"`
	TargetTaskID string          `json:"target_task_id"`
	Timezone     string          `json:"timezone"`
}

// validateScheduleActionTarget checks the target of a pause/resume action at
// write time. Trigger-time state (running/paused) is deliberately not enforced:
// the task may be in a different state when the schedule fires.
func (s *Server) validateScheduleActionTarget(action, target string) error {
	if action == scheduleActionCreate {
		return nil
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("%s 需要指定 target_task_id", scheduleActionText(action))
	}
	if t, ok := s.m.Task(target); !ok || t == nil {
		return fmt.Errorf("目标任务 #%s 不存在", target)
	}
	return nil
}

func (s *Server) createTaskSchedule(w http.ResponseWriter, r *http.Request) {
	var req taskScheduleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	action, err := normalizeScheduleAction(req.Action)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	tz, loc, err := normalizeScheduleTimezone(req.Timezone)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.TargetTaskID = strings.TrimSpace(req.TargetTaskID)
	if strings.TrimSpace(req.Name) == "" {
		req.Name = strings.TrimSpace(req.TaskSpec.Name)
	}
	if strings.TrimSpace(req.Name) == "" {
		if action == scheduleActionCreate {
			req.Name = clipRunesLocal(req.TaskSpec.Description, 40)
		} else {
			req.Name = clipRunesLocal(scheduleActionText(action)+" #"+req.TargetTaskID, 40)
		}
	}
	if err := req.Frequency.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if action == scheduleActionCreate {
		if err := s.validateScheduleSpec(req.TaskSpec); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	} else if err := s.validateScheduleActionTarget(action, req.TargetTaskID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	specRaw, _ := json.Marshal(req.TaskSpec)
	freqRaw, _ := json.Marshal(req.Frequency)
	enabled := req.Enabled == nil || *req.Enabled
	next, err := scheduleNext(req.Frequency, loc, enabled)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	row, err := s.m.pg.CreateTaskScheduleFull(req.Name, enabled, specRaw, freqRaw, action, req.TargetTaskID, tz, next)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.taskScheduleAudit(fmt.Sprintf("计划任务 #%d「%s」已创建：%s · %s · %s", row.ID, row.Name, action, req.Frequency.Describe(), tz))
	writeJSON(w, 201, scheduleDTO(row))
}

func (s *Server) patchTaskSchedule(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	cur, err := s.m.pg.GetTaskSchedule(id)
	if err != nil || cur == nil {
		writeErr(w, 404, "计划任务不存在")
		return
	}
	var req struct {
		Name         *string          `json:"name"`
		Enabled      *bool            `json:"enabled"`
		TaskSpec     *scheduleSpec    `json:"task_spec"`
		Frequency    *sched.Frequency `json:"frequency"`
		Action       *string          `json:"action"`
		TargetTaskID *string          `json:"target_task_id"`
		Timezone     *string          `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}

	action := cur.Action
	if action == "" {
		action = scheduleActionCreate
	}
	var actionPtr *string
	if req.Action != nil {
		normalized, err := normalizeScheduleAction(*req.Action)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		action = normalized
		actionPtr = &normalized
	}
	target := strings.TrimSpace(cur.TargetTaskID)
	var targetPtr *string
	if req.TargetTaskID != nil {
		target = strings.TrimSpace(*req.TargetTaskID)
		targetPtr = &target
	}
	tzRaw := cur.Timezone
	if strings.TrimSpace(tzRaw) == "" {
		tzRaw = defaultScheduleTimezone
	}
	tz, loc, err := normalizeScheduleTimezone(tzRaw)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var tzPtr *string
	if req.Timezone != nil {
		tz, loc, err = normalizeScheduleTimezone(*req.Timezone)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		tzPtr = &tz
	}

	var specRaw json.RawMessage
	if req.TaskSpec != nil {
		if action == scheduleActionCreate {
			if err := s.validateScheduleSpec(*req.TaskSpec); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
		}
		specRaw, _ = json.Marshal(*req.TaskSpec)
	}
	if action == scheduleActionCreate && req.TaskSpec == nil {
		var spec scheduleSpec
		if err := json.Unmarshal(cur.TaskSpec, &spec); err != nil {
			writeErr(w, 400, "现有任务规格不是合法 JSON："+err.Error())
			return
		}
		if err := s.validateScheduleSpec(spec); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if action != scheduleActionCreate {
		if err := s.validateScheduleActionTarget(action, target); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}

	freq := sched.Frequency{}
	_ = json.Unmarshal(cur.Frequency, &freq)
	var freqRaw json.RawMessage
	if req.Frequency != nil {
		if err := req.Frequency.Validate(); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		freq = *req.Frequency
		freqRaw, _ = json.Marshal(freq)
	}
	enabled := cur.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	next, err := scheduleNext(freq, loc, enabled)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	clearNext := !enabled
	row, err := s.m.pg.UpdateTaskScheduleFull(id, req.Name, req.Enabled, specRaw, freqRaw,
		actionPtr, targetPtr, tzPtr, next, clearNext)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if row == nil {
		writeErr(w, 404, "计划任务不存在")
		return
	}
	if req.Enabled != nil && *req.Enabled && !cur.Enabled {
		s.taskScheduleAudit(fmt.Sprintf("计划任务 #%d「%s」已启用（%s · %s · 下次 %s）",
			id, row.Name, freq.Describe(), tz, formatScheduleTime(row.NextRunAt, tz)))
	}
	writeJSON(w, 200, scheduleDTO(row))
}

func (s *Server) deleteTaskSchedule(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	ok, err := s.m.pg.DeleteTaskSchedule(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !ok {
		writeErr(w, 404, "计划任务不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true})
}

// runTaskScheduleNow 立即按规格建一个任务（**额外**一次运行，不改排期）。
func (s *Server) runTaskScheduleNow(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	row, err := s.m.pg.GetTaskSchedule(id)
	if err != nil || row == nil {
		writeErr(w, 404, "计划任务不存在")
		return
	}
	t, err := s.fireSchedule(row)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	note := "已按规格建任务（不影响下次排期）"
	if row.Action != "" && row.Action != scheduleActionCreate {
		note = "已立即执行定时动作（不影响下次排期）"
	}
	writeJSON(w, 200, map[string]any{"task_id": t.ID, "note": note})
}

// listTaskScheduleRuns returns the firing ledger for one schedule. It is a
// read-only ops view: the scheduler's claim/CAS semantics are untouched.
func (s *Server) listTaskScheduleRuns(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	row, err := s.m.pg.GetTaskSchedule(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if row == nil {
		writeErr(w, 404, "计划任务不存在")
		return
	}
	page := int(parseIntDefault(r.URL.Query().Get("page")))
	if page <= 0 {
		page = 1
	}
	size := int(parseIntDefault(r.URL.Query().Get("size")))
	if size <= 0 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	runs, total, err := s.m.pg.ListScheduleRunsPage(r.Context(), id, size, (page-1)*size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"runs": runs, "total": total, "page": page, "size": size})
}

// fireSchedule is the manual "run now" path: it creates a task or applies a
// pause/resume action without touching the occurrence schedule or the scheduled-run ledger.
func (s *Server) fireSchedule(row *db.TaskSchedule) (*Task, error) {
	action := row.Action
	if action == "" {
		action = scheduleActionCreate
	}
	if action == scheduleActionCreate {
		var spec scheduleSpec
		if err := json.Unmarshal(row.TaskSpec, &spec); err != nil {
			return nil, fmt.Errorf("规格不是合法 JSON：%w", err)
		}
		if err := s.validateScheduleSpec(spec); err != nil {
			return nil, err
		}
		t, err := s.createTaskFromSpec(spec)
		if err != nil {
			return nil, err
		}
		log.Printf("[schedule] 计划任务 #%d「%s」立即运行，建任务 #%s（%s）", row.ID, row.Name, t.ID, t.Description)
		_ = s.m.pg.FinishScheduleRun(row.ID, time.Now(), t.ID, "")
		return t, nil
	}
	target := strings.TrimSpace(row.TargetTaskID)
	if target == "" {
		return nil, fmt.Errorf("%s 没有指定目标任务", scheduleActionText(action))
	}
	t, ok := s.m.Task(target)
	if !ok || t == nil {
		return nil, fmt.Errorf("目标任务 #%s 不存在", target)
	}
	if action == scheduleActionStart {
		if _, err := s.admitTask(t, s.resumeAdmissionMode(t)); err != nil {
			_ = s.m.pg.FinishScheduleRun(row.ID, time.Now(), target, err.Error())
			return nil, err
		}
	} else {
		cause := agent.Causef("scheduled_"+action, "计划任务立即"+scheduleActionText(action), "由计划任务「%s」手动触发", row.Name)
		if _, err := s.applyTaskControlWithCause(t, action, cause); err != nil {
			_ = s.m.pg.FinishScheduleRun(row.ID, time.Now(), target, err.Error())
			return nil, err
		}
	}
	log.Printf("[schedule] 计划任务 #%d「%s」立即%s任务 #%s", row.ID, row.Name, scheduleActionVerb(action), target)
	_ = s.m.pg.FinishScheduleRun(row.ID, time.Now(), target, "")
	return t, nil
}

// taskScheduleAudit 写一条全局审计（计划任务不属于任何任务，进不去任务时间线；
// 用系统日志 + 设置页的列表足够——这里刻意不造第四条"全局活动"存储）。
func (s *Server) taskScheduleAudit(msg string) { log.Printf("[schedule] %s", msg) }

// runDueSchedules 是 scheduler tick 的一部分：取到点的计划任务，逐个 CAS 抢跑。
func (sc *Scheduler) runDueSchedules(ctx context.Context) {
	due, err := sc.pg.DueTaskSchedules(time.Now(), 20)
	if err != nil || len(due) == 0 {
		return
	}
	for _, row := range due {
		// 抢跑**之前**再复查一次停机/时间窗。放在这里的而不是抢跑之后，是因为抢跑会推进
		// 水位：抢了却不建任务，这一次就永远丢了（停机期间本来就"不追补"，但那是用户
		// 可见的语义，不该由竞态决定）。
		if sc.s.safetyAdmissionBlocked() {
			return
		}
		if row.NextRunAt == nil {
			continue
		}
		_, loc, tzErr := normalizeScheduleTimezone(row.Timezone)
		if tzErr != nil {
			sc.failDueSchedule(ctx, row, nil, tzErr)
			continue
		}
		var freq sched.Frequency
		if err := json.Unmarshal(row.Frequency, &freq); err != nil {
			sc.failDueSchedule(ctx, row, nil, fmt.Errorf("频率不是合法 JSON：%w", err))
			continue
		}
		// 下一次从"现在"起算：停机期间错过的次数不追补（开机只跑这一次）。
		nextTime, nextErr := freq.NextIn(time.Now(), loc)
		if nextErr != nil {
			sc.failDueSchedule(ctx, row, nil, fmt.Errorf("频率不合法：%w", nextErr))
			continue
		}
		var next *time.Time
		if !nextTime.IsZero() {
			next = &nextTime
		}
		action := row.Action
		if action == "" {
			action = scheduleActionCreate
		}
		if action != scheduleActionCreate {
			target := strings.TrimSpace(row.TargetTaskID)
			if target == "" {
				sc.failDueSchedule(ctx, row, next, fmt.Errorf("%s 没有指定目标任务", scheduleActionText(action)))
				continue
			}
			t, ok := sc.s.m.Task(target)
			if !ok || t == nil {
				sc.failDueSchedule(ctx, row, next, fmt.Errorf("目标任务 #%s 不存在", target))
				continue
			}
			runID, err := sc.pg.ClaimTaskScheduleAction(ctx, row.ID, *row.NextRunAt, next)
			if err != nil {
				if errors.Is(err, db.ErrScheduleRunDuplicate) || errors.Is(err, db.ErrScheduleRunAdvanced) {
					continue
				}
				log.Printf("[schedule] 计划任务 #%d「%s」认领动作失败：%v", row.ID, row.Name, err)
				continue
			}
			state, errMsg := "applied", ""
			if action == scheduleActionStart {
				if _, err := sc.s.admitTask(t, sc.s.resumeAdmissionMode(t)); err != nil {
					state, errMsg = "failed", err.Error()
				}
			} else {
				cause := agent.Causef("scheduled_"+action, "计划任务自动"+scheduleActionText(action), "由计划任务「%s」到点触发", row.Name)
				if _, err := sc.s.applyTaskControlWithCause(t, action, cause); err != nil {
					state, errMsg = "failed", err.Error()
				}
			}
			if err := sc.pg.FinishTaskScheduleAction(ctx, row.ID, runID, state, target, errMsg); err != nil {
				log.Printf("[schedule] 计划任务 #%d「%s」记录动作结果失败：%v", row.ID, row.Name, err)
			}
			if errMsg != "" {
				log.Printf("[schedule] 计划任务 #%d「%s」到点%s任务 #%s 失败：%s", row.ID, row.Name, scheduleActionVerb(action), target, errMsg)
			} else {
				log.Printf("[schedule] 计划任务 #%d「%s」到点%s任务 #%s", row.ID, row.Name, scheduleActionVerb(action), target)
			}
			continue
		}

		var spec scheduleSpec
		if err := json.Unmarshal(row.TaskSpec, &spec); err != nil {
			sc.failDueSchedule(ctx, row, next, fmt.Errorf("规格不是合法 JSON：%w", err))
			continue
		}
		if err := sc.s.validateScheduleSpec(spec); err != nil {
			sc.failDueSchedule(ctx, row, next, err)
			continue
		}
		opts, err := sc.s.scheduleTaskOptions(spec)
		if err != nil {
			sc.failDueSchedule(ctx, row, next, err)
			continue
		}
		// The claim, next_run_at CAS, task creation and ledger write share one
		// transaction. A task-creation error is recorded as a failed occurrence
		// and still advances the schedule, so a broken spec cannot hot-loop.
		pt, err := sc.pg.FireTaskScheduleRunWithNext(ctx, row.ID, *row.NextRunAt, next, spec.Description, spec.Goal, opts)
		if err != nil {
			if errors.Is(err, db.ErrScheduleRunDuplicate) || errors.Is(err, db.ErrScheduleRunAdvanced) {
				continue // another tick/instance owns this occurrence
			}
			log.Printf("[schedule] 计划任务 #%d「%s」触发失败：%v", row.ID, row.Name, err)
			continue
		}
		t := sc.s.m.RegisterCreatedTask(pt)
		sc.s.launchTask(t, spec.Description+" "+spec.Goal, spec.SeedFirstIntent != nil && *spec.SeedFirstIntent)
		log.Printf("[schedule] 计划任务 #%d「%s」到点触发，建任务 #%s（%s）", row.ID, row.Name, t.ID, t.Description)
	}
}

func (sc *Scheduler) failDueSchedule(ctx context.Context, row *db.TaskSchedule, next *time.Time, cause error) {
	if err := sc.pg.FailTaskScheduleRunWithNext(ctx, row.ID, *row.NextRunAt, next, cause.Error()); err != nil {
		if errors.Is(err, db.ErrScheduleRunDuplicate) || errors.Is(err, db.ErrScheduleRunAdvanced) {
			return
		}
		log.Printf("[schedule] 计划任务 #%d「%s」记录失败运行出错：%v（原因：%v）", row.ID, row.Name, err, cause)
	}
}
