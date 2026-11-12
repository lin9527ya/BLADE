package server

import (
	"strconv"
	"testing"

	"github.com/lin9527ya/blade/agent"
)

// setClashEgressPool writes the two settings that define a strict task pool and
// restores the previous values when the test ends.
func setClashEgressPool(t *testing.T, m *Manager, groups, endpoints string) func() {
	t.Helper()
	type saved struct {
		key   string
		value string
		ok    bool
	}
	keys := []string{settingClashProxyGroup, settingClashProxyGroups, settingClashTaskEndpoints}
	old := make([]saved, 0, len(keys))
	for _, key := range keys {
		v, ok, err := m.pg.GetSetting(key)
		if err != nil {
			t.Fatalf("read setting %s: %v", key, err)
		}
		old = append(old, saved{key: key, value: v, ok: ok})
	}
	if err := m.pg.SetSetting(settingClashProxyGroup, ""); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(settingClashProxyGroups, groups); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetSetting(settingClashTaskEndpoints, endpoints); err != nil {
		t.Fatal(err)
	}
	return func() {
		for _, s := range old {
			if s.ok {
				_ = m.pg.SetSetting(s.key, s.value)
				continue
			}
			_, _ = m.pg.Exec(`DELETE FROM settings WHERE key=$1`, s.key)
		}
	}
}

func taskNumericID(t *testing.T, id string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		t.Fatalf("bad task id %q: %v", id, err)
	}
	return n
}

func storedEgressGroup(t *testing.T, m *Manager, id string) string {
	t.Helper()
	row, err := m.pg.GetTask(taskNumericID(t, id))
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatalf("task %s disappeared", id)
	}
	return row.EgressGroup
}

