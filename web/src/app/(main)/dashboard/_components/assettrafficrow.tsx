"use client";

import Link from "next/link";

import {
  ActivityIcon,
  ArrowUpRightIcon,
  CalendarClockIcon,
  CpuIcon,
  NetworkIcon,
  ShieldCheckIcon,
  SmartphoneIcon,
  TrophyIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { cn } from "@/lib/utils";

import {
  ASSET_COLORS,
  ASSET_TYPE_LABELS,
  computePercent,
  fmtBytes,
  fmtDurationSeconds,
  fmtScheduleUpcoming,
  SectionTitle,
  statusBg,
  statusColor,
} from "./dashboard-helpers";
import type { useDashboardState } from "./use-dashboard-state";

function sourceChip(source?: string): { text: string; cls: string } | null {
  if (source === "mobile_hook") {
    return { text: "Frida", cls: "border-violet-500/30 bg-violet-500/10 text-violet-400" };
  }
  if (source === "mobile") {
    return { text: "Mobile", cls: "border-sky-500/30 bg-sky-500/10 text-sky-400" };
  }
  return null;
}

export function RowAssetTraffic({ s }: { s: ReturnType<typeof useDashboardState>["infraGrid"] }) {
  const {
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
  } = s;

  const cpuPct = system?.supported ? Math.round(system.cpu.percent) : 0;
  const memPct = system?.supported ? Math.round(system.memory.percent) : 0;
  const diskPct = system?.supported ? Math.round(system.data_disk.percent) : 0;

  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
      {/* 1. 资产分布 */}
      <Card className="flex flex-col p-4">
        <SectionTitle icon={NetworkIcon} sub={`共 ${totalAssets}`} href="/function/assets">
          资产分布
        </SectionTitle>

        {assetByType.length === 0 ? (
          <div className="flex flex-1 items-center justify-center py-6 text-muted-foreground text-xs">暂无资产数据</div>
        ) : (
          <div className="flex flex-col gap-2.5">
            {assetByType.map(([type, count]) => (
              <div key={type} className="flex items-center gap-2 text-xs">
                <span className="w-11 shrink-0 text-right text-[10px] text-muted-foreground">
                  {ASSET_TYPE_LABELS[type] ?? type}
                </span>
                <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
                  <div
                    className={cn("h-full rounded-full transition-all", ASSET_COLORS[type] ?? "bg-slate-400")}
                    style={{ width: `${(count / assetMax) * 100}%` }}
                  />
                </div>
                <span className="w-14 shrink-0 text-right text-[10px] tabular-nums">
                  {count} <span className="text-muted-foreground">({computePercent(count, totalAssets)}%)</span>
                </span>
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* 2. 流量状态码与近期报文 */}
      <Card className="flex flex-col p-4">
        <SectionTitle icon={ActivityIcon} sub={`样本 ${traffic.length} / 总 ${trafficTotal}`} href="/function/traffic">
          流量态势
        </SectionTitle>

        {trafficByCodes.length === 0 ? (
          <div className="flex flex-1 items-center justify-center py-6 text-muted-foreground text-xs">暂无流量数据</div>
        ) : (
          <>
            <div className="mb-3 flex items-end gap-2" style={{ height: 52 }}>
              {trafficByCodes.map(({ code, n }) => (
                <div key={code} className="flex flex-1 flex-col items-center gap-1">
                  <span className="text-[9px] text-muted-foreground tabular-nums">{n}</span>
                  <div
                    className={cn("min-h-1 w-full rounded-sm opacity-80", statusBg(code))}
                    style={{ height: Math.max(4, (n / trafficMax) * 34) }}
                  />
                  <span className={cn("font-mono text-[9px]", statusColor(code))}>{code}</span>
                </div>
              ))}
            </div>

            <div className="mt-auto border-t pt-2.5">
              <div className="mb-1.5 text-[10px] text-muted-foreground">最近报文</div>
              <div className="flex flex-col gap-1.5">
                {recentTraffic.map((e) => {
                  const chip = sourceChip(e.source);
                  return (
                    <div key={e.id} className="flex items-center gap-1.5 text-[10px]">
                      <span
                        className={cn(
                          "shrink-0 rounded border px-1 py-0 font-mono text-[9px]",
                          e.method === "GET"
                            ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                            : "border-blue-500/30 bg-blue-500/10 text-blue-400",
                        )}
                      >
                        {e.method}
                      </span>
                      {chip && (
                        <span className={cn("shrink-0 rounded border px-1 py-0 text-[8px]", chip.cls)}>
                          {chip.text}
                        </span>
                      )}
                      <span className="min-w-0 flex-1 truncate text-muted-foreground">
                        {e.host}
                        {e.url.replace(/^https?:\/\/[^/]+/, "").slice(0, 26)}
                      </span>
                      <span className={cn("shrink-0 font-mono text-[9px]", statusColor(e.status))}>{e.status}</span>
                    </div>
                  );
                })}
              </div>
            </div>
          </>
        )}
      </Card>

      {/* 3. 移动测试 · 战果 · 计划任务 */}
      <Card className="flex flex-col gap-3 p-4">
        <SectionTitle icon={SmartphoneIcon} href="/function/loot" moreLabel="战果台账">
          移动与战果排期
        </SectionTitle>

        {/* Mobile summary */}
        <div className="rounded-lg border bg-muted/20 p-2.5 text-[10px]">
          <div className="mb-1 flex items-center justify-between">
            <span className="font-medium text-foreground">移动测试与真机</span>
            <Link href="/function/traffic" className="text-muted-foreground hover:text-foreground">
              接入向导 →
            </Link>
          </div>
          <div className="flex flex-wrap gap-x-3 gap-y-1 text-muted-foreground">
            <span>
              已登记 <strong className="text-foreground tabular-nums">{mobile?.devices_total ?? 0}</strong> 台
            </span>
            <span>
              已绑定 <strong className="text-foreground tabular-nums">{mobile?.devices_bound ?? 0}</strong>
            </span>
            <span>
              Root <strong className="text-foreground tabular-nums">{mobile?.devices_rooted ?? 0}</strong>
            </span>
            {(mobile?.pinning_alerts_count ?? 0) > 0 && (
              <span className="text-violet-400">Pinning 告警 {mobile?.pinning_alerts_count}</span>
            )}
          </div>
        </div>

        {/* Loot & footholds */}
        <div className="grid grid-cols-3 gap-1.5 text-center text-[10px]">
          <Link
            href="/function/footholds"
            className="rounded-md border bg-muted/20 p-2 transition-colors hover:bg-muted/40"
          >
            <div className="font-semibold text-sm tabular-nums">
              {ops?.footholds_active ?? 0}
              <span className="font-normal text-[10px] text-muted-foreground">/{ops?.footholds_total ?? 0}</span>
            </div>
            <div className="mt-0.5 flex items-center justify-center gap-0.5 text-[9px] text-muted-foreground">
              <TrophyIcon className="size-2.5" /> 在手据点
            </div>
          </Link>
          <Link
            href="/function/credentials"
            className="rounded-md border bg-muted/20 p-2 transition-colors hover:bg-muted/40"
          >
            <div className="font-semibold text-sm tabular-nums">{ops?.credentials_total ?? 0}</div>
            <div className="mt-0.5 text-[9px] text-muted-foreground">已获凭据</div>
          </Link>
          <Link
            href="/function/loot?kind=artifact"
            className="rounded-md border bg-muted/20 p-2 transition-colors hover:bg-muted/40"
          >
            <div
              className={cn(
                "font-semibold text-sm tabular-nums",
                (ops?.artifacts_uncleaned ?? 0) > 0 && "text-orange-500 dark:text-orange-400",
              )}
            >
              {ops?.artifacts_uncleaned ?? 0}
            </div>
            <div className="mt-0.5 text-[9px] text-muted-foreground">待清遗留</div>
          </Link>
        </div>

        {/* Upcoming schedule */}
        <Link
          href="/function/schedules"
          className="mt-auto flex items-center justify-between gap-2 rounded-lg border bg-muted/20 px-2.5 py-2 text-[10px] transition-colors hover:bg-muted/40"
        >
          <div className="min-w-0">
            <div className="flex items-center gap-1 font-medium">
              <CalendarClockIcon className="size-3 text-muted-foreground" />
              <span>
                计划任务 {ops?.schedules_enabled ?? 0}/{ops?.schedules_total ?? 0} 启用
              </span>
            </div>
            <div className="mt-0.5 truncate text-muted-foreground">
              {ops?.next_schedule_name
                ? `${ops.next_schedule_name} · ${fmtScheduleUpcoming(ops.next_schedule_at_unix)}`
                : "暂无即将执行的排期"}
            </div>
          </div>
          <ArrowUpRightIcon className="size-3 shrink-0 text-muted-foreground" />
        </Link>
      </Card>

      {/* 4. 运行基座与待审批队列 */}
      <Card className="flex flex-col gap-3 p-4">
        <SectionTitle icon={CpuIcon} href="/system/diagnostics" moreLabel="运行体检">
          运行基座与审批
        </SectionTitle>

        {system?.supported ? (
          <div className="space-y-2 text-[10px]">
            <div>
              <div className="mb-1 flex justify-between text-muted-foreground">
                <span>CPU</span>
                <span className="font-mono text-foreground tabular-nums">
                  {cpuPct}%{system.cpu_limit_cores ? ` · ${system.cpu_limit_cores}C` : ""}
                </span>
              </div>
              <Progress value={cpuPct} className="h-1" />
            </div>
            <div>
              <div className="mb-1 flex justify-between text-muted-foreground">
                <span>内存</span>
                <span className="font-mono text-foreground tabular-nums">
                  {memPct}% · {fmtBytes(system.memory.used_bytes)}
                </span>
              </div>
              <Progress value={memPct} className="h-1" />
            </div>
            <div>
              <div className="mb-1 flex justify-between text-muted-foreground">
                <span>数据盘</span>
                <span className="font-mono text-foreground tabular-nums">
                  {diskPct}% · {fmtBytes(system.data_disk.used_bytes)}
                </span>
              </div>
              <Progress value={diskPct} className="h-1" />
            </div>
          </div>
        ) : (
          <div className="rounded-lg border bg-muted/20 px-2.5 py-2 text-[10px] text-muted-foreground">
            系统资源采样当前平台不支持，可用运行体检查看详细 Go/DB 指标。
          </div>
        )}

        <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border bg-muted/20 px-2.5 py-1.5 text-[10px] text-muted-foreground">
          <span>运行 {fmtDurationSeconds(runtime?.uptime_seconds)}</span>
          <span>协程 {runtime?.goroutines ?? "—"}</span>
          <span>
            捕获{" "}
            <strong className={settings?.traffic_capture ? "text-emerald-400" : "text-muted-foreground"}>
              {settings?.traffic_capture ? "开" : "关"}
            </strong>
          </span>
          {activeProfile && <span className="truncate font-mono">{activeProfile.model}</span>}
        </div>

        {/* Pending Approvals */}
        {pendingCount > 0 ? (
          <div className="mt-auto space-y-1.5">
            <div className="flex items-center justify-between font-medium text-[10px] text-amber-400">
              <span>待审批拦截 ({pendingCount})</span>
              <Link href="/system/intercept/approvals" className="hover:underline">
                全部审批 →
              </Link>
            </div>
            {pending.slice(0, 2).map((p) => (
              <Link
                key={p.id}
                href="/system/intercept/approvals"
                className="flex items-center justify-between rounded-md border border-amber-500/25 bg-amber-500/5 px-2.5 py-1.5 hover:bg-amber-500/10"
              >
                <div className="min-w-0">
                  <div className="truncate font-medium text-[10px]">{p.tool_name}</div>
                  <div className="truncate text-[9px] text-muted-foreground">{p.agent_name}</div>
                </div>
                <ArrowUpRightIcon className="size-3 shrink-0 text-amber-400" />
              </Link>
            ))}
          </div>
        ) : (
          <div className="mt-auto flex items-center justify-between rounded-lg border bg-muted/10 px-2.5 py-2 text-[10px] text-muted-foreground">
            <span className="flex items-center gap-1.5">
              <ShieldCheckIcon className="size-3.5 text-emerald-500" />
              无待审批拦截
            </span>
            <Badge
              variant="outline"
              className={cn(
                "px-1.5 py-0 text-[9px]",
                stats?.llm_configured ? "border-emerald-500/30 text-emerald-400" : "border-red-500/30 text-red-400",
              )}
            >
              {stats?.llm_configured ? "LLM 就绪" : "LLM 未配置"}
            </Badge>
          </div>
        )}
      </Card>
    </div>
  );
}
