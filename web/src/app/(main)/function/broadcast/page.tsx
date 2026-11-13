"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpRightIcon,
  ChevronDownIcon,
  ExternalLinkIcon,
  RadioIcon,
  RefreshCwIcon,
  SearchIcon,
} from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter, CardHeader } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Switch } from "@/components/ui/switch";
import { usePolling } from "@/hooks/use-polling";
import { api, sseUrl } from "@/lib/api";
import {
  BROADCAST_KIND_CHIP,
  BROADCAST_KIND_LABEL,
  BROADCAST_KINDS,
  broadcastStateLabel,
  broadcastSummary,
  broadcastViewKind,
  prettyBroadcastPayload,
} from "@/lib/broadcast-format";
import type { ExploreKind, GlobalBroadcastItem, Task } from "@/lib/types";
import { cn } from "@/lib/utils";

const PAGE_SIZES = [30, 50, 100];
const POLL_MS = 8000;
const SSE_FALLBACK_MS = 30_000;

const STATE_OPTIONS = [
  { value: "", label: "全部状态" },
  { value: "open", label: "待领" },
  { value: "running", label: "执行中" },
  { value: "paused", label: "已暂停" },
  { value: "done", label: "已完成" },
  { value: "confirmed", label: "已确认" },
  { value: "dismissed", label: "已排除" },
  { value: "open_proof_gap", label: "待补证" },
  { value: "negative", label: "已测（未发现）" },
  { value: "no_output", label: "无产出" },
  { value: "deleted", label: "已删除" },
  { value: "active", label: "在手" },
  { value: "lost", label: "已丢失" },
  { value: "cleaned", label: "已清理" },
];

const FINDING_STATUS_OPTIONS = [
  { value: "", label: "全部发现状态" },
  { value: "pending", label: "待处理" },
  { value: "in_progress", label: "处理中" },
  { value: "confirmed", label: "已确认" },
  { value: "resolved", label: "已处理" },
  { value: "fixed", label: "已修复" },
  { value: "ruled_out", label: "已排除" },
  { value: "open_proof_gap", label: "待补证" },
  { value: "false_positive", label: "误报" },
  { value: "ignored", label: "忽略" },
];

const TASK_STATE_OPTIONS = [
  { value: "", label: "全部任务状态" },
  { value: "running", label: "运行中" },
  { value: "paused", label: "已暂停" },
  { value: "queued", label: "排队中" },
  { value: "done", label: "已完成" },
  { value: "failed", label: "失败" },
  { value: "timeout", label: "已超时" },
];

const TIME_RANGE_OPTIONS = [
  { value: "all", label: "全部时间", seconds: 0 },
  { value: "1h", label: "最近 1 小时", seconds: 3600 },
  { value: "24h", label: "最近 24 小时", seconds: 86400 },
  { value: "7d", label: "最近 7 天", seconds: 604800 },
  { value: "30d", label: "最近 30 天", seconds: 2592000 },
];

function timeRangeSince(range: string): number | undefined {
  const option = TIME_RANGE_OPTIONS.find((o) => o.value === range);
  if (!option || option.seconds <= 0) return undefined;
  return Math.floor(Date.now() / 1000) - option.seconds;
}

