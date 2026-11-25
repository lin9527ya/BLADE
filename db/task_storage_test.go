package db

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTaskStorageStatsAndPurgeScopedData(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()

	tk, err := d.CreateTaskWithOptions("storage-scope-test", "verify scoped clean", TaskCreateOptions{})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE task_id=$1`, strconv.FormatInt(tk.ID, 10))
		_, _ = d.Exec(`DELETE FROM llm_records WHERE task_id=$1`, strconv.FormatInt(tk.ID, 10))
		_, _ = d.Exec(`DELETE FROM tool_executions WHERE task_id=$1`, strconv.FormatInt(tk.ID, 10))
		_ = d.DeleteTask(tk.ID)
	})

	taskText := strconv.FormatInt(tk.ID, 10)
	if _, err := d.Exec(`INSERT INTO activity(exploration_id, worker, kind, summary, created_at) VALUES ($1,'worker','step','act1',now())`, tk.ExplorationID); err != nil {
		t.Fatalf("insert activity: %v", err)
	}
	if _, err := d.AddFinding(tk.ID, 0, "sqli", "keep-finding", "high", "summary", "evidence", "worker", nil); err != nil {
		t.Fatalf("add finding: %v", err)
	}
	snap, _ := json.Marshal(map[string]any{"padding": strings.Repeat("x", 1024)})
	doneKey := "sq-done-" + taskText
	liveKey := "sq-live-" + taskText
	if _, err := d.Exec(`INSERT INTO side_question_sessions(session_key, task_id, exploration_id, run_id, version, snapshot) VALUES ($1,$2,$3,1,1,$4), ($5,$2,$3,1,1,$4)`,
		doneKey, tk.ID, tk.ExplorationID, string(snap), liveKey); err != nil {
		t.Fatalf("insert side sessions: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO side_question_requests(id, session_key, generation, client_id, question, status, model, snapshot_at)
VALUES ($1,$2,0,'c1','q1','completed','{}',now()), ($3,$4,0,'c2','q2','running','{}',now())`,
		"req-done-"+taskText, doneKey, "req-live-"+taskText, liveKey); err != nil {
		t.Fatalf("insert side requests: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO llm_records(ts, task_id, model, status, request_body, response_body) VALUES (now(), $1, 'm', 'ok', $2, $2)`,
		taskText, strings.Repeat("B", 256)); err != nil {
		t.Fatalf("insert llm_records: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO llm_usage(ts, task_id, model, input_tokens) VALUES (now(), $1, 'm', 42)`, taskText); err != nil {
		t.Fatalf("insert llm_usage: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO tool_executions(task_id, command, status, created_at) VALUES ($1,'echo done','completed',now()), ($1,'sleep 9','running',now())`, taskText); err != nil {
		t.Fatalf("insert tool_executions: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO blackboard_events(exploration_id, seq, epoch, entity, object_key, operation, created_at) VALUES ($1, 999, 'e1', 'node', 'n1', 'insert', now())`, tk.ExplorationID); err != nil {
		t.Fatalf("insert blackboard_events: %v", err)
	}

	statsMap, err := d.TaskStorageStats(ctx, []int64{tk.ID}, map[int64]int64{tk.ID: tk.ExplorationID})
	if err != nil {
		t.Fatalf("TaskStorageStats: %v", err)
	}
	st := statsMap[tk.ID]
	if st.ActivityRows != 1 || st.SideQuestionRows != 2 || st.LLMRecordRows != 1 || st.LLMRecordBodyRows != 1 || st.LLMUsageRows != 1 || st.ToolExecutionRows != 1 || st.BlackboardLogRows < 1 {
		t.Fatalf("unexpected task stats: %+v", st)
	}

	res, err := d.PurgeTaskScopedData(ctx, tk.ID, tk.ExplorationID, TaskCleanScopes{
		Activity:        true,
		SideQuestions:   true,
		LLMRecordBodies: true,
		ToolExecutions:  true,
		BlackboardLogs:  true,
	})
	if err != nil {
		t.Fatalf("PurgeTaskScopedData: %v", err)
	}
	if res.ActivityDeleted != 1 || res.SideQuestionsDeleted != 1 || res.LLMRecordBodiesCleared != 1 || res.ToolExecutionsDeleted != 1 || res.BlackboardLogsDeleted < 1 {
		t.Fatalf("unexpected purge result: %+v", res)
	}

	// Verify protected/retained entities stay intact: task, finding, running side question, running tool_execution, llm_usage, llm_record metadata.
	gotTask, err := d.GetTask(tk.ID)
	if err != nil || gotTask == nil {
		t.Fatalf("task must remain intact: got=%+v err=%v", gotTask, err)
	}
	var findingsLeft, sqLeft, reqLiveLeft, recMetaLeft, recBodiesLeft, usageLeft, execLeft int
	_ = d.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, tk.ID).Scan(&findingsLeft)
	_ = d.QueryRow(`SELECT count(*) FROM side_question_sessions WHERE task_id=$1`, tk.ID).Scan(&sqLeft)
	_ = d.QueryRow(`SELECT count(*) FROM side_question_requests WHERE session_key=$1`, liveKey).Scan(&reqLiveLeft)
	_ = d.QueryRow(`SELECT count(*), count(request_body) FROM llm_records WHERE task_id=$1`, taskText).Scan(&recMetaLeft, &recBodiesLeft)
	_ = d.QueryRow(`SELECT count(*) FROM llm_usage WHERE task_id=$1`, taskText).Scan(&usageLeft)
	_ = d.QueryRow(`SELECT count(*) FROM tool_executions WHERE task_id=$1`, taskText).Scan(&execLeft)
	if findingsLeft != 1 || sqLeft != 1 || reqLiveLeft != 1 || recMetaLeft != 1 || recBodiesLeft != 0 || usageLeft != 1 || execLeft != 1 {
		t.Fatalf("unexpected survivors: findings=%d sq=%d reqLive=%d recMeta=%d recBodies=%d usage=%d exec=%d",
			findingsLeft, sqLeft, reqLiveLeft, recMetaLeft, recBodiesLeft, usageLeft, execLeft)
	}
}

func TestVacuumStorageTablesWhitelist(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if _, err := d.VacuumStorageTables(ctx, []string{"tasks; DROP TABLE tasks"}, false); err == nil {
		t.Fatal("non-whitelisted table must be rejected")
	}
	done, err := d.VacuumStorageTables(ctx, []string{"server_logs"}, false)
	if err != nil {
		t.Fatalf("VacuumStorageTables(server_logs): %v", err)
	}
	if len(done) != 1 || done[0] != "server_logs" {
		t.Fatalf("unexpected vacuumed list: %v", done)
	}
}
