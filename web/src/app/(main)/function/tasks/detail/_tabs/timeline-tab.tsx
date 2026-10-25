"use client";

import * as React from "react";

import { ArrowDownIcon, ArrowUpIcon, ChevronRightIcon, DownloadIcon, LoaderIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { NodeDetailDialog } from "@/components/_parts/node-detail-dialog";
import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { TaskNode, TimelineEvent, TimelineKind, TimelineResponse } from "@/lib/types";

import { ApprovalDialog, ExecutionDialog } from "./_components/timeline-dialogs";

// 作业时间线（P37）：把台账 / 审批 / 战果 / 检测验证按时间串成一条**可交付**的线。
//
// 它和「播报板」是两件事：播报板是平台内部的活动流水（每一步都记），时间线是给客户与蓝队看的
// 那条线——默认只列里程碑，要看全部步骤得显式打开（否则一条线上九成是"第 N 轮工具调用"）。
// 导出走服务端同一份装配，所以页面上看到的就是交付件里的。

const KIND_LABEL: Record<TimelineKind, string> = {
  execution: "执行",
  approval: "审批",
  finding: "发现",
  loot: "战果",
  artifact: "遗留物",
  detection: "检测验证",
  note: "系统",
  step: "步骤",
};

const KIND_TONE: Record<TimelineKind, string> = {
  execution: "border-sky-500/40 text-sky-700 dark:text-sky-400",
  approval: "border-amber-500/40 text-amber-700 dark:text-amber-400",
  finding: "border-rose-500/40 text-rose-700 dark:text-rose-400",
  loot: "border-orange-500/40 text-orange-700 dark:text-orange-400",
  artifact: "border-amber-600/40 text-amber-700 dark:text-amber-400",
  detection: "border-violet-500/40 text-violet-700 dark:text-violet-400",
  note: "border-slate-400/40 text-slate-600 dark:text-slate-300",
  step: "border-muted text-muted-foreground",
};

const MILESTONE_KINDS: TimelineKind[] = ["execution", "approval", "finding", "loot", "artifact", "detection", "note"];
const PAGE_SIZES = [20, 50, 100];

function fmt(ts: string): string {
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString("zh-CN", { hour12: false });
}

function EventRow({ e, onOpen }: { e: TimelineEvent; onOpen: (e: TimelineEvent) => void }) {
  const clickable = Boolean(e.ref_type && e.ref_id);
  return (
    <li className="border-b last:border-b-0">
      <button
        type="button"
        disabled={!clickable}
        onClick={() => onOpen(e)}
        className={`flex w-full items-start gap-3 py-2 text-left ${
          clickable ? "hover:bg-muted/40 cursor-pointer" : "cursor-default"
        }`}
        title={clickable ? "查看对应详情" : undefined}
      >
        <span className="text-muted-foreground w-36 shrink-0 pt-0.5 font-mono text-[11px] tabular-nums">{fmt(e.at)}</span>
        <Badge variant="outline" className={`shrink-0 ${KIND_TONE[e.kind] ?? ""}`}>
          {KIND_LABEL[e.kind] ?? e.kind}
        </Badge>
        <span className="min-w-0 flex-1">
          <span className="block text-sm break-words">{e.title}</span>
          {e.detail && <span className="text-muted-foreground mt-0.5 block text-xs break-words">{e.detail}</span>}
        </span>
        {e.state && (
          <span className="text-muted-foreground shrink-0 text-[11px]" title={e.state}>
            {e.state_label ?? e.state}
          </span>
        )}
        {clickable && <ChevronRightIcon className="text-muted-foreground mt-0.5 size-3.5 shrink-0" />}
      </button>
    </li>
  );
}

export function TimelineTab({ taskId }: { taskId: string }) {
  const [data, setData] = React.useState<TimelineResponse | null>(null);
  const [withSteps, setWithSteps] = React.useState(false);
  const [kinds, setKinds] = React.useState<TimelineKind[]>(MILESTONE_KINDS);
  const [order, setOrder] = React.useState<"asc" | "desc">("desc");
  const [page, setPage] = React.useState(1);
  const [size, setSize] = React.useState(50);
  const [loading, setLoading] = React.useState(false);
  const [failed, setFailed] = React.useState("");
  const [exporting, setExporting] = React.useState(false);

  // 事件点开后的详情：节点走 NodeDetailDialog，执行/审批走各自的弹窗。
  const nodeCache = React.useRef<Map<string, TaskNode> | null>(null);
  const [selectedNode, setSelectedNode] = React.useState<TaskNode | null>(null);
  const [executionId, setExecutionId] = React.useState<number | null>(null);
  const [approvalId, setApprovalId] = React.useState<number | null>(null);

  const kindsParam = React.useMemo(
    () => [...kinds, ...(withSteps ? (["step"] as TimelineKind[]) : [])].join(","),
    [kinds, withSteps],
  );

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .taskTimeline(taskId, kindsParam, { page, size, order })
      .then((d) => {
        setData(d);
        setFailed("");
      })
      .catch((e: unknown) => setFailed(String((e as Error)?.message ?? e)))
      .finally(() => setLoading(false));
  }, [taskId, kindsParam, page, size, order]);

  React.useEffect(() => {
    load();
  }, [load]);

  // 切换任务时清掉按任务缓存的节点图，避免点事件打开上一个任务的节点。
  React.useEffect(() => {
    nodeCache.current = null;
    setSelectedNode(null);
    setExecutionId(null);
    setApprovalId(null);
  }, [taskId]);

  const toggleKind = (kind: TimelineKind) => {
    if (kinds.includes(kind)) {
      if (kinds.length === 1) {
        toast.info("至少保留一类事件");
        return;
      }
      setKinds(kinds.filter((k) => k !== kind));
    } else {
      setKinds([...kinds, kind]);
    }
    setPage(1);
  };

  const doExport = () => {
    setExporting(true);
    api
      .downloadTaskTimeline(taskId, kindsParam)
      .then(() => toast.success("已导出作业时间线（.md）"))
      .catch((e: unknown) => toast.error("导出失败：" + String((e as Error)?.message ?? e)))
      .finally(() => setExporting(false));
  };

  const openNode = async (id: number) => {
    try {
      if (!nodeCache.current) {
        const g = await api.explorationGraph(taskId);
        nodeCache.current = new Map(g.nodes.map((n) => [n.id, n]));
      }
      const node = nodeCache.current.get(String(id));
      if (!node) {
        toast.error("节点不存在或已不在当前图中");
        return;
      }
      setSelectedNode(node);
    } catch (e) {
      toast.error(`读取节点失败：${e instanceof Error ? e.message : "未知错误"}`);
    }
  };

  const openEvent = (e: TimelineEvent) => {
    if (!e.ref_id) return;
    switch (e.ref_type) {
      case "node":
        void openNode(e.ref_id);
        break;
      case "execution":
        setExecutionId(e.ref_id);
        break;
      case "intercept":
        setApprovalId(e.ref_id);
        break;
    }
  };

  const counts = data?.counts ?? {};
  const activeKinds = data?.kinds ?? kinds;
  const total = activeKinds
    .filter((k) => k !== "step")
    .reduce((n, k) => n + (counts[k] ?? 0), 0);

  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="flex flex-col gap-3 p-4">
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" onClick={doExport} disabled={exporting || !data}>
              {exporting ? (
                <LoaderIcon className="animate-spin" data-icon="inline-start" />
              ) : (
                <DownloadIcon data-icon="inline-start" />
              )}
              导出 .md
            </Button>
            <Button variant="ghost" size="sm" onClick={load} disabled={loading}>
              {loading ? (
                <LoaderIcon className="animate-spin" data-icon="inline-start" />
              ) : (
                <RefreshCwIcon data-icon="inline-start" />
              )}
              刷新
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setOrder((o) => (o === "desc" ? "asc" : "desc"));
                setPage(1);
              }}
              aria-label={order === "desc" ? "当前最新在前，点击改为最早在前" : "当前最早在前，点击改为最新在前"}
            >
              {order === "desc" ? <ArrowDownIcon data-icon="inline-start" /> : <ArrowUpIcon data-icon="inline-start" />}
              {order === "desc" ? "最新在前" : "最早在前"}
            </Button>
            <label className="ml-auto flex items-center gap-2 text-xs">
              <input
                type="checkbox"
                className="size-3.5 accent-foreground"
                checked={withSteps}
                onChange={(ev) => {
                  setWithSteps(ev.target.checked);
                  setPage(1);
                }}
              />
              包含全部步骤（原始工具调用流水）
            </label>
          </div>

          {/* 类别过滤：API 一直支持 kinds，这里把选择权交给操作者（交付时经常只看发现/战果）。 */}
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-muted-foreground mr-1 text-xs">事件类别</span>
            {MILESTONE_KINDS.map((k) => {
              const on = kinds.includes(k);
              return (
                <Button
                  key={k}
                  size="sm"
                  variant={on ? "secondary" : "outline"}
                  className="h-7 px-2 text-xs"
                  onClick={() => toggleKind(k)}
                  aria-pressed={on}
                >
                  {KIND_LABEL[k]}
                  {(counts[k] ?? 0) > 0 ? <span className="text-muted-foreground ml-1 tabular-nums">{counts[k]}</span> : null}
                </Button>
              );
            })}
          </div>

          <div className="text-muted-foreground flex flex-wrap items-center gap-3 text-xs">
            <span>
              里程碑 {total} 条{withSteps && (counts.step ?? 0) > 0 ? ` · 步骤 ${counts.step} 条` : ""}
            </span>
            {data?.from && (
              <span>
                {fmt(data.from)} → {data.to ? fmt(data.to) : "—"}
              </span>
            )}
            {data?.has_more ? <span>还有更早的事件 · 可翻页查看</span> : null}
          </div>

          <p className="text-muted-foreground text-xs leading-relaxed">
            默认只列**里程碑**（执行 / 审批 / 发现 / 战果 / 遗留物 / 检测验证 / 系统留言）——这是给客户与蓝队看的
            那条线；点任意一格可查看对应的节点 / 执行台账 / 审批记录。原始步骤流水要看时再打开。
            导出与页面读的是服务端同一份装配（含当前类别筛选），所以交付件里就是这里看到的内容。
          </p>

          {failed && <p className="text-destructive text-sm">读取失败：{failed}</p>}
        </CardContent>
      </Card>

      <Card>
        <CardContent className="p-4">
          {(data?.events.length ?? 0) === 0 ? (
            <p className="text-muted-foreground text-sm">这条任务还没有可列入时间线的事件。</p>
          ) : (
            <>
              <ul className="flex flex-col">
                {[...(data?.events ?? [])]
                  .sort((a, b) => (order === "desc" ? b.at.localeCompare(a.at) : a.at.localeCompare(b.at)))
                  .map((e, i) => (
                    <EventRow key={`${e.at}-${e.kind}-${e.ref_id ?? "x"}-${i}`} e={e} onOpen={openEvent} />
                  ))}
              </ul>
              <div className="mt-3 border-t pt-3">
                <TablePagination
                  page={page}
                  pageSize={size}
                  total={data?.total ?? 0}
                  onPageChange={setPage}
                  onPageSizeChange={(n) => {
                    setSize(n);
                    setPage(1);
                  }}
                  pageSizeOptions={PAGE_SIZES}
                />
              </div>
            </>
          )}
        </CardContent>
      </Card>

      <NodeDetailDialog
        node={selectedNode}
        nodesById={nodeCache.current ?? new Map<string, TaskNode>()}
        onSelect={setSelectedNode}
        onOpenChange={(open) => !open && setSelectedNode(null)}
        taskId={taskId}
      />
      <ExecutionDialog
        id={executionId}
        taskId={taskId}
        onOpenChange={(open) => !open && setExecutionId(null)}
      />
      <ApprovalDialog id={approvalId} onOpenChange={(open) => !open && setApprovalId(null)} />
    </div>
  );
}
