package server

import (
	"testing"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

func TestTaskExecutionServiceRejectsForeignObjectsBeforeSideEffects(t *testing.T) {
	svc, pg, _, task := newExecFixture(t)
	row, err := pg.CreateExecution(&db.ToolExecution{TaskID: task, Worker: "worker", Tool: "run_execution", Command: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	owner := svc.ForTask(task)
	other := svc.ForTask(task + "-other")
	if _, err := owner.Get(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Get(t.Context(), row); err == nil {
		t.Fatal("foreign get allowed")
	}
	if _, err := other.Wait(t.Context(), row, time.Millisecond); err == nil {
		t.Fatal("foreign wait allowed")
	}
	if _, err := other.(agent.ExecutionRowService).GetExecutionRow(t.Context(), row); err == nil {
		t.Fatal("foreign meta/output access allowed")
	}
	if _, err := other.Cancel(t.Context(), row, "must not write"); err == nil {
		t.Fatal("foreign cancel allowed")
	}
	got, err := pg.GetExecution(row)
	if err != nil || got.CancelNote != "" || got.Status != db.ExecQueued {
		t.Fatalf("foreign cancel mutated row: %+v %v", got, err)
	}
	if _, err := svc.Get(t.Context(), row); err != nil {
		t.Fatal("admin global service denied")
	}
	if _, err := owner.Cancel(t.Context(), row, "owner stop"); err != nil {
		t.Fatal(err)
	}
}
