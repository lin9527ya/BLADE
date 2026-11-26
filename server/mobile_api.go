package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/mobile"
	"github.com/lin9527ya/blade/traffic"
)

// Mobile returns the mobile device manager.
func (m *Manager) Mobile() *mobile.Manager { return m.mobile }

// MobileProxy returns the optional authenticated LAN mobile proxy instance.
func (m *Manager) MobileProxy() *traffic.MobileProxy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mobileProxy
}

// StartMobileProxy binds and launches the authenticated LAN proxy forwarding into m.traffic.
func (m *Manager) StartMobileProxy(listenAddr string, cidrs []string) error {
	tr := m.Traffic()
	if tr == nil {
		return fmt.Errorf("请先启动流量录制代理（-proxy），再开启认证移动端 LAN 代理")
	}
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr == "" {
		listenAddr = "0.0.0.0:19899"
	}
	mp, err := traffic.NewMobileProxy(tr, listenAddr, cidrs, "blade", "")
	if err != nil {
		return err
	}
	if err := mp.Bind(); err != nil {
		return err
	}
	m.mu.Lock()
	old := m.mobileProxy
	m.mobileProxy = mp
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	ready := make(chan struct{})
	goSafe("manager/mobile-proxy", nil, func() {
		_ = mp.StartWithReady(ready)
	})
	<-ready
	return nil
}

// StopMobileProxy closes any running authenticated LAN mobile proxy.
func (m *Manager) StopMobileProxy() error {
	m.mu.Lock()
	mp := m.mobileProxy
	m.mobileProxy = nil
	m.mu.Unlock()
	if mp != nil {
		return mp.Close()
	}
	return nil
}

