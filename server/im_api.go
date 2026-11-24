package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/im"
	"github.com/lin9527ya/blade/notify"
)

type imConnectionDTO struct {
	ID              int64          `json:"id"`
	Name            string         `json:"name"`
	Platform        string         `json:"platform"`
	Enabled         bool           `json:"enabled"`
	AppID           string         `json:"app_id"`
	AppSecret       string         `json:"app_secret"`
	Extra           map[string]any `json:"extra"`
	Status          string         `json:"status"`
	LastError       string         `json:"last_error"`
	LastConnectedAt *time.Time     `json:"last_connected_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type imConnectionRequest struct {
	Name      string         `json:"name"`
	Platform  string         `json:"platform"`
	AppID     string         `json:"app_id"`
	AppSecret string         `json:"app_secret"`
	Enabled   bool           `json:"enabled"`
	Extra     map[string]any `json:"extra"`
}

type imUserRequest struct {
	Platform    string `json:"platform"`
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Enabled     *bool  `json:"enabled"`
	Note        string `json:"note"`
}

func (s *Server) registerIMRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /api/im/meta", s.imMeta)
	mux.HandleFunc("GET /api/im/connections", s.imListConnections)
	mux.HandleFunc("POST /api/im/connections", s.imCreateConnection)
	mux.HandleFunc("PUT /api/im/connections/{id}", s.imUpdateConnection)
	mux.HandleFunc("DELETE /api/im/connections/{id}", s.imDeleteConnection)
	mux.HandleFunc("POST /api/im/connections/{id}/test", s.imTestConnection)
	mux.HandleFunc("GET /api/im/users", s.imListUsers)
	mux.HandleFunc("POST /api/im/users", s.imCreateUser)
	mux.HandleFunc("PUT /api/im/users/{id}", s.imUpdateUser)
	mux.HandleFunc("DELETE /api/im/users/{id}", s.imDeleteUser)
	mux.HandleFunc("GET /api/im/messages", s.imListMessages)
}

func (s *Server) requireIMAdmin(w http.ResponseWriter, r *http.Request) bool {
	if canManagePlatform(r.Context()) {
		return true
	}
	writeErr(w, http.StatusForbidden, "IM 控制配置需要管理员权限")
	return false
}

func validIMPlatform(platform string) bool {
	return platform == im.PlatformDingTalk || platform == im.PlatformFeishu
}

var imSecretExtraKeys = []string{"verification_token", "encrypt_key"}

func imConnectionExtra(raw json.RawMessage) map[string]any {
	extra := map[string]any{"allow_group": false}
	var stored map[string]any
	if len(raw) > 0 && json.Unmarshal(raw, &stored) == nil {
		if v, ok := stored["allow_group"].(bool); ok {
			extra["allow_group"] = v
		}
		for _, key := range imSecretExtraKeys {
			if v, ok := stored[key].(string); ok && v != "" {
				extra[key] = notify.MaskedValue(v)
			}
		}
	}
	return extra
}

func toIMConnectionDTO(c *db.IMConnection) imConnectionDTO {
	return imConnectionDTO{
		ID: c.ID, Name: c.Name, Platform: c.Platform, Enabled: c.Enabled,
		AppID: c.AppID, AppSecret: notify.MaskedValue(c.AppSecret), Extra: imConnectionExtra(c.Extra),
		Status: c.Status, LastError: c.LastError, LastConnectedAt: c.LastConnectedAt,
		CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// mergeIMExtra preserves secret extra fields when the client sends the masked
// placeholder (or omits them). An empty string means "leave as-is"; delete and
// recreate the connection to fully reset credentials.
func mergeIMExtra(current json.RawMessage, raw map[string]any) json.RawMessage {
	merged := map[string]any{}
	if len(current) > 0 {
		_ = json.Unmarshal(current, &merged)
	}
	if raw != nil {
		if v, ok := raw["allow_group"].(bool); ok {
			merged["allow_group"] = v
		}
		for _, key := range imSecretExtraKeys {
			v, ok := raw[key].(string)
			if !ok {
				continue
			}
			v = strings.TrimSpace(v)
			if v == "" || notify.IsMasked(v) {
				continue
			}
			merged[key] = v
		}
	}
	b, _ := json.Marshal(merged)
	return b
}

func (s *Server) imMeta(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"platforms": []map[string]any{
			{"key": im.PlatformDingTalk, "label": "钉钉", "fields": []string{"app_id", "app_secret", "extra.allow_group"}, "hint": "钉钉开放平台自建应用的 Client ID / Client Secret，事件订阅选择 Stream 模式。"},
			{"key": im.PlatformFeishu, "label": "飞书", "fields": []string{"app_id", "app_secret", "extra.allow_group"}, "hint": "飞书开放平台自建应用的 App ID / App Secret，事件订阅选择长连接并订阅 im.message.receive_v1。"},
		},
		"roles": []string{"viewer", "operator"},
		"note":  "viewer 只能查看任务与范围；operator 可创建、暂停/继续、编辑元数据。所有操作按平台用户 ID 记录审计。",
	})
}

func (s *Server) imListConnections(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	conns, err := pg.ListIMConnections()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]imConnectionDTO, 0, len(conns))
	for _, c := range conns {
		out = append(out, toIMConnectionDTO(c))
	}
	writeJSON(w, 200, map[string]any{"connections": out})
}

func (s *Server) imCreateConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req imConnectionRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	req.AppID = strings.TrimSpace(req.AppID)
	req.AppSecret = strings.TrimSpace(req.AppSecret)
	req.Name = strings.TrimSpace(req.Name)
	if !validIMPlatform(req.Platform) {
		writeErr(w, 400, "platform 必须是 dingtalk 或 feishu")
		return
	}
	if req.AppID == "" || req.AppSecret == "" {
		writeErr(w, 400, "app_id 与 app_secret 不能为空")
		return
	}
	if req.Name == "" {
		req.Name = req.Platform
	}
	status := "disabled"
	if req.Enabled {
		status = "connecting"
	}
	c, err := pg.CreateIMConnection(r.Context(), &db.IMConnection{
		Name: req.Name, Platform: req.Platform, Enabled: req.Enabled,
		AppID: req.AppID, AppSecret: req.AppSecret, Extra: mergeIMExtra(nil, req.Extra), Status: status,
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeErr(w, 409, "同一平台下该 app_id 已存在")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.reloadIMService()
	writeJSON(w, 201, toIMConnectionDTO(c))
}

func (s *Server) imUpdateConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的连接 id")
		return
	}
	cur, err := pg.GetIMConnection(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if cur == nil {
		writeErr(w, 404, "连接不存在")
		return
	}
	var req imConnectionRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if strings.TrimSpace(req.Platform) != "" {
		req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
		if !validIMPlatform(req.Platform) {
			writeErr(w, 400, "platform 必须是 dingtalk 或 feishu")
			return
		}
		cur.Platform = req.Platform
	}
	if strings.TrimSpace(req.Name) != "" {
		cur.Name = strings.TrimSpace(req.Name)
	}
	if strings.TrimSpace(req.AppID) != "" {
		cur.AppID = strings.TrimSpace(req.AppID)
	}
	if secret := strings.TrimSpace(req.AppSecret); secret != "" && !notify.IsMasked(secret) {
		cur.AppSecret = secret
	}
	cur.Enabled = req.Enabled
	if req.Extra != nil {
		cur.Extra = mergeIMExtra(cur.Extra, req.Extra)
	}
	if !cur.Enabled {
		cur.Status = "disabled"
	} else {
		cur.Status = "connecting"
	}
	updated, err := pg.UpdateIMConnection(r.Context(), cur)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeErr(w, 409, "同一平台下该 app_id 已存在")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	if updated == nil {
		writeErr(w, 404, "连接不存在")
		return
	}
	s.reloadIMService()
	writeJSON(w, 200, toIMConnectionDTO(updated))
}

func (s *Server) imDeleteConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的连接 id")
		return
	}
	ok, err := pg.DeleteIMConnection(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !ok {
		writeErr(w, 404, "连接不存在")
		return
	}
	s.reloadIMService()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) imTestConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的连接 id")
		return
	}
	c, err := pg.GetIMConnection(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if c == nil {
		writeErr(w, 404, "连接不存在")
		return
	}
	connector, err := im.New(im.Config{
		ConnectionID: c.ID, Name: c.Name, Platform: c.Platform,
		AppID: c.AppID, AppSecret: c.AppSecret, Extra: imExtra(c.Extra),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := connector.Probe(ctx); err != nil {
		_ = pg.SetIMConnectionStatus(r.Context(), c.ID, "error", err.Error(), false)
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	_ = pg.SetIMConnectionStatus(r.Context(), c.ID, "connecting", "", false)
	writeJSON(w, 200, map[string]any{"ok": true, "message": "凭据校验通过；长连接状态以列表中的 status 为准。"})
}

func (s *Server) imListUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	users, err := pg.ListIMAllowedUsers()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"users": users})
}

func validateIMUser(req *imUserRequest, requireIdentity bool) error {
	req.Platform = strings.ToLower(strings.TrimSpace(req.Platform))
	req.UserID = strings.TrimSpace(req.UserID)
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	if req.Role == "" {
		req.Role = "viewer"
	}
	if req.Role != "viewer" && req.Role != "operator" {
		return &imValidationError{"role 必须是 viewer 或 operator"}
	}
	if requireIdentity {
		if !validIMPlatform(req.Platform) {
			return &imValidationError{"platform 必须是 dingtalk 或 feishu"}
		}
		if req.UserID == "" {
			return &imValidationError{"user_id 不能为空"}
		}
	}
	return nil
}

type imValidationError struct{ msg string }

func (e *imValidationError) Error() string { return e.msg }

func (s *Server) imCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req imUserRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateIMUser(&req, true); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	u, err := pg.CreateIMAllowedUser(r.Context(), &db.IMAllowedUser{
		Platform: req.Platform, UserID: req.UserID, DisplayName: strings.TrimSpace(req.DisplayName),
		Role: req.Role, Enabled: enabled, Note: strings.TrimSpace(req.Note),
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			writeErr(w, 409, "该平台用户已存在")
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, u)
}

func (s *Server) imUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的用户 id")
		return
	}
	var req imUserRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateIMUser(&req, false); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	u, err := pg.UpdateIMAllowedUser(r.Context(), &db.IMAllowedUser{
		ID: id, DisplayName: strings.TrimSpace(req.DisplayName), Role: req.Role,
		Enabled: enabled, Note: strings.TrimSpace(req.Note),
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if u == nil {
		writeErr(w, 404, "用户不存在")
		return
	}
	writeJSON(w, 200, u)
}

func (s *Server) imDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "无效的用户 id")
		return
	}
	ok, err := pg.DeleteIMAllowedUser(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !ok {
		writeErr(w, 404, "用户不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) imListMessages(w http.ResponseWriter, r *http.Request) {
	if !s.requireIMAdmin(w, r) {
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	msgs, err := pg.RecentIMMessages(r.Context(), limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"messages": msgs})
}

func (s *Server) reloadIMService() {
	if s.imSvc != nil {
		s.imSvc.Reload()
	}
}
