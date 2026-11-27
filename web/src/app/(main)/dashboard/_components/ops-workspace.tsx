"use client";

import Link from "next/link";

import { ActivityIcon, ArrowUpRightIcon, BugIcon, ClockIcon, PauseIcon, PlayIcon, SmartphoneIcon } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";

import {
  computePercent,
  type DashboardTaskFilter,
  fmtDurationSeconds,
  fmtRel,
  matchesTaskFilter,
  resolveTaskElapsedSeconds,
} from "./dashboard-helpers";
import type { ActivityRoleFilter, useDashboardState } from "./use-dashboard-state";

const TASK_FILTER_TABS: { key: DashboardTaskFilter; label: string }[] = [
  { key: "all", label: "全部" },
  { key: "running", label: "运行中" },
  { key: "paused", label: "已暂停" },
  { key: "done", label: "已完结" },
  { key: "mobile", label: "移动端" },
];

const ROLE_FILTERS: { key: ActivityRoleFilter; label: string }[] = [
  { key: "all", label: "全部角色" },
  { key: "planner", label: "Planner" },
  { key: "mainagent", label: "MainAgent" },
  { key: "worker", label: "Worker" },
];

export function OpsWorkspace({ s }: { s: ReturnType<typeof useDashboardState>["opsWorkspace"] }) {
  const {
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
    recentActivity,
    activityRole,
    setActivityRole,
    kindLabel,
    workerBg,
  } = s;

  const visibleTasks = sortedTasks.filter((t) => matchesTaskFilter(t, taskFilter)).slice(0, 7);
  const mobileInWindow = sortedTasks.filter((t) => t.app_mode === "mobile").length;

  const totalFindings = findingStats?.total ?? findings.length;
  const critCount = findingStats?.critical ?? findingsBySev.critical;
  const highCount = findingStats?.high ?? findingsBySev.high;
  const medCount = findingStats?.medium ?? findingsBySev.medium;
  const lowCount = findingStats?.low ?? findingsBySev.low;
  const liveEventCount = activity.filter((a) => a.kind !== "usage").length;

  const filterCountFor = (key: DashboardTaskFilter): number => {
    switch (key) {
      case "all":
        return taskTotal;
      case "running":
        return (tasksByStatus.running ?? 0) + (tasksByStatus.queued ?? 0);
      case "paused":
        return tasksByStatus.paused ?? 0;
      case "done":
        return (
          (tasksByStatus.done ?? 0) +
          (tasksByStatus.failed ?? 0) +
          (tasksByStatus.timeout ?? 0) +
          (tasksByStatus.policy_blocked ?? 0)
        );
      case "mobile":
        return mobileInWindow;
    }
  };

  return (
    <div className="grid grid-cols-1 gap-3 xl:grid-cols-12">
      {/* Left 7/12: 实时任务矩阵 */}
      <Card className="flex flex-col overflow-hidden p-0 xl:col-span-7">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
          <div className="flex items-center gap-2 font-semibold text-xs">
            <ClockIcon className="size-3.5 text-muted-foreground" />
            <span>近期任务态势</span>
            <span className="font-normal text-[10px] text-muted-foreground">共 {taskTotal}</span>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <div className="flex flex-wrap gap-0.5 rounded-md border bg-muted/30 p-0.5">
              {TASK_FILTER_TABS.map(({ key, label }) => {
                const cnt = filterCountFor(key);
                return (
                  <button
                    key={key}
                    type="button"
                    onClick={() => setTaskFilter(key)}
                    className={cn(
                      "inline-flex items-center gap-1 rounded px-2 py-0.5 font-medium text-[10px] transition-colors",
                      taskFilter === key
                        ? "bg-background text-foreground shadow-sm"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {label}
                    {cnt > 0 && <span className="text-[9px] tabular-nums opacity-70">{cnt}</span>}
                  </button>
                );
              })}
            </div>
            <Link
              href="/function/tasks"
              className="inline-flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-foreground"
            >
              全部 <ArrowUpRightIcon className="size-3" />
            </Link>
          </div>
        </div>

        <div className="min-w-0 flex-1 overflow-x-auto">
          <table className="w-full min-w-[640px] border-collapse text-xs">
            <thead>
              <tr className="border-b bg-muted/20">
                {["任务", "状态 / 引擎", "目标进度", "风险点阵", "耗时 / 在途", "控制"].map((h) => (
                  <th
                    key={h}
                    className="whitespace-nowrap px-3 py-2 text-left font-semibold text-[9px] text-muted-foreground uppercase tracking-wider first:pl-4 last:pr-4 last:text-right"
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {visibleTasks.length === 0 ? (
                <tr>
                  <td colSpan={6} className="px-4 py-8 text-center text-muted-foreground text-xs">
                    当前筛选下暂无任务
                  </td>
                </tr>
              ) : (
                visibleTasks.map((t) => {
                  const goalsPct = t.goals_total ? Math.round(((t.goals_met ?? 0) / t.goals_total) * 100) : null;
                  const elapsed = resolveTaskElapsedSeconds(t);
                  const fCounts = t.findings;
                  const fSum =
                    (fCounts?.critical ?? 0) + (fCounts?.high ?? 0) + (fCounts?.medium ?? 0) + (fCounts?.low ?? 0);
                  const busy = controllingTaskId === t.id;
                  return (
                    <tr key={t.id} className="border-b transition-colors last:border-0 hover:bg-muted/30">
                      <td className="max-w-[185px] py-2.5 pr-2 pl-4">
                        <Link href={`/function/tasks/detail?id=${encodeURIComponent(t.id)}`} className="group block">
                          <div className="flex items-center gap-1.5">
                            <span className="truncate font-medium group-hover:underline">
                              {t.name || t.description}
                            </span>
                            {t.app_mode === "mobile" && (
                              <span
                                title={t.app_package_name || "移动 APP 测试"}
                                className="inline-flex shrink-0 items-center gap-0.5 rounded border border-sky-500/30 bg-sky-500/10 px-1 py-0 text-[9px] text-sky-500 dark:text-sky-400"
                              >
                                <SmartphoneIcon className="size-2.5" />
                                App
                              </span>
                            )}
                          </div>
                          <div className="mt-0.5 flex items-center gap-1.5 truncate font-mono text-[10px] text-muted-foreground">
                            <span>#{t.id}</span>
                            {t.category_name && <span>· {t.category_name}</span>}
                            {t.mobile_device_id && <span>· {t.mobile_device_id}</span>}
                          </div>
                        </Link>
                      </td>

                      <td className="px-3 py-2.5">
                        <div className="flex flex-wrap items-center gap-1">
                          <StatusBadge domain="task" value={t.status} dot className="px-1.5 py-0 text-[10px]" />
                          <StatusBadge
                            domain="engine"
                            value={t.engine_mode ?? "idle"}
                            className="px-1.5 py-0 text-[10px]"
                          />
                        </div>
                      </td>

                      <td className="px-3 py-2.5">
                        {goalsPct !== null ? (
                          <div className="flex items-center gap-1.5 whitespace-nowrap">
                            <Progress value={goalsPct} className="h-1.5 w-10" />
                            <span className="text-[11px] text-muted-foreground tabular-nums">
                              {t.goals_met ?? 0}/{t.goals_total}
                            </span>
                          </div>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </td>

                      <td className="px-3 py-2.5">
                        {fSum > 0 ? (
                          <div className="flex items-center gap-1 whitespace-nowrap font-mono text-[10px] tabular-nums">
                            {(fCounts?.critical ?? 0) > 0 && (
                              <span className="rounded bg-rose-500/15 px-1 text-rose-500">C{fCounts?.critical}</span>
                            )}
                            {(fCounts?.high ?? 0) > 0 && (
                              <span className="rounded bg-red-500/15 px-1 text-red-400">H{fCounts?.high}</span>
                            )}
                            {(fCounts?.medium ?? 0) > 0 && (
                              <span className="rounded bg-amber-500/15 px-1 text-amber-400">M{fCounts?.medium}</span>
                            )}
                            {(fCounts?.low ?? 0) > 0 && (
                              <span className="rounded bg-slate-500/15 px-1 text-slate-400">L{fCounts?.low}</span>
                            )}
                          </div>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </td>

                      <td className="whitespace-nowrap px-3 py-2.5 text-[11px] tabular-nums">
                        <div className="flex items-center gap-1.5">
                          <span className="text-foreground">{fmtDurationSeconds(elapsed)}</span>
                          {(t.in_flight ?? 0) > 0 && (
                            <Badge
                              variant="outline"
                              className="whitespace-nowrap border-blue-500/30 px-1 py-0 text-[9px] text-blue-400"
                            >
                              {t.in_flight} 在途
                            </Badge>
                          )}
                        </div>
                        <div className="text-[10px] text-muted-foreground">
                          {fmtRel(t.last_activity_unix ?? t.created_unix)}
                        </div>
                      </td>

                      <td className="py-2.5 pr-4 pl-3 text-right">
                        <div className="inline-flex items-center gap-1">
                          {t.status === "running" && (
                            <Button
                              type="button"
                              variant="ghost"
                              size="icon"
                              className="size-7 text-amber-500 hover:text-amber-400"
                              title="暂停任务"
                              disabled={busy}
                              onClick={() => void controlTask(t.id, "pause")}
                            >
                              <PauseIcon className="size-3.5" />
                            </Button>
                          )}
                          {t.status === "paused" && (
                            <Button
                              type="button"
                              variant="ghost"
                              size="icon"
                              className="size-7 text-emerald-500 hover:text-emerald-400"
                              title="恢复任务"
                              disabled={busy}
                              onClick={() => void controlTask(t.id, "resume")}
                            >
                              <PlayIcon className="size-3.5" />
                            </Button>
                          )}
                          <Button variant="ghost" size="icon" className="size-7" title="查看任务详情" asChild>
                            <Link href={`/function/tasks/detail?id=${encodeURIComponent(t.id)}`}>
                              <ArrowUpRightIcon className="size-3.5" />
                            </Link>
                          </Button>
                        </div>
                      </td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
      </Card>

      {/* Right 5/12: 发现与实时活动流双 Tab */}
      <Card className="flex flex-col p-4 xl:col-span-5">
        <Tabs defaultValue="findings" className="flex h-full flex-col gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <TabsList className="h-7 p-0.5">
              <TabsTrigger value="findings" className="gap-1 px-2.5 py-0.5 text-[11px]">
                <BugIcon className="size-3" />
                最新发现
                {totalFindings > 0 && <span className="text-[10px] tabular-nums opacity-70">({totalFindings})</span>}
              </TabsTrigger>
              <TabsTrigger value="activity" className="gap-1 px-2.5 py-0.5 text-[11px]">
                <ActivityIcon className="size-3" />
                活动流
                {liveEventCount > 0 && <span className="text-[10px] tabular-nums opacity-70">({liveEventCount})</span>}
              </TabsTrigger>
            </TabsList>

            <Link
              href="/function/findings"
              className="inline-flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-foreground"
            >
              发现列表 <ArrowUpRightIcon className="size-3" />
            </Link>
          </div>

          <TabsContent value="findings" className="flex flex-1 flex-col gap-2.5">
            {totalFindings > 0 && (
              <div className="space-y-1.5 rounded-md border bg-muted/20 p-2">
                <div className="flex h-1.5 overflow-hidden rounded-full bg-muted">
                  <div className="bg-rose-500" style={{ width: `${computePercent(critCount, totalFindings)}%` }} />
                  <div className="bg-red-400" style={{ width: `${computePercent(highCount, totalFindings)}%` }} />
                  <div className="bg-amber-400" style={{ width: `${computePercent(medCount, totalFindings)}%` }} />
                  <div className="bg-slate-400" style={{ width: `${computePercent(lowCount, totalFindings)}%` }} />
                </div>
                <div className="flex items-center justify-between text-[10px] text-muted-foreground tabular-nums">
                  <span className="text-rose-500">严重 {critCount}</span>
                  <span className="text-red-400">高危 {highCount}</span>
                  <span className="text-amber-400">中危 {medCount}</span>
                  <span className="text-slate-400">低危 {lowCount}</span>
                </div>
              </div>
            )}

            <div className="divide-y">
              {recentFindings.length === 0 ? (
                <div className="py-8 text-center text-muted-foreground text-xs">暂无安全发现</div>
              ) : (
                recentFindings.map((f) => {
                  const fid = f.finding_id ?? f.id;
                  const href = fid
                    ? `/function/findings/detail?id=${encodeURIComponent(String(fid))}${f.task_id ? `&context_task=${encodeURIComponent(String(f.task_id))}` : ""}`
                    : "/function/findings";
                  return (
                    <Link
                      key={f.id}
                      href={href}
                      className="flex items-start gap-2 py-2 transition-colors hover:bg-muted/30"
                    >
                      <StatusBadge
                        domain="severity"
                        value={f.severity}
                        className="mt-0.5 shrink-0 px-1.5 py-0 text-[10px]"
                      />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-center gap-1.5">
                          <span className="font-semibold text-[11px]">{f.name || f.vulnclass}</span>
                          {f.task_description && (
                            <span className="truncate text-[10px] text-muted-foreground">{f.task_description}</span>
                          )}
                        </div>
                        <div className="mt-0.5 line-clamp-1 text-[10px] text-muted-foreground">{f.summary}</div>
                      </div>
                      <span className="shrink-0 text-[9px] text-muted-foreground tabular-nums">{fmtRel(f.ts)}</span>
                    </Link>
                  );
                })
              )}
            </div>
          </TabsContent>

          <TabsContent value="activity" className="flex flex-1 flex-col gap-2.5">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex flex-wrap gap-0.5 rounded-md border bg-muted/30 p-0.5">
                {ROLE_FILTERS.map(({ key, label }) => (
                  <button
                    key={key}
                    type="button"
                    onClick={() => setActivityRole(key)}
                    className={cn(
                      "rounded px-2 py-0.5 font-medium text-[9px] transition-colors",
                      activityRole === key
                        ? "bg-background text-foreground shadow-sm"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <Link
                href="/function/broadcast"
                className="inline-flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-foreground"
              >
                全局播报 <ArrowUpRightIcon className="size-3" />
              </Link>
            </div>

            <div className="divide-y">
              {recentActivity.length === 0 ? (
                <div className="py-8 text-center text-muted-foreground text-xs">当前角色暂无活动记录</div>
              ) : (
                recentActivity.map((a) => (
                  <div key={a.seq} className="flex gap-2 py-2">
                    <span
                      className={cn(
                        "shrink-0 self-start rounded border px-1.5 py-0.5 font-mono text-[9px]",
                        workerBg(a.worker),
                      )}
                    >
                      {a.worker}
                    </span>
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium text-[11px]">{kindLabel(a)}</div>
                      <div className="mt-0.5 truncate text-[10px] text-muted-foreground">{a.summary}</div>
                    </div>
                    <div className="shrink-0 text-right">
                      <Badge variant="outline" className="px-1.5 py-0 text-[9px] text-muted-foreground">
                        {a.kind}
                      </Badge>
                      <div className="mt-0.5 text-[9px] text-muted-foreground tabular-nums">{fmtRel(a.ts)}</div>
                    </div>
                  </div>
                ))
              )}
            </div>
          </TabsContent>
        </Tabs>
      </Card>
    </div>
  );
}