const timeFmt = new Intl.DateTimeFormat("zh-CN", {
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

function relTime(ts: number, now: number): string {
  if (!now || !ts) return "";
  const sec = Math.max(0, (now - ts) / 1000);
  if (sec < 60) return "刚刚";
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`;
  return `${Math.floor(sec / 86400)} 天前`;
}

function taskLabel(t: Task): string {
  return t.name?.trim() || t.goal.trim() || `任务 #${t.id}`;
}

function itemHref(item: GlobalBroadcastItem): string {
  const base = `/function/tasks/detail?id=${encodeURIComponent(item.task_id)}`;
  return item.session_key
    ? `${base}&tab=sessions&session=${encodeURIComponent(item.session_key)}`
    : `${base}&tab=broadcast`;
}

function KindChip({ kind }: { kind: string }) {
  return (
    <span
      className={cn(
        "whitespace-nowrap rounded px-1.5 py-0.5 font-medium text-xs",
        BROADCAST_KIND_CHIP[kind] ?? BROADCAST_KIND_CHIP.fact,
      )}
    >
      {BROADCAST_KIND_LABEL[kind] ?? kind}
    </span>
  );
}

function Row({ item, now }: { item: GlobalBroadcastItem; now: number }) {
  const [open, setOpen] = React.useState(false);
  const kind = broadcastViewKind(item);
  const ts = Date.parse(item.ts);
  const summary = broadcastSummary(item);
  const href = itemHref(item);
  const boardHref = `/function/tasks/detail?id=${encodeURIComponent(item.task_id)}&tab=broadcast`;

  return (
    <div className="border-b px-4 py-3 last:border-b-0 md:px-6">
      <div className="flex gap-3">
        <div className="w-28 shrink-0 text-right text-muted-foreground text-xs tabular-nums">
          <div>{Number.isNaN(ts) ? "--" : timeFmt.format(ts)}</div>
          <div className="text-[11px] opacity-70">{relTime(ts, now)}</div>
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <KindChip kind={kind} />
            {item.type === "goal" ? (
              <StatusBadge domain="goal" value={item.state} dot />
            ) : item.type === "intent" ? (
              <StatusBadge domain="intent" value={item.state} dot />
            ) : item.type === "finding" && item.finding_status ? (
              <StatusBadge domain="finding" value={item.finding_status} dot />
            ) : (
              <span className="rounded bg-muted px-1.5 py-0.5 text-muted-foreground text-xs">
                {broadcastStateLabel(item)}
              </span>
            )}
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-muted-foreground text-xs">#{item.id}</code>
            {item.finding_status && item.finding_status !== item.state && (
              <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-amber-600 text-xs dark:text-amber-400">
                finding: {item.finding_status}
              </span>
            )}
            <span className="ml-auto flex min-w-0 items-center gap-2">
              <StatusBadge domain="task" value={item.task_state} dot />
              <Link
                href={`/function/tasks/detail?id=${encodeURIComponent(item.task_id)}&tab=sessions`}
                className="max-w-52 truncate text-muted-foreground text-xs hover:text-foreground hover:underline"
                title={item.task_name}
              >
                {item.task_name}
              </Link>
            </span>
          </div>
          <Link
            href={href}
            className="mt-1 line-clamp-3 break-words text-sm leading-6 hover:text-primary hover:underline"
            title={summary || (item.session_key ? "打开产生这条播报的会话" : "该节点没有精确会话，打开任务播报")}
          >
            {summary || `节点 #${item.id}`}
          </Link>
          <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-muted-foreground text-xs">
            <span>来源 {item.origin || "system"}</span>
            {item.session_key && (
              <Link href={href} className="inline-flex items-center gap-0.5 text-primary hover:underline">
                查看会话 <ArrowUpRightIcon className="size-3" />
              </Link>
            )}
            <Link href={boardHref} className="inline-flex items-center gap-0.5 hover:text-foreground hover:underline">
              任务播报 <ExternalLinkIcon className="size-3" />
            </Link>
            <button
              type="button"
              onClick={() => setOpen((v) => !v)}
              className="ml-auto inline-flex items-center gap-0.5 hover:text-foreground"
            >
              payload
              <ChevronDownIcon className={cn("size-3 transition-transform", open && "rotate-180")} />
            </button>
          </div>
          {open && (
            <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap rounded-md border bg-muted/30 p-3 font-mono text-[11px]">
              {prettyBroadcastPayload(item.payload)}
            </pre>
          )}
        </div>
      </div>
    </div>
  );
}

export default function GlobalBroadcastPage() {
  const [page, setPage] = React.useState(1);
  const [size, setSize] = React.useState(PAGE_SIZES[0]);
  const [kinds, setKinds] = React.useState<ExploreKind[]>([]);
  const [queryInput, setQueryInput] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [order, setOrder] = React.useState<"asc" | "desc">("desc");
  const [taskFilter, setTaskFilter] = React.useState("");
  const [stateFilter, setStateFilter] = React.useState("");
  const [findingStatus, setFindingStatus] = React.useState("");
  const [taskState, setTaskState] = React.useState("");
  const [timeRange, setTimeRange] = React.useState("all");
  const [live, setLive] = React.useState(true);
  const [sseLive, setSseLive] = React.useState(false);
  const [refreshTick, setRefreshTick] = React.useState(0);
  const [items, setItems] = React.useState<GlobalBroadcastItem[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<string | null>(null);
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [now, setNow] = React.useState(0);
  const requestRef = React.useRef(0);

  React.useEffect(() => {
    let alive = true;
    api
      .tasks({ page: 1, size: 200 })
      .then((r) => {
        if (alive) setTasks(r.tasks);
      })
      .catch(() => {
        /* 任务下拉失败不阻塞播报流：行内仍带任务名 */
      });
    return () => {
      alive = false;
    };
  }, []);

  React.useEffect(() => {
    const t = setTimeout(() => {
      setQuery(queryInput);
      setPage(1);
    }, 300);
    return () => clearTimeout(t);
  }, [queryInput]);

  usePolling(() => setNow(Date.now()), 30_000);

  // 全局 SSE：服务端按节点 id 追库，只推新节点；浏览器收到后按当前筛选重拉。
  // SSE 断开时保留 8 秒轮询兜底，SSE 正常时降到 30 秒一次心跳校验。
  React.useEffect(() => {
    if (!live) {
      setSseLive(false);
      return;
    }
    let timer: ReturnType<typeof setTimeout> | undefined;
    const es = new EventSource(sseUrl("/api/broadcast/stream"));
    es.onopen = () => {
      setSseLive(true);
      setRefreshTick((v) => v + 1); // 连上后补一次，覆盖首屏请求与建流之间的空隙
    };
    es.onerror = () => setSseLive(false);
    const bump = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => setRefreshTick((v) => v + 1), 300);
    };
    es.addEventListener("node", bump);
    return () => {
      if (timer) clearTimeout(timer);
      es.removeEventListener("node", bump);
      es.close();
    };
  }, [live]);

  const stateFilterKey = stateFilter;
  const findingStatusKey = findingStatus;
  const taskStateKey = taskState;
  const timeRangeKey = timeRange;
  usePolling(
    async (alive) => {
      const req = ++requestRef.current;
      setLoading(true);
      try {
        const r = await api.globalBroadcast({
          page,
          size,
          kinds,
          states: stateFilter ? [stateFilter] : undefined,
          findingStatuses: findingStatus ? [findingStatus] : undefined,
          taskStates: taskState ? [taskState] : undefined,
          since: timeRangeSince(timeRange),
          q: query,
          order,
          task: taskFilter || undefined,
        });
        if (!alive() || req !== requestRef.current) return;
        setItems(r.items);
        setTotal(r.total);
        setError(null);
      } catch (e) {
        if (!alive() || req !== requestRef.current) return;
        setError((e as Error).message || "加载失败");
        throw e;
      } finally {
        if (alive() && req === requestRef.current) setLoading(false);
      }
    },
    live ? (sseLive ? SSE_FALLBACK_MS : POLL_MS) : 0,
    [
      page,
      size,
      kinds.join(","),
      query,
      order,
      taskFilter,
      stateFilterKey,
      findingStatusKey,
      taskStateKey,
      timeRangeKey,
      live,
      sseLive,
      refreshTick,
    ],
  );

  const toggleKind = (kind: ExploreKind) => {
    setKinds((prev) => (prev.includes(kind) ? prev.filter((k) => k !== kind) : [...prev, kind]));
    setPage(1);
  };

  const resetFilters = () => {
    setKinds([]);
    setStateFilter("");
    setFindingStatus("");
    setTaskState("");
    setTimeRange("all");
    setTaskFilter("");
    setQueryInput("");
    setQuery("");
    setPage(1);
  };

  const manualRefresh = () => {
    setNow(Date.now());
    const req = ++requestRef.current; // 让在途轮询的响应失效，触发一次新的读取
    setLoading(true);
    void api
      .globalBroadcast({
        page,
        size,
        kinds,
        states: stateFilter ? [stateFilter] : undefined,
        findingStatuses: findingStatus ? [findingStatus] : undefined,
        taskStates: taskState ? [taskState] : undefined,
        since: timeRangeSince(timeRange),
        q: query,
        order,
        task: taskFilter || undefined,
      })
      .then((r) => {
        if (req !== requestRef.current) return;
        setItems(r.items);
        setTotal(r.total);
        setError(null);
      })
      .catch((e) => {
        if (req === requestRef.current) setError((e as Error).message || "加载失败");
      })
      .finally(() => {
        if (req === requestRef.current) setLoading(false);
      });
  };

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
            <RadioIcon className="size-5 text-primary" />
            全局播报
          </h1>
          <p className="text-muted-foreground text-sm">跨全部任务的探索流水，点击一条可直接打开对应会话</p>
        </div>
        <div className="flex items-center gap-3 text-muted-foreground text-sm">
          <span className="tabular-nums">{loading ? "加载中…" : `${items.length} / ${total} 条`}</span>
          <span className="flex items-center gap-2">
            {live ? (sseLive ? "SSE 实时" : "轮询") : "已暂停"}
            <Switch checked={live} onCheckedChange={setLive} aria-label="实时刷新" />
          </span>
          <Button variant="outline" size="icon" className="size-8" onClick={manualRefresh} disabled={loading}>
            {loading ? <Spinner /> : <RefreshCwIcon />}
          </Button>
        </div>
      </div>

      <Card className="gap-0 overflow-hidden py-0">
        <CardHeader className="gap-3 border-b px-4 py-4 md:px-6">
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative min-w-56 flex-1">
              <SearchIcon className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={queryInput}
                onChange={(e) => setQueryInput(e.target.value)}
                placeholder="搜摘要 / payload / 节点 #id"
                className="h-9 pl-8"
              />
            </div>
            <Select
              value={taskFilter || "all"}
              onValueChange={(v) => {
                setTaskFilter(v === "all" ? "" : v);
                setPage(1);
              }}
            >
              <SelectTrigger className="h-9 w-56">
                <SelectValue placeholder="全部任务" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">全部任务</SelectItem>
                {tasks.map((t) => (
                  <SelectItem key={t.id} value={t.id}>
                    {taskLabel(t)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={order}
              onValueChange={(v) => {
                setOrder(v as "asc" | "desc");
                setPage(1);
              }}
            >
              <SelectTrigger className="h-9 w-28">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="desc">最新在前</SelectItem>
                <SelectItem value="asc">最早在前</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <Button
              type="button"
              variant={kinds.length === 0 ? "secondary" : "outline"}
              size="sm"
              className="h-7 px-2 text-xs"
              onClick={() => {
                setKinds([]);
                setPage(1);
              }}
            >
              全部
            </Button>
            {BROADCAST_KINDS.map((kind) => (
              <Button
                key={kind}
                type="button"
                variant={kinds.includes(kind) ? "secondary" : "outline"}
                size="sm"
                className="h-7 px-2 text-xs"
                onClick={() => toggleKind(kind)}
              >
                {BROADCAST_KIND_LABEL[kind] ?? kind}
              </Button>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Select
              value={stateFilter || "all"}
              onValueChange={(v) => {
                setStateFilter(v === "all" ? "" : v);
                setPage(1);
              }}
            >
              <SelectTrigger className="h-8 w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STATE_OPTIONS.map((o) => (
                  <SelectItem key={o.value || "all"} value={o.value || "all"}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={findingStatus || "all"}
              onValueChange={(v) => {
                setFindingStatus(v === "all" ? "" : v);
                setPage(1);
              }}
            >
              <SelectTrigger className="h-8 w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FINDING_STATUS_OPTIONS.map((o) => (
                  <SelectItem key={o.value || "all"} value={o.value || "all"}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={taskState || "all"}
              onValueChange={(v) => {
                setTaskState(v === "all" ? "" : v);
                setPage(1);
              }}
            >
              <SelectTrigger className="h-8 w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TASK_STATE_OPTIONS.map((o) => (
                  <SelectItem key={o.value || "all"} value={o.value || "all"}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select
              value={timeRange}
              onValueChange={(v) => {
                setTimeRange(v);
                setPage(1);
              }}
            >
              <SelectTrigger className="h-8 w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TIME_RANGE_OPTIONS.map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button type="button" variant="ghost" size="sm" className="h-8 px-2 text-xs" onClick={resetFilters}>
              重置筛选
            </Button>
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {error && <div className="border-b bg-destructive/5 px-4 py-2 text-destructive text-sm md:px-6">{error}</div>}
          {items.length === 0 && !loading ? (
            <div className="px-4 py-16 text-center text-muted-foreground text-sm">没有匹配的播报</div>
          ) : (
            <div>
              {items.map((item) => (
                <Row key={item.id} item={item} now={now} />
              ))}
            </div>
          )}
        </CardContent>
        <CardFooter className="p-0">
          <TablePagination
            page={page}
            pageSize={size}
            total={total}
            onPageChange={setPage}
            onPageSizeChange={(s) => {
              setSize(s);
              setPage(1);
            }}
            pageSizeOptions={PAGE_SIZES}
            busy={loading}
          />
        </CardFooter>
      </Card>
    </div>
  );
}
