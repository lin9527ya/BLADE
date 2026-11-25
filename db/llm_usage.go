package db

import (
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

// LLMUsage is one lightweight LLM-call metering row — the always-on usage ledger,
// distinct from llm_records (which stores full request/response bodies and is a
// gated debug feature). One row per completion call, written on both success and
// error, so token accounting is complete even for interrupted/failed runs. Carries
// only the dimensions needed to slice token spend (model / profile / task / agent),
// never any prompt or response content.
type LLMUsage struct {
	Surface       *RequestSurface `json:"request_surface,omitempty"`
	TaskID        string          `json:"task_id"`        // task registry id (matches llm_records.task_id)
	ExplorationID int64           `json:"exploration_id"` // exploration id parsed from the session (0 = unknown/non-task)
	Worker        string          `json:"worker"`         // agent lane: worker / planner / mainagent / goals
	// SessionID 是这次调用所属的会话（transcript session id）。用于把「首调零命中」
	// 与「会话中途掉命中」分开——只看 worker 聚合会把两种完全不同的原因混在一起。
	SessionID    string `json:"session_id"`
	Model        string `json:"model"`
	ProfileName  string `json:"profile_name"`
	LatencyMs    int    `json:"latency_ms"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	CacheRead    int    `json:"cache_read"`
	CacheWrite   int    `json:"cache_write"`
	Status       string `json:"status"` // ok | error
}

const llmUsageSchema = `
CREATE TABLE IF NOT EXISTS llm_usage (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    task_id        TEXT,
    exploration_id BIGINT,
    worker         TEXT,
    session_id     TEXT,
    model          TEXT,
    profile_name   TEXT,
    latency_ms     INTEGER,
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read     INTEGER NOT NULL DEFAULT 0,
    cache_write    INTEGER NOT NULL DEFAULT 0,
    status         TEXT
);
-- 旧库补列：EnsureLLMUsageTable 在每次启动时执行，ALTER 必须是幂等的。
ALTER TABLE llm_usage ADD COLUMN IF NOT EXISTS session_id TEXT;
ALTER TABLE llm_usage ADD COLUMN IF NOT EXISTS request_surface JSONB;
CREATE INDEX IF NOT EXISTS idx_llm_usage_task  ON llm_usage(task_id);
CREATE INDEX IF NOT EXISTS idx_llm_usage_model ON llm_usage(task_id, model);
CREATE INDEX IF NOT EXISTS idx_llm_usage_exp   ON llm_usage(exploration_id);
CREATE INDEX IF NOT EXISTS idx_llm_usage_session ON llm_usage(worker, session_id);
-- 数据保留按 ts 清理。上面三个索引前导列都不是时间，全表扫；本表是 always-on
-- 写的（每次 LLM 调用一行），是无限增长的主力，必须能按时间走索引。
CREATE INDEX IF NOT EXISTS idx_llm_usage_ts    ON llm_usage(ts);
`

// EnsureLLMUsageTable creates the llm_usage metering table if it does not exist.
func (d *DB) EnsureLLMUsageTable() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(llmUsageSchema); err != nil {
		return err
	}
	return tx.Commit()
}

// TokenUsedByTask sums input+output tokens recorded for one task (taskID is the
// same string the caller passes to TokenByModel). 0 when nothing recorded yet.
//
// task_id 为空时 InsertLLMUsage 存的是 NULL（写入侧把空串转成 NULL），所以空入参
// 永远匹配不到行——直接短路，别去扫表。比较写成裸列相等是为了让 idx_llm_usage_task
// 真正生效：此前用 COALESCE 把 task_id 包起来再比，表达式让索引失效，而本函数跑在
// 「每个运行中任务每 2 秒一次」的轮询里（见 server/engine_timeout.go 的 token 预算护栏）。
func (d *DB) TokenUsedByTask(taskID string) (int64, error) {
	if taskID == "" {
		return 0, nil
	}
	var used int64
	err := d.QueryRow(`SELECT COALESCE(SUM(input_tokens+output_tokens),0) FROM llm_usage WHERE task_id=$1`, taskID).Scan(&used)
	return used, err
}

// InsertLLMUsage appends one metering row. Best-effort: callers log and continue on
// error (a lost metering row must never break the LLM call).
func (d *DB) InsertLLMUsage(u *LLMUsage) error {
	var expID any
	if u.ExplorationID > 0 {
		expID = u.ExplorationID
	}
	var surface any
	if u.Surface != nil {
		raw, err := json.Marshal(u.Surface)
		if err != nil {
			return err
		}
		surface = string(raw)
	}
	_, err := d.Exec(`
INSERT INTO llm_usage(task_id, exploration_id, worker, session_id, model, profile_name, latency_ms, input_tokens, output_tokens, cache_read, cache_write, status, request_surface)
VALUES (NULLIF($1,''),$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10,$11,$12,$13::jsonb)`,
		u.TaskID, expID, u.Worker, u.SessionID, u.Model, u.ProfileName,
		u.LatencyMs, u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheWrite, u.Status, surface)
	return err
}

// TokenByModel aggregates a task's LLM token usage and estimated USD cost grouped
// by model, most-used first, from the always-on llm_usage ledger. taskID is the
// task registry id. Accurate even with per-agent model bindings, pool
// rotation/failover, and interrupted runs, since every call (success or error) is
// metered, and applies the same peak/off-peak price rules as CostSummary.
//
// 同 TokenUsedByTask：空 taskID 短路（空值存为 NULL），比较用裸列以命中
// idx_llm_usage_model(task_id, model)。
func (d *DB) TokenByModel(taskID string) ([]ModelTokenStat, error) {
	if taskID == "" {
		return []ModelTokenStat{}, nil
	}
	prices, err := d.llmPriceMap()
	if err != nil {
		return nil, err
	}
	rows, err := d.Query(`
SELECT COALESCE(NULLIF(u.model,''),'(unknown)') AS model,
       `+costPeriodExprSQL+`,
       COUNT(*) AS calls,
       COALESCE(SUM(u.input_tokens),0), COALESCE(SUM(u.output_tokens),0),
       COALESCE(SUM(u.cache_read),0), COALESCE(SUM(u.cache_write),0)
FROM llm_usage u
`+costPriceJoinSQL+`
WHERE u.task_id = $1
GROUP BY 1, 2`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byModel := map[string]*ModelTokenStat{}
	for rows.Next() {
		var model, period string
		var calls int
		var inTok, outTok, crTok, cwTok int64
		if err := rows.Scan(&model, &period, &calls, &inTok, &outTok, &crTok, &cwTok); err != nil {
			return nil, err
		}
		stat := byModel[model]
		if stat == nil {
			stat = &ModelTokenStat{Model: model}
			byModel[model] = stat
		}
		stat.Calls += calls
		stat.InputTokens += int(inTok)
		stat.OutputTokens += int(outTok)
		stat.CacheReadTokens += int(crTok)
		stat.CacheWriteTokens += int(cwTok)
		if p, priced := pricedFor(model, prices, period); priced {
			stat.Priced = true
			cost, _ := usageCostUSD(inTok, outTok, crTok, cwTok, p)
			stat.CostUSD += cost
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ModelTokenStat, 0, len(byModel))
	for _, m := range byModel {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		ti := out[i].InputTokens + out[i].OutputTokens
		tj := out[j].InputTokens + out[j].OutputTokens
		if ti != tj {
			return ti > tj
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

// ProfileUsage aggregates the whole ledger's token spend for one LLM profile
// (global, all tasks). Powers the dashboard's per-profile token card (new source).
type ProfileUsage struct {
	ProfileName      string `json:"profile_name"`
	Calls            int    `json:"calls"`
	Tasks            int    `json:"tasks"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// UsageByProfile returns global token spend grouped by profile name, most-used
// first. profile_name may be empty for calls made on env/non-persisted configs.
func (d *DB) UsageByProfile() ([]ProfileUsage, error) {
	rows, err := d.Query(`
SELECT COALESCE(profile_name,'') AS profile_name, COUNT(*) AS calls,
       COUNT(DISTINCT task_id) AS tasks,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
GROUP BY profile_name
ORDER BY SUM(input_tokens) + SUM(output_tokens) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileUsage{}
	for rows.Next() {
		var p ProfileUsage
		if err := rows.Scan(&p.ProfileName, &p.Calls, &p.Tasks,
			&p.InputTokens, &p.OutputTokens, &p.CacheReadTokens, &p.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]ProfileUsage, len(out))
	for _, current := range out {
		byName[current.ProfileName] = current
	}
	for _, aggregate := range archived {
		for _, cold := range aggregate.TokenProfiles {
			current := byName[cold.ProfileName]
			current.ProfileName = cold.ProfileName
			current.Calls += cold.Calls
			current.Tasks += cold.Tasks
			current.InputTokens += cold.InputTokens
			current.OutputTokens += cold.OutputTokens
			current.CacheReadTokens += cold.CacheReadTokens
			current.CacheWriteTokens += cold.CacheWriteTokens
			byName[cold.ProfileName] = current
		}
	}
	out = out[:0]
	for _, current := range byName {
		out = append(out, current)
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].InputTokens + out[i].OutputTokens
		right := out[j].InputTokens + out[j].OutputTokens
		if left != right {
			return left > right
		}
		return out[i].ProfileName < out[j].ProfileName
	})
	return out, nil
}

// ProfileDayUsage is one (profile, UTC calendar day) token bucket for the daily
// chart. Unlike the activity-based chart, ts is the real call time, so this is
// actual per-day consumption rather than tokens bucketed by task creation date.
type ProfileDayUsage struct {
	ProfileName     string `json:"profile_name"`
	Date            string `json:"date"` // YYYY-MM-DD (UTC)
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CacheReadTokens int    `json:"cache_read_tokens"`
}

// UsageDaily returns per-(profile, day) token buckets for the past `days` days
// (default 365 when days<=0), so the dashboard can slice by profile + range.
func (d *DB) UsageDaily(days int) ([]ProfileDayUsage, error) {
	if days <= 0 {
		days = 365
	}
	rows, err := d.Query(`
SELECT COALESCE(profile_name,'') AS profile_name,
       to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read),0)
FROM llm_usage
WHERE ts >= now() - ($1 * interval '1 day')
GROUP BY profile_name, day
ORDER BY day`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileDayUsage{}
	for rows.Next() {
		var p ProfileDayUsage
		if err := rows.Scan(&p.ProfileName, &p.Date, &p.InputTokens, &p.OutputTokens, &p.CacheReadTokens); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	byKey := make(map[string]ProfileDayUsage, len(out))
	for _, current := range out {
		byKey[current.ProfileName+"\x00"+current.Date] = current
	}
	for _, aggregate := range archived {
		for _, cold := range aggregate.TokenDaily {
			if cold.Date < cutoff {
				continue
			}
			key := cold.ProfileName + "\x00" + cold.Date
			current := byKey[key]
			current.ProfileName = cold.ProfileName
			current.Date = cold.Date
			current.InputTokens += cold.InputTokens
			current.OutputTokens += cold.OutputTokens
			current.CacheReadTokens += cold.CacheReadTokens
			byKey[key] = current
		}
	}
	out = out[:0]
	for _, current := range byKey {
		out = append(out, current)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].ProfileName < out[j].ProfileName
	})
	return out, nil
}

// JudgeDayUsage is one UTC-day token bucket for the intercept fallback judge,
// for the config page's recent-spend sparkline.
type JudgeDayUsage struct {
	Date         string `json:"date"` // YYYY-MM-DD (UTC)
	Calls        int    `json:"calls"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

// JudgeUsage is the cumulative token spend of the intercept fallback judge
// (worker='judge' rows in the always-on ledger), plus a recent daily series.
type JudgeUsage struct {
	Calls            int             `json:"calls"`
	InputTokens      int             `json:"input_tokens"`
	OutputTokens     int             `json:"output_tokens"`
	CacheReadTokens  int             `json:"cache_read_tokens"`
	CacheWriteTokens int             `json:"cache_write_tokens"`
	Daily            []JudgeDayUsage `json:"daily"`
}

// JudgeUsageStats returns the fallback judge's all-time token totals and a
// per-day series over the past `days` days (default 30). Sourced from the
// always-on llm_usage ledger, so it is accurate across pool rotation and
// interrupted/failed judge calls. days only bounds the daily series; totals are
// all-time.
func (d *DB) JudgeUsageStats(days int) (JudgeUsage, error) {
	if days <= 0 {
		days = 30
	}
	var u JudgeUsage
	err := d.QueryRow(`
SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
WHERE worker = 'judge'`).Scan(&u.Calls, &u.InputTokens, &u.OutputTokens,
		&u.CacheReadTokens, &u.CacheWriteTokens)
	if err != nil {
		return JudgeUsage{}, err
	}
	rows, err := d.Query(`
SELECT to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
       COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0)
FROM llm_usage
WHERE worker = 'judge' AND ts >= now() - ($1 * interval '1 day')
GROUP BY day
ORDER BY day`, days)
	if err != nil {
		return JudgeUsage{}, err
	}
	defer rows.Close()
	u.Daily = []JudgeDayUsage{}
	for rows.Next() {
		var day JudgeDayUsage
		if err := rows.Scan(&day.Date, &day.Calls, &day.InputTokens, &day.OutputTokens); err != nil {
			return JudgeUsage{}, err
		}
		u.Daily = append(u.Daily, day)
	}
	if err := rows.Err(); err != nil {
		return JudgeUsage{}, err
	}
	return u, nil
}

// ParseExpID turns the exploration-id segment parsed from a session string into an
// int64 (0 when empty/non-numeric, e.g. chat sessions keyed by conversation id).
func ParseExpID(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// RequestSurfaceSummary is the cross-language attribution contract consumed by
// /api/diagnostics/cache and bench/scheduler.py. The json tags are explicit
// because Go's default field names (Worker, SystemSHA, ...) silently produced an
// empty attribution in the Python benchmark when tags were missing.
type RequestSurfaceSummary struct {
	Worker         string `json:"worker"`
	Model          string `json:"model"`
	Format         string `json:"format"`
	BuildVersion   string `json:"build_version"`
	SystemSHA      string `json:"system_sha"`
	ToolsSHA       string `json:"tools_sha"`
	ParametersSHA  string `json:"parameters_sha"`
	EndpointSHA    string `json:"endpoint_sha"`
	Calls          int64  `json:"calls"`
	ChangedCalls   int64  `json:"changed_calls"`
	AvgInput       int64  `json:"avg_input"`
	AvgOutput      int64  `json:"avg_output"`
	CacheHitTokens int64  `json:"cache_hit_tokens"`
}

// Only structural fields and numeric usage; no request/response content is read.
func (d *DB) RequestSurfaceSummary(taskID string) ([]RequestSurfaceSummary, error) {
	if taskID == "" {
		return []RequestSurfaceSummary{}, nil
	}
	rows, err := d.Query(`SELECT COALESCE(worker,''),COALESCE(request_surface->>'model',''),COALESCE(request_surface->>'format',''),COALESCE(request_surface->>'build_version',''),
      COALESCE(request_surface->>'system_sha',''),COALESCE(request_surface->>'tools_sha',''),COALESCE(request_surface->>'parameters_sha',''),COALESCE(request_surface->>'endpoint_sha',''),
      count(*),count(*) FILTER (WHERE COALESCE((request_surface->>'changed_during_call')::boolean,false)),
      COALESCE(sum(input_tokens),0)/GREATEST(count(*),1),COALESCE(sum(output_tokens),0)/GREATEST(count(*),1),COALESCE(sum(cache_read),0)
      FROM llm_usage WHERE task_id=$1 AND COALESCE((request_surface->>'observed')::boolean,false)
      GROUP BY 1,2,3,4,5,6,7,8 ORDER BY 1,9 DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RequestSurfaceSummary{}
	for rows.Next() {
		var s RequestSurfaceSummary
		if err := rows.Scan(&s.Worker, &s.Model, &s.Format, &s.BuildVersion, &s.SystemSHA, &s.ToolsSHA, &s.ParametersSHA, &s.EndpointSHA, &s.Calls, &s.ChangedCalls, &s.AvgInput, &s.AvgOutput, &s.CacheHitTokens); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
