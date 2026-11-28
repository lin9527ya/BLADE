package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
)

// MobileBridge defines the host-provided mobile device & Frida runtime operations
// exposed dynamically to agents only when a task has a bound mobile device.
type MobileBridge interface {
	DeviceStatus(ctx context.Context, taskID int64) (string, error)
	AppControl(ctx context.Context, taskID int64, action, pkg, sampleOrSubpath string) (string, error)
	ProxyControl(ctx context.Context, taskID int64, action, mode string) (string, error)
	UIExplore(ctx context.Context, taskID int64, reqJSON json.RawMessage) (string, error)
	FridaHook(ctx context.Context, taskID int64, action, pkg, mode, sessionID string, modules []string, customJS string) (string, error)
	RPCSign(ctx context.Context, taskID int64, sessionID, method string, args []any) (string, error)
	IPCProbe(ctx context.Context, taskID int64, kind, pkg, component, uri, action string, extras map[string]string) (string, error)
	Audit(ctx context.Context, taskID int64, kind string, opts json.RawMessage) (string, error)
}

// Package-level overrides wired via SetOverrides / CurrentOverrides in server/assembly.go.
var (
	Mobile         MobileBridge
	TaskMobileInfo func(taskID int64) (summary string, hasApp bool, hasDevice bool)
)

func (t *ToolSet) requireMobileTask(ctx context.Context, toolName string) (int64, MobileBridge, actool.Result, bool) {
	if Mobile == nil {
		return 0, nil, actool.Errorf(fmt.Sprintf("%s: mobile bridge is not wired in this runtime", toolName)), false
	}
	taskID := RunInfoFrom(ctx).TaskID
	if taskID <= 0 {
		return 0, nil, actool.Errorf(fmt.Sprintf("%s: task context required", toolName)), false
	}
	return taskID, Mobile, actool.Result{}, true
}

