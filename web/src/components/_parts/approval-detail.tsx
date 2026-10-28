"use client";

import * as React from "react";

import { ChevronDownIcon, ChevronRightIcon } from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import type { InterceptApprovalRow, InterceptDetail } from "@/lib/types";
import { cn } from "@/lib/utils";

import { fmtTime, originLabel, source } from "./approval-helpers";
import {
  ApprovalOrigin,
  actionLabels,
  CodeBlock,
  contextLabels,
  type Decide,
  DecisionActions,
  executionLabels,
  MatchCell,
  ModelReviewContext,
  StatusBadge,
} from "./approval-parts";

// 审批详情面板与审批列表（含列可见性计算）。

export function ApprovalDetail({
  row,
  busy,
  decide,
  revision,
  readOnly = false,
  defaultExpanded = false,
  onResolved,
}: {
  row: InterceptApprovalRow;
  busy: boolean;
  decide: Decide;
  revision: number;
  readOnly?: boolean;
  defaultExpanded?: boolean;
  onResolved?: (status: "allowed" | "denied" | "timeout") => void;
}) {
  const [detail, setDetail] = React.useState<InterceptDetail | null>(null);
  const [error, setError] = React.useState("");
  const [retry, setRetry] = React.useState(0);
  const [more, setMore] = React.useState(defaultExpanded);

  // 只在审批仍未落地时轮询；status/revision/重试按钮都会重建轮询并立即拉一次。
  // 走 usePolling 才能复用不可见暂停、失败退避与「上一发未回不发下一发」。
  // 切换行时 detail 可能还留着上一行的状态：只有 id 对得上才采信它的状态。
  const currentDetail = detail?.id === row.id ? detail : null;
  const shouldPoll =
    (currentDetail?.status ?? row.status) === "pending" ||
    currentDetail?.audit?.execution_status === "awaiting_result";
  usePolling(
    async (alive) => {
      try {
        const next = await api.interceptDetail(row.id);
        if (!alive()) return;
        setDetail(next);
        setError("");
        if (next.status !== "pending") onResolved?.(next.status);
      } catch (e) {
        if (alive()) setError((e as Error).message || "详情加载失败");
      }
    },
    shouldPoll ? 5000 : 0,
    [row.id, row.status, revision, retry, onResolved, shouldPoll],
  );

  // Row updates are authoritative until the lazy detail has caught up.
  const current = detail?.status === row.status ? detail : row;
  const audit = detail?.audit;
  let execution = audit ? executionLabels[audit.execution_status] : "未记录";
  if (row.status === "pending") execution = "尚未执行";
  if (audit && audit.correlation !== "exact" && audit.effective_action === "allow") execution = "未关联执行结果";
  const command = typeof row.tool_input?.command === "string" ? row.tool_input.command : undefined;
  let initialLabel = source(row) === "model" ? "模型初判" : "规则初判";
  if (audit?.model_fallback) initialLabel = "模型异常回退";

  return (
    <div className="flex min-w-0 flex-col gap-4 p-3 sm:p-5">
      <div className="grid min-w-0 gap-5 rounded-xl border bg-muted/20 p-4 lg:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-3">
          <CodeBlock
            label={`${current.agent_name || current.conv_agent_key || "Agent"} · 工具请求`}
            text={JSON.stringify(row.tool_input ?? {}, null, 2)}
          />
          {command ? (
            <Collapsible>
              <CollapsibleTrigger asChild>
                <Button variant="ghost" size="sm">
                  <ChevronDownIcon data-icon="inline-start" />
                  查看命令内容
                </Button>
              </CollapsibleTrigger>
              <CollapsibleContent className="pt-2">
                <CodeBlock label="命令内容" text={command} />
              </CollapsibleContent>
            </Collapsible>
          ) : null}
          <p className="text-muted-foreground text-xs">
            执行结果：<span className="text-foreground">{detail ? execution : "加载中…"}</span>
          </p>
        </div>
        <div className="flex min-w-0 flex-col gap-4 lg:border-l lg:pl-5">
          <h3 className="font-medium text-muted-foreground text-xs">
            {current.status === "pending" ? "审查状态" : "审批裁决"}
          </h3>
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge status={current.status} />
            <MatchCell row={current} showReason={false} />
          </div>
          <p className="whitespace-pre-wrap break-words text-sm leading-7 [overflow-wrap:anywhere]">
            {current.reason?.replace(/^\[模型\]\s*/, "") || "未记录审批理由"}
          </p>
          {audit?.decision_reason ? <p className="text-sm">{audit.decision_reason}</p> : null}
          {audit?.effective_action ? <p className="text-sm">最终动作：{actionLabels[audit.effective_action]}</p> : null}
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-xs">
            <dt className="text-muted-foreground">来源</dt>
            <dd className="break-words">
              <ApprovalOrigin row={current} detail />
            </dd>
            <dt className="text-muted-foreground">申请时间</dt>
            <dd>{fmtTime(row.created_at)}</dd>
            <dt className="text-muted-foreground">决定时间</dt>
            <dd>{fmtTime(current.decided_at)}</dd>
            {audit?.rule_name ? (
              <>
                <dt className="text-muted-foreground">规则快照</dt>
                <dd>{audit.rule_name}</dd>
              </>
            ) : null}
            {audit?.profile_id ? (
              <>
                <dt className="text-muted-foreground">审批模型配置</dt>
                <dd>#{audit.profile_id}</dd>
              </>
            ) : null}
          </dl>
          {/* 事前可检出性（P38）：人做决定的那一刻最该看到"这一手会留下什么"。
              刻意标成**预计**——实测结论走检测验证（P31），两者混起来就是把预期读成实测。 */}
          {(audit?.opsec_notes?.length ?? 0) > 0 ? (
            <div className="rounded-md border p-3">
              <p className="text-muted-foreground mb-2 text-xs font-medium">可检出性（预计 · 来自知识库，不是实测）</p>
              <ul className="flex flex-col gap-2">
                {(audit?.opsec_notes ?? []).map((n) => (
                  <li key={n.id} className="text-xs">
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant={n.exposure === "高" ? "destructive" : "secondary"} className="shrink-0">
                        可见度 {n.exposure}
                      </Badge>
                      <span className="font-medium">{n.title}</span>
                    </div>
                    <p className="text-muted-foreground mt-0.5">
                      留在哪：{n.where.join(" / ")}
                      {(n.signals?.length ?? 0) > 0 ? ` · 信号：${(n.signals ?? []).join("；")}` : ""}
                    </p>
                    <p className="mt-0.5 break-words">{n.note}</p>
                  </li>
                ))}
              </ul>
            </div>
          ) : audit?.opsec_uncovered ? (
            <p className="text-muted-foreground rounded-md border p-3 text-xs">
              可检出性：知识库没有收录这条命令——**这不代表没有日志**，只是本库没有依据。必要时用检测验证记录实测结果。
            </p>
          ) : null}
          {!readOnly ? <DecisionActions row={current} busy={busy} decide={decide} /> : null}
        </div>
      </div>
      {error ? (
        <Alert variant="destructive">
          <AlertDescription>
            <div className="flex flex-wrap items-center gap-2">
              <span>详情加载失败：{error}</span>
              <Button variant="outline" size="sm" onClick={() => setRetry((v) => v + 1)}>
                重试详情
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      ) : null}
      {!detail && !error ? <Skeleton className="h-8 w-60" /> : null}
      {detail && !audit ? (
        <Alert>
          <AlertDescription>此记录未保存审批详情快照，无法还原当时的上下文、模型初判和执行输出。</AlertDescription>
        </Alert>
      ) : null}
      {audit ? (
        <Collapsible open={more} onOpenChange={setMore}>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm">
              <ChevronDownIcon data-icon="inline-start" className={cn(more && "rotate-180")} />
              {more ? "收起上下文" : "查看上下文与执行结果"}
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="pt-4">
            <div className="flex min-w-0 flex-col gap-5">
              {audit.model_input ? (
                <ModelReviewContext input={audit.model_input} />
              ) : (
                <Alert>
                  <AlertDescription>
                    {audit.model_input_digest
                      ? "此历史记录仅保存审查输入指纹，未保存输入原文，无法还原当时发给模型的上下文。这不代表审查时没有上下文；新产生的模型裁决会保留输入快照。"
                      : "此记录没有保存模型审查输入，可能由规则直接裁决、模型调用前发生异常或产生于旧版本。"}
                  </AlertDescription>
                </Alert>
              )}
              {audit.user_message || audit.context?.length ? (
                <Collapsible>
                  <CollapsibleTrigger asChild>
                    <Button variant="ghost" size="sm">
                      <ChevronDownIcon data-icon="inline-start" />
                      {audit.model_input && audit.model_input.version >= 3
                        ? "查看会话审计片段（未发送模型）"
                        : "查看会话审计片段"}
                    </Button>
                  </CollapsibleTrigger>
                  <CollapsibleContent className="pt-3">
                    <div className="flex min-w-0 flex-col gap-3">
                      {audit.user_message ? (
                        <CodeBlock
                          label="会话当前轮输入（审计片段）"
                          text={audit.user_message}
                          truncated={audit.user_truncated}
                        />
                      ) : null}
                      <section className="flex min-w-0 flex-col gap-3">
                        <h3 className="font-medium text-muted-foreground text-xs">可见会话上下文</h3>
                        <p className="text-muted-foreground text-xs">
                          保存于 {fmtTime(audit.captured_at)} 的会话记录片段。模型实际使用的内容以“模型审查输入”为准。
                        </p>
                        {audit.context_truncated ? (
                          <p className="text-muted-foreground text-xs">仅保存最近的上下文，部分内容已截断。</p>
                        ) : null}
                        {audit.context?.length ? (
                          audit.context.map((entry, index) => (
                            <CodeBlock
                              key={`${entry.kind}-${entry.tool_use_id || index}`}
                              label={`${contextLabels[entry.kind] ?? entry.kind}${entry.tool ? ` · ${entry.tool}` : ""}${entry.is_error ? " · 异常" : ""}`}
                              text={entry.text}
                              truncated={entry.truncated}
                            />
                          ))
                        ) : (
                          <p className="text-muted-foreground text-sm">未记录可关联的上下文</p>
                        )}
                      </section>
                    </div>
                  </CollapsibleContent>
                </Collapsible>
              ) : null}
              <CodeBlock
                label={`${initialLabel}：${actionLabels[audit.initial_action] ?? audit.initial_action}`}
                text={audit.initial_reason.replace(/^\[模型\]\s*/, "")}
              />
              <CodeBlock
                label="执行输出"
                text={audit.output ?? (audit.execution_status === "not_executed" ? "工具未执行。" : "尚无执行输出")}
                truncated={audit.output_truncated}
              />
              {audit.correlation !== "exact" ? (
                <Alert>
                  <AlertDescription>
                    {audit.correlation === "ambiguous"
                      ? "存在相同参数的并发调用，无法唯一关联工具调用；本记录不展示推测的执行结果。"
                      : "未记录可唯一关联的工具调用 ID。"}
                  </AlertDescription>
                </Alert>
              ) : null}
              <dl className="grid gap-2 text-muted-foreground text-xs [overflow-wrap:anywhere]">
                <div>工具调用 ID：{audit.tool_use_id || "未记录"}</div>
                <div>参数摘要 SHA-256：{audit.input_digest}</div>
                <div>审查配置指纹 SHA-256：{audit.config_digest || "未记录"}</div>
                {audit.model_input_digest ? <div>模型审查输入 SHA-256：{audit.model_input_digest}</div> : null}
                {audit.execution_ended_at ? <div>结果记录时间：{fmtTime(audit.execution_ended_at)}</div> : null}
              </dl>
            </div>
          </CollapsibleContent>
        </Collapsible>
      ) : null}
    </div>
  );
}

