package agent

import (
	"context"
	"time"

	"github.com/lin9527ya/blade/db"
)

// A projection never mixes pooled reads, old caches and a newer event waterline.
func (t *ToolSet) snapshotProjection(fn func(*ToolSet) map[string]any) map[string]any {
	if t.ts == nil {
		return map[string]any{"read_status": db.ReadStatus{State: "unknown"}, "error": "exploration unavailable"}
	}
	if t.ts.InReadSnapshot() {
		return fn(t)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out map[string]any
	err := t.ts.ReadSnapshot(ctx, func(view *db.DB, store *db.ExplorationStore) error {
		copy := *t
		copy.ts = store
		if t.as != nil {
			copy.as = view.Assets()
		}
		if t.cs != nil {
			copy.cs = view.Companies()
		}
		copy.breadthLoaded = false
		copy.breadthCached = nil
		copy.coverageLoaded = false
		copy.coverageCached = nil
		copy.ledgerLoaded = false
		copy.ledgerCached = nil
		copy.chainsLoaded = false
		copy.chainsCached = nil
		out = fn(&copy)
		return nil
	})
	if err != nil {
		return map[string]any{"read_status": db.ReadStatus{State: "unknown"}, "error": "blackboard snapshot failed: " + err.Error(), "read_more": "graph_overview"}
	}
	return out
}
func (t *ToolSet) projectionContract(out map[string]any) {
	var ids []int64
	for _, key := range []string{"recent_facts", "footholds", "credentials", "open_intents"} {
		if rows, ok := out[key].([]map[string]any); ok {
			for _, r := range rows {
				if id, ok := r["id"].(int64); ok {
					ids = append(ids, id)
				}
			}
		}
	}
	if states, err := t.ts.EvidenceStates(ids); err == nil {
		for _, key := range []string{"recent_facts", "footholds", "credentials", "open_intents"} {
			if rows, ok := out[key].([]map[string]any); ok {
				for _, r := range rows {
					if id, ok := r["id"].(int64); ok {
						r["evidence_state"] = states[id]
					}
				}
			}
		}
	}

	sections := map[string]db.ReadStatus{}
	for key, kind := range map[string]string{"goals": db.KindGoal, "hints": db.KindHint, "recent_facts": db.KindFact, "finding_list": db.KindFinding, "footholds": db.KindFoothold, "credentials": db.KindCredential, "open_intents": db.KindIntent, "running_intents": db.KindIntent, "paused_intents": db.KindIntent, "recent_done_intents": db.KindIntent} {
		state := ""
		switch key {
		case "hints":
			state = db.StateHintActive
		case "footholds", "credentials":
			state = db.StateLootActive
		case "open_intents":
			state = "open"
		case "running_intents":
			state = "running"
		case "paused_intents":
			state = "paused"
		}
		total, err := t.ts.CountProjectionNodes(kind, state, key == "recent_done_intents")
		n := 0
		if rows, ok := out[key].([]map[string]any); ok {
			n = len(rows)
		}
		if err != nil {
			sections[key] = db.ReadStatus{State: "unknown"}
		} else {
			sections[key] = db.ReadSetStatus(total, n, "node_detail / graph_overview(detail=true)")
		}
	}
	out["read_status"] = map[string]any{"state": "complete", "sections": sections, "note": "complete表示本快照读取成功，各节窗口完整性独立标注；不是探索已完成"}
	if v, err := t.ts.BlackboardVersion(context.Background()); err == nil {
		out["revision"] = v.RevisionString()
		out["change_cursor"] = v.Cursor()
	}
}
