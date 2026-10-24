"use client";

import * as React from "react";

import { toast } from "sonner";

import { api } from "@/lib/api";
import { useChatSendMode } from "@/lib/chat-send-mode";
import type {
  CustomRule,
  ExplorationPolicyDefault,
  IntelQueryRow,
  OpsecKnowledgeResponse,
  ProxyEgressResult,
  ProxyPoolHealth,
  ScopeEnforcement,
  Settings,
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

export function useSettingsState() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [agentTrafficBinding, setAgentTrafficBinding] = React.useState(false);
  const [agentLootExec, setAgentLootExec] = React.useState(false);
  const [agentAutoRetest, setAgentAutoRetest] = React.useState(false);
  const [oastEnabled, setOastEnabled] = React.useState(false);
  const [oastTokenSet, setOastTokenSet] = React.useState(false);
  const [oastTokenInput, setOastTokenInput] = React.useState("");
  const [oastDomain, setOastDomain] = React.useState("requestrepo.com");
  const [noiseLevel, setNoiseLevel] = React.useState("normal");
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  // 代理池：每行一个候选（Clash 混合端口即可）；健康检查/切换在服务端运行。
  const [globalProxyPoolInput, setGlobalProxyPoolInput] = React.useState("");
  // 已保存的代理池（用于区分"输入了但还没保存"与"正在检测已保存的配置"）。
  const [proxySavedPool, setProxySavedPool] = React.useState("");
  const [proxyHealthURL, setProxyHealthURL] = React.useState("");
  const [proxyHealthTimeout, setProxyHealthTimeout] = React.useState("5");
  const [proxyFailThreshold, setProxyFailThreshold] = React.useState("2");
  const [proxyActive, setProxyActive] = React.useState("");
  const [proxyBlocked, setProxyBlocked] = React.useState(false);
  const [proxyStatus, setProxyStatus] = React.useState<ProxyPoolHealth[]>([]);
  const [proxyEgressIPURL, setProxyEgressIPURL] = React.useState("https://api.ipify.org");
  const [proxyEgress, setProxyEgress] = React.useState<ProxyEgressResult[]>([]);
  const [checkingProxyEgress, setCheckingProxyEgress] = React.useState(false);
  const [proxySaveError, setProxySaveError] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  // 分区导航：一次只渲染一个分区（照 CyberStrikeAI 的设置页骨架）。
  const [section, setSection] = React.useState<SectionId>("basic");
  // 曾经的共享 `saving` 会让流量捕获 / 网络搜索 / Python 三处互相禁用控件——按用途拆开。
  const [savingTraffic, setSavingTraffic] = React.useState(false);
  const [savingLootExec, setSavingLootExec] = React.useState(false);
  const [savingAutoRetest, setSavingAutoRetest] = React.useState(false);
  const [savingOast, setSavingOast] = React.useState(false);
  const [savingWebSearch, setSavingWebSearch] = React.useState(false);
  const [savingPython, setSavingPython] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 任务级 token 预算(0 = 关闭)。
  const [tokenBudget, setTokenBudget] = React.useState("0");
  const [savingBudget, setSavingBudget] = React.useState(false);
  // P24 执行资源限额与外部 MCP 守卫（0 = 不限；保存即对之后提交的执行/调用生效）。
  const [execCPU, setExecCPU] = React.useState("1800");
  const [execMem, setExecMem] = React.useState("0");
  const [execProcs, setExecProcs] = React.useState("0");
  const [execMaxActive, setExecMaxActive] = React.useState("8");
  // 单条托管执行的最大存活分钟数（默认 120，0 = 不限）。
  const [execMaxAge, setExecMaxAge] = React.useState("120");
  const [mcpConcurrent, setMcpConcurrent] = React.useState("2");
  const [mcpBreakerFails, setMcpBreakerFails] = React.useState("5");
  const [mcpBreakerCooldown, setMcpBreakerCooldown] = React.useState("60");
  const [savingLimits, setSavingLimits] = React.useState(false);
  // P21 情报源（先接 FOFA）。key 只写不读：页面只显示"是否已配置"，输入框留空 = 不改。
  const [intelKeyInput, setIntelKeyInput] = React.useState("");
  const [intelKeySet, setIntelKeySet] = React.useState(false);
  const [intelEmail, setIntelEmail] = React.useState("");
  const [intelBaseURL, setIntelBaseURL] = React.useState("");
  const [intelProxy, setIntelProxy] = React.useState("");
  const [intelQuota, setIntelQuota] = React.useState("200");
  const [intelUsed, setIntelUsed] = React.useState(0);
  const [intelBusy, setIntelBusy] = React.useState(false);
  const [intelTesting, setIntelTesting] = React.useState(false);
  const [intelTestMsg, setIntelTestMsg] = React.useState("");
  const [intelRows, setIntelRows] = React.useState<IntelQueryRow[]>([]);
  // P20 探索策略的全局默认（新建任务继承；已存在的任务各自持有快照，不被回溯）。
  const [polDefault, setPolDefault] = React.useState<ExplorationPolicyDefault | null>(null);
  const [polTech, setPolTech] = React.useState<string[]>([]);
  const [polWrites, setPolWrites] = React.useState<string[]>([]);
  const [polCustom, setPolCustom] = React.useState<CustomRule[]>([]);
  const [savingPol, setSavingPol] = React.useState(false);
  // 操作约束注入范围(默认都开)。
  const [injectPlanner, setInjectPlanner] = React.useState(true);
  const [injectWorker, setInjectWorker] = React.useState(true);
  // 实验功能:noa 上下文压缩(默认关)。
  const [noaCompaction, setNoaCompaction] = React.useState(false);
  // 授权范围（RoE）：空间门模式 + 作业时间窗。模式回生效值，另存一个 env 覆盖标志。
  const [scopeMode, setScopeMode] = React.useState("ask");
  const [scopeModeEnv, setScopeModeEnv] = React.useState(false);
  const [scopeHours, setScopeHours] = React.useState("");
  const [savingScope, setSavingScope] = React.useState(false);
  // P41 面向目标的产物语言（默认 zh = 不约束）。与界面语言是两件事，刻意分开放。
  const [artifactLang, setArtifactLang] = React.useState<"zh" | "en">("zh");
  const [savingArtifactLang, setSavingArtifactLang] = React.useState(false);
  // P43 凭据尝试预算：窗口内对同一 (目标, 账号) 允许试几次（0 = 不限），超了怎么处置。
  const [credBudget, setCredBudget] = React.useState("3");
  const [credBudgetAction, setCredBudgetAction] = React.useState<"allow" | "ask" | "deny">("ask");
  const [savingCredBudget, setSavingCredBudget] = React.useState(false);
  // P42 显示名（空 = 内置 BLADE）与 CORS 来源白名单（空 = 不发 CORS 头）。
  const [siteName, setSiteName] = React.useState("");
  const [savingSiteName, setSavingSiteName] = React.useState(false);
  const [corsOrigins, setCorsOrigins] = React.useState("");
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

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setAgentTrafficBinding(!!s.agent_traffic_binding);
    setAgentLootExec(!!s.agent_loot_exec);
    setAgentAutoRetest(!!s.agent_auto_retest);
    setOastEnabled(!!s.oast_enabled);
    setOastTokenSet(!!s.oast_token_set);
    setOastDomain(s.oast_domain || "requestrepo.com");
    setNoiseLevel(s.opsec_noise_level === "quiet" ? "quiet" : "normal");
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    const savedPool = s.global_proxy_pool || s.global_proxy || "";
    setGlobalProxyPoolInput(savedPool);
    setProxySavedPool(savedPool);
    setProxyHealthURL(s.proxy_health_url || "https://www.gstatic.com/generate_204");
    setProxyHealthTimeout(String(s.proxy_health_timeout_seconds ?? 5));
    setProxyFailThreshold(String(s.proxy_fail_threshold ?? 2));
    setProxyEgressIPURL(s.proxy_egress_ip_url || "https://api.ipify.org");
    setProxyActive(s.proxy_active ?? "");
    setProxyBlocked(!!s.proxy_blocked);
    setProxyStatus(s.proxy_status ?? []);
    setPyInterp(s.python_interpreter ?? "");
    setWorkers(String(s.workers ?? 3));
    setTokenBudget(String(s.task_token_budget ?? 0));
    setInjectPlanner(s.constraints_inject_planner !== false);
    setInjectWorker(s.constraints_inject_worker !== false);
    setNoaCompaction(!!s.noa_compaction);
    setScopeMode(s.intercept_scope_mode ?? "ask");
    setScopeModeEnv(!!s.intercept_scope_mode_env);
    setScopeHours(s.intercept_scope_hours ?? "");
    setArtifactLang(s.artifact_language === "en" ? "en" : "zh");
    setCredBudget(String(s.credential_attempt_budget ?? 3));
    setCredBudgetAction(s.credential_attempt_budget_action ?? "ask");
    setSiteName(s.site_name ?? "");
    setCorsOrigins(s.cors_allowed_origins ?? "");
    setIntelKeySet(!!s.fofa_key_set);
    setIntelEmail(s.fofa_email ?? "");
    setIntelBaseURL(s.fofa_base_url ?? "");
    setIntelProxy(s.fofa_proxy ?? "");
    setIntelQuota(String(s.intel_daily_quota ?? 200));
    setIntelUsed(s.intel_used_today ?? 0);
    setExecCPU(String(s.exec_cpu_seconds ?? 1800));
    setExecMem(String(s.exec_memory_mb ?? 0));
    setExecProcs(String(s.exec_max_procs ?? 0));
    setExecMaxActive(String(s.exec_max_active ?? 8));
    setExecMaxAge(String(s.exec_max_age_minutes ?? 120));
    setMcpConcurrent(String(s.mcp_max_concurrent_per_server ?? 2));
    setMcpBreakerFails(String(s.mcp_breaker_threshold ?? 5));
    setMcpBreakerCooldown(String(s.mcp_breaker_cooldown_sec ?? 60));
  }, []);

  // 全局默认策略：独立加载（不进 apply——它由 /api/settings 触发，而策略走自己的端点）。
  React.useEffect(() => {
    let alive = true;
    void api
      .explorationPolicyDefault()
      .then((pd) => {
        if (!alive) return;
        setPolDefault(pd);
        setPolTech([...pd.effective.techniques]);
        setPolWrites([...pd.effective.forbidden_writes]);
        setPolCustom((pd.effective.custom ?? []).map((r) => ({ ...r })));
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  const savePolicyDefault = async () => {
    setSavingPol(true);
    try {
      // 全局默认 = 出厂默认 + 这里的覆盖；两轴显式提交，避免"看着没禁、其实继承着什么"。
      const r = await api.saveExplorationPolicyDefault({
        techniques: polTech,
        forbidden_writes: polWrites,
        custom: polCustom.filter((x) => x.pattern.trim() !== ""),
      });
      toast.success(`探索策略全局默认已更新（第 ${r.version} 版）：只影响之后新建的任务`);
      const pd = await api.explorationPolicyDefault();
      setPolDefault(pd);
      setPolTech([...pd.effective.techniques]);
      setPolWrites([...pd.effective.forbidden_writes]);
      setPolCustom((pd.effective.custom ?? []).map((x) => ({ ...x })));
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSavingPol(false);
    }
  };

  const togglePolicyChip = (list: string[], set: (v: string[]) => void, id: string) => {
    set(list.includes(id) ? list.filter((x) => x !== id) : [...list, id]);
  };

  // 情报台账（用量 + 排障）：进页面拉一次，保存/测试后再刷新。
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
      const s2 = await api.setSettings(patch);
      apply(s2);
      setIntelKeyInput("");
      toast.success("情报源设置已保存");
      loadIntelQueries();
    } catch (e) {
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setIntelBusy(false);
    }
  };

  const clearIntelKey = async () => {
    setIntelBusy(true);
    try {
      const s2 = await api.setSettings({ fofa_key: "" });
      apply(s2);
      setIntelKeyInput("");
      toast.success("已清除情报源 API Key（情报查询工具会提示未配置）");
    } catch (e) {
      toast.error("清除失败：" + (e as Error).message);
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
        setIntelTestMsg("连接失败：" + (r.error ?? "未知错误"));
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
      setIntelTestMsg("连接失败：" + (e as Error).message);
    } finally {
      setIntelTesting(false);
    }
  };

  const saveLimits = async () => {
    setSavingLimits(true);
    try {
      const s2 = await api.setSettings({
        exec_cpu_seconds: Math.max(0, Number(execCPU) || 0),
        exec_memory_mb: Math.max(0, Number(execMem) || 0),
        exec_max_procs: Math.max(0, Number(execProcs) || 0),
        exec_max_active: Math.max(0, Number(execMaxActive) || 0),
        exec_max_age_minutes: Math.max(0, Number(execMaxAge) || 0),
        mcp_max_concurrent_per_server: Math.max(0, Number(mcpConcurrent) || 0),
        mcp_breaker_threshold: Math.max(0, Number(mcpBreakerFails) || 0),
        mcp_breaker_cooldown_sec: Math.max(0, Number(mcpBreakerCooldown) || 0),
      });
      apply(s2);
      toast.success("已保存：对之后提交的长任务执行与 MCP 调用生效");
    } catch (e) {
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setSavingLimits(false);
    }
  };

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("并发数必须是大于 0 的整数");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("已保存并发工作 agent 数（对之后启动的任务生效）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const saveTokenBudget = () => {
    const n = Number(tokenBudget.trim() === "" ? "0" : tokenBudget);
    if (!Number.isInteger(n) || n < 0) {
      toast.error("token 预算必须是不小于 0 的整数（0 = 关闭）");
      return;
    }
    setSavingBudget(true);
    api
      .setSettings({ task_token_budget: n })
      .then((s) => {
        apply(s);
        toast.success(n > 0 ? `已保存任务 token 预算（${n}）` : "已关闭任务 token 预算护栏");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingBudget(false));
  };

  const savePython = () => {
    setSavingPython(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("已保存 Python 解释器配置");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
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

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSavingTraffic(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch((e) => {
        setTrafficCapture(!v); // revert on failure
        toast.error("保存失败：" + (e as Error).message);
      })
      .finally(() => setSavingTraffic(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch((e) => {
        setInjectPlanner(!v); // revert on failure
        toast.error("保存失败：" + (e as Error).message);
      });
  };

  // 授权范围两个设置一起保存：它们是同一条边界的两维（能打哪些目标 + 什么时候能打），
  // 分开存会让用户停在"改了一半"的状态上。拦截层每调用现读，所以改完即时生效。
  const saveScope = () => {
    setSavingScope(true);
    api
      .setSettings({ intercept_scope_mode: scopeMode, intercept_scope_hours: scopeHours.trim() })
      .then((next) => {
        apply(next);
        toast.success("已保存授权范围设置");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingScope(false));
  };

  const saveArtifactLang = (next: "zh" | "en") => {
    const prev = artifactLang;
    setArtifactLang(next); // 乐观切换（与同页其它开关同一套纪律），失败回滚
    setSavingArtifactLang(true);
    api
      .setSettings({ artifact_language: next })
      .then((s) => {
        apply(s);
        toast.success(next === "en" ? "已切换：面向目标的产物一律英文" : "已切换：不约束产物语言（可用中文）");
      })
      .catch((e) => {
        setArtifactLang(prev);
        toast.error("保存失败：" + (e as Error).message);
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
    api
      .setSettings({ credential_attempt_budget: n, credential_attempt_budget_action: credBudgetAction })
      .then((s) => {
        apply(s);
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
    api
      .setSettings({ site_name: siteName.trim() })
      .then((next) => {
        apply(next);
        const name = siteName.trim();
        if (name) document.title = name;
        toast.success("已保存显示名" + (name ? "" : "（回到内置名）"));
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingSiteName(false));
  };

  const saveCors = () => {
    setSavingCors(true);
    api
      .setSettings({ cors_allowed_origins: corsOrigins.trim() })
      .then((next) => {
        apply(next);
        toast.success(corsOrigins.trim() ? "已保存跨源白名单（60 秒内生效）" : "已清空跨源白名单（不再返回 CORS 头）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
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
      .catch((e) => toast.error("操作失败：" + (e as Error).message))
      .finally(() => setKillBusy(false));
  };

  const doResumeAll = () => {
    setResumeBusy(true);
    api
      .resumeAllTasks()
      .then((r) => {
        toast.success(`已继续 ${r.resumed} 个任务` + (r.errors?.length ? `；${r.errors.length} 项失败` : ""));
        loadEnforcement();
      })
      .catch((e) => toast.error("继续失败：" + (e as Error).message))
      .finally(() => setResumeBusy(false));
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSavingTraffic(true);
    api
      .setSettings({ agent_traffic_binding: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启 Agent 自动绑定流量" : "已关闭 Agent 自动绑定流量");
      })
      .catch((e) => {
        setAgentTrafficBinding(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingTraffic(false));
  };

  const toggleAgentLootExec = (v: boolean) => {
    setAgentLootExec(v); // optimistic
    setSavingLootExec(true);
    api
      .setSettings({ agent_loot_exec: v })
      .then((s) => {
        apply(s);
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
    api
      .setSettings({ agent_auto_retest: v })
      .then((s) => {
        apply(s);
        toast.success(v ? "已开启自动复测" : "已关闭自动复测");
      })
      .catch((e) => {
        setAgentAutoRetest(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingAutoRetest(false));
  };

  const saveNoiseLevel = (v: string) => {
    const prev = noiseLevel;
    setNoiseLevel(v);
    setSavingOast(true);
    api
      .setSettings({ opsec_noise_level: v })
      .then((s) => {
        apply(s);
        toast.success(v === "quiet" ? "已切到静默模式（下一轮生效）" : "已切回常规模式");
      })
      .catch((e) => {
        setNoiseLevel(prev);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingOast(false));
  };

  const toggleOast = (v: boolean) => {
    setOastEnabled(v); // optimistic
    setSavingOast(true);
    api
      .setSettings({ oast_enabled: v })
      .then(apply)
      .catch((e) => {
        setOastEnabled(!v);
        toast.error(`保存失败：${(e as Error).message}`);
      })
      .finally(() => setSavingOast(false));
  };

  const saveOast = () => {
    setSavingOast(true);
    const payload: { oast_domain: string; oast_token?: string } = {
      oast_domain: oastDomain.trim() || "requestrepo.com",
    };
    // token 只写：输入为空时不动已存的值（想清除用 clearOastToken）。
    if (oastTokenInput.trim()) payload.oast_token = oastTokenInput.trim();
    api
      .setSettings(payload)
      .then((s) => {
        apply(s);
        setOastTokenInput("");
        toast.success("已保存 OAST 配置");
      })
      .catch((e) => toast.error(`保存失败：${(e as Error).message}`))
      .finally(() => setSavingOast(false));
  };

  const clearOastToken = () => {
    setSavingOast(true);
    api
      .setSettings({ oast_token: "" })
      .then((s) => {
        apply(s);
        toast.success("已清除 OAST token");
      })
      .catch((e) => toast.error(`清除失败：${(e as Error).message}`))
      .finally(() => setSavingOast(false));
  };

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch((e) => {
        setInjectWorker(!v); // revert on failure
        toast.error("保存失败：" + (e as Error).message);
      });
  };

  const toggleNoaCompaction = (v: boolean) => {
    setNoaCompaction(v); // optimistic
    api
      .setSettings({ noa_compaction: v })
      .then((s) => {
        apply(s);
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
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("已保存网络搜索配置");
      })
      .catch((e) => {
        toast.error("保存失败：" + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSavingWebSearch(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("已保存 Brave API Key");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("已保存 Tavily API Key");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "已保存出口代理" : "已清除出口代理（改为直连）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  // 只更新运行时状态，不回写输入框——手动刷新/轮询不能把用户正在编辑、尚未保存的
  // 代理池文本冲掉。
  const applyProxyStatus = (s: Settings) => {
    setProxyActive(s.proxy_active ?? "");
    setProxyBlocked(!!s.proxy_blocked);
    setProxyStatus(s.proxy_status ?? []);
  };

  // 代理健康探测是异步的。保存后轮询几次状态，让"正在检测代理…"能自然收敛；
  // 探测较慢（多个候选 × 超时）时也不会让页面一直停在检测中而不给操作入口。
  const pollProxyStatus = (remaining: number) => {
    if (remaining <= 0) return;
    window.setTimeout(() => {
      api
        .settings()
        .then((s) => {
          applyProxyStatus(s);
          if ((s.proxy_status ?? []).length === 0 && remaining > 1) pollProxyStatus(remaining - 1);
        })
        .catch(() => {
          // 下一次进入设置页会重新拉取；手动刷新按钮也可重试。
        });
    }, 2000);
  };

  // 检测出口 IP：只经候选代理查询（后端不发起直连对比），结果显示每个候选的出口 IP；
  // 出口 IP 与本机网卡地址相同时后端标记 leaked。
  const checkProxyEgress = () => {
    const proxies = globalProxyPoolInput
      .split(/[\n,]/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#"));
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
      .catch((e) => toast.error("检测失败：" + (e as Error).message))
      .finally(() => setCheckingProxyEgress(false));
  };

  const refreshProxyStatus = () => {
    api
      .settings()
      .then(applyProxyStatus)
      .catch((e) => toast.error("刷新代理状态失败：" + (e as Error).message));
  };

  const saveGlobalProxy = () => {
    const timeout = Number.parseInt(proxyHealthTimeout, 10);
    const threshold = Number.parseInt(proxyFailThreshold, 10);
    if (!Number.isFinite(timeout) || timeout <= 0) {
      toast.error("健康检查超时必须大于 0 秒");
      return;
    }
    if (!Number.isFinite(threshold) || threshold <= 0) {
      toast.error("失败阈值必须大于 0");
      return;
    }
    setProxySaveError("");
    setSavingGlobalProxy(true);
    api
      .setSettings({
        global_proxy_pool: globalProxyPoolInput.trim(),
        proxy_health_url: proxyHealthURL.trim(),
        proxy_health_timeout_seconds: timeout,
        proxy_fail_threshold: threshold,
        proxy_egress_ip_url: proxyEgressIPURL.trim(),
      })
      .then((s) => {
        apply(s);
        toast.success(globalProxyPoolInput.trim() ? "已保存代理池" : "已清空代理池（改为直连）");
        pollProxyStatus(6);
      })
      .catch((e) => {
        const msg = (e as Error).message || "未知错误";
        setProxySaveError(msg);
        toast.error("保存失败：" + msg);
      })
      .finally(() => setSavingGlobalProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`搜索测试成功 · ${r.backend} 返回 ${r.count} 条结果`);
        else toast.error("搜索测试失败：" + (r.error || "未知错误"));
      })
      .catch((e) => toast.error("搜索测试失败：" + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return {
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
    proxyFailThreshold,
    setProxyFailThreshold,
    proxyActive,
    proxyBlocked,
    proxyStatus,
    proxySaveError,
    proxyEgressIPURL,
    setProxyEgressIPURL,
    proxyEgress,
    checkingProxyEgress,
    checkProxyEgress,
    refreshProxyStatus,
    savingGlobalProxy,
    setSavingGlobalProxy,
    testing,
    setTesting,
    loaded,
    setLoaded,
    section,
    setSection,
    savingTraffic,
    setSavingTraffic,
    agentLootExec,
    setAgentLootExec,
    savingLootExec,
    toggleAgentLootExec,
    agentAutoRetest,
    setAgentAutoRetest,
    savingAutoRetest,
    toggleAgentAutoRetest,
    oastEnabled,
    oastTokenSet,
    noiseLevel,
    saveNoiseLevel,
    oastTokenInput,
    setOastTokenInput,
    oastDomain,
    setOastDomain,
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
    savingBudget,
    setSavingBudget,
    execCPU,
    setExecCPU,
    execMem,
    setExecMem,
    execProcs,
    setExecProcs,
    execMaxActive,
    setExecMaxActive,
    execMaxAge,
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
    polDefault,
    setPolDefault,
    polTech,
    setPolTech,
    polWrites,
    setPolWrites,
    polCustom,
    setPolCustom,
    savingPol,
    setSavingPol,
    injectPlanner,
    setInjectPlanner,
    injectWorker,
    setInjectWorker,
    noaCompaction,
    setNoaCompaction,
    scopeMode,
    setScopeMode,
    scopeModeEnv,
    setScopeModeEnv,
    scopeHours,
    setScopeHours,
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
    savePolicyDefault,
    togglePolicyChip,
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
    saveWebSearch,
    saveBraveKey,
    saveTavilyKey,
    saveProxy,
    saveGlobalProxy,
    runTest,
    braveNeedsKey,
  };
}
