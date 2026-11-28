package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// wireMobileCaptureHook forwards plaintext HTTP captures sent by Frida native/SSL hooks
// directly into BLADE's recording traffic index under source="mobile_hook".
func (m *Manager) wireMobileCaptureHook() {
	if m == nil || m.mobile == nil {
		return
	}
	m.mobile.SetCaptureHook(func(_ int64, payload map[string]any) {
		tr := m.Traffic()
		if tr == nil || payload == nil {
			return
		}
		strField := func(k string) string {
			v, _ := payload[k].(string)
			return v
		}
		status := 200
		if f, ok := payload["status"].(float64); ok && int(f) > 0 {
			status = int(f)
		}
		tr.RecordSyntheticMobileExchange(
			strField("host"),
			strField("method"),
			strField("url"),
			status,
			strField("req_head"),
			strField("req_body"),
			strField("resp_head"),
			strField("resp_body"),
			strField("source"),
		)
	})
}

// TransparentProxy returns any active loopback SNI/HTTP bridge for iptables REDIRECT.
func (m *Manager) TransparentProxy() *traffic.MobileTransparentBridge {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.transparentProxy
}

// StartTransparentProxy ensures the loopback SNI/HTTP transparent bridge is listening and returns its TCP port.
func (m *Manager) StartTransparentProxy() (int, error) {
	tr := m.Traffic()
	if tr == nil {
		return 0, fmt.Errorf("请先启动流量录制代理（-proxy），再开启 Root 透明代理分流")
	}
	m.wireMobileCaptureHook()
	m.mu.RLock()
	cur := m.transparentProxy
	m.mu.RUnlock()
	if cur != nil && cur.Running() && cur.Port() > 0 {
		return cur.Port(), nil
	}
	bridge, err := traffic.NewMobileTransparentBridge(tr, "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	if err := bridge.Bind(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	old := m.transparentProxy
	m.transparentProxy = bridge
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	ready := make(chan struct{})
	goSafe("manager/mobile-transparent-bridge", nil, func() {
		_ = bridge.StartWithReady(ready)
	})
	<-ready
	return bridge.Port(), nil
}

// StopTransparentProxy stops the loopback SNI/HTTP transparent bridge if running.
func (m *Manager) StopTransparentProxy() error {
	m.mu.Lock()
	b := m.transparentProxy
	m.transparentProxy = nil
	m.mu.Unlock()
	if b != nil {
		return b.Close()
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
			if len(meta.SignatureSchemes) > 0 {
				fmt.Fprintf(&b, "- 签名方案: %s\n", strings.Join(meta.SignatureSchemes, ", "))
			}
			if meta.HermesBytecode {
				b.WriteString("- 跨端字节码: 检测到 Hermes JS Bytecode (已自动提取字串表)\n")
			}
			if len(meta.NativeBinaries) > 0 {
				fmt.Fprintf(&b, "- 原生二进制: %d 个 ELF/Mach-O 镜像已审计\n", len(meta.NativeBinaries))
			}
			if len(meta.SBOMHints) > 0 {
				fmt.Fprintf(&b, "- 三方库版本线索 (SBOM): %s\n", strings.Join(meta.SBOMHints[:min(len(meta.SBOMHints), 8)], ", "))
			}
			if len(meta.StaticFindings) > 0 {
				var hi []string
				for _, sf := range meta.StaticFindings {
					if sf.Severity == "high" || sf.Severity == "medium" {
						hi = append(hi, fmt.Sprintf("[%s/%s] %s", strings.ToUpper(sf.Severity), sf.MASVS, sf.Title))
					}
					if len(hi) >= 6 {
						break
					}
				}
				if len(hi) > 0 {
					fmt.Fprintf(&b, "- SAST 高中危命中 (%d 项摘要): %s\n", len(meta.StaticFindings), strings.Join(hi, " ; "))
				}
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
	case "enable_transparent":
		transPort, err := a.s.m.StartTransparentProxy()
		if err != nil {
			return "", err
		}
		p, err := mob.SetupProxy(ctx, cfg.MobileDeviceID, "transparent", transPort, "")
		if err != nil {
			return "", err
		}
		return "已开启 Root iptables 透明代理（含 UDP 443 QUIC 降级）：" + p, nil
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
		err := mob.ClearProxy(ctx, cfg.MobileDeviceID)
		_ = a.s.m.StopTransparentProxy()
		if err != nil {
			return "", err
		}
		return "已清理设备 ADB 反向隧道、透明代理规则与全局代理设置", nil
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
	var extra struct {
		SaveName string `json:"save_name"`
	}
	_ = json.Unmarshal(reqJSON, &extra)
	mob := a.s.m.Mobile()
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "dump":
		dump, err := mob.UIDump(ctx, cfg.MobileDeviceID)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(dump, "", "  ")
		return string(b), nil
	case "screenshot":
		pngBytes, err := mob.Screenshot(ctx, cfg.MobileDeviceID)
		if err != nil {
			return "", err
		}
		fileName := sanitizeScreenshotName(extra.SaveName)
		taskDir := filepath.Join(a.s.m.dir, "tasks", strconv.FormatInt(taskID, 10))
		shotDir := filepath.Join(taskDir, "screenshots")
		if err := os.MkdirAll(shotDir, 0o755); err != nil {
			return "", err
		}
		target := filepath.Join(shotDir, fileName)
		if err := os.WriteFile(target, pngBytes, 0o600); err != nil {
			return "", err
		}
		out := map[string]any{
			"screenshot_path": filepath.ToSlash(filepath.Join("screenshots", fileName)),
			"bytes":           len(pngBytes),
		}
		if dump, derr := mob.UIDump(ctx, cfg.MobileDeviceID); derr == nil && dump != nil {
			out["foreground_pkg"] = dump.ForegroundPkg
			out["element_count"] = dump.ElementCount
		}
		b, _ := json.MarshalIndent(out, "", "  ")
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
		a.s.m.wireMobileCaptureHook()
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

func (a *mobileBridgeAdapter) Audit(ctx context.Context, taskID int64, kind string, opts json.RawMessage) (string, error) {
	mob := a.s.m.Mobile()
	cfg, err := a.s.m.pg.TaskMobileInfo(taskID)
	if err != nil {
		return "", err
	}
	var params struct {
		Package     string `json:"package"`
		AppID       string `json:"appid"`
		SamplePath  string `json:"sample_path"`
		Mode        string `json:"mode"`
		SkipApktool bool   `json:"skip_apktool"`
		MaxProbes   int    `json:"max_probes"`
		MaxSteps    int    `json:"max_steps"`
		StepDelayMs int    `json:"step_delay_ms"`
		Lines       int    `json:"lines"`
	}
	if len(opts) > 0 {
		_ = json.Unmarshal(opts, &params)
	}
	pkg := strings.TrimSpace(params.Package)
	if pkg == "" {
		pkg = cfg.AppPackageName
	}

	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "decompile_audit":
		hintAppID := strings.TrimSpace(params.AppID)
		if hintAppID == "" {
			hintAppID = cfg.AppPackageName
		}
		rep, err := a.s.runTaskDecompileAudit(ctx, taskID, params.SamplePath, params.Mode, params.SkipApktool, hintAppID)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(rep, "", "  ")
		return string(b), nil
	case "static_audit":
		meta, err := a.s.runTaskStaticAudit(taskID, "")
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(map[string]any{
			"package_name":      meta.PackageName,
			"platform":          meta.Platform,
			"signature_schemes": meta.SignatureSchemes,
			"hermes_bytecode":   meta.HermesBytecode,
			"native_binaries":   meta.NativeBinaries,
			"sbom_hints":        meta.SBOMHints,
			"provisioning":      meta.Provisioning,
			"static_findings":   meta.StaticFindings,
		}, "", "  ")
		return string(b), nil
	case "sandbox_audit":
		if strings.TrimSpace(cfg.MobileDeviceID) == "" {
			return "", fmt.Errorf("任务 #%d 未绑定移动测试机", taskID)
		}
		rep, err := mob.AuditSandboxStorage(ctx, cfg.MobileDeviceID, pkg)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(rep, "", "  ")
		return string(b), nil
	case "logcat_audit":
		if strings.TrimSpace(cfg.MobileDeviceID) == "" {
			return "", fmt.Errorf("任务 #%d 未绑定移动测试机", taskID)
		}
		rep, err := mob.CaptureLogcatAudit(ctx, cfg.MobileDeviceID, pkg, params.Lines)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(rep, "", "  ")
		return string(b), nil
	case "ipc_fuzz":
		if strings.TrimSpace(cfg.MobileDeviceID) == "" {
			return "", fmt.Errorf("任务 #%d 未绑定移动测试机", taskID)
		}
		var meta mobile.PackageMetadata
		if len(cfg.AppManifestJSON) > 2 {
			_ = json.Unmarshal(cfg.AppManifestJSON, &meta)
		}
		if meta.PackageName == "" {
			meta.PackageName = pkg
		}
		rep, err := mob.FuzzIPC(ctx, cfg.MobileDeviceID, pkg, &meta, params.MaxProbes)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(rep, "", "  ")
		return string(b), nil
	case "ui_crawl":
		if strings.TrimSpace(cfg.MobileDeviceID) == "" {
			return "", fmt.Errorf("任务 #%d 未绑定移动测试机", taskID)
		}
		trCount := func() int64 {
			if tr := a.s.m.Traffic(); tr != nil {
				n, _ := tr.Count()
				return int64(n)
			}
			return 0
		}
		rep, err := mob.CrawlUI(ctx, cfg.MobileDeviceID, mobile.UICrawlRequest{
			Package:     pkg,
			MaxSteps:    params.MaxSteps,
			StepDelayMs: params.StepDelayMs,
			SafeMode:    true,
		}, trCount)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(rep, "", "  ")
		return string(b), nil
	default:
		return "", fmt.Errorf("不支持的移动审计动作 %q", kind)
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
	mux.HandleFunc("POST /api/tasks/{id}/mobile/static-audit", s.handleTaskMobileStaticAudit)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/decompile-audit", s.handleTaskMobileDecompileAudit)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/bind", s.bindTaskMobileDevice)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/unbind", s.unbindTaskMobileDevice)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/install", s.installTaskMobileApp)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/ca-push", s.pushTaskMobileCA)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/proxy", s.toggleTaskMobileProxy)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/frida/server", s.startTaskFridaServer)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/frida/start", s.startTaskFridaSession)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/frida/stop", s.stopTaskFridaSession)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/frida/rpc", s.invokeTaskMobileFridaRPC)
	mux.HandleFunc("GET /api/tasks/{id}/mobile/screen", s.getTaskMobileScreen)
	mux.HandleFunc("GET /api/tasks/{id}/mobile/screenshots/{name}", s.getTaskMobileScreenshot)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/ui", s.runTaskMobileUI)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/ui-crawl", s.runTaskMobileUICrawl)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/ipc-probe", s.runTaskMobileIPCProbe)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/ipc-fuzz", s.runTaskMobileIPCFuzz)
	mux.HandleFunc("GET /api/tasks/{id}/mobile/sandbox", s.browseTaskMobileSandbox)
	mux.HandleFunc("POST /api/tasks/{id}/mobile/storage-audit", s.runTaskMobileStorageAudit)
	mux.HandleFunc("GET /api/tasks/{id}/mobile/logcat", s.getTaskMobileLogcat)
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
	decompileAudit := s.loadPersistedDecompileAudit(taskID)
	screenshots := s.listTaskScreenshots(taskID)
	writeJSON(w, 200, map[string]any{
		"task_id":          t.ID,
		"app_mode":         cfg.AppMode,
		"app_platform":     cfg.AppPlatform,
		"app_package_name": cfg.AppPackageName,
		"app_sample_paths": cfg.AppSamplePaths,
		"manifest":         manifest,
		"decompile_audit":  decompileAudit,
		"screenshots":      screenshots,
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
		HostFilter   string `json:"host"`
		AccountLabel string `json:"account_label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	accountLabel := normalizeAccountLabel(req.AccountLabel)

	tr := s.m.Traffic()
	if tr == nil {
		writeErr(w, 400, "流量录制代理未启用")
		return
	}
	rows, _, err := tr.Page(traffic.PageQuery{
		Host:         strings.TrimSpace(req.HostFilter),
		Source:       "mobile",
		ExcludeNoise: true,
		RespMin:      -1,
		RespMax:      -1,
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
			RespMin:      -1,
			RespMax:      -1,
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
			ident := key + "@" + ex.Host
			credSummary := fmt.Sprintf("移动端人工登录提取会话头 %s (%s)", key, ex.Host)
			if accountLabel != "" {
				ident = "[" + accountLabel + "] " + ident
				credSummary = fmt.Sprintf("移动端人工登录[%s]提取会话头 %s (%s)", accountLabel, key, ex.Host)
			}
			credPayload := map[string]any{
				"kind":        "token",
				"identity":    ident,
				"secret":      val,
				"summary":     credSummary,
				"targets":     []string{ex.Host},
				"exchange_id": ex.ID,
			}
			if accountLabel != "" {
				credPayload["account_label"] = accountLabel
			}
			_, _ = t.Store.AddLinkedNode(db.KindCredential, credPayload, 0, db.StateLootActive, "human", nil, beginFact, db.RelYields)
			if len(found) >= 12 {
				break
			}
		}
		if len(found) >= 12 {
			break
		}
	}

	if len(found) > 0 {
		factSummary := fmt.Sprintf("操作者从移动端提取了 %d 条实时会话凭据，可直接用于后续越权与业务接口测试", len(found))
		if accountLabel != "" {
			factSummary = fmt.Sprintf("操作者从移动端（账号：%s）提取了 %d 条实时会话凭据，可用于多账号越权对比测试", accountLabel, len(found))
		}
		factPayload := map[string]any{
			"summary": factSummary,
			"tokens":  found,
		}
		if accountLabel != "" {
			factPayload["account_label"] = accountLabel
		}
		_, _ = t.Store.AddLinkedNode(db.KindFact, factPayload, 0, "confirmed", "human", nil, beginFact, db.RelYields)
		t.Notify()
	}
	writeJSON(w, 200, map[string]any{
		"ok":            true,
		"account_label": accountLabel,
		"extracted":     found,
		"count":         len(found),
	})
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
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	if hostPort, _, hasSlash := strings.Cut(addr, "/"); hasSlash {
		addr = hostPort
	}
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

func sanitizeScreenshotName(raw string) string {
	base := strings.TrimSpace(filepath.Base(strings.ReplaceAll(raw, "\\", "/")))
	if base == "" || base == "." || base == ".." || strings.ContainsAny(base, `/\:*?"<>|`) {
		base = "screen_" + time.Now().Format("20060102_150405")
	}
	if !strings.HasSuffix(strings.ToLower(base), ".png") {
		base += ".png"
	}
	return base
}

