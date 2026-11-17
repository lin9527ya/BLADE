package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 战役阶段：只做展示、归组与交接，不参与任务调度或 RoE。
// 阶段 scope/时间窗是计划信息；授权范围仍以 task_scope/company_scope 为准。

type campaignPhaseReq struct {
	Name     string  `json:"name"`
	Position *int    `json:"position"`
	Status   string  `json:"status"`
	Note     string  `json:"note"`
	StartsAt *string `json:"starts_at"`
	EndsAt   *string `json:"ends_at"`
}

func parsePhaseTimes(req campaignPhaseReq) (starts, ends *time.Time, errMsg string) {
	parse := func(raw *string) (*time.Time, bool) {
		if raw == nil || strings.TrimSpace(*raw) == "" {
			return nil, true
		}
		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(*raw))
		if err != nil {
			return nil, false
		}
		utc := ts.UTC()
		return &utc, true
	}
	var ok bool
	if starts, ok = parse(req.StartsAt); !ok {
		return nil, nil, "starts_at 必须是 RFC3339 时间"
	}
	if ends, ok = parse(req.EndsAt); !ok {
		return nil, nil, "ends_at 必须是 RFC3339 时间"
	}
	return starts, ends, ""
}

func (s *Server) listCampaignPhases(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	phases, err := s.m.pg.ListCampaignPhases(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"phases": phases})
}

func (s *Server) createCampaignPhase(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	var req campaignPhaseReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	starts, ends, msg := parsePhaseTimes(req)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	pos := 0
	if req.Position != nil {
		pos = *req.Position
	}
	p, err := s.m.pg.CreateCampaignPhase(id, req.Name, pos, req.Status, req.Note, starts, ends)
	if err != nil {
		if errors.Is(err, db.ErrCampaignInvalid) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) patchCampaignPhase(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("pid")), 10, 64)
	if err != nil || pid <= 0 {
		writeErr(w, 400, "阶段 id 非法")
		return
	}
	var req campaignPhaseReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	starts, ends, msg := parsePhaseTimes(req)
	if msg != "" {
		writeErr(w, 400, msg)
		return
	}
	pos := 0
	if req.Position != nil {
		pos = *req.Position
	}
	p, err := s.m.pg.UpdateCampaignPhase(id, pid, req.Name, pos, req.Status, req.Note, starts, ends)
	if err != nil {
		if errors.Is(err, db.ErrCampaignInvalid) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "阶段不存在")
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) deleteCampaignPhase(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("pid")), 10, 64)
	if err != nil || pid <= 0 {
		writeErr(w, 400, "阶段 id 非法")
		return
	}
	n, err := s.m.pg.DeleteCampaignPhase(id, pid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n > 0})
}

func (s *Server) setCampaignTaskPhase(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	tid, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("tid")), 10, 64)
	if err != nil || tid <= 0 {
		writeErr(w, 400, "任务 id 非法")
		return
	}
	var req struct {
		PhaseID int64 `json:"phase_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	n, err := s.m.pg.SetTaskCampaignPhase(id, tid, req.PhaseID)
	if err != nil {
		if errors.Is(err, db.ErrCampaignInvalid) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "任务不在该战役中")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
