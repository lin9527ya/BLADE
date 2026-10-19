"use client";

import * as React from "react";

import {
  ArrowUpIcon,
  BrainIcon,
  ChevronDownIcon,
  CircleCheckIcon,
  CircleSlashIcon,
  CircleXIcon,
  ClockIcon,
  HistoryIcon,
  Loader2Icon,
  PaperclipIcon,
  PauseIcon,
  PlusIcon,
  RadioIcon,
  RotateCwIcon,
  ShieldAlertIcon,
  SquareIcon,
  Trash2Icon,
  UserIcon,
  WifiOffIcon,
  XIcon,
  ZapOffIcon,
} from "lucide-react";
import { toast } from "sonner";

import { ApprovalExecutionFocus, useApprovalFocus, useApprovalHistory } from "@/components/approval-execution-focus";
import { MentionTextarea } from "@/components/mention-textarea";
import { SideQuestionButton, SideQuestionWorkspace } from "@/components/side-question-workspace";
import { TodoPopover } from "@/components/todo-popover";
import { Transcript } from "@/components/transcript";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { InputGroup, InputGroupAddon, InputGroupButton } from "@/components/ui/input-group";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { usePolling } from "@/hooks/use-polling";
import { useSideQuestions } from "@/hooks/use-side-questions";
import { api, sseUrl } from "@/lib/api";
import { shouldSubmitOnKey, useChatSendMode } from "@/lib/chat-send-mode";
import { MOCK } from "@/lib/mock/enabled";
import { isBtwCommand } from "@/lib/side-questions";
import { taskAssetSourceLabel, taskAssetTypeLabel } from "@/lib/task-assets";
import type {
  Activity,
  ChatAttachment,
  IntentAsset,
  InterceptApprovalRow,
  Session,
  SessionStatus,
  SessionTokenUsage,
  TaskLLMResolution,
  TaskLLMResolutions,
  TaskNode,
  TokenTotal,
} from "@/lib/types";
import { cn } from "@/lib/utils";

import {
  emptyState,
  fmtBytes,
  fmtDuration,
  fmtTokens,
  intentStatus,
  intentToSession,
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
  SessionState,
  SessionStore,
  STREAM_WINDOW_MS,
  SYSTEM_ID,
  SYSTEM_SCAN_PAGE,
  SYSTEM_SESSION,
  sessionKeyOf,
  statusIcon,
  TokenMetrics,
  workerMessageCharCount,
} from "./_components/session-helpers";
import { useSessionsState } from "./_components/use-sessions-state";

// resolutionLabel names the LLM a session runs on. The badge shows this alone and
// keeps the model id in its tooltip. Env-backed configs can arrive without a name,
// so fall back to the model id rather than rendering an empty badge.

