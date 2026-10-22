package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// 指纹打法库（全局）：列表 / 详情 / 启用-废弃。所有路由都在 registerRoutes 里挂载，
// 因此与其它 /api/* 一样走 requireAuth。
func (s *Server) registerPlaybookRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/playbooks", s.listPlaybooks)
	mux.HandleFunc("GET /api/playbooks/{id}", s.getPlaybook)
	mux.HandleFunc("PATCH /api/playbooks/{id}", s.patchPlaybook)
}

func (s *Server) listPlaybooks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	list, total, err := s.m.Assets().ListPlaybooks(db.PlaybookFilter{
		Q: q.Get("q"), Status: q.Get("status"), Limit: limit, Offset: offset,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"playbooks": list, "total": total})
}

func (s *Server) getPlaybook(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	p, err := s.m.Assets().GetPlaybook(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "打法不存在")
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) patchPlaybook(w http.ResponseWriter, r *http.Request) {
	id := parseIntDefault(r.PathValue("id"))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	status := strings.ToLower(strings.TrimSpace(req.Status))
	if status == db.PlaybookStatusDeprecated && strings.TrimSpace(req.Reason) == "" {
		// 与公司知识/发现撤回同款纪律：撤下一张打法必须写清"为什么不能用了"。
		writeErr(w, 400, "废弃必须给出理由（后来人最需要的就是这句）")
		return
	}
	n, err := s.m.Assets().SetPlaybookStatus(id, status, strings.TrimSpace(req.Reason))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if n == 0 {
		writeErr(w, 404, "打法不存在")
		return
	}
	p, err := s.m.Assets().GetPlaybook(id)
	if err != nil || p == nil {
		writeErr(w, 500, "读取更新后的打法失败")
		return
	}
	writeJSON(w, 200, p)
}
