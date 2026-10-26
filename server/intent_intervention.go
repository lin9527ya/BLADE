package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

const maxWorkerMessageBytes = 64 << 10

func validWorkerMessageRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// workerMessageRoute 把意图状态映射到两种送达方式：steer=运行中实时注入，
// resume=从 transcript 续跑（paused 与 interrupted 终态）。两者都不成立 = 拒绝。
// 单独抽出来是为了让状态集合可单测，避免 handler 里再抄一份。
func workerMessageRoute(state string) (steer, resume bool) {
	switch state {
	case "running":
		return true, false
	case "paused":
		return false, true
	default:
		return false, db.IntentStateRecoverable(state)
	}
}

// sendWorkerMessage 把一条人写的消息送进一条 Worker 意图，按意图状态分两条路：
//
//   - **运行中（running）**：实时注入（SteerWorkHuman）。消息在 worker 下一次工具调用
//     之前送到，并拦掉那次调用、由模型据此改道——不打断、不丢已积累的上下文。代价是
//     它只存在于内存队列里：这条 run 若在取出前就结束，消息不会送达（此时接口只在
//     "压根没有在跑的 work" 上报 409，让用户重发，不做静默排队）。
//   - **已暂停（paused）**：暂停续发。消息作为下一轮输入，走 worker 正常 resume 的
//     resume-from-transcript 路径（ExecuteWithMessage），复用既有暂停/恢复机制而不是
//     另造协议。这条 run 跑在 worker 池之外的独立 goroutine（runDetachedIntent），所以
//     池子占满时也能立刻被接走。同样只在内存：进程重启后该意图会从 transcript 重跑，
//     不带这条消息——对"偶尔打断再继续"这个动作可以接受。
//   - **中断终态（blocked / exhausted / stopped / no_output）**：与 paused 同一条
//     resume-from-transcript 路径，只是起始状态不同。人工消息本就是"接着这条会话
//     继续干"，不该要求先重开意图、再由 planner 重新派一条新意图（那会丢掉现场）。
//     有结论的终态（done/negative/out-of-scope）仍拒绝：那是结论，不是中断。
//
// 两条路都会把"人发了什么"单独落成一条 worker="user" 的活动（见下），与"worker 何时
// 真的被拦下"（PreToolUse 那条 Blocked by hook 记录）分开：前者证明人发了，后者证明送到了。
func (s *Server) sendWorkerMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "bad intent id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerMessageBytes)
	var req struct {
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求体过大")
			return
		}
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	message := strings.TrimSpace(req.Message)
	requestID := strings.TrimSpace(req.RequestID)
	if message == "" {
		writeErr(w, http.StatusBadRequest, "消息不能为空")
		return
	}
	if len([]rune(message)) > 4000 {
		writeErr(w, http.StatusBadRequest, "消息不能超过 4000 个字符")
		return
	}
	// Reject non-runnable task lifecycles up front so the caller gets a clear reason
	// instead of a silent no-op. 意图本身能否接消息（running/paused/中断终态）由下面的路由判定。
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, "任务正在删除，无法向 Worker 发送消息")
		return
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		writeErr(w, http.StatusConflict, "任务已暂停，请先恢复任务再向 Worker 发送消息")
		return
	case lifecycle.Queued:
		writeErr(w, http.StatusConflict, "排队中的任务无法向 Worker 发送消息")
		return
	case isTerminalStatus(lifecycle.Status):
		writeErr(w, http.StatusConflict, "终态任务无法向 Worker 发送消息")
		return
	case s.engine.isSettling(t.ID):
		writeErr(w, http.StatusConflict, "任务正在收尾，无法向 Worker 发送消息")
		return
	}

	node, err := t.Store.GetNode(iid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			writeErr(w, http.StatusConflict, "继承意图为只读，不能发送 Worker 消息")
			return
		}
		writeErr(w, http.StatusNotFound, "intent not found")
		return
	}
	if node.Kind != db.KindIntent {
		writeErr(w, http.StatusConflict, "node is not an intent")
		return
	}
	// 状态闸先判：@ 引用的展开错误不该盖过"这条意图现在根本不能发消息"这个更准确的拒绝。
	steer, resumable := workerMessageRoute(node.State)
	if !steer && !resumable {
		writeErr(w, http.StatusConflict, "仅运行中、已暂停或已中断的 Worker 可以发送消息（当前 "+node.State+"）")
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(w, message)
	if !ok {
		return
	}

	if steer {
		// 实时注入：下一步动作前生效。没有在跑的 work（刚结束/被回收/租约易主）时
		// 如实报错，让用户重发——不做静默排队，也不悄悄降级成"重启一条 run"。
		if err := s.engine.SteerWorkHuman(iid, agentMessage); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		nid := iid
		s.engine.emitActivity(t, db.Activity{
			NodeID: &nid, Worker: "user", Kind: "user", Summary: message, Detail: message,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"id":         iid,
			"state":      "running",
			"accepted":   true,
			"steered":    true,
			"request_id": requestID,
		})
		return
	}

	// request_id 是续发路径的幂等键（runDetachedIntent 用它去重），注入路径不需要它。
	if !validWorkerMessageRequestID(requestID) {
		writeErr(w, http.StatusBadRequest, "request_id 必须是 1-128 位字母、数字、-、_、. 或 :")
		return
	}

	// runDetachedIntent transitions <当前状态>->running, emits the user turn and starts a
	// dedicated run. Root the run at s.ctx so a disconnected browser cannot strand it
	// while task pause/delete/shutdown still stop it.
	if err := s.engine.runDetachedIntent(s.ctx, t, iid, node.State, requestID, message, agentMessage); err != nil {
		switch {
		case errors.Is(err, db.ErrIntentStateConflict):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":         iid,
		"state":      "running",
		"accepted":   true,
		"request_id": requestID,
	})
}
