"use client";

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowUpRightIcon, BuildingIcon, NetworkIcon } from "lucide-react";

import { CopyLinkButton } from "@/components/copy-link-button";
import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { CompanyOverview } from "@/lib/types";

const ASSET_LABELS: Record<string, string> = {
  company: "企业",
  root_domain: "根域名",
  ip: "IP",
  subdomain: "子域名",
  app: "应用",
  service: "服务",
  endpoint: "接口",
};

const LOOT_LABELS: Record<string, string> = {
  foothold: "据点",
  credential: "凭据",
  artifact: "遗留物",
  footprint: "痕迹",
};

function fmtTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

function Stat({ label, value, sub }: { label: string; value: React.ReactNode; sub?: string }) {
  return (
    <Card className="gap-1 py-3">
      <CardHeader className="px-4 pb-0">
        <CardDescription className="text-xs">{label}</CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      {sub ? <CardContent className="px-4 text-muted-foreground text-xs">{sub}</CardContent> : null}
    </Card>
  );
}

function CompanyOverviewInner() {
  const searchParams = useSearchParams();
  const id = searchParams.get("id") ?? "";
  const [data, setData] = React.useState<CompanyOverview | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    if (!id) {
      setLoading(false);
      setError("缺少公司 id");
      return;
    }
    let alive = true;
    api
      .companyOverview(Number(id))
      .then((next) => {
        if (alive) {
          setData(next);
          setError("");
        }
      })
      .catch((e: Error) => {
        if (alive) setError(e.message || "加载失败");
      })
      .finally(() => {
        if (alive) setLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [id]);

  if (loading) {
    return (
      <div className="flex min-h-60 items-center justify-center">
        <Spinner />
      </div>
    );
  }
  if (!data) {
    return (
      <Empty>
        <EmptyHeader>
          <EmptyTitle>未找到公司 {id}</EmptyTitle>
          <EmptyDescription>{error || "公司可能已被删除。"}</EmptyDescription>
        </EmptyHeader>
        <Button asChild variant="outline">
          <Link href="/function/assets">返回资产页</Link>
        </Button>
      </Empty>
    );
  }

  const c = data.company;
  const assetEntries = Object.entries(data.asset_counts)
    .filter(([, n]) => n > 0)
    .sort(([, a], [, b]) => b - a);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <BuildingIcon className="size-5 text-muted-foreground" />
            <h1 className="truncate font-semibold text-xl tracking-tight">{c.name}</h1>
            <Badge variant="outline">{c.asset_count} 项资产</Badge>
          </div>
          <div className="mt-2 flex flex-wrap gap-1">
            {data.scope.length === 0 ? (
              <span className="text-muted-foreground text-xs">未设置资产范围</span>
            ) : (
              data.scope.map((s) => (
                <Badge key={s.id} variant="secondary" className="font-mono text-[10px]">
                  {s.raw}
                </Badge>
              ))
            )}
          </div>
        </div>
        <CopyLinkButton />
      </div>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
        <Stat label="资产" value={data.asset_total} />
        <Stat
          label="任务"
          value={data.tasks.length}
          sub={`${data.tasks.filter((t) => t.status === "running").length} 个运行中`}
        />
        <Stat
          label="发现"
          value={data.findings.total}
          sub={`严重 ${data.findings.critical} · 高危 ${data.findings.high}`}
        />
        <Stat label="战果" value={data.loot_total} sub={`凭据 ${data.loot_by_kind.credential ?? 0}`} />
        <Stat label="凭据尝试" value={data.credential_attempts} />
      </div>

      <Tabs defaultValue="overview" className="flex flex-col gap-3">
        <div className="overflow-x-auto overflow-y-hidden">
          <TabsList className="w-max">
            <TabsTrigger value="overview">概览</TabsTrigger>
            <TabsTrigger value="assets">资产分布</TabsTrigger>
            <TabsTrigger value="tasks">任务</TabsTrigger>
            <TabsTrigger value="findings">发现</TabsTrigger>
            <TabsTrigger value="loot">战果</TabsTrigger>
            <TabsTrigger value="knowledge">知识</TabsTrigger>
            <TabsTrigger value="deliverables">交付物</TabsTrigger>
            <TabsTrigger value="clues">线索</TabsTrigger>
            <TabsTrigger value="activity">活动</TabsTrigger>
          </TabsList>
        </div>

        <TabsContent value="overview" className="mt-0 grid gap-4 lg:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">资产分布</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-wrap gap-2">
              {assetEntries.length === 0 ? (
                <span className="text-muted-foreground text-sm">暂无资产</span>
              ) : (
                assetEntries.map(([type, n]) => (
                  <Badge key={type} variant="outline">
                    {ASSET_LABELS[type] ?? type} {n}
                  </Badge>
                ))
              )}
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">发现严重度</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-wrap gap-3 text-sm">
              <span className="text-rose-500">严重 {data.findings.critical}</span>
              <span className="text-red-400">高危 {data.findings.high}</span>
              <span className="text-amber-400">中危 {data.findings.medium}</span>
              <span className="text-slate-400">低危 {data.findings.low}</span>
            </CardContent>
          </Card>
          <Card className="lg:col-span-2">
            <CardHeader>
              <CardTitle className="text-sm">最近发现</CardTitle>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              {data.recent_findings.length === 0 ? (
                <span className="text-muted-foreground text-sm">暂无发现</span>
              ) : (
                data.recent_findings.slice(0, 8).map((f) => (
                  <Link
                    key={f.finding_id ?? f.id}
                    href={`/function/findings/detail?id=${encodeURIComponent(f.finding_id ?? f.id)}`}
                    className="flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5 hover:bg-muted/50"
                  >
                    <span className="min-w-0 truncate text-sm">{f.name || f.vulnclass || "未分类"}</span>
                    <span className="flex shrink-0 items-center gap-1">
                      <StatusBadge domain="severity" value={f.severity} dot />
                      <StatusBadge domain="finding" value={f.status} dot />
                    </span>
                  </Link>
                ))
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="assets" className="mt-0">
          <Card>
            <CardContent className="grid gap-3 pt-4 sm:grid-cols-2 lg:grid-cols-4">
              {assetEntries.length === 0 ? (
                <span className="text-muted-foreground text-sm">暂无资产</span>
              ) : (
                assetEntries.map(([type, n]) => (
                  <div key={type} className="rounded-md border px-3 py-2">
                    <div className="text-muted-foreground text-xs">{ASSET_LABELS[type] ?? type}</div>
                    <div className="font-semibold text-xl tabular-nums">{n}</div>
                  </div>
                ))
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="tasks" className="mt-0">
          <Card className="py-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>任务</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-20 text-right">发现</TableHead>
                  <TableHead className="w-20 text-right">据点</TableHead>
                  <TableHead className="w-20 text-right">凭据</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.tasks.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="py-10 text-center text-muted-foreground">
                      暂无关联任务
                    </TableCell>
                  </TableRow>
                ) : (
                  data.tasks.map((t) => (
                    <TableRow key={t.id}>
                      <TableCell>
                        <Link
                          href={`/function/tasks/detail?id=${t.id}`}
                          className="font-medium hover:text-primary hover:underline"
                        >
                          {t.name || t.description || `任务 #${t.id}`}
                        </Link>
                        <div className="text-muted-foreground text-xs">#{t.id}</div>
                      </TableCell>
                      <TableCell>
                        <StatusBadge domain="task" value={t.status} dot />
                      </TableCell>
                      <TableCell className="text-right text-sm tabular-nums">{t.findings}</TableCell>
                      <TableCell className="text-right text-sm tabular-nums">{t.footholds}</TableCell>
                      <TableCell className="text-right text-sm tabular-nums">{t.credentials}</TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>

        <TabsContent value="findings" className="mt-0">
          <Card className="py-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>发现</TableHead>
                  <TableHead className="w-24">严重度</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-32">发现时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.recent_findings.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={4} className="py-10 text-center text-muted-foreground">
                      暂无发现
                    </TableCell>
                  </TableRow>
                ) : (
                  data.recent_findings.map((f) => (
                    <TableRow key={f.finding_id ?? f.id}>
                      <TableCell>
                        <Link
                          href={`/function/findings/detail?id=${encodeURIComponent(f.finding_id ?? f.id)}`}
                          className="font-medium hover:text-primary hover:underline"
                        >
                          {f.name || f.vulnclass || "未分类"}
                        </Link>
                        <div className="line-clamp-1 text-muted-foreground text-xs">{f.summary}</div>
                      </TableCell>
                      <TableCell>
                        <StatusBadge domain="severity" value={f.severity} dot />
                      </TableCell>
                      <TableCell>
                        <StatusBadge domain="finding" value={f.status} dot />
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(f.ts)}</TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>

        <TabsContent value="loot" className="mt-0">
          <Card>
            <CardContent className="grid gap-3 pt-4 sm:grid-cols-2 lg:grid-cols-4">
              {["foothold", "credential", "artifact", "footprint"].map((kind) => (
                <div key={kind} className="rounded-md border px-3 py-2">
                  <div className="text-muted-foreground text-xs">{LOOT_LABELS[kind] ?? kind}</div>
                  <div className="font-semibold text-xl tabular-nums">{data.loot_by_kind[kind] ?? 0}</div>
                </div>
              ))}
              <div className="rounded-md border px-3 py-2">
                <div className="text-muted-foreground text-xs">凭据尝试</div>
                <div className="font-semibold text-xl tabular-nums">{data.credential_attempts}</div>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="knowledge" className="mt-0">
          <Card>
            <CardContent className="flex flex-col gap-2 pt-4">
              {data.knowledge.length === 0 ? (
                <span className="text-muted-foreground text-sm">暂无公司知识</span>
              ) : (
                data.knowledge.map((k) => (
                  <div key={k.id} className="rounded-md border px-3 py-2">
                    <div className="flex items-center gap-2">
                      <Badge variant="outline">{k.kind}</Badge>
                      {k.status === "deprecated" ? <Badge variant="secondary">已废弃</Badge> : null}
                    </div>
                    <div className="mt-1 text-sm">{k.summary}</div>
                    {k.detail ? <div className="mt-1 text-muted-foreground text-xs">{k.detail}</div> : null}
                  </div>
                ))
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="deliverables" className="mt-0">
          <Card className="py-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>交付物</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-24 text-right">版本</TableHead>
                  <TableHead className="w-24 text-right">签收</TableHead>
                  <TableHead className="w-32">更新时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.deliverables.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="py-10 text-center text-muted-foreground">
                      暂无交付物
                    </TableCell>
                  </TableRow>
                ) : (
                  data.deliverables.map((d) => (
                    <TableRow key={d.id}>
                      <TableCell>
                        <Link href="/function/deliverables" className="font-medium hover:text-primary hover:underline">
                          {d.name}
                        </Link>
                        <div className="text-muted-foreground text-xs">#{d.id}</div>
                      </TableCell>
                      <TableCell>
                        <StatusBadge domain="delivery" value={d.status} />
                      </TableCell>
                      <TableCell className="text-right text-sm tabular-nums">v{d.latest_version}</TableCell>
                      <TableCell className="text-right text-sm tabular-nums">{d.acceptance_count}</TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(d.updated_at)}</TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>

        <TabsContent value="clues" className="mt-0 grid gap-4 lg:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">CVE 线索</CardTitle>
              <CardDescription>按公司资产指纹匹配的本地 CVE</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              {data.cves.length === 0 ? (
                <span className="text-muted-foreground text-sm">暂无匹配</span>
              ) : (
                data.cves.map((item) => (
                  <Link
                    key={item.cve_id}
                    href={`/function/cve?q=${encodeURIComponent(item.cve_id)}`}
                    className="flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5 hover:bg-muted/50"
                  >
                    <span className="min-w-0">
                      <span className="block truncate text-sm">
                        {item.cve_id} · {item.title}
                      </span>
                      <span className="text-muted-foreground text-xs">
                        匹配 {item.asset_label || "—"} · score {item.score}
                      </span>
                    </span>
                    <StatusBadge domain="severity" value={item.severity} dot />
                  </Link>
                ))
              )}
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">打法线索</CardTitle>
              <CardDescription>按公司任务指纹匹配的历史打法</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-2">
              {data.playbooks.length === 0 ? (
                <span className="text-muted-foreground text-sm">暂无匹配</span>
              ) : (
                data.playbooks.map((item) => (
                  <Link
                    key={item.id}
                    href={`/function/playbooks?q=${encodeURIComponent(item.title)}`}
                    className="flex items-center justify-between gap-2 rounded-md border px-2.5 py-1.5 hover:bg-muted/50"
                  >
                    <span className="min-w-0">
                      <span className="block truncate text-sm">{item.title}</span>
                      <span className="line-clamp-1 text-muted-foreground text-xs">{item.summary}</span>
                    </span>
                    <Badge variant="outline">score {item.score}</Badge>
                  </Link>
                ))
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="activity" className="mt-0">
          <Card>
            <CardContent className="flex flex-col gap-2 pt-4">
              {data.activity.length === 0 ? (
                <span className="text-muted-foreground text-sm">暂无活动</span>
              ) : (
                data.activity.map((a) => (
                  <div key={a.id} className="flex items-start justify-between gap-2 rounded-md border px-2.5 py-1.5">
                    <div className="min-w-0">
                      <div className="line-clamp-1 text-sm">{a.summary}</div>
                      <div className="text-muted-foreground text-xs">
                        {a.task_name || `任务 #${a.task_id}`} · {a.worker || a.kind}
                        {a.tool ? ` · ${a.tool}` : ""}
                      </div>
                    </div>
                    <span className="shrink-0 text-muted-foreground text-xs">{fmtTime(a.created_at)}</span>
                  </div>
                ))
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}

export default function CompanyDetailPage() {
  return (
    <React.Suspense
      fallback={
        <div className="flex min-h-60 items-center justify-center">
          <Spinner />
        </div>
      }
    >
      <CompanyOverviewInner />
    </React.Suspense>
  );
}
