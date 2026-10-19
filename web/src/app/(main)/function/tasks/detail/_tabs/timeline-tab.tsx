"use client";

import * as React from "react";

import { DownloadIcon, LoaderIcon, RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { TimelineEvent, TimelineKind, TimelineResponse } from "@/lib/types";

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
  detection: "检测验证",
  note: "系统",
  step: "步骤",
};

const KIND_TONE: Record<TimelineKind, string> = {
  execution: "border-sky-500/40 text-sky-700 dark:text-sky-400",
  approval: "border-amber-500/40 text-amber-700 dark:text-amber-400",
  finding: "border-rose-500/40 text-rose-700 dark:text-rose-400",
  loot: "border-orange-500/40 text-orange-700 dark:text-orange-400",
  detection: "border-violet-500/40 text-violet-700 dark:text-violet-400",
  note: "border-slate-400/40 text-slate-600 dark:text-slate-300",
  step: "border-muted text-muted-foreground",
};

const MILESTONE_KINDS: TimelineKind[] = ["execution", "approval", "finding", "loot", "detection", "note"];

function fmt(ts: string): string {
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString("zh-CN", { hour12: false });
}

function EventRow({ e }: { e: TimelineEvent }) {
  return (
    <li className="flex items-start gap-3 border-b py-2 last:border-b-0">
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
    </li>
  );
}

export function TimelineTab({ taskId }: { taskId: string }) {
  const [data, setData] = React.useState<TimelineResponse | null>(null);
  const [withSteps, setWithSteps] = React.useState(false);
  const [loading, setLoading] = React.useState(false);
  const [failed, setFailed] = React.useState("");
  const [exporting, setExporting] = React.useState(false);

  const kindsParam = withSteps ? "all" : "";

  const load = React.useCallback(
    (steps: boolean) => {
      setLoading(true);
      api
        .taskTimeline(taskId, steps ? "all" : "")
        .then((d) => {
          setData(d);
          setFailed("");
        })
        .catch((e: unknown) => setFailed(String((e as Error)?.message ?? e)))
        .finally(() => setLoading(false));
    },
    [taskId],
  );

  React.useEffect(() => {
    load(false);
  }, [load]);

  const doExport = () => {
    setExporting(true);
    api
      .downloadTaskTimeline(taskId, kindsParam)
      .then(() => toast.success("已导出作业时间线（.md）"))
      .catch((e: unknown) => toast.error("导出失败：" + String((e as Error)?.message ?? e)))
      .finally(() => setExporting(false));
  };

  const counts = data?.counts ?? {};
  const total = MILESTONE_KINDS.reduce((n, k) => n + (counts[k] ?? 0), 0);

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
            <Button variant="ghost" size="sm" onClick={() => load(withSteps)} disabled={loading}>
              {loading ? (
                <LoaderIcon className="animate-spin" data-icon="inline-start" />
              ) : (
                <RefreshCwIcon data-icon="inline-start" />
              )}
              刷新
            </Button>
            <label className="ml-auto flex items-center gap-2 text-xs">
              <input
                type="checkbox"
                className="size-3.5 accent-foreground"
                checked={withSteps}
                onChange={(ev) => {
                  setWithSteps(ev.target.checked);
                  load(ev.target.checked);
                }}
              />
              包含全部步骤（原始工具调用流水）
            </label>
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
            {data?.truncated ? <span>另有 {data.truncated} 条更早的事件未列出</span> : null}
          </div>

          <p className="text-muted-foreground text-xs leading-relaxed">
            默认只列**里程碑**（执行 / 审批 / 发现 / 战果 / 检测验证 / 系统留言）——这是给客户与蓝队看的
            那条线；每一条都指得回原始记录（执行台账 / 审批记录 / 探索节点）。原始步骤流水要看时再打开。
            导出与页面读的是服务端同一份装配，所以交付件里就是这里看到的内容。
          </p>

          {failed && <p className="text-destructive text-sm">读取失败：{failed}</p>}
        </CardContent>
      </Card>

      <Card>
        <CardContent className="p-4">
          {(data?.events.length ?? 0) === 0 ? (
            <p className="text-muted-foreground text-sm">这条任务还没有可列入时间线的事件。</p>
          ) : (
            <ul className="flex flex-col">
              {(data?.events ?? []).map((e, i) => (
                <EventRow key={`${e.at}-${e.kind}-${e.ref_id ?? "x"}-${i}`} e={e} />
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
