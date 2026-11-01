import type { GraphAuditReport } from "@/lib/types";

// 图体检的展示层（纯函数）。码 → 中文标签与汇总在组件之外，便于单测——
// 这些映射是页面唯一容易写错的地方（漏一个码就会在界面上直接露出英文 code）。

const ISSUE_LABELS: Record<string, string> = {
  cross_exploration_edge: "跨任务边",
  deleted_prerequisite: "前置意图已删除",
  dependency_not_intent: "依赖指向非意图节点",
  dependency_cycle: "依赖成环",
  digest_covers_deleted: "摘要覆盖已删除成员",
  starved_open_intent: "意图长期无人领取",
  expired_running_lease: "意图租约已过期",
  illegal_state: "节点状态非法",
};

export function graphAuditIssueLabel(code: string): string {
  return ISSUE_LABELS[code] ?? code;
}

export interface GraphAuditSummary {
  errors: number;
  warnings: number;
  /** 没有 error 也没有 warn：页面此时不渲染任何横幅（干净图不该占版面）。 */
  clean: boolean;
}

export function graphAuditSummary(report?: GraphAuditReport | null): GraphAuditSummary {
  let errors = 0;
  let warnings = 0;
  for (const issue of report?.issues ?? []) {
    if (issue.severity === "error") errors += 1;
    else warnings += 1;
  }
  return { errors, warnings, clean: errors + warnings === 0 };
}

/**
 * 一行式摘要，用 counts（如实总数，可能大于 issues 里的条数）：`3 个问题：依赖成环 ×2 · 意图租约已过期 ×1`。
 * 空报告返回空串。
 */
export function graphAuditHeadline(report?: GraphAuditReport | null, maxCodes = 3): string {
  const entries = Object.entries(report?.counts ?? {})
    .filter(([, n]) => n > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
  if (entries.length === 0) return "";
  const total = entries.reduce((sum, [, n]) => sum + n, 0);
  const shown = entries.slice(0, Math.max(1, maxCodes)).map(([code, n]) => `${graphAuditIssueLabel(code)} ×${n}`);
  const rest = entries.length > shown.length ? " …" : "";
  return `${total} 个问题：${shown.join(" · ")}${rest}`;
}

/** 一条问题涉及的节点引用（去重、保持出现顺序），给界面上 `#123` 那串用。 */
export function graphAuditRefs(issue: { node_ids?: number[]; from?: number; to?: number }): string[] {
  const out: string[] = [];
  const seen = new Set<number>();
  const push = (id: number | undefined) => {
    if (id === undefined || id === null || seen.has(id)) return;
    seen.add(id);
    out.push(`#${id}`);
  };
  for (const id of issue.node_ids ?? []) push(id);
  push(issue.from);
  push(issue.to);
  return out;
}
