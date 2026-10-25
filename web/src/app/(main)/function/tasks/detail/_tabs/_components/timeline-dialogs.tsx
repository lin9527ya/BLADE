"use client";

import * as React from "react";

import { Loader2Icon } from "lucide-react";

import { ApprovalDetail } from "@/components/_parts/approval-detail";
import type { Decide } from "@/components/_parts/approval-parts";
import { StatusBadge } from "@/components/status-badge";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import type { InterceptDetail, ToolExecution } from "@/lib/types";

// 时间线里非节点事件的详情弹窗：执行台账 / 审批记录。
// 节点事件直接复用 NodeDetailDialog（在时间线页签里打开）。

function fmt(ts?: string): string {
  if (!ts) return "—";
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString("zh-CN", { hour12: false });
}

function KV({ label, children }: { label: string; children: React.ReactNode }) {
  if (children === undefined || children === null || children === "") return null;
  return (
    <div className="flex gap-3 text-sm">
      <span className="text-muted-foreground w-20 shrink-0 text-xs">{label}</span>
      <span className="min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

export function ExecutionDialog({
  id,
  taskId,
  onOpenChange,
}: {
  id: number | null;
  taskId: string;
  onOpenChange: (open: boolean) => void;
}) {
  const [row, setRow] = React.useState<ToolExecution | null>(null);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    setRow(null);
    setError("");
    if (id == null) return;
    let cancelled = false;
    api
      .execution(id, taskId)
      .then((r) => {
        if (!cancelled) setRow(r.execution);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : "加载失败");
      });
    return () => {
      cancelled = true;
    };
  }, [id, taskId]);

  return (
    <Dialog open={id != null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>执行 #{id}</DialogTitle>
          <DialogDescription>长任务执行台账：命令、状态、输出尾部与失败原因。</DialogDescription>
        </DialogHeader>
        {error && <p className="text-destructive text-sm">读取失败：{error}</p>}
        {!row && !error && (
          <p className="text-muted-foreground flex items-center gap-2 text-sm">
            <Loader2Icon className="size-4 animate-spin" /> 加载中…
          </p>
        )}
        {row && (
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <StatusBadge domain="execution" value={row.status} />
              <span className="font-mono text-xs">{row.tool}</span>
              <span className="text-muted-foreground text-xs">{row.worker || "—"}</span>
            </div>
            <div className="flex flex-col gap-1.5">
              <KV label="描述">{row.description}</KV>
              <KV label="意图">{row.intent_id ? `#${row.intent_id}` : undefined}</KV>
              <KV label="开始">{fmt(row.started_at)}</KV>
              <KV label="结束">{fmt(row.ended_at)}</KV>
              <KV label="退出码">{row.exit_code ?? undefined}</KV>
              <KV label="输出">{row.output_bytes ? `${row.output_bytes.toLocaleString()} 字节` : undefined}</KV>
              <KV label="取消原因">{row.cancel_note}</KV>
              <KV label="错误">{row.error}</KV>
            </div>
            <div>
              <h4 className="text-muted-foreground mb-1 text-xs font-medium">命令</h4>
              <pre className="bg-muted/50 max-h-64 overflow-auto rounded-md border p-3 font-mono text-xs whitespace-pre-wrap break-all">
                {row.command || "—"}
              </pre>
            </div>
            {row.partial_tail && (
              <div>
                <h4 className="text-muted-foreground mb-1 text-xs font-medium">输出尾部</h4>
                <pre className="bg-muted/50 max-h-64 overflow-auto rounded-md border p-3 font-mono text-xs whitespace-pre-wrap break-all">
                  {row.partial_tail}
                </pre>
              </div>
            )}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

// 只读审批详情：ReadOnly 模式不会渲染决策按钮，decide 不会被调用。
const noopDecide: Decide = async () => undefined;

export function ApprovalDialog({
  id,
  onOpenChange,
}: {
  id: number | null;
  onOpenChange: (open: boolean) => void;
}) {
  const [row, setRow] = React.useState<InterceptDetail | null>(null);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    setRow(null);
    setError("");
    if (id == null) return;
    let cancelled = false;
    api
      .interceptDetail(id)
      .then((r) => {
        if (!cancelled) setRow(r);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : "加载失败");
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  return (
    <Dialog open={id != null} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>审批 #{id}</DialogTitle>
          <DialogDescription>这次调用的判定来源、规则命中与执行结果（只读）。</DialogDescription>
        </DialogHeader>
        {error && <p className="text-destructive text-sm">读取失败：{error}</p>}
        {!row && !error && (
          <p className="text-muted-foreground flex items-center gap-2 text-sm">
            <Loader2Icon className="size-4 animate-spin" /> 加载中…
          </p>
        )}
        {row && <ApprovalDetail row={row} busy={false} decide={noopDecide} revision={0} readOnly />}
      </DialogContent>
    </Dialog>
  );
}
