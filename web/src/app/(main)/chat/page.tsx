"use client";

import * as React from "react";

import { Bot, ListChecksIcon, Loader2Icon, PlusIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

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
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { retryRequest } from "@/lib/request-lifecycle";
import type { Agent, ChatAttachment, Conversation, LLMProfile } from "@/lib/types";

import {
  COLLAPSED_AGENTS_KEY,
  CONVERSATION_LIST_PAGE,
  conversationIsPinned,
  groupByAgent,
} from "./_components/chat-helpers";
import { ChatView, DraftChat } from "./_components/chat-messages";
import { AgentGroupHeader, ConversationItem } from "./_components/chat-sidebar";

// fmtBytes renders a human file size for attachment chips (mirrors transcript.tsx).

export default function ChatPage() {
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [convs, setConvs] = React.useState<Conversation[]>([]);
  const [agentFilter, setAgentFilter] = React.useState<string | null>(null);
  const [selectedId, setSelectedId] = React.useState<number | null>(null);
  const [sourceRequested, setSourceRequested] = React.useState(false);
  const [convsLoaded, setConvsLoaded] = React.useState(false);
  const selectConversation = React.useCallback(
    (id: number | null) => {
      if (id !== selectedId) {
        const url = new URL(window.location.href);
        url.searchParams.delete("approval");
        setSourceRequested(false);
        window.history.replaceState(null, "", url);
      }
      setSelectedId(id);
    },
    [selectedId],
  );

  const [renamingId, setRenamingId] = React.useState<number | null>(null);
  const [renameText, setRenameText] = React.useState("");
  const [selectedConversationIds, setSelectedConversationIds] = React.useState<Set<number>>(() => new Set());
  // selectionMode gates the multi-select UI: off by default (clean list, no
  // checkboxes); the header "多选" button turns it on, "完成" turns it off and
  // clears the selection.
  const [selectionMode, setSelectionMode] = React.useState(false);
  const [bulkDeleteOpen, setBulkDeleteOpen] = React.useState(false);
  const [bulkDeleting, setBulkDeleting] = React.useState(false);
  // 服务端分页：convs 是已加载的窗口（首屏 CONVERSATION_LIST_PAGE，按「加载更多」向后扩），
  // convTotal 是当前 agent 过滤下的总条数，agentCounts 是全量分 agent 计数。
  const [convTotal, setConvTotal] = React.useState(0);
  const [agentCounts, setAgentCounts] = React.useState<Record<string, number>>({});
  const [loadingMore, setLoadingMore] = React.useState(false);
  // 深链接 ?c=<id> 指向的对话可能不在已加载窗口里：单独取一次，保证能打开。
  const [selectionFallback, setSelectionFallback] = React.useState<Conversation | null>(null);
  const [fallbackError, setFallbackError] = React.useState<string | null>(null);
  const [fallbackRetry, setFallbackRetry] = React.useState(0);
  // Collapsed agent groups. Hydrated from localStorage after mount (not a lazy
  // useState init) so the server and client render the same first pass.
  const [collapsedAgents, setCollapsedAgents] = React.useState<Set<string>>(() => new Set());
  // Pending text + uploaded attachments handed off from a draft that created a
  // conversation via the paperclip, keyed by the new conversation id (consumed once
  // by ChatView on mount; ids never repeat, so leftover entries are harmless).
  const [pendingByConv, setPendingByConv] = React.useState<
    Record<number, { input?: string; attachments?: ChatAttachment[] }>
  >({});

  const conversationListSeq = React.useRef(0);
  // 已加载窗口大小：轮询只刷新 [0, convWindow) 这一段（顺序由服务端定），
  // 不做「拉全量再截断」。「加载更多」扩大窗口后由同一个回调重取。
  //
  // 上限 500：再加就退化成"全量轮询"，而侧栏抽屉本就不适合无限加载。
  const CONVERSATION_WINDOW_MAX = 500;
  const [convWindow, setConvWindow] = React.useState(CONVERSATION_LIST_PAGE);
  const reloadConvs = React.useCallback(async (rethrow = false) => {
    const seq = ++conversationListSeq.current;
    try {
      const page = await api.conversationPage({
        offset: 0,
        limit: convWindow,
        agent: agentFilter ?? undefined,
      });
      if (seq !== conversationListSeq.current) return;
      setConvs(page.items);
      setConvTotal(page.total);
      setAgentCounts(page.agentCounts);
      setConvsLoaded(true);
    } catch (error) {
      // Preserve the selected transcript and list on a transient poll failure.
      // The polling caller rethrows so usePolling backs off; UI callers stay quiet.
      if (rethrow) throw error;
    } finally {
      if (seq === conversationListSeq.current) setLoadingMore(false);
    }
  }, [agentFilter, convWindow]);

  // 「加载更多」只扩大窗口；reloadConvs 因 convWindow 变化由轮询钩子立即重取，
  // 拿到的是服务端排好序的完整前缀，不需要在客户端拼接两段。
  const loadMoreConversations = React.useCallback(() => {
    if (loadingMore || convWindow >= CONVERSATION_WINDOW_MAX) return;
    setLoadingMore(true);
    setConvWindow((current) => Math.min(current + CONVERSATION_LIST_PAGE, CONVERSATION_WINDOW_MAX));
  }, [convWindow, loadingMore]);
  React.useEffect(() => {
    api
      .agents()
      .then(setAgents)
      .catch(() => {
        /* Agent metadata is optional for rendering an existing conversation. */
      });
    api
      .llmProfiles()
      .then(setProfiles)
      .catch(() => {
        /* The conversation remains usable without profile labels. */
      });
  }, []);

  // One list poll supplies runtime state for all sidebar rows, including the
  // unselected ones. usePolling 保证慢请求不叠加，并在后台标签页暂停。
  usePolling(() => reloadConvs(true), 2000, [reloadConvs]);
  React.useEffect(
    () => () => {
      conversationListSeq.current++;
    },
    [reloadConvs],
  );

  React.useEffect(() => {
    setSelectedConversationIds((current) => {
      if (current.size === 0) return current;
      const live = new Set(convs.map((conversation) => conversation.id));
      const next = new Set([...current].filter((id) => live.has(id)));
      return next.size === current.size ? current : next;
    });
  }, [convs]);

  React.useEffect(() => {
    const raw = getLocalStorageValue(COLLAPSED_AGENTS_KEY);
    if (!raw) return;
    try {
      const keys = JSON.parse(raw);
      if (Array.isArray(keys))
        setCollapsedAgents(new Set(keys.filter((key): key is string => typeof key === "string")));
    } catch {
      // Corrupted entry → start with everything expanded.
    }
  }, []);

  const toggleAgentCollapsed = React.useCallback((key: string) => {
    setCollapsedAgents((current) => {
      const next = new Set(current);
      if (!next.delete(key)) next.add(key);
      setLocalStorageValue(COLLAPSED_AGENTS_KEY, JSON.stringify([...next]));
      return next;
    });
  }, []);

  // Restore the open conversation from the URL (?c=<id>) on mount, so a refresh
  // returns to the same thread instead of the empty draft view. Runs after
  // hydration (not a lazy useState init) to avoid a server/client mismatch.
  React.useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    setSourceRequested(params.has("approval"));
    const c = params.get("c");
    const id = c ? Number(c) : NaN;
    if (Number.isFinite(id)) setSelectedId(id);
  }, []);
  // Mirror the current selection into the URL (replaceState → no history spam). A
  // selectedId with no matching conversation (e.g. a stale ?c=, or a just-created
  // one before reloadConvs lands) simply renders the draft view — harmless — so we
  // deliberately do NOT auto-clear it here (that raced new-conversation creation).
  React.useEffect(() => {
    const url = new URL(window.location.href);
    if (selectedId != null) url.searchParams.set("c", String(selectedId));
    else url.searchParams.delete("c");
    window.history.replaceState(null, "", url);
  }, [selectedId]);

  const selected = React.useMemo(
    () => convs.find((c) => c.id === selectedId) ?? (selectionFallback?.id === selectedId ? selectionFallback : null),
    [convs, selectedId, selectionFallback],
  );
  // Depend on membership, not the polling array: a refresh must not cancel an
  // in-flight request for the same out-of-window conversation.
  const selectedInWindow = convs.some((c) => c.id === selectedId);
  const fallbackReady = selectionFallback?.id === selectedId;
  const fallbackPending = selectedId != null && !selected && !fallbackReady;
  // biome-ignore lint/correctness/useExhaustiveDependencies: fallbackRetry is an explicit manual retry trigger; the effect reads only the stable request identity.
  React.useEffect(() => {
    if (selectedId == null || selectedInWindow || fallbackReady) {
      setFallbackError(null);
      return;
    }
    const controller = new AbortController();
    setFallbackError(null);
    void retryRequest((signal) => api.conversation(selectedId, signal), controller.signal)
      .then((c) => {
        if (!controller.signal.aborted) setSelectionFallback(c);
      })
      .catch((error) => {
        if (!controller.signal.aborted) setFallbackError(error instanceof Error ? error.message : "加载对话失败");
      });
    return () => controller.abort();
  }, [selectedId, selectedInWindow, fallbackReady, fallbackRetry]);
  const agentByKey = React.useMemo(() => new Map(agents.map((agent) => [agent.key, agent])), [agents]);
  // 服务端已按 agent 过滤，这里就是已加载的窗口。
  const filteredConversations = convs;
  const visibleConversations = convs;
  // Pinned rows stay a flat block above the groups (server order = pinned_at DESC);
  // everything else is bucketed per agent, most-recently-active agent first.
  const pinnedConversations = React.useMemo(
    () => visibleConversations.filter(conversationIsPinned),
    [visibleConversations],
  );
  const agentGroups = React.useMemo(
    () =>
      groupByAgent(
        visibleConversations.filter((c) => !conversationIsPinned(c)),
        agentByKey,
      ),
    [visibleConversations, agentByKey],
  );
  // conversation agents: custom agents + conversational built-ins (role=assistant,
  // e.g. Auto / 渗透测试). The orchestration built-ins (goals/planner/mainagent/worker)
  // are task-specific and stay hidden from the chat page.
  const chatAgents = React.useMemo(() => agents.filter((a) => !a.builtin || a.role === "assistant"), [agents]);
  const agentFilterOptions = React.useMemo(() => {
    // 计数来自服务端全量 GROUP BY，不随已加载窗口变化（否则筛选标签会随着翻页变小）。
    const keys = new Set([...chatAgents.map((agent) => agent.key), ...Object.keys(agentCounts)]);
    if (agentFilter !== null) keys.add(agentFilter);
    return [...keys]
      .map((key) => ({ key, name: agentByKey.get(key)?.name || key, count: agentCounts[key] ?? 0 }))
      .sort((a, b) => a.name.localeCompare(b.name, "zh-CN"));
  }, [agentCounts, chatAgents, agentByKey, agentFilter]);
  const globalConversationCount = React.useMemo(
    () => Object.values(agentCounts).reduce((sum, n) => sum + n, 0),
    [agentCounts],
  );
  const conversationCountLabel =
    agentFilter === null
      ? `共 ${convTotal} 个`
      : `${convTotal} / ${globalConversationCount || convTotal} 个`;

  function changeAgentFilter(key: string | null) {
    setAgentFilter(key);
    setConvWindow(CONVERSATION_LIST_PAGE);
    setConvs([]);
    setSelectedConversationIds(new Set());
    setBulkDeleteOpen(false);
    setRenamingId(null);
  }

  const selectedConversationCount = selectedConversationIds.size;
  const allConversationsSelected =
    filteredConversations.length > 0 && selectedConversationCount === filteredConversations.length;
  const someConversationsSelected = selectedConversationCount > 0 && !allConversationsSelected;
  let conversationHeaderChecked: boolean | "indeterminate" = false;
  if (allConversationsSelected) conversationHeaderChecked = true;
  else if (someConversationsSelected) conversationHeaderChecked = "indeterminate";

  const toggleConversationSelected = React.useCallback((id: number, checked: boolean) => {
    setSelectedConversationIds((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  function toggleAllConversations(checked: boolean) {
    setSelectedConversationIds(
      checked ? new Set(filteredConversations.map((conversation) => conversation.id)) : new Set(),
    );
  }

  function exitSelectionMode() {
    setSelectionMode(false);
    setSelectedConversationIds(new Set());
  }

  const del = React.useCallback(
    async (id: number) => {
      try {
        await api.deleteConversation(id);
        setSelectedId((current) => (current === id ? null : current));
        setSelectedConversationIds((current) => {
          if (!current.has(id)) return current;
          const next = new Set(current);
          next.delete(id);
          return next;
        });
        void reloadConvs();
      } catch (e) {
        toast.error("删除失败：" + (e as Error).message);
      }
    },
    [reloadConvs],
  );

  async function deleteSelectedConversations() {
    const ids = [...selectedConversationIds];
    if (ids.length === 0 || bulkDeleting) return;
    setBulkDeleting(true);
    const deleted = new Set<number>();
    const failed: { id: number; error: string }[] = [];
    try {
      for (let offset = 0; offset < ids.length; offset += 100) {
        const result = await api.deleteConversations(ids.slice(offset, offset + 100));
        for (const item of result.items) {
          if (item.ok) deleted.add(item.id);
          else failed.push({ id: item.id, error: item.error ?? "对话不存在" });
        }
      }
      if (deleted.has(selectedId ?? -1)) selectConversation(null);
      setSelectedConversationIds((current) => {
        const next = new Set(current);
        for (const id of deleted) next.delete(id);
        return next;
      });
      if (deleted.size > 0) toast.success(`已删除 ${deleted.size} 个对话`);
      if (failed.length > 0) {
        const details = failed
          .slice(0, 3)
          .map((item) => `#${item.id}（${item.error}）`)
          .join("；");
        toast.error(`${failed.length} 个对话删除失败：${details}${failed.length > 3 ? " 等" : ""}`);
      }
      setBulkDeleteOpen(false);
      // Fully successful → return to the clean list; keep selection mode on if
      // some failed so the user can retry the remaining ones.
      if (failed.length === 0) setSelectionMode(false);
      void reloadConvs();
    } catch (error) {
      toast.error(`批量删除失败：${(error as Error).message}`);
      void reloadConvs();
    } finally {
      setBulkDeleting(false);
    }
  }

  const togglePinned = React.useCallback(
    async (conversation: Conversation) => {
      const pinned = conversationIsPinned(conversation);
      try {
        await api.pinConversation(conversation.id, !pinned);
        void reloadConvs();
      } catch (e) {
        toast.error(`${pinned ? "取消置顶" : "置顶"}失败：${(e as Error).message}`);
      }
    },
    [reloadConvs],
  );

  const startRename = React.useCallback((c: Conversation) => {
    setRenamingId(c.id);
    setRenameText(c.title || "");
  }, []);
  const commitRename = React.useCallback(
    async (id: number, value: string) => {
      const title = value.trim();
      setRenamingId(null);
      if (!title) return;
      try {
        await api.renameConversation(id, title);
        void reloadConvs();
      } catch (e) {
        toast.error("重命名失败：" + (e as Error).message);
      }
    },
    [reloadConvs],
  );
  const cancelRename = React.useCallback(() => setRenamingId(null), []);

  return (
    <div
      data-content-padding="false"
      className="flex h-[calc(100svh-3rem)] min-w-0 flex-col overflow-hidden p-3 sm:p-4 md:h-[calc(100svh-4rem)] md:p-6"
    >
      <div className="grid min-h-0 min-w-0 flex-1 grid-cols-1 grid-rows-[minmax(10rem,15rem)_minmax(0,1fr)] gap-3 md:grid-cols-[18rem_minmax(0,1fr)] md:grid-rows-[minmax(0,1fr)] md:gap-4">
        {/* left: conversation list */}
        <div className="bg-card flex flex-col overflow-hidden rounded-lg border">
          <div className="flex flex-col gap-2 border-b p-2">
            <Button size="sm" className="w-full" onClick={() => selectConversation(null)}>
              <PlusIcon /> 新建对话
            </Button>
            <Select
              value={agentFilter === null ? "all" : `agent:${agentFilter}`}
              onValueChange={(value) => changeAgentFilter(value === "all" ? null : value.slice(6))}
              disabled={bulkDeleting}
            >
              <SelectTrigger size="sm" className="w-full min-w-0" aria-label="按 Agent 筛选对话">
                <Bot />
                <SelectValue placeholder="全部 Agent" />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="all">全部 Agent</SelectItem>
                  {agentFilterOptions.map((agent) => (
                    <SelectItem key={agent.key} value={`agent:${agent.key}`}>
                      {agent.name}（{agent.count}）
                    </SelectItem>
                  ))}
                </SelectGroup>
              </SelectContent>
            </Select>
            {convs.length > 0 &&
              (selectionMode ? (
                <div className="flex items-center gap-2 px-1">
                  <Checkbox
                    checked={conversationHeaderChecked}
                    onCheckedChange={(checked) => toggleAllConversations(checked === true)}
                    aria-label="选择当前筛选的全部对话"
                    disabled={filteredConversations.length === 0 || bulkDeleting}
                  />
                  <span className="text-muted-foreground min-w-0 flex-1 text-xs tabular-nums">
                    {selectedConversationCount > 0 ? `已选 ${selectedConversationCount} 个` : conversationCountLabel}
                  </span>
                  {selectedConversationCount > 0 && (
                    <Button
                      size="sm"
                      variant="destructive"
                      disabled={bulkDeleting}
                      onClick={() => setBulkDeleteOpen(true)}
                    >
                      <Trash2Icon data-icon="inline-start" />
                      删除
                    </Button>
                  )}
                  <Button size="sm" variant="ghost" onClick={exitSelectionMode}>
                    完成
                  </Button>
                </div>
              ) : (
                <div className="flex items-center gap-2 px-1">
                  <span className="text-muted-foreground min-w-0 flex-1 text-xs tabular-nums">
                    {conversationCountLabel}
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-muted-foreground"
                    onClick={() => setSelectionMode(true)}
                    disabled={filteredConversations.length === 0}
                  >
                    <ListChecksIcon data-icon="inline-start" />
                    多选
                  </Button>
                </div>
              ))}
          </div>
          <ScrollArea
            key={agentFilter === null ? "all" : `agent:${agentFilter}`}
            type="auto"
            className="min-h-0 min-w-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!"
          >
            <div className="flex min-w-0 flex-col gap-0.5 p-2">
              {filteredConversations.length === 0 && (
                <p className="text-muted-foreground px-2 py-6 text-center text-xs">
                  {agentFilter === null ? "暂无对话" : "该 Agent 暂无对话"}
                </p>
              )}
              {pinnedConversations.map((c) => (
                <ConversationItem
                  key={c.id}
                  conv={c}
                  agent={agentByKey.get(c.agent_key)}
                  active={selectedId === c.id}
                  renaming={renamingId === c.id}
                  renameText={renamingId === c.id ? renameText : ""}
                  onSelect={selectConversation}
                  onStartRename={startRename}
                  onRenameText={setRenameText}
                  onCommitRename={commitRename}
                  onCancelRename={cancelRename}
                  onTogglePinned={togglePinned}
                  onDelete={del}
                  selectionMode={selectionMode}
                  selectedForDelete={selectedConversationIds.has(c.id)}
                  onSelectedForDeleteChange={toggleConversationSelected}
                />
              ))}
              {agentGroups.map((group) => {
                const collapsed = collapsedAgents.has(group.key);
                return (
                  <React.Fragment key={group.key}>
                    <AgentGroupHeader
                      group={group}
                      collapsed={collapsed}
                      hasActive={group.conversations.some((c) => c.id === selectedId)}
                      onToggle={toggleAgentCollapsed}
                    />
                    {!collapsed &&
                      group.conversations.map((c) => (
                        <ConversationItem
                          key={c.id}
                          conv={c}
                          agent={agentByKey.get(c.agent_key)}
                          showAgent={false}
                          active={selectedId === c.id}
                          renaming={renamingId === c.id}
                          renameText={renamingId === c.id ? renameText : ""}
                          onSelect={selectConversation}
                          onStartRename={startRename}
                          onRenameText={setRenameText}
                          onCommitRename={commitRename}
                          onCancelRename={cancelRename}
                          onTogglePinned={togglePinned}
                          onDelete={del}
                          selectionMode={selectionMode}
                          selectedForDelete={selectedConversationIds.has(c.id)}
                          onSelectedForDeleteChange={toggleConversationSelected}
                        />
                      ))}
                  </React.Fragment>
                );
              })}
              {convs.length < convTotal && convWindow < CONVERSATION_WINDOW_MAX && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="mt-1 w-full"
                  disabled={loadingMore}
                  onClick={loadMoreConversations}
                >
                  {loadingMore ? "加载中…" : `加载更多（已载 ${convs.length} / ${convTotal}）`}
                </Button>
              )}
            </div>
          </ScrollArea>
        </div>

        {/* right: chat view */}
        <div className="bg-card flex min-w-0 flex-col overflow-hidden rounded-lg border">
          {fallbackPending || fallbackError ? (
            <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 p-4 text-center">
              <p role={fallbackError ? "alert" : "status"} className="text-sm text-muted-foreground">
                {fallbackError ?? "正在加载对话…"}
              </p>
              {fallbackError && (
                <Button
                  variant="outline"
                  onClick={() => {
                    setFallbackError(null);
                    setFallbackRetry((n) => n + 1);
                  }}
                >
                  重试
                </Button>
              )}
            </div>
          ) : selected ? (
            <ChatView
              key={selected.id}
              conv={selected}
              agents={agents}
              profiles={profiles}
              initial={pendingByConv[selected.id]}
              onTitleMaybeChanged={reloadConvs}
              onConvUpdated={reloadConvs}
            />
          ) : sourceRequested ? (
            <div role="status" className="p-6 text-sm text-muted-foreground">
              {convsLoaded ? "对话已被删除" : "正在加载对应对话…"}
            </div>
          ) : (
            <DraftChat
              agents={chatAgents}
              profiles={profiles}
              onStarted={(c, pending) => {
                if (agentFilter !== null && agentFilter !== c.agent_key) changeAgentFilter(null);
                // Insert the new conversation immediately so `selected` resolves to
                // it on this render (switching to ChatView right away, before the
                // async reloadConvs lands); reloadConvs then reconciles titles etc.
                if (pending) setPendingByConv((p) => ({ ...p, [c.id]: pending }));
                setConvs((prev) => {
                  if (prev.some((item) => item.id === c.id)) return prev;
                  const firstUnpinned = prev.findIndex((item) => !conversationIsPinned(item));
                  const insertAt = firstUnpinned < 0 ? prev.length : firstUnpinned;
                  return [...prev.slice(0, insertAt), c, ...prev.slice(insertAt)];
                });
                setSelectedId(c.id);
                void reloadConvs();
              }}
            />
          )}
        </div>
      </div>
      <AlertDialog open={bulkDeleteOpen} onOpenChange={setBulkDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除选中的 {selectedConversationCount} 个对话？</AlertDialogTitle>
            <AlertDialogDescription>对话消息和执行记录将一并删除，此操作不可撤销。</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={bulkDeleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={bulkDeleting || selectedConversationCount === 0}
              onClick={(event) => {
                event.preventDefault();
                void deleteSelectedConversations();
              }}
            >
              {bulkDeleting && <Loader2Icon data-icon="inline-start" className="animate-spin" />}
              {bulkDeleting ? "删除中" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
