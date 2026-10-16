package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// TestCloseCandidateValidation pins the three-state discipline at the tool
// boundary: ruled_out must name its control point, open_proof_gap must name the
// gap, the confidence label is mandatory, and confirmations are refused (they
// belong to report_finding) — a candidate never closes without a reason.
func TestCloseCandidateValidation(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("candidate closure", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(nil, "")
	ts.ts, ts.taskID = d.Exploration(task.ExplorationID), task.ID
	notices := 0
	ts.notifyFinding = func(int64, string) { notices++ }
	tool := ts.closeCandidate()
	rejected := []struct{ in, want string }{
		{`{"candidate":"x","outcome":"ruled_out","confidence":"high"}`, "counterevidence"},
		{`{"candidate":"x","outcome":"open_proof_gap","confidence":"high"}`, "proof_gap"},
		{`{"candidate":"x","outcome":"ruled_out","counterevidence":"控制点","confidence":""}`, "confidence"},
		{`{"candidate":"x","outcome":"ruled_out","counterevidence":"控制点","confidence":"sure"}`, "confidence"},
		{`{"candidate":"x","outcome":"confirmed","counterevidence":"c","confidence":"high"}`, "ruled_out"},
		{`{"candidate":"","outcome":"open_proof_gap","proof_gap":"g","confidence":"low"}`, "candidate"},
	}
	for _, c := range rejected {
		res, err := tool.Call(context.Background(), json.RawMessage(c.in), nil)
		if err != nil {
			t.Fatalf("%s: transport error %v", c.in, err)
		}
		out := res.Flatten()
		if strings.HasPrefix(out, "candidate closed: ") || !strings.Contains(out, c.want) {
			t.Fatalf("closure without a reason must be rejected (%s): %s", c.want, out)
		}
	}
	if notices != 0 {
		t.Fatalf("rejected closures must not notify the planner: %d", notices)
	}

	// Happy path: ruled_out lands as a node + a findings row with the note.
	res, err := tool.Call(context.Background(), json.RawMessage(`{"candidate":"登录口疑似 SQLi","outcome":"ruled_out","counterevidence":"服务端参数化查询；payload 原样出现在错误日志、未参与执行","confidence":"high","vulnclass":"sqli","severity":"high"}`), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "candidate closed: ruled_out") {
		t.Fatalf("valid closure rejected: %v %s", err, res.Flatten())
	}
	var out db.RecordedFinding
	if err := json.Unmarshal([]byte(strings.SplitN(res.Flatten(), "\n", 2)[1]), &out); err != nil {
		t.Fatal(err)
	}
	f, err := d.GetFinding(out.FindingID)
	if err != nil || f == nil {
		t.Fatalf("finding: %+v err=%v", f, err)
	}
	if f.Status != db.FindingRuledOut || f.Confidence != db.ConfidenceHigh || !strings.Contains(f.Counterevidence, "参数化") {
		t.Fatalf("closure row: %+v", f)
	}
	node, err := ts.ts.GetNode(out.NodeID)
	if err != nil || node == nil || node.State != db.FindingRuledOut {
		t.Fatalf("closure node: %+v err=%v", node, err)
	}
	if notices != 1 {
		t.Fatalf("successful closure notified %d times", notices)
	}

	// open_proof_gap 成功路径：proof_gap 参数必须真正绑定（历史 bug：结构体漏 json tag，
	// Go 的字段名匹配不认下划线 → 该参数永远为空、该态永远被拒）。
	res, err = tool.Call(context.Background(), json.RawMessage(`{"candidate":"搜索框疑似存储型 XSS","outcome":"open_proof_gap","proof_gap":"缺登录态，无法验证渲染执行","confidence":"low","vulnclass":"xss"}`), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "candidate closed: open_proof_gap") {
		t.Fatalf("open_proof_gap happy path rejected: %v %s", err, res.Flatten())
	}
	var gapOut db.RecordedFinding
	if err := json.Unmarshal([]byte(strings.SplitN(res.Flatten(), "\n", 2)[1]), &gapOut); err != nil {
		t.Fatal(err)
	}
	gf, err := d.GetFinding(gapOut.FindingID)
	if err != nil || gf == nil || gf.Status != db.FindingOpenProofGap || !strings.Contains(gf.ProofGap, "登录态") {
		t.Fatalf("proof_gap not persisted: %+v err=%v", gf, err)
	}
}

// TestReportFindingStoresConfidence pins that the honest-calibration label
// reaches the findings row, and that a bogus label is refused rather than
// silently dropped.
func TestReportFindingStoresConfidence(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("finding confidence", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(nil, "")
	ts.ts, ts.taskID = d.Exploration(task.ExplorationID), task.ID
	tool := ts.addFinding()
	res, err := tool.Call(context.Background(), json.RawMessage(`{"vulnclass":"xss","severity":"medium","summary":"反射型 XSS 已验证","evidence":"curl + 浏览器弹窗","confidence":"medium"}`), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "finding recorded: ") {
		t.Fatalf("valid finding rejected: %v %s", err, res.Flatten())
	}
	var out db.RecordedFinding
	if err := json.Unmarshal([]byte(strings.SplitN(res.Flatten(), "\n", 2)[1]), &out); err != nil {
		t.Fatal(err)
	}
	f, err := d.GetFinding(out.FindingID)
	if err != nil || f == nil || f.Confidence != db.ConfidenceMedium {
		t.Fatalf("confidence not stored: %+v err=%v", f, err)
	}
	res, err = tool.Call(context.Background(), json.RawMessage(`{"vulnclass":"xss","severity":"low","summary":"x","evidence":"y","confidence":"obviously"}`), nil)
	if err != nil || strings.HasPrefix(res.Flatten(), "finding recorded: ") || !strings.Contains(res.Flatten(), "confidence") {
		t.Fatalf("bogus confidence must be refused: %v %s", err, res.Flatten())
	}
	// 缺省（未标定）仍放行——旧调用/旧数据不得被硬拒；报告会点名未标定。
	res, err = tool.Call(context.Background(), json.RawMessage(`{"vulnclass":"xss","severity":"low","summary":"legacy","evidence":"y"}`), nil)
	if err != nil || !strings.HasPrefix(res.Flatten(), "finding recorded: ") {
		t.Fatalf("unlabeled legacy call must pass: %v %s", err, res.Flatten())
	}
}