func normalizeAccountLabel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > 32 {
		s = string(r[:32])
	}
	return s
}

func (s *Server) resolveTaskSampleAbs(taskID int64, requestedSample string) (string, db.TaskMobileConfig, error) {
	cfg, err := s.m.pg.TaskMobileInfo(taskID)
	if err != nil {
		return "", cfg, err
	}
	sampleRel := strings.TrimSpace(requestedSample)
	if sampleRel == "" && len(cfg.AppSamplePaths) > 0 {
		sampleRel = cfg.AppSamplePaths[0]
	}
	if sampleRel == "" {
		return "", cfg, fmt.Errorf("任务 #%d 暂无可审计的安装包样本", taskID)
	}
	baseAbs, _ := filepath.Abs(s.m.dir)
	targetAbs := sampleRel
	if !filepath.IsAbs(targetAbs) {
		targetAbs = filepath.Join(s.m.dir, filepath.FromSlash(sampleRel))
	}
	targetAbs, err = filepath.Abs(targetAbs)
	if err != nil || !strings.HasPrefix(targetAbs, baseAbs+string(filepath.Separator)) {
		return "", cfg, fmt.Errorf("非法样本路径")
	}
	if _, err := os.Stat(targetAbs); err != nil {
		return "", cfg, fmt.Errorf("样本文件不存在或已清理: %v", err)
	}
	return targetAbs, cfg, nil
}

