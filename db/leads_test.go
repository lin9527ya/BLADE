package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func traceStore(t *testing.T) (*DB, *ExplorationStore) {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask(fmt.Sprintf("trace-%d", time.Now().UnixNano()), "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	return d, d.Exploration(task.ExplorationID)
}
func traceFact(t *testing.T, s *ExplorationStore, texts ...string) int64 {
	t.Helper()
	leads := []map[string]any{}
	for _, text := range texts {
		leads = append(leads, map[string]any{"text": text})
	}
	id, err := s.AddNode(KindFact, map[string]any{"summary": "independent observations", "leads": leads}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func traceRow(t *testing.T, s *ExplorationStore, id string) LeadTrace {
	t.Helper()
	r, err := s.LeadTraceReport("", id, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 1 {
		t.Fatalf("missing trace %s: %+v", id, r)
	}
	return r.Rows[0]
}

func TestLeadTraceExactAdoptionAndExecution(t *testing.T) {
	_, s := traceStore(t)
	fact := traceFact(t, s, "first branch", "independent second branch")
	id, err := s.AddIntentGraph(map[string]any{"summary": "first branch"}, 5, nil, "planner", []IntentEdge{{From: fact, Rel: RelDerivedFrom}})
	if err != nil {
		t.Fatal(err)
	}
	lead := traceRow(t, s, ExplorationLeadID(fact, 0))
	if lead.State != "queued" || lead.AdoptedAt == nil || lead.Intents[0].Match != "exact" {
		t.Fatalf("wrong adoption: %+v", lead)
	}
	other := traceRow(t, s, ExplorationLeadID(fact, 1))
	if other.State != "pending" {
		t.Fatal("one child consumed an unrelated branch")
	}
	fr, err := s.FrontierForSlot("work#1", nil, DispatchBatchSize)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ClaimNode(t.Context(), id, "work#1", "open", IntentLeaseTTL, nil, true, fr[0].Schedule)
	if err != nil || n == nil {
		t.Fatalf("claim: %v", err)
	}
	owned := s.WithAuthority(n.WriteAuthority(0, s.ID()))
	if err := owned.RecordIntentStart(id); err != nil {
		t.Fatal(err)
	}
	lead = traceRow(t, s, ExplorationLeadID(fact, 0))
	if lead.State != "running" || lead.FirstStartedAt == nil || lead.Intents[0].LastDispatch == nil {
		t.Fatalf("missing execution trace: %+v", lead)
	}
	if err := owned.SettleIntent(id, "done", nil, ""); err != nil {
		t.Fatal(err)
	}
	lead = traceRow(t, s, ExplorationLeadID(fact, 0))
	if lead.State != "completed" || lead.Intents[0].CompletedAt == nil {
		t.Fatalf("missing final outcome: %+v", lead)
	}
	report, err := s.LeadTraceReport("", "", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.Total != 2 || report.Total != 2 || !report.HasMore || report.Stats.AdoptionSamples != 1 || report.Stats.ExecutionSamples != 1 {
		t.Fatalf("window polluted totals: %+v", report)
	}
}

func TestInvalidLeadAssociationRollsBackCreation(t *testing.T) {
	_, s := traceStore(t)
	fact := traceFact(t, s, "a")
	_, err := s.AddIntentGraph(map[string]any{"summary": "bad", "lead_ids": []string{ExplorationLeadID(fact, 3)}}, 5, nil, "planner", nil)
	if err == nil {
		t.Fatal("nonexistent branch accepted")
	}
	if count, err := s.CountByKind(KindIntent); err != nil || count != 0 {
		t.Fatal("invalid reference published a partial intent")
	}
	_, foreign := traceStore(t)
	other := traceFact(t, foreign, "foreign")
	if _, err := s.AddIntentGraph(map[string]any{"summary": "cross task", "lead_ids": []string{ExplorationLeadID(other, 0)}}, 5, nil, "planner", nil); err == nil {
		t.Fatal("cross-task trace contaminated current task")
	}
}

func TestLeadTraceDoesNotInferFromChildCountOrAmbiguousText(t *testing.T) {
	_, s := traceStore(t)
	a, b := traceFact(t, s, "same text"), traceFact(t, s, "same text")
	if _, err := s.AddIntentGraph(map[string]any{"summary": "same text"}, 5, nil, "planner", []IntentEdge{{From: a, Rel: RelDerivedFrom}, {From: b, Rel: RelDerivedFrom}}); err != nil {
		t.Fatal(err)
	}
	if row := traceRow(t, s, ExplorationLeadID(a, 0)); row.State != "pending" {
		t.Fatal("ambiguous exact matches were auto-adopted")
	}
	if _, err := s.AddIntentGraph(map[string]any{"summary": "a different question"}, 5, nil, "planner", []IntentEdge{{From: a, Rel: RelDerivedFrom}}); err != nil {
		t.Fatal(err)
	}
	if row := traceRow(t, s, ExplorationLeadID(a, 0)); row.State != "pending" {
		t.Fatal("parent relationship alone consumed the branch")
	}
}

func TestLegacyLeadAndOldIntentRemainVisible(t *testing.T) {
	_, s := traceStore(t)
	fact := traceFact(t, s, "old branch")
	key := ExplorationLeadID(fact, 0)
	_, err := s.AddIntent(map[string]any{"summary": "old intent", "converted_lead_ids": []string{key}}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 305; i++ {
		if _, err := s.AddIntent(map[string]any{"summary": fmt.Sprintf("new %d", i)}, 5, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	row := traceRow(t, s, key)
	if row.State != "queued" || len(row.Intents) != 1 || row.AdoptedAt != nil || row.Intents[0].LinkedAt != nil {
		t.Fatalf("legacy time invented or old association hidden: %+v", row)
	}
	report, err := s.LeadTraceReport("", "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.AdoptionSamples != 0 || report.Stats.AverageAdoptionSeconds != nil {
		t.Fatal("unknown history reported as zero latency")
	}
}

func TestEditedSourceAndFailedExecutionNeedReview(t *testing.T) {
	d, s := traceStore(t)
	fact := traceFact(t, s, "original")
	key := ExplorationLeadID(fact, 0)
	id, err := s.AddIntentGraph(map[string]any{"summary": "paraphrase", "lead_ids": []string{key}}, 5, nil, "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntentState(id, "no_output"); err != nil {
		t.Fatal(err)
	}
	if row := traceRow(t, s, key); row.State != "needs_follow_up" {
		t.Fatalf("failed run hidden: %+v", row)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET payload=jsonb_set(payload,'{leads,0,text}','"different branch"') WHERE id=$1`, fact); err != nil {
		t.Fatal(err)
	}
	if row := traceRow(t, s, key); row.State != "source_changed" || !row.Intents[0].SourceChanged {
		t.Fatal("edited source inherited old conclusions")
	}
}

func TestProposalTraceBackfillPreservesAllEntries(t *testing.T) {
	d, s := traceStore(t)
	proposals := []map[string]string{}
	for i := 0; i < 20; i++ {
		proposals = append(proposals, map[string]string{"summary": fmt.Sprintf("proposal %d", i)})
	}
	raw, _ := json.Marshal(map[string]any{"proposals": proposals})
	fact, err := s.AddNode(KindFact, map[string]any{"summary": "[意图提案]", "origin": "proposal", "detail": string(raw)}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.BackfillProposalEntries(); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("P%d-19", fact)
	if _, err := s.AddIntentGraph(map[string]any{"summary": "rewritten proposal", "lead_ids": []string{key}}, 5, nil, "planner", nil); err != nil {
		t.Fatal(err)
	}
	row := traceRow(t, s, key)
	if row.Kind != "proposal" || row.State != "queued" {
		t.Fatalf("proposal not tracked: %+v", row)
	}
	report, err := s.LeadTraceReport("", "", 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.Total != 20 || report.Stats.Pending != 19 {
		t.Fatal("proposal batch truncated to normal-lead limit")
	}
	for _, bad := range []string{"", "L0-0", "L1--1", "Q1-0", "Labc-0"} {
		if _, _, err := ParseExplorationLeadID(bad); err == nil {
			t.Fatal(strings.Join([]string{"bad id accepted", bad}, ": "))
		}
	}
}

func TestReconfirmingLegacyLinkCannotInventFirstAdoption(t *testing.T) {
	_, s := traceStore(t)
	fact := traceFact(t, s, "legacy")
	key := ExplorationLeadID(fact, 0)
	id, err := s.AddIntent(map[string]any{"summary": "old", "converted_lead_ids": []string{key}}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkIntentLeads(id, []string{key}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddIntentGraph(map[string]any{"summary": "new", "lead_ids": []string{key}}, 5, nil, "planner", nil); err != nil {
		t.Fatal(err)
	}
	row := traceRow(t, s, key)
	if row.AdoptedAt != nil {
		t.Fatal("later confirmation replaced an unknown first adoption time")
	}
	report, err := s.LeadTraceReport("", "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Stats.AdoptionSamples != 0 {
		t.Fatal("unknown first adoption entered latency samples")
	}
}
