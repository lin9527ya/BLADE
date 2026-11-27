"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpRightIcon, CoinsIcon, ZapIcon } from "lucide-react";
import { Area, AreaChart, Bar, BarChart, CartesianGrid, XAxis, YAxis } from "recharts";

import { Card } from "@/components/ui/card";
import { ChartContainer, ChartTooltip, ChartTooltipContent } from "@/components/ui/chart";
import { Progress } from "@/components/ui/progress";
import { cacheHitRate } from "@/lib/token-stats";
import { cn } from "@/lib/utils";

import {
  costTrendConfig,
  dailyTrendConfig,
  fmtTokens,
  profileTabKey,
  TOKEN_TAB_ALL,
  utcDateKey,
} from "./dashboard-helpers";
import type { TokenRangeDays, useDashboardState } from "./use-dashboard-state";

type TelemetryMode = "tokens" | "costs";

const RANGE_OPTIONS: readonly { days: TokenRangeDays; label: string; headline: string }[] = [
  { days: 7, label: "7天", headline: "近 7 天 (输入+输出)" },
  { days: 30, label: "30天", headline: "近 30 天 (输入+输出)" },
  { days: 90, label: "3月", headline: "近 3 月 (输入+输出)" },
  { days: 180, label: "6月", headline: "近 6 月 (输入+输出)" },
  { days: 365, label: "一年", headline: "近一年 (输入+输出)" },
  { days: 3650, label: "全部", headline: "全量累计 (输入+输出)" },
];

function usd(n: number): string {
  if (!Number.isFinite(n)) return "—";
  if (n >= 100) return `$${n.toFixed(0)}`;
  return `$${n.toFixed(2)}`;
}

function budgetLabel(status?: string, budget?: number): string {
  if (!budget || !status || status === "unset") return "";
  if (status === "exceeded") return "超预算";
  if (status === "warning") return "接近预算";
  return "预算内";
}