func (s *Server) taskMobilePromptSummary(taskID int64) (string, bool, bool) {
	if s == nil || s.m == nil || s.m.pg == nil || taskID <= 0 {
		return "", false, false
	}
	cfg, err := s.m.pg.TaskMobileInfo(taskID)
	if err != nil {
		return "", false, false
	}
	hasApp := cfg.AppMode == "mobile" || len(cfg.AppSamplePaths) > 0 || cfg.AppPackageName != ""
	hasDevice := strings.TrimSpace(cfg.MobileDeviceID) != ""
	if !hasApp && !hasDevice {
		return "", false, false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- 应用包名: %s (%s)\n", fallbackStr(cfg.AppPackageName, "待识别"), fallbackStr(cfg.AppPlatform, "android"))
	if len(cfg.AppSamplePaths) > 0 {
		fmt.Fprintf(&b, "- 样本路径 (相对 dataDir): %s\n", strings.Join(cfg.AppSamplePaths, ", "))
	}
	if len(cfg.AppManifestJSON) > 2 {
		var meta mobile.PackageMetadata
		if json.Unmarshal(cfg.AppManifestJSON, &meta) == nil {
			if len(meta.Frameworks) > 0 {
				fmt.Fprintf(&b, "- 跨端框架: %s\n", strings.Join(meta.Frameworks, ", "))
			}
			if len(meta.Packers) > 0 {
				fmt.Fprintf(&b, "- 检测到加壳: %s\n", strings.Join(meta.Packers, ", "))
			}
			if len(meta.CandidateDomains) > 0 {
				fmt.Fprintf(&b, "- 静态提取候选域: %s\n", strings.Join(meta.CandidateDomains, ", "))
			}
			if len(meta.URLSchemes) > 0 {
				fmt.Fprintf(&b, "- URL Schemes: %s\n", strings.Join(meta.URLSchemes, ", "))
			}
			if meta.Debuggable {
				b.WriteString("- 警告: Manifest debuggable=true\n")
			}
		}
	}
	if hasDevice {
		fmt.Fprintf(&b, "- 已绑定测试机: %s (代理模式=%s)\n", cfg.MobileDeviceID, cfg.MobileProxyMode)
	}
	return strings.TrimSpace(b.String()), hasApp, hasDevice
}

// mobileBridgeAdapter implements agent.MobileBridge using Server's Mobile/Traffic/DB managers.
type mobileBridgeAdapter struct {
	s *Server
}

func (a *mobileBridgeAdapter) resolveTaskDevice(taskID int64) (db.TaskMobileConfig, error) {
	cfg, err := a.s.m.pg.TaskMobileInfo(taskID)
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(cfg.MobileDeviceID) == "" {
		return cfg, fmt.Errorf("任务 #%d 未绑定移动测试机", taskID)
	}
	return cfg, nil
}

func (a *mobileBridgeAdapter) DeviceStatus(ctx context.Context, taskID int64) (string, error) {
	cfg, err := a.resolveTaskDevice(taskID)
	if err != nil {
		return "", err
	}
	var pemCA []byte
	if tr := a.s.m.Traffic(); tr != nil {
		pemCA, _ = tr.CACertPEM()
	}
	devs, err := a.s.m.Mobile().ListDevices(ctx, pemCA)
	if err != nil {
		return "", err
	}
	for _, d := range devs {
		if d.Serial == cfg.MobileDeviceID {
			b, _ := json.MarshalIndent(d, "", "  ")
			return string(b), nil
		}
	}
	return fmt.Sprintf("绑定设备 %s 当前未出现在 adb devices 列表中（请检查 USB 连接或无线 ADB 断开）", cfg.MobileDeviceID), nil
}

func (a *mobileBridgeAdapter) AppControl(ctx context.Context, taskID int64, action, pkg, sampleOrSubpath string) (string, error) {
	cfg, err := a.resolveTaskDevice(taskID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(pkg) == "" {
		pkg = cfg.AppPackageName
	}
	mob := a.s.m.Mobile()
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "install":
		target := strings.TrimSpace(sampleOrSubpath)
		if target == "" && len(cfg.AppSamplePaths) > 0 {
			target = cfg.AppSamplePaths[0]
		}
		if target == "" {
			return "", fmt.Errorf("任务未关联安装包样本路径")
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(a.s.m.dir, filepath.FromSlash(target))
		}
		return mob.InstallAPK(ctx, cfg.MobileDeviceID, target)
	case "uninstall":
		return mob.UninstallApp(ctx, cfg.MobileDeviceID, pkg)
	case "clear_data":
		return mob.ClearAppData(ctx, cfg.MobileDeviceID, pkg)
	case "push_ca":
		tr := a.s.m.Traffic()
		if tr == nil {
			return "", fmt.Errorf("MITM 流量代理未启用")
		}
		return mob.InstallSystemCA(ctx, cfg.MobileDeviceID, tr.CACertPath())
	case "list_sandbox", "read_sandbox":
		entries, contentB64, err := mob.ReadSandboxFiles(ctx, cfg.MobileDeviceID, pkg, sampleOrSubpath)
		if err != nil {
			return "", err
		}
		if contentB64 != "" {
			raw, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(contentB64))
			if decErr == nil {
				return string(raw), nil
			}
			return contentB64, nil
		}
		b, _ := json.MarshalIndent(entries, "", "  ")
		return string(b), nil
	default:
		return "", fmt.Errorf("不支持的 mobile_app_ctrl action: %q", action)
	}
}

