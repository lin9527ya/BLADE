package server

import (
	"net/http"
)

// exhaustionReviews 是穷尽复核（看门狗）的记录入口：任务详情据此展示
// 「planner 想收工 → 谁拦下了 → 理由是什么」。
//
// 只读。复核结论本来就在活动流里（kind=system），但任务跑到上百轮之后没人翻得到，
// 所以单列一个端点给页面用。
func (s *Server) exhaustionReviews(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil {
		writeJSON(w, 200, map[string]any{"items": []any{}})
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), 20)
	items, err := t.Store.ExhaustionReviewRecords(limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
