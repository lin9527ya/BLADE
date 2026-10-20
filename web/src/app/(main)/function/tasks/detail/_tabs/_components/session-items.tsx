"use client";

import * as React from "react";

import { statusIcon } from "./session-helpers";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { taskAssetSourceLabel, taskAssetTypeLabel } from "@/lib/task-assets";
import { IntentAsset, Session } from "@/lib/types";
import { cn } from "@/lib/utils";
import { Loader2Icon, ShieldAlertIcon, Trash2Icon } from "lucide-react";

// 会话列表的条目与 worker 资产徽标。

export function SessionItem({
  s,
  active,
  displayTitle,
  hasPending,
  unread,
  onClick,
  onCancel,
  controlling,
  deleted,
}: {
  s: Session;
  active: boolean;
  displayTitle: string;
  hasPending?: boolean;
  unread?: number;
  onClick: () => void;
  onCancel?: () => void;
  controlling?: boolean;
  deleted?: boolean;
}) {
  const icon = deleted ? (
    <Trash2Icon className="size-3.5 text-destructive" />
  ) : s.role === "worker" ? (
    statusIcon(s.status)
  ) : s.live ? (
    <Loader2Icon className="size-3.5 animate-spin text-blue-500" />
  ) : null;

  const cancellable =
    s.role === "worker" &&
    !s.inherited &&
    !deleted &&
    // pending = 待领(open)意图;连同运行中/已暂停都允许删除。
    (s.status === "running" || s.status === "paused" || s.status === "pending");
  return (
    <div
      className={cn(
        "group/session flex w-full items-center rounded-md transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      <button
        type="button"
        onClick={onClick}
        className="flex min-w-0 flex-1 items-center gap-1 px-2 py-1.5 text-left text-sm"
      >
        {icon ?? <span className="size-3.5 shrink-0" />}
        {s.intent_id && (
          <span className="shrink-0 rounded bg-muted px-1 py-0.5 font-mono text-[10px] tabular-nums text-muted-foreground">
            #{s.intent_id}
          </span>
        )}
        {s.inherited && s.source_task_id && (
          <Badge variant="outline" className="shrink-0">
            来源 #{s.source_task_id}
          </Badge>
        )}
        <span
          className={cn("min-w-0 flex-1 truncate text-sm font-medium", deleted && "text-muted-foreground line-through")}
        >
          {displayTitle}
        </span>
        {deleted && (
          <Badge variant="outline" className="shrink-0 border-destructive/40 text-destructive">
            已删除
          </Badge>
        )}
        {hasPending && <ShieldAlertIcon className="size-3.5 shrink-0 text-amber-500" />}
        {!active && unread ? (
          <span className="inline-flex min-w-4 items-center justify-center rounded-full bg-blue-500/15 px-1 text-[10px] font-medium tabular-nums text-blue-600 dark:text-blue-400">
            {unread > 99 ? "99+" : unread}
          </span>
        ) : null}
        {s.live && (
          <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-600 dark:text-blue-400">
            <span className="size-1 animate-pulse rounded-full bg-blue-500" />
            实时
          </span>
        )}
      </button>
      {cancellable && (
        <div className="flex shrink-0 items-center gap-0.5 pr-1 opacity-100 sm:opacity-0 sm:transition-opacity sm:group-hover/session:opacity-100 sm:group-focus-within/session:opacity-100">
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            onClick={onCancel}
            disabled={controlling}
            title="删除该意图（需填写原因，可选假删除/真删除）"
            aria-label="删除该意图（需填写原因，可选假删除/真删除）"
            className="text-destructive hover:text-destructive"
          >
            <Trash2Icon />
          </Button>
        </div>
      )}
    </div>
  );
}

export function truncateWorkerAssetLabel(value: string, maxChars = 30): string {
  const chars = Array.from(value.trim());
  if (chars.length <= maxChars) return chars.join("");
  return `${chars.slice(0, Math.max(0, maxChars - 1)).join("")}…`;
}

export function WorkerAssetBadge({ assets }: { assets: IntentAsset[] }) {
  const displayAssets = assets.filter(
    (asset) => asset.type === "root_domain" || asset.type === "subdomain" || asset.type === "ip",
  );
  if (displayAssets.length === 0) return null;

  const first = displayAssets[0];
  const firstRawLabel = first.label.trim() || `#${first.asset_id}`;
  const firstLabel = truncateWorkerAssetLabel(firstRawLabel);
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Badge variant="outline" className="max-w-60 shrink-0 font-normal" title={firstRawLabel}>
          <span className="truncate">当前资产：{firstLabel}</span>
          {displayAssets.length > 1 && <span className="shrink-0 tabular-nums">+{displayAssets.length - 1}</span>}
        </Badge>
      </TooltipTrigger>
      <TooltipContent side="right" align="start" className="max-w-sm">
        <div className="flex flex-col gap-2">
          {displayAssets.map((asset) => (
            <div key={`${asset.intent_id}-${asset.asset_id}`} className="min-w-0">
              <div className="break-all font-mono text-xs">{asset.label.trim() || `#${asset.asset_id}`}</div>
              <div className="mt-0.5 text-xs text-muted-foreground">
                {taskAssetTypeLabel(asset.type)} · {taskAssetSourceLabel(asset.source)}
                {asset.inherited ? ` · 来源任务 #${asset.source_task_id}` : ""}
              </div>
              <div className="mt-0.5 [overflow-wrap:anywhere] text-xs">{asset.source_summary}</div>
            </div>
          ))}
        </div>
      </TooltipContent>
    </Tooltip>
  );
}
