"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpRightIcon, GitBranchIcon } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { TaskRelationRef } from "@/lib/types";

function RelationGroup({
  title,
  description,
  items,
}: {
  title: string;
  description: string;
  items: TaskRelationRef[];
}) {
  if (items.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <div>
        <div className="font-medium text-sm">{title}</div>
        <div className="text-muted-foreground text-xs">{description}</div>
      </div>
      <div className="flex flex-col gap-1">
        {items.map((item) => (
          <Link
            key={`${item.direction}-${item.id}`}
            href={`/function/tasks/detail?id=${item.id}`}
            className="flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5 hover:bg-muted/50"
          >
            <span className="min-w-0">
              <span className="block truncate text-sm" title={item.name || item.description}>
                {item.name || item.description || `任务 #${item.id}`}
              </span>
              <span className="text-muted-foreground text-xs">#{item.id}</span>
            </span>
            <span className="flex shrink-0 items-center gap-1.5">
              <StatusBadge domain="task" value={item.status} dot />
              <ArrowUpRightIcon className="size-3.5 text-muted-foreground" />
            </span>
          </Link>
        ))}
      </div>
    </div>
  );
}

export function TaskRelationsCard({ taskId }: { taskId: string }) {
  const [items, setItems] = React.useState<TaskRelationRef[]>([]);
  const [loaded, setLoaded] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    api
      .taskRelations(taskId)
      .then((next) => {
        if (alive) setItems(next);
      })
      .catch(() => {
        // The overview remains useful without the relation card.
      })
      .finally(() => {
        if (alive) setLoaded(true);
      });
    return () => {
      alive = false;
    };
  }, [taskId]);

  if (!loaded || items.length === 0) return null;
  const sources = items.filter((item) => item.direction === "source");
  const dependents = items.filter((item) => item.direction !== "source");

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <GitBranchIcon className="size-4" /> 任务关系
          <Badge variant="outline" className="ml-1">
            {items.length}
          </Badge>
        </CardTitle>
        <CardDescription>只展示一跳关系；更深链路在各任务自己的页面里。</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-4 md:grid-cols-2">
        <RelationGroup title="来源任务" description="本任务继承它们的只读上下文" items={sources} />
        <RelationGroup title="派生任务" description="它们把本任务作为来源" items={dependents} />
      </CardContent>
    </Card>
  );
}
