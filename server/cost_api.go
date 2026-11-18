package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

const (
	settingLLMMonthlyBudget = "llm_monthly_budget_usd"
	settingLLMBudgetAlert   = "llm_budget_alert_pct"
)

func (s *Server) costSummary(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	days := queryInt(r, "days", 30)
	if days <= 0 {
		days = 30
	}
	if days > 3660 {
		days = 3660
	}
	summary, err := pg.CostSummary(days)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSONETag(w, r, 200, summary)
}

func (s *Server) listLLMPrices(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	items, err := pg.ListLLMPrices()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) saveLLMPrices(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		Items []db.LLMPrice `json:"items"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if err := pg.ReplaceLLMPrices(req.Items); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	items, err := pg.ListLLMPrices()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func llmBudgetSettings(pg *db.DB) (float64, int) {
	budget := 0.0
	if raw, ok, _ := pg.GetSetting(settingLLMMonthlyBudget); ok {
		budget, _ = strconv.ParseFloat(strings.TrimSpace(raw), 64)
	}
	alert := 80
	if raw, ok, _ := pg.GetSetting(settingLLMBudgetAlert); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 && n <= 100 {
			alert = n
		}
	}
	return budget, alert
}

func (s *Server) listCostBudgets(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	items, err := pg.ListCostBudgets()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) saveCostBudget(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		ScopeType string  `json:"scope_type"`
		ScopeID   int64   `json:"scope_id"`
		BudgetUSD float64 `json:"budget_usd"`
		AlertPct  int     `json:"alert_pct"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	item, err := pg.UpsertCostBudget(req.ScopeType, req.ScopeID, req.BudgetUSD, req.AlertPct)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, item)
}

func (s *Server) deleteCostBudget(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	scopeType := r.PathValue("scope")
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad scope id")
		return
	}
	if err := pg.DeleteCostBudget(scopeType, id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