func (a *mobileBridgeAdapter) ProxyControl(ctx context.Context, taskID int64, action, mode string) (string, error) {
	cfg, err := a.resolveTaskDevice(taskID)
	if err != nil {
		return "", err
	}
	mob := a.s.m.Mobile()
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "enable_usb":
		rawAddr := ""
		if tr := a.s.m.Traffic(); tr != nil {
			rawAddr = tr.ProxyAddr()
		}
		port := proxyPortFromAddr(rawAddr, 9899)
		p, err := mob.SetupProxy(ctx, cfg.MobileDeviceID, "usb_reverse", port, "")
		if err != nil {
			return "", err
		}
		return "已开启 ADB 反向隧道代理：" + p, nil
	case "enable_lan":
		mp := a.s.m.MobileProxy()
		if mp == nil || !mp.Running() {
			return "", fmt.Errorf("LAN 移动代理未启动，请使用 USB 模式或在面板启动 LAN 移动代理")
		}
		info := a.s.buildMobileSetupInfo("")
		if len(info.RecommendedURLs) == 0 {
			return "", fmt.Errorf("未检测到可用局域网 IP 地址")
		}
		p, err := mob.SetupProxy(ctx, cfg.MobileDeviceID, "lan", 0, info.RecommendedURLs[0])
		if err != nil {
			return "", err
		}
		return "已切换设备全局代理为局域网认证代理：" + p, nil
	case "disable":
		if err := mob.ClearProxy(ctx, cfg.MobileDeviceID); err != nil {
			return "", err
		}
		return "已清理设备 ADB 反向隧道与全局代理设置", nil
	case "pinning_alerts":
		tr := a.s.m.Traffic()
		if tr == nil {
			return "流量录制未启用", nil
		}
		b, _ := json.MarshalIndent(tr.PinningAlerts(), "", "  ")
		return string(b), nil
	case "clear_alerts":
		if tr := a.s.m.Traffic(); tr != nil {
			tr.ClearPinningAlerts()
		}
		return "已清空 Pinning 告警缓存", nil
	default:
		return "", fmt.Errorf("不支持的 mobile_proxy_ctrl action: %q", action)
	}
}

func (a *mobileBridgeAdapter) UIExplore(ctx context.Context, taskID int64, reqJSON json.RawMessage) (string, error) {
	cfg, err := a.resolveTaskDevice(taskID)
	if err != nil {
		return "", err
	}
	var req mobile.UIActionRequest
	if err := json.Unmarshal(reqJSON, &req); err != nil {
		return "", err
	}
	mob := a.s.m.Mobile()
	if strings.EqualFold(req.Action, "dump") {
		dump, err := mob.UIDump(ctx, cfg.MobileDeviceID)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(dump, "", "  ")
		return string(b), nil
	}
	if req.Package == "" {
		req.Package = cfg.AppPackageName
	}
	return mob.PerformUIAction(ctx, cfg.MobileDeviceID, req)
}

func (a *mobileBridgeAdapter) FridaHook(ctx context.Context, taskID int64, action, pkg, mode, sessionID string, modules []string, customJS string) (string, error) {
	mob := a.s.m.Mobile()
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "catalog":
		b, _ := json.MarshalIndent(mobile.FridaCatalog(), "", "  ")
		return string(b), nil
	case "list":
		b, _ := json.MarshalIndent(mob.ListFridaSessions(taskID), "", "  ")
		return string(b), nil
	case "logs":
		sess, ok := mob.GetFridaSession(sessionID, taskID)
		if !ok {
			return "", fmt.Errorf("未找到匹配的 Frida 会话")
		}
		b, _ := json.MarshalIndent(sess, "", "  ")
		return string(b), nil
	case "stop":
		if sessionID == "" {
			if sess, ok := mob.GetFridaSession("", taskID); ok {
				sessionID = sess.ID
			}
		}
		if err := mob.StopFridaSession(sessionID); err != nil {
			return "", err
		}
		return "已停止 Frida 会话 " + sessionID, nil
	}

	cfg, err := a.resolveTaskDevice(taskID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(pkg) == "" {
		pkg = cfg.AppPackageName
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "start_server":
		return mob.StartFridaServer(ctx, cfg.MobileDeviceID, "")
	case "start":
		sess, err := mob.StartFridaSession(ctx, taskID, cfg.MobileDeviceID, pkg, mode, modules, customJS)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(sess, "", "  ")
		return string(b), nil
	default:
		return "", fmt.Errorf("不支持的 mobile_frida_hook action: %q", action)
	}
}