func (s *Server) runTaskStaticAudit(taskID int64, requestedSample string) (*mobile.PackageMetadata, error) {
	targetAbs, cfg, err := s.resolveTaskSampleAbs(taskID, requestedSample)
	if err != nil {
		return nil, err
	}
	meta, err := mobile.InspectPackage(targetAbs)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(s.m.dir, targetAbs)
	meta.SamplePath = filepath.ToSlash(rel)
	manRaw, _ := json.Marshal(meta)
	_ = s.m.pg.UpdateTaskAppMetadata(taskID, cfg.AppSamplePaths, manRaw, string(meta.Platform), meta.PackageName)
	if t, ok := s.m.Task(strconv.FormatInt(taskID, 10)); ok && t != nil {
		t.updateLifecycle(func(st *taskLifecycleState) {
			st.AppMode = "mobile"
			st.AppPlatform = string(meta.Platform)
			st.AppPackageName = meta.PackageName
			st.AppManifestJSON = manRaw
		})
	}
	return meta, nil
}

func (s *Server) runTaskDecompileAudit(ctx context.Context, taskID int64, requestedSample, mode string, skipApktool bool, hintAppID string) (*mobile.DecompileAuditReport, error) {
	targetAbs, cfg, err := s.resolveTaskSampleAbs(taskID, requestedSample)
	if err != nil {
		return nil, err
	}
	taskRoot := filepath.Join(s.m.dir, "tasks", strconv.FormatInt(taskID, 10))
	if err := os.MkdirAll(taskRoot, 0o755); err != nil {
		return nil, err
	}
	rep, err := mobile.DecompileAndAudit(ctx, targetAbs, taskRoot, mobile.DecompileOptions{
		Mode:        mode,
		SkipApktool: skipApktool,
		HintAppID:   fallbackStr(hintAppID, cfg.AppPackageName),
	})
	if err != nil {
		return nil, err
	}
	if rep.ManifestMeta != nil {
		rel, _ := filepath.Rel(s.m.dir, targetAbs)
		rep.ManifestMeta.SamplePath = filepath.ToSlash(rel)
		manRaw, _ := json.Marshal(rep.ManifestMeta)
		_ = s.m.pg.UpdateTaskAppMetadata(taskID, cfg.AppSamplePaths, manRaw, string(rep.ManifestMeta.Platform), rep.ManifestMeta.PackageName)
	}
	s.recordDecompileAuditFacts(taskID, rep)
	return rep, nil
}

