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
import { type BreadcrumbCrumb, buildSidebarLabelMap, resolveBreadcrumbCrumbs } from "@/lib/breadcrumbs-format";
import { sidebarItems } from "@/navigation/sidebar/sidebar-items";

const labels = buildSidebarLabelMap(sidebarItems);

function renderCrumbContent(crumb: BreadcrumbCrumb, isLast: boolean) {
  if (isLast) return <BreadcrumbPage>{crumb.label}</BreadcrumbPage>;
  if (crumb.clickable) {
    return (
      <BreadcrumbLink asChild>
        <Link href={crumb.href}>{crumb.label}</Link>
      </BreadcrumbLink>
    );
  }
  return <span className="text-muted-foreground">{crumb.label}</span>;
}

// Breadcrumbs 是全局导航的层级提示；虚拟段（/function、/system）无对应落地页，不渲染成可点死链。
export function Breadcrumbs() {
  const pathname = usePathname();
  const crumbs = resolveBreadcrumbCrumbs(pathname, labels);
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
              <BreadcrumbItem>{renderCrumbContent(crumb, index === crumbs.length - 1)}</BreadcrumbItem>
            </React.Fragment>
          ))}
        </BreadcrumbList>
      </Breadcrumb>
    </div>
  );
}
