"use client";

import * as React from "react";

import { toast } from "sonner";

import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import {
  buildDailyUsageSeries,
  buildUsageByProfileMap,
  collectExtraUsageProfiles,
  type DashboardTaskFilter,
  selectUsageProfileBucket,
  summarizeAllUsageTokens,
  summarizeDailyUsage,
  TOKEN_TAB_ALL,
} from "@/lib/dashboard-format";
import type {
  Activity,
  CostSummary,
  DashboardLLMPoolSummary,
  DashboardMobileSummary,
  DashboardOpsSummary,
  DashboardRuntimeSummary,
  DashboardSystemSummary,
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
export type TokenRangeDays = 7 | 30 | 90 | 180 | 365 | 3650;

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
  const [usageStats, setUsageStats] = React.useState<UsageStats | null>(null);
  const [costs, setCosts] = React.useState<CostSummary | null>(null);
  const [assetCounts, setAssetCounts] = React.useState<Record<string, number>>({});
  const [traffic, setTraffic] = React.useState<TrafficExchange[]>([]);
  const [trafficTotal, setTrafficTotal] = React.useState(0);
  const [llmProfiles, setLLMProfiles] = React.useState<LLMProfile[]>([]);

  const [tokenTab, setTokenTab] = React.useState<string>(TOKEN_TAB_ALL);
  const [tokenDays, setTokenDays] = React.useState<TokenRangeDays>(30);
  const [taskFilter, setTaskFilter] = React.useState<DashboardTaskFilter>("all");
  const [activityRole, setActivityRole] = React.useState<ActivityRoleFilter>("all");

  const [autoRefresh, setAutoRefresh] = React.useState(true);
  const [summaryReady, setSummaryReady] = React.useState(false);
  const [fastReady, setFastReady] = React.useState(false);
  const [slowReady, setSlowReady] = React.useState(false);
  const [lastSyncAt, setLastSyncAt] = React.useState<number | null>(null);
  const [refreshing, setRefreshing] = React.useState(false);
  const [controllingTaskId, setControllingTaskId] = React.useState<string | null>(null);

  const taskSignatureRef = React.useRef("");

  const applySummary = React.useCallback((summary: Awaited<ReturnType<typeof api.dashboardSummary>>) => {
    const nextSignature = JSON.stringify([
      summary.task_total,
      summary.tasks_by_status,
      summary.recent_tasks,
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
      const [recentPage, nextStats, nextSettings, pendingRecent, nextActivity, findingTotals] = await Promise.all([
        api.findingsPage({ page: 1, pageSize: 8, sort: "time" }),
        api.stats(),
        api.settings(),
        api.interceptPendingRecent(3),
        api.activity(undefined, { limit: 30 }),
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
      setLastSyncAt(Date.now());
    } finally {
      if (!alive || alive()) setFastReady(true);
    }
  }, []);

  const fetchSlowBatchOnce = React.useCallback(
    async (alive?: () => boolean) => {
      try {
        const [traf, counts, profileList, usage, costSummary] = await Promise.all([
          api.traffic(0, 50),
          api.assetCounts(),
          api.llmProfiles(),
          api.usageStats(tokenDays),
          api.costs(30),
        ]);
        if (alive && !alive()) return;
        setTraffic(traf.exchanges ?? []);
        setTrafficTotal(traf.count ?? traf.total ?? traf.exchanges?.length ?? 0);
        setAssetCounts(counts ?? {});
        setLLMProfiles(profileList);
        setUsageStats(usage);
        setCosts(costSummary);
      } finally {
        if (!alive || alive()) setSlowReady(true);
      }
    },
    [tokenDays],
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
      await fetchSlowBatchOnce(alive);
    },
    autoRefresh ? 15000 : 0,
    [autoRefresh, tokenDays, fetchSlowBatchOnce],
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

  const initialLoading = !(summaryReady && fastReady && slowReady);

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

  // llm_usage single-source attribution (exact profile_name match; zero cross-profile absorption)
  const usageByProfile = React.useMemo(() => buildUsageByProfileMap(usageStats), [usageStats]);
  const allUsageTokens = React.useMemo(() => summarizeAllUsageTokens(usageStats), [usageStats]);
  const extraUsageProfiles = React.useMemo(
    () => collectExtraUsageProfiles(usageStats, llmProfiles),
    [usageStats, llmProfiles],
  );

  React.useEffect(() => {
    if (tokenTab === TOKEN_TAB_ALL || !usageStats) return;
    const valid = new Set<string>([
      ...llmProfiles.map((p) => `profile:${p.name}`),
      ...extraUsageProfiles.map((e) => e.tabKey),
    ]);
    if (!valid.has(tokenTab)) setTokenTab(TOKEN_TAB_ALL);
  }, [tokenTab, llmProfiles, extraUsageProfiles, usageStats]);

  const selectedAllTimeTokens = React.useMemo(
    () => selectUsageProfileBucket(usageByProfile, usageStats, tokenTab),
    [usageByProfile, usageStats, tokenTab],
  );

  const dailyTokenData = React.useMemo(
    () => buildDailyUsageSeries(usageStats, tokenTab, tokenDays),
    [usageStats, tokenTab, tokenDays],
  );

  const windowTokens = React.useMemo(() => summarizeDailyUsage(dailyTokenData), [dailyTokenData]);

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
      allUsageTokens,
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
      allUsageTokens,
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
      extraUsageProfiles,
      tokenTab,
      setTokenTab,
      tokenDays,
      setTokenDays,
      usageByProfile,
      selectedAllTimeTokens,
      windowTokens,
      dailyTokenData,
      costs,
      llmPool,
    }),
    [
      llmProfiles,
      extraUsageProfiles,
      tokenTab,
      tokenDays,
      usageByProfile,
      selectedAllTimeTokens,
      windowTokens,
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
