"use client";

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowLeftIcon, ArrowUpRightIcon, BuildingIcon, NetworkIcon, ShieldAlertIcon } from "lucide-react";

import { CopyLinkButton } from "@/components/copy-link-button";
import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import type { AssetDetail, AssetNeighbor, AssetRef } from "@/lib/types";
import { cn } from "@/lib/utils";

import { assetExtras, assetPortStatus, assetTarget, fmtSeen, KIND_LABEL } from "../_components/asset-parts";

const RELATION_LABEL: Record<string, string> = {
  parent: "父域",
  child: "子资产",
  sibling: "同根兄弟",
  same_ip: "同 IP",
  same_csegment: "同 C 段",
};

const REF_KIND_LABEL: Record<string, string> = {
  intent: "意图",
  fact: "事实",
  finding: "发现",
};

function fmtTime(s?: string) {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString("zh-CN");
}

function neighborHref(n: AssetNeighbor, taskId: string) {
  return `/function/assets/detail?id=${n.id}${taskId ? `&task_id=${encodeURIComponent(taskId)}` : ""}`;
}

function RefRow({ item }: { item: AssetRef }) {
  return (
    <div className="flex items-start justify-between gap-3 border-b py-2 last:border-0">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge variant="outline">{REF_KIND_LABEL[item.kind] ?? item.kind}</Badge>
          <Badge variant="secondary">{item.state || "—"}</Badge>
          {item.inherited ? <Badge variant="outline">来源任务 #{item.source_task_id} · 只读</Badge> : null}
        </div>
        <p className="mt-1 line-clamp-2 text-sm" title={item.summary}>
          {item.summary || `#${item.id}`}
        </p>
      </div>
      <code className="shrink-0 text-muted-foreground text-xs">#{item.id}</code>
    </div>
  );
}

function AssetDetailInner() {
  const searchParams = useSearchParams();
  const id = searchParams.get("id") ?? "";
  const taskId = searchParams.get("task_id") ?? "";
  const [detail, setDetail] = React.useState<AssetDetail | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    if (!id) {
      setLoading(false);
      setError("缺少资产 id");
      return;
    }
    let alive = true;
    setLoading(true);
    api
      .assetDetail(id, taskId)
      .then((data) => {
        if (!alive) return;
        setDetail(data);
        setError("");
      })
      .catch((e: Error) => {
        if (!alive) return;
        setError(e.message || "加载失败");
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [id, taskId]);

  if (loading) {
    return (
      <div className="flex min-h-60 items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (!detail) {
    return (
      <Empty>
        <EmptyHeader>
          <EmptyTitle>未找到资产 {id}</EmptyTitle>
          <EmptyDescription>{error || "资产可能已被删除，或不在指定任务范围内。"}</EmptyDescription>
        </EmptyHeader>
        <Button asChild variant="outline">
          <Link href="/function/assets">
            <ArrowLeftIcon /> 返回资产列表
          </Link>
        </Button>
      </Empty>
    );
  }

  const a = detail.asset;
  const label = assetTarget(a);
  const recordValues = Array.isArray(a.record_value) ? a.record_value : a.record_value ? [a.record_value] : [];
  const openPorts = a.open_ports ?? [];

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2">
          <Button asChild variant="ghost" size="icon" className="mt-0.5 size-8 shrink-0" aria-label="返回资产列表">
            <Link href="/function/assets">
              <ArrowLeftIcon />
            </Link>
          </Button>
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <NetworkIcon className="size-4 text-muted-foreground" />
              <h1 className="truncate font-semibold text-xl tracking-tight" title={label}>
                {label}
              </h1>
              <Badge variant="secondary">{KIND_LABEL[a.type] ?? a.type}</Badge>
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-muted-foreground text-xs">#{a.id}</code>
            </div>
            <p className="mt-1 flex flex-wrap items-center gap-2 text-muted-foreground text-sm">
              <span>{assetPortStatus(a)}</span>
              <span>最后发现 {fmtSeen(a.last_seen)}</span>
              {a.status ? (
                <Badge variant={a.status === "active" ? "secondary" : "outline"}>
                  {a.status === "active" ? "活跃" : a.status === "archived" ? "已归档" : "过期"}
                </Badge>
              ) : null}
            </p>
          </div>
        </div>
        <CopyLinkButton />
      </div>

      <div className="grid gap-4 lg:grid-cols-3">
        <Card className="lg:col-span-2">
          <CardHeader>
            <CardTitle>资产信息</CardTitle>
            <CardDescription>目标的身份、归属和来源</CardDescription>
          </CardHeader>
          <CardContent className="grid gap-4 text-sm sm:grid-cols-2">
            <div>
              <div className="text-muted-foreground text-xs">目标</div>
              <div className="break-all font-mono">{label}</div>
            </div>
            <div>
              <div className="text-muted-foreground text-xs">首次发现</div>
              <div>{fmtTime(a.first_seen)}</div>
            </div>
            <div>
              <div className="text-muted-foreground text-xs">最后发现</div>
              <div>{fmtTime(a.last_seen)}</div>
            </div>
            <div>
              <div className="text-muted-foreground text-xs">生命周期</div>
              <div>
                {a.status === "active"
                  ? "活跃"
                  : a.status === "archived"
                    ? "已归档"
                    : a.status === "stale"
                      ? "过期"
                      : "—"}
              </div>
            </div>
            <div>
              <div className="text-muted-foreground text-xs">当前来源</div>
              <div className="font-mono">{a.provider || "—"}</div>
            </div>
            {a.domain ? (
              <div>
                <div className="text-muted-foreground text-xs">域名</div>
                <div className="break-all font-mono">{a.domain}</div>
              </div>
            ) : null}
            {a.root_domain ? (
              <div>
                <div className="text-muted-foreground text-xs">根域名</div>
                <div className="break-all font-mono">{a.root_domain}</div>
              </div>
            ) : null}
            {a.ip ? (
              <div>
                <div className="text-muted-foreground text-xs">IP</div>
                <div className="font-mono">{a.ip}</div>
              </div>
            ) : null}
            {a.c_segment ? (
              <div>
                <div className="text-muted-foreground text-xs">C 段</div>
                <div className="font-mono">{a.c_segment}</div>
              </div>
            ) : null}
            {a.url ? (
              <div className="sm:col-span-2">
                <div className="text-muted-foreground text-xs">URL</div>
                <div className="break-all font-mono">{a.url}</div>
              </div>
            ) : null}
            {a.page_title ? (
              <div className="sm:col-span-2">
                <div className="text-muted-foreground text-xs">页面标题</div>
                <div>{a.page_title}</div>
              </div>
            ) : null}
            {a.technologies?.length ? (
              <div className="sm:col-span-2">
                <div className="text-muted-foreground text-xs">技术栈</div>
                <div className="mt-1 flex flex-wrap gap-1">
                  {a.technologies.map((t) => (
                    <Badge key={t} variant="outline">
                      {t}
                    </Badge>
                  ))}
                </div>
              </div>
            ) : null}
            {openPorts.length ? (
              <div className="sm:col-span-2">
                <div className="text-muted-foreground text-xs">开放端口</div>
                <div className="mt-1 flex flex-wrap gap-1">
                  {openPorts.map((p) => (
                    <Badge key={`${p.port}:${p.service ?? ""}`} variant="secondary" className="font-mono">
                      {p.port}
                      {p.service ? ` · ${p.service}` : ""}
                    </Badge>
                  ))}
                </div>
              </div>
            ) : null}
            {recordValues.length ? (
              <div className="sm:col-span-2">
                <div className="text-muted-foreground text-xs">解析记录</div>
                <div className="mt-1 flex flex-wrap gap-1">
                  {recordValues.map((v) => (
                    <Badge key={v} variant="outline" className="font-mono">
                      {v}
                    </Badge>
                  ))}
                </div>
              </div>
            ) : null}
            {a.auth?.length ? (
              <div>
                <div className="text-muted-foreground text-xs">认证信息</div>
                <div>已记录 {a.auth.length} 条（不在详情页展开）</div>
              </div>
            ) : null}
            {a.task_source_summary ? (
              <div className="sm:col-span-2">
                <div className="text-muted-foreground text-xs">来源</div>
                <div>{a.task_source_summary}</div>
              </div>
            ) : null}
            {assetExtras(a) !== "—" ? (
              <div className="sm:col-span-2">
                <div className="text-muted-foreground text-xs">补充</div>
                <div className="text-muted-foreground">{assetExtras(a)}</div>
              </div>
            ) : null}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <BuildingIcon className="size-4" /> 归属与来源
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 text-sm">
            <div>
              <div className="text-muted-foreground text-xs">归属企业</div>
              <div>{detail.company?.name ?? "未归属"}</div>
            </div>
            <div>
              <div className="text-muted-foreground text-xs">来源记录</div>
              {detail.sources?.length ? (
                <div className="mt-1 flex flex-col gap-1">
                  {detail.sources.map((s) => (
                    <div key={s.source} className="rounded-md border px-2 py-1.5 text-xs">
                      <div className="flex items-center justify-between gap-2">
                        <span className="font-mono">{s.source}</span>
                        <span className="text-muted-foreground">{fmtTime(s.last_seen)}</span>
                      </div>
                      <div className="mt-0.5 text-muted-foreground">
                        首次 {fmtTime(s.first_seen)}
                        {s.last_sync_batch_id ? ` · 批次 #${s.last_sync_batch_id}` : ""}
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="text-muted-foreground">暂无来源记录</div>
              )}
            </div>
            <div>
              <div className="text-muted-foreground text-xs">来源任务</div>
              {detail.tasks.length ? (
                <div className="mt-1 flex flex-col gap-1">
                  {detail.tasks.map((t) => (
                    <Link
                      key={t.id}
                      href={`/function/tasks/detail?id=${encodeURIComponent(t.id)}`}
                      className="flex items-center justify-between gap-2 rounded-md border px-2 py-1.5 hover:bg-muted/50"
                    >
                      <span className="min-w-0 truncate" title={t.name || t.description}>
                        {t.name || t.description || `任务 #${t.id}`}
                      </span>
                      <ArrowUpRightIcon className="size-3.5 shrink-0 text-muted-foreground" />
                    </Link>
                  ))}
                </div>
              ) : (
                <div className="text-muted-foreground">未关联任务</div>
              )}
            </div>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>直接邻域</CardTitle>
          <CardDescription>父域、子资产、同根兄弟、同 IP 与同 C 段；点进去继续调查</CardDescription>
        </CardHeader>
        <CardContent>
          {detail.neighbors.length ? (
            <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {detail.neighbors.map((n) => (
                <Link
                  key={`${n.relation}:${n.id}`}
                  href={neighborHref(n, taskId)}
                  className="flex min-w-0 items-center justify-between gap-2 rounded-md border px-3 py-2 hover:bg-muted/50"
                >
                  <span className="min-w-0">
                    <span className="block truncate font-mono text-xs" title={n.label}>
                      {n.label}
                    </span>
                    <span className="text-muted-foreground text-xs">
                      {RELATION_LABEL[n.relation] ?? n.relation} · {KIND_LABEL[n.type] ?? n.type}
                    </span>
                  </span>
                  <ArrowUpRightIcon className="size-3.5 shrink-0 text-muted-foreground" />
                </Link>
              ))}
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">没有直接邻域记录。</p>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <ShieldAlertIcon className="size-4" /> 关联发现
              <span className="text-muted-foreground text-sm tabular-nums">({detail.findings_total})</span>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {detail.findings.length ? (
              detail.findings.map((f) => (
                <Link
                  key={f.finding_id ?? f.id}
                  href={`/function/findings/detail?id=${encodeURIComponent(f.finding_id ?? f.id)}`}
                  className="flex min-w-0 items-start justify-between gap-2 rounded-md border px-3 py-2 hover:bg-muted/50"
                >
                  <span className="min-w-0">
                    <span className="block truncate font-medium text-sm">{f.name || f.vulnclass || "未分类"}</span>
                    <span className="line-clamp-1 text-muted-foreground text-xs">{f.summary}</span>
                  </span>
                  <span className="flex shrink-0 items-center gap-1">
                    <StatusBadge domain="severity" value={f.severity} dot />
                    <StatusBadge domain="finding" value={f.status} dot />
                  </span>
                </Link>
              ))
            ) : (
              <p className="text-muted-foreground text-sm">该资产暂无关联发现。</p>
            )}
            {detail.findings_total > detail.findings.length ? (
              <Button asChild size="sm" variant="outline" className="self-start">
                <Link href={`/function/findings?view=asset&asset=${encodeURIComponent(`a:${a.id}`)}`}>
                  查看全部 {detail.findings_total} 条
                </Link>
              </Button>
            ) : null}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>图引用</CardTitle>
            <CardDescription>黑板中锚定到该资产的意图、事实与发现</CardDescription>
          </CardHeader>
          <CardContent>
            {detail.refs.length ? (
              detail.refs.map((item) => <RefRow key={item.id} item={item} />)
            ) : (
              <p className="text-muted-foreground text-sm">黑板中没有锚定到该资产的节点。</p>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

export default function AssetDetailPage() {
  return (
    <React.Suspense
      fallback={
        <div className={cn("flex min-h-60 items-center justify-center")}>
          <Spinner />
        </div>
      }
    >
      <AssetDetailInner />
    </React.Suspense>
  );
}