func (a *mobileBridgeAdapter) RPCSign(ctx context.Context, taskID int64, sessionID, method string, args []any) (string, error) {
	if _, err := a.resolveTaskDevice(taskID); err != nil {
		return "", err
	}
	res, err := a.s.m.Mobile().InvokeFridaRPC(ctx, sessionID, taskID, method, args)
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(map[string]any{"method": method, "result": res})
	return string(b), nil
}

func (a *mobileBridgeAdapter) IPCProbe(ctx context.Context, taskID int64, kind, pkg, component, uri, action string, extras map[string]string) (string, error) {
	cfg, err := a.resolveTaskDevice(taskID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(pkg) == "" {
		pkg = cfg.AppPackageName
	}
	res, err := a.s.m.Mobile().ProbeIPC(ctx, cfg.MobileDeviceID, mobile.IPCProbeRequest{
		Kind:      kind,
		Package:   pkg,
		Component: component,
		URI:       uri,
		Action:    action,
		Extras:    extras,
	})
	if err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	return string(b), nil
}

// ---- HTTP Workbench Routes ----

func (s *Server) registerMobileRoutes(mux routeRegistrar) {
	mux.HandleFunc("POST /api/apps/upload", s.appUpload)
	mux.HandleFunc("GET /api/mobile/devices", s.listMobileDevices)
	mux.HandleFunc("POST /api/mobile/devices/connect", s.connectMobileDevice)
	mux.HandleFunc("DELETE /api/mobile/devices/{serial}", s.deleteMobileDevice)
	mux.HandleFunc("GET /api/mobile/frida/scripts", s.listFridaScripts)

	mux.HandleFunc("GET /api/tasks/{id}/mobile/state", s.getTaskMobileState)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/bind", s.bindTaskMobileDevice)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/unbind", s.unbindTaskMobileDevice)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/install", s.installTaskMobileApp)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/ca-push", s.pushTaskMobileCA)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/proxy", s.toggleTaskMobileProxy)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/frida/server", s.startTaskFridaServer)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/frida/start", s.startTaskFridaSession)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/frida/stop", s.stopTaskFridaSession)
	mux.HandleFunc("GET /api/tasks/{id}/mobile/screen", s.getTaskMobileScreen)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/ui", s.runTaskMobileUI)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/ipc-probe", s.runTaskMobileIPCProbe)
	mux.HandleFunc("GET /api/tasks/{id}/mobile/sandbox", s.browseTaskMobileSandbox)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/extract-session", s.extractTaskMobileSession)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/scope-approve", s.approveTaskMobileScope)
}

func (s *Server) listMobileDevices(w http.ResponseWriter, r *http.Request) {
	var pemCA []byte
	if tr := s.m.Traffic(); tr != nil {
		pemCA, _ = tr.CACertPEM()
	}
	live, err := s.m.Mobile().ListDevices(r.Context(), pemCA)
	if err == nil {
		for _, d := range live {
			_ = s.m.pg.UpsertMobileDevice(d)
		}
	}
	records, rerr := s.m.pg.ListMobileDevices()
	if rerr != nil {
		writeErr(w, 500, rerr.Error())
		return
	}
	liveMap := map[string]mobile.DeviceInfo{}
	for _, d := range live {
		liveMap[d.Serial] = d
	}
	type mergedDevice struct {
		db.MobileDeviceRecord
		State         string `json:"state"`
		FridaPID      int    `json:"frida_pid,omitempty"`
		ProxyAddr     string `json:"proxy_addr,omitempty"`
		ForegroundPkg string `json:"foreground_pkg,omitempty"`
		Manufacturer  string `json:"manufacturer,omitempty"`
	}
	out := make([]mergedDevice, 0, len(records))
	for _, rec := range records {
		m := mergedDevice{MobileDeviceRecord: rec, State: "disconnected"}
		if ld, ok := liveMap[rec.Serial]; ok {
			m.State = ld.State
			m.FridaPID = ld.FridaPID
			m.ProxyAddr = ld.ProxyAddr
			m.ForegroundPkg = ld.ForegroundPkg
			m.Manufacturer = ld.Manufacturer
			m.Rooted = ld.Rooted
			m.RootMethod = ld.RootMethod
			m.SystemCAStatus = ld.SystemCAStatus
			m.FridaServer = ld.FridaServer
		}
		out = append(out, m)
	}
	warn := ""
	if err != nil {
		warn = err.Error()
	}
	writeJSON(w, 200, map[string]any{"devices": out, "adb_warning": warn})
}

