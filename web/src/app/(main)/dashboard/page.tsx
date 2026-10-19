"use client";

import * as React from "react";

import Link from "next/link";
import dynamic from "next/dynamic";

import { RowActivity } from "./_components/activityrow";
import { RowAssetTraffic } from "./_components/assettrafficrow";
import { RowStatCards } from "./_components/statcards";
import { RowTaskTable } from "./_components/tasktable";
import { useDashboardState } from "./_components/use-dashboard-state";

// RowTokenCard 是整站唯一用 recharts 的地方，而 recharts 会打进首屏 chunk
// （实测 372KB）。图表在首屏下方、不是立刻可见，所以懒加载：首屏不再背这个包。
// ssr:false 在静态导出下是安全的——本组件不在构建期渲染。
const RowTokenCard = dynamic(() => import("./_components/tokencard").then((m) => m.RowTokenCard), { ssr: false });

// ── chart constants ───────────────────────────────────────────────────────────

// ── helpers ──────────────────────────────────────────────────────────────────

export default function DashboardPage() {
  const s = useDashboardState();
  const {
    tasks,
    setTasks,
    findings,
    setFindings,
    stats,
    setStats,
    settings,
    setSettings,
    pending,
    setPending,
    activity,
    setActivity,
    tokens,
    setTokens,
    convTokens,
    setConvTokens,
    usageStats,
    setUsageStats,
    assetCounts,
    setAssetCounts,
    traffic,
    setTraffic,
    agents,
    setAgents,
    mcpServers,
    setMcpServers,
    skills,
    setSkills,
    tools,
    setTools,
    llmProfiles,
    setLLMProfiles,
    taskSignatureRef,
    tasksByStatus,
    findingsBySev,
    sortedTasks,
    recentFindings,
    recentActivity,
    totalAssets,
    assetByType,
    assetMax,
    trafficByCodes,
    trafficMax,
    recentTraffic,
    activeProfile,
    enabledTools,
    pendingCount,
    defaultProfileId,
    tokenVersion,
    setTokenVersion,
    tokenTab,
    setTokenTab,
    tokenDays,
    setTokenDays,
    profileIdByName,
    tokenByProfileOld,
    tokenByProfileNew,
    tokenByProfile,
    displayedTokens,
    dailyTokenDataOld,
    dailyTokenDataNew,
    dailyTokenData,
    kindLabel,
    workerColor,
    workerBg,
  } = s;

  // ── render ────────────────────────────────────────────────────────────────

  return (
    <div className="flex flex-1 flex-col gap-4 pb-6">
      {/* ── Header ── */}
      <div>
        <h1 className="text-xl font-semibold tracking-tight">总览</h1>
        <p className="text-muted-foreground text-sm">系统全局状态 · 实时刷新</p>
      </div>

      {/* ── Row 1: 5 stat cards ── */}
      <RowStatCards s={s} />

      {/* ── Row 2: LLM Token 消耗 ── */}
      <RowTokenCard s={s} />

      {/* ── Row 3: 活动流 | 发现 ── */}
      <RowActivity s={s} />
      {/* ── Row 4: 任务表格 ── */}
      <RowTaskTable s={s} />

      {/* ── Row 5: 资产分布 | 流量状态码 | 拦截 & 待审批 ── */}
      <RowAssetTraffic s={s} />
    </div>
  );
}
