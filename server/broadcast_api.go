// 全局播报：跨任务按时间汇总探索节点。
//
// 与任务详情的 /api/exploration/nodes 不同，这里不做边/邻居/资产展开——
// 全局流是“正在发生什么”的宽表，点进某一行再由任务详情页补全上下文。
package server

import (
	"log"
	"net/http"

	"github.com/lin9527ya/blade/db"
)

type GlobalBroadcastDTO struct {
	TaskNodeDTO
	TaskID     string `json:"task_id"`
	TaskName   string `json:"task_name"`
	TaskState  string `json:"task_state"`
	SessionKey string `json:"session_key,omitempty"`
}

// globalBroadcast 汇总所有未删除任务的探索节点（节点 id 全局有序，因此跨任务
// 的游标/分页仍然稳定）。默认只播报已确认的 finding；按 state 显式筛选时
// 尊重调用方，便于排查候选/待补证节点。
func (s *Server) globalBroadcast(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	size := atoiDefault(q.Get("size"), 30)
	states := csvValues(q.Get("state"))
	filter := db.NodeFilter{
		Kinds:                 csvValues(q.Get("kind")),
		States:                states,
		Query:                 q.Get("q"),
		Asc:                   q.Get("order") == "asc",
		OnlyConfirmedFindings: len(states) == 0,
	}
	taskID := int64(atoiDefault(q.Get("task"), 0))
	pg := s.m.PG()
	if pg == nil {
		writeJSON(w, 200, map[string]any{"items": []any{}, "total": 0, "page": page, "size": size})
		return
	}
	rows, total, err := pg.GlobalNodesPage(filter, taskID, page, size)
	if err != nil {
		log.Printf("[broadcast] global page=%d size=%d: %v", page, size, err)
		writeErr(w, 500, err.Error())
		return
	}
	items := make([]GlobalBroadcastDTO, 0, len(rows))
	for _, g := range rows {
		if g == nil || g.Node == nil {
			continue
		}
		items = append(items, GlobalBroadcastDTO{
			TaskNodeDTO: taskNodeDTO(g.Node),
			TaskID:      i64s(g.TaskID),
			TaskName:    g.TaskName,
			TaskState:   g.TaskState,
			SessionKey:  g.SessionKey,
		})
	}
	writeJSON(w, 200, map[string]any{
		"items": items,
		"total": total,
		"page":  page,
		"size":  size,
	})
}
