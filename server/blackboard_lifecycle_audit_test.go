package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func openLifecycleTestManager(t *testing.T) *Manager {
	t.Helper()
	if _, _, err := db.DSN(); err != nil {
		t.Skip("no test database configured")
	}
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func TestTriggerMidPriorityBatchAndCriticalImmediate(t *testing.T) {
	tk := &Task{ID: "9001", notify: make(chan struct{}, 1)}
	t.Cleanup(tk.StopTimers)

	// Leading edge: first mid-priority event wakes immediately.
	tk.NotifyDone(1, "done")
	select {
	case <-tk.notify:
	default:
		t.Fatal("leading mid-priority trigger did not wake immediately")
	}

	// Second mid-priority inside 5s cooldown must be buffered, not immediately waking.
	tk.NotifyFact("capability_fact", 1, 10, "discovered admin panel", nil)
	select {
	case <-tk.notify:
		t.Fatal("2nd mid-priority inside cooldown should coalesce instead of waking immediately")
	default:
	}

	// Critical breakthroughs inside cooldown bypass coalescing and wake immediately.
	tk.NotifyFact("contradiction", 1, 11, "refuted earlier claim", nil)
	select {
	case <-tk.notify:
	default:
		t.Fatal("critical breakthrough inside cooldown must wake immediately")
	}

	evs := tk.drainTriggers()
	if len(evs) != 3 {
		t.Fatalf("expected all 3 triggers retained across coalesce/drain, got %d: %+v", len(evs), evs)
	}

	// Burst >= midPriorityMaxBatch inside cooldown triggers early batch flush.
	tk2 := &Task{ID: "9001b", notify: make(chan struct{}, 1)}
	t.Cleanup(tk2.StopTimers)
	tk2.NotifyDone(2, "done")
	select {
	case <-tk2.notify:
	default:
		t.Fatal("leading burst trigger on fresh task should wake immediately")
	}
	for i := 0; i < midPriorityMaxBatch; i++ {
		tk2.NotifyFact("lead_fact", 2, int64(20+i), "lead burst", nil)
	}
	select {
	case <-tk2.notify:
	default:
		t.Fatal("reaching midPriorityMaxBatch inside cooldown must flush wake immediately")
	}
	if got := tk2.drainTriggers(); len(got) != 1+midPriorityMaxBatch {
		t.Fatalf("expected %d retained burst triggers, got %d", 1+midPriorityMaxBatch, len(got))
	}
}

func TestAcquirePrefixWarmSlotPerIntentNoStall(t *testing.T) {
	t.Setenv("BLADE_WORKER_PER_INTENT_SESSION", "true")
	tk := &Task{ID: "9002", notify: make(chan struct{}, 1)}

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tk.acquirePrefixWarmSlot(context.Background()) {
				t.Error("expected PerIntentWorkerSession to skip warm slot serialization")
			}
		}()
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("acquirePrefixWarmSlot stalled for %v under PerIntentWorkerSession", elapsed)
	}
}

