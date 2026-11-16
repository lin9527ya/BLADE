package db

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LLMOffpeakWindow is one daily off-peak interval in the price row's timezone.
// start > end means the window crosses midnight.
type LLMOffpeakWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// LLMPrice is USD per million tokens for one model. model="default" is the
// fallback for models without an exact row.
type LLMPrice struct {
	Model                string  `json:"model"`
	InputPerMillion      float64 `json:"input_per_million"`
	OutputPerMillion     float64 `json:"output_per_million"`
	CacheReadPerMillion  float64 `json:"cache_read_per_million"`
	CacheWritePerMillion float64 `json:"cache_write_per_million"`
	// 谷时时段（HH:MM，可多个；空 = 不启用）。基础价格即峰时/窗口外价格。
	OffpeakWindows              []LLMOffpeakWindow `json:"offpeak_windows"`
	OffpeakTimezone             string             `json:"offpeak_timezone,omitempty"`
	OffpeakInputPerMillion      float64            `json:"offpeak_input_per_million"`
	OffpeakOutputPerMillion     float64            `json:"offpeak_output_per_million"`
	OffpeakCacheReadPerMillion  float64            `json:"offpeak_cache_read_per_million"`
	OffpeakCacheWritePerMillion float64            `json:"offpeak_cache_write_per_million"`
	UpdatedAt                   time.Time          `json:"updated_at"`
}

type CostTotals struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	CacheSavingsUSD  float64 `json:"cache_savings_usd"`
	PeakCostUSD      float64 `json:"peak_cost_usd"`
	OffpeakCostUSD   float64 `json:"offpeak_cost_usd"`
	UnpricedTokens   int64   `json:"unpriced_tokens"`
}

type CostDay struct {
	Date           string  `json:"date"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	UnpricedTokens int64   `json:"unpriced_tokens"`
}

type CostModel struct {
	Model           string  `json:"model"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	CacheReadTokens int64   `json:"cache_read_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	PeakCostUSD     float64 `json:"peak_cost_usd"`
	OffpeakCostUSD  float64 `json:"offpeak_cost_usd"`
	UnpricedTokens  int64   `json:"unpriced_tokens"`
	Priced          bool    `json:"priced"`
}

type CostProfile struct {
	ProfileName    string  `json:"profile_name"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	UnpricedTokens int64   `json:"unpriced_tokens"`
}

type CostSummary struct {
	Days         int           `json:"days"`
	Daily        []CostDay     `json:"daily"`
	ByModel      []CostModel   `json:"by_model"`
	ByProfile    []CostProfile `json:"by_profile"`
	Total        CostTotals    `json:"total"`
	Month        CostTotals    `json:"month"`
	BudgetUSD    float64       `json:"budget_usd"`
	AlertPct     int           `json:"alert_pct"`
	BudgetStatus string        `json:"budget_status"` // unset | ok | warning | exceeded
}

const (
	settingLLMMonthlyBudget = "llm_monthly_budget_usd"
	settingLLMBudgetAlert   = "llm_budget_alert_pct"
)

