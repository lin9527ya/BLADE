package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// TestSteerMsgRenderLabelsTheSource 钉住两件事：规划者那条的文案**一字不改**（既有提示词
// 与注入行为都依赖它），人工那条不能顶着"规划者"的名号——worker 会据此判断要不要推翻
// 当前打算，"谁在下令"是它判断依据的一部分。
func TestSteerMsgRenderLabelsTheSource(t *testing.T) {
	planner := steerMsg{from: steerFromPlanner, text: "别再走 X"}
	want := "【规划者实时纠偏】别再走 X\n（这是规划者对本意图的即时指令；本次工具调用未执行，请据此调整下一步。若与你当前打算冲突，以此为准。）"
	if got := planner.render(); got != want {
		t.Fatalf("规划者文案变了：\n got=%q\nwant=%q", got, want)
	}

	human := steerMsg{from: steerFromHuman, text: "换成 Y"}.render()
	if !strings.Contains(human, "【人工实时提示】换成 Y") {
		t.Fatalf("人工提示没有带人工归属：%q", human)
	}
	if strings.Contains(human, "规划者") {
		t.Fatalf("人工提示被标成规划者发的（worker 会误判上级）：%q", human)
	}
}

// TestSendWorkerMessageSteersRunningWorker 钉住运行中那条路：消息进 steer 队列（人工来源）、
// 返回 steered=true、并**独立留痕**一条 worker="user" 的活动（与"worker 何时真被拦下"的
// hook 记录分开：前者证明人发了，后者证明送到了）。
func TestSendWorkerMessageSteersRunningWorker(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, m: m, engine: NewEngine(m)}
	task, err := m.CreateTask("Steer running worker", "把提示送进运行中的意图", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		drain, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.waitTaskQuiescent(drain, task.ID)
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
	}()

	iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "probe installer"}, 1, "running", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 运行中 = engine.work 里有这条意图的 run（SteerWorkHuman 据此拒绝"其实没在跑"的注入）。
	s.engine.registerWork(iid, func(error) {})
	defer func() {
		s.engine.workMu.Lock()
		delete(s.engine.work, iid)
		s.engine.workMu.Unlock()
	}()

	send := func(message, requestID string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"message": message, "request_id": requestID})
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
		r.SetPathValue("id", task.ID)
		r.SetPathValue("iid", fmt.Sprint(iid))
		w := httptest.NewRecorder()
		s.sendWorkerMessage(w, r)
		return w
	}

	const hint = "别再碰 /recovery 安装器，转去看 /admin 登录面"
	if w := send(hint, ""); w.Code != 200 {
		t.Fatalf("运行中发送应 200（且无需 request_id），得到 %d：%s", w.Code, w.Body)
	} else {
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp["steered"] != true || resp["state"] != "running" {
			t.Fatalf("运行中发送应回 steered=true/state=running，得到 %v", resp)
		}
	}

	queued, ok := s.engine.drainSteer(iid)
	if !ok {
		t.Fatal("消息没有进 steer 队列——运行中的提示应当在 worker 下一次工具调用前投递")
	}
	if queued.from != steerFromHuman {
		t.Fatalf("入队的来源不是人工（worker 会把提示当成规划者发的）：%v", queued.from)
	}
	if !strings.Contains(queued.render(), hint) {
		t.Fatalf("入队的正文丢了：%q", queued.render())
	}

	node, err := task.Store.GetNode(iid)
	if err != nil || node == nil {
		t.Fatalf("读意图失败: %v", err)
	}
	if node.State != "running" {
		t.Fatalf("注入不该改动意图状态，得到 %s", node.State)
	}
	items, _, err := task.Store.ActivityList(&iid, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var turns []db.Activity
	for _, it := range items {
		if it.Worker == "user" && it.Kind == "user" {
			turns = append(turns, it)
		}
	}
	if len(turns) != 1 || turns[0].Summary != hint {
		t.Fatalf("应有一条 worker=user 的独立留痕且正文一致，得到 %d 条：%+v", len(turns), turns)
	}

	// 没有在跑的 work → 如实报错（A 方案：不静默排队，让用户重发）。
	s.engine.workMu.Lock()
	delete(s.engine.work, iid)
	s.engine.workMu.Unlock()
	if w := send("这条不该被接受", ""); w.Code != 409 {
		t.Fatalf("没有在跑的 work 时应 409，得到 %d：%s", w.Code, w.Body)
	}
}