// Hidden cells do not occupy table columns. Keep the detail span in sync so
// expansion cannot create empty columns and leave a gap in the selected row.
const approvalBreakpoints = ["(min-width: 40rem)", "(min-width: 48rem)", "(min-width: 64rem)", "(min-width: 80rem)"];
export function subscribeColumns(onChange: () => void) {
  const queries = approvalBreakpoints.map((query) => window.matchMedia(query));
  for (const query of queries) query.addEventListener("change", onChange);
  return () => {
    for (const query of queries) query.removeEventListener("change", onChange);
  };
}
export function visibleColumnCount() {
  const matches = approvalBreakpoints.map((query) => window.matchMedia(query).matches);
  return 3 + Number(matches[0]) + Number(matches[1]) + 2 * Number(matches[2]) + 2 * Number(matches[3]);
}
export function serverColumnCount() {
  return 9;
}

export function ApprovalTable({
  rows,
  busy,
  decide,
  revision,
  label,
}: {
  rows: InterceptApprovalRow[];
  busy: boolean;
  decide: Decide;
  revision: number;
  label: string;
}) {
  const [expanded, setExpanded] = React.useState<Set<number>>(() => new Set());
  const columns = React.useSyncExternalStore(subscribeColumns, visibleColumnCount, serverColumnCount);
  const prefix = React.useId();
  function toggle(id: number) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }
  return (
    <Table className="table-fixed" aria-label={label}>
      <TableHeader>
        <TableRow>
          <TableHead className="w-10">
            <span className="sr-only">展开详情</span>
          </TableHead>
          <TableHead className="hidden w-14 sm:table-cell">#</TableHead>
          <TableHead className="w-28">工具</TableHead>
          <TableHead className="hidden md:table-cell">来源</TableHead>
          <TableHead className="hidden lg:table-cell">匹配规则</TableHead>
          <TableHead className="hidden xl:table-cell">参数</TableHead>
          <TableHead className="w-24">状态</TableHead>
          <TableHead className="hidden w-36 lg:table-cell">申请时间</TableHead>
          <TableHead className="hidden w-36 xl:table-cell">决定时间</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row) => {
          const open = expanded.has(row.id);
          const panelID = `${prefix}-${row.id}`;
          return (
            <React.Fragment key={row.id}>
              <TableRow
                data-state={open ? "selected" : undefined}
                className="cursor-pointer"
                onClick={() => toggle(row.id)}
              >
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`${open ? "收起" : "展开"}审批 #${row.id}`}
                    aria-expanded={open}
                    aria-controls={open ? panelID : undefined}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggle(row.id);
                    }}
                  >
                    {open ? <ChevronDownIcon /> : <ChevronRightIcon />}
                  </Button>
                </TableCell>
                <TableCell className="hidden text-muted-foreground sm:table-cell">{row.id}</TableCell>
                <TableCell>
                  <code className="block truncate text-xs">{row.tool_name}</code>
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  <div className="flex flex-col gap-1">
                    <span className="truncate" title={originLabel(row)}>
                      <ApprovalOrigin row={row} />
                    </span>
                    <span className="truncate text-muted-foreground text-xs">
                      {row.agent_name || row.conv_agent_key}
                    </span>
                  </div>
                </TableCell>
                <TableCell className="hidden lg:table-cell">
                  <MatchCell row={row} />
                </TableCell>
                <TableCell className="hidden xl:table-cell">
                  <code className="block truncate text-muted-foreground text-xs">{JSON.stringify(row.tool_input)}</code>
                </TableCell>
                <TableCell>
                  <StatusBadge status={row.status} />
                </TableCell>
                <TableCell className="hidden text-muted-foreground text-xs lg:table-cell">
                  {fmtTime(row.created_at)}
                </TableCell>
                <TableCell className="hidden text-muted-foreground text-xs xl:table-cell">
                  {fmtTime(row.decided_at)}
                </TableCell>
              </TableRow>
              {open ? (
                <TableRow className="hover:bg-transparent has-aria-expanded:bg-transparent">
                  <TableCell colSpan={columns} className="whitespace-normal p-0">
                    <section id={panelID} aria-label={`审批详情 #${row.id}`}>
                      <ApprovalDetail row={row} busy={busy} decide={decide} revision={revision} />
                    </section>
                  </TableCell>
                </TableRow>
              ) : null}
            </React.Fragment>
          );
        })}
      </TableBody>
    </Table>
  );
}
