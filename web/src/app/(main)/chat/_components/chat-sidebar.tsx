"use client";

import * as React from "react";

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
import { Checkbox } from "@/components/ui/checkbox";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Spinner } from "@/components/ui/spinner";
import { Agent, Conversation } from "@/lib/types";
import { cn } from "@/lib/utils";
import {
  Bot,
  ChevronRightIcon,
  MoreHorizontalIcon,
  PencilIcon,
  PinIcon,
  PinOffIcon,
  Trash2Icon,
} from "lucide-react";
import { AgentGroup, conversationIsPinned, HISTORY_PAGE, fmtBytes, fmtTokens } from "./chat-helpers";
import { ChatView, Composer, DraftChat, LLMProfileRow } from "./chat-messages";

// 对话页左侧会话列表：会话条目（含置顶/重命名/删除）与按 agent 分组的分组头。

export const ConversationItem = React.memo(function ConversationItem({
  conv,
  agent,
  showAgent = true,
  active,
  renaming,
  renameText,
  onSelect,
  onStartRename,
  onRenameText,
  onCommitRename,
  onCancelRename,
  onTogglePinned,
  onDelete,
  selectionMode,
  selectedForDelete,
  onSelectedForDeleteChange,
}: {
  conv: Conversation;
  agent?: Agent;
  showAgent?: boolean;
  active: boolean;
  renaming: boolean;
  renameText: string;
  onSelect: (id: number) => void;
  onStartRename: (conversation: Conversation) => void;
  onRenameText: (v: string) => void;
  onCommitRename: (id: number, title: string) => void;
  onCancelRename: () => void;
  onTogglePinned: (conversation: Conversation) => void;
  onDelete: (id: number) => void;
  selectionMode: boolean;
  selectedForDelete: boolean;
  onSelectedForDeleteChange: (id: number, checked: boolean) => void;
}) {
  const [deleteOpen, setDeleteOpen] = React.useState(false);
  const renameInputRef = React.useRef<HTMLInputElement>(null);
  const cancelRenameRef = React.useRef(false);
  const pinned = conversationIsPinned(conv);

  React.useEffect(() => {
    if (!renaming) return;
    cancelRenameRef.current = false;
    renameInputRef.current?.focus();
    renameInputRef.current?.select();
  }, [renaming]);

  return (
    <div
      className={cn(
        "group flex min-w-0 items-center gap-1 rounded-md pr-1 transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      {selectionMode && (
        <Checkbox
          checked={selectedForDelete}
          onCheckedChange={(checked) => onSelectedForDeleteChange(conv.id, checked === true)}
          aria-label={`选择对话「${conv.title || "新对话"}」`}
          className="ml-1 shrink-0"
        />
      )}
      {renaming ? (
        <input
          ref={renameInputRef}
          value={renameText}
          onChange={(e) => onRenameText(e.target.value)}
          onBlur={() => {
            if (!cancelRenameRef.current) onCommitRename(conv.id, renameText);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommitRename(conv.id, renameText);
            if (e.key === "Escape") {
              e.preventDefault();
              cancelRenameRef.current = true;
              onCancelRename();
            }
          }}
          className="border-input bg-background min-w-0 flex-1 rounded-md border px-2 py-1 text-sm"
        />
      ) : (
        <button
          type="button"
          onClick={() => onSelect(conv.id)}
          onDoubleClick={() => onStartRename(conv)}
          title="双击重命名"
          className="min-w-0 flex-1 rounded-md px-2 py-1.5 text-left"
        >
          <div className="flex min-w-0 items-center gap-1.5">
            {pinned && <PinIcon className="text-primary size-3 shrink-0" aria-label="已置顶" />}
            <div className="truncate text-sm">{conv.title || "新对话"}</div>
            {conv.running ? (
              <Badge variant="secondary" className="shrink-0 gap-1" title="Agent 正在运行">
                <Spinner className="size-3" aria-hidden="true" />
                运行中
              </Badge>
            ) : null}
          </div>
          <div className="text-muted-foreground flex min-w-0 items-center gap-1 text-[11px]">
            {showAgent && (
              <>
                <Bot className="size-3 shrink-0" />
                <span className="min-w-0 truncate">{agent?.name ?? conv.agent_key}</span>
                <span className="shrink-0">·</span>
              </>
            )}
            <span className="shrink-0">
              {new Date(conv.created_at).toLocaleDateString("zh-CN", {
                month: "numeric",
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
              })}
            </span>
            <span className="shrink-0 opacity-60">#{conv.id}</span>
          </div>
        </button>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground shrink-0"
            aria-label={`管理对话「${conv.title || "新对话"}」`}
          >
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuGroup>
            <DropdownMenuItem onSelect={() => onStartRename(conv)}>
              <PencilIcon />
              重命名
            </DropdownMenuItem>
            <DropdownMenuItem onSelect={() => onTogglePinned(conv)}>
              {pinned ? <PinOffIcon /> : <PinIcon />}
              {pinned ? "取消置顶" : "置顶"}
            </DropdownMenuItem>
          </DropdownMenuGroup>
          <DropdownMenuSeparator />
          <DropdownMenuGroup>
            <DropdownMenuItem variant="destructive" onSelect={() => setDeleteOpen(true)}>
              <Trash2Icon />
              删除
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除对话「{conv.title || "新对话"}」？</AlertDialogTitle>
            <AlertDialogDescription>此操作不可撤销。</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={() => onDelete(conv.id)}>删除</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
});

// AgentGroupHeader is the sticky, clickable divider above one agent's rows:
// collapse chevron, agent name, a running marker, and the row count.
export function AgentGroupHeader({
  group,
  collapsed,
  hasActive,
  onToggle,
}: {
  group: AgentGroup;
  collapsed: boolean;
  hasActive: boolean;
  onToggle: (key: string) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => onToggle(group.key)}
      aria-expanded={!collapsed}
      title={collapsed ? `展开「${group.name}」` : `收起「${group.name}」`}
      className={cn(
        "sticky top-0 z-10 flex min-w-0 items-center gap-1.5 rounded-md bg-card px-1.5 py-1 text-left font-medium text-[11px] transition-colors hover:bg-accent/50",
        collapsed && hasActive ? "text-foreground" : "text-muted-foreground",
      )}
    >
      <ChevronRightIcon className={cn("size-3 shrink-0 transition-transform", !collapsed && "rotate-90")} />
      <Bot className="size-3 shrink-0" />
      <span className="min-w-0 flex-1 truncate">{group.name}</span>
      {collapsed && hasActive && (
        <span className="size-1.5 shrink-0 rounded-full bg-primary" title="当前对话在此分组内" />
      )}
      {group.runningCount > 0 && (
        <Spinner className="size-3 shrink-0" aria-label={`${group.runningCount} 个对话运行中`} />
      )}
      <span className="shrink-0 tabular-nums opacity-60">{group.conversations.length}</span>
    </button>
  );
}
