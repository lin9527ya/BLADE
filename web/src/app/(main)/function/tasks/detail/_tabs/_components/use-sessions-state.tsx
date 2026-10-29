"use client";

import * as React from "react";

import { toast } from "sonner";

import { useApprovalFocus, useApprovalHistory } from "@/components/approval-execution-focus";
import { usePolling } from "@/hooks/use-polling";
import { useSideQuestions } from "@/hooks/use-side-questions";
import { api, sseUrl } from "@/lib/api";
import { useChatSendMode } from "@/lib/chat-send-mode";
import { MOCK } from "@/lib/mock/enabled";
import type {
  Activity,
  ChatAttachment,
  IntentAsset,
  InterceptApprovalRow,
  Session,
  SessionTokenUsage,
  TaskLLMResolution,
  TaskLLMResolutions,
  TaskNode,
  TokenTotal,
  WorkerSlot,
} from "@/lib/types";
import { capSessionStore } from "@/lib/session-cache";
import { rememberInputHistory } from "@/lib/input-history";

import {
  emptyState,
  fmtBytes,
  fmtDuration,
  fmtTokens,
  isResumableWorkerStatus,
  keyForSession,
  MAIN_ID,
  MAIN_SESSION,
  MAX_KEEP,
  MAX_WORKER_MESSAGE_CHARS,
  mainSessionId,
  mainSessionKey,
  mainSessionTitle,
  mergeBySeq,
  newWorkerMessageRequestID,
  PAGE,
  PLANNER_ID,
  PLANNER_SESSION,
  resolutionLabel,
  roleMeta,
  type SessionState,
  type SessionStore,
  STREAM_WINDOW_MS,
  SYSTEM_ID,
  SYSTEM_SCAN_PAGE,
  SYSTEM_SESSION,
  sessionKeyOf,
  statusIcon,
  TokenMetrics,
  workerMessageCharCount,
  workerSlotSessionId,
  workerSlotToSession,
} from "./session-helpers";

// 会话页签的状态与取数：会话列表、分段消息、意图/资产、SSE、令牌与 LLM 解析。

