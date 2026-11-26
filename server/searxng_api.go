package server

import (
	"encoding/json"
	"net/http"
)

func (s *Server) refreshSearchAgentsAfterConfigChange() {
	s.invalidateProfileChatAgents()
	s.llm.mu.Lock()
	cfg, on := s.llm.cfg, s.llm.on
	s.llm.mu.Unlock()
	if on {
		_ = s.applyLLM(cfg)
	} else {
		s.invalidateTaskAgents()
	}
}

func (s *Server) getSearxngStatus(w http.ResponseWriter, r *http.Request) {
	sx := s.m.searxng()
	if sx == nil {
		writeErr(w, 500, "SearXNG 管理器未初始化")
		return
	}
	writeJSON(w, 200, sx.Status(r.Context()))
}

func (s *Server) putSearxngConfig(w http.ResponseWriter, r *http.Request) {
	sx := s.m.searxng()
	if sx == nil {
		writeErr(w, 500, "SearXNG 管理器未初始化")
		return
	}
	var req SearxngManagedConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	st, err := sx.SaveConfig(req)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.refreshSearchAgentsAfterConfigChange()
	writeJSON(w, 200, st)
}

func (s *Server) installSearxng(w http.ResponseWriter, r *http.Request) {
	sx := s.m.searxng()
	if sx == nil {
		writeErr(w, 500, "SearXNG 管理器未初始化")
		return
	}
	var req struct {
		Reinstall bool `json:"reinstall"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	st, err := sx.InstallOnlyAsync(req.Reinstall)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) startSearxng(w http.ResponseWriter, r *http.Request) {
	sx := s.m.searxng()
	if sx == nil {
		writeErr(w, 500, "SearXNG 管理器未初始化")
		return
	}
	var req struct {
		AutoInstall bool `json:"auto_install"`
		Reinstall   bool `json:"reinstall"`
	}
	// 默认允许首次点击「一键部署并启动」自动完成安装与拉起。
	req.AutoInstall = true
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	st, err := sx.StartAsync(req.AutoInstall, req.Reinstall)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	s.refreshSearchAgentsAfterConfigChange()
	writeJSON(w, 200, st)
}

func (s *Server) stopSearxng(w http.ResponseWriter, _ *http.Request) {
	sx := s.m.searxng()
	if sx == nil {
		writeErr(w, 500, "SearXNG 管理器未初始化")
		return
	}
	st, err := sx.Stop()
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) activateSearxngInWebSearch(w http.ResponseWriter, r *http.Request) {
	sx := s.m.searxng()
	if sx == nil {
		writeErr(w, 500, "SearXNG 管理器未初始化")
		return
	}
	st := sx.Status(r.Context())
	if st.URL == "" {
		writeErr(w, 400, "未解析到 SearXNG 本地地址")
		return
	}
	if err := sx.applyURLToManager(st.URL, true); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.refreshSearchAgentsAfterConfigChange()
	writeJSON(w, 200, sx.Status(r.Context()))
}