func (t *ToolSet) mobileDeviceStatus() actool.CoreTool {
	return readTool("mobile_device_status",
		"检查当前任务绑定的移动测试机状态（ADB 在线态、型号、Android 版本、Root 能力、系统 CA 安装情况、frida-server 进程、前台包名与代理状态）。",
		obj(map[string]any{}),
		func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_device_status")
			if !ok {
				return errRes, nil
			}
			out, err := br.DeviceStatus(ctx, taskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

func (t *ToolSet) mobileAppCtrl() actool.CoreTool {
	return writeTool("mobile_app_ctrl",
		"在绑定安卓测试机上管理被测 APP 与执行存储/日志审计：action 支持 install / uninstall / clear_data / push_ca / list_sandbox / read_sandbox / sandbox_audit（一键查明文 Token 与未加密 SQLite）/ logcat_audit（截获敏感日志）/ static_audit（输出 SAST+ELF+签名体检报告）。",
		obj(map[string]any{
			"action":            str("操作类型：install | uninstall | clear_data | push_ca | list_sandbox | read_sandbox | sandbox_audit | logcat_audit | static_audit"),
			"package":           str("应用包名（可选；省略时使用任务默认 package_name）"),
			"sample_or_subpath": str("install 时为样本路径；sandbox 时为 /data/data/<pkg>/ 下子路径"),
		}, "action"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_app_ctrl")
			if !ok {
				return errRes, nil
			}
			var a struct {
				Action          string `json:"action"`
				Package         string `json:"package"`
				SampleOrSubpath string `json:"sample_or_subpath"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			act := strings.ToLower(strings.TrimSpace(a.Action))
			if act == "sandbox_audit" || act == "logcat_audit" || act == "static_audit" {
				out, err := br.Audit(ctx, taskID, act, in)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return actool.Text(out), nil
			}
			out, err := br.AppControl(ctx, taskID, a.Action, a.Package, a.SampleOrSubpath)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

func (t *ToolSet) mobileProxyCtrl() actool.CoreTool {
	return writeTool("mobile_proxy_ctrl",
		"一键管理绑定设备与 BLADE MITM 流量代理链路：enable_usb（adb reverse）/ enable_transparent（Root iptables 透明接管无视代理流量并阻断 QUIC UDP 443 降级回 TCP）/ enable_lan（Wi-Fi 认证代理）/ disable / pinning_alerts / clear_alerts。",
		obj(map[string]any{
			"action": str("enable_usb | enable_transparent | enable_lan | disable | pinning_alerts | clear_alerts"),
			"mode":   str("可选附加参数（usb_reverse | transparent | lan）"),
		}, "action"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_proxy_ctrl")
			if !ok {
				return errRes, nil
			}
			var a struct {
				Action string `json:"action"`
				Mode   string `json:"mode"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out, err := br.ProxyControl(ctx, taskID, a.Action, a.Mode)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

func (t *ToolSet) mobileUIExplore() actool.CoreTool {
	return writeTool("mobile_ui_explore",
		"与绑定安卓设备的 UI 交互或启动自主界面遍历爬虫：action=dump | screenshot（截图并落盘到任务 screenshots/ 目录附带控件摘要）| auto_crawl（状态机自主安全点穿页面触发 API 并统计抓包增量）| launch | deeplink | tap | input | swipe | back | home | force_stop。",
		obj(map[string]any{
			"action":    str("dump | screenshot | auto_crawl | launch | deeplink | tap | input | swipe | back | home | force_stop"),
			"save_name": str("screenshot 可选输出文件名（默认按时间戳命名并保存至 tasks/<id>/screenshots/）"),
			"x":         intp("点击或滑动起点 X 坐标"),
			"y":         intp("点击或滑动起点 Y 坐标"),
			"x2":        intp("滑动终点 X 坐标"),
			"y2":        intp("滑动终点 Y 坐标"),
			"text":      str("input 操作要输入的文本"),
			"max_steps": intp("auto_crawl 最大步数（默认 12，上限 40）"),
			"package":   str("目标包名（省略用默认包名）"),
			"activity":  str("可选指定 Activity"),
			"uri":       str("deeplink URI"),
		}, "action"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_ui_explore")
			if !ok {
				return errRes, nil
			}
			var hdr struct {
				Action string `json:"action"`
			}
			_ = json.Unmarshal(in, &hdr)
			if strings.EqualFold(strings.TrimSpace(hdr.Action), "auto_crawl") {
				out, err := br.Audit(ctx, taskID, "ui_crawl", in)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return actool.Text(out), nil
			}
			out, err := br.UIExplore(ctx, taskID, in)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

func (t *ToolSet) mobileFridaHook() actool.CoreTool {
	return writeTool("mobile_frida_hook",
		"管理 Frida 动态插桩评估会话：支持启动 frida-server、加载内置模块（ssl_unpinning / flutter_ssl_bypass / native_tls_hook / anti_detection / crypto_monitor / okhttp_plaintext / webview_bridge / storage_crypto_audit / flag_secure_bypass / socket_traffic_tap）或自定义脚本；native_tls_hook 可将无视代理的底层 HTTPS 明文直接写入流量库。",
		obj(map[string]any{
			"action":     str("start_server | start | logs | stop | list | catalog"),
			"package":    str("目标应用包名（省略用默认包名）"),
			"mode":       str("spawn（冷启动防早期 pinning）或 attach（附加运行中进程），默认 spawn"),
			"modules":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "内置模块 ID 列表"},
			"custom_js":  str("可选自定义 Frida JS 脚本"),
			"session_id": str("logs / stop 时的会话 ID（省略则用本任务最近活跃会话）"),
		}, "action"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_frida_hook")
			if !ok {
				return errRes, nil
			}
			var a struct {
				Action    string   `json:"action"`
				Package   string   `json:"package"`
				Mode      string   `json:"mode"`
				Modules   []string `json:"modules"`
				CustomJS  string   `json:"custom_js"`
				SessionID string   `json:"session_id"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out, err := br.FridaHook(ctx, taskID, a.Action, a.Package, a.Mode, a.SessionID, a.Modules, a.CustomJS)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

func (t *ToolSet) mobileRPCSign() actool.CoreTool {
	return writeTool("mobile_rpc_sign",
		"调用活跃 Frida 会话通过 rpc.exports 暴露的方法，按需生成移动端的签名/加解密参数供重放与接口安全测试使用。",
		obj(map[string]any{
			"method":     str("Frida 脚本 rpc.exports 导出的函数名"),
			"args":       map[string]any{"type": "array", "description": "传递给导出函数的 JSON 参数列表"},
			"session_id": str("可选指定 Frida 会话 ID（省略使用当前任务活跃会话）"),
		}, "method"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_rpc_sign")
			if !ok {
				return errRes, nil
			}
			var a struct {
				SessionID string `json:"session_id"`
				Method    string `json:"method"`
				Args      []any  `json:"args"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out, err := br.RPCSign(ctx, taskID, a.SessionID, a.Method, a.Args)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

func (t *ToolSet) mobileIPCProbe() actool.CoreTool {
	return writeTool("mobile_ipc_probe",
		"针对 Android Manifest 暴露组件执行单点或批量边界探测：支持 activity / service / deeplink / broadcast / provider / auto_fuzz（自动按样本导出组件与 Scheme 批量测试边界防护）。",
		obj(map[string]any{
			"kind":       str("activity | service | deeplink | broadcast | provider | auto_fuzz"),
			"package":    str("目标包名（省略用任务默认包名）"),
			"component":  str("组件全名（如 com.app.WebActivity）"),
			"uri":        str("deeplink URI 或 content:// URI"),
			"action":     str("broadcast action"),
			"max_probes": intp("auto_fuzz 最大探测数（默认 25，上限 60）"),
			"extras":     map[string]any{"type": "object", "description": "可选 --es 字符串参数键值对"},
		}, "kind"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_ipc_probe")
			if !ok {
				return errRes, nil
			}
			var a struct {
				Kind      string            `json:"kind"`
				Package   string            `json:"package"`
				Component string            `json:"component"`
				URI       string            `json:"uri"`
				Action    string            `json:"action"`
				Extras    map[string]string `json:"extras"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.EqualFold(strings.TrimSpace(a.Kind), "auto_fuzz") {
				out, err := br.Audit(ctx, taskID, "ipc_fuzz", in)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return actool.Text(out), nil
			}
			out, err := br.IPCProbe(ctx, taskID, a.Kind, a.Package, a.Component, a.URI, a.Action, a.Extras)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

func mobilePlannerContextBlock(summary string, hasDevice bool) string {
	var b strings.Builder
	b.WriteString("\n\n【移动 APP 评估上下文（动态注入）】\n")
	b.WriteString(summary)
	b.WriteString("\n规范指引：优先按需加载 `apk-reverse` 技能，并先用 `mobile_decompile_audit` 安全解包并完成源码白盒扫描；提交结论或收尾前调用 `mobile_evidence_check` 自检证据闭环。")
	if !hasDevice {
		b.WriteString("\n当前尚未绑定移动测试机：可依据 `mobile_decompile_audit`、静态 SAST/ELF/签名画像或 Bash (jadx / apktool / apksigner / radare2) 对工作目录 samples/ 下样本深化逆向并验证后端接口（XAPK/APKM/IPA 亦自动支持解包审计）；如需动态测试，可提示操作者在任务详情「移动端工作台」绑定设备。")
	} else {
		b.WriteString("\n已绑定移动测试机：建议规划全覆盖闭环「① 先跑 mobile_decompile_audit 与 static_audit/ELF/签名画像 → ② 配置 Root 系统 CA 与抓包（遇无视系统代理流量/Flutter/Cronet 用 enable_transparent 或 native_tls_hook/flutter_ssl_bypass）→ ③ 用 mobile_ui_explore(auto_crawl / screenshot) 或人机登录后 extract-session（支持 account_label 区分账号 A/B）打通接口覆盖 → ④ 若 API 带签名，在 Frida 导出签名函数并直接用 http_request(mobile_sign_method=...) 批量跑越权/注入 → ⑤ 执行 mobile_ipc_probe(auto_fuzz) 与 mobile_app_ctrl(sandbox_audit/logcat_audit) → ⑥ 用 mobile_evidence_check 校验证据完整性后落库」。")
	}
	return b.String()
}

func mobileWorkerContextBlock(summary string, hasDevice bool) string {
	var b strings.Builder
	b.WriteString("\n\n【当前任务移动样本与设备上下文】\n")
	b.WriteString(summary)
	b.WriteString("\n可用静态与质量自检工具：mobile_decompile_audit（安全解压/质量门控反编译/源码白盒扫描）、mobile_evidence_check（报告与 SRC 闭环自检）；深入逆向可读取 Skill `apk-reverse`。")
	if hasDevice {
		b.WriteString("\n已绑定真机/模拟器动态工具：mobile_device_status / mobile_app_ctrl(含 sandbox_audit/logcat_audit/static_audit) / mobile_proxy_ctrl(含 enable_transparent) / mobile_ui_explore(含 screenshot/auto_crawl) / mobile_frida_hook(含 flutter_ssl_bypass/native_tls_hook/storage_crypto_audit/flag_secure_bypass/socket_traffic_tap 与 iOS 模板) / mobile_rpc_sign / mobile_ipc_probe(含 service/auto_fuzz)。")
		b.WriteString(" 此外 http_request 支持 mobile_sign_method 自动调用活跃 Frida RPC 为接口测试重签参数；无视系统代理的流量可通过 enable_transparent 或 native_tls_hook 直接接入流量库。")
	}
	return b.String()
}

func applyMobileRPCSignResult(rawRes string, targetURL **url.URL, headersText, bodyText *string) error {
	trimmed := strings.TrimSpace(rawRes)
	if trimmed == "" {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) == nil && obj != nil {
		// Unwrap server bridge envelope {"method":"...", "result": ...} when present.
		if _, hasMethod := obj["method"]; hasMethod {
			if inner, hasRes := obj["result"]; hasRes {
				switch v := inner.(type) {
				case map[string]any:
					obj = v
				case string:
					return applyMobileRPCSignResult(v, targetURL, headersText, bodyText)
				default:
					rawInner, _ := json.Marshal(v)
					return applyMobileRPCSignResult(string(rawInner), targetURL, headersText, bodyText)
				}
			}
		}
		if hdrs, ok := obj["headers"].(map[string]any); ok {
			for k, v := range hdrs {
				k = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(k, "\r", ""), "\n", ""))
				val := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(fmt.Sprint(v), "\r", ""), "\n", ""))
				if k != "" {
					if *headersText != "" {
						*headersText += "\n"
					}
					*headersText += k + ": " + val
				}
			}
		}
		if newURL, ok := obj["url"].(string); ok && strings.TrimSpace(newURL) != "" {
			nu, err := url.Parse(strings.TrimSpace(newURL))
			if err != nil {
				return err
			}
			// Security guard: allow query/path signing mutations on same host, forbid unexpected off-host redirect.
			if *targetURL != nil && !strings.EqualFold(nu.Hostname(), (*targetURL).Hostname()) {
				return fmt.Errorf("Frida RPC 不允许将请求重定向到不同主机 (%s -> %s)", (*targetURL).Hostname(), nu.Hostname())
			}
			*targetURL = nu
		}
		if newBody, ok := obj["body"].(string); ok {
			*bodyText = newBody
		}
		return nil
	}
	var scalar string
	if json.Unmarshal([]byte(trimmed), &scalar) == nil && scalar != "" {
		trimmed = scalar
	}
	trimmed = strings.ReplaceAll(strings.ReplaceAll(trimmed, "\r", ""), "\n", "")
	if *headersText != "" {
		*headersText += "\n"
	}
	*headersText += "X-Sign: " + trimmed
	return nil
}
