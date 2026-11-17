"use client";

import * as React from "react";

import Link from "next/link";

import { ExternalLinkIcon, RefreshCwIcon, SearchIcon, ShieldCheckIcon } from "lucide-react";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { VerificationLedgerRow } from "@/lib/types";

const PAGE_SIZES = [25, 50, 100];

function fmtTime(value: string): string {
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

function statusVariant(status: string): "default" | "secondary" | "destructive" | "outline" {
  if (status === "completed") return "default";
  if (status === "failed" || status === "hard_timeout") return "destructive";
  if (status === "running" || status === "queued") return "secondary";
  return "outline";
}

function clip(s: string, n: number): string {
  const first = s.split("\n")[0];
  return first.length > n ? `${first.slice(0, n)}…` : first;
}

export default function VerificationLedgerPage() {
  const [rows, setRows] = React.useState<VerificationLedgerRow[]>([]);
  const [total, setTotal] = React.useState(0);
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [task, setTask] = React.useState("");
  const [tool, setTool] = React.useState("");
  const [status, setStatus] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [queryQ, setQueryQ] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  const [error, setError] = React.useState("");
  const [_refreshKey, setRefreshKey] = React.useState(0);

  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: filters intentionally reset pagination.
  React.useEffect(() => {
    setPage(0);
  }, [task, tool, status, queryQ, size]);

  React.useEffect(() => {
    let alive = true;
    setLoading(true);
    api
      .verificationLedger({
        task_id: task || undefined,
        tool: tool || undefined,
        status: status || undefined,
        q: queryQ || undefined,
        limit: size,
        offset: page * size,
      })
      .then((r) => {
        if (!alive) return;
        setRows(r.items);
        setTotal(r.total);
        setError("");
      })
      .catch((e: Error) => {
        if (!alive) return;
        setRows([]);
        setTotal(0);
        setError(e.message);
      })
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [page, size, task, tool, status, queryQ]);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <ShieldCheckIcon className="size-5 text-muted-foreground" />
          <h1 className="font-semibold text-xl tracking-tight">验证台账</h1>
          <Badge variant="secondary">{total}</Badge>
        </div>
        <Button variant="outline" size="sm" onClick={() => setRefreshKey((k) => k + 1)}>
          <RefreshCwIcon /> 刷新
        </Button>
      </div>
      <p className="text-muted-foreground text-sm">
        只读汇总已有工具执行：命令、状态、退出码与输出尾部。它不是新的 PoC 实体，凭据复核和发现复测仍在各自页面。
      </p>

      <div className="flex flex-wrap items-center gap-2">
        <div className="relative max-w-sm flex-1">
          <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="搜索命令 / 描述 / 输出尾部…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-8 pl-8"
          />
        </div>
        <Input
          placeholder="任务 ID"
          value={task}
          onChange={(e) => setTask(e.target.value.replace(/\D/g, ""))}
          className="h-8 w-28"
        />
        <Input
          placeholder="工具名，如 http_request"
          value={tool}
          onChange={(e) => setTool(e.target.value)}
          className="h-8 w-48 font-mono text-xs"
        />
        <Select value={status || "all"} onValueChange={(v) => setStatus(v === "all" ? "" : v)}>
          <SelectTrigger size="sm" className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部状态</SelectItem>
            <SelectItem value="queued">排队</SelectItem>
            <SelectItem value="running">运行中</SelectItem>
            <SelectItem value="completed">已完成</SelectItem>
            <SelectItem value="failed">失败</SelectItem>
            <SelectItem value="hard_timeout">硬超时</SelectItem>
          </SelectContent>
        </Select>
        <Select value={String(size)} onValueChange={(v) => setSize(Number(v))}>
          <SelectTrigger size="sm" className="w-24">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PAGE_SIZES.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n} / 页
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <TablePagination
          variant="compact"
          page={page + 1}
          pageSize={size}
          total={total}
          onPageChange={(p) => setPage(p - 1)}
        />
      </div>

      {error ? <p className="text-destructive text-sm">加载失败：{error}</p> : null}
      <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
        <div className="min-h-0 flex-1 overflow-auto">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow>
                <TableHead className="w-40">时间</TableHead>
                <TableHead className="w-28">任务</TableHead>
                <TableHead className="w-36">工具</TableHead>
                <TableHead>命令 / 描述</TableHead>
                <TableHead className="w-24">状态</TableHead>
                <TableHead className="w-16 text-right">退出</TableHead>
                <TableHead className="w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {loading && rows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="py-10 text-center">
                    <Spinner />
                  </TableCell>
                </TableRow>
              ) : rows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="py-10 text-center text-muted-foreground text-sm">
                    没有匹配的执行记录。
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((row) => (
                  <TableRow key={row.id}>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(row.created_at)}</TableCell>
                    <TableCell className="font-mono text-xs">
                      {row.task_id ? (
                        <Link
                          href={`/function/tasks/detail?id=${encodeURIComponent(row.task_id)}`}
                          className="hover:text-primary hover:underline"
                          title={row.task_name}
                        >
                          #{row.task_id}
                        </Link>
                      ) : (
                        "—"
                      )}
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline" className="font-mono text-xs">
                        {row.tool}
                      </Badge>
                    </TableCell>
                    <TableCell className="max-w-[36rem]">
                      <div className="truncate font-mono text-xs" title={row.command}>
                        {clip(row.command, 160)}
                      </div>
                      {row.description ? (
                        <div className="truncate text-muted-foreground text-xs" title={row.description}>
                          {clip(row.description, 120)}
                        </div>
                      ) : null}
                      {row.partial_tail ? (
                        <div className="mt-0.5 truncate text-[11px] text-muted-foreground" title={row.partial_tail}>
                          输出：{clip(row.partial_tail, 120)}
                        </div>
                      ) : null}
                    </TableCell>
                    <TableCell>
                      <Badge variant={statusVariant(row.status)}>{row.status}</Badge>
                    </TableCell>
                    <TableCell className="text-right text-xs tabular-nums">{row.exit_code ?? "—"}</TableCell>
                    <TableCell>
                      <Button asChild variant="ghost" size="icon-sm" aria-label="查看任务">
                        <Link href={`/function/tasks/detail?id=${encodeURIComponent(row.task_id)}`}>
                          <ExternalLinkIcon />
                        </Link>
                      </Button>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      </Card>
    </div>
  );
}
