package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

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
