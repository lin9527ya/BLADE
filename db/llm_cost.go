package db

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LLMPrice is USD per million tokens for one model. model="default" is the
// fallback for models without an exact row.
type LLMPrice struct {
	Model                string    `json:"model"`
	InputPerMillion      float64   `json:"input_per_million"`
	OutputPerMillion     float64   `json:"output_per_million"`
	CacheReadPerMillion  float64   `json:"cache_read_per_million"`
	CacheWritePerMillion float64   `json:"cache_write_per_million"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type CostTotals struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	CacheSavingsUSD  float64 `json:"cache_savings_usd"`
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
	rows, err := d.Query(`SELECT model,input_per_million,output_per_million,cache_read_per_million,cache_write_per_million,updated_at
FROM llm_prices ORDER BY (model='default') DESC, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*LLMPrice{}
	for rows.Next() {
		var p LLMPrice
		if err := rows.Scan(&p.Model, &p.InputPerMillion, &p.OutputPerMillion, &p.CacheReadPerMillion, &p.CacheWritePerMillion, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &p)
	}
	return out, rows.Err()
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
		for _, v := range []float64{item.InputPerMillion, item.OutputPerMillion, item.CacheReadPerMillion, item.CacheWritePerMillion} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e7 {
				return fmt.Errorf("模型 %q 的价格必须是 0–1e7 的非负数", model)
			}
		}
		if _, err := tx.Exec(`INSERT INTO llm_prices(model,input_per_million,output_per_million,cache_read_per_million,cache_write_per_million)
VALUES($1,$2,$3,$4,$5)`, model, item.InputPerMillion, item.OutputPerMillion, item.CacheReadPerMillion, item.CacheWritePerMillion); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type costUsageRow struct {
	Date       string
	Model      string
	Profile    string
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
SELECT to_char(ts AT TIME ZONE 'UTC','YYYY-MM-DD'),
       COALESCE(NULLIF(model,''), '(unknown)'),
       COALESCE(profile_name,''),
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
WHERE ts >= now() - ($1 * interval '1 day')
GROUP BY 1,2,3
ORDER BY 1`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []costUsageRow{}
	for rows.Next() {
		var r costUsageRow
		if err := rows.Scan(&r.Date, &r.Model, &r.Profile, &r.Input, &r.Output, &r.CacheRead, &r.CacheWrite); err != nil {
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
				Date: cold.Date, Model: "(archived)", Profile: cold.ProfileName,
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

func pricedFor(model string, prices map[string]LLMPrice) (LLMPrice, bool) {
	if p, ok := prices[model]; ok {
		return p, true
	}
	if p, ok := prices["default"]; ok {
		if p.InputPerMillion > 0 || p.OutputPerMillion > 0 || p.CacheReadPerMillion > 0 || p.CacheWritePerMillion > 0 {
			return p, true
		}
	}
	return LLMPrice{}, false
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
	t.CostUSD += (float64(uncached)*p.InputPerMillion +
		float64(r.CacheRead)*p.CacheReadPerMillion +
		float64(r.CacheWrite)*p.CacheWritePerMillion +
		float64(r.Output)*p.OutputPerMillion) / 1e6
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
		p, priced := pricedFor(r.Model, prices)
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
