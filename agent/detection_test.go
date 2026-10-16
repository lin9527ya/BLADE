package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 检测验证是「平台」与「扫描器」的分水岭：只会打的是扫描器，能回答「这套防御拦不拦得住」的
// 才是平台。这一层最容易出的错是**把「测不到」写成「没响」**——那会给出一个比不给更危险的结论。

// 三态都要能记下来，并且 state 就是结果本身（kind 的三态在 schema 里定死）。
func TestReportDetectionRecordsThreeResults(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	for _, r := range []string{db.StateDetectionTriggered, db.StateDetectionNotTriggered, db.StateDetectionUnknown} {
		out := callTool(t, tools.WorkerTools(), "report_detection_check",
			`{"summary":"跑 T1059.001 看防御响不响","technique":"t1059.001","result":"`+r+`",`+
				`"evidence":"grep sysmon 日志","confidence":"high","intent_id":`+strconv.FormatInt(intent, 10)+`}`)
		if !strings.HasPrefix(out, "detection check recorded: ") {
			t.Fatalf("%s 没记上：%s", r, out)
		}
	}
	nodes, err := ts.ListByKind(db.KindDetection, 10)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("应有 3 条检测记录，得到 %d (err=%v)", len(nodes), err)
	}
	states := map[string]bool{}
	for _, n := range nodes {
		states[n.State] = true
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		if p["technique"] != "T1059.001" { // 小写要被归一成大写
			t.Errorf("技术 ID 没归一：%v", p["technique"])
		}
	}
	for _, r := range []string{db.StateDetectionTriggered, db.StateDetectionNotTriggered, db.StateDetectionUnknown} {
		if !states[r] {
			t.Errorf("三态里的 %s 丢了：%v", r, states)
		}
	}
	if got := tools.Writes().Detections; got != 3 {
		t.Errorf("检测写回计数 = %d，应为 3", got)
	}
}

// 校验：技术 ID 要形状合法、result 要三态之一、evidence 与 confidence 必填。
// 被拒的调用一个字节都不该写进图。
func TestReportDetectionValidation(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	id := strconv.FormatInt(intent, 10)
	cases := []struct{ name, in, want string }{
		{"技术 ID 形状不对", `{"summary":"x","technique":"凭据访问","result":"unknown","evidence":"e","confidence":"high","intent_id":` + id + `}`, "technique"},
		{"result 不在三态内", `{"summary":"x","technique":"T1059.001","result":"maybe","evidence":"e","confidence":"high","intent_id":` + id + `}`, "result"},
		{"缺 evidence", `{"summary":"x","technique":"T1059.001","result":"unknown","confidence":"high","intent_id":` + id + `}`, "evidence"},
		{"缺 confidence", `{"summary":"x","technique":"T1059.001","result":"unknown","evidence":"e","intent_id":` + id + `}`, "confidence"},
		{"缺 summary", `{"summary":" ","technique":"T1059.001","result":"unknown","evidence":"e","confidence":"high","intent_id":` + id + `}`, "summary"},
	}
	for _, c := range cases {
		out := callTool(t, tools.WorkerTools(), "report_detection_check", c.in)
		if strings.HasPrefix(out, "detection check recorded: ") {
			t.Fatalf("%s：本该被拒，却记上了：%s", c.name, out)
		}
		if !strings.Contains(out, c.want) {
			t.Fatalf("%s：拒绝理由没说清 %q：%s", c.name, c.want, out)
		}
	}
	if nodes, _ := ts.ListByKind(db.KindDetection, 10); len(nodes) != 0 {
		t.Fatalf("被拒的调用写了 %d 条检测记录", len(nodes))
	}
}

// 概览里三态**分别计数**，不合成「检出率」——把 unknown 混进分母就是把「测不到」读成「没响」。
func TestGraphOverviewDetectionResultsStaySeparate(t *testing.T) {
	tools, ts, _ := newToolWorker(t)
	for _, r := range []string{db.StateDetectionTriggered, db.StateDetectionNotTriggered, db.StateDetectionUnknown, db.StateDetectionUnknown} {
		if _, err := ts.AddNode(db.KindDetection, map[string]any{"summary": "x", "technique": "T1059.001"}, 6, r, "work#1", nil); err != nil {
			t.Fatal(err)
		}
	}
	out := tools.graphOverviewData()
	got, ok := out["detection_results"].(map[string]int)
	if !ok {
		t.Fatalf("概览缺 detection_results：%#v", out["detection_results"])
	}
	want := map[string]int{db.StateDetectionTriggered: 1, db.StateDetectionNotTriggered: 1, db.StateDetectionUnknown: 2}
	if len(got) != len(want) {
		t.Fatalf("三态必须分别计数（不能合成检出率）：got %v want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d want %d", k, got[k], v)
		}
	}
	if total, _ := out["detections_total"].(int); total != 4 {
		t.Errorf("detections_total = %v，应为 4", out["detections_total"])
	}
}
