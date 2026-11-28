"use client";

import * as React from "react";

import { useSearchParams } from "next/navigation";

import { BarChart3Icon, Loader2Icon, SearchIcon, ShieldCheckIcon, TerminalIcon } from "lucide-react";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { CommandRecord, ToolStat } from "@/lib/types";
import { cn } from "@/lib/utils";

import { VerificationLedgerPanel } from "./verification-ledger-panel";

function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function toolInput(raw: string): string {
  try {
    const obj = JSON.parse(raw);
    if (obj && typeof obj.command === "string") return obj.command;
    return JSON.stringify(obj, null, 2);
  } catch {
    /* not JSON */
  }
  return raw;
}

function truncate(s: string, maxLen: number): string {
  const first = s.split("\n")[0];
  if (first.length <= maxLen) return first;
  return `${first.slice(0, maxLen)}…`;
}

const PAGE_SIZES = [25, 50, 100];
const CMD_MAX_LEN = 80;

export type CommandsViewMode = "commands" | "verification";

function CommandsWorkbenchInner({ initialView = "commands" }: { initialView?: CommandsViewMode }) {
  const searchParams = useSearchParams();
  const viewParam = searchParams.get("view");
  const [view, setView] = React.useState<CommandsViewMode>(
    initialView !== "commands" ? initialView : viewParam === "verification" ? "verification" : initialView,
  );
  const [verificationTotal, setVerificationTotal] = React.useState<number | null>(null);
  const prevViewParam = React.useRef<string | null>(viewParam);

  React.useEffect(() => {
    if (initialView !== "commands") return;
    if (viewParam === "commands" || viewParam === "verification") {
      setView(viewParam);
    } else if (prevViewParam.current && !viewParam) {
      setView("commands");
    }
    prevViewParam.current = viewParam;
  }, [initialView, viewParam]);

  const handleViewChange = React.useCallback(
    (v: string) => {
      const next = v as CommandsViewMode;
      setView(next);
      if (initialView === "commands" && typeof window !== "undefined") {
        const url = new URL(window.location.href);
        if (next === "commands") url.searchParams.delete("view");
        else url.searchParams.set("view", next);
        window.history.replaceState(null, "", `${url.pathname}${url.search}${url.hash}`);
      }
    },
    [initialView],
  );

  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [query, setQuery] = React.useState("");
  const [queryQ, setQueryQ] = React.useState("");
  const [taskFilter, setTaskFilter] = React.useState("");

  const [commands, setCommands] = React.useState<CommandRecord[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(false);

  const [statsOpen, setStatsOpen] = React.useState(false);
  const [stats, setStats] = React.useState<ToolStat[]>([]);
  const [statsLoading, setStatsLoading] = React.useState(false);

  const [selected, setSelected] = React.useState<CommandRecord | null>(null);

  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: every filter change intentionally resets pagination.
  React.useEffect(() => {
    setPage(0);
  }, [queryQ, taskFilter, size]);

  React.useEffect(() => {
    if (view !== "commands") return;
    let alive = true;
    setLoading(true);
    api
      .commands({ task: taskFilter || undefined, q: queryQ || undefined, page, size })
      .then((r) => {
        if (!alive) return;
        setCommands(r.commands ?? []);
        setTotal(r.total ?? 0);
      })
      .catch(() => {
        if (!alive) return;
        setCommands([]);
        setTotal(0);
      })
      .finally(() => alive && setLoading(false));
    return () => {
      alive = false;
    };
  }, [view, page, size, queryQ, taskFilter]);

  React.useEffect(() => {
    if (!statsOpen) return;
    let alive = true;
    setStatsLoading(true);
    api
      .commandStats({ task: taskFilter || undefined, q: queryQ || undefined })
      .then((r) => alive && setStats(r.stats ?? []))
      .catch(() => alive && setStats([]))
      .finally(() => alive && setStatsLoading(false));
    return () => {
      alive = false;
    };
  }, [statsOpen, queryQ, taskFilter]);

  const statsTotal = stats.reduce((n, s) => n + s.total, 0);
  const statsErrors = stats.reduce((n, s) => n + s.errors, 0);
  const statsMax = stats.reduce((n, s) => Math.max(n, s.total), 0);

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            {view === "commands" ? (
              <TerminalIcon className="h-5 w-5 text-muted-foreground" />
            ) : (
              <ShieldCheckIcon className="h-5 w-5 text-muted-foreground" />
            )}
            <h1 className="font-semibold text-xl tracking-tight">工具执行</h1>
            <Badge variant="secondary">{view === "commands" ? total : (verificationTotal ?? "—")}</Badge>
          </div>
          {view === "verification" && (
            <p className="mt-1 text-muted-foreground text-xs">
              按工具执行状态、退出码与输出尾部汇总（原验证台账视图；凭据复核与发现复测仍在各自页面）。
            </p>
          )}
        </div>
        <Tabs value={view} onValueChange={handleViewChange}>
          <TabsList>
            <TabsTrigger value="commands">
              <TerminalIcon className="size-3.5" /> 执行明细
            </TabsTrigger>
            <TabsTrigger value="verification">
              <ShieldCheckIcon className="size-3.5" /> 验证视角
            </TabsTrigger>
          </TabsList>
        </Tabs>
      </div>

      {view === "verification" ? (
        <VerificationLedgerPanel onTotalChange={setVerificationTotal} />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <div className="relative max-w-sm flex-1">
              <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                placeholder="搜索工具 / 参数..."
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                className="h-8 pl-8"
              />
            </div>
            <Input
              placeholder="任务 ID"
              className="h-8 w-28"
              value={taskFilter}
              onChange={(e) => setTaskFilter(e.target.value.replace(/\D/g, ""))}
            />
            <Select value={String(size)} onValueChange={(v) => setSize(Number(v))}>
              <SelectTrigger size="sm" className="w-28">
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

            <Button variant="outline" size="sm" className="h-8" onClick={() => setStatsOpen(true)}>
              <BarChart3Icon className="size-4" />
              统计
            </Button>

            <TablePagination
              variant="compact"
              page={page + 1}
              pageSize={size}
              total={total}
              onPageChange={(p) => setPage(p - 1)}
            />
          </div>

          <div className="flex h-[calc(100vh-14.5rem)] min-h-0 flex-col">
            <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
              <div className="min-h-0 flex-1 overflow-auto">
                <Table>
                  <TableHeader className="sticky top-0 z-10 bg-card">
                    <TableRow>
                      <TableHead className="w-[130px]">时间</TableHead>
                      <TableHead className="w-[60px]">任务</TableHead>
                      <TableHead className="w-[90px]">Worker</TableHead>
                      <TableHead className="w-[110px]">工具</TableHead>
                      <TableHead>输入</TableHead>
                      <TableHead className="w-[60px]">状态</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {loading && commands.length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={6} className="py-12 text-center">
                          <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
                        </TableCell>
                      </TableRow>
                    ) : commands.length === 0 ? (
                      <TableRow>
                        <TableCell colSpan={6} className="py-12 text-center text-muted-foreground text-sm">
                          暂无工具执行记录
                        </TableCell>
                      </TableRow>
                    ) : (
                      commands.map((cmd) => (
                        <TableRow
                          key={cmd.id}
                          className={cn("cursor-pointer", selected?.id === cmd.id && "bg-accent hover:bg-accent")}
                          onClick={() => setSelected(cmd)}
                        >
                          <TableCell className="text-muted-foreground text-xs tabular-nums">
                            {fmtTime(cmd.created_at)}
                          </TableCell>
                          <TableCell className="font-mono text-muted-foreground text-xs">
                            #{cmd.exploration_id}
                          </TableCell>
                          <TableCell>
                            <Badge variant="outline" className="font-mono text-xs">
                              {cmd.worker || "-"}
                            </Badge>
                          </TableCell>
                          <TableCell>
                            <Badge variant="secondary" className="font-mono text-xs">
                              {cmd.tool || "-"}
                            </Badge>
                          </TableCell>
                          <TableCell className="max-w-0">
                            <code className="block truncate font-mono text-xs">
                              {truncate(toolInput(cmd.command), CMD_MAX_LEN)}
                            </code>
                          </TableCell>
                          <TableCell>
                            {cmd.is_error ? (
                              <Badge variant="destructive" className="text-xs">
                                失败
                              </Badge>
                            ) : (
                              <Badge variant="secondary" className="text-emerald-600 text-xs">
                                成功
                              </Badge>
                            )}
                          </TableCell>
                        </TableRow>
                      ))
                    )}
                  </TableBody>
                </Table>
              </div>
            </Card>
          </div>
        </>
      )}

      <Dialog open={statsOpen} onOpenChange={setStatsOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>工具调用统计</DialogTitle>
            <DialogDescription>
              {taskFilter || queryQ ? "当前筛选条件下的全部记录" : "全部工具执行记录"}
              {stats.length > 0 && (
                <>
                  {" · "}
                  <span className="tabular-nums">{stats.length}</span> 个工具 ·{" "}
                  <span className="tabular-nums">{statsTotal}</span> 次调用
                  {statsErrors > 0 && (
                    <>
                      {" · 失败 "}
                      <span className="text-red-600 tabular-nums dark:text-red-400">{statsErrors}</span>
                    </>
                  )}
                </>
              )}
            </DialogDescription>
          </DialogHeader>

          {statsLoading && stats.length === 0 ? (
            <div className="py-10 text-center">
              <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : stats.length === 0 ? (
            <div className="py-10 text-center text-muted-foreground text-sm">暂无统计数据</div>
          ) : (
            <div className="-mr-2 max-h-[55vh] space-y-1 overflow-auto pr-2">
              {stats.map((s) => (
                <div key={s.tool} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 rounded-md p-2">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="truncate font-medium font-mono text-xs">{s.tool}</span>
                      {s.errors > 0 && (
                        <span className="text-[11px] text-red-600 tabular-nums dark:text-red-400">失败 {s.errors}</span>
                      )}
                    </div>
                    <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
                      <div
                        className="h-full rounded-full bg-primary"
                        style={{ width: `${statsMax > 0 ? (s.total / statsMax) * 100 : 0}%` }}
                      />
                    </div>
                  </div>
                  <div className="text-right">
                    <div className="font-semibold text-xs tabular-nums">{s.total}</div>
                    <div className="text-[11px] text-muted-foreground tabular-nums">
                      {statsTotal > 0 ? ((s.total / statsTotal) * 100).toFixed(1) : "0.0"}%
                    </div>
                  </div>
                </div>
              ))}
            </div>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-4xl">
          {selected && (
            <>
              <DialogHeader className="border-b px-5 py-4">
                <DialogTitle className="pr-8">工具执行详情</DialogTitle>
                <DialogDescription>{fmtTime(selected.created_at)}</DialogDescription>
                <div className="flex flex-wrap items-center gap-2 pt-2">
                  <Badge variant="outline" className="font-mono text-xs">
                    #{selected.exploration_id}
                  </Badge>
                  <Badge variant="outline" className="font-mono text-xs">
                    {selected.worker || "-"}
                  </Badge>
                  <Badge variant="secondary" className="font-mono text-xs">
                    {selected.tool || "-"}
                  </Badge>
                  {selected.is_error ? (
                    <Badge variant="destructive" className="text-xs">
                      失败
                    </Badge>
                  ) : (
                    <Badge variant="secondary" className="text-emerald-600 text-xs">
                      成功
                    </Badge>
                  )}
                </div>
              </DialogHeader>
              <div className="grid min-h-0 flex-1 grid-rows-2 divide-y">
                <div className="flex min-h-0 min-w-0 flex-col">
                  <div className="border-b px-5 py-2 font-medium text-muted-foreground text-xs">输入 Input</div>
                  <div className="min-h-0 flex-1 overflow-auto">
                    <pre className="whitespace-pre-wrap break-all p-5 font-mono text-xs">
                      {toolInput(selected.command)}
                    </pre>
                  </div>
                </div>
                <div className="flex min-h-0 min-w-0 flex-col">
                  <div className="border-b px-5 py-2 font-medium text-muted-foreground text-xs">输出 Output</div>
                  <div className="min-h-0 flex-1 overflow-auto">
                    <pre
                      className={cn(
                        "whitespace-pre-wrap break-all p-5 font-mono text-xs",
                        selected.is_error && "text-red-600 dark:text-red-400",
                      )}
                    >
                      {selected.output || "（空）"}
                    </pre>
                  </div>
                </div>
              </div>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

export function CommandsWorkbench({ initialView = "commands" }: { initialView?: CommandsViewMode }) {
  return (
    <React.Suspense fallback={null}>
      <CommandsWorkbenchInner initialView={initialView} />
    </React.Suspense>
  );
}