func (s *Server) connectMobileDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint     string `json:"endpoint"`
		PairEndpoint string `json:"pair_endpoint"`
		PairCode     string `json:"pair_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	var outMsg strings.Builder
	if strings.TrimSpace(req.PairEndpoint) != "" && strings.TrimSpace(req.PairCode) != "" {
		msg, err := s.m.Mobile().PairWiFi(r.Context(), req.PairEndpoint, req.PairCode)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		outMsg.WriteString("pair: " + msg + "; ")
	}
	if strings.TrimSpace(req.Endpoint) != "" {
		msg, err := s.m.Mobile().ConnectWiFi(r.Context(), req.Endpoint)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		outMsg.WriteString("connect: " + msg)
	}
	// Refresh inventory immediately so DB reflects newly attached device.
	var pemCA []byte
	if tr := s.m.Traffic(); tr != nil {
		pemCA, _ = tr.CACertPEM()
	}
	if devs, err := s.m.Mobile().ListDevices(r.Context(), pemCA); err == nil {
		for _, d := range devs {
			_ = s.m.pg.UpsertMobileDevice(d)
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": strings.TrimSpace(outMsg.String())})
}

func (s *Server) deleteMobileDevice(w http.ResponseWriter, r *http.Request) {
	serial := strings.TrimSpace(r.PathValue("serial"))
	if serial == "" {
		writeErr(w, 400, "missing serial")
		return
	}
	if err := s.m.pg.DeleteMobileDevice(serial); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listFridaScripts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"scripts": mobile.FridaCatalog()})
}

func (s *Server) getTaskMobileState(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	cfg, err := s.m.pg.TaskMobileInfo(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var manifest *mobile.PackageMetadata
	if len(cfg.AppManifestJSON) > 2 {
		var pm mobile.PackageMetadata
		if json.Unmarshal(cfg.AppManifestJSON, &pm) == nil && (pm.PackageName != "" || pm.SampleName != "") {
			manifest = &pm
		}
	}
	var pemCA []byte
	var alerts []traffic.PinningAlert
	if tr := s.m.Traffic(); tr != nil {
		pemCA, _ = tr.CACertPEM()
		alerts = tr.PinningAlerts()
	}
	var boundDev *mobile.DeviceInfo
	if cfg.MobileDeviceID != "" {
		if devs, lerr := s.m.Mobile().ListDevices(r.Context(), pemCA); lerr == nil {
			for i := range devs {
				if devs[i].Serial == cfg.MobileDeviceID {
					cp := devs[i]
					boundDev = &cp
					_ = s.m.pg.UpsertMobileDevice(cp)
					break
				}
			}
		}
	}
	var scopeHosts []string
	if as := s.m.Assets(); as != nil {
		if rows, rerr := as.ListTaskScopeWithSources(taskID); rerr == nil {
			for _, row := range rows {
				if row.Value != "" {
					scopeHosts = appendUniqueStr(scopeHosts, strings.ToLower(row.Value))
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"task_id":          t.ID,
		"app_mode":         cfg.AppMode,
		"app_platform":     cfg.AppPlatform,
		"app_package_name": cfg.AppPackageName,
		"app_sample_paths": cfg.AppSamplePaths,
		"manifest":         manifest,
		"mobile_device_id": cfg.MobileDeviceID,
		"proxy_mode":       cfg.MobileProxyMode,
		"bound_device":     boundDev,
		"frida_sessions":   s.m.Mobile().ListFridaSessions(taskID),
		"pinning_alerts":   alerts,
		"scope_hosts":      scopeHosts,
		"setup_info":       s.buildMobileSetupInfo(r.Host),
	})
}

func (s *Server) bindTaskMobileDevice(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	var req struct {
		Serial    string `json:"serial"`
		ProxyMode string `json:"proxy_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	serial := strings.TrimPrefix(strings.TrimSpace(req.Serial), "adb:")
	if err := mobile.ValidateDeviceSerial(serial); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	if err := s.m.Mobile().BindTask(taskID, serial); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	if err := s.m.pg.BindTaskMobileDevice(taskID, serial, req.ProxyMode); err != nil {
		s.m.Mobile().UnbindTask(taskID)
		writeErr(w, 409, err.Error())
		return
	}
	proxyMode := req.ProxyMode
	if proxyMode == "" {
		proxyMode = "usb_reverse"
	}
	t.updateLifecycle(func(st *taskLifecycleState) {
		st.AppMode = "mobile"
		st.MobileDeviceID = serial
		st.MobileProxyMode = proxyMode
	})
	writeJSON(w, 200, map[string]any{"ok": true, "mobile_device_id": serial, "proxy_mode": proxyMode})
}