func (s *Server) recordDecompileAuditFacts(taskID int64, rep *mobile.DecompileAuditReport) {
	if rep == nil {
		return
	}
	t, ok := s.m.Task(strconv.FormatInt(taskID, 10))
	if !ok || t == nil || t.Store == nil {
		return
	}
	beginFact, _ := t.Store.OriginFactID()
	if beginFact <= 0 {
		return
	}
	_, _ = t.Store.AddLinkedNode(db.KindFact, map[string]any{
		"summary": fmt.Sprintf("移动端反编译白盒审计完成（quality=%s，Java=%d，Smali=%d，端点=%d，硬编码提示=%d，签名算法候选=%d）",
			rep.QualityStatus, rep.JavaFileCount, rep.SmaliFileCount, len(rep.Endpoints), len(rep.Secrets), len(rep.SignCandidates)),
		"audit_dir":       rep.AuditDir,
		"quality_status":  rep.QualityStatus,
		"package_name":    rep.PackageName,
		"sample_sha256":   rep.SampleSHA256,
		"endpoints_count": len(rep.Endpoints),
		"secrets_count":   len(rep.Secrets),
		"sign_count":      len(rep.SignCandidates),
	}, 0, "confirmed", "system", nil, beginFact, db.RelYields)
	t.Notify()
}

