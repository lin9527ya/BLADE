package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestOptionalLeadIDsCreateAndTraceWithoutExtraParents(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	props := tools.linkIntentSource().InputSchema()["properties"].(map[string]any)
	if props["lead_ids"] == nil {
		t.Fatal("link schema hides optional branch ids")
	}
	fact, err := ts.AddNode(db.KindFact, map[string]any{"summary": "branches", "leads": []map[string]string{{"text": "specific next direction"}, {"text": "another independent branch"}}}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	out := callTool(t, tools.PlannerTools(), "add_intent", fmt.Sprintf(`{"summary":"rewritten next direction","lead_ids":["L%d-0"]}`, fact))
	if !strings.HasPrefix(out, "intent created:") {
		t.Fatalf("optional reference rejected: %s", out)
	}
	view := tools.graphOverviewData()
	stats, ok := view["lead_tracking"].(db.LeadTraceStats)
	if !ok || stats.Adopted != 1 || stats.Pending != 1 {
		t.Fatalf("tracking wrong: %+v", view["lead_tracking"])
	}
	pending := view["recent_leads"].([]map[string]any)
	if len(pending) != 1 || pending[0]["id"] != db.ExplorationLeadID(fact, 1) {
		t.Fatal("independent pending branch hidden")
	}
	id := lootNodeID(t, out)
	linked := callTool(t, tools.PlannerTools(), "link_intent_source", fmt.Sprintf(`{"intent_id":%d,"lead_ids":["L%d-1"]}`, id, fact))
	if !strings.Contains(linked, "已补") {
		t.Fatalf("cannot link existing intent: %s", linked)
	}
	report, err := ts.LeadTraceReport("", "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.Adopted != 2 {
		t.Fatal("explicit linkage not persisted")
	}
}
