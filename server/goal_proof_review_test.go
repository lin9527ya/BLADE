package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

func TestGoalProofReviewEndpoints(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	manager := &Manager{tasks: map[string]*Task{}}
	for _, key := range []string{"owner", "other"} {
		exp, err := d.CreateExploration("review endpoint", "verify observation")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, exp) })
		manager.tasks[key] = &Task{ID: key, ExpID: exp, Store: d.Exploration(exp)}
	}
	store := manager.tasks["owner"].Store
	goal, err := store.AddGoal(map[string]any{"text": "check evidence"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AddGoal(map[string]any{"text": "still open"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "payload-only-sentinel"
	for _, kind := range []string{db.KindCredential, db.KindFact} {
		state := "confirmed"
		reason := "verified by inspection"
		if kind == db.KindCredential {
			state = "active"
			reason = secret
		}
		ev, err := store.AddNode(kind, map[string]any{"value": secret}, 0, state, "test", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.ProveGoal(goal, ev, reason); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	(&Server{m: manager}).registerWorkspaceRoutes(mux)
	get := func(path string, want int) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want || strings.Contains(w.Body.String(), secret) {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	var result struct {
		Goals []GoalDTO `json:"goals"`
	}
	if err = json.Unmarshal(get("/api/tasks/owner/goals", 200), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Goals) != 2 || result.Goals[0].ProofReview != nil || result.Goals[1].ProofReview == nil || result.Goals[1].ProofReview.Total != 2 {
		t.Fatalf("goals=%+v", result.Goals)
	}
	path := fmt.Sprintf("/api/tasks/owner/goals/%d/proofs", goal)
	var first, second db.GoalProofPage
	if err = json.Unmarshal(get(path+"?limit=1", 200), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Proofs) != 1 || first.NextBefore != strconv.FormatInt(first.Proofs[0].EvidenceID, 10) || first.Proofs[0].Reason != "verified by inspection" {
		t.Fatal(first)
	}
	if err = json.Unmarshal(get(path+"?limit=1&before="+first.NextBefore, 200), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Proofs) != 1 || second.NextBefore != "" || second.Proofs[0].Reason != "" || second.Proofs[0].Kind != db.KindCredential {
		t.Fatal(second)
	}
	get(strings.Replace(path, "/owner/", "/other/", 1), 404)
	get(path+"?limit=0", 400)
	get(path+"?before=invalid", 400)
}