func (d *DB) ListLLMPrices() ([]*LLMPrice, error) {
	rows, err := d.Query(`SELECT model,input_per_million,output_per_million,cache_read_per_million,cache_write_per_million,
COALESCE(offpeak_timezone,'Asia/Shanghai'),
offpeak_input_per_million,offpeak_output_per_million,offpeak_cache_read_per_million,offpeak_cache_write_per_million,
updated_at
FROM llm_prices ORDER BY (model='default') DESC, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	out := []*LLMPrice{}
	byModel := map[string]*LLMPrice{}
	for rows.Next() {
		var p LLMPrice
		if err := rows.Scan(&p.Model, &p.InputPerMillion, &p.OutputPerMillion, &p.CacheReadPerMillion, &p.CacheWritePerMillion,
			&p.OffpeakTimezone,
			&p.OffpeakInputPerMillion, &p.OffpeakOutputPerMillion, &p.OffpeakCacheReadPerMillion, &p.OffpeakCacheWritePerMillion,
			&p.UpdatedAt); err != nil {
			return nil, err
		}
		p.OffpeakWindows = []LLMOffpeakWindow{}
		out = append(out, &p)
		byModel[p.Model] = &p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	windows, err := d.Query(`SELECT model, to_char(start_time,'HH24:MI'), to_char(end_time,'HH24:MI')
FROM llm_price_offpeak_windows ORDER BY model, position`)
	if err != nil {
		return nil, err
	}
	defer windows.Close() //nolint:errcheck
	for windows.Next() {
		var model, start, end string
		if err := windows.Scan(&model, &start, &end); err != nil {
			return nil, err
		}
		if p := byModel[model]; p != nil {
			p.OffpeakWindows = append(p.OffpeakWindows, LLMOffpeakWindow{Start: start, End: end})
		}
	}
	return out, windows.Err()
}

func normalizeLLMPrice(item LLMPrice) (LLMPrice, error) {
	model := strings.TrimSpace(item.Model)
	item.Model = model
	item.OffpeakTimezone = strings.TrimSpace(item.OffpeakTimezone)
	if item.OffpeakTimezone == "" {
		item.OffpeakTimezone = "Asia/Shanghai"
	}
	if len(item.OffpeakWindows) > 8 {
		return item, fmt.Errorf("模型 %q 最多配置 8 个谷时时段", model)
	}
	windows := make([]LLMOffpeakWindow, 0, len(item.OffpeakWindows))
	seenWindows := map[string]bool{}
	for _, window := range item.OffpeakWindows {
		window.Start = strings.TrimSpace(window.Start)
		window.End = strings.TrimSpace(window.End)
		if window.Start == "" && window.End == "" {
			continue
		}
		if window.Start == "" || window.End == "" {
			return item, fmt.Errorf("模型 %q 的每个谷时时段都必须同时填写开始和结束", model)
		}
		start, err := time.Parse("15:04", window.Start)
		if err != nil || start.Format("15:04") != window.Start {
			return item, fmt.Errorf("模型 %q 的谷时开始时间必须是 HH:MM", model)
		}
		end, err := time.Parse("15:04", window.End)
		if err != nil || end.Format("15:04") != window.End {
			return item, fmt.Errorf("模型 %q 的谷时结束时间必须是 HH:MM", model)
		}
		if start.Equal(end) {
			return item, fmt.Errorf("模型 %q 的谷时开始和结束不能相同", model)
		}
		key := window.Start + "-" + window.End
		if seenWindows[key] {
			return item, fmt.Errorf("模型 %q 的谷时时段 %s 重复", model, key)
		}
		seenWindows[key] = true
		windows = append(windows, LLMOffpeakWindow{Start: window.Start, End: window.End})
	}
	item.OffpeakWindows = windows
	if _, err := time.LoadLocation(item.OffpeakTimezone); err != nil {
		return item, fmt.Errorf("模型 %q 的谷时时区无效：%v", model, err)
	}
	for _, v := range []float64{item.InputPerMillion, item.OutputPerMillion, item.CacheReadPerMillion, item.CacheWritePerMillion,
		item.OffpeakInputPerMillion, item.OffpeakOutputPerMillion, item.OffpeakCacheReadPerMillion, item.OffpeakCacheWritePerMillion} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e7 {
			return item, fmt.Errorf("模型 %q 的价格必须是 0–1e7 的非负数", model)
		}
	}
	return item, nil
}

// ReplaceLLMPrices replaces the whole table atomically. The table is tiny
// (models in use), so a delete+insert keeps the API and UI simple.
func (d *DB) ReplaceLLMPrices(items []LLMPrice) error {
	if len(items) > 200 {
		return fmt.Errorf("价格表最多 200 行")
	}
	seen := map[string]bool{}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM llm_prices`); err != nil {
		return err
	}
	for _, item := range items {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			return fmt.Errorf("模型名不能为空")
		}
		if seen[model] {
			return fmt.Errorf("模型 %q 重复", model)
		}
		seen[model] = true
		normalized, err := normalizeLLMPrice(item)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO llm_prices(model,input_per_million,output_per_million,cache_read_per_million,cache_write_per_million,
offpeak_timezone,offpeak_input_per_million,offpeak_output_per_million,offpeak_cache_read_per_million,offpeak_cache_write_per_million)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			model, normalized.InputPerMillion, normalized.OutputPerMillion, normalized.CacheReadPerMillion, normalized.CacheWritePerMillion,
			normalized.OffpeakTimezone,
			normalized.OffpeakInputPerMillion, normalized.OffpeakOutputPerMillion, normalized.OffpeakCacheReadPerMillion, normalized.OffpeakCacheWritePerMillion); err != nil {
			return err
		}
		for position, window := range normalized.OffpeakWindows {
			if _, err := tx.Exec(`INSERT INTO llm_price_offpeak_windows(model,position,start_time,end_time)
VALUES($1,$2,$3::time,$4::time)`, model, position, window.Start, window.End); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

type costUsageRow struct {
	Date       string
	Model      string
	Profile    string
	Period     string // peak | offpeak
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

func (d *DB) costRows(days int) ([]costUsageRow, error) {
	if days <= 0 {
		days = 30
	}
	if days > 3660 {
		days = 3660
	}
	rows, err := d.Query(`
SELECT to_char(u.ts AT TIME ZONE 'UTC','YYYY-MM-DD'),
       COALESCE(NULLIF(u.model,''), '(unknown)'),
       COALESCE(u.profile_name,''),
       CASE WHEN EXISTS (
         SELECT 1 FROM llm_price_offpeak_windows w
         WHERE w.model = pr.model AND (
           CASE WHEN w.start_time < w.end_time
             THEN (u.ts AT TIME ZONE pr.offpeak_timezone)::time >= w.start_time
                  AND (u.ts AT TIME ZONE pr.offpeak_timezone)::time < w.end_time
             ELSE (u.ts AT TIME ZONE pr.offpeak_timezone)::time >= w.start_time
                  OR (u.ts AT TIME ZONE pr.offpeak_timezone)::time < w.end_time
           END
         )
       ) THEN 'offpeak' ELSE 'peak' END,
       COALESCE(SUM(u.input_tokens),0), COALESCE(SUM(u.output_tokens),0),
       COALESCE(SUM(u.cache_read),0), COALESCE(SUM(u.cache_write),0)
FROM llm_usage u
LEFT JOIN LATERAL (
  SELECT p.* FROM llm_prices p
  WHERE p.model = COALESCE(NULLIF(u.model,''), '(unknown)') OR p.model = 'default'
  ORDER BY (p.model = 'default') ASC
  LIMIT 1
) pr ON true
WHERE u.ts >= now() - ($1 * interval '1 day')
GROUP BY 1,2,3,4
ORDER BY 1`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []costUsageRow{}
	for rows.Next() {
		var r costUsageRow
		if err := rows.Scan(&r.Date, &r.Model, &r.Profile, &r.Period, &r.Input, &r.Output, &r.CacheRead, &r.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	for _, aggregate := range archived {
		for _, cold := range aggregate.TokenDaily {
			if cold.Date < cutoff {
				continue
			}
			out = append(out, costUsageRow{
				Date: cold.Date, Model: "(archived)", Profile: cold.ProfileName, Period: "peak",
				Input: int64(cold.InputTokens), Output: int64(cold.OutputTokens), CacheRead: int64(cold.CacheReadTokens),
			})
		}
	}
	return out, nil
}

func (d *DB) llmPriceMap() (map[string]LLMPrice, error) {
	prices, err := d.ListLLMPrices()
	if err != nil {
		return nil, err
	}
	out := map[string]LLMPrice{}
	for _, p := range prices {
		out[p.Model] = *p
	}
	return out, nil
}

func pricedFor(model string, prices map[string]LLMPrice, period string) (LLMPrice, bool) {
	p, exact := prices[model]
	if !exact {
		var ok bool
		p, ok = prices["default"]
		if !ok {
			return LLMPrice{}, false
		}
		if p.InputPerMillion <= 0 && p.OutputPerMillion <= 0 && p.CacheReadPerMillion <= 0 && p.CacheWritePerMillion <= 0 &&
			p.OffpeakInputPerMillion <= 0 && p.OffpeakOutputPerMillion <= 0 && p.OffpeakCacheReadPerMillion <= 0 && p.OffpeakCacheWritePerMillion <= 0 {
			return LLMPrice{}, false
		}
	}
	if period == "offpeak" && len(p.OffpeakWindows) > 0 {
		p.InputPerMillion = p.OffpeakInputPerMillion
		p.OutputPerMillion = p.OffpeakOutputPerMillion
		p.CacheReadPerMillion = p.OffpeakCacheReadPerMillion
		p.CacheWritePerMillion = p.OffpeakCacheWritePerMillion
	}
	return p, true
}

func addCost(t *CostTotals, r costUsageRow, p LLMPrice, priced bool) {
	t.InputTokens += r.Input
	t.OutputTokens += r.Output
	t.CacheReadTokens += r.CacheRead
	t.CacheWriteTokens += r.CacheWrite
	if !priced {
		t.UnpricedTokens += r.Input + r.Output
		return
	}
	uncached := r.Input - r.CacheRead
	if uncached < 0 {
		uncached = 0
	}
	cost := (float64(uncached)*p.InputPerMillion +
		float64(r.CacheRead)*p.CacheReadPerMillion +
		float64(r.CacheWrite)*p.CacheWritePerMillion +
		float64(r.Output)*p.OutputPerMillion) / 1e6
	t.CostUSD += cost
	if r.Period == "offpeak" {
		t.OffpeakCostUSD += cost
	} else {
		t.PeakCostUSD += cost
	}
	if p.CacheReadPerMillion < p.InputPerMillion {
		t.CacheSavingsUSD += float64(r.CacheRead) * (p.InputPerMillion - p.CacheReadPerMillion) / 1e6
	}
}

// CostSummary computes estimated spend from the live llm_usage ledger plus
// archived task aggregates. Cache-read tokens are treated as a subset of input;
// only the non-cached portion pays the input price.
func (d *DB) CostSummary(days int) (*CostSummary, error) {
	if days <= 0 {
		days = 30
	}
	prices, err := d.llmPriceMap()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	queryDays := days
	if elapsed := now.Day(); elapsed > queryDays {
		queryDays = elapsed + 1
	}
	rows, err := d.costRows(queryDays)
	if err != nil {
		return nil, err
	}
	cutoff := now.AddDate(0, 0, -days).Format("2006-01-02")
	dayMap := map[string]*CostDay{}
	modelMap := map[string]*CostModel{}
	profileMap := map[string]*CostProfile{}
	var total, month CostTotals
	for _, r := range rows {
		p, priced := pricedFor(r.Model, prices, r.Period)
		dayCost := CostTotals{}
		addCost(&dayCost, r, p, priced)
		if r.Date >= cutoff {
			day := dayMap[r.Date]
			if day == nil {
				day = &CostDay{Date: r.Date}
				dayMap[r.Date] = day
			}
			day.InputTokens += r.Input
			day.OutputTokens += r.Output
			day.CostUSD += dayCost.CostUSD
			day.UnpricedTokens += dayCost.UnpricedTokens
			addCost(&total, r, p, priced)
			m := modelMap[r.Model]
			if m == nil {
				m = &CostModel{Model: r.Model, Priced: priced}
				modelMap[r.Model] = m
			}
			m.InputTokens += r.Input
			m.OutputTokens += r.Output
			m.CacheReadTokens += r.CacheRead
			m.CostUSD += dayCost.CostUSD
			m.PeakCostUSD += dayCost.PeakCostUSD
			m.OffpeakCostUSD += dayCost.OffpeakCostUSD
			m.UnpricedTokens += dayCost.UnpricedTokens
			pr := profileMap[r.Profile]
			if pr == nil {
				pr = &CostProfile{ProfileName: r.Profile}
				profileMap[r.Profile] = pr
			}
			pr.InputTokens += r.Input
			pr.OutputTokens += r.Output
			pr.CostUSD += dayCost.CostUSD
			pr.UnpricedTokens += dayCost.UnpricedTokens
		}
		if r.Date >= monthStart.Format("2006-01-02") {
			addCost(&month, r, p, priced)
		}
	}

	out := &CostSummary{Days: days, Daily: []CostDay{}, ByModel: []CostModel{}, ByProfile: []CostProfile{}, Total: total, Month: month}
	for _, day := range dayMap {
		out.Daily = append(out.Daily, *day)
	}
	sort.Slice(out.Daily, func(i, j int) bool { return out.Daily[i].Date < out.Daily[j].Date })
	for _, m := range modelMap {
		out.ByModel = append(out.ByModel, *m)
	}
	sort.Slice(out.ByModel, func(i, j int) bool { return out.ByModel[i].CostUSD > out.ByModel[j].CostUSD })
	for _, p := range profileMap {
		out.ByProfile = append(out.ByProfile, *p)
	}
	sort.Slice(out.ByProfile, func(i, j int) bool { return out.ByProfile[i].CostUSD > out.ByProfile[j].CostUSD })

	if raw, ok, _ := d.GetSetting(settingLLMMonthlyBudget); ok {
		out.BudgetUSD, _ = strconv.ParseFloat(strings.TrimSpace(raw), 64)
	}
	out.AlertPct = 80
	if raw, ok, _ := d.GetSetting(settingLLMBudgetAlert); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 && n <= 100 {
			out.AlertPct = n
		}
	}
	switch {
	case out.BudgetUSD <= 0:
		out.BudgetStatus = "unset"
	case out.Month.CostUSD >= out.BudgetUSD:
		out.BudgetStatus = "exceeded"
	case out.Month.CostUSD*100 >= out.BudgetUSD*float64(out.AlertPct):
		out.BudgetStatus = "warning"
	default:
		out.BudgetStatus = "ok"
	}
	return out, nil
}
