package agent

import (
	"context"

	actool "github.com/lin9527ya/Norma/tool"
)

// Overrides 是 agent 依赖宿主的**外部能力清单**：prompt 模板、产物语言、工具解析与
// 增强、流量工具绑定开关。它们全部由 server 在启动时注入、此后只读。
//
// 收成一份结构体不是审美问题：这 10 个函数变量此前散在 7 个文件里，没人能一眼答出
// 「agent 包到底向宿主依赖了什么」。现在这是一份可读的清单，注入也收敛到 SetOverrides
// 一处。包内各处的读取仍直接读这些变量（不为此改动任何函数签名）。
type Overrides struct {
	// PromptOverride 返回某个 agent 的 system prompt 模板（DB 覆盖优先于内置默认）。
	PromptOverride func(agentKey string) (string, bool)
	// PromptAppend 返回渲染在正文之后、代码尾段之前的追加段（可为空）。
	PromptAppend func(agentKey string) (string, bool)
	// CompactionPromptOverride 返回领域化压缩模板（P14）。
	CompactionPromptOverride func() (string, bool)
	// ArtifactLanguage 返回出站产物语言（P41）。agent 每 run 现读，改完即时生效。
	ArtifactLanguage func() string

	// WrapupOverride / WrapupMaxTurnsOverride：per-run 收尾词与轮数的 DB 覆盖。
	WrapupOverride         func(agentKey string) (string, bool)
	WrapupMaxTurnsOverride func(agentKey string) (int, bool)
	// WrapupTaskTimeoutOverride / …TurnsOverride：**任务到点**收尾词与轮数。
	// 语义与 per-run 常相反，只给 worker/planner 配置。
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)

	// ToolResolve / ToolAugment 在工具装配时按 agent 过滤与追加工具。
	ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool
	ToolAugment func(ctx context.Context, agentKey string) (extra []actool.CoreTool, def DeferredInfo, cleanup func())
	// FindingTrafficBindingEnabled 是流量宿主工具能否绑定到 agent 的全局开关。
	FindingTrafficBindingEnabled func() bool
	// LootExecEnabled 是 loot_exec 的总开关（默认关）：它把库里的凭据直接变成对目标的
	// 动作，是 agent 能力里风险最高的一项。
	LootExecEnabled func() bool
	// OASTConfig 返回 OAST 回调服务的配置（token、域名、是否启用）。nil/未启用时
	// oast_mint / oast_poll 从工具面移除。
	OASTConfig func() (token, domain string, enabled bool)
}

// SetOverrides 用 o **整体替换**当前这套 hook；nil 字段表示把那一项关掉。
//
// 启动时这项能力分两处接上（prompt 类在 server.New，工具类在 assembly 的 wire 函数），
// 所以后接的一方请用 CurrentOverrides 读出、改自己那几个字段、再 SetOverrides 写回，
// 否则会把先接上的清掉。
func SetOverrides(o Overrides) {
	PromptOverride = o.PromptOverride
	PromptAppend = o.PromptAppend
	CompactionPromptOverride = o.CompactionPromptOverride
	ArtifactLanguage = o.ArtifactLanguage
	WrapupOverride = o.WrapupOverride
	WrapupMaxTurnsOverride = o.WrapupMaxTurnsOverride
	WrapupTaskTimeoutOverride = o.WrapupTaskTimeoutOverride
	WrapupTaskTimeoutTurnsOverride = o.WrapupTaskTimeoutTurnsOverride
	ToolResolve = o.ToolResolve
	ToolAugment = o.ToolAugment
	FindingTrafficBindingEnabled = o.FindingTrafficBindingEnabled
	LootExecEnabled = o.LootExecEnabled
	OASTConfig = o.OASTConfig
}

// CurrentOverrides 返回当前生效的一整套 hook（读取 / 保存 / 恢复用）。
func CurrentOverrides() Overrides {
	return Overrides{
		PromptOverride:                 PromptOverride,
		PromptAppend:                   PromptAppend,
		CompactionPromptOverride:       CompactionPromptOverride,
		ArtifactLanguage:               ArtifactLanguage,
		WrapupOverride:                 WrapupOverride,
		WrapupMaxTurnsOverride:         WrapupMaxTurnsOverride,
		WrapupTaskTimeoutOverride:      WrapupTaskTimeoutOverride,
		WrapupTaskTimeoutTurnsOverride: WrapupTaskTimeoutTurnsOverride,
		ToolResolve:                    ToolResolve,
		ToolAugment:                    ToolAugment,
		FindingTrafficBindingEnabled:   FindingTrafficBindingEnabled,
		LootExecEnabled:                LootExecEnabled,
		OASTConfig:                     OASTConfig,
	}
}

// WithOverrides 在 fn 执行期间临时换一套 hook，结束后恢复原样（panic 也会恢复）。
//
// 测试用它替代「直接改包级变量 + t.Cleanup 恢复」——后者散落在十几个测试里、容易漏写
// 恢复；更重要的是它把「这段时间用的是哪套 hook」写成一处显式的作用域。
//
// 注意：这仍然是包级状态，**不能**让两个并行测试同时改 hook。真要并行，需要把
// Overrides 顺着调用链传下去，那是改签名的事，不在这一步的范围里。
func WithOverrides(o Overrides, fn func()) {
	saved := CurrentOverrides()
	SetOverrides(o)
	defer SetOverrides(saved)
	fn()
}