func TestEditMetGoalReopensAndApplyIntentResumeRejectsTerminal(t *testing.T) {
	m := openLifecycleTestManager(t)
	s := newAdmissionTestServer(m, nil)

	task, err := m.CreateTask(fmt.Sprintf("goal-reopen-%d", time.Now().UnixNano()), "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteTask(tidOfTask(task)) })

	gid, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "initial target"}, 0, "met", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeNode, err := task.Store.GetNode(gid)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"text": "updated objective", "vulnclass": "sqli"})
	req := httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/tasks/%s/goals/%d", task.ID, gid), bytes.NewReader(body))
	req.SetPathValue("id", task.ID)
	req.SetPathValue("gid", strconv.FormatInt(gid, 10))
	rec := httptest.NewRecorder()
	s.editGoal(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("editGoal status=%d body=%s", rec.Code, rec.Body.String())
	}

	reopened, err := task.Store.GetNode(gid)
	if err != nil || reopened == nil {
		t.Fatalf("GetNode after editGoal: %v", err)
	}
	if reopened.State != "open" {
		t.Fatalf("edited met goal state=%q, want open", reopened.State)
	}
	if reopened.ContentVersion <= beforeNode.ContentVersion {
		t.Fatalf("edited goal content_version=%d, want > %d", reopened.ContentVersion, beforeNode.ContentVersion)
	}

	// Terminal task must reject single-intent resume from paused->open.
	iid, err := task.Store.AddIntent(map[string]any{"summary": "paused work"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(iid, "paused"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetGoallessDoneGuarded(task.ID); err != nil {
		t.Fatal(err)
	}
	// Mark task status done directly in case paused intent prevented SetGoallessDoneGuarded.
	if won, err := m.SetTaskStatusGuarded(task.ID, "done"); err != nil || !won {
		t.Fatalf("SetTaskStatusGuarded: won=%v err=%v", won, err)
	}
	if _, err := s.applyIntentControl(context.Background(), task, iid, "resume", "", ""); err == nil {
		t.Fatal("applyIntentControl(resume) succeeded on terminal task, expected rejection")
	}
	intentAfter, _ := task.Store.GetNode(iid)
	if intentAfter == nil || intentAfter.State != "paused" {
		t.Fatalf("intent state after rejected resume = %+v, want paused", intentAfter)
	}
}

func TestGoallessDoneGuardedPausedIntentAndDetachedSettling(t *testing.T) {
	m := openLifecycleTestManager(t)
	e := NewEngine(m)
	worker := new(agent.Worker)
	planner := new(agent.Planner)
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return planner, worker
	})

	task, err := m.CreateTask(fmt.Sprintf("goalless-guard-%d", time.Now().UnixNano()), "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteTask(tidOfTask(task)) })

	if _, err := task.Store.AddNode(db.KindGoal, map[string]any{"text": "resolved"}, 0, "met", "test", nil); err != nil {
		t.Fatal(err)
	}
	iid, err := task.Store.AddIntent(map[string]any{"summary": "paused manual"}, 5, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentState(iid, "paused"); err != nil {
		t.Fatal(err)
	}

	if won, err := m.SetGoallessDoneGuarded(task.ID); err != nil || won {
		t.Fatalf("SetGoallessDoneGuarded closed task with paused intent: won=%v err=%v", won, err)
	}

	if !e.markSettling(task.ID) {
		t.Fatal("markSettling failed")
	}
	if err := e.runDetachedIntent(context.Background(), task, iid, "paused", "req-1", "hi", "hi"); !errors.Is(err, errTaskControlConflict) {
		t.Fatalf("runDetachedIntent during settling got %v, want errTaskControlConflict", err)
	}
	e.settling.Delete(task.ID)

	if err := task.Store.SetIntentState(iid, "done"); err != nil {
		t.Fatal(err)
	}
	if won, err := m.SetGoallessDoneGuarded(task.ID); err != nil || !won {
		t.Fatalf("SetGoallessDoneGuarded after all done failed: won=%v err=%v", won, err)
	}
}

func TestWatchExecutionsCancelledOnDrainTimeoutAndPreservedOnPause(t *testing.T) {
	e := NewEngine(nil)

	ctxPause, cancelPause := context.WithCancel(context.Background())
	e.execWatch.Store(int64(101), &execWatchHandle{generation: 1, taskID: "t1", cancel: cancelPause, done: make(chan struct{})})
	_ = e.execContextFor(context.Background(), "t1")

	e.cancelExec("t1", agent.AbortPausedByUser)
	select {
	case <-ctxPause.Done():
		t.Fatal("user pause must keep execution watcher alive")
	default:
	}

	_ = e.execContextFor(context.Background(), "t1")
	e.cancelExec("t1", agent.AbortSettleDrainTimeout)
	select {
	case <-ctxPause.Done():
	default:
		t.Fatal("settle drain timeout must cancel execution watcher")
	}
}

func TestDeadlinePausedGuardSkipsSettle(t *testing.T) {
	m := openLifecycleTestManager(t)
	e := NewEngine(m)
	task, err := m.CreateTask(fmt.Sprintf("paused-guard-%d", time.Now().UnixNano()), "test", nil, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteTask(tidOfTask(task)) })

	e.paused.Store(task.ID, true)
	e.settleTask(context.Background(), task, "timeout", "timeout")
	if e.isSettling(task.ID) || isTerminalStatus(m.TaskStatus(task.ID)) {
		t.Fatalf("paused task must not enter settleTask/timeout: settling=%v status=%s", e.isSettling(task.ID), m.TaskStatus(task.ID))
	}
}

func TestExplorationAPIDTONewestPlannerStateAndSingleNode(t *testing.T) {
	m := openLifecycleTestManager(t)
	s := newAdmissionTestServer(m, nil)

	task, err := m.CreateTask(fmt.Sprintf("api-audit-%d", time.Now().UnixNano()), "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.pg.DeleteTask(tidOfTask(task)) })

	intentID, err := task.Store.AddIntent(map[string]any{"summary": "check endpoint"}, 8, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := task.Store.ClaimNode(context.Background(), intentID, "worker#"+strconv.FormatInt(intentID, 10), "open", db.IntentLeaseTTL, nil, false); err != nil || claimed == nil {
		t.Fatalf("ClaimNode intent: %v", err)
	}
	if err := task.Store.SetIntentSemanticOutcome(intentID, "negative", "all checks returned 404"); err != nil {
		t.Fatal(err)
	}
	if changed, err := task.Store.CompareAndSetIntentState(intentID, "running", "negative"); err != nil || !changed {
		t.Fatalf("settle negative intent: changed=%v err=%v", changed, err)
	}
	blockedIntentID, err := task.Store.AddIntent(map[string]any{"summary": "blocked endpoint"}, 7, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentBlockedReason(blockedIntentID, db.IntentBlockedLLMQuota); err != nil {
		t.Fatal(err)
	}
	findingNodeID, err := task.Store.AddNode(db.KindFinding, map[string]any{"name": "XSS", "summary": "reflected xss", "severity": "high"}, 8, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	var findingID int64
	if err := m.pg.QueryRow(`INSERT INTO findings(exploration_id, task_id, node_id, name, vulnclass, severity, status, summary) VALUES($1,$2,$3,'XSS','xss','high','pending','reflected xss') RETURNING id`,
		task.ExpID, tidOfTask(task), findingNodeID).Scan(&findingID); err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SavePlannerState(tidOfTask(task), []db.PlannerTodoItem{{Content: "verify xss", Status: "in_progress"}}, "checked auth boundary"); err != nil {
		t.Fatal(err)
	}

	// Single node detail endpoint must populate finding_id, finding_status, blocked_reason, semantic_outcome.
	reqNode := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/exploration/nodes/%d?task=%s", findingNodeID, task.ID), nil)
	reqNode.SetPathValue("nid", strconv.FormatInt(findingNodeID, 10))
	recNode := httptest.NewRecorder()
	s.explorationNodeDetail(recNode, reqNode)
	if recNode.Code != http.StatusOK {
		t.Fatalf("explorationNodeDetail status=%d body=%s", recNode.Code, recNode.Body.String())
	}
	var gotFindingNode TaskNodeDTO
	if err := json.Unmarshal(recNode.Body.Bytes(), &gotFindingNode); err != nil {
		t.Fatal(err)
	}
	if gotFindingNode.FindingID != strconv.FormatInt(findingID, 10) || gotFindingNode.FindingStatus != "pending" {
		t.Fatalf("finding DTO missing status/id: %+v", gotFindingNode)
	}

	reqIntent := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/exploration/nodes/%d?task=%s", intentID, task.ID), nil)
	reqIntent.SetPathValue("nid", strconv.FormatInt(intentID, 10))
	recIntent := httptest.NewRecorder()
	s.explorationNodeDetail(recIntent, reqIntent)
	if recIntent.Code != http.StatusOK {
		t.Fatalf("explorationNodeDetail(intent) status=%d body=%s", recIntent.Code, recIntent.Body.String())
	}
	var gotIntentDTO TaskNodeDTO
	_ = json.Unmarshal(recIntent.Body.Bytes(), &gotIntentDTO)
	if gotIntentDTO.SemanticOutcome != "negative" || gotIntentDTO.SemanticNote != "all checks returned 404" || gotIntentDTO.Owner == "" {
		t.Fatalf("intent DTO missing semantic/owner fields: %+v", gotIntentDTO)
	}

	reqBlocked := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/exploration/nodes/%d?task=%s", blockedIntentID, task.ID), nil)
	reqBlocked.SetPathValue("nid", strconv.FormatInt(blockedIntentID, 10))
	recBlocked := httptest.NewRecorder()
	s.explorationNodeDetail(recBlocked, reqBlocked)
	if recBlocked.Code != http.StatusOK {
		t.Fatalf("explorationNodeDetail(blocked) status=%d body=%s", recBlocked.Code, recBlocked.Body.String())
	}
	var gotBlockedDTO TaskNodeDTO
	_ = json.Unmarshal(recBlocked.Body.Bytes(), &gotBlockedDTO)
	if gotBlockedDTO.BlockedReason != db.IntentBlockedLLMQuota {
		t.Fatalf("blocked intent DTO missing blocked_reason: %+v", gotBlockedDTO)
	}

	// Planner state endpoint must return saved todos + last_round_note.
	reqPS := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/tasks/%s/planner-state", task.ID), nil)
	reqPS.SetPathValue("id", task.ID)
	recPS := httptest.NewRecorder()
	s.taskPlannerState(recPS, reqPS)
	if recPS.Code != http.StatusOK {
		t.Fatalf("taskPlannerState status=%d body=%s", recPS.Code, recPS.Body.String())
	}
	var ps struct {
		Found         bool                 `json:"found"`
		Todos         []db.PlannerTodoItem `json:"todos"`
		LastRoundNote string               `json:"last_round_note"`
		UpdatedAt     string               `json:"updated_at"`
	}
	if err := json.Unmarshal(recPS.Body.Bytes(), &ps); err != nil {
		t.Fatal(err)
	}
	if !ps.Found || len(ps.Todos) != 1 || ps.Todos[0].Content != "verify xss" || ps.LastRoundNote != "checked auth boundary" || ps.UpdatedAt == "" {
		t.Fatalf("unexpected planner-state response: %+v", ps)
	}

	// Graph endpoint must return total and truncated=false for small graph.
	reqG := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/exploration/graph?task=%s", task.ID), nil)
	recG := httptest.NewRecorder()
	s.explorationGraph(recG, reqG)
	if recG.Code != http.StatusOK {
		t.Fatalf("explorationGraph status=%d body=%s", recG.Code, recG.Body.String())
	}
	var gResp struct {
		Nodes     []TaskNodeDTO `json:"nodes"`
		Edges     []EdgeDTO     `json:"edges"`
		Total     int64         `json:"total"`
		Truncated bool          `json:"truncated"`
	}
	if err := json.Unmarshal(recG.Body.Bytes(), &gResp); err != nil {
		t.Fatal(err)
	}
	if gResp.Truncated || int(gResp.Total) != len(gResp.Nodes) || gResp.Total < 2 {
		t.Fatalf("unexpected graph metadata: total=%d len=%d truncated=%v", gResp.Total, len(gResp.Nodes), gResp.Truncated)
	}
}
