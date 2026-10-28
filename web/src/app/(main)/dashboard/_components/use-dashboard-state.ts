"use client";

import * as React from "react";

import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type {
  Activity,
  ConvTokenSummary,
  DashboardTaskTokenDay,
  DashboardTaskTokenProfile,
  Finding,
  FindingStats,
  InterceptPending,
  LLMProfile,
  Settings,
  Stats,
  Task,
  TrafficExchange,
  UsageStats,
} from "@/lib/types";

// 仪表盘的状态与取数：任务/发现/统计/活动/流量/Agent 等面板的数据源与轮询。
//
// 抽成 hook 是为了让页面本体只剩五行面板的 JSX。
// 各面板通过 `ReturnType<typeof useDashboardState>` 拿到同一份状态。

// 这几个助手是**纯函数**（只看入参），所以放在模块作用域。写成 hook 内的函数声明会
// 每次渲染都拿到新身份，任何把它们放进依赖数组的 useMemo 都会因此永远失效。
function kindLabel(a: Activity): string {
  if (a.kind === "tool_use") return a.tool ?? "tool_use";
  if (a.kind === "tool_result") return "tool_result";
  return a.kind;
}

function workerBg(w: string): string {
  if (w === "planner") return "bg-violet-500/10 text-violet-400 border-violet-500/20";
  if (w === "mainagent") return "bg-cyan-500/10 text-cyan-400 border-cyan-500/20";
  return "bg-blue-500/10 text-blue-400 border-blue-500/20";
}

