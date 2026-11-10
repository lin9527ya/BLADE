"use client";

import * as React from "react";

import Link from "next/link";

import { BugIcon, ChevronRightIcon, FolderIcon } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import { findingCandidateStatus } from "@/lib/finding-status";
import type { Finding, FindingCase, FindingCaseListRow, FindingQuery } from "@/lib/types";
import { cn } from "@/lib/utils";

function findingLabel(f: Finding): string {
  return f.name?.trim() || f.vulnclass?.trim() || "未分类";
}

function findingKey(f: Finding): string {
  return f.finding_id ?? f.id;
}

function MemberRow({ finding, contextTask }: { finding: Finding; contextTask?: string }) {
  const id = findingKey(finding);
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2 border-b px-4 py-2.5 text-sm last:border-b-0">
      <StatusBadge domain="severity" value={finding.severity} />
      <Link
        href={`/function/findings/detail?id=${id}${contextTask ? `&context_task=${contextTask}` : ""}`}
        className="min-w-0 flex-1 truncate font-medium hover:underline"
        title={finding.summary}
      >
        #{id} {findingLabel(finding)}
      </Link>
      <StatusBadge domain="finding" value={finding.status} dot />
    </div>
  );
}

function CaseRow({
  row,
  contextTask,
  expanded,
  detail,
  loading,
  onToggle,
}: {
  row: FindingCaseListRow;
  contextTask?: string;
  expanded: boolean;
  detail?: FindingCase;
  loading: boolean;
  onToggle: () => void;
}) {
  const c = row.case;
  if (!c) return null;
  const members = (detail?.members ?? []).filter((m) => !findingCandidateStatus(m.status));
  return (
    <div className="border-b last:border-b-0">
      <div className="flex min-w-0 flex-wrap items-center gap-2 px-4 py-3 text-sm">
        <button type="button" className="flex min-w-0 flex-1 items-center gap-2 text-left" onClick={onToggle}>
          <ChevronRightIcon
            className={cn("size-4 shrink-0 text-muted-foreground transition-transform", expanded && "rotate-90")}
          />
          <FolderIcon className="size-4 shrink-0 text-amber-500" />
          <span className="min-w-0 flex-1 truncate font-medium">{c.title}</span>
        </button>
        {c.severity ? <StatusBadge domain="severity" value={c.severity} /> : null}
        <Badge variant="outline">{c.member_count} 条上报</Badge>
        <Badge variant={c.report && c.report_version === c.version ? "secondary" : "outline"}>
          {!c.report ? "统一报告未生成" : c.report_version === c.version ? "统一报告已更新" : "统一报告待更新"}
        </Badge>
        <span className="text-muted-foreground text-xs tabular-nums">#{c.id}</span>
        <span className="text-muted-foreground text-xs">{new Date(c.updated_at).toLocaleString("zh-CN")}</span>
      </div>
      {c.reason ? <p className="px-10 pb-2 text-muted-foreground text-xs">{c.reason}</p> : null}
      {expanded ? (
        <div className="bg-muted/30 pl-10">
          {loading ? (
            <div className="flex justify-center p-4">
              <Spinner />
            </div>
          ) : members.length > 0 ? (
            members.map((m) => <MemberRow key={findingKey(m)} finding={m} contextTask={contextTask} />)
          ) : (
            <p className="px-4 py-3 text-muted-foreground text-xs">案例暂无已确认的成员漏洞。</p>
          )}
        </div>
      ) : null}
    </div>
  );
}