export function useSessionsState(taskId: string) {
  const approvalFocus = useApprovalFocus({ taskId });
  const [selectedSessionId, setActiveId] = React.useState(MAIN_ID);
  const focusSession = React.useMemo<Session | undefined>(() => {
    const source = approvalFocus.state?.source;
    if (!source) return undefined;
    if (source.session.startsWith("main:")) {
      const seg = Number(source.session.slice(5));
      return { ...MAIN_SESSION, id: mainSessionId(seg), seg, title: mainSessionTitle(seg), live: false };
    }
    if (source.session === "plan") return { ...PLANNER_SESSION, live: false };
    if (source.session.startsWith("slot:")) {
      const slot = Number(source.session.slice(5));
      if (Number.isFinite(slot) && slot > 0) {
        return {
          id: workerSlotSessionId(slot),
          role: "worker",
          slot,
          worker_no: slot,
          title: `Worker #${slot}`,
          status: "done",
          live: false,
          last_activity: source.items[0]?.ts ?? "",
        };
      }
    }
    if (source.session.startsWith("intent:")) {
      const id = source.session.slice(7);
      return {
        id,
        role: "worker",
        intent_id: id,
        title: `Worker #${id}`,
        status: "done",
        live: false,
        last_activity: source.items[0]?.ts ?? "",
      };
    }
    return undefined;
  }, [approvalFocus.state?.source]);
  // Keep a located archived/older session selectable after leaving focus mode.
  const [locatedSession, setLocatedSession] = React.useState<{ taskId: string; session: Session }>();
  React.useEffect(() => {
    if (focusSession) setLocatedSession({ taskId, session: focusSession });
  }, [taskId, focusSession]);
  const retainedSession = locatedSession?.taskId === taskId ? locatedSession.session : undefined;
  const activeId = focusSession?.id ?? selectedSessionId;
  // Main-agent conversation segments (newest-first); currentSeg is the writable one.
  const [mainSegs, setMainSegs] = React.useState<{ seq: number; created_at: string }[]>([{ seq: 0, created_at: "" }]);
  const [currentSeg, setCurrentSeg] = React.useState(0);
  const [creatingMain, setCreatingMain] = React.useState(false);
  const [confirmNewMain, setConfirmNewMain] = React.useState(false);
  // 手机端（<lg）会话列表默认折叠：屏幕高度本就紧张，列表若固定占掉 10~15rem，
  // 下方的会话记录会被挤到只剩标题与输入框。折叠后记录区拿到几乎全部高度，
  // 点标题栏可展开选会话，选完自动收起。桌面端不受影响（lg 起始终展开）。
  const [listOpen, setListOpen] = React.useState(false);
  // Per-session lazily-loaded caches, keyed by session_key (main | plan | intent:<id>).
  const [store, setStore] = React.useState<SessionStore>({});
  // Fixed worker slots (exactly N sessions; reused across intents).
  const [workerSlots, setWorkerSlots] = React.useState<WorkerSlot[]>([]);
  // Worker sessions derived from exploration intents (kept for intent metadata/history).
  const [intents, setIntents] = React.useState<TaskNode[]>([]);
  const [intentAssets, setIntentAssets] = React.useState<IntentAsset[]>([]);
  const [olderIntents, setOlderIntents] = React.useState<TaskNode[]>([]);
  const [firstIntentsHasMore, setFirstIntentsHasMore] = React.useState(false);
  const [olderIntentsHasMore, setOlderIntentsHasMore] = React.useState(false);
  const [hasLoadedOlderIntentsPage, setHasLoadedOlderIntentsPage] = React.useState(false);
  const [loadingOlderIntents, setLoadingOlderIntents] = React.useState(false);
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [stopping, setStopping] = React.useState(false);
  const [mainChatRunning, setMainChatRunning] = React.useState<boolean | null>(null);
  const [controllingIntent, setControllingIntent] = React.useState<string | null>(null);
  const [cancelIntent, setCancelIntent] = React.useState<Session | null>(null);
  const [cancelReason, setCancelReason] = React.useState("");
  // 删除模式:soft=假删除(默认,置 deleted + 记原因,保留数据)| hard=真删除(级联移除独占子孙)。
  const [deleteMode, setDeleteMode] = React.useState<"soft" | "hard">("soft");
  const [workerMessage, setWorkerMessage] = React.useState("");
  const [workerMessageRequestId, setWorkerMessageRequestId] = React.useState("");
  const [workerMessageSending, setWorkerMessageSending] = React.useState(false);
  // 方式1 文件上传:选好的附件(已落到任务工作目录 uploads/),随下条消息一起发。
  const [attachments, setAttachments] = React.useState<ChatAttachment[]>([]);
  const [uploading, setUploading] = React.useState(false);
  const fileInputRef = React.useRef<HTMLInputElement>(null);

  async function pickFiles(files: FileList | null) {
    if (!files || files.length === 0) return;
    setUploading(true);
    try {
      const r = await api.chatUpload("task", taskId, Array.from(files));
      setAttachments((prev) => [...prev, ...r.attachments]);
    } catch (e) {
      toast.error(`上传失败：${(e as Error).message}`);
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  }

  // Start a fresh main-agent session: only the segment counter advances — the task's
  // graph/assets/goal are untouched, so the agent continues over the same task with a
  // clean context. The old segment stays as read-only history you can switch back to.
  async function createMainSession() {
    if (creatingMain) return;
    setCreatingMain(true);
    try {
      const r = await api.newMainSession(taskId);
      setMainSegs((prev) => [{ seq: r.seq, created_at: r.created_at }, ...prev.filter((m) => m.seq !== r.seq)]);
      setCurrentSeg(r.current ?? r.seq);
      // Seed an empty, loaded state so the new (empty) session renders immediately.
      setStore((prev) => ({ ...prev, [mainSessionKey(r.seq)]: { ...emptyState(), loaded: true } }));
      setActiveId(mainSessionId(r.seq));
      setListOpen(false);
      setInput("");
    } catch (e) {
      toast.error(`新建会话失败：${(e as Error).message}`);
    } finally {
      setCreatingMain(false);
      setConfirmNewMain(false);
    }
  }

  const patchIntentState = React.useCallback((intentId: string, state?: string) => {
    const patch = (rows: TaskNode[]) =>
      state
        ? rows.map((row) => (row.id === intentId ? { ...row, state } : row))
        : rows.filter((row) => row.id !== intentId);
    setIntents(patch);
    setOlderIntents(patch);
  }, []);

  const controlWorker = React.useCallback(
    async (session: Session, action: "pause" | "resume" | "cancel", reason?: string, mode?: "soft" | "hard") => {
      if (!session.intent_id || session.inherited || controllingIntent) return;
      if (action === "cancel" && !reason?.trim()) {
        toast.error("请填写删除原因");
        return;
      }
      setControllingIntent(session.intent_id);
      try {
        const res = await api.controlIntent(taskId, session.intent_id, action, reason, mode);
        if (action === "pause") {
          patchIntentState(session.intent_id, "paused");
          toast.success(`Worker #${session.worker_no ?? session.intent_id} 已暂停`);
        } else if (action === "resume") {
          patchIntentState(session.intent_id, "open");
          toast.success(`Worker #${session.worker_no ?? session.intent_id} 已恢复，等待重新领取`);
        } else if (mode === "hard") {
          // 真删除:意图及独占下游已物理移除,从列表剔除该行。
          patchIntentState(session.intent_id);
          const d = res.deleted;
          const extra = d ? `（含 ${d.intents} 意图 / ${d.facts} 事实 / ${d.findings} 漏洞）` : "";
          toast.success(`Worker #${session.worker_no ?? session.intent_id} 及其独占下游已彻底删除${extra}`);
          setCancelReason("");
        } else {
          // 假删除:意图置 deleted、记录删除原因,保留节点与产出。
          patchIntentState(session.intent_id, "deleted");
          toast.success(`Worker #${session.worker_no ?? session.intent_id} 已删除（原因已记录，规划者将据此重新规划）`);
          setCancelReason("");
        }
      } catch (error) {
        toast.error(`Worker 操作失败：${(error as Error).message}`);
      } finally {
        setControllingIntent(null);
        setCancelIntent(null);
      }
    },
    [controllingIntent, patchIntentState, taskId],
  );

  // SSE connection state — surfaced so a dropped realtime link is visible, never
  // silently shown as "no messages".
  const [sseLive, setSseLive] = React.useState(false);
  // Whole-task token total (all agents), polled from the backend aggregate.
  const [taskTokens, setTaskTokens] = React.useState<TokenTotal | null>(null);
  const [sessionTokens, setSessionTokens] = React.useState<Record<string, SessionTokenUsage>>({});
  const [llmResolutions, setLLMResolutions] = React.useState<TaskLLMResolutions | null>(null);
  // Pending intercept requests for this task — used to show warning icons on sessions.
  const [pendingIntercepts, setPendingIntercepts] = React.useState<InterceptApprovalRow[]>([]);

  // Refs backing SSE/loading without re-render churn.
  const snapshotRef = React.useRef(0); // task-level snapshot cursor → SSE since=
  const esRef = React.useRef<EventSource | null>(null);
  const activeKeyRef = React.useRef(mainSessionKey(0)); // current session key (for SSE dispatch/unread)
  const atBottomRef = React.useRef(true); // transcript pinned to bottom?
  const llmToastSeqRef = React.useRef<Set<number>>(new Set());
  const chatStatusRequestRef = React.useRef(0);
  const firstIntentsRef = React.useRef<TaskNode[]>([]);
  // Per-key request token: a stale response for a key is ignored (guards fast
  // latest/older interleaving). Writes are ALWAYS keyed, so a late response can only
  // touch its own session cache — never the currently-viewed one (see §7.5).
  const reqTokenRef = React.useRef<Record<string, number>>({});
  // Keys with a latest-page load in flight — dedups the double trigger where the
  // first-load effect and the active-session effect both want "main" on mount (the
  // former also opens the SSE, so it must not be pre-empted).
  const loadingKeysRef = React.useRef<Set<string>>(new Set());
  // 手动“重试”入口：SSE 尚未建立时重新跑一次首屏加载（含建流），而不是只补历史。
  // boot 定义在 taskId effect 内，生命周期与该任务绑定。
  const bootRef = React.useRef<(() => void) | null>(null);

  // ── store helpers ──────────────────────────────────────────────────────────────
  const patchStore = React.useCallback((key: string, fn: (s: SessionState) => SessionState) => {
    setStore((prev) => ({ ...prev, [key]: fn(prev[key] ?? emptyState()) }));
  }, []);

  // Load a session's LATEST page (before=0) on first open. Keyed + request-token
  // guarded so a switch away can't corrupt the view.
  const loadSession = React.useCallback(
    (key: string) => {
      if (MOCK) return; // MOCK preloads everything up front
      if (loadingKeysRef.current.has(key)) return; // already in flight (e.g. main on mount)
      loadingKeysRef.current.add(key);
      const token = (reqTokenRef.current[key] ?? 0) + 1;
      reqTokenRef.current[key] = token;
      patchStore(key, (s) => ({ ...s, loading: true, error: undefined }));

      if (key === "system") {
        void (async () => {
          let since = 0;
          let systemItems: Activity[] = [];
          for (;;) {
            const page = await api.activity(taskId, { since, limit: SYSTEM_SCAN_PAGE });
            if (reqTokenRef.current[key] !== token) return;
            systemItems = mergeBySeq(
              systemItems,
              page.items.filter(
                (item) => item.worker === "system" || item.kind === "llm_switch" || item.kind === "llm_failover",
              ),
            );
            if (page.items.length < SYSTEM_SCAN_PAGE || page.cursor <= since) break;
            since = page.cursor;
          }
          patchStore(key, (s) => {
            const items = mergeBySeq(systemItems, s.items);
            return {
              ...s,
              items,
              loaded: true,
              loading: false,
              hasMore: false,
              earliestSeq: items.length ? items[0].seq : 0,
              unread: 0,
              lastTs: items.length ? items[items.length - 1].ts : s.lastTs,
              error: undefined,
            };
          });
        })()
          .catch((error) => {
            if (reqTokenRef.current[key] !== token) return;
            patchStore(key, (s) => ({
              ...s,
              loading: false,
              error: (error as Error).message || "加载失败",
            }));
          })
          .finally(() => loadingKeysRef.current.delete(key));
        return;
      }

      api
        .activityHistory(taskId, key, 0, PAGE)
        .then((r) => {
          if (reqTokenRef.current[key] !== token) return; // superseded
          if (r.snapshotCursor > snapshotRef.current) snapshotRef.current = r.snapshotCursor;
          patchStore(key, (s) => {
            const items = mergeBySeq(r.items, s.items); // keep any live frames arrived meanwhile
            return {
              ...s,
              items,
              loaded: true,
              loading: false,
              hasMore: r.hasMore,
              earliestSeq: items.length ? items[0].seq : 0,
              firstTs: r.firstActivity || s.firstTs,
              unread: 0,
              error: undefined,
            };
          });
        })
        .catch((e) => {
          if (reqTokenRef.current[key] !== token) return;
          patchStore(key, (s) => ({ ...s, loading: false, error: (e as Error).message || "加载失败" }));
        })
        .finally(() => loadingKeysRef.current.delete(key));
    },
    [taskId, patchStore],
  );

  // 会话面板的“重试”：实时流不存在时说明首屏加载（含建流）曾失败，重跑整条
  // boot 流程；流已存在时只需补这个会话的历史。
  const retrySession = React.useCallback(
    (key: string) => {
      if (!esRef.current) {
        bootRef.current?.();
        return;
      }
      loadSession(key);
    },
    [loadSession],
  );

  // Load one older page (scroll-up) for a session, preserving scroll position.
  const loadEarlier = React.useCallback(
    (key: string, viewport: () => HTMLElement | null) => {
      const st = store[key];
      if (!st || st.loadingMore || !st.hasMore || !st.earliestSeq) return;
      const vp = viewport();
      const prevH = vp?.scrollHeight ?? 0;
      const prevTop = vp?.scrollTop ?? 0;
      patchStore(key, (s) => ({ ...s, loadingMore: true }));
      api
        .activityHistory(taskId, key, st.earliestSeq, PAGE)
        .then((r) => {
          patchStore(key, (s) => {
            const items = mergeBySeq(r.items, s.items);
            return {
              ...s,
              items,
              loadingMore: false,
              hasMore: r.hasMore,
              earliestSeq: items.length ? items[0].seq : s.earliestSeq,
              firstTs: r.firstActivity || s.firstTs,
            };
          });
          requestAnimationFrame(() => {
            const v = viewport();
            if (v) v.scrollTop = prevTop + (v.scrollHeight - prevH);
          });
        })
        .catch(() => {
          patchStore(key, (s) => ({ ...s, loadingMore: false }));
        });
    },
    [taskId, store, patchStore],
  );

  // ── task token total (whole task, all agents) ───────────────────────────────────
  usePolling(
    async (alive) => {
      const load = () =>
        api
          .tokenStats(taskId)
          .then((r) => {
            if (!alive()) return;
            setTaskTokens(r.total);
            setSessionTokens(Object.fromEntries(r.sessions.map((item) => [item.session, item])));
          })
          .catch((e) => {
            if (alive()) throw e;
          });
      await load();
    },
    5000,
    [taskId],
  );

  usePolling(
    async (alive) => {
      // 不在这里把 running 置回 null：每 2 秒置一次会让「运行中」徽标灭一下再亮，
      // liveMainSeg / anyLive 也跟着抖。失败时保留上一次的权威值（见下方 catch）。
      const load = () => {
        const request = ++chatStatusRequestRef.current;
        return api
          .chatStatus(taskId)
          .then(({ running }) => {
            if (alive() && chatStatusRequestRef.current === request) setMainChatRunning(running);
          })
          .catch((e) => {
            // Keep the last authoritative value. Before the first success, recent
            // activity remains a conservative fallback; let the hook back off.
            if (alive()) throw e;
          });
      };
      await load();
    },
    2000,
    [taskId],
  );

  usePolling(
    async (alive) => {
      const load = () =>
        api
          .taskLLMResolution(taskId)
          .then((value) => {
            if (alive()) setLLMResolutions(value);
          })
          .catch((e) => {
            if (alive()) throw e;
          });
      await load();
    },
    10_000,
    [taskId],
  );

  usePolling(
    async (alive) => {
      const load = () =>
        api
          .interceptTask(taskId)
          .then((rows) => {
            if (alive()) setPendingIntercepts(rows.filter((r) => r.status === "pending"));
          })
          .catch((e) => {
            if (alive()) throw e;
          });
      await load();
    },
    5000,
    [taskId],
  );

  // Re-render on a timer so "streaming" liveness recomputes as activity goes stale.
  // 这个 tick 挪到 recentLive 定义之后（见下方）——它只在「有东西正在流式」时才需要跑。
  const [, setTick] = React.useState(0);

  // ── first load + single task SSE ────────────────────────────────────────────────
  // On task open: load Main's latest page, take the task-level snapshot cursor from
  // it, THEN open ONE task SSE at since=snapshot_cursor. History covers id≤cursor and
  // the SSE covers id>cursor with no gap. The SSE tails ALL agents; frames are routed
  // by session_key. EventSource auto-reconnects and (via our `id:` lines → Last-Event-
  // ID) resumes from the DB, so a dropped realtime link self-heals; seq-merge dedups.
  React.useEffect(() => {
    setStore({});
    setWorkerSlots([]);
    setIntents([]);
    setIntentAssets([]);
    setOlderIntents([]);
    setFirstIntentsHasMore(false);
    setOlderIntentsHasMore(false);
    firstIntentsRef.current = []; // 别拿上个任务的首页快照去算 displaced
    setHasLoadedOlderIntentsPage(false);
    setTaskTokens(null);
    setSessionTokens({});
    setLLMResolutions(null);
    setSseLive(false);
    setActiveId(MAIN_ID); // a stale worker id from the previous task must not leak in
    setMainSegs([{ seq: 0, created_at: "" }]);
    setCurrentSeg(0);
    snapshotRef.current = 0;
    llmToastSeqRef.current = new Set();
    reqTokenRef.current = {};
    // Reserve the initial main key so the active-session effect (which fires for main on
    // mount) won't double-load it and pre-empt the SSE opened here.
    loadingKeysRef.current = new Set([mainSessionKey(0)]);
    let alive = true;

    // MOCK demo: no SSE backend — pull one activity snapshot and bucket by session.
    if (MOCK) {
      api
        .activity(taskId)
        .then((r) => {
          if (!alive) return;
          const buckets: SessionStore = {};
          for (const a of r.items) {
            const k = sessionKeyOf(a);
            if (!buckets[k]) buckets[k] = emptyState();
            buckets[k].items.push(a);
          }
          for (const k of Object.keys(buckets)) {
            const st = buckets[k];
            st.items.sort((p, q) => p.seq - q.seq);
            st.loaded = true;
            st.hasMore = false;
            st.earliestSeq = st.items.length ? st.items[0].seq : 0;
            st.lastTs = st.items.length ? st.items[st.items.length - 1].ts : "";
          }
          buckets[mainSessionKey(0)] ??= { ...emptyState(), loaded: true };
          buckets.system ??= { ...emptyState(), loaded: true };
          setStore(buckets);
        })
        .catch(() =>
          setStore({
            [mainSessionKey(0)]: { ...emptyState(), loaded: true },
            system: { ...emptyState(), loaded: true },
          }),
        );
      return () => {
        alive = false;
      };
    }

    const token = (reqTokenRef.current.mainboot ?? 0) + 1;
    reqTokenRef.current.mainboot = token;
    let bootKey = mainSessionKey(0);
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let bootInFlight = false;
    // Resolve the main-agent segments first, then load the CURRENT segment's history and
    // open the SSE from its snapshot cursor. The SSE tails all segments and routes each
    // frame by session_key (main:<seg>), so switching segments needs no new stream.
    //
    // 首次加载失败必须重试并最终重新建立 SSE：此前 catch 只把错误留在 store 里，
    // 既不重试也不再打开实时流，页面会永远停在快照 + 错误上。重试用指数退避，
    // 成功后才打开 EventSource（失败时没有可用的 snapshot cursor）。
    const boot = (attempt: number) => {
      if (bootInFlight) return;
      bootInFlight = true;
      if (retryTimer) {
        clearTimeout(retryTimer);
        retryTimer = undefined;
      }
      bootKey = mainSessionKey(0);
      loadingKeysRef.current.add(bootKey);
      patchStore(bootKey, (s) => ({ ...s, loading: true, error: undefined }));
      api
        .mainSessions(taskId)
        .then((ms) => {
          if (!alive || reqTokenRef.current.mainboot !== token) throw new Error("superseded");
          const segs = ms.sessions.length ? ms.sessions : [{ seq: 0, created_at: "" }];
          setMainSegs(segs);
          setCurrentSeg(ms.current);
          bootKey = mainSessionKey(ms.current);
          if (bootKey !== mainSessionKey(0)) loadingKeysRef.current.delete(mainSessionKey(0));
          loadingKeysRef.current.add(bootKey);
          setActiveId(mainSessionId(ms.current));
          patchStore(bootKey, (s) => ({ ...s, loading: true }));
          return api.activityHistory(taskId, bootKey, 0, PAGE);
        })
        .then((r) => {
          if (!alive || reqTokenRef.current.mainboot !== token) return;
          snapshotRef.current = r.snapshotCursor;
          patchStore(bootKey, (s) => {
            const items = mergeBySeq(r.items, s.items);
            return {
              ...s,
              items,
              loaded: true,
              loading: false,
              hasMore: r.hasMore,
              earliestSeq: items.length ? items[0].seq : 0,
              firstTs: r.firstActivity || s.firstTs,
              unread: 0,
            };
          });
          bootInFlight = false;
          loadingKeysRef.current.delete(bootKey);
          // Open the single task SSE from the snapshot cursor. Close a stale one first:
          // a retry must not leave two streams writing into the same store.
          esRef.current?.close();
          const es = new EventSource(
            sseUrl(`/api/exploration/activity/stream?task=${encodeURIComponent(taskId)}&since=${snapshotRef.current}`),
          );
          esRef.current = es;
          es.onopen = () => setSseLive(true);
          es.onerror = () => setSseLive(false); // EventSource auto-reconnects; DB compensates the gap
          // The server emits this when a subscriber buffer overflowed and closes
          // the stream right after. Reconnecting replays from Last-Event-ID, but
          // also refresh the visible session so a gap outside the replay window
          // (or a retention trim) is filled immediately.
          es.addEventListener("resync", () => {
            setSseLive(false);
            const key = activeKeyRef.current;
            if (key) loadSession(key);
          });
          es.onmessage = (e) => {
            let a: Activity;
            try {
              a = JSON.parse(e.data) as Activity;
            } catch {
              return; // ignore malformed frame
            }
            if ((a.kind === "llm_switch" || a.kind === "llm_failover") && !llmToastSeqRef.current.has(a.seq)) {
              llmToastSeqRef.current.add(a.seq);
              const transition = a.metadata?.llm_transition;
              if (transition?.mode === "exhausted" || a.is_error) {
                toast.error(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
              } else if (transition?.mode === "automatic") {
                toast.success(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
              } else {
                toast.info(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
              }
              void api
                .taskLLMResolution(taskId)
                .then((value) => {
                  if (alive) setLLMResolutions(value);
                })
                .catch(() => {
                  // The periodic resolver poll will retry if this event-triggered refresh fails.
                });
            }
            const k = sessionKeyOf(a);
            setStore((prev) => {
              const cur = prev[k] ?? emptyState();
              const activeK = activeKeyRef.current;
              // Merge into sessions that are loaded, actively loading, or the current
              // view (so returning is instant + a frame that lands mid-load isn't lost).
              // A cold, inactive session also keeps a tiny accounting tail so the
              // sidebar can add the latest unfinished usage without loading history.
              if (!cur.loaded && !cur.loading && k !== activeK) {
                const accountingItems =
                  a.kind === "usage" || a.kind === "result" ? mergeBySeq(cur.items, [a]).slice(-4) : cur.items;
                return {
                  ...prev,
                  [k]: { ...cur, items: accountingItems, lastTs: a.ts, unread: cur.unread + 1 },
                };
              }
              let items = mergeBySeq(cur.items, [a]);
              // Memory bound: trim oldest when over cap (older re-fetched on scroll-up),
              // but never while the user is reading this session's history (scrolled up).
              let hasMore = cur.hasMore;
              let earliestSeq = cur.earliestSeq;
              const trimmable = k !== activeK || atBottomRef.current;
              if (trimmable && items.length > MAX_KEEP) {
                items = items.slice(items.length - MAX_KEEP);
                hasMore = true;
                earliestSeq = items[0].seq;
              }
              const unread = k === activeK ? 0 : cur.unread + 1;
              return { ...prev, [k]: { ...cur, items, lastTs: a.ts, unread, hasMore, earliestSeq } };
            });
          };
        })
        .catch((err) => {
          if (!alive || reqTokenRef.current.mainboot !== token) return;
          if ((err as Error).message === "superseded") return;
          bootInFlight = false;
          loadingKeysRef.current.delete(bootKey);
          patchStore(bootKey, (s) => ({
            ...s,
            loading: false,
            error: (err as Error).message || "加载失败",
          }));
          if (!alive) return;
          const delay = Math.min(2000 * 2 ** Math.min(attempt, 3), 15_000);
          retryTimer = setTimeout(() => {
            if (alive) boot(attempt + 1);
          }, delay);
        });
    };
    bootRef.current = () => {
      if (alive) boot(0);
    };
    boot(0);

    return () => {
      alive = false;
      bootRef.current = null;
      if (retryTimer) clearTimeout(retryTimer);
      esRef.current?.close();
      esRef.current = null;
    };
  }, [taskId, patchStore, loadSession]);

  usePolling(
    async (active) => {
      const load = () =>
        api
          .taskIntentAssets(taskId)
          .then((assets) => {
            if (active()) setIntentAssets(assets);
          })
          .catch((e) => {
            // Worker controls and transcripts remain available; let the hook back off.
            if (active()) throw e;
          });
      await load();
    },
    5000,
    [taskId],
  );

  // ── worker (intent) session list — paged, poll first page lightly ───────────────
  usePolling(
    async (active) => {
      // 只拉第一页，不动本地状态。清空会让 worker 列表在请求返回前空一帧（右侧面板
      // 同时回落到主 Agent），还会把 displaced 保留逻辑和用户翻出来的 olderIntents
      // 一起抹掉。切任务时的重置在下面的 taskId effect 里做。
      const load = () =>
        api
          .intentsPage(taskId, 0, 300)
          .then((r) => {
            if (!active()) return;
            const freshIds = new Set(r.items.map((item) => item.id));
            const oldestFreshId = r.items.reduce((minimum, item) => Math.min(minimum, Number(item.id)), Infinity);
            const displaced = r.hasMore
              ? firstIntentsRef.current.filter((item) => !freshIds.has(item.id) && Number(item.id) < oldestFreshId)
              : [];
            if (displaced.length > 0) {
              setOlderIntents((previous) => {
                const byId = new Map(previous.map((item) => [item.id, item]));
                for (const item of displaced) byId.set(item.id, item);
                return [...byId.values()];
              });
            }
            firstIntentsRef.current = r.items;
            setIntents(r.items);
            setFirstIntentsHasMore(r.hasMore);
          })
          .catch((e) => {
            if (active()) throw e;
          });
      await load();
    },
    5000,
    [taskId],
  );

  usePolling(
    (alive) =>
      api
        .workerSlots(taskId)
        .then((r) => {
          if (alive()) setWorkerSlots(r.slots);
        })
        .catch((e) => {
          if (alive()) throw e;
        }),
    5000,
    [taskId],
  );

  const loadOlderIntents = React.useCallback(() => {
    if (loadingOlderIntents) return;
    const all = [...intents, ...olderIntents];
    const minId = all.reduce((m, n) => Math.min(m, Number(n.id)), Infinity);
    if (!Number.isFinite(minId)) return;
    setLoadingOlderIntents(true);
    api
      .intentsPage(taskId, minId, 300)
      .then((r) => {
        setOlderIntents((prev) => {
          const seen = new Set([...intents, ...prev].map((n) => n.id));
          return [...prev, ...r.items.filter((n) => !seen.has(n.id))];
        });
        setOlderIntentsHasMore(r.hasMore);
        setHasLoadedOlderIntentsPage(true);
      })
      .catch(() => {
        // A later manual retry can fetch this page again.
      })
      .finally(() => setLoadingOlderIntents(false));
  }, [taskId, intents, olderIntents, loadingOlderIntents]);

  // Combined, de-duplicated worker list (newest first page + older loaded pages).
  const allIntents = React.useMemo(() => {
    const byId = new Map<string, TaskNode>();
    for (const n of olderIntents) byId.set(n.id, n);
    for (const n of intents) byId.set(n.id, n); // fresh poll wins over older snapshot
    return [...byId.values()].sort((a, b) => Number(b.id) - Number(a.id));
  }, [intents, olderIntents]);
  const intentAssetsByID = React.useMemo(() => {
    const grouped = new Map<string, IntentAsset[]>();
    for (const asset of intentAssets) {
      const key = String(asset.intent_id);
      const current = grouped.get(key);
      if (current) current.push(asset);
      else grouped.set(key, [asset]);
    }
    return grouped;
  }, [intentAssets]);

  // Exactly the task's fixed N slots — never one session per intent.
  const workerSessions = React.useMemo(() => workerSlots.map(workerSlotToSession), [workerSlots]);
  const intentsHasMore = false; // legacy: the session list no longer pages per-intent

  // For each worker session (intent), derive the display title from the intent
  // payload summary. Also store the full TaskNode for the hover-JSON tooltip.
  const sessionMeta = React.useMemo(() => {
    const map = new Map<string, { title: string; json: unknown; deleted: boolean; deleteReason: string }>();
    for (const node of allIntents) {
      let title = `Intent ${node.id}`;
      let parsedPayload: unknown = node.payload;
      // 假删除:意图 state='deleted',删除原因在独立字段 delete_reason 上。
      const deleted = node.state === "deleted";
      const deleteReason = node.delete_reason ?? "";
      if (node.payload) {
        try {
          const p = JSON.parse(node.payload);
          parsedPayload = p;
          if (p?.summary) title = String(p.summary);
        } catch {
          title = node.payload.trim() || title;
        }
      }
      const json = { ...node, payload: parsedPayload };
      map.set(node.id, { title, json, deleted, deleteReason });
    }
    return map;
  }, [allIntents]);

  // Returns true if any pending intercept belongs to this session.
  // Worker agent_name format: "work#N · #intentID". Main/planner match by role key.
  const hasPendingForSession = React.useCallback(
    (s: Session): boolean => {
      if (s.inherited) return false;
      if (!pendingIntercepts.length) return false;
      if (s.role === "mainagent") return pendingIntercepts.some((r) => r.agent_name === "mainagent");
      if (s.role === "planner") return pendingIntercepts.some((r) => r.agent_name === "planner");
      // Worker: extract intent ID from "work#N · #<intentID>"
      return pendingIntercepts.some((r) => {
        const m = r.agent_name.match(/·\s*#(\d+)$/);
        return m ? m[1] === s.intent_id : false;
      });
    },
    [pendingIntercepts],
  );

  // Liveness from each session's last-seen activity time (kept fresh by the 1.5s tick).
  const recentLive = React.useCallback(
    (key: string) => {
      const ts = store[key]?.lastTs;
      if (!ts) return false;
      const t = Date.parse(ts);
      return t > 0 && Date.now() - t < STREAM_WINDOW_MS;
    },
    [store],
  );
  const currentMainKey = mainSessionKey(currentSeg);
  const plannerLive = recentLive("plan");

  // 只在「有东西正在流式」时才需要周期性重渲染：没有活的会话时，「活」这个渲染期判断
  // （recentLive 读 Date.now()）不会随时间改变，周期重渲染纯属浪费。此前是无条件每 1.5
  // 秒重渲染整个 hook——40 次/分钟，每次都把全部派生值重建一遍，而任务详情页大部分时间
  // 是没东西在跑的。
  // intervalMs=0 是 usePolling 的「只跑一次不重复」：不排下一次，也就彻底不再重渲染。
  // 有新活动进来（store 变化）会重新渲染，那时 anyLive 转真、那个 1.5 秒的 tick 自己接回来。
  const anyLive = sending || mainChatRunning === true || Object.keys(store).some((k) => recentLive(k));
  usePolling(() => setTick((t) => t + 1), anyLive ? 1500 : 0);

  // Which main segment is streaming RIGHT NOW. A main turn is serialized per task, so at
  // most one segment is live. Gate on the real running flag (sending / mainChatRunning)
  // rather than "recent activity", and drop it the moment the turn's terminal record
  // (kind='result', or an error) lands — otherwise the badge lingers for STREAM_WINDOW_MS
  // after the agent already finished. null = nothing running.
  const liveMainSeg = React.useMemo<number | null>(() => {
    if (!(sending || mainChatRunning)) return null;
    // the streaming segment is the one with the freshest activity (incl. the just-sent turn)
    let seg = currentSeg;
    let bestTs = -1;
    for (const m of mainSegs) {
      const raw = store[mainSessionKey(m.seq)]?.lastTs;
      const ts = raw ? Date.parse(raw) : -1;
      if (ts > bestTs) {
        bestTs = ts;
        seg = m.seq;
      }
    }
    const items = store[mainSessionKey(seg)]?.items ?? [];
    const last = items[items.length - 1];
    if (last && (last.kind === "result" || (last.kind === "text" && last.is_error))) return null;
    return seg;
  }, [sending, mainChatRunning, mainSegs, store, currentSeg]);

  // Every main-agent segment is an independent, interactive session (like the top-level
  // chat conversations) — you can talk in any of them, newest-first.
  const mainSessions = React.useMemo<Session[]>(
    () =>
      mainSegs.map((m) => ({
        id: mainSessionId(m.seq),
        role: "mainagent",
        title: mainSessionTitle(m.seq),
        status: "running",
        live: m.seq === liveMainSeg,
        last_activity: m.created_at,
        seg: m.seq,
      })),
    [mainSegs, liveMainSeg],
  );

  const sessions = React.useMemo(() => {
    const items = [...mainSessions, { ...PLANNER_SESSION, live: plannerLive }, ...workerSessions, SYSTEM_SESSION];
    const located = focusSession ?? retainedSession;
    if (located && !items.some((s) => s.id === located.id)) items.push(located);
    return items;
  }, [mainSessions, workerSessions, plannerLive, focusSession, retainedSession]);

  const grouped = {
    mainagent: sessions.filter((s) => s.role === "mainagent"),
    planner: sessions.filter((s) => s.role === "planner"),
    worker: sessions.filter((s) => s.role === "worker"),
    system: sessions.filter((s) => s.role === "system"),
  };

  const active = sessions.find((s) => s.id === activeId) ?? MAIN_SESSION;
  const side = useSideQuestions(
    active.role === "mainagent"
      ? `/api/tasks/${taskId}/chat`
      : active.role === "worker" && !active.inherited && active.intent_id
        ? `/api/tasks/${taskId}/intents/${active.intent_id}`
        : null,
  );
  const isMain = active.role === "mainagent";
  const isPlanner = active.role === "planner";
  const isSystem = active.role === "system";
  const activeKey = keyForSession(active);
  const activeState = store[activeKey];
  // A main turn is serialized per task (chat lock), so "busy" is task-wide: while any
  // main segment is mid-turn the active composer is disabled. Clear it the moment the
  // active session's terminal record (kind='result', or an error) lands, so the input
  // re-enables immediately instead of waiting for the next chat-status poll.
  const activeItems = activeState?.items ?? [];
  const activeLast = activeItems[activeItems.length - 1];
  const activeSettled =
    !!activeLast && (activeLast.kind === "result" || (activeLast.kind === "text" && activeLast.is_error));
  const mainBusy = isMain && (sending || (!activeSettled && (mainChatRunning ?? recentLive(activeKey))));
  // 折叠态（手机端）标题栏要替代整张列表：显示当前会话名 + 其它会话的未读合计，
  // 否则收起后既不知道自己在看哪个会话，也看不到别处有新消息。
  const activeDisplayTitle =
    (active.role === "worker" ? sessionMeta.get(active.intent_id ?? "")?.title : "") || active.title;
  const hiddenUnread = React.useMemo(
    () => Object.entries(store).reduce((sum, [key, s]) => (key === activeKey ? sum : sum + s.unread), 0),
    [store, activeKey],
  );

  // Keep the SSE dispatcher's notion of the active session current, and lazily load
  // + clear unread whenever the active session changes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: cache updates must not reactivate the current session.
  React.useEffect(() => {
    activeKeyRef.current = activeKey;
    const st = store[activeKey];
    if (!st || (!st.loaded && !st.loading)) {
      loadSession(activeKey);
    } else if (st.unread) {
      patchStore(activeKey, (s) => ({ ...s, unread: 0 }));
    }
  }, [activeKey]);

  // MAX_KEEP bounds one session; this bounds the whole open-session set.
  // Check at most every two seconds: summing every session on every SSE frame
  // would make the memory guard itself a source of render work.
  const capCheckedAtRef = React.useRef(0);
  React.useEffect(() => {
    const now = Date.now();
    if (now - capCheckedAtRef.current < 2000) return;
    capCheckedAtRef.current = now;
    setStore((prev) => capSessionStore(prev, activeKeyRef.current));
  }, [store, activeKey]);

  const focusKey = approvalFocus.state?.source?.session;
  const loadFocusPage = React.useCallback(
    (before: number) => api.activityHistory(taskId, focusKey ?? "main", before, PAGE),
    [taskId, focusKey],
  );
  const mergeFocusPage = React.useCallback(
    (page: { items: Activity[]; hasMore: boolean }) => {
      if (!focusKey) return;
      patchStore(focusKey, (s) => {
        const items = mergeBySeq(page.items, s.items);
        return { ...s, items, hasMore: page.hasMore, earliestSeq: items[0]?.seq ?? s.earliestSeq };
      });
    },
    [focusKey, patchStore],
  );
  const focusHistory = useApprovalHistory(
    approvalFocus.state?.source,
    !!focusKey && !!store[focusKey]?.loaded,
    focusKey ? (store[focusKey]?.items ?? []) : [],
    loadFocusPage,
    mergeFocusPage,
  );
  React.useEffect(() => {
    if (approvalFocus.state) atBottomRef.current = false;
  }, [approvalFocus.state]);

  // Main agent is the human↔orchestrator CONSOLE: only the conversation (user msgs +
  // the main agent's own replies/steps). Planner session shows planner steps; a
  // worker session shows only its intent's activity, led by the intent objective.
  const activity = React.useMemo(() => {
    const items = activeState?.items ?? [];
    // The main-agent console renders purely from server data: the human turn is
    // persisted+broadcast by the backend BEFORE it returns, so it arrives over the
    // same SSE stream (worker="mainagent") as every agent step — no client-side
    // optimistic echo, hence no fabricated seq that could collide with real DB ids.
    if (isMain) return items;
    if (isPlanner || isSystem) return items;
    // Slot sessions get a backend "intent" boundary row on each new intent, so the
    // transcript already leads with the real objective. Only legacy intent-focused
    // sessions still need the client-side synthetic bubble.
    if (active.slot) return items;
    const intentTitle = sessionMeta.get(active.intent_id ?? "")?.title ?? active.title;
    const intentMsg: Activity = {
      seq: -1, // sorts/leads before any real step (real seq ≥ 0)
      worker: items[0]?.worker ?? active.id, // reuse the lane so no worker chips appear
      ts: active.last_activity || "",
      kind: "intent", // LLM-generated objective — rendered as a distinct (non-human) bubble
      summary: intentTitle,
      source_task_id: active.source_task_id,
      inherited: active.inherited,
    };
    return [intentMsg, ...items];
  }, [
    isMain,
    isPlanner,
    isSystem,
    activeState,
    active.id,
    active.title,
    active.last_activity,
    active.source_task_id,
    active.inherited,
    sessionMeta,
  ]);

  // seq of this session's most-recent TodoWrite call — for the Todo popover.
  const latestTodoSeq = React.useMemo(() => {
    for (let i = activity.length - 1; i >= 0; i--) {
      const a = activity[i];
      if (a.kind === "tool_use" && a.tool === "TodoWrite") return a.seq;
    }
    return null;
  }, [activity]);

  // Persisted result totals come from the backend's full activity history. Only
  // the latest unfinished usage frame is added client-side, so partial history
  // pages cannot undercount completed runs or double-count the active one.
  const tokenForSession = React.useCallback(
    (key: string): TokenTotal => {
      const saved = sessionTokens[key];
      const total: TokenTotal = {
        input_tokens: saved?.input_tokens ?? 0,
        output_tokens: saved?.output_tokens ?? 0,
        cache_read_tokens: saved?.cache_read_tokens ?? 0,
        cache_write_tokens: saved?.cache_write_tokens ?? 0,
      };
      const items = store[key]?.items ?? [];
      for (let index = items.length - 1; index >= 0; index--) {
        const item = items[index];
        if (item.kind === "result") break;
        if (item.kind !== "usage") continue;
        total.input_tokens += item.input_tokens ?? 0;
        total.output_tokens += item.output_tokens ?? 0;
        total.cache_read_tokens += item.cache_read_tokens ?? 0;
        total.cache_write_tokens += item.cache_write_tokens ?? 0;
        break;
      }
      return total;
    },
    [sessionTokens, store],
  );

  const activeTokens = tokenForSession(activeKey);
  const tokenTotal = {
    i: activeTokens.input_tokens,
    o: activeTokens.output_tokens,
    cr: activeTokens.cache_read_tokens,
    any:
      activeTokens.input_tokens +
        activeTokens.output_tokens +
        activeTokens.cache_read_tokens +
        activeTokens.cache_write_tokens >
      0,
  };

  // Run duration = span from this session's first step to its last (for a live
  // session, "now" so it ticks up — the 1.5s setTick above re-renders it).
  // 运行时长 = 首步 → 末步。首步优先用服务端给的**整个会话**首步时间：
  // 历史是倒序分页的，只加载最近 N 步时，用窗口内最早一条会把时长算短。
  // 末步取已加载窗口的最大时间（较新的页总在窗口里，不会失真）。
  const runDuration = React.useMemo(() => {
    let min = activeState?.firstTs ? Date.parse(activeState.firstTs) : Infinity;
    let max = 0;
    for (const a of activity) {
      const t = Date.parse(a.ts);
      if (!Number.isFinite(t)) continue;
      if (t < min) min = t;
      if (t > max) max = t;
    }
    if (!Number.isFinite(min) || max === 0) return null;
    const end = active.live ? Date.now() : max;
    return Math.max(0, end - min);
  }, [activity, active.live, activeState?.firstTs]);

  // ---- transcript auto-scroll (open → bottom; stick to bottom unless scrolled up) ----
  const contentRef = React.useRef<HTMLDivElement | null>(null);
  const viewport = React.useCallback(
    () => (contentRef.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null) ?? null,
    [],
  );
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeId intentionally rebinds the scroll listener.
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      if (approvalFocus.state && !focusHistory.ready) return;
      atBottomRef.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - 60;
      if (vp.scrollTop <= 80) loadEarlier(activeKeyRef.current, viewport); // near top → older page
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport, activeId, loadEarlier, approvalFocus.state, focusHistory.ready]);
  // open/switch a session → jump to the latest (bottom)
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeId intentionally scrolls a newly selected session.
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp && !approvalFocus.state) {
      vp.scrollTop = vp.scrollHeight;
      atBottomRef.current = true;
    }
  }, [activeId, viewport, approvalFocus.state]);
  // new activity → stick to bottom only if the user is already pinned there
  // biome-ignore lint/correctness/useExhaustiveDependencies: activity growth intentionally drives live-edge scrolling.
  React.useLayoutEffect(() => {
    if (approvalFocus.state || !atBottomRef.current) return;
    const vp = viewport();
    if (vp) vp.scrollTop = vp.scrollHeight;
  }, [activity, viewport, approvalFocus.state]);
  // Lazy detail loads (AnswerBlock / ToolBlock / Markdown) grow the content AFTER the
  // activity array settles, WITHOUT changing its reference — so the layout effects
  // above never re-fire and a freshly opened session would leave its last message
  // scrolled partly off-screen (the final answer expands from a one-line summary to
  // full markdown below the fold). A ResizeObserver re-pins to the bottom on any
  // height change while the user is still at the bottom, so opening the main agent
  // lands on the last message fully shown. contentRef's div is always mounted, so the
  // observer catches the transcript mounting + each detail expanding.
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeId intentionally rebinds the observer to the new session's content.
  React.useEffect(() => {
    const el = contentRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (approvalFocus.state || !atBottomRef.current) return;
      const vp = viewport();
      if (vp) vp.scrollTop = vp.scrollHeight;
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [activeId, viewport, approvalFocus.state]);

  function stop() {
    if (stopping) return;
    setStopping(true);
    void api.stopChat(taskId).finally(() => setStopping(false));
  }

  function send() {
    const text = input.trim();
    const atts = attachments;
    if (side.handleCommand(text, () => setInput(""))) return;
    if ((!text && atts.length === 0) || sending || mainBusy) return;
    if (text) rememberInputHistory(`task:${taskId}:${active.id}`, text);
    // No optimistic echo: the backend persists+broadcasts the human turn before it
    // returns, so it streams back over SSE (worker="mainagent") with its real DB
    // seq — the transcript renders it from server data like every other step. Clear
    // the composer eagerly for responsiveness; restore it if the send fails.
    setInput("");
    setAttachments([]);
    setSending(true);
    chatStatusRequestRef.current++;
    api
      .chat(text, taskId, atts.length > 0 ? atts : undefined, active.seg ?? 0)
      .then(({ mode }) => {
        chatStatusRequestRef.current++;
        setMainChatRunning(mode === "llm");
      })
      .catch((e) => {
        setInput(text); // restore so the user doesn't lose their text / attachments
        setAttachments(atts);
        toast.error(`发送失败：${(e as Error).message || "请稍后重试"}`);
      })
      .finally(() => setSending(false));
  }

  function sendWorkerChat() {
    const intentId = active.intent_id;
    const message = workerMessage.trim();
    if (side.handleCommand(message, () => setWorkerMessage(""))) return;
    // 运行中 = 实时注入（下一步动作前生效，拦掉那一次工具调用让模型改道）；
    // 已暂停 / 已中断（blocked/exhausted/stopped/no_output）= 从原会话续发。
    // 其余状态（done/negative/out-of-scope 等）是有结论的收场，不发。
    const steer = active.status === "running";
    const canResume = active.status === "paused" || isResumableWorkerStatus(active.status);
    if (!intentId || active.inherited || (!steer && !canResume) || workerMessageSending || !message) return;
    if (workerMessageCharCount(message) > MAX_WORKER_MESSAGE_CHARS) {
      toast.error(`消息不能超过 ${MAX_WORKER_MESSAGE_CHARS} 个字符`);
      return;
    }
    rememberInputHistory(`task:${taskId}:${active.id}`, message);
    const requestId = workerMessageRequestId || newWorkerMessageRequestID();
    if (!workerMessageRequestId) setWorkerMessageRequestId(requestId);
    setWorkerMessageSending(true);
    api
      .sendWorkerMessage(taskId, intentId, message, requestId)
      .then((result) => {
        // The server records the user turn and streams the run over SSE, so there is
        // no optimistic insert: the message and the continuation arrive live.
        patchIntentState(intentId, result.state);
        setWorkerMessage("");
        setWorkerMessageRequestId("");
        toast.success(
          result.steered
            ? `提示已投递：Worker #${active.worker_no ?? intentId} 下一次动作前生效（那一次动作会被拦下、据此改道）`
            : active.status === "paused"
              ? `消息已发送给 Worker #${active.worker_no ?? intentId}，已立即继续执行`
              : `消息已发送给 Worker #${active.worker_no ?? intentId}，已从原会话继续执行`,
        );
      })
      .catch((error) => {
        toast.error(`发送失败：${(error as Error).message || "请稍后重试"}`);
      })
      .finally(() => setWorkerMessageSending(false));
  }

  const mainLoaded = !!store[currentMainKey]?.loaded;
  // 发送键位由系统设置决定（localStorage），默认 Enter 发送。
  const sendMode = useChatSendMode();
  // What the transcript pane should show for the active session.
  const showLoader = !activeState || (activeState.loading && !activeState.loaded);
  const resolutionForSession = (session: Session): TaskLLMResolution | undefined => {
    if (!llmResolutions || session.inherited || session.role === "system") return undefined;
    if (session.role === "mainagent") return llmResolutions.mainagent;
    if (session.role === "planner") return llmResolutions.planner;
    return llmResolutions.worker;
  };
  const activeResolution = resolutionForSession(active);
  const activeAssets =
    active.role === "worker" && active.intent_id ? intentAssetsByID.get(active.intent_id) : undefined;

  return {
    approvalFocus,
    selectedSessionId,
    setActiveId,
    focusSession,
    locatedSession,
    setLocatedSession,
    retainedSession,
    activeId,
    mainSegs,
    setMainSegs,
    currentSeg,
    setCurrentSeg,
    creatingMain,
    setCreatingMain,
    confirmNewMain,
    setConfirmNewMain,
    listOpen,
    setListOpen,
    store,
    setStore,
    intents,
    setIntents,
    intentAssets,
    setIntentAssets,
    olderIntents,
    setOlderIntents,
    firstIntentsHasMore,
    setFirstIntentsHasMore,
    olderIntentsHasMore,
    setOlderIntentsHasMore,
    hasLoadedOlderIntentsPage,
    setHasLoadedOlderIntentsPage,
    loadingOlderIntents,
    setLoadingOlderIntents,
    input,
    setInput,
    sending,
    setSending,
    stopping,
    setStopping,
    mainChatRunning,
    setMainChatRunning,
    controllingIntent,
    setControllingIntent,
    cancelIntent,
    setCancelIntent,
    cancelReason,
    setCancelReason,
    deleteMode,
    setDeleteMode,
    workerMessage,
    setWorkerMessage,
    workerMessageRequestId,
    setWorkerMessageRequestId,
    workerMessageSending,
    setWorkerMessageSending,
    attachments,
    setAttachments,
    uploading,
    setUploading,
    fileInputRef,
    patchIntentState,
    controlWorker,
    sseLive,
    setSseLive,
    taskTokens,
    setTaskTokens,
    sessionTokens,
    setSessionTokens,
    llmResolutions,
    setLLMResolutions,
    pendingIntercepts,
    setPendingIntercepts,
    snapshotRef,
    esRef,
    activeKeyRef,
    atBottomRef,
    llmToastSeqRef,
    chatStatusRequestRef,
    firstIntentsRef,
    reqTokenRef,
    loadingKeysRef,
    patchStore,
    loadSession,
    retrySession,
    loadEarlier,
    loadOlderIntents,
    allIntents,
    intentAssetsByID,
    workerSessions,
    intentsHasMore,
    sessionMeta,
    hasPendingForSession,
    recentLive,
    currentMainKey,
    plannerLive,
    liveMainSeg,
    mainSessions,
    sessions,
    grouped,
    active,
    side,
    isMain,
    isPlanner,
    isSystem,
    activeKey,
    activeState,
    activeItems,
    activeLast,
    activeSettled,
    mainBusy,
    activeDisplayTitle,
    hiddenUnread,
    focusKey,
    loadFocusPage,
    mergeFocusPage,
    focusHistory,
    activity,
    latestTodoSeq,
    tokenForSession,
    activeTokens,
    tokenTotal,
    runDuration,
    contentRef,
    viewport,
    stop,
    send,
    sendWorkerChat,
    mainLoaded,
    sendMode,
    showLoader,
    resolutionForSession,
    activeResolution,
    activeAssets,
    pickFiles,
    createMainSession,
  };
}
