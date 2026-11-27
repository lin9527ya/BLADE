"use client";

import Link from "next/link";

import {
  AlertTriangleIcon,
  ArrowUpRightIcon,
  BotIcon,
  PauseIcon,
  PlayIcon,
  PlusIcon,
  RadioIcon,
  RefreshCwIcon,
  ShieldAlertIcon,
  SmartphoneIcon,
  Trash2Icon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

import { fmtClockTime, LiveDot } from "./dashboard-helpers";
import type { useDashboardState } from "./use-dashboard-state";

export function CommandHeader({
  s,
  onOpenCreate,
}: {
  s: ReturnType<typeof useDashboardState>["headerSlice"];
  onOpenCreate: () => void;
}) {
  const {
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
  } = s;

  const runningCount = tasksByStatus.running ?? 0;
  const pausedCount = tasksByStatus.paused ?? 0;
  const trippedCount = llmPool?.tripped_count ?? 0;
  const pinningAlerts = mobile?.pinning_alerts_count ?? 0;
  const uncleanedArtifacts = ops?.artifacts_uncleaned ?? 0;
  const scheduleErrors = ops?.schedules_error ?? 0;
  const unconfiguredLLM = stats !== null && !stats.llm_configured;

  const hasAlerts =
    pendingCount > 0 ||
    unconfiguredLLM ||
    trippedCount > 0 ||
    pinningAlerts > 0 ||
    uncleanedArtifacts > 0 ||
    scheduleErrors > 0;

  return (
    <div className="flex flex-col gap-3">
      {/* Top command bar */}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="font-semibold text-xl tracking-tight">安全作战总览</h1>
            <span
              className={cn(
                "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 font-medium text-[11px]",
                runningCount > 0
                  ? "border-blue-500/30 bg-blue-500/10 text-blue-500 dark:text-blue-400"
                  : "border-border bg-muted/40 text-muted-foreground",
              )}
            >
              {runningCount > 0 ? (
                <>
                  <LiveDot />
                  <span>探索中 · {runningCount} 任务活跃</span>
                </>
              ) : (
                <span>引擎待命 · 共 {taskTotal} 任务</span>
              )}
            </span>
            {pausedCount > 0 && (
              <span className="rounded-full border border-amber-500/30 bg-amber-500/10 px-2 py-0.5 text-[10px] text-amber-500 dark:text-amber-400">
                已暂停 {pausedCount}
              </span>
            )}
          </div>
          <p className="mt-1 flex flex-wrap items-center gap-2 text-muted-foreground text-xs">
            <span>全局攻击面、智能体探索、模型能耗与真机测试态势</span>
            <span className="text-border">·</span>
            <span className="tabular-nums">
              最近同步 {fmtClockTime(lastSyncAt)}（{autoRefresh ? "5s 自动轮询" : "自动轮询已暂停"}）
            </span>
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          <Button
            type="button"
            variant={autoRefresh ? "outline" : "secondary"}
            size="sm"
            onClick={() => setAutoRefresh((prev) => !prev)}
            title={autoRefresh ? "暂停首页自动轮询" : "恢复 5s/15s 自动轮询"}
          >
            {autoRefresh ? <PauseIcon className="size-3.5" /> : <PlayIcon className="size-3.5" />}
            {autoRefresh ? "自动刷新" : "已暂停"}
          </Button>
          <Button type="button" variant="outline" size="sm" onClick={() => void refreshNow()} disabled={refreshing}>
            <RefreshCwIcon className={cn("size-3.5", refreshing && "animate-spin")} />
            刷新
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href="/function/broadcast">
              <RadioIcon className="size-3.5" />
              全局播报
            </Link>
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href="/chat">
              <BotIcon className="size-3.5" />
              AI 对话
            </Link>
          </Button>
          <Button type="button" size="sm" onClick={onOpenCreate}>
            <PlusIcon className="size-3.5" />
            新建任务
          </Button>
        </div>
      </div>

      {/* Contextual Alert / Action Strip */}
      {hasAlerts && (
        <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-card px-3 py-2 text-xs">
          {pendingCount > 0 && (
            <Link
              href="/system/intercept/approvals"
              className="flex min-w-0 items-center gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 px-2.5 py-1 font-medium text-amber-600 transition-colors hover:bg-amber-500/15 dark:text-amber-400"
            >
              <ShieldAlertIcon className="size-3.5 shrink-0" />
              <span>
                {pendingCount} 条拦截待审批
                {pending[0] ? `（${pending[0].tool_name} · ${pending[0].agent_name}）` : ""}
              </span>
              <ArrowUpRightIcon className="size-3 shrink-0" />
            </Link>
          )}

          {unconfiguredLLM && (
            <Link
              href="/system/llm"
              className="flex items-center gap-1.5 rounded-md border border-red-500/40 bg-red-500/10 px-2.5 py-1 font-medium text-red-600 transition-colors hover:bg-red-500/15 dark:text-red-400"
            >
              <AlertTriangleIcon className="size-3.5 shrink-0" />
              <span>尚未配置可用 LLM 模型，前往配置</span>
              <ArrowUpRightIcon className="size-3 shrink-0" />
            </Link>
          )}

          {trippedCount > 0 && (
            <Link
              href="/system/llm"
              className="flex items-center gap-1.5 rounded-md border border-red-500/30 bg-red-500/10 px-2.5 py-1 text-red-500 transition-colors hover:bg-red-500/15 dark:text-red-400"
            >
              <AlertTriangleIcon className="size-3.5 shrink-0" />
              <span>{trippedCount} 个模型节点冷却熔断中</span>
              <ArrowUpRightIcon className="size-3 shrink-0" />
            </Link>
          )}

          {pinningAlerts > 0 && (
            <Link
              href="/function/traffic"
              className="flex items-center gap-1.5 rounded-md border border-violet-500/30 bg-violet-500/10 px-2.5 py-1 text-violet-600 transition-colors hover:bg-violet-500/15 dark:text-violet-400"
            >
              <SmartphoneIcon className="size-3.5 shrink-0" />
              <span>{pinningAlerts} 个域触发移动端 SSL Pinning 断连告警</span>
              <ArrowUpRightIcon className="size-3 shrink-0" />
            </Link>
          )}

          {uncleanedArtifacts > 0 && (
            <Link
              href="/function/loot?kind=artifact"
              className="flex items-center gap-1.5 rounded-md border border-orange-500/30 bg-orange-500/10 px-2.5 py-1 text-orange-600 transition-colors hover:bg-orange-500/15 dark:text-orange-400"
            >
              <Trash2Icon className="size-3.5 shrink-0" />
              <span>{uncleanedArtifacts} 项在目标遗留物待清理</span>
              <ArrowUpRightIcon className="size-3 shrink-0" />
            </Link>
          )}

          {scheduleErrors > 0 && (
            <Link
              href="/function/schedules"
              className="flex items-center gap-1.5 rounded-md border border-red-500/30 bg-red-500/10 px-2.5 py-1 text-red-500 transition-colors hover:bg-red-500/15 dark:text-red-400"
            >
              <AlertTriangleIcon className="size-3.5 shrink-0" />
              <span>{scheduleErrors} 条计划任务最近执行失败</span>
              <ArrowUpRightIcon className="size-3 shrink-0" />
            </Link>
          )}
        </div>
      )}
    </div>
  );
}