func (s *Server) handleTaskMobileStaticAudit(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req struct {
		SamplePath string `json:"sample_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	meta, err := s.runTaskStaticAudit(taskID, req.SamplePath)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "manifest": meta})
}

func (s *Server) handleTaskMobileDecompileAudit(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req struct {
		SamplePath  string `json:"sample_path"`
		AppID       string `json:"appid"`
		Mode        string `json:"mode"`
		SkipApktool bool   `json:"skip_apktool"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	rep, err := s.runTaskDecompileAudit(r.Context(), taskID, req.SamplePath, req.Mode, req.SkipApktool, req.AppID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "audit": rep})
}

func (s *Server) loadPersistedDecompileAudit(taskID int64) *mobile.DecompileAuditReport {
	if taskID <= 0 || strings.TrimSpace(s.m.dir) == "" {
		return nil
	}
	p := filepath.Join(s.m.dir, "tasks", strconv.FormatInt(taskID, 10), "mobile_audit", "report.json")
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) == 0 {
		return nil
	}
	var rep mobile.DecompileAuditReport
	if json.Unmarshal(raw, &rep) != nil {
		return nil
	}
	return &rep
}

func (s *Server) listTaskScreenshots(taskID int64) []string {
	if taskID <= 0 || strings.TrimSpace(s.m.dir) == "" {
		return nil
	}
	shotDir := filepath.Join(s.m.dir, "tasks", strconv.FormatInt(taskID, 10), "screenshots")
	entries, err := os.ReadDir(shotDir)
	if err != nil {
		return nil
	}
	type shotItem struct {
		name  string
		mtime int64
	}
	var items []shotItem
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(strings.ToLower(ent.Name()), ".png") {
			continue
		}
		if info, ierr := ent.Info(); ierr == nil {
			items = append(items, shotItem{name: ent.Name(), mtime: info.ModTime().UnixNano()})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].mtime != items[j].mtime {
			return items[i].mtime > items[j].mtime
		}
		return items[i].name > items[j].name
	})
	if len(items) > 20 {
		items = items[:20]
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.name
	}
	return out
}

