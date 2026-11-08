"use client";

import { useFindingTriage } from "@/components/finding-triage-dialog";

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
  SearchIcon,
} from "lucide-react";
import { toast } from "sonner";

import { FindingCaseList } from "@/components/finding-case-list";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { usePolling } from "@/hooks/use-polling";
import { api } from "@/lib/api";
import { filterAndSortFindings, type FindingSortField } from "@/lib/finding-severity";
import { useStoredSortPreference } from "@/lib/sort-preference";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingStatus, Severity, TaskSrcBatchPlan } from "@/lib/types";
import { cn } from "@/lib/utils";

const FINDING_SORT_FIELDS: readonly FindingSortField[] = ["time", "severity"];
// 发现列表按页展示（与播报板一致）：长任务几百条发现时，全量渲染既卡又找不到东西。
const PAGE_SIZES = [20, 50, 100];
const DEFAULT_PAGE_SIZE = 20;
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
            <span className="truncate text-muted-foreground text-xs">{f.summary}</span>
            {(f.status === "ruled_out" || f.status === "open_proof_gap") && (f.counterevidence || f.proof_gap) && (
              <span
                className="line-clamp-2 text-amber-600 text-xs dark:text-amber-400"
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
            {f.assets.length > 2 && <span className="text-muted-foreground text-xs">+{f.assets.length - 2}</span>}
          </div>
        )}
        <span className="hidden shrink-0 text-muted-foreground text-xs md:block">
          {new Date(f.ts).toLocaleString("zh-CN")}
        </span>
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
        {f.finding_id && (
          <Link
            href={
              f.inherited
                ? `/function/findings/detail?id=${f.finding_id}&context_task=${contextTaskId}`
                : `/function/findings/detail?id=${f.finding_id}`
            }
            className="inline-flex shrink-0 items-center gap-0.5 text-muted-foreground text-xs hover:text-primary"
            title="查看漏洞详情"
          >
            详情
            <ArrowUpRightIcon className="size-3" />
          </Link>
        )}
      </div>
      {open && (
        <div className="bg-muted/30 px-4 pb-4 pl-11">
          <div className="mb-1 font-medium text-muted-foreground text-xs">证据 / PoC</div>
          <pre className="overflow-auto whitespace-pre-wrap rounded-md border bg-background p-3 font-mono text-xs">
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
  const [severity, setSeverity] = React.useState<"all" | Severity>("all");
  const [search, setSearch] = React.useState("");
  const [query, setQuery] = React.useState("");
  // 分页：接口一次返回全量（含直接关联任务），在客户端切片。page 为 1-based。
  const [page, setPage] = React.useState(1);
  const [size, setSize] = React.useState(DEFAULT_PAGE_SIZE);
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
    (active) =>
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
          if (active()) {
            setLoadError(e instanceof Error ? e.message : "加载失败");
            throw e; // 让 usePolling 退避，而不是把失败当成功
          }
        }),
    3000,
    [taskId],
  );

  const triage = useFindingTriage((f, updated) => {
    setFindings((cur) => cur.map((x) => (x.id === f.id ? { ...x, ...updated } : x)));
  });
  const onStatus = triage.openTriage;

  const taskFindings = findings.filter((f) => f.task_id === taskId || f.inherited);
  const items = filterAndSortFindings(taskFindings, severity, sortPreference, query);

  const pageCount = Math.max(1, Math.ceil(items.length / size));
  // 条数变少（撤回/换筛选/换任务）时把越界页码收回来，否则会停在空白页。
  React.useEffect(() => {
    if (page > pageCount) setPage(pageCount);
  }, [page, pageCount]);
  // 换排序方向 = 换了一屏内容，回到第一页。
  // biome-ignore lint/correctness/useExhaustiveDependencies: 只关心排序方向变化，重置页码时不该重跑。
  React.useEffect(() => {
    setPage(1);
  }, [sortPreference.direction]);
  const pageItems = items.slice((page - 1) * size, page * size);

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
      {triage.dialog}
      {/* 批量交付：一个任务的全部发现打成一份包（提交说明 + 每条发现的简报/docx），
          证据门与疑似重复都在提交说明里说清楚。 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <PackageIcon className="size-4 text-sky-500" /> 批量交付
            <span className="font-normal text-muted-foreground text-xs">
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
      <div className="flex flex-wrap items-center gap-2">
        <ToggleGroup
          type="single"
          value={severity}
          onValueChange={(value) => value && setSeverity(value as "all" | Severity)}
          variant="outline"
          size="sm"
          spacing={0}
        >
          {(
            [
              ["all", "全部"],
              ["critical", "严重"],
              ["high", "高危"],
              ["medium", "中危"],
              ["low", "低危"],
            ] as const
          ).map(([value, label]) => (
            <ToggleGroupItem key={value} value={value} aria-label={`按${label}等级筛选`}>
              {label}
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <Select
          value={sortPreference.field}
          onValueChange={(field) => {
            if (field === "time" || field === "severity") {
              setSortPreference((current) => ({ ...current, field }));
            }
          }}
        >
          <SelectTrigger size="sm" className="w-36" aria-label="发现排序方式">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="time">按时间排序</SelectItem>
            <SelectItem value="severity">按严重度排序</SelectItem>
          </SelectContent>
        </Select>
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label={`发现时间当前${sortPreference.direction === "asc" ? "正序" : "倒序"}，点击切换排序方向`}
          onClick={() =>
            setSortPreference((current) => ({
              ...current,
              direction: current.direction === "asc" ? "desc" : "asc",
            }))
          }
        >
          {sortPreference.direction === "asc" ? <ArrowUpIcon /> : <ArrowDownIcon />}
          {sortPreference.direction === "asc" ? "最早优先" : "最新优先"}
        </Button>
        <form
          className="flex w-full items-center gap-2 sm:w-auto"
          onSubmit={(event) => {
            event.preventDefault();
            setQuery(search.trim());
            setPage(1);
          }}
        >
          <InputGroup className="flex-1 sm:w-72">
            <InputGroupInput
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="检索漏洞内容"
              aria-label="检索漏洞内容"
            />
            <InputGroupAddon>
              <SearchIcon aria-hidden="true" />
            </InputGroupAddon>
          </InputGroup>
          <Button type="submit" size="sm">
            搜索
          </Button>
        </form>
      </div>
      <FindingCaseList
        query={{
          task: taskId,
          severity,
          query: query || undefined,
          sort: sortPreference.field,
          timeDirection: sortPreference.direction,
        }}
        contextTask={taskId}
      />
      <details>
        <summary className="cursor-pointer text-muted-foreground text-sm">查看原始上报列表</summary>
        <Card className="overflow-hidden py-0">
          <CardContent className="px-0">
            <div className="flex items-center border-b px-4 py-2 text-muted-foreground text-xs">
              <span className="min-w-0 flex-1">
                漏洞
                {items.length > 0 && (
                  <span className="ml-1.5 text-muted-foreground tabular-nums">（{items.length} 条）</span>
                )}
              </span>
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
            {pageItems.map((f) => (
              <Row key={f.id} f={f} contextTaskId={taskId} onStatus={onStatus} />
            ))}
            {items.length === 0 && loadError ? (
              <p className="px-4 py-8 text-center text-red-600 text-sm dark:text-red-400">
                发现列表加载失败：{loadError}（每 3 秒自动重试）
              </p>
            ) : items.length === 0 ? (
              <p className="px-4 py-8 text-center text-muted-foreground text-sm">本任务及直接关联任务暂无确认发现。</p>
            ) : null}
          </CardContent>
          {items.length > 0 && (
            <CardFooter className="flex flex-wrap items-center gap-2 border-t px-4 py-2.5 text-muted-foreground text-xs">
              <TablePagination
                variant="compact"
                page={page}
                pageSize={size}
                total={items.length}
                onPageChange={setPage}
                onPageSizeChange={(s) => {
                  setSize(s);
                  setPage(1);
                }}
                pageSizeOptions={PAGE_SIZES}
              />
            </CardFooter>
          )}
        </Card>
      </details>
    </div>
  );
}
