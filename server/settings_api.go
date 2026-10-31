// 运行时设置的读取与写入（含网页搜索连通性测试）。
// 从 server.go 拆出：纯搬运，零行为变化。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/intercept"
)

// proxyEgressIPs 经每个候选代理查询出口 IP（按需触发，不参与周期探测）。它**只走
// 候选代理**，不会为了对比而直连——直连查询本身就会把本机 IP 暴露给查询服务。
// leaked 只由"出口 IP == 本机网卡地址"判定（用于发现 Clash 规则把流量直连的情况）。
func (s *Server) proxyEgressIPs(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Proxies []string `json:"proxies"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // 空 body = 用已保存的候选
	cfg, err := NormalizeProxyPoolConfig(ProxyPoolConfig{Candidates: req.Proxies})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	proxies := cfg.Candidates
	if len(proxies) == 0 {
		proxies = s.m.ProxyPoolConfig().Candidates
	}
	if len(proxies) == 0 {
		writeErr(w, 400, "没有可检测的代理：先填写代理池")
		return
	}
	if len(proxies) > maxEgressIPChecks {
		proxies = proxies[:maxEgressIPChecks]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	results := checkProxyEgressIPs(ctx, proxies, s.m.EgressIPURL(), 5*time.Second)
	writeJSON(w, 200, map[string]any{
		"results":       results,
		"egress_ip_url": s.m.EgressIPURL(),
		"note":          "仅经候选代理查询出口 IP，不会发起直连检测；出口 IP 与本机网卡地址相同时会标记 leaked",
	})
}

// getSettings returns the runtime app settings the UI toggles. The Brave API key
// is returned as a boolean presence flag (brave_key_set), never the value itself,
// so the UI can show "configured" without echoing the secret back.
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.settingsPayload())
}

// scopeHoursSetting 读原始作业时间窗设置串（空 = 不限时）。原样回显用户写的那串，不做归一化
// ——页面要显示的就是他填的东西，改了才改得回去。
// corsAllowedOriginsSetting 读 CORS 白名单原文（空 = 不发 CORS 头）。
func corsAllowedOriginsSetting(pg *db.DB) string {
	v, ok, err := pg.GetSetting(settingCORSAllowedOrigins)
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// compactionPromptSetting 读领域压缩模板的 DB 覆盖（空 = 用内置 7 段模板）。只读回显。
func compactionPromptSetting(pg *db.DB) string {
	v, ok, err := pg.GetSetting(settingCompactionPrompt)
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// siteNameSetting 读界面显示名（空 = 内置 BLADE）。
func siteNameSetting(pg *db.DB) string {
	v, ok, err := pg.GetSetting(settingSiteName)
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// artifactLanguageSetting 读面向目标的产物语言（缺省 zh = 不约束）。
func artifactLanguageSetting(pg *db.DB) string {
	v, ok, err := pg.GetSetting(settingArtifactLanguage)
	if err != nil || !ok || strings.TrimSpace(v) == "" {
		return agent.ArtifactLangZh
	}
	return strings.TrimSpace(v)
}

func scopeHoursSetting(pg *db.DB) string {
	v, _, _ := pg.GetSetting(intercept.SettingScopeHours)
	return strings.TrimSpace(v)
}

func (s *Server) settingsPayload() map[string]any {
	on, backend, braveKey, tavilyKey, proxy := s.m.WebSearch()
	pyStored, _, _ := s.m.pg.GetSetting(settingPythonInterp)
	intelKey, _, _ := s.m.pg.GetSetting(settingIntelKey)
	oastToken, _, _ := s.m.pg.GetSetting(settingOASTToken)
	oastDomain, _, _ := s.m.pg.GetSetting(settingOASTDomain)
	updateToken, _, _ := s.m.pg.GetSetting(settingUpdateToken)
	intelEmail, _, _ := s.m.pg.GetSetting(settingIntelEmail)
	intelBaseURL, _, _ := s.m.pg.GetSetting(settingIntelBaseURL)
	intelProxy, _, _ := s.m.pg.GetSetting(settingIntelProxy)
	intelUsed, _ := s.intelUsageToday(context.Background())
	concOn, concLimit := s.m.ConcurrencyLimit()
	if concLimit == 0 {
		concLimit = defaultConcurrencyLimit // 关闭时也回显一个合理默认值给 UI
	}
	proxyCfg := s.m.ProxyPoolConfig()
	proxyStatus := s.m.ProxyStatus()
	clashCfg := s.m.ClashEgressConfig()
	return map[string]any{
		"traffic_capture":       s.m.TrafficEnabled(),
		"agent_traffic_binding": s.m.pg.GetBool(settingAgentTrafficBinding, false),
		"agent_loot_exec":       s.m.pg.GetBool(settingLootExec, false),
		"oast_enabled":          s.m.pg.GetBool(settingOASTEnabled, false),
		"oast_token_set":        strings.TrimSpace(oastToken) != "", // 只回"是否已配"，绝不回显 token
		"oast_domain":           oastDomain,
		"opsec_noise_level":     noiseLevel(s.m.pg),
		"agent_auto_retest":     s.m.pg.GetBool(settingAutoRetest, false),
		"task_token_budget":     s.taskTokenBudget(),
		"llm_record":            s.m.LLMRecordEnabled(),
		"web_search_enabled":    on,
		"web_search_backend":    backend,
		"brave_key_set":         strings.TrimSpace(braveKey) != "",
		"tavily_key_set":        strings.TrimSpace(tavilyKey) != "",
		"web_search_proxy":      proxy,             // 独立出口代理(http/https/socks5)，空=直连
		"global_proxy":          s.m.GlobalProxy(), // 兼容旧字段：代理池第一项（空=未配置）
		// 代理池：每行一个候选；健康检查与切换运行时生效。proxy_blocked=true 表示
		// 全部候选都不可用、出口已阻断（**不会直连**）。
		"global_proxy_pool":             strings.Join(proxyCfg.Candidates, "\n"),
		"proxy_health_url":              proxyCfg.HealthURL,
		"proxy_health_timeout_seconds":  int(proxyCfg.Timeout.Seconds()),
		"proxy_health_interval_seconds": int(proxyCfg.Interval.Seconds()),
		"proxy_fail_threshold":          proxyCfg.FailThreshold,
		"proxy_switch_cooldown_seconds": int(proxyCfg.Cooldown.Seconds()),
		"proxy_active":                  proxyStatus.Active,
		"proxy_blocked":                 proxyStatus.Blocked,
		"proxy_status":                  proxyStatus.Candidates,
		"proxy_egress_ip_url":           s.m.EgressIPURL(),
		// Clash 出口切换：secret 只回 *_set 布尔，绝不回显。
		"clash_api_url":                 clashCfg.APIURL,
		"clash_api_secret_set":          clashCfg.Secret != "",
		"clash_proxy_group":             clashCfg.Group,
		"clash_auto_rotate":             clashCfg.Auto,
		"clash_rotate_cooldown_seconds": int(clashCfg.Cooldown.Seconds()),
		"clash_status":                  s.m.ClashEgressStatus(),
		"python_interpreter":            strings.TrimSpace(pyStored), // 用户/自动设的值(空=用运行时检测)
		"log_level":                     LogLevelName(),
		"workers":                       s.m.Workers(), // 并发工作 agent 数(默认3)；对之后启动的任务生效
		"task_concurrency_enabled":      concOn,        // 任务并发上限开关(默认关)
		"task_concurrency_limit":        concLimit,     // 同时运行任务上限(开启后默认5)
		// LLM 轮询(故障转移)。默认关；开启后走全局激活配置的 agent 在当前配置不可用时
		// 自动切到下一个配置。bind_fallback 仅在轮询开启时有意义(默认关)。
		"llm_pool_enabled":       s.m.LLMPoolEnabled(),
		"llm_pool_bind_fallback": s.m.LLMPoolBindFallback(),
		// 操作约束注入范围(默认都开):把本任务的 allow/deny 约束拼进对应 agent 的系统提示。
		"constraints_inject_planner": s.constraintInjectPlanner(),
		"constraints_inject_worker":  s.constraintInjectWorker(),
		// 实验功能:noa 模型驱动上下文压缩(默认关)。开启后平台接入的四类 agent 由 noa
		// 接管上下文压缩,取代内置 compaction;每 run 读一次,对之后启动的 run 生效。
		"noa_compaction": s.m.NoaCompactionEnabled(),
		// 授权范围（RoE）：空间门模式 + 作业时间窗（空 = 不限时）。
		// 模式回的是**生效值**（env 覆盖优先于 setting），并单独给一个 env 标志——页面据此提示
		// 「环境变量把它压住了」，否则用户改了没反应会莫名其妙。
		"intercept_scope_mode":     s.m.interceptor.ScopeMode(),
		"intercept_scope_mode_env": strings.TrimSpace(os.Getenv("BLADE_SCOPE_ENFORCEMENT")) != "",
		"intercept_scope_hours":    scopeHoursSetting(s.m.pg),
		// P41 面向目标的产物语言：回**生效值**（缺省即 zh），页面据此显示当前档位。
		"artifact_language": artifactLanguageSetting(s.m.pg),
		// P43 凭据尝试预算：回**生效值**（缺省 3 / ask），页面据此显示当前档位。
		"credential_attempt_budget":        s.m.interceptor.CredentialBudget(),
		"credential_attempt_budget_action": s.m.interceptor.CredentialBudgetAction(),
		"site_name":                        siteNameSetting(s.m.pg),
		"cors_allowed_origins":             corsAllowedOriginsSetting(s.m.pg),
		// P14 领域压缩模板：只读回显 DB 覆盖（空 = 内置模板）。设置页不暴露编辑框，
		// 写入走 API/DB；在 settings_contract_test.go 的 settingsReadOnly 里登记了理由。
		"compaction_prompt": compactionPromptSetting(s.m.pg),
		// 漏洞 IM 推送的全局项。渠道本身是独立资源，走 /api/notify/* 管理；
		// 这里只放「作用于全部渠道」的三项。
		// P21 情报源：key 与 web search 同款——只回"是否已配"，绝不回显明文。
		"fofa_key_set":      strings.TrimSpace(intelKey) != "",
		"update_token_set":  strings.TrimSpace(updateToken) != "", // 自更新 token：只回布尔
		"fofa_email":        intelEmail,
		"fofa_base_url":     intelBaseURL,
		"fofa_proxy":        intelProxy,
		"intel_daily_quota": s.intelDailyQuota(),
		"intel_used_today":  intelUsed,
		// P28 存储清理：每个分类的保留天数（0 = 不清理；默认全 0，装上什么都不动）。
		"storage_retention_tasks_days":       s.storageRetentionDays(settingStorageRetentionTasks),
		"storage_retention_sessions_days":    s.storageRetentionDays(settingStorageRetentionSessions),
		"storage_retention_drafts_days":      s.storageRetentionDays(settingStorageRetentionDrafts),
		"storage_retention_transcripts_days": s.storageRetentionDays(settingStorageRetentionTranscript),
		"storage_retention_binary_days":      s.storageRetentionDays(settingStorageRetentionBinary),
		// 数据库侧同一套保留契约。前三个是 always-on 写的表（无限增长主力）。
		"storage_retention_llm_usage_days":           s.storageRetentionDays(settingRetentionLLMUsage),
		"storage_retention_activity_days":            s.storageRetentionDays(settingRetentionActivity),
		"storage_retention_server_logs_days":         s.storageRetentionDays(settingRetentionServerLogs),
		"storage_retention_llm_record_bodies_days":   s.storageRetentionDays(settingRetentionLLMRecordBodys),
		"storage_retention_llm_records_days":         s.storageRetentionDays(settingRetentionLLMRecords),
		"storage_retention_tool_executions_days":     s.storageRetentionDays(settingRetentionToolExecutions),
		"storage_retention_credential_attempts_days": s.storageRetentionDays(settingRetentionCredentialAttempts),
		"storage_retention_traffic_days":             s.storageRetentionDays(settingRetentionTraffic),
		// P24 长任务执行的资源限额（0 = 该轴不限）与外部 MCP 的并发/熔断。
		"exec_cpu_seconds":              execLimitsFromSettings(s.m.pg).CPUSeconds,
		"exec_memory_mb":                execLimitsFromSettings(s.m.pg).MemoryMB,
		"exec_max_procs":                execLimitsFromSettings(s.m.pg).MaxProcs,
		"exec_max_active":               execMaxActiveSetting(s.m.pg),
		"exec_max_age_minutes":          execMaxAgeMinutes(s.m.pg),
		"mcp_max_concurrent_per_server": s.mcpGuardSetting(settingMCPMaxConcurrent, defaultMCPMaxConcurrent),
		"mcp_breaker_threshold":         s.mcpGuardSetting(settingMCPBreakerFails, defaultMCPBreakerFails),
		"mcp_breaker_cooldown_sec":      s.mcpGuardSetting(settingMCPBreakerCooldown, defaultMCPBreakerCooldown),
		"notify_enabled":                s.m.pg.GetBool(settingNotifyEnabled, true),
		"notify_public_base_url":        notifyPublicBaseURL(s.m.pg),
		"notify_digest_interval_min":    notifyDigestIntervalMin(s.m.pg),
	}
}

// notifyPublicBaseURL 读推送回链用的外部地址。
func notifyPublicBaseURL(pg *db.DB) string {
	v, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	return v
}

// notifyDigestIntervalMin 读汇总周期（分钟），非法或未配置时回落到默认值。
// 回显默认值而不是空串，UI 才能把当前生效值填进输入框。
func notifyDigestIntervalMin(pg *db.DB) int {
	v, ok, _ := pg.GetSetting(settingNotifyDigestMinutes)
	if !ok {
		return notifyDefaultDigestMinutes
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return notifyDefaultDigestMinutes
	}
	return n
}

// pgDetectPython re-runs interpreter detection, stores + returns it.
func (s *Server) pgDetectPython(w http.ResponseWriter, r *http.Request) {
	p := detectPython()
	if p == "" {
		writeErr(w, 404, "未检测到 python(python3/python 均不在 PATH)")
		return
	}
	if err := s.m.pg.SetSetting(settingPythonInterp, p); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"python_interpreter": p})
}

// putSettings applies a settings change. Toggling traffic_capture rebuilds the
// agents (applyLLM) so the new proxy/traffic-tools/prompt state takes hold — when
// off, agents get no proxy config, no traffic tools, and no proxy prompt content.

// settingsRequest 是设置保存请求体。提取为具名类型，让「先全量校验、再写库/应用」
// 两阶段处理能共用同一份字段定义（见 settings_validate.go）。
type settingsRequest struct {
	TrafficCapture      *bool   `json:"traffic_capture"`
	AgentTrafficBinding *bool   `json:"agent_traffic_binding"`
	LootExec            *bool   `json:"agent_loot_exec"`
	OastEnabled         *bool   `json:"oast_enabled"`
	OastToken           *string `json:"oast_token"` // 只写：空串 = 清除
	OastDomain          *string `json:"oast_domain"`
	OpsecNoise          *string `json:"opsec_noise_level"`
	AgentAutoRetest     *bool   `json:"agent_auto_retest"`
	TaskTokenBudget     *int64  `json:"task_token_budget"`
	LLMRecord           *bool   `json:"llm_record"` // LLM 录制开关（默认关）；即时生效，无需重建 agent
	// Web search. WebSearchEnabled/Backend toggle the tool + backend; BraveKey/TavilyKey
	// are optional — omit (null) to leave a stored key untouched, send "" to clear.
	WebSearchEnabled    *bool   `json:"web_search_enabled"`
	WebSearchBackend    *string `json:"web_search_backend"`
	BraveKey            *string `json:"brave_search_api_key"`
	TavilyKey           *string `json:"tavily_search_api_key"`
	WebSearchProxy      *string `json:"web_search_proxy"`  // 独立出口代理(http/https/socks5)；null=不改，""=清空
	GlobalProxy         *string `json:"global_proxy"`      // 兼容旧字段：单代理；null=不改，""=清空(直连)
	GlobalProxyPool     *string `json:"global_proxy_pool"` // 代理池（每行一个）；null=不改，""=清空
	ProxyHealthURL      *string `json:"proxy_health_url"`
	ProxyHealthTimeout  *int    `json:"proxy_health_timeout_seconds"`
	ProxyHealthInterval *int    `json:"proxy_health_interval_seconds"`
	ProxyFailThreshold  *int    `json:"proxy_fail_threshold"`
	ProxySwitchCooldown *int    `json:"proxy_switch_cooldown_seconds"`
	ProxyEgressIPURL    *string `json:"proxy_egress_ip_url"`
	// Clash 出口切换：URL/组/自动/冷却；secret 只写不回显（null=不改，""=清空）。
	ClashAPIURL         *string `json:"clash_api_url"`
	ClashAPISecret      *string `json:"clash_api_secret"`
	ClashProxyGroup     *string `json:"clash_proxy_group"`
	ClashAutoRotate     *bool   `json:"clash_auto_rotate"`
	ClashRotateCooldown *int    `json:"clash_rotate_cooldown_seconds"`
	PythonInterp        *string `json:"python_interpreter"` // 自定义脚本工具的 python 解释器路径
	LogLevel            *string `json:"log_level"`          // debug|info|warn|error；热切换
	Workers             *int    `json:"workers"`            // 并发工作 agent 数(>0)；对之后启动的任务生效
	// 任务并发上限:同时「运行中」的任务数上限。关闭=不限;开启后新建任务超限则排队,有空位自动启动。
	ConcurrencyEnabled *bool `json:"task_concurrency_enabled"`
	ConcurrencyLimit   *int  `json:"task_concurrency_limit"`
	// LLM 轮询(故障转移)开关 + 「绑定配置失败也兜底回轮询链」开关。两者都需要
	// 重建 provider 链才生效，走下面的 changed → applyLLM 路径。
	LLMPoolEnabled      *bool `json:"llm_pool_enabled"`
	LLMPoolBindFallback *bool `json:"llm_pool_bind_fallback"`
	// 操作约束注入范围开关(默认都开);即时生效(planner/worker 每轮读),无需重建 agent。
	ConstraintsInjectPlanner *bool `json:"constraints_inject_planner"`
	ConstraintsInjectWorker  *bool `json:"constraints_inject_worker"`
	// 授权范围（RoE）两个设置：空间门模式 + 作业时间窗。空串 hours = 不限时。
	// 拦截层每调用现读，即时生效。
	ScopeMode  *string `json:"intercept_scope_mode"`
	ScopeHours *string `json:"intercept_scope_hours"`
	// P41 面向目标的产物语言（zh 默认不约束 / en 一律 ASCII 英文）。"" = 回到默认。
	// agent 侧每 run 现读，改完即时生效。与界面语言无关。
	ArtifactLanguage *string `json:"artifact_language"`
	// P43 凭据尝试预算：窗口内对同一 (目标, 账号) 允许的尝试次数（0 = 不限），
	// 以及超预算后的裁决（allow|ask|deny）。拦截层每调用现读，即时生效。
	CredentialBudget       *int    `json:"credential_attempt_budget"`
	CredentialBudgetAction *string `json:"credential_attempt_budget_action"`
	// P42 界面显示名（空 = 内置 BLADE）。只影响运行时标题；静态 HTML 的 title 是构建期常量。
	SiteName *string `json:"site_name"`
	// P42 允许跨源访问 API 的来源白名单（逗号分隔；空 = 不发 CORS 头）。
	CORSAllowedOrigins *string `json:"cors_allowed_origins"`
	// 实验功能:noa 上下文压缩开关(默认关);每 run 读,对之后启动的 run 生效,无需重建 agent。
	NoaCompaction *bool `json:"noa_compaction"`
	// 漏洞 IM 推送的全局项。三者都由投递引擎每轮读一次，改完即时生效，
	// 不需要重建 agent 或重启。
	NotifyEnabled    *bool   `json:"notify_enabled"`
	NotifyBaseURL    *string `json:"notify_public_base_url"`
	NotifyDigestMins *int    `json:"notify_digest_interval_min"`
	// P21 情报源。key：null=不改、""=清空；base_url/proxy 同理；quota：0=不限。
	FOFAKey         *string `json:"fofa_key"`
	UpdateToken     *string `json:"update_github_token"` // 自更新 token（只存不回显）
	FOFAEmail       *string `json:"fofa_email"`
	FOFABaseURL     *string `json:"fofa_base_url"`
	FOFAProxy       *string `json:"fofa_proxy"`
	IntelDailyQuota *int    `json:"intel_daily_quota"`
	// P24：执行资源限额（0 = 该轴不限）与外部 MCP 守卫（并发上限/熔断阈值/冷却秒）。
	ExecCPUSeconds *int `json:"exec_cpu_seconds"`
	// P28 存储清理保留天数（0 = 不清理）。
	StorageRetentionTasks      *int `json:"storage_retention_tasks_days"`
	StorageRetentionSessions   *int `json:"storage_retention_sessions_days"`
	StorageRetentionDrafts     *int `json:"storage_retention_drafts_days"`
	StorageRetentionTranscript *int `json:"storage_retention_transcripts_days"`
	StorageRetentionBinary     *int `json:"storage_retention_binary_days"`
	// 数据库侧保留天数（同一套 0 = 不清理）。改完由后台清理器按日生效。
	RetentionLLMUsage       *int `json:"storage_retention_llm_usage_days"`
	RetentionActivity       *int `json:"storage_retention_activity_days"`
	RetentionServerLogs     *int `json:"storage_retention_server_logs_days"`
	RetentionLLMRecordBodys *int `json:"storage_retention_llm_record_bodies_days"`
	RetentionLLMRecords     *int `json:"storage_retention_llm_records_days"`
	RetentionTraffic        *int `json:"storage_retention_traffic_days"`
	RetentionToolExecs      *int `json:"storage_retention_tool_executions_days"`
	RetentionCredAttempts   *int `json:"storage_retention_credential_attempts_days"`
	ExecMemoryMB            *int `json:"exec_memory_mb"`
	ExecMaxProcs            *int `json:"exec_max_procs"`
	ExecMaxActive           *int `json:"exec_max_active"`
	ExecMaxAge              *int `json:"exec_max_age_minutes"`
	MCPMaxConcurrent        *int `json:"mcp_max_concurrent_per_server"`
	MCPBreakerFails         *int `json:"mcp_breaker_threshold"`
	MCPBreakerCooldown      *int `json:"mcp_breaker_cooldown_sec"`
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// 全量校验必须在任何写库/运行态动作之前：后字段非法不能留下"改了一半"的状态。
	if err := s.validateSettingsRequest(req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// 纯 DB 键先收集，最后一次性事务提交；运行态应用（带内存字段的 setter 与
	// applyLLM）在提交之后执行——提交失败则一条都不写，应用失败则明确告知
	// 「设置已保存，运行态未生效」。
	pending := map[string]string{}
	applyErr := func(err error) {
		writeErr(w, 500, "设置已保存，但运行态应用失败（部分新配置尚未生效）："+err.Error())
	}
	var runtimeApplies []func() error
	corsDirty := false
	// 授权范围（RoE）：空间门（目标在不在 task_scope 内，模式 off|ask|deny）与时间门
	// （作业时间窗）。两个都由拦截层**每调用现读 setting**，所以改完即时生效，不需要重建 agent
	// ——因此刻意不进下面的 changed 分支。
	if req.ScopeMode != nil {
		v := strings.TrimSpace(*req.ScopeMode)
		if v != "off" && v != "ask" && v != "deny" {
			writeErr(w, 400, "授权范围模式必须是 off / ask / deny")
			return
		}
		pending[intercept.SettingScopeMode] = v
	}
	if req.ScopeHours != nil {
		// 落库前挡住格式错误：存进去一条解析不了的窗口，之后**每一次**工具调用都会落到人工确认上
		// （CheckScope 对无法解析的窗口如实 ask），而用户多半要等任务跑不动了才发现。
		v := strings.TrimSpace(*req.ScopeHours)
		if err := intercept.ValidateScopeHours(v); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		pending[intercept.SettingScopeHours] = v
	}
	if req.ArtifactLanguage != nil {
		// 取值当场校验：写进一个不认识的档，agent 侧会当成"不约束"静默放行——那正是
		// 本仓一路在防的静默降级。空串 = 回到默认（zh）。
		v := strings.TrimSpace(*req.ArtifactLanguage)
		if v == "" {
			v = agent.ArtifactLangZh
		}
		if v != agent.ArtifactLangZh && v != agent.ArtifactLangEn {
			writeErr(w, 400, "产物语言必须是 zh / en（zh = 不约束，en = 面向目标的产物一律英文）")
			return
		}
		pending[settingArtifactLanguage] = v
	}
	if req.CredentialBudget != nil {
		// 负数没有意义：0 已经是"不限"了，-1 只会让判定侧多一种要解释的状态。
		v := *req.CredentialBudget
		if v < 0 || v > 1000 {
			writeErr(w, 400, "凭据尝试预算必须是 0–1000（0 = 不限）")
			return
		}
		pending[intercept.SettingCredentialBudget] = strconv.Itoa(v)
	}
	if req.CredentialBudgetAction != nil {
		// 同产物语言：写进一个不认识的档，判定侧会退回默认（ask）——那是静默降级，当场拒掉。
		v := strings.TrimSpace(*req.CredentialBudgetAction)
		switch v {
		case "allow", "ask", "deny":
		default:
			writeErr(w, 400, "超预算处置必须是 allow / ask / deny")
			return
		}
		pending[intercept.SettingCredentialBudgetAction] = v
	}
	if req.CORSAllowedOrigins != nil {
		v := strings.TrimSpace(*req.CORSAllowedOrigins)
		if utf8.RuneCountInString(v) > 500 {
			writeErr(w, 400, "CORS 来源列表过长（最多 500 字）")
			return
		}
		pending[settingCORSAllowedOrigins] = v
		corsDirty = true // 提交后失效缓存，改完即时生效
	}
	if req.SiteName != nil {
		v := strings.TrimSpace(*req.SiteName)
		if utf8.RuneCountInString(v) > 60 {
			writeErr(w, 400, "显示名过长（最多 60 字）")
			return
		}
		pending[settingSiteName] = v
	}
	if req.ConstraintsInjectPlanner != nil {
		pending[settingConstraintsInjectPlanner] = strconv.FormatBool(*req.ConstraintsInjectPlanner)
	}
	if req.ConstraintsInjectWorker != nil {
		pending[settingConstraintsInjectWorker] = strconv.FormatBool(*req.ConstraintsInjectWorker)
	}
	if req.NoaCompaction != nil {
		// 每 run 读的解析器,切换对之后启动的 run 生效,无需 applyLLM 重建。
		pending[settingNoaCompaction] = strconv.FormatBool(*req.NoaCompaction)
	}
	// 推送全局项:投递引擎每轮重新读取,所以即时生效、无需重启。
	if req.NotifyEnabled != nil {
		pending[settingNotifyEnabled] = strconv.FormatBool(*req.NotifyEnabled)
	}
	if req.NotifyBaseURL != nil {
		// 统一裁掉尾部斜杠:回链拼接用的是 fmt.Sprintf("%s/function/..."),
		// 留着尾部斜杠会产出 "//function/..." 这种双斜杠路径。
		base := trimTrailingSlash(strings.TrimSpace(*req.NotifyBaseURL))
		if base != "" && !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			writeErr(w, 400, "回链地址需以 http:// 或 https:// 开头")
			return
		}
		pending[settingNotifyPublicBaseURL] = base
	}
	if req.NotifyDigestMins != nil {
		// 下限 1 分钟:更短的周期等于实时推送,那样应该直接把渠道改成 realtime 模式。
		if *req.NotifyDigestMins < 1 || *req.NotifyDigestMins > 24*60 {
			writeErr(w, 400, "汇总周期需在 1 到 1440 分钟之间")
			return
		}
		pending[settingNotifyDigestMinutes] = strconv.Itoa(*req.NotifyDigestMins)
	}
	// P21 情报源：都是"每调用现读"的键，保存即生效，无需重建 agent。
	// key/email/base_url/proxy：null=不改、""=清空；quota：0=不限。
	for _, f := range []struct {
		key string
		val *string
	}{
		{settingIntelKey, req.FOFAKey},
		{settingUpdateToken, req.UpdateToken},
		{settingIntelEmail, req.FOFAEmail},
		{settingIntelBaseURL, req.FOFABaseURL},
		{settingIntelProxy, req.FOFAProxy},
	} {
		if f.val == nil {
			continue
		}
		pending[f.key] = strings.TrimSpace(*f.val)
	}
	if req.IntelDailyQuota != nil {
		if *req.IntelDailyQuota < 0 || *req.IntelDailyQuota > 100000 {
			writeErr(w, 400, "情报每日上限需在 0（不限）到 100000 之间")
			return
		}
		pending[settingIntelDailyQuota] = strconv.Itoa(*req.IntelDailyQuota)
	}
	// P24 资源限额与 MCP 守卫：都是"每调用现读"的键，保存即对之后提交的执行/调用生效。
	for _, f := range []struct {
		key  string
		val  *int
		max  int
		name string
	}{
		{settingExecCPUSeconds, req.ExecCPUSeconds, 86400, "CPU 限额（秒）"},
		{settingExecMemoryMB, req.ExecMemoryMB, 1048576, "内存限额（MB）"},
		{settingExecMaxProcs, req.ExecMaxProcs, 65536, "进程数限额"},
		{settingExecMaxActive, req.ExecMaxActive, 4096, "同时执行数上限"},
		{settingExecMaxAge, req.ExecMaxAge, 525600, "单条执行最大存活（分钟）"},
		{settingMCPMaxConcurrent, req.MCPMaxConcurrent, 128, "MCP 并发上限"},
		{settingMCPBreakerFails, req.MCPBreakerFails, 1000, "MCP 熔断阈值"},
		{settingMCPBreakerCooldown, req.MCPBreakerCooldown, 86400, "MCP 熔断冷却（秒）"},
		// P28 存储清理：保留天数（0 = 不清理；上限 10 年，防手滑写成天文数字）
		{settingStorageRetentionTasks, req.StorageRetentionTasks, 3650, "任务产物保留天数"},
		{settingStorageRetentionSessions, req.StorageRetentionSessions, 3650, "会话目录保留天数"},
		{settingStorageRetentionDrafts, req.StorageRetentionDrafts, 3650, "草稿保留天数"},
		{settingStorageRetentionTranscript, req.StorageRetentionTranscript, 3650, "转写保留天数"},
		{settingStorageRetentionBinary, req.StorageRetentionBinary, 3650, "更新残留保留天数"},
		// 数据库侧保留天数（同一套 0 = 不清理、上限 10 年）
		{settingRetentionLLMUsage, req.RetentionLLMUsage, 3650, "LLM 用量台账保留天数"},
		{settingRetentionActivity, req.RetentionActivity, 3650, "执行活动流保留天数"},
		{settingRetentionServerLogs, req.RetentionServerLogs, 3650, "后端日志保留天数"},
		{settingRetentionLLMRecordBodys, req.RetentionLLMRecordBodys, 3650, "LLM 录制载荷保留天数"},
		{settingRetentionLLMRecords, req.RetentionLLMRecords, 3650, "LLM 录制整行保留天数"},
		{settingRetentionTraffic, req.RetentionTraffic, 3650, "流量归档保留天数"},
		{settingRetentionToolExecutions, req.RetentionToolExecs, 3650, "长任务台账保留天数"},
		{settingRetentionCredentialAttempts, req.RetentionCredAttempts, 3650, "凭据尝试记录保留天数"},
	} {
		if f.val == nil {
			continue
		}
		if *f.val < 0 || *f.val > f.max {
			writeErr(w, 400, fmt.Sprintf("%s 需在 0（不限）到 %d 之间", f.name, f.max))
			return
		}
		pending[f.key] = strconv.Itoa(*f.val)
	}
	if req.Workers != nil {
		pending[settingWorkers] = strconv.Itoa(*req.Workers)
	}
	if req.ConcurrencyEnabled != nil || req.ConcurrencyLimit != nil {
		// 部分 PUT:未给的字段用当前值兜底,避免只改一个把另一个重置。
		curOn, curLimit := s.m.ConcurrencyLimit()
		if curLimit == 0 {
			curLimit = defaultConcurrencyLimit
		}
		on, limit := curOn, curLimit
		if req.ConcurrencyEnabled != nil {
			on = *req.ConcurrencyEnabled
		}
		if req.ConcurrencyLimit != nil {
			limit = *req.ConcurrencyLimit
		}
		pending[settingConcurrencyLimit] = strconv.Itoa(limit)
		pending[settingConcurrencyOn] = strconv.FormatBool(on)
		// 提交后立即协调一次:关闭时放行全部排队,调高上限时补位启动,不必等下一个 tick。
		runtimeApplies = append(runtimeApplies, func() error {
			goSafe("server/reconcile-concurrency", nil, func() { s.reconcileConcurrency() })
			return nil
		})
	}
	if req.PythonInterp != nil {
		p := strings.TrimSpace(*req.PythonInterp)
		if err := validatePythonInterp(p); err != nil {
			writeErr(w, 400, "python_interpreter 不合法："+err.Error())
			return
		}
		pending[settingPythonInterp] = p
	}
	if req.LogLevel != nil {
		level := strings.ToLower(strings.TrimSpace(*req.LogLevel))
		if _, ok := parseLogLevel(level); !ok {
			writeErr(w, 400, "log_level 必须是 debug/info/warn/error 之一")
			return
		}
		pending[settingLogLevel] = level
		runtimeApplies = append(runtimeApplies, func() error {
			SetLogLevel(level)
			return nil
		})
	}
	if req.LLMRecord != nil {
		// 录制器每次调用读取该标志，切换即时生效，无需 applyLLM 重建。
		on := *req.LLMRecord
		pending[settingLLMRecord] = strconv.FormatBool(on)
		runtimeApplies = append(runtimeApplies, func() error { return s.m.SetLLMRecordEnabled(on) })
	}
	changed := false
	if req.LLMPoolEnabled != nil {
		pending[settingLLMPoolOn] = strconv.FormatBool(*req.LLMPoolEnabled)
		changed = true // the provider chain itself changes shape → rebuild
	}
	if req.LLMPoolBindFallback != nil {
		pending[settingLLMPoolBindFallback] = strconv.FormatBool(*req.LLMPoolBindFallback)
		changed = true
	}
	if req.LLMPoolEnabled != nil || req.LLMPoolBindFallback != nil {
		// Pinned tasks' planner/worker and per-profile chat agents hold providers
		// built under the OLD switch state — drop them so they pick up the new one.
		// 提交后再清：提交失败时不该先让运行中的 agent 失去旧配置。
		runtimeApplies = append(runtimeApplies, func() error {
			s.invalidateProfileAgents()
			return nil
		})
	}
	if req.AgentAutoRetest != nil {
		pending[settingAutoRetest] = strconv.FormatBool(*req.AgentAutoRetest)
	}
	if req.TaskTokenBudget != nil {
		v := *req.TaskTokenBudget
		if v < 0 {
			v = 0
		}
		pending[settingTokenBudget] = strconv.FormatInt(v, 10)
	}
	if req.AgentTrafficBinding != nil {
		pending[settingAgentTrafficBinding] = strconv.FormatBool(*req.AgentTrafficBinding)
	}
	if req.LootExec != nil {
		pending[settingLootExec] = strconv.FormatBool(*req.LootExec)
	}
	if req.OastEnabled != nil {
		pending[settingOASTEnabled] = strconv.FormatBool(*req.OastEnabled)
	}
	if req.OastToken != nil {
		// 空串 = 清除 token（与其它只写密钥同一口径）。
		pending[settingOASTToken] = strings.TrimSpace(*req.OastToken)
	}
	if req.OpsecNoise != nil {
		mode := strings.ToLower(strings.TrimSpace(*req.OpsecNoise))
		if mode != "normal" && mode != "quiet" {
			writeErr(w, 400, "opsec_noise_level 只能是 normal 或 quiet")
			return
		}
		pending[settingOpsecNoise] = mode
	}
	if req.OastDomain != nil {
		pending[settingOASTDomain] = strings.TrimSpace(*req.OastDomain)
	}
	if req.TrafficCapture != nil {
		on := *req.TrafficCapture
		pending[settingTrafficCapture] = strconv.FormatBool(on)
		runtimeApplies = append(runtimeApplies, func() error { return s.m.SetTrafficEnabled(on) })
		changed = true
	}
	if req.GlobalProxy != nil || req.GlobalProxyPool != nil || req.ProxyHealthURL != nil ||
		req.ProxyHealthTimeout != nil || req.ProxyHealthInterval != nil ||
		req.ProxyFailThreshold != nil || req.ProxySwitchCooldown != nil {
		// Fill unspecified fields from current state so a partial PUT (e.g. only
		// changing the health timeout) does not wipe the candidate list.
		cfg := s.m.ProxyPoolConfig()
		switch {
		case req.GlobalProxyPool != nil:
			cfg.Candidates = splitProxyCandidates(*req.GlobalProxyPool)
		case req.GlobalProxy != nil:
			cfg.Candidates = splitProxyCandidates(*req.GlobalProxy)
		}
		if req.ProxyHealthURL != nil {
			cfg.HealthURL = strings.TrimSpace(*req.ProxyHealthURL)
		}
		if req.ProxyHealthTimeout != nil {
			cfg.Timeout = time.Duration(*req.ProxyHealthTimeout) * time.Second
		}
		if req.ProxyHealthInterval != nil {
			cfg.Interval = time.Duration(*req.ProxyHealthInterval) * time.Second
		}
		if req.ProxyFailThreshold != nil {
			cfg.FailThreshold = *req.ProxyFailThreshold
		}
		if req.ProxySwitchCooldown != nil {
			cfg.Cooldown = time.Duration(*req.ProxySwitchCooldown) * time.Second
		}
		// Validation failure (bad scheme/host/health URL) is a client error, not a 500.
		norm, err := NormalizeProxyPoolConfig(cfg)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		primary := ""
		if len(norm.Candidates) > 0 {
			primary = norm.Candidates[0]
		}
		pending[settingGlobalProxyPool] = strings.Join(norm.Candidates, "\n")
		pending[settingGlobalProxy] = primary
		pending[settingProxyHealthURL] = norm.HealthURL
		pending[settingProxyHealthTimeout] = strconv.Itoa(int(norm.Timeout.Seconds()))
		pending[settingProxyHealthInterval] = strconv.Itoa(int(norm.Interval.Seconds()))
		pending[settingProxyFailThreshold] = strconv.Itoa(norm.FailThreshold)
		pending[settingProxySwitchCooldown] = strconv.Itoa(int(norm.Cooldown.Seconds()))
		runtimeApplies = append(runtimeApplies, func() error { return s.m.SetProxyPool(cfg) })
		changed = true
	}
	if req.ProxyEgressIPURL != nil {
		u, err := normalizeEgressIPURL(*req.ProxyEgressIPURL)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		pending[settingProxyEgressIPURL] = u
	}
	if req.ClashAPIURL != nil || req.ClashAPISecret != nil || req.ClashProxyGroup != nil ||
		req.ClashAutoRotate != nil || req.ClashRotateCooldown != nil {
		cfg := s.m.ClashEgressConfig()
		if req.ClashAPIURL != nil {
			u := strings.TrimRight(strings.TrimSpace(*req.ClashAPIURL), "/")
			if u != "" {
				pu, err := url.Parse(u)
				if err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https") {
					writeErr(w, 400, "Clash 控制器地址必须是 http(s) 绝对地址，例如 http://192.168.10.137:9090")
					return
				}
			}
			cfg.APIURL = u
			pending[settingClashAPIURL] = u
		}
		if req.ClashAPISecret != nil {
			cfg.Secret = strings.TrimSpace(*req.ClashAPISecret) // "" = 清空
			pending[settingClashAPISecret] = cfg.Secret
		}
		if req.ClashProxyGroup != nil {
			cfg.Group = strings.TrimSpace(*req.ClashProxyGroup)
			pending[settingClashProxyGroup] = cfg.Group
		}
		if req.ClashAutoRotate != nil {
			cfg.Auto = *req.ClashAutoRotate
			pending[settingClashAutoRotate] = strconv.FormatBool(cfg.Auto)
		}
		if req.ClashRotateCooldown != nil {
			secs := *req.ClashRotateCooldown
			if secs < 0 {
				secs = 0
			}
			if secs > 600 {
				secs = 600
			}
			cfg.Cooldown = time.Duration(secs) * time.Second
			pending[settingClashRotateCooldown] = strconv.Itoa(secs)
		}
	}
	if req.WebSearchEnabled != nil || req.WebSearchBackend != nil || req.BraveKey != nil || req.TavilyKey != nil || req.WebSearchProxy != nil {
		// Fill unspecified fields from current state so a partial PUT doesn't reset them.
		on, backend, _, _, _ := s.m.WebSearch()
		if req.WebSearchEnabled != nil {
			on = *req.WebSearchEnabled
		}
		if req.WebSearchBackend != nil {
			backend = *req.WebSearchBackend
		}
		if strings.TrimSpace(backend) == "" {
			backend = defaultWebSearchBackend
		}
		pending[settingWebSearchOn] = strconv.FormatBool(on)
		pending[settingWebSearchBackend] = backend
		if req.BraveKey != nil {
			pending[settingBraveKey] = *req.BraveKey
		}
		if req.TavilyKey != nil {
			pending[settingTavilyKey] = *req.TavilyKey
		}
		if req.WebSearchProxy != nil {
			pending[settingWebSearchProxy] = strings.TrimSpace(*req.WebSearchProxy)
		}
		runtimeApplies = append(runtimeApplies, func() error {
			return s.m.SetWebSearch(on, backend, req.BraveKey, req.TavilyKey, req.WebSearchProxy)
		})
		changed = true
	}
	// 先原子提交全部纯 DB 键；提交失败则本次改动一条都不写。
	if err := s.m.pg.SetSettings(pending); err != nil {
		writeErr(w, 500, "设置保存失败（本次改动未写入）："+err.Error())
		return
	}
	if corsDirty {
		s.cors.mu.Lock()
		s.cors.at = time.Time{} // 立刻失效缓存，改完即时生效（与其它设置同一口径）
		s.cors.mu.Unlock()
	}
	for _, apply := range runtimeApplies {
		if err := apply(); err != nil {
			applyErr(err)
			return
		}
	}
	if changed {
		// rebuild agents so the new proxy/tools/prompt/web-search take hold (only if LLM configured).
		s.llm.mu.Lock()
		cfg, on := s.llm.cfg, s.llm.on
		s.llm.mu.Unlock()
		if on {
			if err := s.applyLLM(cfg); err != nil {
				// 设置已经落库，只是运行态没重建成功：不能再用一个 500 让用户以为"全都没变"。
				writeErr(w, 500, "设置已保存，但运行态重建失败（新配置尚未生效）："+err.Error())
				return
			}
		}
	}
	writeJSON(w, 200, s.settingsPayload())
}

// testWebSearch runs a real "test" search with the given (or currently saved)
// backend/proxy/key to verify the config can actually reach a search backend —
// mirroring testLLM. Backend/proxy come from the request (so the form's unsaved
// edits are tested); empty API keys fall back to stored values so the user need
// not retype them. Always 200 with {ok, error?, count?, backend?}.
func (s *Server) testWebSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Backend   string `json:"web_search_backend"`
		Proxy     string `json:"web_search_proxy"`
		BraveKey  string `json:"brave_search_api_key"`
		TavilyKey string `json:"tavily_search_api_key"`
	}
	// Empty body is fine — fall back entirely to the saved config below.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, 400, err.Error())
		return
	}
	_, backend, storedBraveKey, storedTavilyKey, _ := s.m.WebSearch()
	if strings.TrimSpace(req.Backend) != "" {
		backend = req.Backend
	}
	// Proxy is taken from the form as-is (empty = direct), so testing reflects exactly
	// what's shown — including an intentional "clear proxy to test direct" before saving.
	proxy := strings.TrimSpace(req.Proxy)
	// API keys are secrets the form omits when already saved, so fall back to stored.
	braveKey := storedBraveKey
	if strings.TrimSpace(req.BraveKey) != "" {
		braveKey = req.BraveKey
	}
	tavilyKey := storedTavilyKey
	if strings.TrimSpace(req.TavilyKey) != "" {
		tavilyKey = req.TavilyKey
	}
	// 与线上工具**同一套解析**（后端归一 + 凭据 + 路由头）——自己拼一份的话，
	// "测试通过但工具不能用"是迟早的事（opencode 的 x-opencode-session 就是这么漏的）。
	cfg := actool.WebSearchConfig{BraveAPIKey: braveKey, TavilyAPIKey: tavilyKey, Proxy: proxy}
	res := s.m.searchBackendForSDK(backend)
	backend, cfg.Backend = res.Backend, res.Backend
	cfg.DeepSeekBaseURL, cfg.DeepSeekAPIKey, cfg.DeepSeekModel, cfg.DeepSeekHeaders = res.BaseURL, res.APIKey, res.Model, res.Headers
	cfg.CommandCodeBaseURL, cfg.CommandCodeAPIKey = res.CCBaseURL, res.CCAPIKey
	// Hard cap so a slow/blocked proxy can't hang the request.
	wall := 30 * time.Second
	// deepseek 的凭据不在表单里，来自当前激活的 LLM 配置；同时它每次搜索都跑一次
	// 模型推理，30s 的通用上限偏紧，单独放宽。这里不预判配置能不能用——测这一下
	// 本来就是给用户自己确认的手段，真跑不通时下面的报错比预判更有信息量。
	probeQuery := "test"
	if strings.TrimSpace(backend) == deepSeekWebSearchBackend {
		wall = 120 * time.Second
		// 搜索词由 DeepSeek 端的模型自行决定，"test" 太空泛会让它跳过搜索直接作答。
		probeQuery = "DeepSeek company official website"
	}
	ctx, cancel := context.WithTimeout(r.Context(), wall)
	defer cancel()
	results, err := actool.WebSearchProbe(ctx, cfg, probeQuery, 3)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error(), "backend": backend})
		return
	}
	if len(results) == 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "搜索返回 0 条结果（可能被限流或代理不通）", "backend": backend})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "count": len(results), "backend": backend})
}

// noiseLevel 读噪声档位（缺省 normal）。
func noiseLevel(pg *db.DB) string {
	v, ok, _ := pg.GetSetting(settingOpsecNoise)
	if ok && strings.EqualFold(strings.TrimSpace(v), "quiet") {
		return "quiet"
	}
	return "normal"
}

// proxyRotateEgress switches the Clash Selector to another safe node (manual
// button / external caller). The same safety rules as the agent tool apply:
// non-DIRECT nodes only, egress IP must actually change, never direct.
func (s *Server) proxyRotateEgress(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "手动切换出口"
	}
	res, err := s.m.RotateEgress(r.Context(), reason)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "rotation": res, "status": s.m.ClashEgressStatus()})
}
