"use client";

import type * as React from "react";

import Link from "next/link";

import { ArrowUpRightIcon } from "lucide-react";

import { Card } from "@/components/ui/card";
import type { ChartConfig } from "@/components/ui/chart";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

export {
  buildDailyUsageSeries,
  buildUsageByProfileMap,
  collectExtraUsageProfiles,
  computePercent,
  type DailyUsagePoint,
  type DailyUsageTotals,
  type DashboardTaskFilter,
  daysAgoDateKey,
  type ExtraUsageProfile,
  fmtBytes,
  fmtDurationSeconds,
  localDateKey,
  matchesTaskFilter,
  profileTabKey,
  resolveTaskElapsedSeconds,
  selectUsageProfileBucket,
  summarizeAllUsageTokens,
  summarizeDailyUsage,
  TOKEN_TAB_ALL,
  type UsageBucket,
  utcDateKey,
  utcUsageWindowStartKey,
} from "@/lib/dashboard-format";

export function fmtRel(ts?: string | number): string {
  if (!ts) return "—";
  const ms = Date.now() - (typeof ts === "number" ? ts * 1000 : Date.parse(ts as string));
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s 前`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m 前`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h 前`;
  return `${Math.floor(h / 24)}d 前`;
}

export function fmtTokens(n: number): string {
  if (n >= 1_000_000_000) return `${(n / 1_000_000_000).toFixed(1)}B`;
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

export function fmtClockTime(tsMs: number | null): string {
  if (!tsMs) return "—";
  const d = new Date(tsMs);
  const pad = (v: number) => String(v).padStart(2, "0");
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function fmtScheduleUpcoming(unixSec?: number): string {
  if (!unixSec || unixSec <= 0) return "暂无排期";
  const diff = unixSec - Math.floor(Date.now() / 1000);
  if (diff <= 0) return "即将执行";
  if (diff < 60) return `${diff}s 后`;
  const m = Math.floor(diff / 60);
  if (m < 60) return `${m}m 后`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m 后`;
  return new Date(unixSec * 1000).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export const ASSET_TYPE_LABELS: Record<string, string> = {
  root_domain: "根域名",
  ip: "IP",
  subdomain: "子域名",
  app: "应用",
  service: "服务",
  endpoint: "端点",
};

export const ASSET_COLORS: Record<string, string> = {
  root_domain: "bg-blue-500",
  ip: "bg-indigo-400",
  subdomain: "bg-violet-500",
  app: "bg-orange-400",
  service: "bg-cyan-500",
  endpoint: "bg-emerald-500",
};

export function statusColor(code: number): string {
  if (code < 300) return "text-emerald-500";
  if (code < 400) return "text-blue-400";
  if (code < 500) return "text-amber-400";
  return "text-red-400";
}

export function statusBg(code: number): string {
  if (code < 300) return "bg-emerald-500";
  if (code < 400) return "bg-blue-400";
  if (code < 500) return "bg-amber-400";
  return "bg-red-400";
}

export function LiveDot({ className }: { className?: string }) {
  return <span className={cn("inline-block size-1.5 shrink-0 animate-pulse rounded-full bg-blue-400", className)} />;
}

export function SectionTitle({
  icon: Icon,
  children,
  sub,
  href,
  moreLabel = "查看全部",
}: {
  icon: React.ElementType;
  children: React.ReactNode;
  sub?: React.ReactNode;
  href?: string;
  moreLabel?: string;
}) {
  return (
    <div className="mb-3 flex items-center justify-between gap-2">
      <div className="flex items-center gap-1.5 font-semibold text-xs">
        <Icon className="size-3.5 text-muted-foreground" />
        {children}
      </div>
      <div className="flex items-center gap-2">
        {sub && <span className="text-[10px] text-muted-foreground">{sub}</span>}
        {href && (
          <Link
            href={href}
            className="inline-flex items-center gap-0.5 text-[10px] text-muted-foreground transition-colors hover:text-foreground"
          >
            {moreLabel}
            <ArrowUpRightIcon className="size-3" />
          </Link>
        )}
      </div>
    </div>
  );
}

export const dailyTrendConfig = {
  // 堆叠图的 input 段必须是「未命中输入」：总输入已含缓存，再叠 cacheRead 会重复计数。
  miss: { label: "输入(未命中)", color: "hsl(217 91% 60%)" },
  input: { label: "输入", color: "hsl(217 91% 60%)" },
  output: { label: "输出", color: "hsl(263 70% 60%)" },
  cacheRead: { label: "缓存读", color: "hsl(160 60% 45%)" },
} satisfies ChartConfig;

export const costTrendConfig = {
  cost_usd: { label: "每日成本 (USD)", color: "hsl(160 64% 45%)" },
} satisfies ChartConfig;

export function DashboardSkeleton() {
  return (
    <div className="flex flex-1 flex-col gap-4 pb-6" role="status" aria-busy="true" aria-label="加载作战总览中">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="space-y-1.5">
          <Skeleton className="h-7 w-48" />
          <Skeleton className="h-4 w-72" />
        </div>
        <div className="flex items-center gap-2">
          <Skeleton className="h-8 w-24" />
          <Skeleton className="h-8 w-20" />
          <Skeleton className="h-8 w-28" />
        </div>
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-5">
        {["kpi-tasks", "kpi-findings", "kpi-assets", "kpi-traffic", "kpi-llm"].map((id) => (
          <Card key={id} className="gap-2 p-4">
            <Skeleton className="h-3 w-20" />
            <Skeleton className="h-7 w-24" />
            <Skeleton className="h-1.5 w-full" />
            <Skeleton className="h-3 w-32" />
          </Card>
        ))}
      </div>

      <div className="grid grid-cols-1 gap-3 xl:grid-cols-12">
        <Card className="p-4 xl:col-span-7">
          <Skeleton className="mb-4 h-6 w-48" />
          <div className="space-y-3">
            {["row-1", "row-2", "row-3", "row-4", "row-5"].map((id) => (
              <Skeleton key={id} className="h-10 w-full" />
            ))}
          </div>
        </Card>
        <Card className="p-4 xl:col-span-5">
          <Skeleton className="mb-4 h-6 w-40" />
          <div className="space-y-3">
            {["feed-1", "feed-2", "feed-3", "feed-4", "feed-5"].map((id) => (
              <Skeleton key={id} className="h-11 w-full" />
            ))}
          </div>
        </Card>
      </div>

      <Card className="p-4">
        <Skeleton className="mb-4 h-6 w-64" />
        <Skeleton className="h-[220px] w-full" />
      </Card>

      <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
        {["bot-assets", "bot-traffic", "bot-mobile", "bot-sys"].map((id) => (
          <Card key={id} className="p-4">
            <Skeleton className="mb-3 h-4 w-28" />
            <div className="space-y-2.5">
              <Skeleton className="h-3 w-full" />
              <Skeleton className="h-3 w-5/6" />
              <Skeleton className="h-3 w-4/6" />
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}
