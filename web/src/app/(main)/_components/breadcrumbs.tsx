"use client";

import * as React from "react";

import Link from "next/link";
import { usePathname } from "next/navigation";

import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb";
import { sidebarItems } from "@/navigation/sidebar/sidebar-items";

const labels = new Map<string, string>();
for (const group of sidebarItems) {
  for (const item of group.items) {
    if ("url" in item) labels.set(item.url.replace(/\/+$/, ""), item.title);
    for (const sub of item.subItems ?? []) labels.set(sub.url.replace(/\/+$/, ""), sub.title);
  }
}

const DETAIL_LABELS: Record<string, string> = {
  "/function/tasks/detail": "任务详情",
  "/function/findings/detail": "发现详情",
  "/function/assets/detail": "资产详情",
};

function pathLabel(path: string, last: boolean): string {
  if (path === "/function") return "功能";
  if (path === "/system") return "系统";
  if (path === "/chat") return "对话";
  if (last && DETAIL_LABELS[path]) return DETAIL_LABELS[path];
  const known = labels.get(path);
  if (known) return known;
  const tail = path.split("/").filter(Boolean).pop() ?? "";
  if (last && tail === "detail") return "详情";
  return tail || "首页";
}

// Breadcrumbs 是全局导航的层级提示；详情页由 query 参数承载 id，所以只标到详情页。
export function Breadcrumbs() {
  const pathname = usePathname();
  const path = (pathname ?? "/").replace(/\/+$/, "") || "/";
  if (path === "/" || path === "/dashboard") return null;

  const segments = path.split("/").filter(Boolean);
  const crumbs = segments.map((_, index) => {
    const href = `/${segments.slice(0, index + 1).join("/")}`;
    return { href, label: pathLabel(href, index === segments.length - 1) };
  });
  if (crumbs.length === 0) return null;

  return (
    <div className="border-b px-4 py-1.5 lg:px-6">
      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            <BreadcrumbLink asChild>
              <Link href="/dashboard">首页</Link>
            </BreadcrumbLink>
          </BreadcrumbItem>
          {crumbs.map((crumb, index) => (
            <React.Fragment key={crumb.href}>
              <BreadcrumbSeparator />
              <BreadcrumbItem>
                {index === crumbs.length - 1 ? (
                  <BreadcrumbPage>{crumb.label}</BreadcrumbPage>
                ) : (
                  <BreadcrumbLink asChild>
                    <Link href={crumb.href}>{crumb.label}</Link>
                  </BreadcrumbLink>
                )}
              </BreadcrumbItem>
            </React.Fragment>
          ))}
        </BreadcrumbList>
      </Breadcrumb>
    </div>
  );
}