func TestTaskEgressClaimsAreExclusiveAndPersisted(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer setClashEgressPool(t, m, "G1\nG2", "G1=http://127.0.0.1:7801\nG2=http://127.0.0.1:7802")()
	if got := m.TaskEgressCapacity(); got != 2 {
		t.Fatalf("capacity=%d, want 2", got)
	}

	tasks := make([]*Task, 0, 3)
	for i := 0; i < 3; i++ {
		task, err := m.CreateTask("egress "+strconv.Itoa(i), "claim a pool group", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	defer func() {
		for _, task := range tasks {
			_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
		}
	}()

	first, err := m.ClaimTaskEgress(tasks[0].ID)
	if err != nil || !first.Pooled || !first.Acquired || first.Group != "G1" {
		t.Fatalf("first claim=%+v err=%v", first, err)
	}
	second, err := m.ClaimTaskEgress(tasks[1].ID)
	if err != nil || !second.Acquired || second.Group != "G2" {
		t.Fatalf("second claim=%+v err=%v", second, err)
	}
	third, err := m.ClaimTaskEgress(tasks[2].ID)
	if err != nil || !third.Pooled || third.Group != "" || third.Acquired {
		t.Fatalf("third claim should find no free group: %+v err=%v", third, err)
	}
	if got := storedEgressGroup(t, m, tasks[0].ID); got != "G1" {
		t.Fatalf("task 1 persisted group=%q, want G1", got)
	}
	if got := storedEgressGroup(t, m, tasks[1].ID); got != "G2" {
		t.Fatalf("task 2 persisted group=%q, want G2", got)
	}

	m.ReleaseTaskEgress(tasks[0].ID)
	if got := storedEgressGroup(t, m, tasks[0].ID); got != "" {
		t.Fatalf("released task still persists group %q", got)
	}
	reused, err := m.ClaimTaskEgress(tasks[2].ID)
	if err != nil || !reused.Acquired || reused.Group != "G1" {
		t.Fatalf("released slot was not reused: %+v err=%v", reused, err)
	}
}

func TestTaskEgressPoolRequiresDedicatedListeners(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer setClashEgressPool(t, m, "G1\nG2", "")()
	if got := m.TaskEgressCapacity(); got != 0 {
		t.Fatalf("group names without listeners must not form a strict pool, capacity=%d", got)
	}
	task, err := m.CreateTask("rotation only", "no listener", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	claim, err := m.ClaimTaskEgress(task.ID)
	if err != nil || claim.Pooled || claim.Acquired {
		t.Fatalf("rotation-only pool must not reserve a slot: %+v err=%v", claim, err)
	}
	if got := m.TaskEgressGroup(task.ID); got == "" {
		t.Fatalf("legacy rotation should still assign a group, got empty")
	}
}

func TestPauseReleasesGroupAndPromotesQueuedTask(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	defer setClashEgressPool(t, m, "G1", "G1=http://127.0.0.1:7801")()

	holder, err := m.CreateTask("holder", "hold the only group", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(holder.ID, DeleteTaskOptions{}) }()

	s := newAdmissionTestServer(m, nil)
	queued, err := s.admitTask(holder, "resume")
	if err != nil || queued {
		t.Fatalf("holder admission: queued=%v err=%v", queued, err)
	}
	if got := storedEgressGroup(t, m, holder.ID); got != "G1" {
		t.Fatalf("holder group=%q, want G1", got)
	}

	// Create the second task only after the holder is admitted; a created task
	// counts as a ready occupant until it is admitted or queued.
	waiting, err := m.CreateTask("waiting", "promote after pause", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(waiting.ID, DeleteTaskOptions{}) }()
	queued, err = s.admitTask(waiting, "resume")
	if err != nil || !queued || !waiting.Queued {
		t.Fatalf("second task should queue on the one-group pool: queued=%v task=%+v err=%v", queued, waiting, err)
	}
	if got := storedEgressGroup(t, m, waiting.ID); got != "" {
		t.Fatalf("queued task must not hold a group, got %q", got)
	}

	if _, err := s.applyTaskControl(holder, "pause"); err != nil {
		t.Fatalf("pause holder: %v", err)
	}
	if got := storedEgressGroup(t, m, holder.ID); got != "" {
		t.Fatalf("paused task still holds group %q", got)
	}
	s.reconcileConcurrency()
	if waiting.Queued || s.engine.IsPaused(waiting.ID) {
		t.Fatalf("queued task was not promoted after pause: %+v paused=%v", waiting, s.engine.IsPaused(waiting.ID))
	}
	if got := storedEgressGroup(t, m, waiting.ID); got != "G1" {
		t.Fatalf("promoted task group=%q, want G1", got)
	}
}

func TestThreeGroupPoolQueuesFiveTasksAndBackfills(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}
	defer setClashEgressPool(t, m, "G1\nG2\nG3",
		"G1=http://127.0.0.1:7801\nG2=http://127.0.0.1:7802\nG3=http://127.0.0.1:7803")()

	s := newAdmissionTestServer(m, nil)
	running := make([]*Task, 0, 3)
	for i := 0; i < 3; i++ {
		task, err := m.CreateTask("running "+strconv.Itoa(i), "own one group", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		running = append(running, task)
		defer func(task *Task) { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }(task)
		if queued, err := s.admitTask(task, "resume"); err != nil || queued {
			t.Fatalf("running task %s admission: queued=%v err=%v", task.ID, queued, err)
		}
	}
	seen := map[string]bool{}
	for _, task := range running {
		g := storedEgressGroup(t, m, task.ID)
		if g == "" || seen[g] {
			t.Fatalf("three running tasks must hold distinct groups, got %q for %s (seen=%v)", g, task.ID, seen)
		}
		seen[g] = true
	}

	queuedTasks := make([]*Task, 0, 2)
	for i := 0; i < 2; i++ {
		task, err := m.CreateTask("queued "+strconv.Itoa(i), "wait for a group", nil, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		queuedTasks = append(queuedTasks, task)
		defer func(task *Task) { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }(task)
		if queued, err := s.admitTask(task, "resume"); err != nil || !queued || !task.Queued {
			t.Fatalf("overflow task %s must queue: queued=%v task=%+v err=%v", task.ID, queued, task, err)
		}
		if g := storedEgressGroup(t, m, task.ID); g != "" {
			t.Fatalf("queued task %s holds group %q", task.ID, g)
		}
	}

	if _, err := s.applyTaskControl(running[0], "pause"); err != nil {
		t.Fatalf("pause first running task: %v", err)
	}
	s.reconcileConcurrency()
	if queuedTasks[0].Queued || s.engine.IsPaused(queuedTasks[0].ID) {
		t.Fatalf("first queued task was not promoted: %+v", queuedTasks[0])
	}
	if !queuedTasks[1].Queued {
		t.Fatalf("second queued task must stay queued until another group frees: %+v", queuedTasks[1])
	}
	if got := storedEgressGroup(t, m, queuedTasks[0].ID); got != "G1" {
		t.Fatalf("promoted task group=%q, want the paused task's G1", got)
	}
}

func TestTaskEgressGroupRestoredFromTaskRow(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer setClashEgressPool(t, m, "G1\nG2", "G1=http://127.0.0.1:7801\nG2=http://127.0.0.1:7802")()
	task, err := m.CreateTask("restore", "restore binding", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	if err := m.pg.SetStatus(taskNumericID(t, task.ID), "running"); err != nil {
		t.Fatal(err)
	}
	if err := m.pg.SetTaskEgressGroup(taskNumericID(t, task.ID), "G2"); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	delete(m.tasks, task.ID)
	delete(m.clashTaskGroups, task.ID)
	m.mu.Unlock()
	loaded, err := m.LoadExisting()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, loadedTask := range loaded {
		if loadedTask.ID == task.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("task %s was not reloaded", task.ID)
	}
	if got := m.TaskEgressGroup(task.ID); got != "G2" {
		t.Fatalf("restored binding=%q, want G2", got)
	}
	if got := storedEgressGroup(t, m, task.ID); got != "G2" {
		t.Fatalf("stored binding=%q, want G2", got)
	}
	// Keep the engine's pause barrier out of this pure manager test.
	_ = agent.AbortPausedOnReload
}
