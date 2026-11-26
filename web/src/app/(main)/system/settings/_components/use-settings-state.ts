"use client";

import * as React from "react";

import { toast } from "sonner";

import { useUnsavedChanges } from "@/hooks/use-unsaved-changes";
import { api } from "@/lib/api";
import { useChatSendMode } from "@/lib/chat-send-mode";
import { createLatestRequest } from "@/lib/request-lifecycle";
import { formatWebSearchTestToast } from "@/lib/search-helpers";
import { createSettingsDrafts, type SettingsTicket } from "@/lib/settings-drafts";
import type {
  ClashEgressStatus,
  EgressStatus,
  IntelQueryRow,
  LLMProfile,
  OpsecKnowledgeResponse,
  ProxyEgressResult,
  ProxyPoolHealth,
  ProxyPoolStatusPayload,
  ProxyPoolStrategy,
  ScopeEnforcement,
  Settings,
  WebSearchProxyMode,
  WebSearchStats,
  WebSearchTestResult,
} from "@/lib/types";

// 系统配置页的状态与全部读写操作（抓包/搜索/代理/限额/情报/策略/运行时等）。
//
// 抽成 hook 是为了让页面本体只剩「分节导航 + 七个分节的 JSX」。各分节组件通过
// `ReturnType<typeof useSettingsState>` 拿到同一份状态，不必逐项声明 props。

export const SECTIONS = [
  { id: "basic", label: "基本设置" },
  { id: "network", label: "网络与代理" },
  { id: "search", label: "联网搜索" },
  { id: "intel", label: "情报来源" },
  { id: "policy", label: "安全与策略" },
  { id: "runtime", label: "执行与并发" },
  { id: "advanced", label: "高级与界面" },
] as const;

export type SectionId = (typeof SECTIONS)[number]["id"];

export function exposureWeight(exposure: string): number {
  switch (exposure) {
    case "高":
      return 3;
    case "中":
      return 2;
    case "低":
      return 1;
  }
  return 0;
}

function useSettingsField<T>(
  drafts: ReturnType<typeof createSettingsDrafts>,
  key: string,
  deps: string[],
  initial: T,
  writeOnly = false,
): [T, React.Dispatch<React.SetStateAction<T>>] {
  const [value, publish] = React.useState(initial);
  drafts.register(key, initial, deps, publish as (value: unknown) => void, writeOnly);
  const edit = React.useCallback<React.Dispatch<React.SetStateAction<T>>>(
    (next) => {
      drafts.edit(key, next as unknown);
    },
    [drafts, key],
  );
  return [value, edit];
}

