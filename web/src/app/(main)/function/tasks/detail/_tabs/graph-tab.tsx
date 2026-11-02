"use client";

import * as React from "react";

import { AlertTriangleIcon, RefreshCwIcon } from "lucide-react";

import { ExplorationGraph } from "@/components/exploration-graph";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { graphAuditHeadline, graphAuditIssueLabel, graphAuditRefs, graphAuditSummary } from "@/lib/graph-audit";
import type { Edge, GraphAuditReport, TaskNode } from "@/lib/types";

export function GraphTab({ taskId }: { taskId: string }) {
  const [nodes, setNodes] = React.useState<TaskNode[]>([]);
  const [edges, setEdges] = React.useState<Edge[]>([]);
  // 图体检：只读报告。干净时不渲染任何东西——干净图不该占版面。
  const [audit, setAudit] = React.useState<GraphAuditReport | null>(null);
  const [auditOpen, setAuditOpen] = React.useState(false);
  const [auditBusy, setAuditBusy] = React.useState(false);
  // 上一次图数据的签名:轮询拿到相同数据时跳过 setState,避免整图无谓重建(拖动时
  // 才不会被 20s 轮询打断而顿挫)。只取影响渲染的字段。
  const sigRef = React.useRef("");

  // 只在换任务时清空签名；此前写在轮询回调里，每轮都清空 → 相同数据也重新 setState，
  // 图在 20s 轮询时被无谓重建（拖动中的顿挫就是这么来的）。
  React.useEffect(() => {
    sigRef.current = "";
    setAudit(null);
    setAuditOpen(false);
  }, []);

  const refreshAudit = React.useCallback(() => {
    setAuditBusy(true);
    return api
      .explorationInvariants(taskId)
      .then((report) => setAudit(report))
      .catch(() => {
        /* 404（没有可体检的图）/网络失败：保留上一次结论；体检失败不该影响看图 */
      })
      .finally(() => setAuditBusy(false));
  }, [taskId]);

  React.useEffect(() => {
    void refreshAudit();
  }, [refreshAudit]);

  usePolling(
    (alive) => {
      return Promise.all([
        api
          .explorationGraph(taskId)
          .then((g) => {
            if (!alive()) return;
            const ns = g.nodes ?? [];
            const es = g.edges ?? [];
            const sig = JSON.stringify([
              ns.map((n) => [n.id, n.type, n.state, n.priority, n.payload]),
              es.map((e) => [e.src, e.dst, e.rel]),
            ]);
            if (sig === sigRef.current) return; // 无变化 → 不重建
            sigRef.current = sig;
            setNodes(ns);
            setEdges(es);
          })
          .catch(() => {
            /* keep last good data */
          }),
        // 体检很轻（几个聚合查询），跟图同频；失败静默，不打断已有的结论。
        api
          .explorationInvariants(taskId)
          .then((report) => {
            if (alive()) setAudit(report);
          })
          .catch(() => {}),
      ]).then(() => undefined);
    },
    20000,
    [taskId],
  );

  const summary = graphAuditSummary(audit);

  return (
    <div className="flex flex-col gap-3">
      {!summary.clean && (
        <Card
          className={
            summary.errors > 0 ? "border-destructive/40 bg-destructive/5" : "border-amber-500/40 bg-amber-500/5"
          }
        >
          <CardHeader className="py-3">
            <CardTitle className="flex flex-wrap items-center gap-2 text-sm">
              <AlertTriangleIcon
                className={summary.errors > 0 ? "size-4 text-destructive" : "size-4 text-amber-600 dark:text-amber-400"}
              />
              图体检发现问题
              <span className="font-normal text-muted-foreground">{graphAuditHeadline(audit)}</span>
              <span className="ml-auto flex items-center gap-1">
                <Button variant="ghost" size="sm" onClick={() => setAuditOpen((v) => !v)}>
                  {auditOpen ? "收起" : "展开"}
                </Button>
                <Button variant="ghost" size="sm" onClick={() => void refreshAudit()} disabled={auditBusy}>
                  <RefreshCwIcon className="size-3.5" />
                  重新体检
                </Button>
              </span>
            </CardTitle>
          </CardHeader>
          {auditOpen && (
            <CardContent className="flex flex-col gap-2 pt-0 text-xs">
              {(audit?.issues ?? []).map((issue, index) => {
                const refs = graphAuditRefs(issue);
                return (
                  <div
                    key={`${issue.code}-${index}`}
                    className="rounded-md border border-border/60 bg-background/40 p-2"
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <Badge variant={issue.severity === "error" ? "destructive" : "secondary"}>
                        {graphAuditIssueLabel(issue.code)}
                      </Badge>
                      {refs.length > 0 && <span className="font-mono text-muted-foreground">{refs.join(" ")}</span>}
                    </div>
                    {issue.detail && <p className="mt-1 text-muted-foreground">{issue.detail}</p>}
                  </div>
                );
              })}
              {audit?.truncated && Object.keys(audit.truncated).length > 0 && (
                <p className="text-muted-foreground">同类问题过多，列表只显示前 50 条；总数以上方统计为准。</p>
              )}
            </CardContent>
          )}
        </Card>
      )}
      <Card>
        <CardContent className="p-0">
          <ExplorationGraph nodes={nodes} edges={edges} className="h-[72vh]" taskId={taskId} />
        </CardContent>
      </Card>
    </div>
  );
}