func (s *Server) getTaskMobileScreenshot(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	rawName := strings.TrimSpace(r.PathValue("name"))
	if rawName == "" || strings.Contains(rawName, "/") || strings.Contains(rawName, `\`) || strings.Contains(rawName, "..") || !strings.HasSuffix(strings.ToLower(rawName), ".png") {
		writeErr(w, 400, "非法截图文件名")
		return
	}
	shotDir := filepath.Join(s.m.dir, "tasks", strconv.FormatInt(taskID, 10), "screenshots")
	target := filepath.Join(shotDir, filepath.Base(rawName))
	raw, err := os.ReadFile(target)
	if err != nil {
		writeErr(w, 404, "截图不存在")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(raw)
}

func (s *Server) runTaskMobileIPCFuzz(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req struct {
		Package   string `json:"package"`
		MaxProbes int    `json:"max_probes"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "请先绑定移动测试机")
		return
	}
	pkg := strings.TrimSpace(req.Package)
	if pkg == "" {
		pkg = cfg.AppPackageName
	}
	var meta mobile.PackageMetadata
	if len(cfg.AppManifestJSON) > 2 {
		_ = json.Unmarshal(cfg.AppManifestJSON, &meta)
	}
	if meta.PackageName == "" {
		meta.PackageName = pkg
	}
	rep, err := s.m.Mobile().FuzzIPC(r.Context(), cfg.MobileDeviceID, pkg, &meta, req.MaxProbes)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) runTaskMobileStorageAudit(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req struct {
		Package string `json:"package"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "请先绑定移动测试机")
		return
	}
	pkg := strings.TrimSpace(req.Package)
	if pkg == "" {
		pkg = cfg.AppPackageName
	}
	rep, err := s.m.Mobile().AuditSandboxStorage(r.Context(), cfg.MobileDeviceID, pkg)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) getTaskMobileLogcat(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "请先绑定移动测试机")
		return
	}
	pkg := strings.TrimSpace(r.URL.Query().Get("pkg"))
	if pkg == "" {
		pkg = cfg.AppPackageName
	}
	lines, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("lines")))
	rep, err := s.m.Mobile().CaptureLogcatAudit(r.Context(), cfg.MobileDeviceID, pkg, lines)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) runTaskMobileUICrawl(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req mobile.UICrawlRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	if cfg.MobileDeviceID == "" {
		writeErr(w, 400, "请先绑定移动测试机")
		return
	}
	if req.Package == "" {
		req.Package = cfg.AppPackageName
	}
	req.SafeMode = true
	trCount := func() int64 {
		if tr := s.m.Traffic(); tr != nil {
			n, _ := tr.Count()
			return int64(n)
		}
		return 0
	}
	rep, err := s.m.Mobile().CrawlUI(r.Context(), cfg.MobileDeviceID, req, trCount)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) invokeTaskMobileFridaRPC(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDOr404(s, w, r)
	if !ok {
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		Method    string `json:"method"`
		Args      []any  `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	out, err := (&mobileBridgeAdapter{s: s}).RPCSign(r.Context(), taskID, req.SessionID, req.Method, req.Args)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "raw": out})
}
