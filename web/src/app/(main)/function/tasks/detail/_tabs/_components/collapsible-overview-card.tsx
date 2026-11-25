"use client";

import type * as React from "react";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";

/** 任务总览可折叠管理卡片：右上角收起/查看切换，默认展开内容区。 */
export function CollapsibleOverviewCard({
  title,
  icon,
  meta,
  description,
  open,
  onOpenChange,
  openLabel,
  closedLabel,
  children,
}: {
  title: string;
  icon: React.ReactNode;
  meta?: React.ReactNode;
  description?: React.ReactNode;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  openLabel: string;
  closedLabel: string;
  children: React.ReactNode;
}) {
  return (
    <Collapsible open={open} onOpenChange={onOpenChange}>
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <CardTitle className="flex items-center gap-2 text-base">
              {icon} {title}
              {meta}
            </CardTitle>
            <CollapsibleTrigger asChild>
              <Button size="sm" variant="outline">
                {open ? openLabel : closedLabel}
              </Button>
            </CollapsibleTrigger>
          </div>
          {description && <CardDescription>{description}</CardDescription>}
        </CardHeader>
        <CollapsibleContent>
          <CardContent className="flex flex-col gap-3">{children}</CardContent>
        </CollapsibleContent>
      </Card>
    </Collapsible>
  );
}
