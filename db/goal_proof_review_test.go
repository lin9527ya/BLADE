package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func reviewNode(t *testing.T, s *ExplorationStore, kind, state string) int64 {
	t.Helper()
	id, err := s.AddNode(kind, map[string]any{"summary": "material sentinel"}, 0, state, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func reviewProve(t *testing.T, s *ExplorationStore, goal, evidence int64) {
	t.Helper()
	if _, err := s.ProveGoal(goal, evidence, "review rationale"); err != nil {
		t.Fatal(err)
	}
}

func reviewPage(t *testing.T, s *ExplorationStore, goal int64) []GoalProofDetail {
	t.Helper()
	page, err := s.GoalProofs(t.Context(), goal, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	return page.Proofs
}

func checkReviewSummary(t *testing.T, s *ExplorationStore, goal int64, want GoalProofSummary) {
	t.Helper()
	rows, err := s.GoalProofSummaries(t.Context(), []int64{goal})
	if err != nil || rows[goal] != want {
		t.Fatalf("summary=%+v want=%+v err=%v", rows, want, err)
	}
}

func TestGoalProofReviewTracksChangesAndGoalRevision(t *testing.T) {
	d, s := intentLeaseFixture(t)
	goal := reviewNode(t, s, KindGoal, "open")
	a := reviewNode(t, s, KindFact, "confirmed")
	b := reviewNode(t, s, KindFact, "confirmed")
	reviewProve(t, s, goal, a)
	reviewProve(t, s, goal, b)
	if _, err := d.Exec(`UPDATE exploration_nodes SET payload=payload, priority=priority+1 WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	checkReviewSummary(t, s, goal, GoalProofSummary{Total: 2})
	first, err := s.GoalProofs(t.Context(), goal, 0, 1)
	if err != nil || len(first.Proofs) != 1 || first.Proofs[0].EvidenceID != b || first.NextBefore != strconv.FormatInt(b, 10) {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	next, err := s.GoalProofs(t.Context(), goal, b, 1)
	if err != nil || len(next.Proofs) != 1 || next.Proofs[0].EvidenceID != a || next.NextBefore != "" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	if err := s.SetNodePayload(a, KindFact, map[string]any{"summary": "edited", "evidence_meta": map[string]string{"valid_until": "2000-01-01T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{RelSupersedes, RelContradicts} {
		if err := s.LinkEvidenceRelation(b, rel, a); err != nil {
			t.Fatal(err)
		}
	}
	p := reviewPage(t, s, goal)[1]
	if !slices.Equal(p.Changes, []string{"material_changed"}) || !slices.Equal(p.CurrentFlags, []string{"expired", "contested", "superseded"}) || p.Reason != "review rationale" {
		t.Fatalf("edited=%+v", p)
	}
	if err := s.Unlink(a, RelProves, goal); err != nil {
		t.Fatal(err)
	}
	if p = reviewPage(t, s, goal)[1]; !slices.Contains(p.Changes, "relation_removed") || p.Reason != "" {
		t.Fatal(p)
	}
	if _, err := d.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	checkReviewSummary(t, s, goal, GoalProofSummary{Total: 2, NeedsReview: 1})
	p = reviewPage(t, s, goal)[1]
	if p.EvidenceID != a || p.ProvedAt == nil || !slices.Equal(p.Changes, []string{"evidence_removed"}) {
		t.Fatal(p)
	}
	if err := s.UpdateGoalPayload(goal, "edited goal", ""); err != nil {
		t.Fatal(err)
	}
	reviewProve(t, s, goal, b)
	reviewProve(t, s, goal, b)
	checkReviewSummary(t, s, goal, GoalProofSummary{Total: 1})
	if err := s.DeleteGoal(goal); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM goal_proof_refs WHERE goal_id=$1`, goal).Scan(&count); err != nil || count != 0 {
		t.Fatalf("remaining=%d err=%v", count, err)
	}
}

func TestGoalProofReviewFindingAndSourceVisibility(t *testing.T) {
	d, s := intentLeaseFixture(t)
	src, err := d.CreateTask("review source", "observation", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(src.ID) })
	taskID, err := s.TaskID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, taskID, src.ID); err != nil {
		t.Fatal(err)
	}
	source := d.Exploration(src.ExplorationID)
	ev := reviewNode(t, source, KindFinding, "confirmed")
	goal := reviewNode(t, s, KindGoal, "open")
	var finding int64
	if err = d.QueryRow(`INSERT INTO findings(task_id,node_id,evidence,status) VALUES($1,$2,'private evidence body','pending') RETURNING id`, src.ID, ev).Scan(&finding); err != nil {
		t.Fatal(err)
	}
	reviewProve(t, s, goal, ev)
	if _, err = d.Exec(`UPDATE findings SET evidence_version=evidence_version+1,status='retracted' WHERE id=$1`, finding); err != nil {
		t.Fatal(err)
	}
	p := reviewPage(t, s, goal)[0]
	if !slices.Equal(p.Changes, []string{"finding_evidence_changed", "finding_status_changed"}) {
		t.Fatal(p)
	}
	raw, _ := json.Marshal(p)
	if strings.Contains(string(raw), "private evidence body") || strings.Contains(string(raw), "material sentinel") {
		t.Fatalf("raw material in %s", raw)
	}
	if _, err := s.ProveGoal(goal, ev, "copied private evidence body"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE findings SET evidence='replacement',evidence_version=evidence_version+1 WHERE id=$1`, finding); err != nil {
		t.Fatal(err)
	}
	if p := reviewPage(t, s, goal)[0]; p.Reason != "" {
		t.Fatalf("quoted evidence exposed after replacement: %+v", p)
	}
	reviewProve(t, s, goal, ev)
	checkReviewSummary(t, s, goal, GoalProofSummary{Total: 1})
	if _, err = d.Exec(`DELETE FROM task_relations WHERE task_id=$1 AND source_task_id=$2`, taskID, src.ID); err != nil {
		t.Fatal(err)
	}
	p = reviewPage(t, s, goal)[0]
	raw, _ = json.Marshal(p)
	var public map[string]any
	if err = json.Unmarshal(raw, &public); err != nil {
		t.Fatal(err)
	}
	if len(public) != 3 || p.EvidenceID != ev || p.ProvedAt == nil || !slices.Equal(p.Changes, []string{"source_unavailable"}) {
		t.Fatalf("revoked=%s", raw)
	}
	checkReviewSummary(t, s, goal, GoalProofSummary{Total: 1, NeedsReview: 1})
}

func TestGoalProofReviewLegacyBaselines(t *testing.T) {
	d, s := intentLeaseFixture(t)
	goal := reviewNode(t, s, KindGoal, "open")
	ev := reviewNode(t, s, KindFact, "confirmed")
	removed := reviewNode(t, s, KindFact, "confirmed")
	reviewProve(t, s, goal, removed)
	reviewProve(t, s, goal, ev)
	if _, err := d.Exec(`DELETE FROM goal_proof_refs WHERE goal_id=$1`, goal); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, removed); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := d.Exec(`SELECT backfill_goal_proof_refs($1)`, s.ID()); err != nil {
			t.Fatal(err)
		}
	}
	checkReviewSummary(t, s, goal, GoalProofSummary{Total: 2, HistoryUnknown: 2, NeedsReview: 1})
	items := reviewPage(t, s, goal)
	if !items[1].HistoryUnknown || len(items[1].Changes) != 0 || items[0].EvidenceID != removed || items[0].ProvedAt == nil {
		t.Fatalf("legacy=%+v", items)
	}
	reviewProve(t, s, goal, ev)
	checkReviewSummary(t, s, goal, GoalProofSummary{Total: 2, NeedsReview: 1, HistoryUnknown: 1})
}

func TestGoalProofReviewRollbackAndConcurrentEdit(t *testing.T) {
	d, s := intentLeaseFixture(t)
	goal := reviewNode(t, s, KindGoal, "open")
	ev := reviewNode(t, s, KindFact, "confirmed")
	// Fail the last insert so the goal, edge and journal must all roll back.
	const drop = "ALTER TABLE goal_proof_refs DROP CONSTRAINT IF EXISTS test_goal_proof_rollback"
	if _, err := d.Exec(fmt.Sprintf("ALTER TABLE goal_proof_refs ADD CONSTRAINT test_goal_proof_rollback CHECK (goal_id <> %d) NOT VALID", goal)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(drop) })
	version, err := s.BlackboardVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ProveGoal(goal, ev, "rollback"); err == nil {
		t.Fatal("expected failure")
	}
	current, err := s.BlackboardVersion(t.Context())
	if err != nil || version != current {
		t.Fatalf("version=%v err=%v", current, err)
	}
	node, err := s.GetNode(goal)
	if err != nil || node.State != "open" {
		t.Fatalf("node=%+v err=%v", node, err)
	}
	if _, err = d.Exec(drop); err != nil {
		t.Fatal(err)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var pid int
	if err = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE exploration_nodes SET payload='{"summary":"new material"}' WHERE id=$1`, ev); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.ProveGoal(goal, ev, "after concurrent edit"); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err = d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("proof did not wait for evidence transaction")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	checkReviewSummary(t, s, goal, GoalProofSummary{Total: 1})
}

func TestGoalProofReviewArchiveRoundTrip(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_%v", legacy), func(t *testing.T) {
			d, s := intentLeaseFixture(t)
			if err := d.EnsureLLMRecordsTable(); err != nil {
				t.Fatal(err)
			}
			if err := d.EnsureLLMUsageTable(); err != nil {
				t.Fatal(err)
			}
			task, err := s.TaskID()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task) })
			src, err := d.CreateTask("archived source", "review", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.DeleteTask(src.ID) })
			if _, err = d.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, task, src.ID); err != nil {
				t.Fatal(err)
			}
			goal := reviewNode(t, s, KindGoal, "open")
			ev := reviewNode(t, s, KindFact, "confirmed")
			inherited := reviewNode(t, d.Exploration(src.ExplorationID), KindFact, "confirmed")
			reviewProve(t, s, goal, ev)
			reviewProve(t, s, goal, inherited)
			if err := s.SetNodePayload(ev, KindFact, map[string]any{"summary": "revised"}); err != nil {
				t.Fatal(err)
			}
			if err = d.SetPaused(task, true); err != nil {
				t.Fatal(err)
			}
			archive, err := d.QueueTaskArchive(task)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
				t.Fatal(err)
			}
			snapshot, err := d.SnapshotTaskArchive(task)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.DataCounts["goal_proof_refs"] != 2 {
				t.Fatal(snapshot.DataCounts)
			}
			if err := s.SetNodePayload(ev, KindFact, map[string]any{"summary": "changed during packaging"}); err != nil {
				t.Fatal(err)
			}
			if err := d.CompleteTaskArchive(archive.ID, snapshot, "review-test.tar", "test", 1, 1); !errors.Is(err, ErrTaskArchiveState) {
				t.Fatalf("compacted a stale proof snapshot: %v", err)
			}
			snapshot, err = d.SnapshotTaskArchive(task)
			if err != nil {
				t.Fatal(err)
			}
			if err = d.CompleteTaskArchive(archive.ID, snapshot, "review-test.tar", "test", 1, 1); err != nil {
				t.Fatal(err)
			}
			if err = d.DeleteTask(src.ID); err != nil {
				t.Fatal(err)
			}
			if legacy {
				snapshot.FormatVersion = 4
				delete(snapshot.Tables, "goal_proof_refs")
				delete(snapshot.DataCounts, "goal_proof_refs")
				nodes, err := decodeArchiveRows(snapshot.Tables["exploration_nodes"])
				if err != nil {
					t.Fatal(err)
				}
				for _, node := range nodes {
					delete(node, "proof_material_version")
				}
				snapshot.Tables["exploration_nodes"], err = json.Marshal(nodes)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = d.QueueTaskArchiveRestore(archive.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = d.ClaimTaskArchiveJob(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = d.RestoreTaskArchive(archive.ID, snapshot, 0); err != nil {
				t.Fatal(err)
			}
			proofs := reviewPage(t, s, goal)
			if len(proofs) != 2 || proofs[0].EvidenceID != inherited || !slices.Equal(proofs[0].Changes, []string{"source_unavailable"}) || proofs[0].Reason != "" {
				t.Fatalf("restored=%+v", proofs)
			}
			if proofs[1].HistoryUnknown != legacy || slices.Contains(proofs[1].Changes, "material_changed") == legacy {
				t.Fatalf("restored local evidence=%+v", proofs[1])
			}
		})
	}
}
