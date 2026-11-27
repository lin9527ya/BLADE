import type { LLMProfile, Task, UsageStats } from "./types.ts";

export type DashboardTaskFilter = "all" | "running" | "paused" | "done" | "mobile";

export const TOKEN_TAB_ALL = "all" as const;

export interface UsageBucket {
  calls: number;
  taskCount: number;
  input: number;
  output: number;
  cacheRead: number;
  cacheWrite: number;
}

export interface DailyUsagePoint {
  fullDate: string;
  date: string;
  input: number;
  miss: number;
  output: number;
  cacheRead: number;
}

export interface DailyUsageTotals {
  input: number;
  miss: number;
  output: number;
  cacheRead: number;
}

export interface ExtraUsageProfile {
  tabKey: string;
  profileName: string;
  label: string;
  isUnattributed: boolean;
}

/**
 * localDateKey formats a Date as local YYYY-MM-DD (avoiding UTC midnight shift).
 */
export function localDateKey(d: Date = new Date()): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

/**
 * daysAgoDateKey returns the local YYYY-MM-DD string `days` days before `base`.
 */
export function daysAgoDateKey(days: number, base: Date = new Date()): string {
  const d = new Date(base);
  d.setDate(d.getDate() - days);
  return localDateKey(d);
}

/**
 * utcDateKey formats a Date as UTC YYYY-MM-DD, matching Postgres AT TIME ZONE 'UTC' buckets.
 */
