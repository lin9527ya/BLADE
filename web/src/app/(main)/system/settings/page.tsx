"use client";

import * as React from "react";

import { CpuIcon, FlaskConicalIcon, GaugeIcon, KeyboardIcon, RadarIcon, RadioTowerIcon, SearchIcon, ShieldAlertIcon } from "lucide-react";
import Link from "next/link";
import { toast } from "sonner";

import { CustomRulesEditor } from "@/components/custom-rules-editor";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { CustomRule, ExplorationPolicyDefault, IntelQueryRow, Settings } from "@/lib/types";

import { UpdateCard } from "./_components/update-card";

export default function SystemSettingsPage() {
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
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 任务级 token 预算(0 = 关闭)。
  const [tokenBudget, setTokenBudget] = React.useState("0");
  const [savingBudget, setSavingBudget] = React.useState(false);
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
    setIntelKeySet(!!s.fofa_key_set);
    setIntelEmail(s.fofa_email ?? "");
    setIntelBaseURL(s.fofa_base_url ?? "");
    setIntelProxy(s.fofa_proxy ?? "");
    setIntelQuota(String(s.intel_daily_quota ?? 200));
    setIntelUsed(s.intel_used_today ?? 0);
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
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("已保存 Python 解释器配置");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api
      .detectPython()
      .then((r) => setPyInterp(r.python_interpreter))
      .catch(() => undefined)
      .finally(() => setSaving(false));
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
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  const toggleInjectPlanner = (v: boolean) => {
    setInjectPlanner(v); // optimistic
    api
      .setSettings({ constraints_inject_planner: v })
      .then(apply)
      .catch(() => setInjectPlanner(!v)); // revert on failure
  };

  const toggleAgentTrafficBinding = (v: boolean) => {
    setAgentTrafficBinding(v);
    setSaving(true);
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
      .finally(() => setSaving(false));
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
    setSaving(true);
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
      .finally(() => setSaving(false));
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

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">系统配置</h1>
        <p className="text-muted-foreground text-sm">全局运行时开关</p>
      </div>

      {/* 多列而非 grid：网络搜索卡片比其余高数倍，且高度随所选后端变化（brave/tavily
          的 key 输入是条件渲染）。grid 会按最高的一张撑满整行、在旁边留下大片空白，
          多列则自动按内容高度平衡填充。卡片间距靠 mb 而非 gap——多列布局下
          column-gap 只管列间距，行间距要由子元素自己给。 */}
      <div className="columns-1 gap-4 md:gap-6 lg:columns-2">
        <UpdateCard />

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              流量捕获
            </CardTitle>
            <CardDescription>
              开启后，所有 Agent 的 HTTP 流量经记录代理全量落库，并向 Agent 注入 traffic_search / traffic_get
              工具与代理配置（提示词含代理说明）。
              <br />
              关闭（默认）时不记录任何流量：Agent
              <b>不会</b>拿到代理配置与流量工具，提示词也<b>不含</b>代理相关内容。切换后会即时重建 Agent 生效。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
              {trafficCapture ? "已开启 · 正在记录流量并注入代理" : "已关闭 · 不记录、不注入代理"}
            </Label>
            <Switch
              id="traffic-capture"
              checked={trafficCapture}
              disabled={!loaded || saving}
              onCheckedChange={toggleTraffic}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              Agent 自动绑定流量
            </CardTitle>
            <CardDescription id="agent-traffic-binding-description">
              默认关闭。开启后，漏洞入库时触发的报告 Agent 会核对已有 HTTP 请求/响应，关联对应流量后再编写报告。
              <b>查阅数据包及额外的工具调用会增加 Token 消耗。</b>
              <br />
              TCP、未抓包或没有匹配流量时仍可正常上报。此开关不影响流量捕获、人工绑定及已保存证据的查看。 对下一轮 Agent
              生效；关闭后会立即拒绝新的自动绑定。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="agent-traffic-binding" className="text-sm font-normal text-muted-foreground">
              {agentTrafficBinding ? "已开启 · 会增加 Token 消耗" : "已关闭 · 可继续人工绑定"}
            </Label>
            <Switch
              id="agent-traffic-binding"
              aria-describedby="agent-traffic-binding-description"
              checked={agentTrafficBinding}
              disabled={!loaded || saving}
              onCheckedChange={toggleAgentTrafficBinding}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              全局代理
            </CardTitle>
            <CardDescription>
              所有 Agent 的<b>目标流量</b>经此代理出网（隐藏源 IP / 走跳板）。支持 <b>http / https / socks5</b>，可带{" "}
              <code>user:pass</code> 认证。留空=直连。
              <br />
              开启<b>流量捕获</b>时，它作为记录代理的<b>上游</b>（流量仍全量落库，再经此代理出网）；关闭捕获时，直接注入
              Agent 的 bash / WebFetch 出网。与网络搜索代理、LLM 代理相互独立。
              <br />
              <b>提示</b>：socks5 在<b>关闭捕获</b>时依赖各命令行工具对 <code>ALL_PROXY</code> 的支持（curl
              可用，部分工具可能忽略）； 若主要用 socks5，建议开启流量捕获——此路径由 MITM
              亲自拨号，工具无感知、稳定生效。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="global-proxy" className="text-sm font-normal text-muted-foreground">
              代理地址
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="global-proxy"
                autoComplete="off"
                placeholder="socks5://user:pass@host:1080 或 http://host:port（留空=直连）"
                value={globalProxyInput}
                disabled={!loaded || savingGlobalProxy}
                onChange={(e) => setGlobalProxyInput(e.target.value)}
              />
              <Button type="button" onClick={saveGlobalProxy} disabled={!loaded || savingGlobalProxy}>
                保存
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {globalProxyInput.trim() ? "已配置 · 所有目标流量经此代理出网" : "未配置 · 目标流量直连出网"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <GaugeIcon className="size-4" />
              任务 Token 预算
            </CardTitle>
            <CardDescription>
              按任务累计 LLM 用量（输入+输出）：到 <b>70% / 85% / 95%</b> 各向 planner
              投递一次收敛提示，达到 <b>100%</b> 时任务进入收尾——停派新意图、收在跑方向、出终局报告，不再无限消耗。
              留空或填 <b>0</b> = 关闭护栏。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            <Label htmlFor="token-budget" className="text-sm font-normal text-muted-foreground">
              预算（token 数）
            </Label>
            <div className="flex items-center gap-2">
              <Input
                id="token-budget"
                inputMode="numeric"
                autoComplete="off"
                placeholder="例如 2000000（0 = 关闭）"
                value={tokenBudget}
                disabled={!loaded || savingBudget}
                onChange={(e) => setTokenBudget(e.target.value)}
              />
              <Button type="button" onClick={saveTokenBudget} disabled={!loaded || savingBudget}>
                保存
              </Button>
            </div>
            <p className="text-muted-foreground text-xs">
              {Number(tokenBudget) > 0 ? "已配置 · 到量提示并自动收尾" : "未配置 · 任务不设 token 上限"}
            </p>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadarIcon className="size-4" />
              情报源（测绘引擎）
            </CardTitle>
            <CardDescription>
              给 Agent 一个"自己去查互联网资产"的工具（当前接入 <b>FOFA</b>）。查到的东西
              <b>只进回执、不入库、不自动纳入测试范围</b>——要不要登记成资产（insert_assets）、
              要不要纳入本任务范围（add_task_scope）由 Agent 或人显式决定，资产拦截规则照常生效。
              <br />
              每次真实查询都会记入台账并占用<b>平台侧每日上限</b>（FOFA 自己的额度之外再加一道闸，
              防模型一天把额度刷光）。Key 只存不回显，页面只显示"是否已配置"。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="grid gap-2">
              <Label htmlFor="fofa-key">API Key{intelKeySet ? "（已配置）" : "（未配置）"}</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="fofa-key"
                  type="password"
                  autoComplete="off"
                  placeholder={intelKeySet ? "已配置 · 留空表示不修改" : "填入 FOFA API Key"}
                  value={intelKeyInput}
                  onChange={(e) => setIntelKeyInput(e.target.value)}
                  disabled={!loaded || intelBusy}
                />
                {intelKeySet && (
                  <Button type="button" variant="outline" onClick={() => void clearIntelKey()} disabled={intelBusy}>
                    清除 Key
                  </Button>
                )}
              </div>
            </div>
            <div className="grid gap-2 md:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="fofa-email" className="text-sm font-normal text-muted-foreground">
                  FOFA 账号邮箱（老账号需要，新账号留空）
                </Label>
                <Input
                  id="fofa-email"
                  autoComplete="off"
                  placeholder="可选"
                  value={intelEmail}
                  onChange={(e) => setIntelEmail(e.target.value)}
                  disabled={!loaded || intelBusy}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="fofa-base" className="text-sm font-normal text-muted-foreground">
                  API 端点（企业版/自建可改，留空用官方）
                </Label>
                <Input
                  id="fofa-base"
                  autoComplete="off"
                  placeholder="https://fofa.info"
                  value={intelBaseURL}
                  onChange={(e) => setIntelBaseURL(e.target.value)}
                  disabled={!loaded || intelBusy}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="fofa-proxy" className="text-sm font-normal text-muted-foreground">
                  独立出口代理（留空跟随全局代理）
                </Label>
                <Input
                  id="fofa-proxy"
                  autoComplete="off"
                  placeholder="http://127.0.0.1:7897"
                  value={intelProxy}
                  onChange={(e) => setIntelProxy(e.target.value)}
                  disabled={!loaded || intelBusy}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="intel-quota" className="text-sm font-normal text-muted-foreground">
                  平台侧每日上限（0 = 不限；今日已用 {intelUsed} 次）
                </Label>
                <Input
                  id="intel-quota"
                  inputMode="numeric"
                  autoComplete="off"
                  value={intelQuota}
                  onChange={(e) => setIntelQuota(e.target.value.replace(/[^0-9]/g, ""))}
                  disabled={!loaded || intelBusy}
                />
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" onClick={() => void saveIntel()} disabled={!loaded || intelBusy}>
                保存
              </Button>
              <Button type="button" variant="outline" onClick={() => void testIntel()} disabled={!loaded || intelTesting}>
                {intelTesting ? "测试中…" : "测试连接"}
              </Button>
              {intelTestMsg && <span className="text-muted-foreground text-xs">{intelTestMsg}</span>}
            </div>
            {intelRows.length > 0 && (
              <div className="flex flex-col gap-1">
                <span className="text-sm font-medium">最近查询（审计与排障）</span>
                {intelRows.slice(0, 5).map((q) => (
                  <div key={q.id} className="flex flex-wrap items-center gap-2 text-xs">
                    <span className="text-muted-foreground tabular-nums">
                      {new Date(q.created_at).toLocaleString("zh-CN", { hour12: false })}
                    </span>
                    <code className="bg-muted max-w-80 truncate rounded px-1">{q.expr}</code>
                    <span className={q.error ? "text-red-500" : "text-muted-foreground"}>
                      {q.error ? `失败：${q.error}` : `命中 ${q.total_hits} 条 · 取回 ${q.returned} 条`}
                    </span>
                    {q.task_id && (
                      <Link
                        href={`/function/tasks/detail?id=${encodeURIComponent(q.task_id)}`}
                        className="hover:underline"
                      >
                        任务 #{q.task_id}
                      </Link>
                    )}
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              探索策略（全局默认）
            </CardTitle>
            <CardDescription>
              给「探索链路」划边界的默认值，<b>只影响之后新建的任务</b>——每个任务创建时会把当时的默认解析成
              自己的快照（运行中的任务不会被这里改动意外改变边界），任务页可单独覆盖。
              <br />
              生效方式与任务页一致：不符合的<b>探索方向不会进探索图</b>（拒绝时说明命中什么、往哪改），同一份策略
              还会编译成<b>任务级拦截规则</b>兜住命令层——换措辞也绕不过。词表覆盖不到时可以手写
              <b>自定义禁止条目</b>（关键词或正则；「仅命令」层只拦工具调用、不判意图措辞）。
              这里是<b>加法</b>：平台内置的破坏性拦截（删除类请求等）始终生效，不因这里放开而解除。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            {polDefault ? (
              <>
                <div className="flex flex-col gap-1.5">
                  <span className="text-sm font-medium">默认禁用手段</span>
                  <div className="flex flex-wrap gap-1.5">
                    {polDefault.techniques.map((t) => {
                      const on = polTech.includes(t.id);
                      return (
                        <button
                          key={t.id}
                          type="button"
                          title={t.hint}
                          disabled={savingPol}
                          onClick={() => togglePolicyChip(polTech, setPolTech, t.id)}
                          className={`rounded-full border px-2 py-0.5 text-xs transition-colors ${
                            on
                              ? "border-red-500/40 bg-red-500/15 text-red-600 dark:text-red-400"
                              : "border-border text-muted-foreground hover:bg-muted"
                          }`}
                        >
                          {on ? "禁止 · " : ""}
                          {t.label}
                        </button>
                      );
                    })}
                  </div>
                </div>
                <div className="flex flex-col gap-1.5">
                  <span className="text-sm font-medium">默认禁止的敏感写</span>
                  <div className="flex flex-wrap gap-1.5">
                    {polDefault.writes.map((w) => {
                      const on = polWrites.includes(w.id);
                      return (
                        <button
                          key={w.id}
                          type="button"
                          title={w.hint}
                          disabled={savingPol}
                          onClick={() => togglePolicyChip(polWrites, setPolWrites, w.id)}
                          className={`rounded-full border px-2 py-0.5 text-xs transition-colors ${
                            on
                              ? "border-red-500/40 bg-red-500/15 text-red-600 dark:text-red-400"
                              : "border-border text-muted-foreground hover:bg-muted"
                          }`}
                        >
                          {on ? "禁止 · " : ""}
                          {w.label}
                        </button>
                      );
                    })}
                  </div>
                </div>
                <div className="flex flex-col gap-1.5">
                  <span className="text-sm font-medium">自定义禁止条目（词表覆盖不到时手写）</span>
                  <CustomRulesEditor
                    rules={polCustom}
                    onChange={setPolCustom}
                    disabled={savingPol}
                    limit={polDefault.custom_limit ?? 16}
                    patternLimit={polDefault.custom_pattern_limit ?? 200}
                  />
                </div>
                <div className="flex flex-wrap items-center gap-3">
                  <Button type="button" onClick={() => void savePolicyDefault()} disabled={savingPol}>
                    保存默认
                  </Button>
                  <span className="text-muted-foreground text-xs">
                    当前第 {polDefault.version} 版
                    {polDefault.effective.max_intents_per_round
                      ? ` · 每轮最多 ${polDefault.effective.max_intents_per_round} 条意图`
                      : " · 每轮意图数不限"}
                  </span>
                </div>
              </>
            ) : (
              <p className="text-muted-foreground text-sm">加载中…</p>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <ShieldAlertIcon className="size-4" />
              操作约束注入
            </CardTitle>
            <CardDescription>
              开启后，把每个任务的<b>操作约束</b>（在任务总览「操作约束」里维护的 allow/deny 条目）拼进对应 Agent
              的系统提示，用来框定探索边界（如「仅测当前端口」「禁止爆破」）。
              <br />
              可分别控制注入到 <b>规划者（planner）</b>与 <b>执行者（worker）</b>
              ；默认都开。切换即时生效（下一轮读取），无需重建 Agent。关闭后该 Agent 不再看到约束。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-planner" className="text-sm font-normal text-muted-foreground">
                注入规划者（planner）{injectPlanner ? " · 已开启" : " · 已关闭"}
              </Label>
              <Switch
                id="inject-planner"
                checked={injectPlanner}
                disabled={!loaded}
                onCheckedChange={toggleInjectPlanner}
              />
            </div>
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="inject-worker" className="text-sm font-normal text-muted-foreground">
                注入执行者（worker）{injectWorker ? " · 已开启" : " · 已关闭"}
              </Label>
              <Switch
                id="inject-worker"
                checked={injectWorker}
                disabled={!loaded}
                onCheckedChange={toggleInjectWorker}
              />
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <FlaskConicalIcon className="size-4" />
              实验功能
            </CardTitle>
            <CardDescription>
              尚在验证中的机制，默认关闭。可能改变 Agent 行为或影响稳定性，请在了解影响后启用。
              <br />
              <b>noa 上下文压缩</b>：由模型主动压缩长对话历史（norma v0.4.0）。开启后平台接入的四类 Agent（
              <b>规划者 / 执行者 / 主 Agent / 对话</b>）改用 noa 接管上下文，取代内置压缩，
              压缩原文会归档到任务工作目录下便于回溯。切换即时生效（对之后启动的运行生效），无需重建 Agent；
              关闭后立即恢复内置压缩。
              <br />
              <b>注意（与 P14 领域化压缩互斥）</b>：默认走的是「领域化压缩 + 常驻索引」
              （7 段渗透领域摘要、压缩前原文落盘可回读、每轮附目标/约束/活跃方向索引）。
              开启 noa 后这一层整体让位——两者抢同一个上下文管理器槽位，不会叠加。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="noa-compaction" className="text-sm font-normal text-muted-foreground">
              noa 上下文压缩{noaCompaction ? " · 已开启" : " · 已关闭"}
            </Label>
            <Switch
              id="noa-compaction"
              checked={noaCompaction}
              disabled={!loaded}
              onCheckedChange={toggleNoaCompaction}
            />
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <SearchIcon className="size-4" />
              网络搜索
            </CardTitle>
            <CardDescription>
              这是网络搜索的<b>总开关 + 来源配置</b>。开启后，才能在<b>每个 Agent 的配置</b>里单独选择是否启用
              <b>web_search</b>（仅返回标题/链接/摘要，不抓取正文；抓取由 WebFetch 负责）。网络搜索<b>不走</b>
              记录代理，独立于流量捕获。
              <br />
              来源可选 <b>DuckDuckGo（ddgs）</b>（无需 Key）、<b>Brave（免费版）</b>（需填写 Brave API Key）、{" "}
              <b>Tavily</b>（需填写 Tavily API Key）或 <b>DeepSeek</b>（复用当前 LLM 配置）。总开关关闭时，各
              Agent 的网络搜索开关不可用。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex items-center justify-between gap-4">
              <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
                {webSearch ? "总开关已开启 · 可在各 Agent 配置里单独启用" : "已关闭 · 各 Agent 无法启用网络搜索"}
              </Label>
              <Switch
                id="web-search"
                checked={webSearch}
                disabled={!loaded || saving}
                onCheckedChange={(v) => {
                  setWebSearch(v); // optimistic
                  saveWebSearch({ web_search_enabled: v });
                }}
              />
            </div>

            {webSearch && (
              <div className="flex items-center justify-between gap-4">
                <Label className="text-sm font-normal text-muted-foreground">搜索来源</Label>
                <Select
                  value={backend}
                  disabled={!loaded || saving}
                  onValueChange={(v) => {
                    setBackend(v); // optimistic
                    saveWebSearch({ web_search_backend: v });
                  }}
                >
                  <SelectTrigger className="w-48 shrink-0">
                    <SelectValue placeholder="选择来源" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="ddgs">DuckDuckGo（ddgs · 免费无 Key）</SelectItem>
                    <SelectItem value="brave-free">Brave（免费版 · 需 Key）</SelectItem>
                    <SelectItem value="tavily">Tavily（需 Key）</SelectItem>
                    <SelectItem value="deepseek">DeepSeek（官方）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}

            {webSearch && backend === "deepseek" && (
              <div className="border-border/60 bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                <p className="text-sm font-medium">DeepSeek 官方联网搜索</p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  该来源直接复用<b>当前激活的 LLM 配置</b>。因此它
                  <b>仅支持 DeepSeek 官方模型</b>，且该配置<b>必须使用 anthropic 协议</b>
                  ——DeepSeek 的 OpenAI 协议端点不支持服务端搜索。切换 LLM 配置后此来源可能失效。
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  与其它来源不同，搜索由 <b>DeepSeek 服务端执行</b>：每次搜索会额外消耗一次模型调用（产生 Token
                  费用），搜索请求<b>不经过上面的出口代理</b>，也<b>不计入流量留痕</b>；返回结果<b>只有标题和链接</b>
                  （无摘要），需要正文时由 WebFetch 抓取。
                </p>
                <p className="text-muted-foreground text-xs leading-relaxed">
                  是否满足上述条件由你自行确认，系统不做拦截；可用下方「测试搜索」按钮实际跑一次来验证。
                </p>
              </div>
            )}

            {webSearch && backend === "brave-free" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                  Brave Search API Key
                  {braveKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="brave-key"
                    type="password"
                    autoComplete="off"
                    placeholder={braveKeySet ? "已配置（留空则不变）" : "输入 Brave API Key"}
                    value={braveKeyInput}
                    disabled={!loaded || savingKey}
                    onChange={(e) => setBraveKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveBraveKey}
                    disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                  >
                    保存
                  </Button>
                </div>
                {braveNeedsKey && (
                  <p className="text-xs text-amber-500">
                    已选择 Brave 但尚未配置 Key —— 在保存 Key 之前，搜索工具不会启用。
                  </p>
                )}
                <p className="text-muted-foreground text-xs">
                  免费版额度约 2,000 次/月。前往 https://brave.com/search/api/ 获取 Key。
                </p>
              </div>
            )}

            {webSearch && backend === "tavily" && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                  Tavily Search API Key
                  {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="tavily-key"
                    type="password"
                    autoComplete="off"
                    placeholder={tavilyKeySet ? "已配置（留空则不变）" : "输入 Tavily API Key（tvly-…）"}
                    value={tavilyKeyInput}
                    disabled={!loaded || savingTavilyKey}
                    onChange={(e) => setTavilyKeyInput(e.target.value)}
                  />
                  <Button
                    type="button"
                    onClick={saveTavilyKey}
                    disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                  >
                    保存
                  </Button>
                </div>
                {webSearch && backend === "tavily" && !tavilyKeySet && (
                  <p className="text-xs text-amber-500">
                    已选择 Tavily 但尚未配置 Key —— 在保存 Key 之前，搜索工具不会启用。
                  </p>
                )}
                <p className="text-muted-foreground text-xs">前往 https://tavily.com 注册并获取 API Key。</p>
              </div>
            )}

            {webSearch && (
              <div className="flex flex-col gap-2">
                <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                  出口代理（可选）
                </Label>
                <div className="flex items-center gap-2">
                  <Input
                    id="ws-proxy"
                    autoComplete="off"
                    placeholder="http://host:port 或 socks5://host:port（留空=直连）"
                    value={proxyInput}
                    disabled={!loaded || savingProxy}
                    onChange={(e) => setProxyInput(e.target.value)}
                  />
                  <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                    保存
                  </Button>
                </div>
                <p className="text-muted-foreground text-xs">
                  独立出口代理，仅用于访问搜索端点（VPN/SOCKS 等）。与记录流量的 MITM 代理无关；网络不通时经此代理访问。
                </p>
              </div>
            )}

            {webSearch && (
              <div className="flex items-center justify-between gap-4 border-t pt-4">
                <p className="text-muted-foreground text-xs">
                  用当前配置（来源 + 代理 + Key）实际搜索一次「test」，验证是否可用。
                </p>
                <Button
                  type="button"
                  variant="outline"
                  onClick={runTest}
                  disabled={!loaded || testing}
                  className="shrink-0"
                >
                  {testing ? "测试中…" : "测试搜索"}
                </Button>
              </div>
            )}
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <RadioTowerIcon className="size-4" />
              自定义脚本 · Python 解释器
            </CardTitle>
            <CardDescription>
              自定义 <b>script</b> 类型工具用它跑 Python。开机会自动检测（python3 优先）；此处可手填 venv /
              特定版本的绝对路径，留空则运行时自动检测。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                className="font-mono text-sm"
                placeholder="/usr/bin/python3（留空=自动检测）"
                value={pyInterp}
                disabled={!loaded || saving}
                onChange={(e) => setPyInterp(e.target.value)}
              />
              <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
                重新检测
              </Button>
              <Button onClick={savePython} disabled={!loaded || saving}>
                保存
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <CpuIcon className="size-4" />
              工作并发 · Work Agent 数
            </CardTitle>
            <CardDescription>
              每个任务并发运行的工作 agent 数量（默认 3）。数值越大并发探测越多、消耗也越高。修改后
              <b>对之后启动的任务生效</b>，正在运行的任务不受影响。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-center gap-2">
              <Input
                type="number"
                min={1}
                className="w-32 font-mono text-sm"
                placeholder="3"
                value={workers}
                disabled={!loaded || savingWorkers}
                onChange={(e) => setWorkers(e.target.value)}
              />
              <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
                保存
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card className="mb-4 break-inside-avoid md:mb-6">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <KeyboardIcon className="size-4" />
              会话输入框发送键位
            </CardTitle>
            <CardDescription>
              对话页与任务详情的主 Agent 会话输入框共用此设置，选择后立即生效、无需保存。
              <br />
              该偏好<b>只存在本浏览器</b>，不随账号同步，换浏览器或清理站点数据后需重新设置。
            </CardDescription>
          </CardHeader>
          <CardContent className="flex items-center justify-between gap-4">
            <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
              发送方式
            </Label>
            <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
              <SelectTrigger id="chat-send-mode" className="w-72">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {CHAT_SEND_MODE_OPTIONS.map((option) => (
                  <SelectItem key={option.value} value={option.value}>
                    {option.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
