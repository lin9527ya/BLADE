"use client";

import * as React from "react";

import Link from "next/link";

import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { TaskCompareRow } from "@/lib/types";

function fmtDuration(seconds?: number): string {
  const s = Math.max(0, Math.floor(seconds ?? 0));
  if (s <= 0) return "—";
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function fmtTokens(row?: TaskCompareRow): string {
  const t = row?.task.tokens;
  if (!t) return "—";
  return `${Math.round((t.input_tokens + t.output_tokens) / 1000)}k`;
}

export function TaskCompareDialog({
  ids,
  open,
  onOpenChange,
}: {
  ids: string[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [rows, setRows] = React.useState<TaskCompareRow[]>([]);
  const [loading, setLoading] = React.useState(false);

  React.useEffect(() => {
    if (!open || ids.length < 2) return;
    let alive = true;
    setLoading(true);
    api
      .compareTasks(ids)
      .then((items) => {
        if (alive) setRows(items);
      })
      .catch((e: Error) => {
        if (alive) toast.error(`对比失败：${e.message}`);
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [ids, open]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[88vh] overflow-y-auto sm:max-w-7xl">
        <DialogHeader>
          <DialogTitle>任务对比</DialogTitle>
          <DialogDescription>对比配置结果与执行规模；不展开报告或发现正文。</DialogDescription>
        </DialogHeader>
        {loading ? (
          <div className="flex min-h-40 items-center justify-center">
            <Spinner />
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>任务</TableHead>
                <TableHead className="w-24">状态</TableHead>
                <TableHead className="w-20 text-right">资产</TableHead>
                <TableHead className="w-24 text-right">发现</TableHead>
                <TableHead className="w-36">严重度</TableHead>
                <TableHead className="w-24 text-right">实跑</TableHead>
                <TableHead className="w-20 text-right">Token</TableHead>
                <TableHead className="w-28">创建时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={row.task.id}>
                  <TableCell className="max-w-xs">
                    <Link
                      href={`/function/tasks/detail?id=${encodeURIComponent(row.task.id)}`}
                      className="block truncate font-medium hover:text-primary hover:underline"
                      title={row.task.name || row.task.description}
                    >
                      {row.task.name || row.task.description || `任务 #${row.task.id}`}
                    </Link>
                    <span className="text-muted-foreground text-xs">#{row.task.id}</span>
                  </TableCell>
                  <TableCell>
                    <StatusBadge domain="task" value={row.task.status} dot />
                  </TableCell>
                  <TableCell className="text-right text-sm tabular-nums">{row.assets_total}</TableCell>
                  <TableCell className="text-right text-sm tabular-nums">{row.findings_total}</TableCell>
                  <TableCell>
                    <span className="flex flex-wrap gap-1 text-xs tabular-nums">
                      {(["critical", "high", "medium", "low"] as const).map((severity) => {
                        const n = row.task.findings?.[severity] ?? 0;
                        return n > 0 ? (
                          <span key={severity} className="inline-flex items-center gap-1">
                            <StatusBadge domain="severity" value={severity} dot />
                            {n}
                          </span>
                        ) : null;
                      })}
                    </span>
                  </TableCell>
                  <TableCell className="text-right text-sm tabular-nums">
                    {fmtDuration(row.task.active_run_seconds)}
                  </TableCell>
                  <TableCell className="text-right text-sm tabular-nums">{fmtTokens(row)}</TableCell>
                  <TableCell className="text-muted-foreground text-xs">
                    {row.task.created_at ? new Date(row.task.created_at).toLocaleDateString("zh-CN") : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </DialogContent>
    </Dialog>
  );
}
