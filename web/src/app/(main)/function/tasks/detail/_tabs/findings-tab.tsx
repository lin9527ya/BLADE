"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowDownIcon,
  ArrowUpIcon,
  ArrowUpRightIcon,
  ChevronRightIcon,
  ClipboardCheckIcon,
  DownloadIcon,
  PackageIcon,
} from "lucide-react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger } from "@/components/ui/select";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { useStoredSortPreference } from "@/lib/sort-preference";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingStatus, TaskSrcBatchPlan } from "@/lib/types";
import { cn } from "@/lib/utils";

type FindingSortField = "time";

const FINDING_SORT_FIELDS: readonly FindingSortField[] = ["time"];
const FINDING_SORT_PREFERENCE_KEY = "blade_task_findings_sort";

function findingLabel(finding: Finding): string {
  if (finding.name?.trim()) return finding.name;
  if (finding.vulnclass?.trim()) return finding.vulnclass;
  return "未分类";
}

const FINDING_STATUSES: FindingStatus[] = [
  "pending",
  "in_progress",
  "confirmed",
  "resolved",
  "fixed",
  "false_positive",
  "ignored",
  "duplicate",
  "risk_accepted",
  "retracted",
  "ruled_out",
  "open_proof_gap",
];

function Row({
  f,
  contextTaskId,
  onStatus,
}: {
  f: Finding;
  contextTaskId: string;
  onStatus: (f: Finding, next: FindingStatus) => void;
}) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="border-b last:border-b-0">
      <div className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-accent/40">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className="flex min-w-0 flex-1 items-center gap-3 text-left"
        >
          <ChevronRightIcon
            className={cn("size-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-90")}
          />
          <StatusBadge domain="severity" value={f.severity} dot />
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <div className="flex min-w-0 items-center gap-2">
              <span className="truncate font-medium">{findingLabel(f)}</span>
              {f.inherited && f.source_task_id && (
                <Badge variant="outline" className="shrink-0">
                  来源 #{f.source_task_id} · 只读
                </Badge>
              )}
            </div>
            <span className="truncate text-xs text-muted-foreground">{f.summary}</span>
                    {(f.status === "ruled_out" || f.status === "open_proof_gap") && (f.counterevidence || f.proof_gap) && (
                      <span
                        className="line-clamp-2 text-xs text-amber-600 dark:text-amber-400"
                        title={f.status === "ruled_out" ? f.counterevidence : f.proof_gap}
                      >
                        {f.status === "ruled_out" ? "排除原因：" : "待补证："}
                        {f.status === "ruled_out" ? f.counterevidence : f.proof_gap}
                      </span>
                    )}

          </div>
        </button>
        <Badge variant="outline">流量证据 {f.traffic_count ?? 0} 条</Badge>
        {f.assets && f.assets.length > 0 && (
          <div className="hidden shrink-0 flex-wrap justify-end gap-1 sm:flex">
            {f.assets.slice(0, 2).map((a) => (
              <code
                key={a.id}
                className="max-w-[10rem] truncate rounded bg-muted px-1.5 py-0.5 font-mono text-xs"
                title={`${a.type} · ${a.label}`}
              >
                {a.label}
              </code>
            ))}
            {f.assets.length > 2 && <span className="text-xs text-muted-foreground">+{f.assets.length - 2}</span>}
          </div>
        )}
        {f.finding_id && !f.inherited ? (
          <Select value={f.status} onValueChange={(v) => onStatus(f, v as FindingStatus)}>
            <SelectTrigger size="sm" className="h-7 w-28 shrink-0 border-none px-1 shadow-none focus-visible:ring-0">
              <StatusBadge domain="finding" value={f.status} dot />
            </SelectTrigger>
            <SelectContent position="popper" align="end">
              <SelectGroup>
                {FINDING_STATUSES.map((st) => (
                  <SelectItem key={st} value={st}>
                    {statusMeta("finding", st).label}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        ) : (
          <StatusBadge domain="finding" value={f.status} dot />
        )}
        <span className="hidden shrink-0 text-xs text-muted-foreground md:block">
          {new Date(f.ts).toLocaleString("zh-CN")}
        </span>
        {f.finding_id && (
          <Link
            href={
              f.inherited
                ? `/function/findings/detail?id=${f.finding_id}&context_task=${contextTaskId}`
                : `/function/findings/detail?id=${f.finding_id}`
            }
            className="text-muted-foreground hover:text-primary inline-flex shrink-0 items-center gap-0.5 text-xs"
            title="查看漏洞详情"
          >
            详情
            <ArrowUpRightIcon className="size-3" />
          </Link>
        )}
      </div>
      {open && (
        <div className="bg-muted/30 px-4 pb-4 pl-11">
          <div className="mb-1 text-xs font-medium text-muted-foreground">证据 / PoC</div>
          <pre className="overflow-auto rounded-md border bg-background p-3 font-mono text-xs whitespace-pre-wrap">
            {f.evidence}
          </pre>
        </div>
      )}
    </div>
  );
}

export function FindingsTab({ taskId }: { taskId: string }) {
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [loadError, setLoadError] = React.useState("");
  // P19 批量交付：交付计划（清单 + 证据门 + 疑似重复）与打包下载。
  const [batch, setBatch] = React.useState<TaskSrcBatchPlan | null>(null);
  const [batchBusy, setBatchBusy] = React.useState(false);
  const [sortPreference, setSortPreference] = useStoredSortPreference(
    FINDING_SORT_PREFERENCE_KEY,
    FINDING_SORT_FIELDS,
    "time",
    "desc",
  );

  usePolling(
    async (active) => {
      const load = () => {
        api
          .findings(taskId)
          .then((fs) => {
            if (active()) {
              setFindings(fs);
              setLoadError("");
            }
          })
          .catch((e) => {
            // 刷新失败保留上一次快照，但必须让人知道"这不是没有发现"。
            if (active()) setLoadError(e instanceof Error ? e.message : "加载失败");
          });
      };
      load();
    },
    3000,
    [taskId],
  );

  const onStatus = React.useCallback(async (f: Finding, next: FindingStatus) => {
    if (f.inherited || !f.finding_id || next === f.status) return;
    const prev = f.status;
    setFindings((cur) => cur.map((x) => (x.id === f.id ? { ...x, status: next } : x)));
    try {
      await api.setFindingStatus(f.finding_id, next);
      toast.success(`已标记为「${statusMeta("finding", next).label}」`);
    } catch (e) {
      setFindings((cur) => cur.map((x) => (x.id === f.id ? { ...x, status: prev } : x)));
      toast.error("更新失败：" + (e as Error).message);
    }
  }, []);

  const items = findings
    .filter((f) => f.task_id === taskId || f.inherited)
    .sort((a, b) => {
      const compared = Date.parse(a.ts) - Date.parse(b.ts);
      if (compared !== 0) return sortPreference.direction === "asc" ? compared : -compared;
      return b.id.localeCompare(a.id, undefined, { numeric: true });
    });

  const loadBatch = async () => {
    setBatchBusy(true);
    try {
      setBatch(await api.taskSrcBatch(taskId));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "读取交付计划失败");
    } finally {
      setBatchBusy(false);
    }
  };

  const downloadZip = async () => {
    setBatchBusy(true);
    try {
      await api.downloadTaskSrcZip(taskId);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "打包失败");
    } finally {
      setBatchBusy(false);
    }
  };

  const included = batch?.entries?.filter((e) => e.included) ?? [];
  const titleOf = (id: number) => batch?.entries?.find((e) => e.id === id)?.title ?? `#${id}`;

  return (
    <div className="flex flex-col gap-3">
      {/* 批量交付：一个任务的全部发现打成一份包（提交说明 + 每条发现的简报/docx），
          证据门与疑似重复都在提交说明里说清楚。 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <PackageIcon className="size-4 text-sky-500" /> 批量交付
            <span className="text-muted-foreground text-xs font-normal">
              （把本任务的发现打成一个包：提交说明 + 每条发现的规范简报与 docx；先看计划再下载）
            </span>
          </CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" variant="outline" disabled={batchBusy} onClick={() => void loadBatch()}>
              <ClipboardCheckIcon className="size-3.5" /> {batch ? "重新检查" : "检查交付包"}
            </Button>
            <Button size="sm" disabled={batchBusy || included.length === 0} onClick={() => void downloadZip()}>
              <DownloadIcon className="size-3.5" /> 下载交付包（zip）
            </Button>
            {batch ? (
              <span className="text-muted-foreground text-xs">
                进包 {batch.included} 条{batch.skipped > 0 ? `，跳过 ${batch.skipped} 条` : ""}
              </span>
            ) : null}
          </div>
          {batch?.warnings?.length ? (
            <div className="flex flex-col gap-1 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-xs">
              <span className="font-medium">证据门（{batch.warnings.length} 项待补，不阻断导出）：</span>
              {batch.warnings.map((w) => (
                <span key={w} className="break-all">
                  ⚠ {w}
                </span>
              ))}
            </div>
          ) : null}
          {batch?.entries?.some((e) => !e.included) ? (
            <div className="flex flex-col gap-1 rounded-md border bg-muted/40 p-2 text-xs">
              <span className="font-medium">未进包的发现（按原因）：</span>
              {batch.entries
                .filter((e) => !e.included)
                .map((e) => (
                  <span key={e.id} className="break-all">
                    #{e.id}「{e.title}」：{e.skip_reason ?? "状态不符，不进交付包"}
                  </span>
                ))}
            </div>
          ) : null}
          {batch?.duplicate_groups?.length ? (
            <div className="flex flex-col gap-1 rounded-md border border-sky-500/40 bg-sky-500/5 p-2 text-xs">
              <span className="font-medium">疑似重复（同一漏洞类型 + 同一组端点，建议只交一条）：</span>
              {batch.duplicate_groups.map((g, i) => (
                <span key={g.join("-")} className="break-all">
                  组 {i + 1}：{g.map((id) => `#${id}「${titleOf(id)}」`).join("、")}
                </span>
              ))}
            </div>
          ) : null}
        </CardContent>
      </Card>
      <Card className="overflow-hidden py-0">
        <CardContent className="px-0">
          <div className="flex items-center border-b px-4 py-2 text-xs text-muted-foreground">
            <span className="min-w-0 flex-1">漏洞</span>
            <button
              type="button"
              className="inline-flex items-center gap-1 outline-none focus-visible:underline"
              aria-label={`发现时间当前${sortPreference.direction === "asc" ? "正序" : "倒序"}，点击切换排序方向`}
              onClick={() =>
                setSortPreference((current) => ({
                  field: "time",
                  direction: current.direction === "asc" ? "desc" : "asc",
                }))
              }
            >
              <span>发现时间</span>
              {sortPreference.direction === "asc" ? (
                <ArrowUpIcon className="size-3.5" />
              ) : (
                <ArrowDownIcon className="size-3.5" />
              )}
            </button>
          </div>
          {items.map((f) => (
            <Row key={f.id} f={f} contextTaskId={taskId} onStatus={onStatus} />
          ))}
          {items.length === 0 && loadError ? (
            <p className="px-4 py-8 text-center text-sm text-red-600 dark:text-red-400">
              发现列表加载失败：{loadError}（每 3 秒自动重试）
            </p>
          ) : items.length === 0 ? (
            <p className="px-4 py-8 text-center text-sm text-muted-foreground">本任务及直接关联任务暂无确认发现。</p>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}
