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
	writeJSON(w, 200, summary)
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
