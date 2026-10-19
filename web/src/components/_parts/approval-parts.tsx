"use client";

import * as React from "react";

import { originLabel, source } from "./approval-helpers";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { api } from "@/lib/api";
import { InterceptApprovalRow, InterceptAudit, InterceptReviewInput } from "@/lib/types";
import {
  BotIcon,
  CheckIcon,
  ChevronDownIcon,
  CopyIcon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";

// 审批页的展示基件：来源标签、状态徽标、匹配单元格、代码块、模型复核上下文与决策按钮。

export function ApprovalOrigin({ row, detail = false }: { row: InterceptApprovalRow; detail?: boolean }) {
  const [locating, setLocating] = React.useState(false);
  const label = detail && row.task_id ? `任务 ${row.task_id}` : originLabel(row);
  const query = new URLSearchParams({ approval: String(row.id) });
  let href: string | undefined;
  if (row.conversation_id) {
    query.set("c", String(row.conversation_id));
    href = `/chat?${query}`;
  } else if (row.task_id) {
    query.set("id", row.task_id);
    href = `/function/tasks/detail?${query}`;
  }
  return href ? (
    <a
      href={href}
      className="text-primary underline-offset-4 hover:underline"
      aria-label={`定位审批 #${row.id} 的来源：${label}`}
      aria-busy={locating}
      onClick={async (e) => {
        e.stopPropagation();
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        if (locating) return;
        setLocating(true);
        try {
          await api.interceptExecution(row.id, row.conversation_id ?? undefined);
          window.location.assign(href);
        } catch (error) {
          toast.error((error as Error).message || "无法定位对应执行");
          setLocating(false);
        }
      }}
    >
      {label}
    </a>
  ) : (
    <span>{label}</span>
  );
}

export function StatusBadge({ status }: { status: string }) {
  const labels: Record<string, string> = { pending: "待审批", allowed: "已允许", denied: "已拒绝", timeout: "已超时" };
  let variant: "default" | "destructive" | "secondary" | "outline" = "outline";
  if (status === "allowed") variant = "default";
  if (status === "denied") variant = "destructive";
  if (status === "pending") variant = "secondary";
  return <Badge variant={variant}>{labels[status] ?? status}</Badge>;
}

export function MatchCell({ row, showReason = true }: { row: InterceptApprovalRow; showReason?: boolean }) {
  const reason = row.reason?.replace(/^\[模型\]\s*/, "");
  return (
    <div className="flex min-w-0 flex-col gap-1">
      {source(row) === "model" ? (
        <Badge variant="outline">
          <BotIcon />
          模型判定
        </Badge>
      ) : (
        <span className="truncate">{row.rule_name || "规则未记录或已删除"}</span>
      )}
      {showReason ? (
        <p className="truncate text-muted-foreground text-xs" title={reason}>
          {reason || "未记录理由"}
        </p>
      ) : null}
    </div>
  );
}

export function CodeBlock({ label, text, truncated = false }: { label: string; text: string; truncated?: boolean }) {
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      toast.success("已复制");
    } catch {
      toast.error("复制失败，请手动选择内容复制");
    }
  }
  return (
    <section className="flex min-w-0 flex-col gap-2" aria-label={label}>
      <div className="flex items-center justify-between gap-2">
        <h3 className="font-medium text-muted-foreground text-xs">{label}</h3>
        {text ? (
          <Button variant="ghost" size="icon-xs" aria-label={`复制${label}`} onClick={() => void copy()}>
            <CopyIcon />
          </Button>
        ) : null}
      </div>
      <pre className="max-h-80 min-w-0 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-muted/60 p-3 font-mono text-xs leading-6 [overflow-wrap:anywhere]">
        {text || "未记录"}
      </pre>
      {truncated ? <p className="text-muted-foreground text-xs">内容已截断，以上为保存的片段。</p> : null}
    </section>
  );
}

export const contextLabels: Record<string, string> = {
  user: "用户消息",
  assistant: "Agent 消息",
  text: "Agent 消息",
  tool_use: "工具请求",
  tool_result: "工具输出",
};

export const actionLabels: Record<string, string> = { allow: "允许", ask: "转人工审批", deny: "拒绝" };
export const executionLabels: Record<InterceptAudit["execution_status"], string> = {
  not_started: "尚未执行",
  not_executed: "未执行",
  awaiting_result: "已允许，等待执行结果",
  succeeded: "执行成功",
  failed: "执行失败",
  unknown: "执行结果未知",
};

