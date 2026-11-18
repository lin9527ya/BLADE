// Package toolpolicy 是「会接触目标的内置工具」的唯一登记处。
//
// 新增执行类工具时，过去要同时改五处：intercept.defaultEnabledTools、
// intercept.builtinActionTools、intercept.shellCommandOf、server.footprintCommandTools
// 与 server/assembly.go 的默认关剔除列表。漏一处不会报错，只会静默失去 RoE、停机、
// 时间窗或痕迹归纳的覆盖。这里把登记集中到一张表，其余位置从表派生，并用测试兜底。
//
// 只登记动作类（Action）与需要审计的本地写（LocalWrite）工具；纯读工具不在此表。
package toolpolicy

import (
	"encoding/json"
	"strings"
)

// Class 是工具对系统的影响面。
type Class int

const (
	// Read 不改变任何状态；不在登记表里。
	Read Class = iota
	// LocalWrite 只写平台自己的库/文件，不接触目标。
	LocalWrite
	// Action 会接触目标（网络请求、远程命令、部署、配置能启动进程的工具）。
	Action
)

// Tool 是一条登记。
type Tool struct {
	Name             string
	Class            Class
	Surface          func(input []byte) string // Action 的目标提取；nil = 无网络目标
	InterceptDefault bool                      // 进入默认拦截规则集
	Footprint        bool                      // 自动归纳目标侧痕迹
	RequiresSetting  string                    // 非空时受该设置开关控制（关闭即从工具面移除）
}

func command(input []byte) string {
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Command
}

func hostCommand(input []byte) string {
	var in struct {
		Host    string `json:"host"`
		Command string `json:"command"`
	}
	_ = json.Unmarshal(input, &in)
	return strings.TrimSpace(in.Host + " " + in.Command)
}

func hostRemotePath(input []byte) string {
	var in struct {
		Host       string `json:"host"`
		RemotePath string `json:"remote_path"`
	}
	_ = json.Unmarshal(input, &in)
	return strings.TrimSpace(in.Host + " " + in.RemotePath)
}

func hostOnly(input []byte) string {
	var in struct {
		Host string `json:"host"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Host
}

func text(input []byte) string {
	var in struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Text
}

func url(input []byte) string {
	var in struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(input, &in)
	return in.URL
}

func targetArgs(input []byte) string {
	var in struct {
		Target string   `json:"target"`
		Args   []string `json:"args"`
	}
	_ = json.Unmarshal(input, &in)
	return strings.TrimSpace(in.Target + " " + strings.Join(in.Args, " "))
}

// Registry 是唯一的动作类工具清单。新增 Action 工具必须加到这里，否则
// server/toolpolicy_test.go 会失败并指出要改的位置。
var Registry = []Tool{
	// 本机/后台命令：目标在 command 文本里。
	{Name: "Bash", Class: Action, Surface: command, InterceptDefault: true, Footprint: true},
	{Name: "run_execution", Class: Action, Surface: command, InterceptDefault: true, Footprint: true},
	{Name: "shell_open", Class: Action, Surface: command, InterceptDefault: true, Footprint: true},
	{Name: "shell_send", Class: Action, Surface: text, InterceptDefault: true, Footprint: true},

	// 只读但默认进入拦截规则集（与旧列表保持一致）。
	{Name: "web_search", Class: Read, InterceptDefault: true},

	// 出站 HTTP。
	{Name: "WebFetch", Class: Action, Surface: url, InterceptDefault: true},
	{Name: "http_request", Class: Action, Surface: url, InterceptDefault: true},
	{Name: "session_login", Class: Action, Surface: url, InterceptDefault: true},

	// 结构化后渗透执行：命令由平台构造，目标在 host 字段。
	{Name: "loot_exec", Class: Action, Surface: hostCommand, InterceptDefault: true, Footprint: true, RequiresSetting: "agent_loot_exec"},
	{Name: "foothold_exec", Class: Action, Surface: hostCommand, InterceptDefault: true, Footprint: true, RequiresSetting: "agent_loot_exec"},
	{Name: "tunnel_open", Class: Action, Surface: hostOnly, InterceptDefault: true, RequiresSetting: "agent_loot_exec"},
	{Name: "loot_upload", Class: Action, Surface: hostRemotePath, InterceptDefault: true, RequiresSetting: "agent_loot_exec"},
	{Name: "loot_download", Class: Action, Surface: hostRemotePath, InterceptDefault: true, RequiresSetting: "agent_loot_exec"},
	{Name: "enum_privilege", Class: Action, Surface: hostOnly, InterceptDefault: true, Footprint: true, RequiresSetting: "agent_loot_exec"},

	// CVE 脚本与本地文件/配置写入。
	{Name: "cve_exploit", Class: Action, Surface: targetArgs, InterceptDefault: true},
	{Name: "Write", Class: Action, InterceptDefault: true},
	{Name: "Edit", Class: Action, InterceptDefault: true},
	{Name: "MultiEdit", Class: Action, InterceptDefault: true},
	{Name: "create_mcp", Class: Action, InterceptDefault: true},
	{Name: "update_mcp", Class: Action, InterceptDefault: true},
	{Name: "create_custom_tool", Class: Action, InterceptDefault: true},
	{Name: "update_custom_tool", Class: Action, InterceptDefault: true},

	// 本地写：只改平台台账/节点，不接触目标。
	{Name: "record_credential_check", Class: LocalWrite},
	{Name: "mark_loot_lost", Class: LocalWrite},
	{Name: "oast_mint", Class: LocalWrite},
	{Name: "oast_poll", Class: LocalWrite},
}

var byName = func() map[string]Tool {
	m := make(map[string]Tool, len(Registry))
	for _, t := range Registry {
		m[t.Name] = t
	}
	return m
}()

// Lookup 返回登记项。
func Lookup(name string) (Tool, bool) { t, ok := byName[name]; return t, ok }

// ActionTools 返回所有 Action 工具（停机开关/时间窗/范围门的作用集）。
func ActionTools() map[string]bool {
	out := map[string]bool{}
	for _, t := range Registry {
		if t.Class == Action {
			out[t.Name] = true
		}
	}
	return out
}

// DefaultEnabledTools 返回默认进入拦截规则集的工具。
func DefaultEnabledTools() []string {
	out := make([]string, 0, len(Registry))
	for _, t := range Registry {
		if t.InterceptDefault {
			out = append(out, t.Name)
		}
	}
	return out
}

// FootprintTools 返回需要自动归纳目标侧痕迹的工具。
func FootprintTools() map[string]bool {
	out := map[string]bool{}
	for _, t := range Registry {
		if t.Footprint {
			out[t.Name] = true
		}
	}
	return out
}

// Surface 提取一次调用的 RoE 目标面；未登记或没有网络目标时返回空串。
func Surface(name string, input []byte) string {
	t, ok := byName[name]
	if !ok || t.Surface == nil {
		return ""
	}
	return t.Surface(input)
}

// GatedBy 返回控制该工具开关的设置键（空 = 始终可用）。
func GatedBy(name string) string {
	t, ok := byName[name]
	if !ok {
		return ""
	}
	return t.RequiresSetting
}

// GatedTools 返回由某个设置控制的工具集合。
func GatedTools(setting string) []string {
	out := []string{}
	for _, t := range Registry {
		if t.RequiresSetting == setting {
			out = append(out, t.Name)
		}
	}
	return out
}
