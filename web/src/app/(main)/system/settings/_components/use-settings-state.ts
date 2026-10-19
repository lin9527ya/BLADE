"use client";

import * as React from "react";

import { api } from "@/lib/api";
import { useChatSendMode } from "@/lib/chat-send-mode";
import {
  CustomRule,
  ExplorationPolicyDefault,
  IntelQueryRow,
  OpsecKnowledgeResponse,
  ScopeEnforcement,
  Settings,
} from "@/lib/types";
import { toast } from "sonner";

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
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [globalProxyInput, setGlobalProxyInput] = React.useState("");
  const [savingGlobalProxy, setSavingGlobalProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  // 分区导航：一次只渲染一个分区（照 CyberStrikeAI 的设置页骨架）。
  const [section, setSection] = React.useState<SectionId>("basic");
  // 曾经的共享 `saving` 会让流量捕获 / 网络搜索 / Python 三处互相禁用控件——按用途拆开。
  const [savingTraffic, setSavingTraffic] = React.useState(false);
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
  // P42 显示名（空 = 内置 BLADE）。
  const [siteName, setSiteName] = React.useState("");
  const [savingSiteName, setSavingSiteName] = React.useState(false);
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
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy ?? "");
    setGlobalProxyInput(s.global_proxy ?? "");
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
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSavingTraffic(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
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

  const toggleInjectWorker = (v: boolean) => {
    setInjectWorker(v); // optimistic
    api
      .setSettings({ constraints_inject_worker: v })
      .then(apply)
      .catch(() => setInjectWorker(!v)); // revert on failure
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

  const saveGlobalProxy = () => {
    setSavingGlobalProxy(true);
    api
      .setSettings({ global_proxy: globalProxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(globalProxyInput.trim() ? "已保存全局代理" : "已清除全局代理（改为直连）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
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
    trafficCapture, setTrafficCapture, agentTrafficBinding, setAgentTrafficBinding, webSearch, setWebSearch, backend, setBackend, braveKeySet, setBraveKeySet, braveKeyInput, setBraveKeyInput, tavilyKeySet, setTavilyKeySet, tavilyKeyInput, setTavilyKeyInput, savingTavilyKey, setSavingTavilyKey, proxyInput, setProxyInput, savingProxy, setSavingProxy, globalProxyInput, setGlobalProxyInput, savingGlobalProxy, setSavingGlobalProxy, testing, setTesting, loaded, setLoaded, section, setSection, savingTraffic, setSavingTraffic, savingWebSearch, setSavingWebSearch, savingPython, setSavingPython, savingKey, setSavingKey, pyInterp, setPyInterp, workers, setWorkers, savingWorkers, setSavingWorkers, tokenBudget, setTokenBudget, savingBudget, setSavingBudget, execCPU, setExecCPU, execMem, setExecMem, execProcs, setExecProcs, execMaxActive, setExecMaxActive, mcpConcurrent, setMcpConcurrent, mcpBreakerFails, setMcpBreakerFails, mcpBreakerCooldown, setMcpBreakerCooldown, savingLimits, setSavingLimits, intelKeyInput, setIntelKeyInput, intelKeySet, setIntelKeySet, intelEmail, setIntelEmail, intelBaseURL, setIntelBaseURL, intelProxy, setIntelProxy, intelQuota, setIntelQuota, intelUsed, setIntelUsed, intelBusy, setIntelBusy, intelTesting, setIntelTesting, intelTestMsg, setIntelTestMsg, intelRows, setIntelRows, polDefault, setPolDefault, polTech, setPolTech, polWrites, setPolWrites, polCustom, setPolCustom, savingPol, setSavingPol, injectPlanner, setInjectPlanner, injectWorker, setInjectWorker, noaCompaction, setNoaCompaction, scopeMode, setScopeMode, scopeModeEnv, setScopeModeEnv, scopeHours, setScopeHours, savingScope, setSavingScope, artifactLang, setArtifactLang, savingArtifactLang, setSavingArtifactLang, credBudget, setCredBudget, credBudgetAction, setCredBudgetAction, savingCredBudget, setSavingCredBudget, siteName, setSiteName, savingSiteName, setSavingSiteName, enforce, setEnforce, killReason, setKillReason, killBusy, setKillBusy, opsecKB, setOpsecKB, resumeBusy, setResumeBusy, sendMode, apply, savePolicyDefault, togglePolicyChip, loadIntelQueries, saveIntel, clearIntelKey, testIntel, saveLimits, saveWorkers, saveTokenBudget, savePython, detectPython, toggleTraffic, toggleInjectPlanner, saveScope, saveArtifactLang, saveCredBudget, saveSiteName, loadEnforcement, doKillSwitch, doResumeAll, toggleAgentTrafficBinding, toggleInjectWorker, toggleNoaCompaction, saveWebSearch, saveBraveKey, saveTavilyKey, saveProxy, saveGlobalProxy, runTest, braveNeedsKey
  };
}