export function ModelReviewContext({ input }: { input: InterceptReviewInput }) {
  return (
    <section className="flex min-w-0 flex-col gap-4" aria-label="模型审查上下文">
      <div className="flex flex-col gap-2">
        <h3 className="font-medium text-sm">模型审查上下文</h3>
        <p className="text-muted-foreground text-xs">
          以下为本次实际发送给审查模型的输入快照。背景仅用于理解当前动作，裁决依据为审查策略。
          {input.version < 4 ? "此记录使用旧版输入，保留当时实际发送的内容。" : null}
        </p>
      </div>
      <CodeBlock
        label="当前待审查调用"
        text={JSON.stringify({ tool_name: input.tool_name, arguments: input.arguments }, null, 2)}
      />
      {input.version >= 2 ? (
        input.background ? (
          <div className="flex min-w-0 flex-col gap-2">
            <CodeBlock
              label={input.background.source === "user_message" ? "背景 · 用户消息" : "背景 · Worker 意图摘要（旧版）"}
              text={input.background.text}
              truncated={input.background.truncated}
            />
            <p className="text-muted-foreground text-xs">
              {input.background.source === "user_message"
                ? "取自当前用户消息。"
                : "这是旧版发送的 Worker 意图摘要；新版 Worker 审查不再发送此内容。"}
            </p>
          </div>
        ) : (
          <p className="text-muted-foreground text-xs">本次审查未附带背景消息。</p>
        )
      ) : (
        <>
          {input.turn_input ? (
            <CodeBlock label="当前轮输入（旧版）" text={input.turn_input} truncated={input.background_truncated} />
          ) : null}
          {input.task ? (
            <>
              <CodeBlock label="任务描述（旧版）" text={input.task.description} truncated={input.task.truncated} />
              <CodeBlock label="任务目标（旧版）" text={input.task.goal} truncated={input.task.truncated} />
              <CodeBlock label="任务操作约束（旧版）" text={JSON.stringify(input.task.constraints, null, 2)} />
            </>
          ) : null}
          {input.worker_intent ? (
            <CodeBlock label="Worker 意图（旧版）" text={input.worker_intent} truncated={input.background_truncated} />
          ) : null}
        </>
      )}
      {input.working_directory ? <CodeBlock label="工作目录" text={input.working_directory} /> : null}
      {input.version >= 3 ? (
        <p className="text-muted-foreground text-xs">本次审查未发送历史调用或执行结果。</p>
      ) : (
        <div className="flex min-w-0 flex-col gap-3">
          <h4 className="font-medium text-muted-foreground text-xs">当时提供给模型的历史调用（旧版）</h4>
          {input.history?.length ? (
            input.history.map((entry) => (
              <div key={entry.tool_use_id} className="flex min-w-0 flex-col gap-2 rounded-lg border p-3">
                <p className="break-words font-medium text-xs">
                  {entry.tool} · {entry.status === "succeeded" ? "成功" : "失败（可能有部分副作用）"}
                </p>
                <CodeBlock label="历史调用参数" text={entry.arguments_preview} truncated={entry.truncated} />
                <CodeBlock label="历史执行结果" text={entry.result} truncated={entry.truncated} />
              </div>
            ))
          ) : (
            <p className="text-muted-foreground text-xs">本次未提供可配对的历史工具执行记录。</p>
          )}
          {input.history_truncated ? (
            <p className="text-muted-foreground text-xs">历史为有限窗口，部分内容已截断。</p>
          ) : null}
        </div>
      )}

      <Collapsible>
        <CollapsibleTrigger asChild>
          <Button variant="outline" size="sm" className="self-start">
            <ChevronDownIcon data-icon="inline-start" />
            查看完整模型审查输入 JSON
          </Button>
        </CollapsibleTrigger>
        <CollapsibleContent className="pt-3">
          <CodeBlock label="模型审查输入" text={JSON.stringify(input, null, 2)} />
        </CollapsibleContent>
      </Collapsible>
    </section>
  );
}

export type Decide = (id: number, decision: "allowed" | "denied", editedInput?: unknown) => Promise<void>;

// NarrowEditor（P15 改参放行）：人可以对参数做【更保守】的修改后再放行。只允许收窄
// ——删键、缩短字符串、下调数值、true→false、数组变短、目标只减不增；服务端复核，
// 越界会被拒。默认填原参数，人改哪算哪。
export function NarrowEditor({
  row,
  busy,
  decide,
  onClose,
}: {
  row: InterceptApprovalRow;
  busy: boolean;
  decide: Decide;
  onClose: () => void;
}) {
  const initial = React.useMemo(() => JSON.stringify(row.tool_input ?? {}, null, 2), [row.tool_input]);
  const [text, setText] = React.useState(initial);
  const submit = () => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(text);
    } catch (e) {
      toast.error(`改后的参数不是合法 JSON：${(e as Error).message}`);
      return;
    }
    void decide(row.id, "allowed", parsed).then(onClose);
  };
  return (
    <div className="flex flex-col gap-2 rounded-lg border bg-muted/30 p-3">
      <div className="text-muted-foreground text-xs">
        改参放行只允许<strong>收窄</strong>：删键、从字符串里删字符（保序，不得新增/改写/重排）、
        下调数值、true→false、数组变短、目标只减不增。任何加长或新增都会被平台拒绝。
      </div>
      <textarea
        className="min-h-40 w-full rounded-md border bg-background p-2 font-mono text-xs"
        value={text}
        spellCheck={false}
        onChange={(e) => setText(e.target.value)}
      />
      <div className="flex gap-2">
        <Button size="sm" disabled={busy} onClick={submit}>
          <CheckIcon data-icon="inline-start" />
          按改后参数放行
        </Button>
        <Button size="sm" variant="outline" disabled={busy} onClick={onClose}>
          取消
        </Button>
      </div>
    </div>
  );
}

export function DecisionActions({ row, busy, decide }: { row: InterceptApprovalRow; busy: boolean; decide: Decide }) {
  const [editing, setEditing] = React.useState(false);
  if (row.status !== "pending") return null;
  if (editing) {
    return <NarrowEditor row={row} busy={busy} decide={decide} onClose={() => setEditing(false)} />;
  }
  return (
    <div className="flex flex-wrap gap-2">
      <Button size="sm" disabled={busy} onClick={() => void decide(row.id, "allowed")}>
        <CheckIcon data-icon="inline-start" />
        允许
      </Button>
      <Button size="sm" variant="outline" disabled={busy} onClick={() => setEditing(true)}>
        改参放行
      </Button>
      <Button size="sm" variant="destructive" disabled={busy} onClick={() => void decide(row.id, "denied")}>
        <XIcon data-icon="inline-start" />
        拒绝
      </Button>
    </div>
  );
}
