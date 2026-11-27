package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

func (s *Server) sendPlannerMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
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
	if message == "" {
		writeErr(w, http.StatusBadRequest, "消息不能为空")
		return
	}
	if len([]rune(message)) > 4000 {
		writeErr(w, http.StatusBadRequest, "消息不能超过 4000 个字符")
		return
	}
	if reqID := strings.TrimSpace(req.RequestID); reqID != "" && !validWorkerMessageRequestID(reqID) {
		writeErr(w, http.StatusBadRequest, "request_id 必须是 1-128 位字母、数字、-、_、. 或 :")
		return
	}
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, "任务正在删除，无法向 Planner 发送消息")
		return
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.Queued {
		writeErr(w, http.StatusConflict, "排队中的任务尚未启动，无法向 Planner 发送消息")
		return
	}
	if !s.taskRuntimeAvailable(t, "planner") {
		writeErr(w, http.StatusServiceUnavailable, "当前任务没有可用的 Planner 模型配置，请先配置可用 LLM")
		return
	}
	planner, _ := s.engine.snapshotFor(t)
	if planner == nil {
		writeErr(w, http.StatusServiceUnavailable, "当前任务的 Planner 实例未就绪")
		return
	}
	agentMessage, ok := s.prepareChatMentionMessage(w, message)
	if !ok {
		return
	}
	res, err := s.engine.SendPlannerMessage(t, planner, message, agentMessage)
	if err != nil {
		if errors.Is(err, errTaskControlConflict) {
			writeErr(w, http.StatusConflict, "任务正在删除或状态变更，无法发送消息")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) plannerChatStatus(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running": s.engine.IsPlannerRunning(t.ID),
		"queued":  s.engine.PlannerSteerCount(t.ID),
	})
}

func (s *Server) stopPlannerTurn(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	if !s.engine.StopPlannerTurn(t.ID) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "idle"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "stopping"})
}

func (s *Server) resetPlannerContext(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.PathValue("id"))
	if t == nil {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerMessageBytes)
	var req struct {
		ClearTodos bool   `json:"clear_todos"`
		Reason     string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求体过大")
			return
		}
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if s.engine.IsDeleting(t.ID) {
		writeErr(w, http.StatusConflict, "任务正在删除，无法清空 Planner 上下文")
		return
	}
	lifecycle := t.lifecycleSnapshot()
	if lifecycle.Queued {
		writeErr(w, http.StatusConflict, "排队中的任务尚未启动，无法操作 Planner 上下文")
		return
	}
	planner, _ := s.engine.snapshotFor(t)
	res, err := s.engine.ResetPlannerContext(r.Context(), t, planner, req.ClearTodos, req.Reason)
	if err != nil {
		if errors.Is(err, errTaskControlConflict) {
			writeErr(w, http.StatusConflict, "任务正在删除或状态变更，无法清空 Planner 上下文")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
