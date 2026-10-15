package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/intel"
)

// P21 情报源的两个小接口：测试连接（设置页按钮）与台账（用量/排障）。

// testIntelSource 用当前设置打一次 provider 的 Info：验证 key 与端点是否可用，
// 并回显账号信息与剩余额度。**不回显 key 本身**。
func (s *Server) testIntelSource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string `json:"provider"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Provider)
	if name == "" {
		name = intel.Names()[0]
	}
	cfg := s.intelConfig()
	if strings.TrimSpace(cfg.Key) == "" {
		writeErr(w, 400, "还没配 API Key：先填入 key 再测试")
		return
	}
	prov, err := intel.New(name, cfg)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	info, err := prov.Info(ctx)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	used, quota := s.intelUsageToday(r.Context())
	// 账号信息原样回（不含 key；provider 也不会回 key），另附平台侧用量。
	writeJSON(w, 200, map[string]any{"ok": true, "info": info, "used_today": used, "daily_quota": quota})
}

// searchIntelAssets 是资产页「FOFA 搜索」的后端：与 agent 的 intel_search 工具
// **同一条执行路径**（同配额、同台账、同落盘），只是回执是结构化 JSON。
// 无任务上下文（人手工查）时结果落 <数据目录>/intel/。
//
// 状态码分两类：改配置就能解决的（没配 key / 配额用尽 / 表达式为空 / 未知源）→ 400；
// 上游失败、应答解析失败 → 502（可重试）。混成一个码会让前端无法给出正确的下一步。
func (s *Server) searchIntelAssets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider string   `json:"provider"`
		Expr     string   `json:"expr"`
		Limit    int      `json:"limit"`
		Fields   []string `json:"fields"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, spool, used, quota, note, err := s.intelSearch(r.Context(), agent.RunInfo{}, intelSearchRequest{
		Provider: req.Provider, Expr: req.Expr, Limit: req.Limit, Fields: req.Fields,
	})
	if err != nil {
		var ue intelUserError
		if errors.As(err, &ue) {
			writeErr(w, http.StatusBadRequest, err.Error()+note)
			return
		}
		writeErr(w, http.StatusBadGateway, err.Error()+note)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"provider":    res.Provider,
		"expr":        res.Expr,
		"fields":      res.Fields,
		"total":       res.Total,
		"returned":    len(res.Assets),
		"assets":      res.Assets,
		"spool":       spool,
		"used_today":  used + 1,
		"daily_quota": quota,
		"note":        note,
	})
}

// listIntelQueries 返回最近的查询台账（设置页展示"今日用量"与排障用）。
func (s *Server) listIntelQueries(w http.ResponseWriter, r *http.Request) {
	rows, err := s.m.pg.ListIntelQueries(r.Context(), 50)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	used, quota := s.intelUsageToday(r.Context())
	writeJSON(w, 200, map[string]any{"queries": rows, "used_today": used, "daily_quota": quota})
}