export function useDashboardState() {
  // data state
  // Only the recent window is needed for the table; counts and token aggregates
  // come from the server summary instead of downloading every task DTO.
  const [recentTasks, setRecentTasks] = React.useState<Task[]>([]);
  const [taskTotal, setTaskTotal] = React.useState(0);
  const [tasksByStatus, setTasksByStatus] = React.useState<Record<string, number>>({});
  const [taskTokenProfiles, setTaskTokenProfiles] = React.useState<DashboardTaskTokenProfile[]>([]);
  const [taskTokenDaily, setTaskTokenDaily] = React.useState<DashboardTaskTokenDay[]>([]);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  // 发现总数与严重度分布走 findingStats 的 COUNT(*)；findings 只是最近窗口（500）。
  const [findingStats, setFindingStats] = React.useState<FindingStats | null>(null);
  const [stats, setStats] = React.useState<Stats | null>(null);
  const [settings, setSettings] = React.useState<Settings | null>(null);
  const [pending, setPending] = React.useState<InterceptPending[]>([]);
  // 待审批总数与「前 3 条」分开：列表只取 3 条用于展示，徽章数字必须是精确总数。
  const [pendingTotal, setPendingTotal] = React.useState(0);
  const [activity, setActivity] = React.useState<Activity[]>([]);
  const [convTokens, setConvTokens] = React.useState<ConvTokenSummary[]>([]);
  const [usageStats, setUsageStats] = React.useState<UsageStats | null>(null);
  const [assetCounts, setAssetCounts] = React.useState<Record<string, number>>({});
  const [traffic, setTraffic] = React.useState<TrafficExchange[]>([]);
  // 流量总条数（服务端 count 是 COUNT(*)，不是当前 50 行窗口）。
  const [trafficTotal, setTrafficTotal] = React.useState(0);
  const [llmProfiles, setLLMProfiles] = React.useState<LLMProfile[]>([]);
  // 数据源开关：旧版 = activity（task.tokens + 会话），新版 = llm_usage 计量账本。
  const [tokenVersion, setTokenVersion] = React.useState<"old" | "new">("old");
  // selected profile tab: "all" = 全部; number = specific profile id
  const [tokenTab, setTokenTab] = React.useState<number | null | "all">("all");
  // day range for the daily bar chart
  const [tokenDays, setTokenDays] = React.useState<7 | 30 | 90 | 180 | 365>(30);

  // The task list is the most expensive dashboard source. Poll it independently
  // so a large history cannot hold back every other dashboard panel.
  // 去重用的 signature 必须**跨轮询持久**：hook 的回调每次渲染重建，写成回调里的
  // `let signature` 会随渲染重置，去重就失效了（每次轮询都重新 setTasks）。
  const taskSignatureRef = React.useRef("");
  usePolling(async (alive) => {
    // 失败由 hook 接住并退避重试。
    const summary = await api.dashboardSummary();
    if (!alive()) return;
    const nextSignature = JSON.stringify([
      summary.task_total,
      summary.tasks_by_status,
      summary.recent_tasks,
      summary.token_by_profile,
      summary.daily_tokens,
    ]);
    if (nextSignature === taskSignatureRef.current) return;
    taskSignatureRef.current = nextSignature;
    setRecentTasks(summary.recent_tasks);
    setTaskTotal(summary.task_total);
    setTasksByStatus(summary.tasks_by_status);
    setTaskTokenProfiles(summary.token_by_profile);
    setTaskTokenDaily(summary.daily_tokens);
  }, 5000);

  // The remaining fast sources stay batched so one poll causes a single render.
  usePolling(
    async (alive) => {
      let loading = false;
      const load = async () => {
        if (loading) return;
        loading = true;
        try {
          const [recentPage, stats, settings, pendingRecent, activity, conversationTokens, findingTotals] = await Promise.all([
            // 仪表盘只渲染最近 8 条；此前拉 500 条再截断，等于每 5 秒搬一整页。
            api.findingsPage({ page: 1, pageSize: 8, sort: "time" }),
            api.stats(),
            api.settings(),
            api.interceptPendingRecent(3),
            api.activity(undefined, { limit: 30 }),
            api.conversationTokens(),
            api.findingStats(),
          ]);
          if (!alive()) return;
          setFindings(recentPage.items);
          setFindingStats(findingTotals);
          setStats(stats);
          setSettings(settings);
          setPending(pendingRecent.pending);
          setPendingTotal(pendingRecent.total);
          setActivity(activity.items);
          setConvTokens(conversationTokens);
        } catch (e) {
          // Preserve stale data, but let usePolling see the failure and back off.
          throw e;
        } finally {
          loading = false;
        }
      };
      await load();
    },
    5000,
    [],
  );

  // slow poll: traffic, assets, system-static (every 15s)
  //
  // `usageStats` 只服务「新版」token 视图：旧版（默认）读的是 activity 侧的 task.tokens 与
  // 会话用量，这份数据在旧版下一个字段都没人读。而它的后端是 by_profile 全表 GROUP BY +
  // 按日聚合，随表增长线性变贵——所以只在「新版」时才拉，天数按用户选的窗口收敛（不再固定
  // 365）。切到新版 / 改天数会重建轮询、立即按新参数拉一次（deps 见下）。
  usePolling(
    async (alive) => {
      const load = async () => {
        try {
          // 只取**真正会被渲染**的东西。此前这里还拉了 agents / mcpServers /
          // skills / tools，但 dashboard 上没有任何面板读它们（唯一的 tools 下游
          // enabledTools 自己也是死的）——等于每 15 秒白发四个请求。要加相关面板时
          // 再把对应的源加回来。
          const wantUsage = tokenVersion === "new";
          const [traf, counts, profileList, usage] = await Promise.all([
            api.traffic(0, 50),
            api.assetCounts(),
            api.llmProfiles(),
            wantUsage ? api.usageStats(tokenDays) : Promise.resolve(null),
          ]);
          if (!alive()) return;
          setTraffic(traf.exchanges ?? []);
          setTrafficTotal(traf.count ?? traf.total ?? traf.exchanges?.length ?? 0);
          setAssetCounts(counts ?? {});
          setLLMProfiles(profileList);
          if (usage) setUsageStats(usage);
        } catch (e) {
          throw e; // preserve stale data; usePolling owns the backoff
        }
      };
      await load();
    },
    15000,
    [tokenVersion, tokenDays],
  );

  // ── derived ───────────────────────────────────────────────────────────────

  const findingsBySev = React.useMemo(() => {
    const m = { critical: 0, high: 0, medium: 0, low: 0 };
    for (const f of findings) {
      if (f.severity in m) m[f.severity as keyof typeof m]++;
    }
    return m;
  }, [findings]);

  const sortedTasks = React.useMemo(
    () =>
      [...recentTasks]
        .sort((a, b) => (b.last_activity_unix ?? b.created_unix ?? 0) - (a.last_activity_unix ?? a.created_unix ?? 0))
        .slice(0, 5),
    [recentTasks],
  );

  const recentFindings = React.useMemo(
    () => [...findings].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).slice(0, 8),
    [findings],
  );

  const recentActivity = React.useMemo(
    () =>
      [...activity]
        .filter((a) => a.kind !== "usage")
        .sort((a, b) => b.seq - a.seq)
        .slice(0, 6),
    [activity],
  );

  const totalAssets = React.useMemo(() => Object.values(assetCounts).reduce((a, b) => a + b, 0), [assetCounts]);

  // asset type breakdown
  const assetByType = React.useMemo(() => {
    return Object.entries(assetCounts)
      .filter(([, n]) => n > 0)
      .sort(([, a], [, b]) => b - a)
      .slice(0, 8);
  }, [assetCounts]);

  const assetMax = React.useMemo(() => assetByType[0]?.[1] ?? 1, [assetByType]);

  // traffic status code breakdown
  const trafficByCodes = React.useMemo(() => {
    const m: Record<number, number> = {};
    for (const e of traffic) {
      const bucket = Math.floor(e.status / 100) * 100;
      m[bucket] = (m[bucket] ?? 0) + 1;
    }
    return Object.entries(m)
      .sort(([a], [b]) => Number(a) - Number(b))
      .map(([code, n]) => ({ code: Number(code), n }));
  }, [traffic]);

  const trafficMax = React.useMemo(() => Math.max(...trafficByCodes.map((x) => x.n), 1), [trafficByCodes]);
  // 这两个此前每次渲染都重跑：recentTraffic 尤其贵（整份 traffic 拷贝 + 排序 + 切片），
  // 而 dashboard 每 5 秒就因轮询重渲染一次，成本会一直累积。
  const recentTraffic = React.useMemo(
    () => [...traffic].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).slice(0, 5),
    [traffic],
  );

  // system
  const activeProfile = React.useMemo(() => llmProfiles.find((p) => p.is_default), [llmProfiles]);
  const pendingCount = pendingTotal;

  // ── token stats per LLM profile ──────────────────────────────────────────
  // tasks whose llm_profile_id is null/undefined used the active default profile
  const defaultProfileId = activeProfile ? Number(activeProfile.id) : null;

  // profile 名 → id，用于把 llm_usage 的 profile_name 映射到现有 profile 分栏。
  const profileIdByName = React.useMemo(() => {
    const m = new Map<string, number>();
    for (const p of llmProfiles) m.set(p.name, Number(p.id));
    return m;
  }, [llmProfiles]);

  type Bucket = { input: number; output: number; cacheRead: number; cacheWrite: number; taskCount: number };

  // 旧版：按 profile 归桶（来自 activity 的 task.tokens + 会话用量）。
  const tokenByProfileOld = React.useMemo<Map<number | null, Bucket>>(() => {
    const m = new Map<number | null, Bucket>();
    const fold = (key: number | null, inp: number, out: number, cr: number, cw: number, tasks: number) => {
      const prev = m.get(key) ?? { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, taskCount: 0 };
      m.set(key, {
        input: prev.input + inp,
        output: prev.output + out,
        cacheRead: prev.cacheRead + cr,
        cacheWrite: prev.cacheWrite + cw,
        taskCount: prev.taskCount + tasks,
      });
    };
    for (const p of taskTokenProfiles) {
      fold(
        p.profile_id ?? defaultProfileId,
        p.input_tokens,
        p.output_tokens,
        p.cache_read_tokens,
        p.cache_write_tokens,
        p.task_count,
      );
    }
    for (const c of convTokens) {
      fold(
        c.llm_profile_id ?? defaultProfileId,
        c.input_tokens,
        c.output_tokens,
        c.cache_read_tokens,
        c.cache_write_tokens,
        0,
      );
    }
    return m;
  }, [taskTokenProfiles, convTokens, defaultProfileId]);

  // 新版：按 profile 归桶（来自 llm_usage 全局聚合，逐次调用精确）。
  const tokenByProfileNew = React.useMemo<Map<number | null, Bucket>>(() => {
    const m = new Map<number | null, Bucket>();
    for (const p of usageStats?.by_profile ?? []) {
      // 未匹配到现有 profile（改名/删除/空名）→ 落到默认桶，仍计入「全部」。
      const key = profileIdByName.get(p.profile_name) ?? defaultProfileId;
      const prev = m.get(key) ?? { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, taskCount: 0 };
      m.set(key, {
        input: prev.input + p.input_tokens,
        output: prev.output + p.output_tokens,
        cacheRead: prev.cacheRead + p.cache_read_tokens,
        cacheWrite: prev.cacheWrite + p.cache_write_tokens,
        taskCount: prev.taskCount + p.tasks,
      });
    }
    return m;
  }, [usageStats, profileIdByName, defaultProfileId]);

  const tokenByProfile = tokenVersion === "new" ? tokenByProfileNew : tokenByProfileOld;

  const displayedTokens = React.useMemo(() => {
    if (tokenTab === "all") {
      let input = 0,
        output = 0,
        cacheRead = 0,
        cacheWrite = 0,
        taskCount = 0;
      for (const v of tokenByProfile.values()) {
        input += v.input;
        output += v.output;
        cacheRead += v.cacheRead;
        cacheWrite += v.cacheWrite;
        taskCount += v.taskCount;
      }
      return { input, output, cacheRead, cacheWrite, taskCount };
    }
    const v = tokenByProfile.get(tokenTab as number | null);
    return v
      ? { input: v.input, output: v.output, cacheRead: v.cacheRead, cacheWrite: v.cacheWrite, taskCount: v.taskCount }
      : { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, taskCount: 0 };
  }, [tokenTab, tokenByProfile]);

  // 旧版每日：把任务/会话的总量按其创建日期归桶（近似，非真实每日消耗）。
  const dailyTokenDataOld = React.useMemo(() => {
    const now = new Date();
    now.setDate(now.getDate() - tokenDays);
    const cutoff = now.toISOString().slice(0, 10);
    const m = new Map<string, { input: number; miss: number; output: number; cacheRead: number }>();
    const fold = (date: string, inp: number, out: number, cr: number) => {
      if (date < cutoff) return;
      const prev = m.get(date) ?? { input: 0, miss: 0, output: 0, cacheRead: 0 };
      m.set(date, {
        input: prev.input + inp,
        miss: prev.miss + Math.max(0, inp - cr),
        output: prev.output + out,
        cacheRead: prev.cacheRead + cr,
      });
    };
    for (const row of taskTokenDaily) {
      if (tokenTab !== "all" && (row.profile_id ?? defaultProfileId) !== tokenTab) continue;
      fold(row.date, row.input_tokens, row.output_tokens, row.cache_read_tokens);
    }
    for (const c of convTokens) {
      if (tokenTab !== "all" && (c.llm_profile_id ?? defaultProfileId) !== tokenTab) continue;
      fold(c.created_at.slice(0, 10), c.input_tokens, c.output_tokens, c.cache_read_tokens);
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([date, v]) => ({ date: date.slice(5), ...v }));
  }, [taskTokenDaily, convTokens, tokenDays, tokenTab, defaultProfileId]);

  // 新版每日：来自 llm_usage 的真实每日消耗（ts 是实际调用时刻）。
  const dailyTokenDataNew = React.useMemo(() => {
    const now = new Date();
    now.setDate(now.getDate() - tokenDays);
    const cutoff = now.toISOString().slice(0, 10);
    const m = new Map<string, { input: number; miss: number; output: number; cacheRead: number }>();
    for (const d of usageStats?.daily ?? []) {
      if (d.date < cutoff) continue;
      if (tokenTab !== "all" && (profileIdByName.get(d.profile_name) ?? defaultProfileId) !== tokenTab) continue;
      const prev = m.get(d.date) ?? { input: 0, miss: 0, output: 0, cacheRead: 0 };
      m.set(d.date, {
        input: prev.input + d.input_tokens,
        miss: prev.miss + Math.max(0, d.input_tokens - d.cache_read_tokens),
        output: prev.output + d.output_tokens,
        cacheRead: prev.cacheRead + d.cache_read_tokens,
      });
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([date, v]) => ({ date: date.slice(5), ...v }));
  }, [usageStats, tokenDays, tokenTab, profileIdByName, defaultProfileId]);

  const dailyTokenData = tokenVersion === "new" ? dailyTokenDataNew : dailyTokenDataOld;

  // ── 面板切片 ───────────────────────────────────────────────────────────────
  // 此前页面把整个返回对象交给 5 个面板，而那个对象**每次渲染都是新的**——任何一个
  // 字段变了，5 个面板全部重渲染（dashboard 每 5 秒轮询一次，成本一直累积）。切成
  // 按面板的稳定对象后，只有真正用到变化字段的那个面板会重渲染。
  // 纯拆分：数据获取与派生逻辑一行没动。
  const statCards = React.useMemo(
    () => ({
      taskTotal,
      findings,
      findingStats,
      settings,
      traffic,
      trafficTotal,
      tasksByStatus,
      findingsBySev,
      totalAssets,
      displayedTokens,
    }),
    [
      taskTotal,
      findings,
      findingStats,
      settings,
      traffic,
      trafficTotal,
      tasksByStatus,
      findingsBySev,
      totalAssets,
      displayedTokens,
    ],
  );
  const tokenCard = React.useMemo(
    () => ({
      activity,
      llmProfiles,
      defaultProfileId,
      tokenVersion,
      setTokenVersion,
      tokenTab,
      setTokenTab,
      tokenDays,
      setTokenDays,
      tokenByProfile,
      displayedTokens,
      dailyTokenData,
    }),
    [
      activity,
      llmProfiles,
      defaultProfileId,
      tokenVersion,
      setTokenVersion,
      tokenTab,
      setTokenTab,
      tokenDays,
      setTokenDays,
      tokenByProfile,
      displayedTokens,
      dailyTokenData,
    ],
  );
  // kindLabel / workerBg 现在是模块级纯函数，身份恒定，放进切片不会导致失效。
  const activityRow = React.useMemo(
    () => ({ findings, activity, recentFindings, recentActivity, kindLabel, workerBg }),
    [findings, activity, recentFindings, recentActivity],
  );
  const taskTable = React.useMemo(
    () => ({ tasks: recentTasks, taskTotal, sortedTasks }),
    [recentTasks, taskTotal, sortedTasks],
  );
  const assetTraffic = React.useMemo(
    () => ({
      stats,
      settings,
      pending,
      traffic,
      totalAssets,
      assetByType,
      assetMax,
      trafficByCodes,
      trafficMax,
      recentTraffic,
      activeProfile,
      pendingCount,
      pendingTotal,
    }),
    [
      stats,
      settings,
      pending,
      traffic,
      totalAssets,
      assetByType,
      assetMax,
      trafficByCodes,
      trafficMax,
      recentTraffic,
      activeProfile,
      pendingCount,
      pendingTotal,
    ],
  );

  return { statCards, tokenCard, activityRow, taskTable, assetTraffic };
}
