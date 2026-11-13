// 全局播报：跨任务按时间汇总探索节点。
//
// 与任务详情的 /api/exploration/nodes 不同，这里不做边/邻居/资产展开——
// 全局流是“正在发生什么”的宽表，点进某一行再由任务详情页补全上下文。
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/lin9527ya/blade/db"
)

type GlobalBroadcastDTO struct {
	TaskNodeDTO
	TaskID     string `json:"task_id"`
	TaskName   string `json:"task_name"`
	TaskState  string `json:"task_state"`
	SessionKey string `json:"session_key,omitempty"`
}

func globalBroadcastDTO(g *db.GlobalNode) GlobalBroadcastDTO {
	return GlobalBroadcastDTO{
		TaskNodeDTO: taskNodeDTO(g.Node),
		TaskID:      i64s(g.TaskID),
		TaskName:    g.TaskName,
		TaskState:   g.TaskState,
		SessionKey:  g.SessionKey,
	}
}

func queryUnixTime(raw string) time.Time {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// globalBroadcast 汇总所有未删除任务的探索节点（节点 id 全局有序，因此跨任务
// 的游标/分页仍然稳定）。默认只播报已确认的 finding；按 state/finding_status
// 显式筛选时尊重调用方，便于排查候选/待补证节点。
func (s *Server) globalBroadcast(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 1)
	size := atoiDefault(q.Get("size"), 30)
	states := csvValues(q.Get("state"))
	findingStatuses := csvValues(q.Get("finding_status"))
	filter := db.GlobalNodeFilter{
		Kinds:                 csvValues(q.Get("kind")),
		States:                states,
		FindingStatuses:       findingStatuses,
		TaskStates:            csvValues(q.Get("task_state")),
		Query:                 q.Get("q"),
		Asc:                   q.Get("order") == "asc",
		OnlyConfirmedFindings: len(states) == 0 && len(findingStatuses) == 0,
		Since:                 queryUnixTime(q.Get("since")),
		Until:                 queryUnixTime(q.Get("until")),
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
		items = append(items, globalBroadcastDTO(g))
	}
	writeJSON(w, 200, map[string]any{
		"items": items,
		"total": total,
		"page":  page,
		"size":  size,
	})
}

// streamGlobalBroadcast is the global SSE tail. Node writes happen across many
// agent paths, so instead of inventing a second publish hook the stream tails
// the DB by the global node id cursor. The browser receives real SSE frames and
// only refreshes its filtered view when a new node is committed; the DB stays
// the source of truth, including after a dropped connection.
func (s *Server) streamGlobalBroadcast(w http.ResponseWriter, r *http.Request) {
	pg := s.m.PG()
	if pg == nil {
		writeErr(w, 503, "database unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	since := int64(atoiDefault(r.URL.Query().Get("since"), 0))
	if le := r.Header.Get("Last-Event-ID"); le != "" {
		if n, err := strconv.ParseInt(le, 10, 64); err == nil {
			since = n
		}
	}
	if since <= 0 {
		var err error
		since, err = pg.GlobalLatestNodeID()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = sseWrite(w, flusher, "retry: 5000\n\n")

	ctx := r.Context()
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		rows, _, err := pg.GlobalNodesPage(db.GlobalNodeFilter{AfterID: since, Asc: true}, 0, 1, 100)
		if err != nil {
			log.Printf("[broadcast/stream] tail since=%d: %v", since, err)
			return
		}
		for _, g := range rows {
			if g == nil || g.Node == nil {
				continue
			}
			b, err := json.Marshal(globalBroadcastDTO(g))
			if err != nil {
				continue
			}
			if !sseWrite(w, flusher, fmt.Sprintf("id: %d\nevent: node\ndata: %s\n\n", g.Node.ID, b)) {
				return
			}
			since = g.Node.ID
		}
		if len(rows) == 100 {
			continue // a burst larger than one page: drain it before waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		case <-ping.C:
			if !sseWrite(w, flusher, ": ping\n\n") {
				return
			}
		}
	}
}
