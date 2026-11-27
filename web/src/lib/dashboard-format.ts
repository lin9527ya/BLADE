import type { Task } from "@/lib/types";

export type DashboardTaskFilter = "all" | "running" | "paused" | "done" | "mobile";

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
