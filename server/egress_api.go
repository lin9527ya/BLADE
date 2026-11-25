package server

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/traffic"
)

// GET /api/egress/status：出口只读视图。
//
// 组合三份缓存：代理池健康、Clash 最近轮换、任务→出口组绑定。不做实时探测，
// 避免每次打开页面都拖慢请求；也不返回 Clash 控制器密钥或带 userinfo 的代理 URL。
func (s *Server) egressStatus(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	proxy := redactProxyStatus(s.m.ProxyStatus())

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

	recordedCount := 0
	proxyListening, caReady, proxyAddr := false, false, ""
	if tr := s.m.Traffic(); tr != nil {
		proxyListening = true
		proxyAddr = tr.ProxyAddr()
		caReady = tr.CACertPath() != ""
		if n, err := tr.Count(); err == nil {
			recordedCount = n
		}
	}

	writeJSON(w, 200, map[string]any{
		"proxy": proxy,
		"clash": s.m.ClashEgressStatus(),
		"tasks": tasks,
		"traffic": map[string]any{
			"capture_enabled":            s.m.TrafficEnabled(),
			"auto_binding_enabled":       s.m.pg.GetBool(settingAgentTrafficBinding, false),
			"proxy_listening":            proxyListening,
			"proxy_addr":                 proxyAddr,
			"ca_ready":                   caReady,
			"recorded_count":             recordedCount,
			"clash_isolation_overridden": s.m.taskEgressIsolationDisabled() && len(taskEgressPool(s.m.ClashEgressConfig())) > 0,
		},
		"note": "只读缓存：不实时探测节点，也不返回 Clash 控制器密钥。",
	})
}

func redactProxyURL(raw string) string {
	return traffic.RedactProxyURL(raw)
}

func redactProxyStatus(st ProxyPoolStatus) ProxyPoolStatus {
	st.Active = traffic.RedactProxyURL(st.Active)
	st.Pinned = traffic.RedactProxyURL(st.Pinned)
	cands := make([]ProxyHealth, len(st.Candidates))
	for i, c := range st.Candidates {
		c.Proxy = traffic.RedactProxyURL(c.Proxy)
		cands[i] = c
	}
	st.Candidates = cands
	return st
}
