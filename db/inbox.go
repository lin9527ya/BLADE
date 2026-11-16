package db

import (
	"fmt"
	"time"
)

// InboxItem is one operator-facing notification. Items are derived from the
// existing event/approval/task/schedule facts; there is no second event bus to
// keep in sync.
type InboxItem struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // approval | task | schedule | finding
	Title     string    `json:"title"`
	Subtitle  string    `json:"subtitle,omitempty"`
	URL       string    `json:"url"`
	Severity  string    `json:"severity,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// ListInboxItems returns the newest inbox facts. The window keeps a stale
// backlog from turning the bell into an archive: seven days is enough to catch
// a missed approval/task while the dedicated pages remain the full history.
func (d *DB) ListInboxItems(limit int) ([]InboxItem, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := d.Query(`
SELECT id, kind, title, subtitle, url, severity, created_at FROM (
  SELECT 'approval-' || id::text AS id, 'approval' AS kind,
         tool_name AS title,
         concat_ws(' · ', NULLIF(agent_name,''), NULLIF(reason,'')) AS subtitle,
         '/system/intercept/approvals' AS url,
         '' AS severity, created_at
  FROM intercept_pending WHERE status='pending'

  UNION ALL

  SELECT 'task-' || id::text, 'task',
         COALESCE(NULLIF(name,''), NULLIF(description,''), '任务 #' || id::text),
         CASE status WHEN 'done' THEN '任务已完成' WHEN 'failed' THEN '任务失败' WHEN 'timeout' THEN '任务已超时' ELSE status END,
         '/function/tasks/detail?id=' || id::text,
         '', completed_at
  FROM tasks
  WHERE deleted_at IS NULL AND status IN ('done','failed','timeout')
    AND completed_at >= now() - interval '7 days'

  UNION ALL

  SELECT 'schedule-' || r.id::text, 'schedule',
         COALESCE(NULLIF(s.name,''), '计划任务 #' || s.id::text),
         '执行失败：' || left(COALESCE(NULLIF(r.error,''), '未知错误'), 200),
         '/function/schedules',
         '', COALESCE(r.updated_at, r.scheduled_at)
  FROM schedule_runs r
  LEFT JOIN task_schedules s ON s.id=r.schedule_id
  WHERE r.state='failed' AND COALESCE(r.updated_at, r.scheduled_at) >= now() - interval '7 days'

  UNION ALL

  SELECT 'finding-' || e.id::text, 'finding',
         COALESCE(NULLIF(e.snapshot->>'name',''), NULLIF(e.snapshot->>'vulnclass',''), '发现 #' || e.finding_id::text),
         concat_ws(' · ', NULLIF(e.snapshot->>'severity',''),
           CASE e.kind WHEN 'finding_created' THEN '新发现' ELSE '状态变更' END),
         '/function/findings/detail?id=' || e.finding_id::text,
         COALESCE(e.snapshot->>'severity',''), e.created_at
  FROM notification_events e
  WHERE e.finding_id > 0 AND e.created_at >= now() - interval '7 days'
) items
ORDER BY created_at DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxItem{}
	for rows.Next() {
		var item InboxItem
		if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.Subtitle, &item.URL, &item.Severity, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if item := d.budgetInboxItem(); item != nil {
		// Keep the budget alert at the top: it is a current-state warning, not a
		// historical event, and it must not be buried under routine findings.
		out = append([]InboxItem{*item}, out...)
	}
	return out, nil
}

func (d *DB) budgetInboxItem() *InboxItem {
	summary, err := d.CostSummary(31)
	if err != nil || summary.BudgetStatus != "warning" && summary.BudgetStatus != "exceeded" {
		return nil
	}
	percent := 0.0
	if summary.BudgetUSD > 0 {
		percent = summary.Month.CostUSD / summary.BudgetUSD * 100
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return &InboxItem{
		ID:        "budget-" + start.Format("2006-01-02"),
		Kind:      "budget",
		Title:     "LLM 预算告警",
		Subtitle:  fmt.Sprintf("本月 $%.2f / 预算 $%.2f（%.0f%%）", summary.Month.CostUSD, summary.BudgetUSD, percent),
		URL:       "/system/llm",
		Severity:  "high",
		CreatedAt: start,
	}
}