export function useSettingsState() {
  const [draftRevision, changed] = React.useReducer((value: number) => value + 1, 0);
  const [drafts] = React.useState(() => createSettingsDrafts(changed));
  const [loadError, setLoadError] = React.useState("");
  const [loadingSettings, setLoadingSettings] = React.useState(false);
  const [loadOwner] = React.useState(createLatestRequest);
  const [trafficCapture, setTrafficCapture] = useSettingsField(drafts, "trafficCapture", ["traffic_capture"], false);
  const [agentTrafficBinding, setAgentTrafficBinding] = useSettingsField(
    drafts,
    "agentTrafficBinding",
    ["agent_traffic_binding"],
    false,
  );
  const [agentLootExec, setAgentLootExec] = useSettingsField(drafts, "agentLootExec", ["agent_loot_exec"], false);
  const [agentAutoRetest, setAgentAutoRetest] = useSettingsField(
    drafts,
    "agentAutoRetest",
    ["agent_auto_retest"],
    false,
  );
  const [attackChainContinuation, setAttackChainContinuation] = useSettingsField(
    drafts,
    "attackChainContinuation",
    ["attack_chain_continuation"],
    true,
  );
  const [savingAttackChain, setSavingAttackChain] = React.useState(false);
  const [emptyReportPatrol, setEmptyReportPatrol] = useSettingsField(
    drafts,
    "emptyReportPatrol",
    ["empty_report_patrol"],
    false,
  );
  const [savingEmptyReportPatrol, setSavingEmptyReportPatrol] = React.useState(false);
  const [credentialPlaintext, setCredentialPlaintext] = useSettingsField(
    drafts,
    "credentialPlaintext",
    ["credential_plaintext_fidelity"],
    true,
  );
  const [savingCredentialPlaintext, setSavingCredentialPlaintext] = React.useState(false);
  const [compactorProfileID, setCompactorProfileID] = useSettingsField(
    drafts,
    "compactorProfileID",
    ["compactor_llm_profile_id"],
    "0",
  );
  const [llmProfiles, setLlmProfiles] = React.useState<LLMProfile[]>([]);
  const [savingCompactorProfile, setSavingCompactorProfile] = React.useState(false);
  const [oastEnabled, setOastEnabled] = useSettingsField(drafts, "oastEnabled", ["oast_enabled"], false);
  const [oastTokenSet, setOastTokenSet] = React.useState(false);
  const [oastTokenInput, setOastTokenInput] = useSettingsField(drafts, "oastTokenInput", ["oast_token"], "", true);
  const [oastDomain, setOastDomain] = useSettingsField(drafts, "oastDomain", ["oast_domain"], "requestrepo.com");
  const [oastNucleiServer, setOastNucleiServer] = useSettingsField(
    drafts,
    "oastNucleiServer",
    ["oast_nuclei_server"],
    "oast.fun",
  );
  const [noiseLevel, setNoiseLevel] = useSettingsField(drafts, "noiseLevel", ["opsec_noise_level"], "normal");
  const [evidenceVerify, setEvidenceVerify] = useSettingsField<"off" | "warn" | "block">(
    drafts,
    "evidenceVerify",
    ["finding_evidence_verify"],
    "warn",
  );
  const [savingEvidenceVerify, setSavingEvidenceVerify] = React.useState(false);
  const [webSearch, setWebSearch] = useSettingsField(drafts, "webSearch", ["web_search_enabled"], false);
  const [backend, setBackend] = useSettingsField(drafts, "backend", ["web_search_backend"], "ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = useSettingsField(
    drafts,
    "braveKeyInput",
    ["brave_search_api_key"],
    "",
    true,
  );
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = useSettingsField(
    drafts,
    "tavilyKeyInput",
    ["tavily_search_api_key"],
    "",
    true,
  );
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = useSettingsField(drafts, "proxyInput", ["web_search_proxy"], "");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [searxngURL, setSearxngURL] = useSettingsField(drafts, "searxngURL", ["web_search_searxng_url"], "");
  const [savingSearxngURL, setSavingSearxngURL] = React.useState(false);
  const [webSearchProxyMode, setWebSearchProxyMode] = React.useState<WebSearchProxyMode>("direct");
  const [webSearchStats, setWebSearchStats] = React.useState<WebSearchStats | null>(null);
  const [lastSearchTest, setLastSearchTest] = React.useState<WebSearchTestResult | null>(null);
  // 代理池：每行一个候选（Clash 混合端口即可）；健康检查/切换在服务端运行。
  const [globalProxyPoolInput, setGlobalProxyPoolInput] = useSettingsField(
    drafts,
    "globalProxyPoolInput",
    ["global_proxy_pool", "global_proxy"],
    "",
  );
  // 已保存的代理池（用于区分"输入了但还没保存"与"正在检测已保存的配置"）。
  const [proxySavedPool, setProxySavedPool] = React.useState("");
  const [proxyHealthURL, setProxyHealthURL] = useSettingsField(drafts, "proxyHealthURL", ["proxy_health_url"], "");
  const [proxyHealthTimeout, setProxyHealthTimeout] = useSettingsField(
    drafts,
    "proxyHealthTimeout",
    ["proxy_health_timeout_seconds"],
    "5",
  );
  const [proxyHealthInterval, setProxyHealthInterval] = useSettingsField(
    drafts,
    "proxyHealthInterval",
    ["proxy_health_interval_seconds"],
    "30",
  );
  const [proxyFailThreshold, setProxyFailThreshold] = useSettingsField(
    drafts,
    "proxyFailThreshold",
    ["proxy_fail_threshold"],
    "2",
  );
  const [proxySwitchCooldown, setProxySwitchCooldown] = useSettingsField(
    drafts,
    "proxySwitchCooldown",
    ["proxy_switch_cooldown_seconds"],
    "30",
  );
  const [proxyStrategy, setProxyStrategy] = useSettingsField<ProxyPoolStrategy>(
    drafts,
    "proxyStrategy",
    ["proxy_strategy"],
    "ordered",
  );
  const [proxyActive, setProxyActive] = React.useState("");
  const [proxyBlocked, setProxyBlocked] = React.useState(false);
  const [proxyPinned, setProxyPinned] = React.useState("");
  const [proxyProbing, setProxyProbing] = React.useState(false);
  const [probingProxy, setProbingProxy] = React.useState(false);
  const [pinningProxy, setPinningProxy] = React.useState("");
  const [proxyStatus, setProxyStatus] = React.useState<ProxyPoolHealth[]>([]);
  const [proxyEgressIPURL, setProxyEgressIPURL] = useSettingsField(
    drafts,
    "proxyEgressIPURL",
    ["proxy_egress_ip_url"],
    "https://api.ipify.org",
  );
  const [proxyEgress, setProxyEgress] = React.useState<ProxyEgressResult[]>([]);
  const [checkingProxyEgress, setCheckingProxyEgress] = React.useState(false);
  const [proxySaveError, setProxySaveError] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  // Clash 出口切换（WAF 封禁出口时换节点；服务端验证新出口 IP，绝不直连）。
  const [clashApiURL, setClashApiURL] = useSettingsField(drafts, "clashApiURL", ["clash_api_url"], "");
  const [clashSecretInput, setClashSecretInput] = useSettingsField(
    drafts,
    "clashSecretInput",
    ["clash_api_secret"],
    "",
    true,
  );
  const [clashSecretSet, setClashSecretSet] = React.useState(false);
  const [clashGroup, setClashGroup] = useSettingsField(drafts, "clashGroup", ["clash_proxy_group"], "");
  const [clashGroups, setClashGroups] = useSettingsField(drafts, "clashGroups", ["clash_proxy_groups"], "");
  const [clashTaskEndpoints, setClashTaskEndpoints] = useSettingsField(
    drafts,
    "clashTaskEndpoints",
    ["clash_task_endpoints"],
    "",
  );
  const [clashAuto, setClashAuto] = useSettingsField(drafts, "clashAuto", ["clash_auto_rotate"], true);
  const [clashCooldown, setClashCooldown] = useSettingsField(
    drafts,
    "clashCooldown",
    ["clash_rotate_cooldown_seconds"],
    "120",
  );
  const [clashStatus, setClashStatus] = React.useState<ClashEgressStatus | null>(null);
  const [egress, setEgress] = React.useState<EgressStatus | null>(null);
  const [savingClash, setSavingClash] = React.useState(false);
  const [rotatingClash, setRotatingClash] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  // 分区导航：一次只渲染一个分区（照 CyberStrikeAI 的设置页骨架）。
  const [section, setSection] = React.useState<SectionId>("basic");
  // 曾经的共享 `saving` 会让流量捕获 / 网络搜索 / Python 三处互相禁用控件——按用途拆开。
  const [savingTraffic, setSavingTraffic] = React.useState(false);
  const [savingAgentBinding, setSavingAgentBinding] = React.useState(false);
  const [savingLootExec, setSavingLootExec] = React.useState(false);
  const [savingAutoRetest, setSavingAutoRetest] = React.useState(false);
  const [savingOast, setSavingOast] = React.useState(false);
  const [savingWebSearch, setSavingWebSearch] = React.useState(false);
  const [savingPython, setSavingPython] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = useSettingsField(drafts, "pyInterp", ["python_interpreter"], "");
  const [workers, setWorkers] = useSettingsField(drafts, "workers", ["workers"], "3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 执行审核员：开关 + 周期（秒）。运行时每周期现读，改完对下一个周期生效。
  // 任务级 token 预算(0 = 关闭)。
  const [tokenBudget, setTokenBudget] = useSettingsField(drafts, "tokenBudget", ["task_token_budget"], "0");
  const [tokenBudgetPause, setTokenBudgetPause] = useSettingsField(
    drafts,
    "tokenBudgetPause",
    ["task_token_budget_pause"],
    false,
  );
  const [savingBudget, setSavingBudget] = React.useState(false);
  const [llmBudgetUSD, setLLMBudgetUSD] = useSettingsField(drafts, "llmBudgetUSD", ["llm_monthly_budget_usd"], "0");
  const [llmBudgetAlertPct, setLLMBudgetAlertPct] = useSettingsField(
    drafts,
    "llmBudgetAlertPct",
    ["llm_budget_alert_pct"],
    "80",
  );
  const [savingLLMBudget, setSavingLLMBudget] = React.useState(false);
  // 有界黑板视图：默认裁剪态势省 token；关掉后 planner 每轮预取完整态势。
  const [boundedView, setBoundedView] = useSettingsField(drafts, "boundedView", ["planner_bounded_view"], true);
  const [savingBoundedView, setSavingBoundedView] = React.useState(false);
  // P24 执行资源限额与外部 MCP 守卫（0 = 不限；保存即对之后提交的执行/调用生效）。
  const [execCPU, setExecCPU] = useSettingsField(drafts, "execCPU", ["exec_cpu_seconds"], "1800");
  const [execMem, setExecMem] = useSettingsField(drafts, "execMem", ["exec_memory_mb"], "0");
  const [execProcs, setExecProcs] = useSettingsField(drafts, "execProcs", ["exec_max_procs"], "0");
  const [execMaxActive, setExecMaxActive] = useSettingsField(drafts, "execMaxActive", ["exec_max_active"], "8");
  // 单条托管执行的最大存活分钟数（默认 120，0 = 不限）。
  const [execMaxAge, setExecMaxAge] = useSettingsField(drafts, "execMaxAge", ["exec_max_age_minutes"], "120");
  const [logLevel, setLogLevel] = useSettingsField<Settings["log_level"]>(drafts, "logLevel", ["log_level"], "info");
  const [savingLogLevel, setSavingLogLevel] = React.useState(false);
  const [mcpConcurrent, setMcpConcurrent] = useSettingsField(
    drafts,
    "mcpConcurrent",
    ["mcp_max_concurrent_per_server"],
    "2",
  );
  const [mcpBreakerFails, setMcpBreakerFails] = useSettingsField(
    drafts,
    "mcpBreakerFails",
    ["mcp_breaker_threshold"],
    "5",
  );
  const [mcpBreakerCooldown, setMcpBreakerCooldown] = useSettingsField(
    drafts,
    "mcpBreakerCooldown",
    ["mcp_breaker_cooldown_sec"],
    "60",
  );
  const [savingLimits, setSavingLimits] = React.useState(false);
  // P21 情报源（先接 FOFA）。key 只写不读：页面只显示"是否已配置"，输入框留空 = 不改。
  const [intelKeyInput, setIntelKeyInput] = useSettingsField(drafts, "intelKeyInput", ["fofa_key"], "", true);
  const [intelKeySet, setIntelKeySet] = React.useState(false);
  const [intelEmail, setIntelEmail] = useSettingsField(drafts, "intelEmail", ["fofa_email"], "");
  const [intelBaseURL, setIntelBaseURL] = useSettingsField(drafts, "intelBaseURL", ["fofa_base_url"], "");
  const [intelProxy, setIntelProxy] = useSettingsField(drafts, "intelProxy", ["fofa_proxy"], "");
  const [intelQuota, setIntelQuota] = useSettingsField(drafts, "intelQuota", ["intel_daily_quota"], "200");
  const [intelUsed, setIntelUsed] = React.useState(0);
  const [intelBusy, setIntelBusy] = React.useState(false);
  const [intelTesting, setIntelTesting] = React.useState(false);
  const [intelTestMsg, setIntelTestMsg] = React.useState("");
  const [intelRows, setIntelRows] = React.useState<IntelQueryRow[]>([]);
  // 操作约束注入范围(默认都开)。
  const [injectPlanner, setInjectPlanner] = useSettingsField(
    drafts,
    "injectPlanner",
    ["constraints_inject_planner"],
    true,
  );
  const [injectWorker, setInjectWorker] = useSettingsField(drafts, "injectWorker", ["constraints_inject_worker"], true);
  // 实验功能:noa 上下文压缩(默认关)。
  const [noaCompaction, setNoaCompaction] = useSettingsField(drafts, "noaCompaction", ["noa_compaction"], false);
  const [chatAutoTitle, setChatAutoTitle] = useSettingsField(drafts, "chatAutoTitle", ["chat_auto_title"], false);
  // 授权范围（RoE）：空间门模式 + 作业时间窗。模式回生效值，另存一个 env 覆盖标志。
  const [scopeMode, setScopeMode] = useSettingsField(drafts, "scopeMode", ["intercept_scope_mode"], "ask");
  const [scopeModeEnv, setScopeModeEnv] = React.useState(false);
  const [scopeHours, setScopeHours] = useSettingsField(drafts, "scopeHours", ["intercept_scope_hours"], "");
  // 作业时间窗的时区（IANA 名；空 = 跟随平台所在机器）。与窗口一起保存。
  const [scopeHoursTZ, setScopeHoursTZ] = useSettingsField(drafts, "scopeHoursTZ", ["intercept_scope_hours_tz"], "");
  // 服务器本地时间与时区名：页面据此提示"窗口按哪个时区算"，不必让人自己去对时。
  const [serverLocalTime, setServerLocalTime] = React.useState("");
  const [serverLocalZone, setServerLocalZone] = React.useState("");
  const [savingScope, setSavingScope] = React.useState(false);
  // P41 面向目标的产物语言（默认 zh = 不约束）。与界面语言是两件事，刻意分开放。
  const [artifactLang, setArtifactLang] = useSettingsField<"zh" | "en">(
    drafts,
    "artifactLang",
    ["artifact_language"],
    "zh",
  );
  const [savingArtifactLang, setSavingArtifactLang] = React.useState(false);
  // P43 凭据尝试预算：窗口内对同一 (目标, 账号) 允许试几次（0 = 不限），超了怎么处置。
  const [credBudget, setCredBudget] = useSettingsField(drafts, "credBudget", ["credential_attempt_budget"], "3");
  const [credBudgetAction, setCredBudgetAction] = useSettingsField<"allow" | "ask" | "deny">(
    drafts,
    "credBudgetAction",
    ["credential_attempt_budget_action"],
    "ask",
  );
  const [savingCredBudget, setSavingCredBudget] = React.useState(false);
  // P42 显示名（空 = 内置 BLADE）与 CORS 来源白名单（空 = 不发 CORS 头）。
  const [siteName, setSiteName] = useSettingsField(drafts, "siteName", ["site_name"], "");
  const [savingSiteName, setSavingSiteName] = React.useState(false);
  const [corsOrigins, setCorsOrigins] = useSettingsField(drafts, "corsOrigins", ["cors_allowed_origins"], "");
  const [savingCors, setSavingCors] = React.useState(false);
  // P35 停机开关与作业时间窗的**执行状态**（不是设置）：到点/停机时平台停到哪一步了。
  const [enforce, setEnforce] = React.useState<ScopeEnforcement | null>(null);
  const [killReason, setKillReason] = React.useState("");
  const [killBusy, setKillBusy] = React.useState(false);
  // P38 可检出性知识库（只读一览）：让人看得见这份清单覆盖到哪、边界在哪。
  const [opsecKB, setOpsecKB] = React.useState<OpsecKnowledgeResponse | null>(null);
  const [resumeBusy, setResumeBusy] = React.useState(false);
  // 纯前端偏好：不走 /api/settings，直接读写 localStorage。
  const sendMode = useChatSendMode();

  const apply = React.useCallback(
    (s: Settings, ticket: SettingsTicket) => {
      const applyReadonly = drafts.applyReadonly(ticket);
      drafts.applyField("trafficCapture", !!s.traffic_capture, ticket);
      drafts.applyField("agentTrafficBinding", !!s.agent_traffic_binding, ticket);
      drafts.applyField("agentLootExec", !!s.agent_loot_exec, ticket);
      drafts.applyField("agentAutoRetest", !!s.agent_auto_retest, ticket);
      drafts.applyField("attackChainContinuation", s.attack_chain_continuation !== false, ticket);
      drafts.applyField("emptyReportPatrol", !!s.empty_report_patrol, ticket);
      drafts.applyField("credentialPlaintext", s.credential_plaintext_fidelity !== false, ticket);
      drafts.applyField("compactorProfileID", String(s.compactor_llm_profile_id ?? 0), ticket);
      drafts.applyField("oastEnabled", !!s.oast_enabled, ticket);
      if (applyReadonly) setOastTokenSet(!!s.oast_token_set);
      drafts.applyField("oastDomain", s.oast_domain || "requestrepo.com", ticket);
      drafts.applyField("oastNucleiServer", s.oast_nuclei_server || "oast.fun", ticket);
      drafts.applyField("noiseLevel", s.opsec_noise_level === "quiet" ? "quiet" : "normal", ticket);
      drafts.applyField(
        "evidenceVerify",
        s.finding_evidence_verify === "off" || s.finding_evidence_verify === "block"
          ? s.finding_evidence_verify
          : "warn",
        ticket,
      );
      drafts.applyField("webSearch", !!s.web_search_enabled, ticket);
      drafts.applyField("backend", s.web_search_backend || "ddgs", ticket);
      if (applyReadonly) setBraveKeySet(!!s.brave_key_set);
      if (applyReadonly) setTavilyKeySet(!!s.tavily_key_set);
      drafts.applyField("proxyInput", s.web_search_proxy ?? "", ticket);
      drafts.applyField("searxngURL", s.web_search_searxng_url ?? "", ticket);
      if (applyReadonly) setWebSearchProxyMode(s.web_search_proxy_mode ?? "direct");
      if (applyReadonly) setWebSearchStats(s.web_search_stats ?? null);
      const savedPool = s.global_proxy_pool || s.global_proxy || "";
      drafts.applyField("globalProxyPoolInput", savedPool, ticket);
      if (applyReadonly) setProxySavedPool(savedPool);
      drafts.applyField("proxyHealthURL", s.proxy_health_url || "https://www.gstatic.com/generate_204", ticket);
      drafts.applyField("proxyHealthTimeout", String(s.proxy_health_timeout_seconds ?? 5), ticket);
      drafts.applyField("proxyHealthInterval", String(s.proxy_health_interval_seconds ?? 30), ticket);
      drafts.applyField("proxyFailThreshold", String(s.proxy_fail_threshold ?? 2), ticket);
      drafts.applyField("proxySwitchCooldown", String(s.proxy_switch_cooldown_seconds ?? 30), ticket);
      drafts.applyField("proxyStrategy", s.proxy_strategy || "ordered", ticket);
      drafts.applyField("proxyEgressIPURL", s.proxy_egress_ip_url || "https://api.ipify.org", ticket);
      drafts.applyField("clashApiURL", s.clash_api_url ?? "", ticket);
      if (applyReadonly) setClashSecretSet(!!s.clash_api_secret_set);
      drafts.applyField("clashGroup", s.clash_proxy_group ?? "", ticket);
      drafts.applyField("clashGroups", s.clash_proxy_groups ?? "", ticket);
      drafts.applyField("clashTaskEndpoints", s.clash_task_endpoints ?? "", ticket);
      drafts.applyField("clashAuto", s.clash_auto_rotate !== false, ticket);
      drafts.applyField("clashCooldown", String(s.clash_rotate_cooldown_seconds ?? 120), ticket);
      if (applyReadonly) setClashStatus(s.clash_status ?? null);
      if (applyReadonly) {
        setProxyActive(s.proxy_active ?? "");
        setProxyBlocked(!!s.proxy_blocked);
        setProxyPinned(s.proxy_pinned ?? "");
        setProxyProbing(!!s.proxy_probing);
        setProxyStatus(s.proxy_status ?? []);
      }
      drafts.applyField("pyInterp", s.python_interpreter ?? "", ticket);
      drafts.applyField("workers", String(s.workers ?? 3), ticket);
      drafts.applyField("tokenBudget", String(s.task_token_budget ?? 0), ticket);
      drafts.applyField("tokenBudgetPause", !!s.task_token_budget_pause, ticket);
      drafts.applyField("llmBudgetUSD", String(s.llm_monthly_budget_usd ?? 0), ticket);
      drafts.applyField("llmBudgetAlertPct", String(s.llm_budget_alert_pct ?? 80), ticket);
      drafts.applyField("boundedView", s.planner_bounded_view !== false, ticket);
      drafts.applyField("injectPlanner", s.constraints_inject_planner !== false, ticket);
      drafts.applyField("injectWorker", s.constraints_inject_worker !== false, ticket);
      drafts.applyField("noaCompaction", !!s.noa_compaction, ticket);
      drafts.applyField("chatAutoTitle", !!s.chat_auto_title, ticket);
      drafts.applyField("scopeMode", s.intercept_scope_mode ?? "ask", ticket);
      if (applyReadonly) setScopeModeEnv(!!s.intercept_scope_mode_env);
      drafts.applyField("scopeHours", s.intercept_scope_hours ?? "", ticket);
      drafts.applyField("scopeHoursTZ", s.intercept_scope_hours_tz ?? "", ticket);
      if (applyReadonly) setServerLocalTime(s.server_local_time ?? "");
      if (applyReadonly) setServerLocalZone(s.server_local_zone ?? "");
      drafts.applyField("artifactLang", s.artifact_language === "en" ? "en" : "zh", ticket);
      drafts.applyField("credBudget", String(s.credential_attempt_budget ?? 3), ticket);
      drafts.applyField("credBudgetAction", s.credential_attempt_budget_action ?? "ask", ticket);
      drafts.applyField("siteName", s.site_name ?? "", ticket);
      drafts.applyField("corsOrigins", s.cors_allowed_origins ?? "", ticket);
      if (applyReadonly) setIntelKeySet(!!s.fofa_key_set);
      drafts.applyField("intelEmail", s.fofa_email ?? "", ticket);
      drafts.applyField("intelBaseURL", s.fofa_base_url ?? "", ticket);
      drafts.applyField("intelProxy", s.fofa_proxy ?? "", ticket);
      drafts.applyField("intelQuota", String(s.intel_daily_quota ?? 200), ticket);
      if (applyReadonly) setIntelUsed(s.intel_used_today ?? 0);
      drafts.applyField("execCPU", String(s.exec_cpu_seconds ?? 1800), ticket);
      drafts.applyField("execMem", String(s.exec_memory_mb ?? 0), ticket);
      drafts.applyField("execProcs", String(s.exec_max_procs ?? 0), ticket);
      drafts.applyField("execMaxActive", String(s.exec_max_active ?? 8), ticket);
      drafts.applyField("execMaxAge", String(s.exec_max_age_minutes ?? 120), ticket);
      drafts.applyField("logLevel", s.log_level ?? "info", ticket);
      drafts.applyField("mcpConcurrent", String(s.mcp_max_concurrent_per_server ?? 2), ticket);
      drafts.applyField("mcpBreakerFails", String(s.mcp_breaker_threshold ?? 5), ticket);
      drafts.applyField("mcpBreakerCooldown", String(s.mcp_breaker_cooldown_sec ?? 60), ticket);
    },
    [drafts],
  );

  const saveSettings = React.useCallback(
    (patch: Record<string, unknown>) =>
      drafts.submit(patch, async (payload, ticket) => {
        const result = await api.setSettings(payload);
        apply(result, ticket);
        return result;
      }),
    [drafts, apply],
  );

  const loadSettings = React.useCallback(async () => {
    const request = loadOwner.start();
    const ticket = drafts.read();
    setLoadingSettings(true);
    setLoadError("");
    try {
      const result = await api.settings({ signal: request.signal });
      if (!request.alive()) return;
      apply(result, ticket);
      setLoaded(true);
    } catch (error) {
      if (request.alive()) setLoadError((error as Error).message || "设置读取失败");
    } finally {
      if (request.alive()) setLoadingSettings(false);
    }
  }, [apply, drafts, loadOwner]);
  const hasUnsavedChanges = drafts.dirty(draftRevision);
  useUnsavedChanges(hasUnsavedChanges);

  // 全局默认策略：独立加载（不进 apply——它由 /api/settings 触发，而策略走自己的端点）。

  const loadIntelQueries = React.useCallback(() => {
    void api
      .intelQueries()
      .then((r) => {
        setIntelRows(r.queries ?? []);
        setIntelUsed(r.used_today);
      })
      .catch(() => {});
  }, []);

  React.useEffect(() => {
    loadIntelQueries();
  }, [loadIntelQueries]);

  const saveIntel = async () => {
    setIntelBusy(true);
    try {
      const patch: Record<string, unknown> = {
        fofa_email: intelEmail.trim(),
        fofa_base_url: intelBaseURL.trim(),
        fofa_proxy: intelProxy.trim(),
        intel_daily_quota: Math.max(0, Number(intelQuota) || 0),
      };
      // 留空 = 不改 key（要清空就点"清除 Key"）。
      if (intelKeyInput.trim() !== "") patch.fofa_key = intelKeyInput.trim();
      const s2 = await saveSettings(patch);

      toast.success("情报源设置已保存");
      loadIntelQueries();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setIntelBusy(false);
    }
  };

  const clearIntelKey = async () => {
    setIntelBusy(true);
    try {
      const s2 = await saveSettings({ fofa_key: "" });

      toast.success("已清除情报源 API Key（情报查询工具会提示未配置）");
    } catch (e) {
      toast.error(`清除失败：${(e as Error).message}`);
    } finally {
      setIntelBusy(false);
    }
  };

  const testIntel = async () => {
    setIntelTesting(true);
    setIntelTestMsg("");
    try {
      const r = await api.testIntelSource("fofa");
      if (!r.ok) {
        setIntelTestMsg(`连接失败：${r.error ?? "未知错误"}`);
      } else {
        const info = r.info ?? {};
        const parts = ["连接成功"];
        if (info.email) parts.push(`账号 ${String(info.email)}`);
        if (typeof info.remain_free_point === "number") parts.push(`剩余额度 ${info.remain_free_point}`);
        if (typeof info.remain_api_query === "number") parts.push(`剩余查询 ${info.remain_api_query}`);
        parts.push(`平台侧今日 ${r.used_today ?? 0}/${r.daily_quota ?? 0} 次`);
        setIntelTestMsg(parts.join(" · "));
      }
      loadIntelQueries();
    } catch (e) {
      setIntelTestMsg(`连接失败：${(e as Error).message}`);
    } finally {
      setIntelTesting(false);
    }
  };

  const saveLimits = async () => {
    setSavingLimits(true);
    try {
      const s2 = await saveSettings({
        exec_cpu_seconds: Math.max(0, Number(execCPU) || 0),
        exec_memory_mb: Math.max(0, Number(execMem) || 0),
        exec_max_procs: Math.max(0, Number(execProcs) || 0),
        exec_max_active: Math.max(0, Number(execMaxActive) || 0),
        exec_max_age_minutes: Math.max(0, Number(execMaxAge) || 0),
        mcp_max_concurrent_per_server: Math.max(0, Number(mcpConcurrent) || 0),
        mcp_breaker_threshold: Math.max(0, Number(mcpBreakerFails) || 0),
        mcp_breaker_cooldown_sec: Math.max(0, Number(mcpBreakerCooldown) || 0),
      });

      toast.success("已保存：对之后提交的长任务执行与 MCP 调用生效");
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSavingLimits(false);
    }
  };

  const saveLogLevel = async () => {
    setSavingLogLevel(true);
    try {
      const s2 = await saveSettings({ log_level: logLevel });

      toast.success("日志级别已切换（立即生效）");
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSavingLogLevel(false);
    }
  };

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("并发数必须是大于 0 的整数");
      return;
    }
    setSavingWorkers(true);
    saveSettings({ workers: n })
      .then((s) => {
        toast.success("已保存并发数");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingWorkers(false));
  };

  const saveBoundedView = () => {
    setSavingBoundedView(true);
    saveSettings({ planner_bounded_view: boundedView })
      .then((s) => {
        toast.success(boundedView ? "已开启有界黑板视图（省 token）" : "已关闭有界视图：planner 每轮预取完整态势");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingBoundedView(false));
  };

  const saveTokenBudget = () => {
    const n = Number(tokenBudget.trim() === "" ? "0" : tokenBudget);
    if (!Number.isInteger(n) || n < 0) {
      toast.error("token 预算必须是不小于 0 的整数（0 = 关闭）");
      return;
    }
    setSavingBudget(true);
    saveSettings({ task_token_budget: n, task_token_budget_pause: tokenBudgetPause })
      .then((s) => {
        toast.success(n > 0 ? `已保存任务 token 预算（${n}）` : "已关闭任务 token 预算护栏");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingBudget(false));
  };

  const toggleTokenBudgetPause = (next: boolean) => {
    const prev = tokenBudgetPause;
    setTokenBudgetPause(next);
    setSavingBudget(true);
    saveSettings({ task_token_budget_pause: next })
      .then((s) => {
        toast.success(next ? "预算耗尽时将暂停任务" : "已改为仅告警，任务继续");
      })
      .catch((e) => {
        setTokenBudgetPause(prev);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingBudget(false));
  };

  const saveLLMBudget = () => {
    const budget = Number(llmBudgetUSD.trim() === "" ? "0" : llmBudgetUSD);
    const alertPct = Number(llmBudgetAlertPct.trim() === "" ? "80" : llmBudgetAlertPct);
    if (!Number.isFinite(budget) || budget < 0) {
      toast.error("月度预算必须是不小于 0 的数字（0 = 不设预算）");
      return;
    }
    if (!Number.isInteger(alertPct) || alertPct < 1 || alertPct > 100) {
      toast.error("告警阈值必须是 1–100 的整数");
      return;
    }
    setSavingLLMBudget(true);
    saveSettings({ llm_monthly_budget_usd: budget, llm_budget_alert_pct: alertPct })
      .then((s) => {
        toast.success(budget > 0 ? "已保存 LLM 月度预算" : "已关闭 LLM 月度预算告警");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingLLMBudget(false));
  };

  const savePython = () => {
    setSavingPython(true);
    saveSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        toast.success("已保存 Python 解释器配置");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingPython(false));
  };
  const detectPython = () => {
    setSavingPython(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSavingPython(false));
  };

  const refreshEgress = React.useCallback(() => {
    api
      .egressStatus()
      .then(setEgress)
      .catch(() => undefined);
  }, []);

  React.useEffect(() => {
    void loadSettings();
    refreshEgress();
    api
      .llmProfiles()
      .then(setLlmProfiles)
      .catch(() => undefined);
    return () => loadOwner.cancel();
  }, [loadSettings, loadOwner, refreshEgress]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSavingTraffic(true);
    saveSettings({ traffic_capture: v })
      .then(() => {
        toast.success(v ? "已开启流量捕获" : "已关闭流量捕获");
        refreshEgress();
      })
      .catch((e) => {
        setTrafficCapture(!v); // revert on failure
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingTraffic(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    saveSettings({ constraints_inject_planner: v })
      .then(() => undefined)
      .catch((e) => {
        setInjectPlanner(!v); // revert on failure
        toast.error(`保存失败：${(e as Error).message}`);
      });
  };

  // 授权范围两个设置一起保存：它们是同一条边界的两维（能打哪些目标 + 什么时候能打），
  // 分开存会让用户停在"改了一半"的状态上。拦截层每调用现读，所以改完即时生效。
  const saveScope = () => {
    setSavingScope(true);
    saveSettings({
      intercept_scope_mode: scopeMode,
      intercept_scope_hours: scopeHours.trim(),
      intercept_scope_hours_tz: scopeHoursTZ.trim(),
    })
      .then((next) => {
        toast.success("已保存授权范围设置");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingScope(false));
  };

  const saveArtifactLang = (next: "zh" | "en") => {
    const prev = artifactLang;
    setArtifactLang(next); // 乐观切换（与同页其它开关同一套纪律），失败回滚
    setSavingArtifactLang(true);
    saveSettings({ artifact_language: next })
      .then((s) => {
        toast.success(next === "en" ? "已切换：面向目标的产物一律英文" : "已切换：不约束产物语言（可用中文）");
      })
      .catch((e) => {
        setArtifactLang(prev);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingArtifactLang(false));
  };

  const saveCredBudget = () => {
    const n = Number(credBudget);
    if (!Number.isInteger(n) || n < 0 || n > 1000) {
      toast.error("凭据尝试预算必须是 0–1000 的整数（0 = 不限）");
      return;
    }
    const prevB = credBudget;
    const prevA = credBudgetAction;
    setSavingCredBudget(true);
    saveSettings({ credential_attempt_budget: n, credential_attempt_budget_action: credBudgetAction })
      .then((s) => {
        toast.success(n === 0 ? "已保存：不限制凭据尝试次数" : `已保存：同一账号超过 ${n} 次转「${credBudgetAction}」`);
      })
      .catch((e) => {
        setCredBudget(prevB);
        setCredBudgetAction(prevA);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingCredBudget(false));
  };

  const saveSiteName = () => {
    setSavingSiteName(true);
    saveSettings({ site_name: siteName.trim() })
      .then((next) => {
        const name = siteName.trim();
        if (name) document.title = name;
        toast.success(`已保存显示名${name ? "" : "（回到内置名）"}`);
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingSiteName(false));
  };

  const saveCors = () => {
    setSavingCors(true);
    saveSettings({ cors_allowed_origins: corsOrigins.trim() })
      .then((next) => {
        toast.success(corsOrigins.trim() ? "已保存跨源白名单（60 秒内生效）" : "已清空跨源白名单（不再返回 CORS 头）");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingCors(false));
  };

  const loadEnforcement = React.useCallback(() => {
    api
      .scopeEnforcement()
      .then(setEnforce)
      .catch(() => {
        /* 读不到就不显示状态块：它是提示，不该自己变成错误源 */
      });
  }, []);

  React.useEffect(() => {
    loadEnforcement();
  }, [loadEnforcement]);

  React.useEffect(() => {
    api
      .opsecKnowledge()
      .then(setOpsecKB)
      .catch(() => {
        /* 读不到就不显示这张清单：它是参考资料，不该自己变成错误源 */
      });
  }, []);

  const doKillSwitch = (on: boolean) => {
    setKillBusy(true);
    api
      .setKillSwitch(on, killReason.trim())
      .then((r) => {
        setEnforce((prev) =>
          prev ? { ...prev, kill_switch: r.kill_switch, paused_tasks: prev.paused_tasks + r.tasks_paused } : prev,
        );
        toast.success(r.note + (r.errors?.length ? `；${r.errors.length} 项失败` : ""));
        if (!on) setKillReason("");
        loadEnforcement();
      })
      .catch((e) => toast.error(`操作失败：${(e as Error).message}`))
      .finally(() => setKillBusy(false));
  };

  const doResumeAll = () => {
    setResumeBusy(true);
    api
      .resumeAllTasks()
      .then((r) => {
        toast.success(`已继续 ${r.resumed} 个任务${r.errors?.length ? `；${r.errors.length} 项失败` : ""}`);
        loadEnforcement();
      })
      .catch((e) => toast.error(`继续失败：${(e as Error).message}`))
      .finally(() => setResumeBusy(false));
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSavingAgentBinding(true);
    saveSettings({ agent_traffic_binding: v })
      .then((s) => {
        toast.success(v ? "已开启 Agent 自动绑定流量" : "已关闭 Agent 自动绑定流量");
        refreshEgress();
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingAgentBinding(false));
  };

  const toggleAgentLootExec = (v: boolean) => {
    setAgentLootExec(v); // optimistic
    setSavingLootExec(true);
    saveSettings({ agent_loot_exec: v })
      .then((s) => {
        toast.success(v ? "已开启战果直接执行" : "已关闭战果直接执行");
      })
      .catch((e) => {
        setAgentLootExec(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingLootExec(false));
  };

  const toggleAgentAutoRetest = (v: boolean) => {
    setAgentAutoRetest(v); // optimistic
    setSavingAutoRetest(true);
    saveSettings({ agent_auto_retest: v })
      .then((s) => {
        toast.success(v ? "已开启自动复测" : "已关闭自动复测");
      })
      .catch((e) => {
        setAgentAutoRetest(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingAutoRetest(false));
  };

  const toggleAttackChainContinuation = (v: boolean) => {
    setAttackChainContinuation(v); // optimistic
    setSavingAttackChain(true);
    saveSettings({ attack_chain_continuation: v })
      .then((s) => {
        toast.success(v ? "已开启攻击链续跳提醒" : "已关闭攻击链续跳提醒");
      })
      .catch((e) => {
        setAttackChainContinuation(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingAttackChain(false));
  };

  const toggleEmptyReportPatrol = (v: boolean) => {
    setEmptyReportPatrol(v); // optimistic
    setSavingEmptyReportPatrol(true);
    saveSettings({ empty_report_patrol: v })
      .then((s) => {
        toast.success(v ? "已开启空报告巡检" : "已关闭空报告巡检");
      })
      .catch((e) => {
        setEmptyReportPatrol(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingEmptyReportPatrol(false));
  };

  const toggleCredentialPlaintext = (v: boolean) => {
    setCredentialPlaintext(v); // optimistic
    setSavingCredentialPlaintext(true);
    saveSettings({ credential_plaintext_fidelity: v })
      .then((s) => {
        toast.success(v ? "已开启凭据明文保真" : "已关闭凭据明文保真（改为指纹 + 受控查看）");
      })
      .catch((e) => {
        setCredentialPlaintext(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingCredentialPlaintext(false));
  };

  const saveCompactorProfile = () => {
    const id = Number.parseInt(compactorProfileID, 10) || 0;
    setSavingCompactorProfile(true);
    saveSettings({ compactor_llm_profile_id: id })
      .then((s) => {
        toast.success(id > 0 ? "已保存压缩摘要模型" : "已改为跟随任务链/角色绑定");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingCompactorProfile(false));
  };

  const saveNoiseLevel = (v: string) => {
    const prev = noiseLevel;
    setNoiseLevel(v);
    setSavingOast(true);
    saveSettings({ opsec_noise_level: v })
      .then((s) => {
        toast.success(v === "quiet" ? "已切到静默模式（下一轮生效）" : "已切回常规模式");
      })
      .catch((e) => {
        setNoiseLevel(prev);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingOast(false));
  };

  const saveEvidenceVerify = (v: "off" | "warn" | "block") => {
    const prev = evidenceVerify;
    setEvidenceVerify(v);
    setSavingEvidenceVerify(true);
    saveSettings({ finding_evidence_verify: v })
      .then((next) => {
        toast.success(
          v === "block"
            ? "已开启证据拦截：critical/high 的关键证据不通过将被拒收"
            : v === "off"
              ? "已关闭证据质量闸"
              : "证据闸已切到告警模式",
        );
      })
      .catch((e) => {
        setEvidenceVerify(prev);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingEvidenceVerify(false));
  };

  const toggleOast = (v: boolean) => {
    setOastEnabled(v); // optimistic
    setSavingOast(true);
    saveSettings({ oast_enabled: v })
      .then(() => undefined)
      .catch((e) => {
        setOastEnabled(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingOast(false));
  };

  const saveOast = () => {
    setSavingOast(true);
    const payload: { oast_domain: string; oast_nuclei_server: string; oast_token?: string } = {
      oast_domain: oastDomain.trim() || "requestrepo.com",
      oast_nuclei_server: oastNucleiServer.trim() || "oast.fun",
    };
    // token 只写：输入为空时不动已存的值（想清除用 clearOastToken）。
    if (oastTokenInput.trim()) payload.oast_token = oastTokenInput.trim();
    saveSettings(payload)
      .then((s) => {
        toast.success("已保存 OAST 配置");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingOast(false));
  };

  const clearOastToken = () => {
    setSavingOast(true);
    saveSettings({ oast_token: "" })
      .then((s) => {
        toast.success("已清除 OAST token");
      })
      .catch((e) => toast.error(`清除失败：${(e as Error).message}`))
      .finally(() => setSavingOast(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    saveSettings({ constraints_inject_worker: v })
      .then(() => undefined)
      .catch((e) => {
        setInjectWorker(!v); // revert on failure
        toast.error(`保存失败：${(e as Error).message}`);
      });
  };

  const toggleChatAutoTitle = (v: boolean) => {
    setChatAutoTitle(v); // optimistic
    saveSettings({ chat_auto_title: v })
      .then((s) => {
        toast.success(v ? "已开启新会话自动标题" : "已关闭新会话自动标题");
      })
      .catch((e) => {
        setChatAutoTitle(!v); // revert on failure
        toast.error(`保存失败：${(e as Error).message}`);
      });
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    saveSettings({ noa_compaction: v })
      .then((s) => {
        toast.success(v ? "已开启 noa 上下文压缩（对之后启动的运行生效）" : "已关闭 noa 上下文压缩（恢复内置压缩）");
      })
      .catch((e) => {
        setNoaCompaction(!v); // revert on failure
        toast.error(`保存失败：${(e as Error).message}`);
      });
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSavingWebSearch(true);
    saveSettings(patch)
      .then((s) => {
        toast.success("已保存网络搜索配置");
      })
      .catch((e) => {
        toast.error(`保存失败：${(e as Error).message}`);
        api
          .settings()
          .then(() => undefined)
          .catch(() => undefined);
      })
      .finally(() => setSavingWebSearch(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    saveSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        toast.success("已保存 Brave API Key");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    saveSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        toast.success("已保存 Tavily API Key");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveSearxngURL = () => {
    setSavingSearxngURL(true);
    saveSettings({ web_search_searxng_url: searxngURL.trim() })
      .then(() => {
        toast.success(searxngURL.trim() ? "已保存 SearXNG 实例地址" : "已清除 SearXNG 实例地址");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingSearxngURL(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    saveSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        if (proxyInput.trim()) {
          toast.success("已保存独立搜索代理");
        } else if (s.web_search_proxy_mode === "egress_pool") {
          toast.success("已清除独立搜索代理（ddgs / brave-html 将自动跟随平台出口代理池）");
        } else if (s.web_search_proxy_mode === "proxy_blocked") {
          toast.warning("已清除独立搜索代理（平台出口代理池当前已阻断，ddgs / brave-html 暂不可用）");
        } else {
          toast.success("已清除独立搜索代理（当前无平台出口代理，将本机直连）");
        }
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingProxy(false));
  };

  // 只更新运行时状态，不回写输入框——手动刷新/轮询不能把用户正在编辑、尚未保存的
  // 代理池文本冲掉。
  const applyPoolStatus = (st: ProxyPoolStatusPayload) => {
    setProxyActive(st.active ?? "");
    setProxyBlocked(!!st.blocked);
    setProxyPinned(st.pinned ?? "");
    setProxyProbing(!!st.probing);
    setProxyStatus(st.candidates ?? []);
  };

  const applyProxyStatus = (s: Settings) => {
    setProxyActive(s.proxy_active ?? "");
    setProxyBlocked(!!s.proxy_blocked);
    setProxyPinned(s.proxy_pinned ?? "");
    setProxyProbing(!!s.proxy_probing);
    setProxyStatus(s.proxy_status ?? []);
    setClashStatus(s.clash_status ?? null);
  };

  // 代理健康探测是异步的。保存后轮询直到探测完成且收敛，不再在仅有 1 条结果时早退。
  const pollProxyStatus = (remaining: number, expectedCandidates?: number) => {
    if (remaining <= 0) return;
    window.setTimeout(() => {
      api
        .settings()
        .then((s) => {
          applyProxyStatus(s);
          const count = (s.proxy_status ?? []).length;
          const stillPending = !!s.proxy_probing || (expectedCandidates ? count < expectedCandidates : count === 0);
          if (stillPending && remaining > 1) pollProxyStatus(remaining - 1, expectedCandidates);
        })
        .catch(() => {
          // 下一次进入设置页会重新拉取；手动探测按钮也可重试。
        });
    }, 1500);
  };

  const parsePoolInput = () =>
    globalProxyPoolInput
      .split(/[\n,]/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#"));

  // 检测出口 IP：只经候选代理查询（后端不发起直连对比），结果显示每个候选的出口 IP；
  // 出口 IP 与本机网卡地址相同时后端标记 leaked。
  const checkProxyEgress = () => {
    const proxies = parsePoolInput();
    if (proxies.length === 0) {
      toast.error("先填写代理池（每行一个代理地址）");
      return;
    }
    setCheckingProxyEgress(true);
    api
      .proxyEgressIPs(proxies)
      .then((r) => {
        setProxyEgress(r.results ?? []);
        const leak = (r.results ?? []).filter((x) => x.leaked).length;
        if (leak > 0) {
          toast.error(`检测到 ${leak} 个代理的出口 IP 与本机地址相同，请检查 Clash 规则`);
        } else {
          toast.success("出口 IP 检测完成");
        }
      })
      .catch((e) => toast.error(`检测失败：${(e as Error).message}`))
      .finally(() => setCheckingProxyEgress(false));
  };

  const checkSingleProxyEgress = (proxy: string) => {
    setCheckingProxyEgress(true);
    api
      .proxyEgressIPs([proxy])
      .then((r) => {
        const res = r.results?.[0];
        if (!res) return;
        setProxyEgress((prev) => {
          const rest = prev.filter((item) => item.proxy !== res.proxy);
          return [...rest, res];
        });
        if (res.leaked) {
          toast.error(`代理 ${res.proxy} 出口 IP 与本机相同，疑似规则直连`);
        } else if (res.error) {
          toast.error(`检测失败：${res.error}`);
        } else {
          toast.success(`${res.proxy} 出口 IP：${res.ip ?? "-"}`);
        }
      })
      .catch((e) => toast.error(`检测失败：${(e as Error).message}`))
      .finally(() => setCheckingProxyEgress(false));
  };

  const refreshProxyStatus = () => {
    setProbingProxy(true);
    refreshEgress();
    api
      .proxyProbe()
      .then((res) => {
        applyPoolStatus(res.status);
        toast.success(res.status.blocked ? "探测完成：全部代理当前不可用" : "代理池实测完成");
      })
      .catch((e) => toast.error(`探测代理状态失败：${(e as Error).message}`))
      .finally(() => setProbingProxy(false));
  };

  const pinProxy = (proxy: string) => {
    setPinningProxy(proxy || "__unpin__");
    api
      .proxyPin(proxy)
      .then((res) => {
        applyPoolStatus(res.status);
        refreshEgress();
        toast.success(proxy ? `已固定出口为 ${proxy}` : "已取消固定，恢复自动策略调度");
      })
      .catch((e) => toast.error((e as Error).message || "切换失败"))
      .finally(() => setPinningProxy(""));
  };

  const saveGlobalProxy = () => {
    const timeout = Number.parseInt(proxyHealthTimeout, 10);
    const interval = Number.parseInt(proxyHealthInterval, 10);
    const threshold = Number.parseInt(proxyFailThreshold, 10);
    const cooldown = Number.parseInt(proxySwitchCooldown, 10);
    if (!Number.isFinite(timeout) || timeout <= 0 || timeout > 60) {
      toast.error("健康检查超时需在 1–60 秒之间");
      return;
    }
    if (!Number.isFinite(interval) || interval < 5 || interval > 600) {
      toast.error("检查间隔需在 5–600 秒之间");
      return;
    }
    if (!Number.isFinite(threshold) || threshold <= 0 || threshold > 10) {
      toast.error("失败阈值需在 1–10 之间");
      return;
    }
    if (!Number.isFinite(cooldown) || cooldown < 0 || cooldown > 600) {
      toast.error("切换冷却需在 0–600 秒之间");
      return;
    }
    setProxySaveError("");
    setSavingGlobalProxy(true);
    const expectedCount = parsePoolInput().length;
    saveSettings({
      global_proxy_pool: globalProxyPoolInput.trim(),
      proxy_health_url: proxyHealthURL.trim(),
      proxy_health_timeout_seconds: timeout,
      proxy_health_interval_seconds: interval,
      proxy_fail_threshold: threshold,
      proxy_switch_cooldown_seconds: cooldown,
      proxy_strategy: proxyStrategy,
      proxy_egress_ip_url: proxyEgressIPURL.trim(),
    })
      .then((s) => {
        const nextSaved = s.global_proxy_pool || s.global_proxy || "";
        setProxySavedPool(nextSaved);
        applyProxyStatus(s);
        toast.success(nextSaved ? "已保存代理池（支持无中断热更新）" : "已清空代理池（改为直连）");
        if (expectedCount > 0) pollProxyStatus(8, expectedCount);
        refreshEgress();
      })
      .catch((e) => {
        const msg = (e as Error).message || "未知错误";
        setProxySaveError(msg);
        toast.error(`保存失败：${msg}`);
      })
      .finally(() => setSavingGlobalProxy(false));
  };

  const saveClash = () => {
    const cooldown = Number.parseInt(clashCooldown, 10);
    if (!Number.isFinite(cooldown) || cooldown < 0 || cooldown > 600) {
      toast.error("切换冷却需在 0–600 秒之间");
      return;
    }
    setSavingClash(true);
    saveSettings({
      clash_api_url: clashApiURL.trim(),
      clash_proxy_group: clashGroup.trim(),
      clash_proxy_groups: clashGroups.trim(),
      clash_task_endpoints: clashTaskEndpoints.trim(),
      clash_auto_rotate: clashAuto,
      clash_rotate_cooldown_seconds: cooldown,
      ...(clashSecretInput.trim() ? { clash_api_secret: clashSecretInput.trim() } : {}),
    })
      .then((s) => {
        toast.success("已保存 Clash 出口切换配置");
        refreshEgress();
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message || "未知错误"}`))
      .finally(() => setSavingClash(false));
  };

  const rotateClash = () => {
    setRotatingClash(true);
    api
      .rotateEgress("设置页手动切换")
      .then((r) => {
        setClashStatus(r.status);
        refreshEgress();
        toast.success(
          r.rotation ? `${r.rotation.from} → ${r.rotation.to}（出口 IP ${r.rotation.egress_ip}）` : "已切换",
        );
      })
      .catch((e) => toast.error(`切换失败：${(e as Error).message || "未知错误"}`))
      .finally(() => setRotatingClash(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + SearXNG URL + entered key), bypassing cache server-side.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        web_search_searxng_url: searxngURL.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        setLastSearchTest(r);
        const msg = formatWebSearchTestToast(r);
        if (r.ok) toast.success(msg);
        else toast.error(msg);
      })
      .catch((e) => {
        const errMsg = (e as Error).message || "未知错误";
        setLastSearchTest({ ok: false, backend, error: errMsg });
        toast.error(`搜索测试失败：${errMsg}`);
      })
      .finally(() => setTesting(false));
  };

  // Missing required credential/URL for the selected primary backend.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;
  const tavilyNeedsKey = webSearch && backend === "tavily" && !tavilyKeySet;
  const searxngNeedsURL = webSearch && backend === "searxng" && searxngURL.trim() === "";

  return {
    loadError,
    loadingSettings,
    loadSettings,
    hasUnsavedChanges,
    trafficCapture,
    setTrafficCapture,
    agentTrafficBinding,
    setAgentTrafficBinding,
    webSearch,
    setWebSearch,
    backend,
    setBackend,
    braveKeySet,
    setBraveKeySet,
    braveKeyInput,
    setBraveKeyInput,
    tavilyKeySet,
    setTavilyKeySet,
    tavilyKeyInput,
    setTavilyKeyInput,
    savingTavilyKey,
    setSavingTavilyKey,
    proxyInput,
    setProxyInput,
    savingProxy,
    setSavingProxy,
    globalProxyPoolInput,
    setGlobalProxyPoolInput,
    proxySavedPool,
    proxyHealthURL,
    setProxyHealthURL,
    proxyHealthTimeout,
    setProxyHealthTimeout,
    proxyHealthInterval,
    setProxyHealthInterval,
    proxyFailThreshold,
    setProxyFailThreshold,
    proxySwitchCooldown,
    setProxySwitchCooldown,
    proxyStrategy,
    setProxyStrategy,
    proxyActive,
    proxyBlocked,
    proxyPinned,
    proxyProbing,
    probingProxy,
    pinningProxy,
    proxyStatus,
    proxySaveError,
    proxyEgressIPURL,
    setProxyEgressIPURL,
    proxyEgress,
    checkingProxyEgress,
    checkProxyEgress,
    checkSingleProxyEgress,
    refreshProxyStatus,
    pinProxy,
    savingGlobalProxy,
    setSavingGlobalProxy,
    clashApiURL,
    setClashApiURL,
    clashSecretInput,
    setClashSecretInput,
    clashSecretSet,
    clashGroup,
    setClashGroup,
    clashGroups,
    setClashGroups,
    clashTaskEndpoints,
    setClashTaskEndpoints,
    clashAuto,
    setClashAuto,
    clashCooldown,
    setClashCooldown,
    clashStatus,
    egress,
    refreshEgress,
    savingClash,
    rotatingClash,
    saveClash,
    rotateClash,
    testing,
    setTesting,
    loaded,
    setLoaded,
    section,
    setSection,
    savingTraffic,
    setSavingTraffic,
    savingAgentBinding,
    agentLootExec,
    setAgentLootExec,
    savingLootExec,
    toggleAgentLootExec,
    agentAutoRetest,
    setAgentAutoRetest,
    savingAutoRetest,
    toggleAgentAutoRetest,
    attackChainContinuation,
    setAttackChainContinuation,
    savingAttackChain,
    toggleAttackChainContinuation,
    emptyReportPatrol,
    setEmptyReportPatrol,
    savingEmptyReportPatrol,
    toggleEmptyReportPatrol,
    credentialPlaintext,
    setCredentialPlaintext,
    savingCredentialPlaintext,
    toggleCredentialPlaintext,
    compactorProfileID,
    setCompactorProfileID,
    llmProfiles,
    savingCompactorProfile,
    saveCompactorProfile,
    oastEnabled,
    oastTokenSet,
    noiseLevel,
    saveNoiseLevel,
    evidenceVerify,
    setEvidenceVerify,
    savingEvidenceVerify,
    saveEvidenceVerify,
    oastTokenInput,
    setOastTokenInput,
    oastDomain,
    setOastDomain,
    oastNucleiServer,
    setOastNucleiServer,
    savingOast,
    toggleOast,
    saveOast,
    clearOastToken,
    savingWebSearch,
    setSavingWebSearch,
    savingPython,
    setSavingPython,
    savingKey,
    setSavingKey,
    pyInterp,
    setPyInterp,
    workers,
    setWorkers,
    savingWorkers,
    setSavingWorkers,
    tokenBudget,
    setTokenBudget,
    tokenBudgetPause,
    setTokenBudgetPause,
    toggleTokenBudgetPause,
    savingBudget,
    setSavingBudget,
    llmBudgetUSD,
    setLLMBudgetUSD,
    llmBudgetAlertPct,
    setLLMBudgetAlertPct,
    savingLLMBudget,
    saveLLMBudget,
    boundedView,
    setBoundedView,
    savingBoundedView,
    saveBoundedView,
    execCPU,
    setExecCPU,
    execMem,
    setExecMem,
    execProcs,
    setExecProcs,
    execMaxActive,
    setExecMaxActive,
    execMaxAge,
    logLevel,
    setLogLevel,
    savingLogLevel,
    saveLogLevel,
    setExecMaxAge,
    mcpConcurrent,
    setMcpConcurrent,
    mcpBreakerFails,
    setMcpBreakerFails,
    mcpBreakerCooldown,
    setMcpBreakerCooldown,
    savingLimits,
    setSavingLimits,
    intelKeyInput,
    setIntelKeyInput,
    intelKeySet,
    setIntelKeySet,
    intelEmail,
    setIntelEmail,
    intelBaseURL,
    setIntelBaseURL,
    intelProxy,
    setIntelProxy,
    intelQuota,
    setIntelQuota,
    intelUsed,
    setIntelUsed,
    intelBusy,
    setIntelBusy,
    intelTesting,
    setIntelTesting,
    intelTestMsg,
    setIntelTestMsg,
    intelRows,
    setIntelRows,
    injectPlanner,
    setInjectPlanner,
    injectWorker,
    setInjectWorker,
    noaCompaction,
    chatAutoTitle,
    toggleChatAutoTitle,
    setNoaCompaction,
    scopeMode,
    setScopeMode,
    scopeModeEnv,
    setScopeModeEnv,
    scopeHours,
    setScopeHours,
    scopeHoursTZ,
    setScopeHoursTZ,
    serverLocalTime,
    serverLocalZone,
    savingScope,
    setSavingScope,
    artifactLang,
    setArtifactLang,
    savingArtifactLang,
    setSavingArtifactLang,
    credBudget,
    setCredBudget,
    credBudgetAction,
    setCredBudgetAction,
    savingCredBudget,
    setSavingCredBudget,
    siteName,
    setSiteName,
    savingSiteName,
    setSavingSiteName,
    corsOrigins,
    setCorsOrigins,
    savingCors,
    saveCors,
    enforce,
    setEnforce,
    killReason,
    setKillReason,
    killBusy,
    setKillBusy,
    opsecKB,
    setOpsecKB,
    resumeBusy,
    setResumeBusy,
    sendMode,
    apply,
    loadIntelQueries,
    saveIntel,
    clearIntelKey,
    testIntel,
    saveLimits,
    saveWorkers,
    saveTokenBudget,
    savePython,
    detectPython,
    toggleTraffic,
    toggleInjectPlanner,
    saveScope,
    saveArtifactLang,
    saveCredBudget,
    saveSiteName,
    loadEnforcement,
    doKillSwitch,
    doResumeAll,
    toggleAgentTrafficBinding,
    toggleInjectWorker,
    toggleNoaCompaction,
    searxngURL,
    setSearxngURL,
    savingSearxngURL,
    saveSearxngURL,
    webSearchProxyMode,
    webSearchStats,
    lastSearchTest,
    saveWebSearch,
    saveBraveKey,
    saveTavilyKey,
    saveProxy,
    saveGlobalProxy,
    runTest,
    braveNeedsKey,
    tavilyNeedsKey,
    searxngNeedsURL,
  };
}
