"use client";

import * as React from "react";

import { useRouter } from "next/navigation";

import {
  BellIcon,
  BugIcon,
  CalendarClockIcon,
  CheckCheckIcon,
  ShieldAlertIcon,
  TargetIcon,
  WalletIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Spinner } from "@/components/ui/spinner";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { statusMeta } from "@/lib/status";
import type { InboxItem } from "@/lib/types";
import { cn } from "@/lib/utils";

const KIND_ICON = {
  approval: ShieldAlertIcon,
  task: TargetIcon,
  schedule: CalendarClockIcon,
  finding: BugIcon,
  budget: WalletIcon,
} as const;

function relTime(iso: string): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return "";
  const secs = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (secs < 60) return "刚刚";
  if (secs < 3600) return `${Math.floor(secs / 60)} 分钟前`;
  if (secs < 86400) return `${Math.floor(secs / 3600)} 小时前`;
  return `${Math.floor(secs / 86400)} 天前`;
}

// InboxBell 是站内通知中心：数据来自现有审批/任务/计划/发现事件，不依赖外部渠道。
export function InboxBell() {
  const router = useRouter();
  const [open, setOpen] = React.useState(false);
  const [items, setItems] = React.useState<InboxItem[]>([]);
  const [unread, setUnread] = React.useState(0);
  const [readAt, setReadAt] = React.useState("");
  const [loading, setLoading] = React.useState(true);
  const [marking, setMarking] = React.useState(false);

  const load = React.useCallback(async (alive: () => boolean = () => true) => {
    try {
      const page = await api.inbox(100);
      if (!alive()) return;
      setItems(page.items);
      setUnread(page.unread);
      setReadAt(page.read_at ?? "");
    } catch {
      // Keep the previous snapshot; the bell is not allowed to nag on transient failures.
    } finally {
      if (alive()) setLoading(false);
    }
  }, []);

  usePolling((alive) => load(alive), 30000, [load]);

  async function markAllRead() {
    setMarking(true);
    try {
      await api.markInboxRead();
      setUnread(0);
      setReadAt(new Date().toISOString());
      toast.success("已全部标记为已读");
      await load();
    } catch (e) {
      toast.error(`标记失败：${(e as Error).message}`);
    } finally {
      setMarking(false);
    }
  }

  function openItem(item: InboxItem) {
    setOpen(false);
    router.push(item.url);
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button variant="ghost" size="icon" className="relative size-8" aria-label="站内通知">
          <BellIcon className="size-4" />
          {unread > 0 ? (
            <span className="absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-destructive px-1 font-medium text-[10px] text-destructive-foreground tabular-nums">
              {unread > 99 ? "99+" : unread}
            </span>
          ) : null}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-[min(92vw,24rem)] p-0">
        <div className="flex items-center justify-between border-b px-3 py-2">
          <span className="font-medium text-sm">通知</span>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 gap-1 px-2 text-xs"
            onClick={() => void markAllRead()}
            disabled={marking || unread === 0}
          >
            <CheckCheckIcon className="size-3.5" />
            全部已读
          </Button>
        </div>
        <div className="max-h-[26rem] overflow-y-auto p-1">
          {loading && items.length === 0 ? (
            <div className="flex items-center justify-center py-10">
              <Spinner />
            </div>
          ) : items.length === 0 ? (
            <Empty className="border-0 py-8">
              <EmptyHeader>
                <EmptyTitle>没有通知</EmptyTitle>
                <EmptyDescription>待审批、任务终态、计划失败和新发现会出现在这里。</EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            items.map((item) => {
              const Icon = KIND_ICON[item.kind as keyof typeof KIND_ICON] ?? BellIcon;
              const unreadItem = readAt ? Date.parse(item.created_at) > Date.parse(readAt) : unread > 0;
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => openItem(item)}
                  className={cn(
                    "flex w-full items-start gap-2 rounded-md px-2 py-2 text-left hover:bg-muted/60",
                    unreadItem && "bg-muted/30",
                  )}
                >
                  <Icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1">
                    <span className="flex items-center gap-1.5">
                      <span className="truncate font-medium text-sm">{item.title}</span>
                      {item.severity ? (
                        <span className="shrink-0 text-[10px] text-muted-foreground">
                          {statusMeta("severity", item.severity).label}
                        </span>
                      ) : null}
                    </span>
                    {item.subtitle ? (
                      <span className="line-clamp-2 text-muted-foreground text-xs">{item.subtitle}</span>
                    ) : null}
                  </span>
                  <span className="shrink-0 text-[10px] text-muted-foreground">{relTime(item.created_at)}</span>
                </button>
              );
            })
          )}
        </div>
      </PopoverContent>
    </Popover>
  );
}
