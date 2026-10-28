import type { Activity } from "./types";

// History and live responses can overlap or arrive out of order. A persisted
// activity's seq is its identity; replay must not duplicate rows or token totals.
export function mergeActivities(current: Activity[], incoming: Activity[]): Activity[] {
  if (!incoming.length) return current;
  const first = incoming[0];
  const last = incoming[incoming.length - 1];
  // API pages and live tails are ascending and, in the common case, entirely
  // newer or entirely older than the loaded window. Avoid a Map + sort there.
  if (first.seq <= last.seq) {
    if (!current.length) return [...incoming];
    if (first.seq > current[current.length - 1].seq) return [...current, ...incoming];
    if (last.seq < current[0].seq) return [...incoming, ...current];
  }
  const bySeq = new Map<number, Activity>();
  for (const activity of current) bySeq.set(activity.seq, activity);
  for (const activity of incoming) bySeq.set(activity.seq, activity);
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq);
}