export function utcDateKey(d: Date = new Date()): string {
  const y = d.getUTCFullYear();
  const m = String(d.getUTCMonth() + 1).padStart(2, "0");
  const day = String(d.getUTCDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

/**
 * utcUsageWindowStartKey returns the inclusive UTC YYYY-MM-DD start date covering `days`
 * complete UTC calendar days ending today (`base`), matching db.UsageDaily 1:1.
 * Returns empty string for unbounded/all-time windows (`days <= 0` or `days >= 3650`).
 */
export function utcUsageWindowStartKey(days: number, base: Date = new Date()): string {
  if (!Number.isFinite(days) || days <= 0 || days >= 3650) return "";
  const wholeDays = Math.floor(days);
  const start = new Date(Date.UTC(base.getUTCFullYear(), base.getUTCMonth(), base.getUTCDate() - (wholeDays - 1)));
  return utcDateKey(start);
}

export function profileTabKey(profileName: string): string {
  return `profile:${profileName}`;
}

export function parseProfileTabKey(tab: string): string | null {
  if (tab === TOKEN_TAB_ALL) return null;
  if (tab.startsWith("profile:")) return tab.slice("profile:".length);
  return tab;
}

function emptyUsageBucket(): UsageBucket {
  return { calls: 0, taskCount: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };
}

/**
 * buildUsageByProfileMap aggregates llm_usage profile rows keyed strictly by exact profile_name,
 * without absorbing unknown/deleted/empty profiles into the default profile.
 */
export function buildUsageByProfileMap(usageStats: UsageStats | null): Map<string, UsageBucket> {
  const map = new Map<string, UsageBucket>();
  for (const row of usageStats?.by_profile ?? []) {
    const name = row.profile_name ?? "";
    const prev = map.get(name) ?? emptyUsageBucket();
    map.set(name, {
      calls: prev.calls + row.calls,
      taskCount: prev.taskCount + row.tasks,
      input: prev.input + row.input_tokens,
      output: prev.output + row.output_tokens,
      cacheRead: prev.cacheRead + row.cache_read_tokens,
      cacheWrite: prev.cacheWrite + row.cache_write_tokens,
    });
  }
  return map;
}

/**
 * summarizeAllUsageTokens returns the global all-profile llm_usage totals, using exact
 * backend distinct-task count when available.
 */
export function summarizeAllUsageTokens(usageStats: UsageStats | null): UsageBucket {
  if (!usageStats) return emptyUsageBucket();
  let calls = 0;
  let taskSum = 0;
  let input = 0;
  let output = 0;
  let cacheRead = 0;
  let cacheWrite = 0;
  for (const row of usageStats.by_profile ?? []) {
    calls += row.calls;
    taskSum += row.tasks;
    input += row.input_tokens;
    output += row.output_tokens;
    cacheRead += row.cache_read_tokens;
    cacheWrite += row.cache_write_tokens;
  }
  return {
    calls,
    taskCount: usageStats.summary?.tasks ?? taskSum,
    input,
    output,
    cacheRead,
    cacheWrite,
  };
}

/**
 * selectUsageProfileBucket picks either global totals ("all") or one exact profile bucket.
 */
export function selectUsageProfileBucket(
  byProfileMap: Map<string, UsageBucket>,
  usageStats: UsageStats | null,
  tokenTab: string,
): UsageBucket {
  if (tokenTab === TOKEN_TAB_ALL) {
    return summarizeAllUsageTokens(usageStats);
  }
  const targetName = parseProfileTabKey(tokenTab);
  if (targetName === null) return summarizeAllUsageTokens(usageStats);
  return byProfileMap.get(targetName) ?? emptyUsageBucket();
}

/**
 * collectExtraUsageProfiles returns non-empty historical or unattributed profile names present
 * in llm_usage but absent from active configured LLM profiles so every token stays inspectable.
 */
export function collectExtraUsageProfiles(
  usageStats: UsageStats | null,
  configuredProfiles: readonly Pick<LLMProfile, "name">[],
): ExtraUsageProfile[] {
  const configuredNames = new Set(configuredProfiles.map((p) => p.name));
  const activeExtras = new Set<string>();
  for (const p of usageStats?.by_profile ?? []) {
    const name = p.profile_name ?? "";
    const total = p.calls + p.input_tokens + p.output_tokens + p.cache_read_tokens + p.cache_write_tokens;
    if (total > 0 && !configuredNames.has(name)) {
      activeExtras.add(name);
    }
  }
  for (const d of usageStats?.daily ?? []) {
    const name = d.profile_name ?? "";
    const total = d.input_tokens + d.output_tokens + d.cache_read_tokens;
    if (total > 0 && !configuredNames.has(name)) {
      activeExtras.add(name);
    }
  }
  return [...activeExtras]
    .sort((a, b) => {
      if (a === "" && b !== "") return 1;
      if (b === "" && a !== "") return -1;
      return a.localeCompare(b);
    })
    .map((profileName) => ({
      tabKey: profileTabKey(profileName),
      profileName,
      label: profileName === "" ? "未归属/默认环境" : profileName,
      isUnattributed: profileName === "",
    }));
}

/**
 * buildDailyUsageSeries produces sorted UTC daily chart points filtered by profile and window.
 */
export function buildDailyUsageSeries(
  usageStats: UsageStats | null,
  tokenTab: string,
  days: number,
  baseDate: Date = new Date(),
): DailyUsagePoint[] {
  const cutoff = utcUsageWindowStartKey(days, baseDate);
  const targetName = parseProfileTabKey(tokenTab);
  const byDate = new Map<string, DailyUsageTotals>();
  for (const d of usageStats?.daily ?? []) {
    if (cutoff && d.date < cutoff) continue;
    const rowProfile = d.profile_name ?? "";
    if (targetName !== null && rowProfile !== targetName) continue;
    const prev = byDate.get(d.date) ?? { input: 0, miss: 0, output: 0, cacheRead: 0 };
    byDate.set(d.date, {
      input: prev.input + d.input_tokens,
      miss: prev.miss + Math.max(0, d.input_tokens - d.cache_read_tokens),
      output: prev.output + d.output_tokens,
      cacheRead: prev.cacheRead + d.cache_read_tokens,
    });
  }
  return [...byDate.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([fullDate, vals]) => ({
      fullDate,
      date: fullDate.slice(5),
      ...vals,
    }));
}

/**
 * summarizeDailyUsage sums the visible daily series so left window totals match bars 1:1.
 */
export function summarizeDailyUsage(series: readonly DailyUsagePoint[]): DailyUsageTotals {
  let input = 0;
  let miss = 0;
  let output = 0;
  let cacheRead = 0;
  for (const point of series) {
    input += point.input;
    miss += point.miss;
    output += point.output;
    cacheRead += point.cacheRead;
  }
  return { input, miss, output, cacheRead };
}

/**
 * fmtDurationSeconds formats a duration in seconds to compact human readable text.
 */
export function fmtDurationSeconds(sec?: number): string {
  if (sec === undefined || !Number.isFinite(sec) || sec <= 0) return "—";
  const total = Math.floor(sec);
  if (total < 60) return `${total}s`;
  const m = Math.floor(total / 60);
  const s = total % 60;
  if (m < 60) return s > 0 ? `${m}m ${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const remM = m % 60;
  if (h < 24) return remM > 0 ? `${h}h ${remM}m` : `${h}h`;
  const d = Math.floor(h / 24);
  const remH = h % 24;
  return remH > 0 ? `${d}d ${remH}h` : `${d}d`;
}

/**
 * fmtBytes formats byte sizes cleanly for runtime memory and disk metrics.
 */
export function fmtBytes(bytes?: number): string {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes === 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = bytes;
  let idx = 0;
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024;
    idx++;
  }
  const fixed = value >= 10 || idx === 0 ? value.toFixed(0) : value.toFixed(1);
  return `${fixed} ${units[idx]}`;
}

/**
 * computePercent computes bounded [0, 100] percentages safely.
 */
export function computePercent(part: number, total: number): number {
  if (!Number.isFinite(part) || !Number.isFinite(total) || total <= 0 || part <= 0) return 0;
  return Math.min(100, Math.round((part / total) * 1000) / 10);
}

/**
 * matchesTaskFilter filters dashboard task rows by operational tab.
 */
export function matchesTaskFilter(task: Task, filter: DashboardTaskFilter): boolean {
  switch (filter) {
    case "all":
      return true;
    case "running":
      return task.status === "running" || task.status === "queued";
    case "paused":
      return task.status === "paused";
    case "done":
      return (
        task.status === "done" ||
        task.status === "failed" ||
        task.status === "timeout" ||
        task.status === "policy_blocked"
      );
    case "mobile":
      return task.app_mode === "mobile";
  }
}

/**
 * resolveTaskElapsedSeconds computes live elapsed runtime including an open run segment.
 */
export function resolveTaskElapsedSeconds(task: Task, nowUnixSec: number = Math.floor(Date.now() / 1000)): number {
  const settled = task.active_run_seconds ?? 0;
  const started = task.run_segment_started_unix ?? 0;
  if (started > 0 && nowUnixSec > started) {
    return settled + (nowUnixSec - started);
  }
  return settled;
}
