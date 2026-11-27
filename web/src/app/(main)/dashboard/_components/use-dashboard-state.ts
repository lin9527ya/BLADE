"use client";

import * as React from "react";

import { toast } from "sonner";

import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { type DashboardTaskFilter, daysAgoDateKey } from "@/lib/dashboard-format";
import type {
  Activity,
  ConvTokenSummary,
  CostSummary,
  DashboardLLMPoolSummary,
  DashboardMobileSummary,
  DashboardOpsSummary,
  DashboardRuntimeSummary,
  DashboardSystemSummary,
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

export type ActivityRoleFilter = "all" | "planner" | "mainagent" | "worker";

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

function matchesActivityRole(a: Activity, role: ActivityRoleFilter): boolean {
  if (role === "all") return true;
  if (role === "worker") return a.worker !== "planner" && a.worker !== "mainagent";
  return a.worker === role;
}

export function useDashboardState() {
  const [recentTasks, setRecentTasks] = React.useState<Task[]>([]);
  const [taskTotal, setTaskTotal] = React.useState(0);
  const [tasksByStatus, setTasksByStatus] = React.useState<Record<string, number>>({});
  const [taskTokenProfiles, setTaskTokenProfiles] = React.useState<DashboardTaskTokenProfile[]>([]);
  const [taskTokenDaily, setTaskTokenDaily] = React.useState<DashboardTaskTokenDay[]>([]);
  const [ops, setOps] = React.useState<DashboardOpsSummary | undefined>(undefined);
  const [system, setSystem] = React.useState<DashboardSystemSummary | undefined>(undefined);
  const [runtime, setRuntime] = React.useState<DashboardRuntimeSummary | undefined>(undefined);
  const [llmPool, setLLMPool] = React.useState<DashboardLLMPoolSummary | undefined>(undefined);
  const [mobile, setMobile] = React.useState<DashboardMobileSummary | undefined>(undefined);

  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [findingStats, setFindingStats] = React.useState<FindingStats | null>(null);
  const [stats, setStats] = React.useState<Stats | null>(null);
  const [settings, setSettings] = React.useState<Settings | null>(null);
  const [pending, setPending] = React.useState<InterceptPending[]>([]);
  const [pendingTotal, setPendingTotal] = React.useState(0);
  const [activity, setActivity] = React.useState<Activity[]>([]);
  const [convTokens, setConvTokens] = React.useState<ConvTokenSummary[]>([]);
  const [usageStats, setUsageStats] = React.useState<UsageStats | null>(null);
  const [costs, setCosts] = React.useState<CostSummary | null>(null);
  const [assetCounts, setAssetCounts] = React.useState<Record<string, number>>({});
  const [traffic, setTraffic] = React.useState<TrafficExchange[]>([]);
  const [trafficTotal, setTrafficTotal] = React.useState(0);
  const [llmProfiles, setLLMProfiles] = React.useState<LLMProfile[]>([]);

  const [tokenVersion, setTokenVersion] = React.useState<"old" | "new">("old");
  const [tokenTab, setTokenTab] = React.useState<number | null | "all">("all");
  const [tokenDays, setTokenDays] = React.useState<7 | 30 | 90 | 180 | 365>(30);
  const [taskFilter, setTaskFilter] = React.useState<DashboardTaskFilter>("all");
  const [activityRole, setActivityRole] = React.useState<ActivityRoleFilter>("all");

  const [autoRefresh, setAutoRefresh] = React.useState(true);
  const [summaryReady, setSummaryReady] = React.useState(false);
  const [fastReady, setFastReady] = React.useState(false);
  const [lastSyncAt, setLastSyncAt] = React.useState<number | null>(null);
  const [refreshing, setRefreshing] = React.useState(false);
  const [controllingTaskId, setControllingTaskId] = React.useState<string | null>(null);

  const taskSignatureRef = React.useRef("");

  const applySummary = React.useCallback((summary: Awaited<ReturnType<typeof api.dashboardSummary>>) => {
    const nextSignature = JSON.stringify([
      summary.task_total,
      summary.tasks_by_status,
      summary.recent_tasks,
      summary.token_by_profile,
      summary.daily_tokens,
      summary.ops,
      summary.system,
      summary.runtime,
      summary.llm_pool,
      summary.mobile,
    ]);
    if (nextSignature !== taskSignatureRef.current) {
      taskSignatureRef.current = nextSignature;
      setRecentTasks(summary.recent_tasks);
      setTaskTotal(summary.task_total);
      setTasksByStatus(summary.tasks_by_status);
      setTaskTokenProfiles(summary.token_by_profile);
      setTaskTokenDaily(summary.daily_tokens);
      setOps(summary.ops);
      setSystem(summary.system);
      setRuntime(summary.runtime);
      setLLMPool(summary.llm_pool);
      setMobile(summary.mobile);
    }
    setSummaryReady(true);
    setLastSyncAt(Date.now());
  }, []);

  const fetchSummaryOnce = React.useCallback(
    async (alive?: () => boolean) => {
      try {
        const summary = await api.dashboardSummary();
        if (alive && !alive()) return;
        applySummary(summary);
      } catch (err) {
        if (alive && !alive()) return;
        setSummaryReady(true);
        throw err;
      }
    },
    [applySummary],
  );

  const fetchFastBatchOnce = React.useCallback(async (alive?: () => boolean) => {
    try {
      const [recentPage, nextStats, nextSettings, pendingRecent, nextActivity, conversationTokens, findingTotals] =
        await Promise.all([
          api.findingsPage({ page: 1, pageSize: 8, sort: "time" }),
          api.stats(),
          api.settings(),
          api.interceptPendingRecent(3),
          api.activity(undefined, { limit: 30 }),
          api.conversationTokens(),
          api.findingStats(),
        ]);
      if (alive && !alive()) return;
      setFindings(recentPage.items);
      setFindingStats(findingTotals);
      setStats(nextStats);
      setSettings(nextSettings);
      setPending(pendingRecent.pending);
      setPendingTotal(pendingRecent.total);
      setActivity(nextActivity.items);
      setConvTokens(conversationTokens);
      setLastSyncAt(Date.now());
    } finally {
      if (!alive || alive()) setFastReady(true);
    }
  }, []);

  const fetchSlowBatchOnce = React.useCallback(
    async (alive?: () => boolean, wantUsageOverride?: boolean) => {
      const wantUsage = wantUsageOverride ?? tokenVersion === "new";
      const [traf, counts, profileList, usage, costSummary] = await Promise.all([
        api.traffic(0, 50),
        api.assetCounts(),
        api.llmProfiles(),
        wantUsage ? api.usageStats(tokenDays) : Promise.resolve(null),
        api.costs(30),
      ]);
      if (alive && !alive()) return;
      setTraffic(traf.exchanges ?? []);
      setTrafficTotal(traf.count ?? traf.total ?? traf.exchanges?.length ?? 0);
      setAssetCounts(counts ?? {});
      setLLMProfiles(profileList);
      if (usage) setUsageStats(usage);
      setCosts(costSummary);
    },
    [tokenVersion, tokenDays],
  );

  usePolling(
    async (alive) => {
      await fetchSummaryOnce(alive);
    },
    autoRefresh ? 5000 : 0,
    [autoRefresh, fetchSummaryOnce],
  );

  usePolling(
    async (alive) => {
      await fetchFastBatchOnce(alive);
    },
    autoRefresh ? 5000 : 0,
    [autoRefresh, fetchFastBatchOnce],
  );

  usePolling(
    async (alive) => {
      await fetchSlowBatchOnce(alive, tokenVersion === "new");
    },
    autoRefresh ? 15000 : 0,
    [autoRefresh, tokenVersion, tokenDays, fetchSlowBatchOnce],
  );

  const refreshNow = React.useCallback(async () => {
    if (refreshing) return;
    setRefreshing(true);
    try {
      await Promise.allSettled([fetchSummaryOnce(), fetchFastBatchOnce(), fetchSlowBatchOnce()]);
    } finally {
      setRefreshing(false);
    }
  }, [refreshing, fetchSummaryOnce, fetchFastBatchOnce, fetchSlowBatchOnce]);

  const controlTask = React.useCallback(
    async (taskId: string, action: "pause" | "resume") => {
      if (controllingTaskId) return;
      setControllingTaskId(taskId);
      try {
        await api.controlTask(taskId, action);
        toast.success(action === "pause" ? `已暂停任务 #${taskId}` : `已恢复任务 #${taskId}`);
        taskSignatureRef.current = "";
        await fetchSummaryOnce();
      } catch (err) {
        toast.error(`任务操作失败：${(err as Error).message}`);
      } finally {
        setControllingTaskId(null);
      }
    },
    [controllingTaskId, fetchSummaryOnce],
  );

  // ── derived ───────────────────────────────────────────────────────────────

  const initialLoading = !(summaryReady && fastReady);

  const findingsBySev = React.useMemo(() => {
    const m = { critical: 0, high: 0, medium: 0, low: 0 };
    for (const f of findings) {
      if (f.severity in m) m[f.severity as keyof typeof m]++;
    }
    return m;
  }, [findings]);

  const sortedTasks = React.useMemo(
    () =>
      [...recentTasks].sort(
        (a, b) => (b.last_activity_unix ?? b.created_unix ?? 0) - (a.last_activity_unix ?? a.created_unix ?? 0),
      ),
    [recentTasks],
  );

  const recentFindings = React.useMemo(
    () => [...findings].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).slice(0, 8),
    [findings],
  );

  const filteredActivity = React.useMemo(
    () =>
      [...activity]
        .filter((a) => a.kind !== "usage" && matchesActivityRole(a, activityRole))
        .sort((a, b) => b.seq - a.seq)
        .slice(0, 8),
    [activity, activityRole],
  );

  const totalAssets = React.useMemo(() => Object.values(assetCounts).reduce((a, b) => a + b, 0), [assetCounts]);

  const assetByType = React.useMemo(() => {
    return Object.entries(assetCounts)
      .filter(([, n]) => n > 0)
      .sort(([, a], [, b]) => b - a)
      .slice(0, 8);
  }, [assetCounts]);

  const assetMax = React.useMemo(() => assetByType[0]?.[1] ?? 1, [assetByType]);

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

  const recentTraffic = React.useMemo(
    () => [...traffic].sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts)).slice(0, 5),
    [traffic],
  );

  const activeProfile = React.useMemo(() => llmProfiles.find((p) => p.is_default), [llmProfiles]);
  const pendingCount = pendingTotal;
  const defaultProfileId = activeProfile ? Number(activeProfile.id) : null;

  const profileIdByName = React.useMemo(() => {
    const m = new Map<string, number>();
    for (const p of llmProfiles) m.set(p.name, Number(p.id));
    return m;
  }, [llmProfiles]);

  type Bucket = { input: number; output: number; cacheRead: number; cacheWrite: number; taskCount: number };

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

  const tokenByProfileNew = React.useMemo<Map<number | null, Bucket>>(() => {
    const m = new Map<number | null, Bucket>();
    for (const p of usageStats?.by_profile ?? []) {
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

  const dailyTokenDataOld = React.useMemo(() => {
    const cutoff = daysAgoDateKey(tokenDays);
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

  const dailyTokenDataNew = React.useMemo(() => {
    const cutoff = daysAgoDateKey(tokenDays);
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

  const headerSlice = React.useMemo(
    () => ({
      stats,
      tasksByStatus,
      taskTotal,
      pending,
      pendingCount,
      ops,
      llmPool,
      mobile,
      autoRefresh,
      setAutoRefresh,
      refreshing,
      refreshNow,
      lastSyncAt,
    }),
    [
      stats,
      tasksByStatus,
      taskTotal,
      pending,
      pendingCount,
      ops,
      llmPool,
      mobile,
      autoRefresh,
      refreshing,
      refreshNow,
      lastSyncAt,
    ],
  );

  const statCards = React.useMemo(
    () => ({
      taskTotal,
      tasksByStatus,
      findings,
      findingStats,
      findingsBySev,
      totalAssets,
      trafficTotal,
      settings,
      displayedTokens,
      costs,
      ops,
      mobile,
      runtime,
    }),
    [
      taskTotal,
      tasksByStatus,
      findings,
      findingStats,
      findingsBySev,
      totalAssets,
      trafficTotal,
      settings,
      displayedTokens,
      costs,
      ops,
      mobile,
      runtime,
    ],
  );

  const opsWorkspace = React.useMemo(
    () => ({
      tasks: recentTasks,
      taskTotal,
      tasksByStatus,
      sortedTasks,
      taskFilter,
      setTaskFilter,
      controllingTaskId,
      controlTask,
      findings,
      recentFindings,
      findingStats,
      findingsBySev,
      activity,
      recentActivity: filteredActivity,
      activityRole,
      setActivityRole,
      kindLabel,
      workerBg,
    }),
    [
      recentTasks,
      taskTotal,
      tasksByStatus,
      sortedTasks,
      taskFilter,
      controllingTaskId,
      controlTask,
      findings,
      recentFindings,
      findingStats,
      findingsBySev,
      activity,
      filteredActivity,
      activityRole,
    ],
  );

  const telemetryPanel = React.useMemo(
    () => ({
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
      costs,
      llmPool,
    }),
    [
      llmProfiles,
      defaultProfileId,
      tokenVersion,
      tokenTab,
      tokenDays,
      tokenByProfile,
      displayedTokens,
      dailyTokenData,
      costs,
      llmPool,
    ],
  );

  const infraGrid = React.useMemo(
    () => ({
      stats,
      settings,
      pending,
      traffic,
      trafficTotal,
      totalAssets,
      assetByType,
      assetMax,
      trafficByCodes,
      trafficMax,
      recentTraffic,
      activeProfile,
      pendingCount,
      ops,
      system,
      runtime,
      mobile,
    }),
    [
      stats,
      settings,
      pending,
      traffic,
      trafficTotal,
      totalAssets,
      assetByType,
      assetMax,
      trafficByCodes,
      trafficMax,
      recentTraffic,
      activeProfile,
      pendingCount,
      ops,
      system,
      runtime,
      mobile,
    ],
  );

  return { initialLoading, headerSlice, statCards, opsWorkspace, telemetryPanel, infraGrid };
}
