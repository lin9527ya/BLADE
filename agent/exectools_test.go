package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// fakeExecService records what the tools asked for and replays canned snapshots.
type fakeExecService struct {
	submits  []fakeSubmit
	waited   []time.Duration
	cancels  []string
	status   ExecStatus
	submitID int64
	ledger   []*db.ToolExecution
}

type fakeSubmit struct {
	taskID, worker, command, desc string
	intentID                      int64
	timeout                       time.Duration
}

func (f *fakeExecService) Submit(_ context.Context, taskID string, intentID int64, worker, command, desc string, timeout time.Duration) (ExecStatus, error) {
	f.submits = append(f.submits, fakeSubmit{taskID: taskID, intentID: intentID, worker: worker, command: command, desc: desc, timeout: timeout})
	st := f.status
	if st.ID == 0 {
		st = ExecStatus{ID: f.submitID, Status: "running", OutputPath: "/data/tasks/" + taskID + "/executions/7.log"}
	}
	return st, nil
}

func (f *fakeExecService) Wait(_ context.Context, id int64, timeout time.Duration) (ExecStatus, error) {
	f.waited = append(f.waited, timeout)
	st := f.status
	st.ID = id
	return st, nil
}

func (f *fakeExecService) Get(_ context.Context, id int64) (ExecStatus, error) {
	st := f.status
	st.ID = id
	return st, nil
}

func (f *fakeExecService) Cancel(_ context.Context, id int64, reason string) (bool, error) {
	f.cancels = append(f.cancels, reason)
	return true, nil
}

func (f *fakeExecService) CountRanForIntent(int64) (int64, error) { return 0, nil }

func (f *fakeExecService) ListForIntent(int64) ([]*db.ToolExecution, error) { return f.ledger, nil }

func execTool(t *testing.T, tools []actool.CoreTool, name string) actool.CoreTool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("missing tool: %s", name)
	return nil
}

// TestExecToolsRequireService pins the un-wired refusal: every execution tool says so
// in plain words instead of failing obscurely (the tools are always registered).
func TestExecToolsRequireService(t *testing.T) {
	tsx := NewToolSet(nil, "work#1")
	for _, name := range []string{"run_execution", "wait_execution", "get_execution", "cancel_execution"} {
		tool := execTool(t, tsx.WorkerTools(), name)
		res, err := tool.Call(context.Background(), json.RawMessage(`{"command":"ls","execution_id":1,"reason":"x"}`), nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(res.Flatten(), "未接线") {
			t.Fatalf("%s must refuse readably: %s", name, res.Flatten())
		}
	}
}

// TestRunExecutionPassesLedgerFields pins what run_execution hands the service: the
// task id as a string, the owning intent, the worker label, the command and the
// description (which lands in the ledger), plus the optional hard timeout.
func TestRunExecutionPassesLedgerFields(t *testing.T) {
	fake := &fakeExecService{submitID: 42}
	tsx := NewToolSet(nil, "work#3")
	tsx.SetTaskID(77)
	tsx.SetOwnerNode(9)
	tsx.SetExecService(fake)
	tool := execTool(t, tsx.WorkerTools(), "run_execution")
	res, err := tool.Call(context.Background(), json.RawMessage(
		`{"command":"nuclei -u https://t.example","timeout_ms":120000,"description":"全站扫描"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "execution started: 42") {
		t.Fatalf("tool result must lead with the execution id: %s", res.Flatten())
	}
	if len(fake.submits) != 1 {
		t.Fatalf("submit count = %d", len(fake.submits))
	}
	got := fake.submits[0]
	if got.taskID != "77" || got.intentID != 9 || got.worker != "work#3" ||
		got.command != "nuclei -u https://t.example" || got.desc != "全站扫描" || got.timeout != 120*time.Second {
		t.Fatalf("submit payload wrong: %+v", got)
	}
	// 空命令必须被挡在服务之前。
	if _, err := tool.Call(context.Background(), json.RawMessage(`{"command":"  "}`), nil); err != nil {
		t.Fatal(err)
	}
	if len(fake.submits) != 1 {
		t.Fatalf("blank command reached the service: %+v", fake.submits)
	}
}

// TestWaitExecutionTimeoutIsNotFailure pins the wording discipline that the whole
// P13 feature hangs on: a still-running snapshot comes back as NORMAL text (no error
// result) that says waiting can continue — and the tool clamps the wait window.
func TestWaitExecutionTimeoutIsNotFailure(t *testing.T) {
	fake := &fakeExecService{status: ExecStatus{Status: "running", Tail: "partial...", OutputPath: "/abs/1.log"}}
	tsx := NewToolSet(nil, "work#1")
	tsx.SetExecService(fake)
	tool := execTool(t, tsx.WorkerTools(), "wait_execution")
	res, err := tool.Call(context.Background(), json.RawMessage(`{"execution_id":5,"timeout_ms":300000}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Flatten()
	if !strings.Contains(text, "超时是观察不是失败") || !strings.Contains(text, "running") {
		t.Fatalf("running snapshot must read as an observation: %s", text)
	}
	if !strings.Contains(text, "partial...") {
		t.Fatalf("partial output must be surfaced: %s", text)
	}
	if len(fake.waited) != 1 || fake.waited[0] != 300*time.Second {
		t.Fatalf("wait window not passed through: %+v", fake.waited)
	}
	// 超上限被夹到 10 分钟；缺省 60 秒。
	_, _ = tool.Call(context.Background(), json.RawMessage(`{"execution_id":5,"timeout_ms":99999999}`), nil)
	if fake.waited[1] != 10*time.Minute {
		t.Fatalf("wait must clamp to 10m, got %v", fake.waited[1])
	}
	_, _ = tool.Call(context.Background(), json.RawMessage(`{"execution_id":5}`), nil)
	if fake.waited[2] != 60*time.Second {
		t.Fatalf("wait default must be 60s, got %v", fake.waited[2])
	}
}

// TestCancelExecutionRequiresReason pins that a cancellation always carries its why
// into the ledger, and that the reason comes back in the tool text.
func TestCancelExecutionRequiresReason(t *testing.T) {
	fake := &fakeExecService{}
	tsx := NewToolSet(nil, "work#1")
	tsx.SetExecService(fake)
	tool := execTool(t, tsx.WorkerTools(), "cancel_execution")
	res, err := tool.Call(context.Background(), json.RawMessage(`{"execution_id":3}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "reason 必填") || len(fake.cancels) != 0 {
		t.Fatalf("reasonless cancel must be refused: %s %+v", res.Flatten(), fake.cancels)
	}
	res, err = tool.Call(context.Background(), json.RawMessage(`{"execution_id":3,"reason":"扫错域名了"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Flatten(), "扫错域名了") || len(fake.cancels) != 1 {
		t.Fatalf("reason must round-trip: %s %+v", res.Flatten(), fake.cancels)
	}
}