function budgetBadgeStyle(status: string): { text: string; cls: string } {
  if (status === "exceeded") return { text: "已超预算", cls: "border-red-500/40 bg-red-500/10 text-red-400" };
  if (status === "warning") return { text: "接近预算", cls: "border-amber-500/40 bg-amber-500/10 text-amber-400" };
  if (status === "ok") return { text: "预算正常", cls: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400" };
  return { text: "未设预算", cls: "border-border text-muted-foreground" };
}

function poolDotClass(state: string, excluded: boolean): string {
  if (excluded) return "bg-slate-400";
  if (state === "tripped") return "bg-red-500";
  if (state === "half_open") return "bg-blue-400";
  if (state === "degraded") return "bg-amber-400";
  return "bg-emerald-500";
}

export function TelemetryPanel({ s }: { s: ReturnType<typeof useDashboardState>["telemetryPanel"] }) {
  const [mode, setMode] = React.useState<TelemetryMode>("tokens");
  const gradId = React.useId().replaceAll(":", "");

  const {
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
  } = s;

  const isAllTimeRange = tokenDays >= 3650;
  const activeRange = RANGE_OPTIONS.find((opt) => opt.days === tokenDays) ?? RANGE_OPTIONS[1];
  const primaryMetrics = isAllTimeRange ? selectedAllTimeTokens : windowTokens;
  const primaryTotal = primaryMetrics.input + primaryMetrics.output;
  const primaryMiss = isAllTimeRange
    ? Math.max(0, selectedAllTimeTokens.input - selectedAllTimeTokens.cacheRead)
    : windowTokens.miss;
  const allTimeTotal = selectedAllTimeTokens.input + selectedAllTimeTokens.output;

  const todayKey = utcDateKey();
  const todayCost = costs?.daily.find((d) => d.date === todayKey)?.cost_usd ?? 0;
  const monthCost = costs?.month.cost_usd ?? 0;
  const budget = costs?.budget_usd ?? 0;
  const budgetPct = budget > 0 ? Math.min(100, (monthCost / budget) * 100) : 0;
  const status = costs?.budget_status ?? "unset";
  const badge = budgetBadgeStyle(status);

  const costSeries = React.useMemo(
    () =>
      (costs?.daily ?? []).slice(-30).map((d) => ({
        date: d.date.slice(5),
        fullDate: d.date,
        cost_usd: d.cost_usd,
      })),
    [costs],
  );
  const topModels = costs?.by_model.filter((m) => m.cost_usd > 0).slice(0, 4) ?? [];
  const topCompanies = costs?.by_company.filter((c) => c.cost_usd > 0).slice(0, 2) ?? [];
  const topCampaigns = costs?.by_campaign.filter((c) => c.cost_usd > 0).slice(0, 2) ?? [];
  const peakCost = costs?.total.peak_cost_usd ?? 0;
  const offpeakCost = costs?.total.offpeak_cost_usd ?? 0;

  return (
    <Card className="p-4">
      {/* Unified Header */}
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex items-center gap-1.5 font-semibold text-xs">
            {mode === "tokens" ? (
              <ZapIcon className="size-3.5 text-muted-foreground" />
            ) : (
              <CoinsIcon className="size-3.5 text-muted-foreground" />
            )}
            LLM 遥测与成本中心
          </div>

          <div className="flex gap-0.5 rounded-md border bg-muted/30 p-0.5">
            {(
              [
                { k: "tokens", label: "Token 趋势" },
                { k: "costs", label: "成本与预算" },
              ] as const
            ).map(({ k, label }) => (
              <button
                key={k}
                type="button"
                onClick={() => setMode(k)}
                className={cn(
                  "rounded px-2.5 py-0.5 font-medium text-[10px] transition-colors",
                  mode === k
                    ? "bg-background text-foreground shadow-sm"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {label}
              </button>
            ))}
          </div>

          <span
            title="单一精准计量来源：llm_usage 常驻账本，含全部成功、失败、中断及独立对话调用，按 UTC 自然日汇总"
            className="rounded border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 font-mono text-[9px] text-emerald-500"
          >
            llm_usage 账本
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-1.5">
          {mode === "tokens" ? (
            <div className="flex flex-wrap items-center gap-1">
              <button
                type="button"
                onClick={() => setTokenTab(TOKEN_TAB_ALL)}
                className={cn(
                  "rounded-md px-2.5 py-1 font-medium text-[10px] transition-colors",
                  tokenTab === TOKEN_TAB_ALL
                    ? "bg-foreground text-background"
                    : "bg-muted/30 text-muted-foreground hover:text-foreground",
                )}
              >
                全部
              </button>
              {llmProfiles.map((p) => {
                const key = profileTabKey(p.name);
                const bucket = usageByProfile.get(p.name);
                const hasData = Boolean(
                  bucket && (bucket.calls > 0 || bucket.input > 0 || bucket.output > 0 || bucket.cacheRead > 0),
                );
                return (
                  <button
                    type="button"
                    key={p.id}
                    onClick={() => setTokenTab(key)}
                    className={cn(
                      "flex items-center gap-1 rounded-md px-2.5 py-1 font-medium text-[10px] transition-colors",
                      tokenTab === key
                        ? "bg-foreground text-background"
                        : "bg-muted/30 text-muted-foreground hover:text-foreground",
                      !hasData && "opacity-45",
                    )}
                  >
                    {p.name}
                    {p.is_default && (
                      <span
                        className={cn(
                          "rounded px-1 py-0 text-[8px]",
                          tokenTab === key ? "bg-background/20 text-background" : "bg-emerald-500/20 text-emerald-400",
                        )}
                      >
                        默认
                      </span>
                    )}
                  </button>
                );
              })}
              {extraUsageProfiles.map((extra) => (
                <button
                  type="button"
                  key={extra.tabKey}
                  onClick={() => setTokenTab(extra.tabKey)}
                  title={
                    extra.isUnattributed
                      ? "历史环境变量或未指定 profile_name 的调用"
                      : `已删除或重命名配置 "${extra.profileName}" 的历史消耗`
                  }
                  className={cn(
                    "flex items-center gap-1 rounded-md px-2.5 py-1 font-medium text-[10px] transition-colors",
                    tokenTab === extra.tabKey
                      ? "bg-foreground text-background"
                      : "bg-muted/30 text-muted-foreground hover:text-foreground",
                  )}
                >
                  {extra.label}
                  {!extra.isUnattributed && (
                    <span className="rounded bg-muted px-1 py-0 text-[8px] text-muted-foreground">历史</span>
                  )}
                </button>
              ))}
            </div>
          ) : (
            <div className="flex items-center gap-2">
              <span className="text-[10px] text-muted-foreground">按模型价格估算 · 近 30 天 (UTC)</span>
              <span className={cn("rounded-md border px-2 py-0.5 font-medium text-[10px]", badge.cls)}>
                {badge.text}
              </span>
            </div>
          )}
        </div>
      </div>

      {/* Main Content */}
      {mode === "tokens" ? (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[248px_1fr]">
          <div className="flex flex-col gap-3.5">
            <div>
              <div className="flex items-center justify-between text-[10px] text-muted-foreground">
                <span>{activeRange.headline}</span>
                {!isAllTimeRange && <span className="font-mono text-[9px]">UTC</span>}
              </div>
              <div className="mt-0.5 font-bold text-3xl tabular-nums tracking-tight">
                {primaryTotal > 0 ? fmtTokens(primaryTotal) : "0"}
              </div>
              <div className="mt-0.5 text-[10px] text-muted-foreground tabular-nums">
                {isAllTimeRange
                  ? `${selectedAllTimeTokens.calls} 次调用 · ${selectedAllTimeTokens.taskCount} 个任务`
                  : `${dailyTokenData.length} 个有记录自然日 · 窗口合计与右图对齐`}
              </div>
            </div>

            <div className="space-y-2.5">
              {[
                {
                  label: "输入(未命中)",
                  value: primaryMiss,
                  barColor: dailyTrendConfig.input.color,
                  text: "text-blue-400",
                },
                {
                  label: "缓存命中",
                  value: primaryMetrics.cacheRead,
                  barColor: dailyTrendConfig.cacheRead.color,
                  text: "text-emerald-400",
                },
                {
                  label: "输出",
                  value: primaryMetrics.output,
                  barColor: dailyTrendConfig.output.color,
                  text: "text-violet-400",
                },
              ].map(({ label, value, barColor, text }) => {
                const pct = primaryTotal > 0 ? Math.min(100, (value / primaryTotal) * 100) : 0;
                return (
                  <div key={label}>
                    <div className="mb-1 flex items-center justify-between text-[10px]">
                      <span className="text-muted-foreground">{label}</span>
                      <span className={cn("font-mono font-semibold tabular-nums", text)}>{fmtTokens(value)}</span>
                    </div>
                    <div className="h-1.5 overflow-hidden rounded-full bg-muted">
                      <div
                        className="h-full rounded-full transition-all duration-500"
                        style={{ width: `${pct}%`, background: barColor }}
                      />
                    </div>
                  </div>
                );
              })}
            </div>

            {(() => {
              const denominator = primaryMetrics.input;
              const hitPct = denominator > 0 ? (primaryMetrics.cacheRead / denominator) * 100 : 0;
              return (
                <div className="flex items-center justify-between rounded-lg border bg-muted/20 px-3 py-2 text-[10px]">
                  <span className="text-muted-foreground">缓存命中率</span>
                  <span
                    className={cn("font-semibold tabular-nums", hitPct > 50 ? "text-emerald-400" : "text-amber-400")}
                  >
                    {cacheHitRate(primaryMetrics.cacheRead, denominator)}
                  </span>
                </div>
              );
            })()}

            <div className="rounded-lg border border-dashed bg-muted/10 px-3 py-2 text-[10px] text-muted-foreground">
              <div className="flex items-center justify-between">
                <span>全量台账累计</span>
                <span className="font-mono font-semibold text-foreground tabular-nums">
                  {fmtTokens(allTimeTotal)} tok
                </span>
              </div>
              <div className="mt-1 flex flex-wrap items-center justify-between gap-1 text-[9px] tabular-nums">
                <span>
                  {selectedAllTimeTokens.calls} 次调用 · {selectedAllTimeTokens.taskCount} 任务
                </span>
                <span>写入 {fmtTokens(selectedAllTimeTokens.cacheWrite)}</span>
              </div>
            </div>
          </div>

          <div className="flex min-h-0 flex-col">
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <div className="flex flex-wrap items-center gap-3 text-[9px] text-muted-foreground">
                {(["miss", "output", "cacheRead"] as const).map((k) => (
                  <span key={k} className="flex items-center gap-1">
                    <span
                      className="inline-block size-2 rounded-sm"
                      style={{ background: dailyTrendConfig[k].color }}
                    />
                    {dailyTrendConfig[k].label}
                  </span>
                ))}
                <span>· 按 UTC 自然日</span>
              </div>
              <div className="flex gap-0.5 rounded-md border bg-muted/30 p-0.5">
                {RANGE_OPTIONS.map(({ days, label }) => (
                  <button
                    type="button"
                    key={days}
                    onClick={() => setTokenDays(days)}
                    className={cn(
                      "rounded px-2.5 py-0.5 font-medium text-[9px] transition-colors",
                      tokenDays === days
                        ? "bg-background text-foreground shadow-sm"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    {label}
                  </button>
                ))}
              </div>
            </div>

            {dailyTokenData.length === 0 ? (
              <div
                className="flex flex-1 items-center justify-center rounded-lg border bg-muted/10 text-muted-foreground text-xs"
                style={{ minHeight: 200 }}
              >
                {allTimeTotal > 0
                  ? "所选时间窗口内暂无 Token 记录，可切换更长周期或「全部」"
                  : "暂无 llm_usage Token 统计数据"}
              </div>
            ) : (
              <ChartContainer config={dailyTrendConfig} className="h-[225px] w-full">
                <BarChart data={dailyTokenData} margin={{ top: 4, right: 4, bottom: 0, left: 0 }} maxBarSize={40}>
                  <CartesianGrid vertical={false} />
                  <XAxis
                    dataKey="date"
                    tickLine={false}
                    axisLine={false}
                    tick={{ fontSize: 10 }}
                    tickMargin={6}
                    interval="preserveStartEnd"
                  />
                  <YAxis
                    tickFormatter={(v) => fmtTokens(Number(v))}
                    tickLine={false}
                    axisLine={false}
                    tick={{ fontSize: 10 }}
                    width={44}
                  />
                  <ChartTooltip
                    cursor={false}
                    content={
                      <ChartTooltipContent
                        indicator="line"
                        labelFormatter={(label, payload) => {
                          const full = (payload?.[0]?.payload as { fullDate?: string } | undefined)?.fullDate;
                          return full ? `${full} (UTC)` : String(label);
                        }}
                        formatter={(value, name) => (
                          <div className="flex w-full items-center justify-between gap-4">
                            <span>
                              {(dailyTrendConfig as Record<string, { label: string }>)[String(name)]?.label ??
                                String(name)}
                            </span>
                            <span className="font-mono font-semibold tabular-nums">{fmtTokens(Number(value))}</span>
                          </div>
                        )}
                      />
                    }
                  />
                  <Bar dataKey="miss" stackId="1" fill="var(--color-miss)" radius={[0, 0, 0, 0]} />
                  <Bar dataKey="output" stackId="1" fill="var(--color-output)" radius={[0, 0, 0, 0]} />
                  <Bar dataKey="cacheRead" stackId="1" fill="var(--color-cacheRead)" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ChartContainer>
            )}
          </div>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-[260px_1fr]">
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-2 gap-3">
              <div>
                <div className="text-[10px] text-muted-foreground">今日 (UTC)</div>
                <div className="font-bold text-2xl tabular-nums">{usd(todayCost)}</div>
              </div>
              <div>
                <div className="text-[10px] text-muted-foreground">本月</div>
                <div className="font-bold text-2xl tabular-nums">{usd(monthCost)}</div>
              </div>
            </div>
            <div>
              <div className="mb-1 flex items-center justify-between text-[10px] text-muted-foreground">
                <span>月度预算</span>
                <span className="tabular-nums">{budget > 0 ? `${usd(monthCost)} / ${usd(budget)}` : "未设置"}</span>
              </div>
              <Progress value={budgetPct} className="h-1.5" />
            </div>
            <div className="flex flex-wrap gap-x-3 gap-y-1 text-[10px] text-muted-foreground">
              <span>缓存节省 {usd(costs?.total.cache_savings_usd ?? 0)}</span>
              {offpeakCost > 0 && (
                <span>
                  峰 {usd(peakCost)} · 谷 <span className="text-emerald-500">{usd(offpeakCost)}</span>
                </span>
              )}
              {(costs?.total.unpriced_tokens ?? 0) > 0 && (
                <span className="text-amber-500">未计价 {fmtTokens(costs?.total.unpriced_tokens ?? 0)} tok</span>
              )}
            </div>
            {topModels.length > 0 && (
              <div className="space-y-1">
                <div className="text-[10px] text-muted-foreground">花费最高模型</div>
                {topModels.map((m) => (
                  <div key={m.model} className="flex items-center justify-between gap-2 text-[10px]">
                    <span className="min-w-0 truncate font-mono" title={m.model}>
                      {m.model}
                    </span>
                    <span className="shrink-0 font-semibold tabular-nums">{usd(m.cost_usd)}</span>
                  </div>
                ))}
              </div>
            )}
            {(topCompanies.length > 0 || topCampaigns.length > 0) && (
              <div className="grid grid-cols-2 gap-2 text-[10px]">
                {topCompanies.map((c) => (
                  <div key={`co-${c.id}`} className="flex items-center justify-between gap-1 truncate">
                    <span className="truncate text-muted-foreground">
                      {c.name || `#${c.id}`} {budgetLabel(c.budget_status, c.budget_usd)}
                    </span>
                    <span className="shrink-0 font-semibold tabular-nums">{usd(c.cost_usd)}</span>
                  </div>
                ))}
                {topCampaigns.map((c) => (
                  <div key={`ca-${c.id}`} className="flex items-center justify-between gap-1 truncate">
                    <span className="truncate text-muted-foreground">{c.name || `#${c.id}`}</span>
                    <span className="shrink-0 font-semibold tabular-nums">{usd(c.cost_usd)}</span>
                  </div>
                ))}
              </div>
            )}
          </div>

          <div className="flex min-h-0 flex-col">
            {costSeries.length === 0 ? (
              <div
                className="flex flex-1 items-center justify-center rounded-lg border bg-muted/10 text-muted-foreground text-xs"
                style={{ minHeight: 200 }}
              >
                暂无计价数据
              </div>
            ) : (
              <ChartContainer config={costTrendConfig} className="h-[205px] w-full">
                <AreaChart data={costSeries} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
                  <defs>
                    <linearGradient id={`costFill-${gradId}`} x1="0" y1="0" x2="0" y2="1">
                      <stop offset="5%" stopColor="var(--color-cost_usd)" stopOpacity={0.35} />
                      <stop offset="95%" stopColor="var(--color-cost_usd)" stopOpacity={0.02} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid vertical={false} />
                  <XAxis
                    dataKey="date"
                    tickLine={false}
                    axisLine={false}
                    tick={{ fontSize: 10 }}
                    tickMargin={6}
                    interval="preserveStartEnd"
                  />
                  <YAxis
                    tickFormatter={(v) => usd(Number(v))}
                    tickLine={false}
                    axisLine={false}
                    tick={{ fontSize: 10 }}
                    width={48}
                  />
                  <ChartTooltip
                    content={
                      <ChartTooltipContent
                        labelFormatter={(label, payload) => {
                          const full = (payload?.[0]?.payload as { fullDate?: string } | undefined)?.fullDate;
                          return full ? `${full} (UTC)` : String(label);
                        }}
                        formatter={(value) => (
                          <div className="flex w-full items-center justify-between gap-4">
                            <span>每日成本</span>
                            <span className="font-mono font-semibold tabular-nums">{usd(Number(value))}</span>
                          </div>
                        )}
                      />
                    }
                  />
                  <Area
                    type="monotone"
                    dataKey="cost_usd"
                    stroke="var(--color-cost_usd)"
                    strokeWidth={2}
                    fill={`url(#costFill-${gradId})`}
                  />
                </AreaChart>
              </ChartContainer>
            )}
          </div>
        </div>
      )}

      {/* LLM Failover Pool Health Strip */}
      {llmPool && llmPool.members.length > 0 && (
        <div className="mt-4 flex flex-wrap items-center justify-between gap-2 border-t pt-3">
          <div className="flex flex-wrap items-center gap-1.5 text-[10px]">
            <span className="font-medium text-muted-foreground">
              LLM 故障转移池（{llmPool.enabled ? "已开启" : "未开启"}）：
            </span>
            {llmPool.members.map((m) => (
              <Link
                key={m.profile_id}
                href="/system/llm"
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 transition-colors hover:bg-muted/40",
                  m.excluded && "opacity-55",
                )}
              >
                <span className={cn("size-1.5 rounded-full", poolDotClass(m.state, m.excluded))} />
                <span className="font-medium">{m.name}</span>
                <span className="font-mono text-[9px] text-muted-foreground">{m.model}</span>
                {m.state === "tripped" && (m.cooldown_secs ?? 0) > 0 && (
                  <span className="text-[9px] text-red-400 tabular-nums">冷却 {m.cooldown_secs}s</span>
                )}
              </Link>
            ))}
          </div>
          <Link
            href="/system/llm"
            className="inline-flex items-center gap-0.5 text-[10px] text-muted-foreground hover:text-foreground"
          >
            模型管理 <ArrowUpRightIcon className="size-3" />
          </Link>
        </div>
      )}
    </Card>
  );
}