func (s *Server) unbindTaskMobileDevice(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	serial, err := s.m.pg.UnbindTaskMobileDevice(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.m.Mobile().UnbindTask(taskID)
	if serial != "" {
		_ = s.m.Mobile().ClearProxy(r.Context(), serial)
	}
	for _, sess := range s.m.Mobile().ListFridaSessions(taskID) {
		if sess.Running {
			_ = s.m.Mobile().StopFridaSession(sess.ID)
		}
	}
	t.updateLifecycle(func(st *taskLifecycleState) {
		st.MobileDeviceID = ""
	})
	writeJSON(w, 200, map[string]any{"ok": true, "unbound": serial})
}

func (s *Server) installTaskMobileApp(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	msg, err := (&mobileBridgeAdapter{s: s}).AppControl(r.Context(), taskID, "install", "", "")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

func (s *Server) pushTaskMobileCA(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	msg, err := (&mobileBridgeAdapter{s: s}).AppControl(r.Context(), taskID, "push_ca", "", "")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

func (s *Server) toggleTaskMobileProxy(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req struct {
		Action string `json:"action"` // enable_usb | enable_lan | disable
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	msg, err := (&mobileBridgeAdapter{s: s}).ProxyControl(r.Context(), taskID, req.Action, "")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

func (s *Server) startTaskFridaServer(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	msg, err := (&mobileBridgeAdapter{s: s}).FridaHook(r.Context(), taskID, "start_server", "", "", "", nil, "")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

func (s *Server) startTaskFridaSession(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req struct {
		Package  string   `json:"package"`
		Mode     string   `json:"mode"`
		Modules  []string `json:"modules"`
		CustomJS string   `json:"custom_js"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "请先绑定移动测试机")
		return
	}
	pkg := strings.TrimSpace(req.Package)
	if pkg == "" {
		pkg = cfg.AppPackageName
	}
	sess, err := s.m.Mobile().StartFridaSession(r.Context(), taskID, cfg.MobileDeviceID, pkg, req.Mode, req.Modules, req.CustomJS)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "session": sess})
}

func (s *Server) stopTaskFridaSession(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sid := strings.TrimSpace(req.SessionID)
	if sid == "" {
		if latest, exists := s.m.Mobile().GetFridaSession("", taskID); exists {
			sid = latest.ID
		}
	}
	if err := s.m.Mobile().StopFridaSession(sid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "stopped": sid})
}

func (s *Server) getTaskMobileScreen(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "任务未绑定移动测试机")
		return
	}
	pngBytes, err := s.m.Mobile().Screenshot(r.Context(), cfg.MobileDeviceID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	dump, _ := s.m.Mobile().UIDump(r.Context(), cfg.MobileDeviceID)
	writeJSON(w, 200, map[string]any{
		"png_base64": base64.StdEncoding.EncodeToString(pngBytes),
		"ui_tree":    dump,
	})
}

func (s *Server) runTaskMobileUI(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req mobile.UIActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "任务未绑定移动测试机")
		return
	}
	if req.Package == "" {
		req.Package = cfg.AppPackageName
	}
	msg, err := s.m.Mobile().PerformUIAction(r.Context(), cfg.MobileDeviceID, req)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

func (s *Server) runTaskMobileIPCProbe(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req mobile.IPCProbeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "任务未绑定移动测试机")
		return
	}
	if req.Package == "" {
		req.Package = cfg.AppPackageName
	}
	res, err := s.m.Mobile().ProbeIPC(r.Context(), cfg.MobileDeviceID, req)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) browseTaskMobileSandbox(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "任务未绑定移动测试机")
		return
	}
	pkg := strings.TrimSpace(r.URL.Query().Get("pkg"))
	if pkg == "" {
		pkg = cfg.AppPackageName
	}
	sub := strings.TrimSpace(r.URL.Query().Get("path"))
	entries, fileB64, err := s.m.Mobile().ReadSandboxFiles(r.Context(), cfg.MobileDeviceID, pkg, sub)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	preview := ""
	if fileB64 != "" {
		if dec, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(fileB64)); derr == nil {
			preview = string(dec)
		} else {
			preview = fileB64
		}
	}
	writeJSON(w, 200, map[string]any{
		"package": pkg,
		"path":    sub,
		"entries": entries,
		"preview": preview,
	})
}

var reHeaderAuthLine = regexp.MustCompile(`(?im)^(?:Authorization|Cookie|X-Auth-Token|X-Token|Token|Access-Token|X-Session-Id):\s*([^\r\n]+)$`)

// extractTaskMobileSession inspects recent captured mobile traffic headers and writes verified tokens
// directly into the task's exploration credential & fact graph for Planner/Worker reuse.
func (s *Server) extractTaskMobileSession(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	var req struct {
		HostFilter string `json:"host"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 400, "流量录制代理未启用")
		return
	}
	rows, _, err := tr.Page(traffic.PageQuery{
		Host:         strings.TrimSpace(req.HostFilter),
		Source:       "mobile",
		ExcludeNoise: true,
	}, 0, 40)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Fall back to recent non-noise exchanges if mobile source filter had none (e.g. adb reverse flows go to loopback as "web").
	if len(rows) == 0 {
		rows, _, _ = tr.Page(traffic.PageQuery{
			Host:         strings.TrimSpace(req.HostFilter),
			ExcludeNoise: true,
		}, 0, 25)
	}

	type extractedCred struct {
		Host   string `json:"host"`
		Header string `json:"header"`
		Value  string `json:"value"`
		ExID   string `json:"exchange_id"`
	}
	seen := map[string]bool{}
	var found []extractedCred
	beginFact, _ := t.Store.OriginFactID()
	for _, ex := range rows {
		rawReq, _, err := tr.Get(ex.ID)
		if err != nil {
			continue
		}
		matches := reHeaderAuthLine.FindAllString(rawReq, 10)
		for _, line := range matches {
			k, v, has := strings.Cut(line, ":")
			if !has {
				continue
			}
			key := strings.TrimSpace(k)
			val := strings.TrimSpace(v)
			if len(val) < 8 || seen[ex.Host+"\x00"+key+"\x00"+val] {
				continue
			}
			seen[ex.Host+"\x00"+key+"\x00"+val] = true
			found = append(found, extractedCred{Host: ex.Host, Header: key, Value: val, ExID: ex.ID})
			_, _ = t.Store.AddLinkedNode(db.KindCredential, map[string]any{
				"kind":        "token",
				"identity":    key + "@" + ex.Host,
				"secret":      val,
				"summary":     fmt.Sprintf("移动端人工登录提取会话头 %s (%s)", key, ex.Host),
				"targets":     []string{ex.Host},
				"exchange_id": ex.ID,
			}, 0, db.StateLootActive, "human", nil, beginFact, db.RelYields)
			if len(found) >= 12 {
				break
			}
		}
		if len(found) >= 12 {
			break
		}
	}

	if len(found) > 0 {
		_, _ = t.Store.AddLinkedNode(db.KindFact, map[string]any{
			"summary": fmt.Sprintf("操作者从移动端提取了 %d 条实时会话凭据，可直接用于后续越权与业务接口测试", len(found)),
			"tokens":  found,
		}, 0, "confirmed", "human", nil, beginFact, db.RelYields)
		t.Notify()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "extracted": found, "count": len(found)})
}

func (s *Server) approveTaskMobileScope(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	var req struct {
		Domains []string `json:"domains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, "asset store unavailable")
		return
	}
	added := 0
	for _, dom := range req.Domains {
		dom = strings.ToLower(strings.TrimSpace(dom))
		if dom == "" {
			continue
		}
		if net.ParseIP(dom) != nil {
			_, _ = as.UpsertIP(db.UpsertIPReq{IP: dom, TaskID: taskID})
			if _, err := as.AddAgentScope(taskID, "ip", dom, "移动工作台确认候选域名", "manual"); err == nil {
				added++
			}
		} else {
			_, _ = as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: dom, TaskID: taskID})
			if _, err := as.AddAgentScope(taskID, "subdomain", dom, "移动工作台确认候选域名", "manual"); err == nil {
				added++
			}
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "added": added})
}

func parseTaskIDOr404(s *Server, w http.ResponseWriter, r *http.Request) (int64, bool) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return 0, false
	}
	tid, err := strconv.ParseInt(t.ID, 10, 64)
	if err != nil || tid <= 0 {
		writeErr(w, 400, "invalid task id")
		return 0, false
	}
	return tid, true
}

func (s *Server) buildMobileSetupInfo(reqHost string) traffic.MobileSetupInfo {
	rawAddr := ""
	if tr := s.m.Traffic(); tr != nil {
		rawAddr = tr.ProxyAddr()
	}
	mitmPort := proxyPortFromAddr(rawAddr, 9899)
	info := traffic.MobileSetupInfo{
		LocalMITMPort:  mitmPort,
		USBReverseCmd:  fmt.Sprintf("adb reverse tcp:%d tcp:%d", mitmPort, mitmPort),
		USBProxySetCmd: fmt.Sprintf("adb shell settings put global http_proxy 127.0.0.1:%d", mitmPort),
		USBProxyClrCmd: "adb shell settings delete global http_proxy && adb reverse --remove-all",
	}
	if tr := s.m.Traffic(); tr != nil {
		info.CAHashAndroid, _ = tr.CASubjectHash()
	}
	if mp := s.m.MobileProxy(); mp != nil && mp.Running() {
		u, p, cidrs := mp.Credentials()
		info.Enabled = true
		info.ListenAddr = mp.Addr()
		info.Username = u
		info.Password = p
		info.AllowCIDRs = cidrs
		info.CAToken = mp.MintCAToken(30 * time.Minute)
		_, mpPort, _ := net.SplitHostPort(mp.Addr())
		for _, ip := range localLANIPs(reqHost) {
			info.RecommendedURLs = append(info.RecommendedURLs, net.JoinHostPort(ip, mpPort))
		}
	}
	return info
}

func proxyPortFromAddr(addr string, def int) int {
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		if p, perr := strconv.Atoi(portStr); perr == nil && p > 0 {
			return p
		}
	}
	return def
}

func localLANIPs(reqHost string) []string {
	var ips []string
	if h, _, err := net.SplitHostPort(reqHost); err == nil && h != "" && h != "127.0.0.1" && h != "localhost" {
		ips = appendUniqueStr(ips, h)
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP == nil {
				continue
			}
			v4 := ipnet.IP.To4()
			if v4 == nil || v4.IsLoopback() {
				continue
			}
			ips = appendUniqueStr(ips, v4.String())
		}
	}
	return ips
}

func fallbackStr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
