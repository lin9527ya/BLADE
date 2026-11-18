package server

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// GET /api/egress/status：出口只读视图。
//
// 组合三份缓存：代理池健康、Clash 最近轮换、任务→出口组绑定。不做实时探测，
// 避免每次打开页面都拖慢请求；也不返回 Clash 控制器密钥或带 userinfo 的代理 URL。
func (s *Server) egressStatus(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	proxy := s.m.ProxyStatus()
	for i := range proxy.Candidates {
		proxy.Candidates[i].Proxy = redactProxyURL(proxy.Candidates[i].Proxy)
	}
	proxy.Active = redactProxyURL(proxy.Active)

	type taskBinding struct {
		TaskID int64  `json:"task_id"`
		Name   string `json:"name,omitempty"`
		Group  string `json:"group"`
	}
	bindings := s.m.ClashTaskBindings()
	tasks := make([]taskBinding, 0, len(bindings))
	for idText, group := range bindings {
		id, _ := strconv.ParseInt(idText, 10, 64)
		name := ""
		if t, ok := s.m.Task(idText); ok && t != nil {
			name = strings.TrimSpace(t.Name)
			if name == "" {
				name = t.Description
			}
		}
		tasks = append(tasks, taskBinding{TaskID: id, Name: name, Group: group})
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })

	writeJSON(w, 200, map[string]any{
		"proxy": proxy,
		"clash": s.m.ClashEgressStatus(),
		"tasks": tasks,
		"note":  "只读缓存：不实时探测节点，也不返回 Clash 控制器密钥。",
	})
}

// redactProxyURL 去掉代理 URL 里的 userinfo（http://user:pass@host）。
func redactProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}
