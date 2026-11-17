package db

import (
	"fmt"
	"strings"
	"time"
)

// CostBudget is a company- or campaign-scoped monthly LLM budget. scope_id is a
// soft reference: deleting a company/campaign leaves the row for audit, and the
// read side simply ignores it.
type CostBudget struct {
	ScopeType string    `json:"scope_type"`
	ScopeID   int64     `json:"scope_id"`
	BudgetUSD float64   `json:"budget_usd"`
	AlertPct  int       `json:"alert_pct"`
	UpdatedAt time.Time `json:"updated_at"`
}

func normalizeCostBudgetScope(scopeType string) (string, error) {
	scopeType = strings.ToLower(strings.TrimSpace(scopeType))
	if scopeType != "company" && scopeType != "campaign" {
		return "", fmt.Errorf("scope_type 必须是 company 或 campaign")
	}
	return scopeType, nil
}

func (d *DB) ListCostBudgets() ([]*CostBudget, error) {
	rows, err := d.Query(`SELECT scope_type, scope_id, budget_usd, alert_pct, updated_at
FROM cost_budgets ORDER BY scope_type, scope_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*CostBudget{}
	for rows.Next() {
		var item CostBudget
		if err := rows.Scan(&item.ScopeType, &item.ScopeID, &item.BudgetUSD, &item.AlertPct, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &item)
	}
	return out, rows.Err()
}

func (d *DB) UpsertCostBudget(scopeType string, scopeID int64, budgetUSD float64, alertPct int) (*CostBudget, error) {
	st, err := normalizeCostBudgetScope(scopeType)
	if err != nil {
		return nil, err
	}
	if scopeID <= 0 {
		return nil, fmt.Errorf("scope_id 必须是正数")
	}
	if budgetUSD < 0 || budgetUSD > 1e9 {
		return nil, fmt.Errorf("预算必须在 0–1e9 之间")
	}
	if alertPct <= 0 || alertPct > 100 {
		return nil, fmt.Errorf("告警阈值必须在 1–100 之间")
	}
	var item CostBudget
	err = d.QueryRow(`INSERT INTO cost_budgets(scope_type,scope_id,budget_usd,alert_pct)
VALUES($1,$2,$3,$4)
ON CONFLICT (scope_type, scope_id) DO UPDATE SET
  budget_usd=EXCLUDED.budget_usd, alert_pct=EXCLUDED.alert_pct, updated_at=now()
RETURNING scope_type, scope_id, budget_usd, alert_pct, updated_at`,
		st, scopeID, budgetUSD, alertPct).Scan(&item.ScopeType, &item.ScopeID, &item.BudgetUSD, &item.AlertPct, &item.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (d *DB) DeleteCostBudget(scopeType string, scopeID int64) error {
	st, err := normalizeCostBudgetScope(scopeType)
	if err != nil {
		return err
	}
	_, err = d.Exec(`DELETE FROM cost_budgets WHERE scope_type=$1 AND scope_id=$2`, st, scopeID)
	return err
}
