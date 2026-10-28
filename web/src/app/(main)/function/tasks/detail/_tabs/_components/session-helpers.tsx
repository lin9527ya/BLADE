"use client";

import * as React from "react";

import {
  BrainIcon,
  CircleCheckIcon,
  CircleDashedIcon,
  CircleMinusIcon,
  CircleSlashIcon,
  CircleXIcon,
  ClockIcon,
  HistoryIcon,
  Loader2Icon,
  PauseIcon,
  RadioIcon,
  UserIcon,
  ZapOffIcon,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import type { Activity, Session, SessionStatus, TaskLLMResolution, TaskNode, WorkerSlot } from "@/lib/types";
import { cn } from "@/lib/utils";

// 会话页签的类型、常量与纯函数：会话键、消息合并、状态图标与意图映射。

export function resolutionLabel(r: TaskLLMResolution): string {
  return r.name || r.model || "未命名配置";
}

// fmtBytes renders a human file size for attachment chips (mirrors transcript.tsx).
export function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

// ── Reliability model (see docs/task-session-history-sse-remediation.md) ──────────
// The task's activity is NO LONGER one unbounded `allActivity` array replayed from
// SSE since=0. Instead:
//   • Each UI session (main | plan | intent:<id>) has its own lazily-loaded, reverse-
//     paginated cache (SessionState below). Opening a session loads only its latest
//     page; scrolling up pages older history in.
//   • A single task-level SSE (opened at since=snapshot_cursor from the first history
//     page) tails ALL agents' new activity; frames are dispatched by session_key.
//   • History + SSE meet gap-free at snapshot_cursor and are merged by seq (dedup),
//     so refresh / tab-switch / sleep / reconnect never drop the newest records.

export const PAGE = 200; // history page size
export const SYSTEM_SCAN_PAGE = 500; // generic activity pages scanned to recover sparse system audit events
export const MAX_KEEP = 4000; // per-session in-memory cap; older pages re-fetched on scroll-up
export const STREAM_WINDOW_MS = 5000; // "live" = activity seen within this window
export const MAX_WORKER_MESSAGE_CHARS = 4000;

export function newWorkerMessageRequestID(): string {
  return (
    globalThis.crypto?.randomUUID?.() ??
    `worker-message-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
  );
}

export function workerMessageCharCount(value: string): number {
  return Array.from(value).length;
}

// One session's lazily-loaded, reverse-paginated cache. `lastTs`/`unread` are kept
// live even for sessions that were never opened, so the list shows liveness + unread
// without holding their full history.
export type SessionState = {
  items: Activity[];
  loaded: boolean;
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean; // older history remains above the loaded window
  earliestSeq: number; // earliest loaded id — reverse-pagination anchor
  firstTs: string; // server-reported first activity of the WHOLE session (not just the window)
  unread: number;
  lastTs: string; // most-recent activity time (drives live badge; updated even when unloaded)
  error?: string;
};
export type SessionStore = Record<string, SessionState>;

export function emptyState(): SessionState {
  return {
    items: [],
    loaded: false,
    loading: false,
    loadingMore: false,
    hasMore: false,
    earliestSeq: 0,
    firstTs: "",
    unread: 0,
    lastTs: "",
  };
}

// sessionKeyOf routes an activity to its stable session key. worker="planner" covers
// BOTH the Goal Agent's round-0 decomposition and the Planner (single Plan session).
export function sessionKeyOf(a: Activity): string {
  if (a.worker === "system" || a.kind === "llm_switch" || a.kind === "llm_failover") return "system";
  if (a.worker === "mainagent") return `main:${a.main_seg ?? 0}`; // one key per conversation segment
  if (a.worker === "planner") return "plan";
  // Fixed worker slots are the primary key; pre-upgrade rows fall back to intent.
  if (a.slot_no) return `slot:${a.slot_no}`;
  if (a.intent_id) return `intent:${a.intent_id}`;
  return "unknown";
}

// mergeBySeq unions two activity lists by seq (dedup) in ascending seq order. Every
// data source — latest page, older page, SSE compensation, live tail — goes through
// this, so history responses can never clobber live records received meanwhile.
export function mergeBySeq(current: Activity[], incoming: Activity[]): Activity[] {
  if (!incoming.length) return current;
  const bySeq = new Map<number, Activity>();
  for (const a of current) bySeq.set(a.seq, a);
  for (const a of incoming) bySeq.set(a.seq, a);
  return [...bySeq.values()].sort((p, q) => p.seq - q.seq);
}

// statusIcon maps a session status to its icon. Worker terminal states are
// distinct & color-coded: 完成(绿勾圈) / 已测未发现(绿减号圈) / 范围外(灰虚线圈) /
// 取消停止(琥珀斜杠圈) / 出错(红叉圈) / 步数耗尽(紫). running=蓝色转圈,
// pending(待领取)=灰时钟.
export function statusIcon(status: SessionStatus) {
  switch (status) {
    case "running": // 执行中
      return <Loader2Icon className="size-3.5 animate-spin text-blue-500 dark:text-blue-400" />;
    case "paused":
      return <PauseIcon className="size-3.5 text-amber-500 dark:text-amber-400" />;
    case "pending": // 待领取(open intent)
      return <ClockIcon className="size-3.5 text-muted-foreground" />;
    case "done": // 完成
      return <CircleCheckIcon className="size-3.5 text-emerald-500 dark:text-emerald-400" />;
    case "stopped": // 取消/停止(被 planner 终止)
      return <CircleSlashIcon className="size-3.5 text-amber-500" />;
    case "blocked": // 出错
      return <CircleXIcon className="size-3.5 text-red-500 dark:text-red-400" />;
    case "exhausted": // 步数耗尽(撞 max_turns)
      return <ZapOffIcon className="size-3.5 text-violet-500 dark:text-violet-400" />;
    case "no_output": // 跑完了但什么都没写回（可重派）
      return <CircleDashedIcon className="size-3.5 text-amber-500 dark:text-amber-400" />;
    case "negative": // 已测(未发现)：正常成功收尾，只是回包明确否定
      return <CircleMinusIcon className="size-3.5 text-emerald-500 dark:text-emerald-400" />;
    case "out-of-scope": // 范围外：超出任务授权范围
      return <CircleDashedIcon className="size-3.5 text-muted-foreground" />;
    case "deleted": // 用户假删除
      return <CircleSlashIcon className="size-3.5 text-muted-foreground" />;
    case "idle": // 固定槽位空闲
      return <CircleDashedIcon className="size-3.5 text-muted-foreground/60" />;
    default: {
      // 给 SessionStatus 加成员却忘了在这里配图标时，这一行会让 tsc 直接报错，
      // 而不是像以前那样静默落空（negative/out-of-scope 漏映射就是这么被放过去的）。
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M).
export function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k`;
  return String(n);
}

export const TokenMetrics = React.forwardRef<
  HTMLSpanElement,
  React.ComponentPropsWithoutRef<"span"> & {
    input: number;
    cache: number;
    output: number;
    labels?: "short" | "long";
  }
>(({ input, cache, output, labels = "short", className, ...props }, ref) => {
  const names = labels === "short" ? ["入", "缓", "出"] : ["input", "cache", "output"];
  const values = [input, cache, output];
  return (
    <span
      ref={ref}
      className={cn("inline-flex min-w-0 flex-wrap items-center gap-1 text-muted-foreground", className)}
      {...props}
    >
      {values.map((value, index) => (
        <React.Fragment key={names[index]}>
          <span>{names[index]}</span>
          <Badge variant="secondary" className="h-5 px-1.5 font-mono tabular-nums">
            {fmtTokens(value)}
          </Badge>
        </React.Fragment>
      ))}
    </span>
  );
});
TokenMetrics.displayName = "TokenMetrics";

// fmtDuration renders an elapsed milliseconds span compactly (90s → 1m30s).
export function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

export const roleMeta = {
  mainagent: { label: "主 Agent", icon: UserIcon },
  planner: { label: "规划 Planner", icon: BrainIcon },
  worker: { label: "Workers", icon: RadioIcon },
  system: { label: "系统审计", icon: HistoryIcon },
} as const;

// The main-agent session is the interactive entry point of this tab and has no
// dedicated backend "sessions" endpoint — it is a fixed UI affordance whose
// transcript is the main-agent activity stream (worker="mainagent") for the task.
// A main-agent session is one resettable conversation segment. Segment 0 is the
// original session; "新建会话" creates further segments (seq 1,2,…) so the agent
// starts on a clean transcript while the task's graph/assets/goal stay shared. Each
// segment is a switchable UI session; only the current (highest) one is writable.
export const mainSessionId = (seg: number) => `s-main-${seg}`;
export const mainSessionKey = (seg: number) => `main:${seg}`;
export const mainSessionTitle = (seg: number) => `主 Agent · 会话 #${seg + 1}`;
export const MAIN_ID = mainSessionId(0);
export const MAIN_SESSION: Session = {
  id: MAIN_ID,
  role: "mainagent",
  title: mainSessionTitle(0),
  status: "running",
  live: true,
  last_activity: "",
  seg: 0,
};

// The planner session is, like the main-agent session, a fixed UI affordance with
// no dedicated backend "sessions" endpoint — its transcript is every activity step
// the planner emits (worker === "planner", which also carries the Goal Agent's
// round-0 decomposition; the planner carries no intent_id since it generates intents).
export const PLANNER_ID = "s-planner";
export const PLANNER_SESSION: Session = {
  id: PLANNER_ID,
  role: "planner",
  title: "规划 Planner · 态势研判",
  status: "running",
  live: true,
  last_activity: "",
};

// LLM provider switches are task-level audit events rather than agent output. The
// history endpoint has no "system" filter, so this fixed session is populated by
// scanning the generic incremental activity endpoint and then tailed by task SSE.
export const SYSTEM_ID = "s-system";
export const SYSTEM_SESSION: Session = {
  id: SYSTEM_ID,
  role: "system",
  title: "系统事件 · LLM 故障转移",
  status: "done",
  live: false,
  last_activity: "",
};

// keyForSession maps a UI Session → its stable store key
// (main:<seg> | plan | slot:<n> | intent:<id> for legacy focused views).
export function keyForSession(s: Session): string {
  if (s.role === "mainagent") return `main:${s.seg ?? 0}`;
  if (s.role === "planner") return "plan";
  if (s.role === "system") return "system";
  if (s.slot) return `slot:${s.slot}`;
  return `intent:${s.intent_id}`;
}

export const workerSlotSessionId = (slot: number) => `s-slot-${slot}`;

// Slot-derived sessions: exactly N, stable ids, and the current intent (if any)
// supplies the status/title. An idle slot stays visible between intents.
export function workerSlotToSession(w: WorkerSlot): Session {
  const current = !!w.intent_id;
  return {
    id: workerSlotSessionId(w.slot),
    role: "worker",
    title: current ? w.summary || `Intent ${w.intent_id}` : `Worker #${w.slot} · 等待下一个意图`,
    status: current ? intentStatus(w.state) : "idle",
    live: current && w.state === "running",
    last_activity: w.last_activity || w.started_at || "",
    intent_id: w.intent_id,
    worker_no: w.slot,
    slot: w.slot,
  };
}

// Map an exploration intent (TaskNode) state → a session status the UI renders.
//
// **必须覆盖 exploration_nodes.state 的全部取值**（db/schema.sql 里 kind='intent' 的
// CHECK：open/running/paused/done/negative/out-of-scope/blocked/exhausted/no_output/stopped/deleted）。
// 漏一个就会落进 default 被当成 running——worker 明明已经收尾，会话页却永远转圈、
// 底部一直显示「实时流式中…」（negative/out-of-scope 就是这样漏掉的）。
export function intentStatus(state: string): SessionStatus {
  switch (state) {
    case "done":
      return "done";
    case "blocked":
      return "blocked";
    case "exhausted":
      return "exhausted";
    case "no_output": // 空跑：什么都没写回，按未覆盖处理
      return "no_output";
    case "stopped":
      return "stopped";
    case "paused":
      return "paused";
    case "open": // 待领取，区别于执行中
      return "pending";
    case "deleted": // 用户假删除
      return "deleted";
    case "negative": // 已测(未发现)
      return "negative";
    case "out-of-scope": // 范围外
      return "out-of-scope";
    default: // running
      return "running";
  }
}

// 中断终态：没有结论、可以从原 transcript 带人工消息续跑。与后端
// db.IntentRecoverableStates 保持一致（done/negative/out-of-scope 不在此列——
// 那是有结论的收场，换方向应新建意图）。
export const RESUMABLE_WORKER_STATUSES: readonly SessionStatus[] = ["blocked", "exhausted", "stopped", "no_output"];

export function isResumableWorkerStatus(status: SessionStatus): boolean {
  return (RESUMABLE_WORKER_STATUSES as readonly string[]).includes(status);
}

// Derive worker sessions from running/open intents — there is no backend
// sessions endpoint, so intents (≈ worker units) are the closest real source.
export function intentToSession(n: TaskNode): Session {
  const label = (n.payload ?? "").trim();
  const state = intentStatus(n.state);
  return {
    id: n.id,
    role: "worker",
    title: label || `Intent ${n.id}`,
    status: state,
    live: !n.inherited && state === "running",
    last_activity: n.ts,
    intent_id: n.id,
    worker_no: n.worker_seq,
    source_task_id: n.source_task_id,
    inherited: n.inherited,
  };
}
