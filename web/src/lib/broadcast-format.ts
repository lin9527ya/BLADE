import type { ExploreKind, TaskNode } from "@/lib/types";

// 播报行的轻量展示元数据。任务详情页的播报 tab 有更重的拓扑/资产视图；
// 全局播报只做跨任务流水，保持这套标签/配色与之同源即可。
export const BROADCAST_KINDS: ExploreKind[] = [
  "goal",
  "intent",
  "fact",
  "finding",
  "hint",
  "digest",
  "foothold",
  "credential",
  "artifact",
  "footprint",
  "detection",
  "ad_object",
  "cloud_object",
];

export const BROADCAST_KIND_LABEL: Record<string, string> = {
  begin: "起点",
  goal: "目标",
  intent: "意图",
  fact: "事实",
  finding: "漏洞",
  hint: "提示",
  digest: "压缩",
  foothold: "据点",
  credential: "凭据",
  artifact: "遗留物",
  detection: "检测",
  ad_object: "AD 对象",
  cloud_object: "云对象",
  footprint: "痕迹",
};

export const BROADCAST_KIND_CHIP: Record<string, string> = {
  begin: "bg-slate-500/15 text-slate-600 dark:text-slate-300",
  goal: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
  intent: "bg-blue-500/15 text-blue-600 dark:text-blue-400",
  fact: "bg-amber-500/15 text-amber-600 dark:text-amber-400",
  finding: "bg-rose-500/15 text-rose-600 dark:text-rose-400",
  hint: "bg-violet-500/15 text-violet-600 dark:text-violet-400",
  digest: "bg-teal-500/15 text-teal-600 dark:text-teal-400",
  foothold: "bg-orange-500/15 text-orange-600 dark:text-orange-400",
  credential: "bg-cyan-500/15 text-cyan-600 dark:text-cyan-400",
  artifact: "bg-violet-500/15 text-violet-600 dark:text-violet-400",
  detection: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
  ad_object: "bg-purple-500/15 text-purple-600 dark:text-purple-400",
  cloud_object: "bg-sky-500/15 text-sky-600 dark:text-sky-400",
  footprint: "bg-rose-500/15 text-rose-600 dark:text-rose-400",
};

export function broadcastViewKind(n: TaskNode): string {
  return n.type === "fact" && n.state === "origin" ? "begin" : n.type;
}

const STATE_LABEL: Record<string, Record<string, string>> = {
  begin: { origin: "起点" },
  goal: { open: "未达成", met: "已达成", abandoned: "已放弃" },
  fact: { origin: "起点", confirmed: "已确认", dismissed: "已否定" },
  finding: { confirmed: "已确认", dismissed: "已排除", ruled_out: "已排除", open_proof_gap: "待补证" },
  hint: { active: "待采纳", consumed: "已采纳" },
  digest: { active: "生效中", superseded: "已替代" },
  foothold: { active: "在手", lost: "已丢失" },
  credential: { active: "在手", lost: "已丢失" },
  detection: { triggered: "已检出", not_triggered: "未检出", unknown: "未知" },
  artifact: { active: "未清理", removed: "已清理", unknown: "未知" },
  footprint: {
    open: "未处置",
    unknown: "未确认",
    accepted: "接受风险",
    cleaned: "已清理",
    not_found: "未发现",
  },
  ad_object: { active: "在册" },
  cloud_object: { active: "在册" },
};

export function broadcastStateLabel(n: TaskNode): string {
  return STATE_LABEL[broadcastViewKind(n)]?.[n.state] ?? n.state;
}

const SUMMARY_FIELDS: Record<string, string[]> = {
  begin: ["summary", "description"],
  goal: ["text"],
  intent: ["summary"],
  fact: ["summary"],
  finding: ["name", "summary"],
  hint: ["text", "summary"],
  digest: ["body", "summary"],
  foothold: ["summary"],
  credential: ["summary"],
  artifact: ["summary"],
  footprint: ["summary"],
  detection: ["summary"],
  ad_object: ["name", "sam"],
  cloud_object: ["name"],
};

export function broadcastSummary(n: TaskNode): string {
  const raw = n.payload ?? "";
  if (!raw.trim()) return "";
  for (const field of SUMMARY_FIELDS[broadcastViewKind(n)] ?? []) {
    try {
      const obj: unknown = JSON.parse(raw);
      if (obj && typeof obj === "object") {
        const v = (obj as Record<string, unknown>)[field];
        if (typeof v === "string" && v.trim()) return v;
      }
    } catch {
      return raw;
    }
  }
  return raw;
}

export function prettyBroadcastPayload(raw?: string): string {
  if (!raw?.trim()) return "（无 payload）";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}
