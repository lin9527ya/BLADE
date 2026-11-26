package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
		"在绑定安卓测试机上管理被测 APP 与沙箱数据：action 支持 install / uninstall / clear_data / push_ca / list_sandbox / read_sandbox。",
		obj(map[string]any{
			"action":            str("操作类型：install | uninstall | clear_data | push_ca | list_sandbox | read_sandbox"),
			"package":           str("应用包名（可选；省略时使用任务默认 package_name）"),
			"sample_or_subpath": str("install 时为样本路径（省略用任务首样本）；sandbox 时为 /data/data/<pkg>/ 下子路径"),
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
			out, err := br.AppControl(ctx, taskID, a.Action, a.Package, a.SampleOrSubpath)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(out), nil
		})
}

func (t *ToolSet) mobileProxyCtrl() actool.CoreTool {
	return writeTool("mobile_proxy_ctrl",
		"一键管理绑定设备与 BLADE MITM 流量代理之间的链路：enable_usb（adb reverse 零暴露端口转发+全局代理）/ enable_lan（Wi-Fi 认证代理）/ disable / pinning_alerts（查看 TLS Pinning 拒绝告警）。",
		obj(map[string]any{
			"action": str("enable_usb | enable_lan | disable | pinning_alerts | clear_alerts"),
			"mode":   str("可选附加参数（usb_reverse | lan）"),
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
		"与绑定安卓设备的 UI 交互：action=dump（获取精简可交互控件树与中心坐标）/ launch / deeplink / tap / input / swipe / back / force_stop。",
		obj(map[string]any{
			"action":   str("dump | launch | deeplink | tap | input | swipe | back | home | force_stop"),
			"x":        intp("点击或滑动起点 X 坐标"),
			"y":        intp("点击或滑动起点 Y 坐标"),
			"x2":       intp("滑动终点 X 坐标"),
			"y2":       intp("滑动终点 Y 坐标"),
			"text":     str("input 操作要输入的文本"),
			"package":  str("launch / force_stop 目标包名（省略用默认包名）"),
			"activity": str("可选指定 Activity"),
			"uri":      str("deeplink URI"),
		}, "action"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			taskID, br, errRes, ok := t.requireMobileTask(ctx, "mobile_ui_explore")
			if !ok {
				return errRes, nil
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
		"管理 Frida 动态插桩评估会话：支持启动 frida-server、加载内置合规测试模块（ssl_unpinning / anti_detection / crypto_monitor / okhttp_plaintext / webview_bridge / flutter_ssl_bypass）或自定义 Hook 脚本并读取解密/签名日志。",
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
		"针对 Android Manifest 暴露组件执行 IPC 边界探测：支持 exported Activity、DeepLink URI（Scheme/Universal Link）、Broadcast Receiver 与 Content Provider 查询校验。",
		obj(map[string]any{
			"kind":      str("activity | deeplink | broadcast | provider"),
			"package":   str("目标包名（省略用任务默认包名）"),
			"component": str("组件全名（如 com.app.WebActivity）"),
			"uri":       str("deeplink URI 或 content:// URI"),
			"action":    str("broadcast action"),
			"extras":    map[string]any{"type": "object", "description": "可选 --es 字符串参数键值对"},
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
	if !hasDevice {
		b.WriteString("\n当前尚未绑定移动测试机：可先通过 Bash (jadx / apktool / apksigner) 对任务工作目录 samples/ 下安装包做静态逆向、提取后端 API 与前端逻辑，并通过 http_request/http_probe 直接评估后端接口；如需动态 Hook/抓包，可提示操作者在任务详情「移动端工作台」绑定设备。")
	} else {
		b.WriteString("\n已绑定移动测试机：建议规划串联「① 静态梳理样本加壳/导出组件/候选域名 → ② 校验设备 Root/系统 CA 与 USB 代理 → ③ Frida 加载 ssl_unpinning/crypto_monitor 启动 APP 并用 mobile_ui_explore/人机登录流触发核心 API → ④ 检查 traffic_search(mobile) 排除冗余 SDK 域并深挖后端越权/认证/签名绕过 → ⑤ 探测 Manifest 导出组件/DeepLink/Provider/私有沙箱存储」。")
	}
	return b.String()
}

func mobileWorkerContextBlock(summary string, hasDevice bool) string {
	var b strings.Builder
	b.WriteString("\n\n【当前任务移动样本与设备上下文】\n")
	b.WriteString(summary)
	if hasDevice {
		b.WriteString("\n可用移动端工具：mobile_device_status / mobile_app_ctrl / mobile_proxy_ctrl / mobile_ui_explore / mobile_frida_hook / mobile_rpc_sign / mobile_ipc_probe。")
		b.WriteString(" 遇到 TLS Pinning 拦截时，先用 mobile_proxy_ctrl(pinning_alerts) 确认域名，再用 mobile_frida_hook(ssl_unpinning) 冷启动绕过；若遇登录验证码，在界面停驻或 record_fact 提示操作者在工作台「提取实时会话」。")
	}
	return b.String()
}