function FindingRow({ row, contextTask }: { row: FindingCaseListRow; contextTask?: string }) {
  const f = row.finding;
  if (!f) return null;
  const id = findingKey(f);
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-2 border-b px-4 py-3 text-sm last:border-b-0">
      <BugIcon className="size-4 shrink-0 text-rose-500" />
      <StatusBadge domain="severity" value={f.severity} />
      <Link
        href={`/function/findings/detail?id=${id}${contextTask ? `&context_task=${contextTask}` : ""}`}
        className="min-w-0 flex-1 truncate font-medium hover:underline"
        title={f.summary}
      >
        #{id} {findingLabel(f)}
      </Link>
      <span className="text-muted-foreground text-xs">{new Date(f.ts).toLocaleString("zh-CN")}</span>
      <StatusBadge domain="finding" value={f.status} dot />
      <Button asChild variant="ghost" size="sm">
        <Link href={`/function/findings/detail?id=${id}${contextTask ? `&context_task=${contextTask}` : ""}`}>
          查看报告
        </Link>
      </Button>
    </div>
  );
}

// FindingCaseList 是「按漏洞」视图：案例文件夹 + 未归并发现混排。
// 查询与全局发现页共用筛选条件；后端默认隐藏 ruled_out/open_proof_gap。
export function FindingCaseList({
  query,
  onTotal,
  contextTask,
}: {
  query: Omit<FindingQuery, "page" | "pageSize">;
  onTotal?: (total: number) => void;
  contextTask?: string;
}) {
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [rows, setRows] = React.useState<FindingCaseListRow[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [expanded, setExpanded] = React.useState<number | null>(null);
  const [details, setDetails] = React.useState<Record<number, FindingCase>>({});
  const [detailLoading, setDetailLoading] = React.useState(false);
  const fingerprint = JSON.stringify(query);
  const queryRef = React.useRef(query);
  queryRef.current = query;
  const onTotalRef = React.useRef(onTotal);
  onTotalRef.current = onTotal;

  React.useEffect(() => {
    setPage(1);
    setExpanded(null);
  }, [fingerprint]);

  React.useEffect(() => {
    let alive = true;
    setLoading(true);
    setError("");
    api
      .findingCaseRows({ ...queryRef.current, page, pageSize })
      .then((r) => {
        if (!alive) return;
        setRows(r.items);
        setTotal(r.total);
        onTotalRef.current?.(r.total);
      })
      .catch((e) => {
        if (alive) setError(e instanceof Error ? e.message : "加载失败");
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [fingerprint, page, pageSize]);

  async function toggleCase(id: number) {
    if (expanded === id) {
      setExpanded(null);
      return;
    }
    setExpanded(id);
    if (details[id]) return;
    setDetailLoading(true);
    try {
      const c = await api.getFindingCase(id);
      setDetails((current) => ({ ...current, [id]: c }));
    } catch {
      /* keep the folder collapsed; a later click retries */
    } finally {
      setDetailLoading(false);
    }
  }

  if (loading && rows.length === 0) {
    return (
      <div className="flex min-h-36 items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (error && rows.length === 0) {
    return (
      <Card>
        <CardContent className="py-10 text-center text-destructive text-sm">{error}</CardContent>
      </Card>
    );
  }
  return (
    <Card className="overflow-hidden py-0">
      <CardContent className="px-0">
        {rows.length === 0 ? (
          <p className="px-4 py-10 text-center text-muted-foreground text-sm">当前筛选下暂无已确认漏洞。</p>
        ) : (
          rows.map((row) =>
            row.case ? (
              <CaseRow
                key={`c:${row.case.id}`}
                row={row}
                contextTask={contextTask}
                expanded={expanded === row.case.id}
                detail={details[row.case.id]}
                loading={detailLoading && expanded === row.case.id}
                onToggle={() => void toggleCase(row.case!.id)}
              />
            ) : (
              <FindingRow key={`f:${row.finding?.id}`} row={row} contextTask={contextTask} />
            ),
          )
        )}
      </CardContent>
      {total > pageSize ? (
        <div className="border-t px-4 py-2">
          <TablePagination
            variant="compact"
            page={page}
            pageSize={pageSize}
            total={total}
            onPageChange={setPage}
            onPageSizeChange={(next) => {
              setPageSize(next);
              setPage(1);
            }}
            pageSizeOptions={[10, 20, 50]}
          />
        </div>
      ) : null}
    </Card>
  );
}