function SessionItem({
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

function truncateWorkerAssetLabel(value: string, maxChars = 30): string {
  const chars = Array.from(value.trim());
  if (chars.length <= maxChars) return chars.join("");
  return `${chars.slice(0, Math.max(0, maxChars - 1)).join("")}…`;
}

function WorkerAssetBadge({ assets }: { assets: IntentAsset[] }) {
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

export function SessionsTab({ taskId }: { taskId: string }) {
  const {
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
  } = useSessionsState(taskId);

  return (
    <TooltipProvider delayDuration={300}>
      {/* 高度预留：页面头部（标题行 + 目标 + Tabs ≈ 7.5rem）+ 内容内边距。手机端 p-4、
        桌面端 lg:p-6，且桌面还要留出滚动余量，所以两档分别预留 10rem / 13rem —— 手机端
        沿用 13rem 会白白吃掉 3rem 的记录高度。 */}
      <div
        className={cn(
          "grid h-[calc(100svh-10rem)] min-h-0 grid-cols-1 gap-4",
          listOpen ? "grid-rows-[minmax(0,40svh)_minmax(0,1fr)]" : "grid-rows-[auto_minmax(0,1fr)]",
          "lg:h-[calc(100svh-13rem)] lg:grid-cols-[18rem_1fr] lg:grid-rows-[minmax(0,1fr)]",
        )}
      >
        {/* Left: session list */}
        <div className="flex min-h-0 flex-col overflow-hidden rounded-lg border bg-card">
          <div className="border-b px-3 py-2">
            <div className="flex items-center justify-between gap-2">
              <button
                type="button"
                onClick={() => setListOpen((open) => !open)}
                aria-expanded={listOpen}
                aria-controls="session-list-panel"
                className="flex min-w-0 flex-1 items-center gap-1 text-left text-xs font-medium text-muted-foreground lg:pointer-events-none"
              >
                <ChevronDownIcon
                  className={cn("size-3.5 shrink-0 transition-transform lg:hidden", !listOpen && "-rotate-90")}
                />
                <span className="shrink-0">会话列表</span>
                {!listOpen && (
                  <>
                    <span className="min-w-0 truncate text-foreground lg:hidden" title={activeDisplayTitle}>
                      · {activeDisplayTitle}
                    </span>
                    {hiddenUnread > 0 && (
                      <span className="inline-flex min-w-4 shrink-0 items-center justify-center rounded-full bg-blue-500/15 px-1 text-[10px] font-medium tabular-nums text-blue-600 lg:hidden dark:text-blue-400">
                        {hiddenUnread > 99 ? "99+" : hiddenUnread}
                      </span>
                    )}
                  </>
                )}
              </button>
              {!MOCK && (
                <span
                  className={cn(
                    "inline-flex items-center gap-1 text-[10px]",
                    sseLive ? "text-emerald-500" : "text-amber-500",
                  )}
                  title={sseLive ? "实时连接正常" : "实时连接中断，正在自动重连（历史仍可见）"}
                >
                  {sseLive ? (
                    <>
                      <span className="size-1 animate-pulse rounded-full bg-emerald-500" />
                      实时
                    </>
                  ) : (
                    <>
                      <WifiOffIcon className="size-3" />
                      重连中
                    </>
                  )}
                </span>
              )}
            </div>
            {taskTokens && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <div className="mt-2 flex min-w-0 flex-wrap items-center gap-1 text-xs text-muted-foreground">
                    <span>任务总计</span>
                    <span>·</span>
                    <TokenMetrics
                      input={taskTokens.input_tokens}
                      cache={taskTokens.cache_read_tokens}
                      output={taskTokens.output_tokens}
                    />
                  </div>
                </TooltipTrigger>
                <TooltipContent>
                  输入 {taskTokens.input_tokens.toLocaleString()} · 输出 {taskTokens.output_tokens.toLocaleString()} ·
                  缓存读取 {taskTokens.cache_read_tokens.toLocaleString()} · 缓存写入{" "}
                  {taskTokens.cache_write_tokens.toLocaleString()}
                </TooltipContent>
              </Tooltip>
            )}
          </div>
          <ScrollArea
            id="session-list-panel"
            type="auto"
            className={cn(
              "min-h-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!",
              !listOpen && "max-lg:hidden",
            )}
          >
            <div className="flex w-full flex-col gap-3 p-2">
              {(["mainagent", "planner", "system", "worker"] as const).map((role) => {
                const items = grouped[role];
                if (!items.length) return null;
                const Meta = roleMeta[role];
                return (
                  <div key={role} className="flex flex-col gap-0.5">
                    <div className="flex items-center gap-1.5 px-2 py-1 text-xs font-medium text-muted-foreground">
                      <Meta.icon className="size-3.5" />
                      {Meta.label}
                      {role === "mainagent" && (
                        <button
                          type="button"
                          onClick={() => setConfirmNewMain(true)}
                          disabled={creatingMain}
                          title="新建主 Agent 会话（清空上下文，任务状态保留）"
                          aria-label="新建主 Agent 会话"
                          className="ml-auto flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-muted-foreground hover:bg-accent/60 hover:text-foreground disabled:opacity-50"
                        >
                          {creatingMain ? (
                            <Loader2Icon className="size-3.5 animate-spin" />
                          ) : (
                            <PlusIcon className="size-3.5" />
                          )}
                          新建
                        </button>
                      )}
                    </div>
                    {items.map((s) => {
                      const meta = s.role === "worker" ? sessionMeta.get(s.id) : undefined;
                      return (
                        <SessionItem
                          key={s.id}
                          s={s}
                          active={s.id === activeId}
                          displayTitle={meta?.title ?? s.title}
                          hasPending={hasPendingForSession(s)}
                          unread={store[keyForSession(s)]?.unread}
                          onClick={() => {
                            approvalFocus.close();
                            setActiveId(s.id);
                            setListOpen(false); // 手机端选完即收起，把高度还给会话记录
                            setWorkerMessage("");
                            setWorkerMessageRequestId("");
                          }}
                          controlling={controllingIntent === s.intent_id}
                          deleted={meta?.deleted}
                          onCancel={() => {
                            setCancelIntent(s);
                          }}
                        />
                      );
                    })}
                    {role === "worker" && intentsHasMore && (
                      <button
                        type="button"
                        onClick={loadOlderIntents}
                        disabled={loadingOlderIntents}
                        className="mt-0.5 flex items-center justify-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-accent/50"
                      >
                        {loadingOlderIntents ? (
                          <Loader2Icon className="size-3.5 animate-spin" />
                        ) : (
                          <RotateCwIcon className="size-3.5" />
                        )}
                        加载更早的 Worker
                      </button>
                    )}
                  </div>
                );
              })}
              {mainLoaded && !workerSessions.length && (
                <div className="px-2 py-1 text-xs text-muted-foreground">暂无运行中的 Worker 会话。</div>
              )}
            </div>
          </ScrollArea>
        </div>

        {/* Right: transcript */}
        <SideQuestionWorkspace
          side={side}
          label={active.role === "worker" ? `Worker #${active.intent_id} · ${activeDisplayTitle}` : activeDisplayTitle}
        >
          <div className="flex min-h-0 flex-1 min-w-0 flex-col overflow-hidden rounded-lg border bg-card">
            <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 border-b px-3 py-2 sm:px-4 sm:py-2.5">
              {(() => {
                const isWorker = active.role === "worker";
                const meta = isWorker ? sessionMeta.get(active.id) : undefined;
                // Worker: the intent moved into the transcript as a message, so the
                // header shows a stable generic label (intent JSON stays on hover).
                const title = isWorker ? "Worker 执行会话" : active.title;
                const titleEl = <span className="min-w-0 truncate text-sm font-medium">{title}</span>;
                return meta?.json ? (
                  <Tooltip>
                    <TooltipTrigger asChild>{titleEl}</TooltipTrigger>
                    <TooltipContent side="bottom" align="start" className="max-h-80 max-w-sm overflow-auto p-0">
                      <pre className="p-2 text-[10px] leading-relaxed">{JSON.stringify(meta.json, null, 2)}</pre>
                    </TooltipContent>
                  </Tooltip>
                ) : (
                  titleEl
                );
              })()}
              <SideQuestionButton side={side} />
              {activeResolution && (
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Badge
                      variant="outline"
                      className="max-w-28 shrink-0 font-normal"
                      aria-label={
                        activeResolution.available ? `当前配置：${resolutionLabel(activeResolution)}` : "模型不可用"
                      }
                    >
                      <span className="truncate">
                        {activeResolution.available ? resolutionLabel(activeResolution) : "模型不可用"}
                      </span>
                    </Badge>
                  </TooltipTrigger>
                  <TooltipContent side="bottom" className="max-w-xs [overflow-wrap:anywhere]">
                    {activeResolution.available
                      ? [resolutionLabel(activeResolution), activeResolution.model].filter(Boolean).join(" / ")
                      : activeResolution.reason || "没有可用的 LLM 配置"}
                  </TooltipContent>
                </Tooltip>
              )}
              {activeAssets && activeAssets.length > 0 && <WorkerAssetBadge assets={activeAssets} />}
              {active.inherited && active.source_task_id && (
                <Badge variant="outline">来源任务 #{active.source_task_id} · 只读历史</Badge>
              )}
              {active.live && (
                <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-600 dark:text-blue-400">
                  <span className="size-1 animate-pulse rounded-full bg-blue-500" />
                  实时
                </span>
              )}
              {activeState?.hasMore && (
                <span className="text-[10px] text-muted-foreground" title="向上滚动加载更早历史">
                  ↑ 更早历史
                </span>
              )}
              <div className="ml-auto flex min-w-0 max-w-full items-center justify-end gap-x-3 gap-y-1 text-xs text-muted-foreground max-sm:w-full max-sm:flex-wrap">
                {tokenTotal.any && (
                  <Tooltip>
                    {/* 手机端用短标签（入/缓/出）：长标签会把这一行撑成两行，进一步压缩记录区。 */}
                    <TooltipTrigger asChild>
                      <span className="inline-flex min-w-0 items-center">
                        <TokenMetrics
                          input={tokenTotal.i}
                          cache={tokenTotal.cr}
                          output={tokenTotal.o}
                          labels="short"
                          className="sm:hidden"
                        />
                        <TokenMetrics
                          input={tokenTotal.i}
                          cache={tokenTotal.cr}
                          output={tokenTotal.o}
                          labels="long"
                          className="max-sm:hidden"
                        />
                      </span>
                    </TooltipTrigger>
                    <TooltipContent>
                      输入 {activeTokens.input_tokens.toLocaleString()} · 输出{" "}
                      {activeTokens.output_tokens.toLocaleString()} · 缓存读取{" "}
                      {activeTokens.cache_read_tokens.toLocaleString()} · 缓存写入{" "}
                      {activeTokens.cache_write_tokens.toLocaleString()}
                    </TooltipContent>
                  </Tooltip>
                )}
                {runDuration != null && (
                  <span className="inline-flex items-center gap-1" title="运行时长（首步 → 末步）">
                    <ClockIcon className="size-3" />
                    {fmtDuration(runDuration)}
                  </span>
                )}
                {isMain && <span>可交互</span>}
              </div>
            </div>
            {(() => {
              const dm = active.role === "worker" ? sessionMeta.get(active.id) : undefined;
              if (!dm?.deleted) return null;
              return (
                <div className="flex items-start gap-2 border-b border-destructive/30 bg-destructive/5 px-4 py-2.5 text-xs">
                  <Trash2Icon className="mt-0.5 size-3.5 shrink-0 text-destructive" />
                  <div className="min-w-0">
                    <span className="font-medium text-destructive">此意图已被用户删除</span>
                    <span className="text-muted-foreground">
                      （已停止执行，规划者已收到通知；意图与产出保留，可在下方查看历史）
                    </span>
                    {dm.deleteReason && (
                      <p className="mt-1 break-words text-foreground">
                        <span className="text-muted-foreground">删除原因：</span>
                        {dm.deleteReason}
                      </p>
                    )}
                  </div>
                </div>
              );
            })()}
            <ApprovalExecutionFocus
              focus={{
                ...approvalFocus,
                close: () => {
                  setActiveId(activeId);
                  approvalFocus.close();
                },
              }}
              history={focusHistory}
            />
            {/* Force Radix's internal viewport wrapper (display:table, sizes to content)
            to block so wide/unbreakable steps (long commands, code, URLs) can't blow
            out the width and defeat the truncation below — the transcript wraps to
            the panel instead of overflowing horizontally. */}
            <ScrollArea type="auto" className="min-h-0 min-w-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!">
              <div className="min-w-0 max-w-full p-4" ref={contentRef}>
                {activeState?.loadingMore && (
                  <div className="flex items-center justify-center gap-2 pb-2 text-xs text-muted-foreground">
                    <Loader2Icon className="size-3.5 animate-spin" />
                    加载更早历史…
                  </div>
                )}
                {showLoader ? (
                  <div className="flex items-center gap-2 pl-9 text-xs text-muted-foreground">
                    <Loader2Icon className="size-3.5 animate-spin" />
                    加载活动流…
                  </div>
                ) : activeState?.error ? (
                  <div className="flex items-center gap-2 pl-9 text-xs text-red-500">
                    <CircleXIcon className="size-3.5" />
                    加载失败：{activeState.error}
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 px-2 text-xs"
                      onClick={() => loadSession(activeKey)}
                    >
                      重试
                    </Button>
                  </div>
                ) : activity.length ? (
                  <Transcript
                    activity={activity}
                    live={active.live}
                    taskId={taskId}
                    chat={isMain}
                    focusedSeq={focusHistory.ready ? approvalFocus.state?.source?.seq : undefined}
                  />
                ) : (
                  <div className="pl-9 text-xs text-muted-foreground">
                    {isMain ? "还没有对话。在下方给主 Agent 发消息，引导探索方向或介入流程。" : "暂无活动记录。"}
                  </div>
                )}
              </div>
            </ScrollArea>
            {isMain ? (
              <div className="border-t p-3">
                {attachments.length > 0 && (
                  <div className="mb-2 flex flex-wrap gap-1.5">
                    {attachments.map((a) => (
                      <div
                        key={a.path}
                        className="flex items-center gap-1.5 rounded-md border bg-muted/50 px-2 py-1 text-xs"
                        title={a.path}
                      >
                        <PaperclipIcon className="size-3 shrink-0 text-primary" />
                        <span className="max-w-[160px] truncate">{a.name}</span>
                        <span className="text-muted-foreground">{fmtBytes(a.size)}</span>
                        <button
                          type="button"
                          className="ml-0.5 text-muted-foreground hover:text-foreground"
                          onClick={() => setAttachments((p) => p.filter((x) => x.path !== a.path))}
                          title="移除"
                        >
                          <XIcon className="size-3" />
                        </button>
                      </div>
                    ))}
                  </div>
                )}
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  className="hidden"
                  onChange={(e) => void pickFiles(e.target.files)}
                />
                <InputGroup className="min-h-9 has-disabled:opacity-100">
                  <MentionTextarea
                    inputGroup
                    rows={1}
                    aria-label="给主 Agent 发消息"
                    placeholder={
                      mainBusy ? "主 Agent 正在运行，可输入 /btw 提问…" : "给主 Agent 发消息，@ 引用漏洞、资产等…"
                    }
                    value={input}
                    disabled={sending}
                    onValueChange={setInput}
                    onKeyDown={(e) => {
                      if (!shouldSubmitOnKey(e, sendMode)) return;
                      e.preventDefault();
                      send();
                    }}
                    className="max-h-36 min-h-9 overflow-y-auto"
                  />
                  <InputGroupAddon align="block-end">
                    <InputGroupButton
                      size="icon-xs"
                      variant="ghost"
                      onClick={() => fileInputRef.current?.click()}
                      disabled={mainBusy || uploading}
                      title="上传文件"
                      aria-label="上传文件"
                    >
                      {uploading ? <Loader2Icon className="animate-spin" /> : <PaperclipIcon />}
                    </InputGroupButton>
                    {mainBusy && isBtwCommand(input) && (
                      <InputGroupButton size="icon-xs" onClick={send} aria-label="发送旁路问题">
                        <ArrowUpIcon />
                      </InputGroupButton>
                    )}
                    {mainBusy ? (
                      <InputGroupButton
                        className="ml-auto"
                        size="icon-xs"
                        variant="destructive"
                        onClick={stop}
                        disabled={stopping}
                        title="停止当前执行"
                        aria-label="停止当前执行"
                      >
                        {stopping ? <Loader2Icon className="animate-spin" /> : <SquareIcon />}
                      </InputGroupButton>
                    ) : (
                      <InputGroupButton
                        className="ml-auto"
                        size="icon-xs"
                        variant="default"
                        onClick={send}
                        disabled={(!input.trim() && attachments.length === 0) || sending}
                        title="发送消息"
                        aria-label="发送消息"
                      >
                        {sending ? <Loader2Icon className="animate-spin" /> : <ArrowUpIcon />}
                      </InputGroupButton>
                    )}
                  </InputGroupAddon>
                </InputGroup>
              </div>
            ) : active.role === "worker" &&
              !active.inherited &&
              (active.status === "running" || active.status === "paused") ? (
              <div className="border-t p-3">
                <InputGroup className="min-h-9 has-disabled:opacity-100">
                  <MentionTextarea
                    inputGroup
                    rows={1}
                    aria-label={`给 Worker #${active.intent_id} 发消息`}
                    placeholder={`给 Worker #${active.intent_id} 发消息，@ 引用记录，调整执行方向…`}
                    value={workerMessage}
                    onValueChange={(value) => {
                      setWorkerMessage(value);
                      setWorkerMessageRequestId("");
                    }}
                    onKeyDown={(event) => {
                      if (!shouldSubmitOnKey(event, sendMode)) return;
                      event.preventDefault();
                      sendWorkerChat();
                    }}
                    disabled={workerMessageSending}
                    aria-invalid={workerMessageCharCount(workerMessage) > MAX_WORKER_MESSAGE_CHARS}
                    className="max-h-36 min-h-9 overflow-y-auto"
                  />
                  <InputGroupAddon align="block-end">
                    <span
                      className={cn(
                        "px-1 text-[10px] tabular-nums text-muted-foreground",
                        workerMessageCharCount(workerMessage) > MAX_WORKER_MESSAGE_CHARS && "text-destructive",
                      )}
                    >
                      {workerMessageCharCount(workerMessage)}/{MAX_WORKER_MESSAGE_CHARS}
                    </span>
                    {active.status === "running" && isBtwCommand(workerMessage) && (
                      <InputGroupButton size="icon-xs" onClick={sendWorkerChat} aria-label="发送旁路问题">
                        <ArrowUpIcon />
                      </InputGroupButton>
                    )}
                    {active.status === "running" ? (
                      <InputGroupButton
                        className="ml-auto"
                        size="icon-xs"
                        variant="destructive"
                        onClick={() => void controlWorker(active, "pause")}
                        disabled={controllingIntent === active.intent_id}
                        title="暂停当前 Worker"
                        aria-label="暂停当前 Worker"
                      >
                        {controllingIntent === active.intent_id ? (
                          <Loader2Icon className="animate-spin" />
                        ) : (
                          <SquareIcon />
                        )}
                      </InputGroupButton>
                    ) : (
                      <>
                        <InputGroupButton
                          className="ml-auto"
                          size="xs"
                          variant="ghost"
                          onClick={() => void controlWorker(active, "resume")}
                          disabled={controllingIntent === active.intent_id || workerMessageSending}
                          title="不发消息，直接继续执行"
                          aria-label="直接继续执行"
                        >
                          {controllingIntent === active.intent_id ? (
                            <Loader2Icon className="animate-spin" />
                          ) : (
                            "直接继续"
                          )}
                        </InputGroupButton>
                        <InputGroupButton
                          size="icon-xs"
                          variant="default"
                          onClick={sendWorkerChat}
                          disabled={
                            workerMessageSending ||
                            !workerMessage.trim() ||
                            workerMessageCharCount(workerMessage) > MAX_WORKER_MESSAGE_CHARS
                          }
                          title="发送消息"
                          aria-label="发送消息"
                        >
                          {workerMessageSending ? <Spinner /> : <ArrowUpIcon />}
                        </InputGroupButton>
                      </>
                    )}
                  </InputGroupAddon>
                </InputGroup>
              </div>
            ) : (
              <div className="flex items-center border-t px-4 py-2">
                <TodoPopover
                  seq={latestTodoSeq}
                  fetchDetail={(seq) => api.activityDetail(seq, taskId).then((r) => r.detail ?? "")}
                />
              </div>
            )}
          </div>
        </SideQuestionWorkspace>
        <AlertDialog
          open={cancelIntent !== null}
          onOpenChange={(open) => {
            if (!open) {
              setCancelIntent(null);
              setCancelReason("");
              setDeleteMode("soft");
            }
          }}
        >
          <AlertDialogContent className="max-w-[min(32rem,calc(100vw-2rem))]">
            <AlertDialogHeader>
              <AlertDialogTitle>删除 Worker #{cancelIntent?.intent_id}？</AlertDialogTitle>
              <AlertDialogDescription className="break-words whitespace-normal">
                {deleteMode === "hard" ? (
                  <>
                    <strong>真删除</strong>会物理移除该意图，以及<strong>仅由它支撑</strong>
                    的下游节点（级联到叶子，避免留下孤立数据）；共享节点、目标和任务根事实会保留。
                    <strong>此操作不可恢复。</strong>规划者会收到删除通知并据此重新规划。
                  </>
                ) : (
                  <>
                    <strong>假删除</strong>会把该意图置为「已删除」并记录删除原因，意图节点、执行记录、
                    已登记的事实和漏洞<strong>都会保留</strong>。规划者会收到「该意图由用户删除 + 原因」并据此重新规划。
                  </>
                )}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <div className="grid gap-3 py-1">
              <div className="grid grid-cols-2 gap-2">
                <button
                  type="button"
                  onClick={() => setDeleteMode("soft")}
                  className={cn(
                    "rounded-md border px-3 py-2 text-left text-sm transition-colors",
                    deleteMode === "soft" ? "border-primary bg-primary/5" : "hover:bg-accent",
                  )}
                >
                  <div className="font-medium">假删除</div>
                  <div className="text-xs text-muted-foreground">保留数据，可追溯</div>
                </button>
                <button
                  type="button"
                  onClick={() => setDeleteMode("hard")}
                  className={cn(
                    "rounded-md border px-3 py-2 text-left text-sm transition-colors",
                    deleteMode === "hard" ? "border-destructive bg-destructive/5" : "hover:bg-accent",
                  )}
                >
                  <div className="font-medium">真删除</div>
                  <div className="text-xs text-muted-foreground">级联移除，不可恢复</div>
                </button>
              </div>
              <div className="grid gap-2">
                <label htmlFor="cancel-reason" className="text-sm font-medium">
                  删除原因（必填）
                </label>
                <Textarea
                  id="cancel-reason"
                  value={cancelReason}
                  onChange={(e) => setCancelReason(e.target.value)}
                  placeholder="说明为什么删除这条意图，例如：方向判断错误 / 目标已失效 / 与其他意图重复…"
                  rows={3}
                  autoFocus
                />
              </div>
            </div>
            <AlertDialogFooter>
              <AlertDialogCancel>返回</AlertDialogCancel>
              <AlertDialogAction
                variant="destructive"
                disabled={!cancelIntent || controllingIntent !== null || !cancelReason.trim()}
                onClick={() => cancelIntent && void controlWorker(cancelIntent, "cancel", cancelReason, deleteMode)}
              >
                {controllingIntent ? <Loader2Icon className="animate-spin" /> : <Trash2Icon />}
                {deleteMode === "hard" ? "彻底删除" : "确认删除"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>

        <AlertDialog open={confirmNewMain} onOpenChange={(open) => !open && setConfirmNewMain(false)}>
          <AlertDialogContent className="max-w-[min(32rem,calc(100vw-2rem))]">
            <AlertDialogHeader>
              <AlertDialogTitle>开启新会话？</AlertDialogTitle>
              <AlertDialogDescription className="break-words whitespace-normal">
                当前会话会被归档（可随时切回），主 Agent 将以干净的上下文继续。任务的图谱、资产、目标不受影响。
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel disabled={creatingMain}>取消</AlertDialogCancel>
              <AlertDialogAction disabled={creatingMain} onClick={() => void createMainSession()}>
                {creatingMain ? "开启中…" : "开启新会话"}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </TooltipProvider>
  );
}
