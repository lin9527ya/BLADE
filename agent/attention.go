package agent

import (
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// attentionWindows adds a few deliberately small recall windows over the FULL
// graph. The main overview lists are newest-first and bounded; an old unresolved
// item can fall out of every window and never be seen again. These slots are
// attention aids, not a second ledger: each is capped at 5 and points at the
// normal node_detail / lead tools for content.
// stabilizeAgeMinutes buckets volatile elapsed-minute integers into 5-minute
// steps so consecutive requests within a window don't churn JSON fields and
// bust prompt caches. Values under 5 collapse to 0; >=5 floor to multiples of 5.
func stabilizeAgeMinutes(mins int) int {
	if mins < 5 {
		return 0
	}
	return (mins / 5) * 5
}

func ageMinutesStable(at time.Time) int {
	if at.IsZero() {
		return 0
	}
	return stabilizeAgeMinutes(int(time.Since(at).Minutes()))
}

func formatOldestUnconsumedFacts(facts []db.AttentionFact) []map[string]any {
	rows := make([]map[string]any, 0, len(facts))
	for _, f := range facts {
		row := map[string]any{
			"id": f.ID, "state": f.State, "summary": clipLine(f.Summary, 160), "age_min": ageMinutesStable(f.CreatedAt),
		}
		if f.Polarity != "" {
			row["polarity"] = f.Polarity
		}
		if f.Confidence != "" {
			row["confidence"] = f.Confidence
		}
		rows = append(rows, row)
	}
	return rows
}

func (t *ToolSet) attentionWindows(out map[string]any, tracking *db.LeadTraceReport) {
	if t == nil || t.ts == nil {
		return
	}
	errs := []string{}
	ageMin := ageMinutesStable

	if tracking != nil {
		leads := make([]map[string]any, 0, 5)
		for _, lead := range tracking.OldestPendingPreview {
			if len(leads) >= 5 {
				break
			}
			leads = append(leads, map[string]any{
				"id": lead.ID, "from_fact": lead.FactID, "text": clipLine(lead.Text, 160),
				"kind": lead.Kind, "state": lead.State, "age_min": ageMin(lead.FactCreatedAt),
			})
		}
		if len(leads) > 0 {
			out["attention_oldest_leads"] = leads
		}
	}

	if facts, err := t.ts.OldestUnconsumedFacts(5); err != nil {
		errs = append(errs, "oldest_facts: "+err.Error())
	} else if len(facts) > 0 {
		out["attention_oldest_facts"] = formatOldestUnconsumedFacts(facts)
	}

	if unresolved, err := t.ts.OldestUnresolved(5); err != nil {
		errs = append(errs, "oldest_unresolved: "+err.Error())
	} else if len(unresolved) > 0 {
		rows := make([]map[string]any, 0, len(unresolved))
		for _, u := range unresolved {
			rows = append(rows, map[string]any{
				"id": u.ID, "kind": u.Kind, "state": u.State,
				"summary": clipLine(u.Summary, 160), "age_min": ageMin(u.CreatedAt),
			})
		}
		out["attention_oldest_unresolved"] = rows
	}

	if len(errs) > 0 {
		out["attention_status"] = map[string]any{"state": "partial", "error": strings.Join(errs, "; ")}
		return
	}
	out["attention_status"] = map[string]any{"state": "complete"}
	if _, a := out["attention_oldest_leads"]; a {
		out["attention_note"] = "attention_* 是最老/久未复查的召回窗口（各最多 5 条），不是全量；内容用 node_detail / lead 工具查看。"
	}
}
