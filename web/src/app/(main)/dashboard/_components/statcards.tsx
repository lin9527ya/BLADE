"use client";

import Link from "next/link";

import {
  ActivityIcon,
  ArrowUpRightIcon,
  BugIcon,
  CoinsIcon,
  NetworkIcon,
  SmartphoneIcon,
  TargetIcon,
} from "lucide-react";

import { Card, CardContent, CardHeader } from "@/components/ui/card";
import { cacheHitRate } from "@/lib/token-stats";
import { cn } from "@/lib/utils";

import { computePercent, fmtTokens, LiveDot, localDateKey } from "./dashboard-helpers";
import type { useDashboardState } from "./use-dashboard-state";

function compactUsd(n: number): string {
  if (!Number.isFinite(n)) return "—";
  if (n >= 100) return `$${n.toFixed(0)}`;
  return `$${n.toFixed(2)}`;
}

export function RowStatCards({ s }: { s: ReturnType<typeof useDashboardState>["statCards"] }) {
  const {
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
  } = s;

  const running = tasksByStatus.running ?? 0;
  const paused = tasksByStatus.paused ?? 0;
  const done = tasksByStatus.done ?? 0;
  const failed = (tasksByStatus.failed ?? 0) + (tasksByStatus.timeout ?? 0) + (tasksByStatus.policy_blocked ?? 0);

  const totalFindings = findingStats?.total ?? findings.length;
  const critCount = findingStats?.critical ?? findingsBySev.critical;
  const highCount = findingStats?.high ?? findingsBySev.high;
  const medCount = findingStats?.medium ?? findingsBySev.medium;
  const lowCount = findingStats?.low ?? findingsBySev.low;
  const pendingFindings = findingStats?.pending ?? 0;

  const todayKey = localDateKey();
  const todayCost = costs?.daily.find((d) => d.date === todayKey)?.cost_usd ?? 0;
  const monthCost = costs?.month.cost_usd ?? 0;
  const budget = costs?.budget_usd ?? 0;
  const budgetPct = budget > 0 ? computePercent(monthCost, budget) : 0;
  const totalTokens = displayedTokens.input + displayedTokens.output;
  const hasCostData = monthCost > 0 || todayCost > 0 || budget > 0;

  let budgetBarClass = "bg-emerald-500";
  if (budgetPct >= 100) budgetBarClass = "bg-red-500";
  else if (budgetPct >= 80) budgetBarClass = "bg-amber-400";

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5">
      {/* 1. 任务态势 */}
      <Link href="/function/tasks" className="group block">
        <Card className="h-full gap-2 p-4 transition-colors hover:border-foreground/25 hover:bg-muted/20">
          <CardHeader className="p-0">
            <div className="flex items-center justify-between text-[11px] text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <TargetIcon className="size-3.5" />
                任务态势
              </span>
              <ArrowUpRightIcon className="size-3.5 opacity-40 transition-opacity group-hover:opacity-100" />
            </div>
            <div className="flex items-baseline justify-between gap-2">
              <div className="flex items-baseline gap-1.5">
                <span className="font-bold text-2xl tabular-nums">{running}</span>
                <span className="text-muted-foreground text-xs">/ {taskTotal}</span>
              </div>
              {(mobile?.mobile_tasks_total ?? 0) > 0 && (
                <span className="inline-flex items-center gap-1 rounded border border-sky-500/30 bg-sky-500/10 px-1.5 py-0 text-[10px] text-sky-500 dark:text-sky-400">
                  <SmartphoneIcon className="size-2.5" />
                  移动 {mobile?.mobile_tasks_total}
                </span>
              )}
            </div>
          </CardHeader>
          <CardContent className="mt-auto space-y-2 p-0">
            <div className="flex h-1.5 overflow-hidden rounded-full bg-muted">
              {taskTotal > 0 && (
                <>
                  <div
                    className="bg-blue-500 transition-all"
                    style={{ width: `${computePercent(running, taskTotal)}%` }}
                  />
                  <div
                    className="bg-amber-400 transition-all"
                    style={{ width: `${computePercent(paused, taskTotal)}%` }}
                  />
                  <div
                    className="bg-emerald-500 transition-all"
                    style={{ width: `${computePercent(done, taskTotal)}%` }}
                  />
                  <div
                    className="bg-red-400 transition-all"
                    style={{ width: `${computePercent(failed, taskTotal)}%` }}
                  />
                </>
              )}
            </div>
            <div className="flex flex-wrap gap-x-2.5 gap-y-0.5 text-[10px] text-muted-foreground">
              <span className="text-blue-400">运行 {running}</span>
              <span className="text-amber-400">暂停 {paused}</span>
              <span className="text-emerald-400">完成 {done}</span>
              {failed > 0 && <span className="text-red-400">异常 {failed}</span>}
            </div>
          </CardContent>
        </Card>
      </Link>

      {/* 2. 风险发现 */}
      <Link href="/function/findings" className="group block">
        <Card className="h-full gap-2 p-4 transition-colors hover:border-foreground/25 hover:bg-muted/20">
          <CardHeader className="p-0">
            <div className="flex items-center justify-between text-[11px] text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <BugIcon className="size-3.5" />
                风险发现
              </span>
              <ArrowUpRightIcon className="size-3.5 opacity-40 transition-opacity group-hover:opacity-100" />
            </div>
            <div className="flex items-baseline justify-between gap-2">
              <span className="font-bold text-2xl tabular-nums">{totalFindings}</span>
              {pendingFindings > 0 && (
                <span className="rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0 text-[10px] text-amber-500 dark:text-amber-400">
                  待研判 {pendingFindings}
                </span>
              )}
            </div>
          </CardHeader>
          <CardContent className="mt-auto space-y-2 p-0">
            <div className="flex h-1.5 overflow-hidden rounded-full bg-muted">
              {totalFindings > 0 && (
                <>
                  <div
                    className="bg-rose-500 transition-all"
                    style={{ width: `${computePercent(critCount, totalFindings)}%` }}
                  />
                  <div
                    className="bg-red-400 transition-all"
                    style={{ width: `${computePercent(highCount, totalFindings)}%` }}
                  />
                  <div
                    className="bg-amber-400 transition-all"
                    style={{ width: `${computePercent(medCount, totalFindings)}%` }}
                  />
                  <div
                    className="bg-slate-400 transition-all"
                    style={{ width: `${computePercent(lowCount, totalFindings)}%` }}
                  />
                </>
              )}
            </div>
            <div className="flex flex-wrap gap-x-2.5 gap-y-0.5 text-[10px]">
              <span className="text-rose-500">严重 {critCount}</span>
              <span className="text-red-400">高 {highCount}</span>
              <span className="text-amber-400">中 {medCount}</span>
              <span className="text-slate-400">低 {lowCount}</span>
            </div>
          </CardContent>
        </Card>
      </Link>

      {/* 3. 资产与战果 */}
      <Link href="/function/assets" className="group block">
        <Card className="h-full gap-2 p-4 transition-colors hover:border-foreground/25 hover:bg-muted/20">
          <CardHeader className="p-0">
            <div className="flex items-center justify-between text-[11px] text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <NetworkIcon className="size-3.5" />
                资产与战果
              </span>
              <ArrowUpRightIcon className="size-3.5 opacity-40 transition-opacity group-hover:opacity-100" />
            </div>
            <div className="flex items-baseline gap-1.5">
              <span className="font-bold text-2xl tabular-nums">{totalAssets}</span>
              <span className="text-muted-foreground text-xs">资产节点</span>
            </div>
          </CardHeader>
          <CardContent className="mt-auto space-y-1 p-0 text-[10px] text-muted-foreground">
            <div className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5">
              <span>
                据点 <strong className="text-foreground tabular-nums">{ops?.footholds_active ?? 0}</strong>/
                {ops?.footholds_total ?? 0}
              </span>
              <span>
                凭据 <strong className="text-foreground tabular-nums">{ops?.credentials_total ?? 0}</strong>
              </span>
              <span className={cn((ops?.artifacts_uncleaned ?? 0) > 0 && "text-orange-500 dark:text-orange-400")}>
                遗留 <strong className="tabular-nums">{ops?.artifacts_uncleaned ?? 0}</strong>
              </span>
            </div>
          </CardContent>
        </Card>
      </Link>

      {/* 4. 流量与移动接入 */}
      <Link href="/function/traffic" className="group block">
        <Card className="h-full gap-2 p-4 transition-colors hover:border-foreground/25 hover:bg-muted/20">
          <CardHeader className="p-0">
            <div className="flex items-center justify-between text-[11px] text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <ActivityIcon className="size-3.5" />
                流量与真机
              </span>
              <ArrowUpRightIcon className="size-3.5 opacity-40 transition-opacity group-hover:opacity-100" />
            </div>
            <div className="flex items-baseline justify-between gap-2">
              <span className="font-bold text-2xl tabular-nums">{trafficTotal}</span>
              <span className="inline-flex items-center gap-1 text-[10px] text-muted-foreground">
                {settings?.traffic_capture ? (
                  <>
                    <LiveDot />
                    <span className="text-blue-400">录制中</span>
                  </>
                ) : (
                  <span>未开启</span>
                )}
              </span>
            </div>
          </CardHeader>
          <CardContent className="mt-auto space-y-1 p-0 text-[10px] text-muted-foreground">
            <div className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5">
              <span>
                设备 <strong className="text-foreground tabular-nums">{mobile?.devices_total ?? 0}</strong>（绑定{" "}
                {mobile?.devices_bound ?? 0}）
              </span>
              {mobile?.proxy_transparent_running && <span className="text-emerald-500">透明代理</span>}
              {mobile?.proxy_lan_running && !mobile?.proxy_transparent_running && (
                <span className="text-blue-400">LAN 代理</span>
              )}
              {(mobile?.pinning_alerts_count ?? 0) > 0 && (
                <span className="text-violet-400">Pinning {mobile?.pinning_alerts_count}</span>
              )}
            </div>
          </CardContent>
        </Card>
      </Link>

      {/* 5. LLM 成本与效能 */}
      <Link href="/system/llm" className="group block">
        <Card className="h-full gap-2 p-4 transition-colors hover:border-foreground/25 hover:bg-muted/20">
          <CardHeader className="p-0">
            <div className="flex items-center justify-between text-[11px] text-muted-foreground">
              <span className="flex items-center gap-1.5">
                <CoinsIcon className="size-3.5" />
                LLM 用量与预算
              </span>
              <ArrowUpRightIcon className="size-3.5 opacity-40 transition-opacity group-hover:opacity-100" />
            </div>
            <div className="flex items-baseline justify-between gap-2">
              {hasCostData ? (
                <span className="font-bold text-2xl tabular-nums">{compactUsd(monthCost)}</span>
              ) : (
                <span className="font-bold text-2xl tabular-nums">
                  {totalTokens > 0 ? fmtTokens(totalTokens) : "—"}
                </span>
              )}
              {(runtime?.llm_in_flight ?? 0) > 0 && (
                <span className="shrink-0 whitespace-nowrap rounded border border-blue-500/30 bg-blue-500/10 px-1.5 py-0 text-[10px] text-blue-400 tabular-nums">
                  在途 {runtime?.llm_in_flight}
                </span>
              )}
            </div>
          </CardHeader>
          <CardContent className="mt-auto space-y-1.5 p-0">
            {budget > 0 && (
              <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                <div className={cn("h-full transition-all", budgetBarClass)} style={{ width: `${budgetPct}%` }} />
              </div>
            )}
            <div className="flex flex-wrap items-center justify-between gap-1 text-[10px] text-muted-foreground">
              <span>
                {hasCostData && <span className="mr-1.5 tabular-nums">今日 {compactUsd(todayCost)} ·</span>}
                命中{" "}
                <strong className="text-emerald-400">
                  {cacheHitRate(displayedTokens.cacheRead, displayedTokens.input)}
                </strong>
              </span>
              <span className="tabular-nums">{fmtTokens(totalTokens)} tok</span>
            </div>
          </CardContent>
        </Card>
      </Link>
    </div>
  );
}
