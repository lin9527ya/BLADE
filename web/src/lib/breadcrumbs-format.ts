import type { NavGroup } from "../navigation/sidebar/sidebar-items.ts";

export interface BreadcrumbCrumb {
  href: string;
  label: string;
  clickable: boolean;
}

const NON_LINK_SEGMENTS = new Set(["/function", "/system"]);

const DETAIL_LABELS: Record<string, string> = {
  "/function/tasks/detail": "任务详情",
  "/function/findings/detail": "发现详情",
  "/function/assets/detail": "资产详情",
  "/function/companies/detail": "企业详情",
  "/function/schedules": "计划任务",
  "/function/sync": "资产同步",
  "/function/credentials": "凭据工作台",
  "/function/footholds": "据点与隧道",
  "/function/verification-ledger": "验证台账",
  "/system/agents/detail": "Agent 详情",
  "/system/api-tokens": "API 令牌",
  "/system/intercept/approvals": "审批记录",
  "/system/intercept/assets": "资产黑名单",
};

export function buildSidebarLabelMap(groups: readonly NavGroup[]): Map<string, string> {
  const labels = new Map<string, string>();
  for (const group of groups) {
    for (const item of group.items) {
      if ("url" in item) labels.set(item.url.replace(/\/+$/, ""), item.title);
      for (const sub of item.subItems ?? []) labels.set(sub.url.replace(/\/+$/, ""), sub.title);
    }
  }
  return labels;
}

function pathLabel(path: string, last: boolean, labels: ReadonlyMap<string, string>): string {
  if (path === "/function") return "功能";
  if (path === "/system") return "系统";
  if (path === "/chat") return "对话";
  if (last && DETAIL_LABELS[path]) return DETAIL_LABELS[path];
  const known = labels.get(path);
  if (known) return known;
  if (DETAIL_LABELS[path]) return DETAIL_LABELS[path];
  const tail = path.split("/").filter(Boolean).pop() ?? "";
  if (last && tail === "detail") return "详情";
  return tail || "首页";
}

/**
 * resolveBreadcrumbCrumbs builds breadcrumb segments while preventing broken links
 * on virtual group segments (/function, /system) that have no backing page route.
 */
export function resolveBreadcrumbCrumbs(
  pathname: string | null | undefined,
  labels: ReadonlyMap<string, string>,
): BreadcrumbCrumb[] {
  const path = (pathname ?? "/").replace(/\/+$/, "") || "/";
  if (path === "/" || path === "/dashboard") return [];

  const segments = path.split("/").filter(Boolean);
  return segments.map((_, index) => {
    const href = `/${segments.slice(0, index + 1).join("/")}`;
    const last = index === segments.length - 1;
    return {
      href,
      label: pathLabel(href, last, labels),
      clickable: !last && !NON_LINK_SEGMENTS.has(href),
    };
  });
}
