"use client";

import * as React from "react";
import Link from "next/link";

import { DownloadIcon, KeyRoundIcon, RefreshCwIcon, TrophyIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { Campaign, Company, CredentialAttempt, LootItem } from "@/lib/types";

const KIND_LABEL: Record<string, string> = {
  foothold: "据点",
  credential: "凭据",
  artifact: "遗留物",
  footprint: "痕迹",
};

function fmtTime(ts?: string): string {
  if (!ts) return "—";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

function targetText(it: LootItem): string {
  if (it.kind === "credential") {
    const ts = it.targets ?? [];
    if (ts.length === 0) return "—";
    return ts
      .map((t) => `${t.host}${t.port ? `:${t.port}` : ""}${t.service ? `（${t.service}）` : ""}${t.verified ? " ✓" : ""}`)
      .join("、");
  }
  if (it.kind === "foothold") {
    const host = it.host ? `${it.host}${it.port ? `:${it.port}` : ""}` : it.url ?? "—";
    return [host, it.protocol, it.access, it.privilege].filter(Boolean).join(" · ");
  }
  if (it.kind === "artifact") {
    return [it.host, it.locator, it.url].filter(Boolean).join(" · ") || "—";
  }
  return [it.host, it.where, it.category, it.exposure].filter(Boolean).join(" · ") || "—";
}

export default function LootPage() {
  const [kind, setKind] = React.useState("all");
  const [state, setState] = React.useState("");
  const [task, setTask] = React.useState("");
  const [company, setCompany] = React.useState(0);
  const [campaign, setCampaign] = React.useState(0);
  const [page, setPage] = React.useState(1);
  const [items, setItems] = React.useState<LootItem[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [campaigns, setCampaigns] = React.useState<Campaign[]>([]);

  const [attemptTask, setAttemptTask] = React.useState("");
  const [attemptHost, setAttemptHost] = React.useState("");
  const [attemptAccount, setAttemptAccount] = React.useState("");
  const [attemptPage, setAttemptPage] = React.useState(1);
  const [attempts, setAttempts] = React.useState<CredentialAttempt[]>([]);
  const [attemptTotal, setAttemptTotal] = React.useState(0);
  const [attemptLoading, setAttemptLoading] = React.useState(true);

  React.useEffect(() => {
    void api.companies().then(setCompanies).catch(() => {});
    void api.campaigns().then(setCampaigns).catch(() => {});
  }, []);

  const loadLoot = React.useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const r = await api.loot({ kind, state, task: task.trim(), company, campaign, page, size: 50 });
      setItems(r.items);
      setTotal(r.total);
    } catch (e) {
      setError(e instanceof Error ? e.message : "读取战果台账失败");
    } finally {
      setLoading(false);
    }
  }, [kind, state, task, company, campaign, page]);

  React.useEffect(() => {
    void loadLoot();
  }, [loadLoot]);

  const loadAttempts = React.useCallback(async () => {
    setAttemptLoading(true);
    try {
      const r = await api.credentialAttempts({
        task: attemptTask.trim(),
        host: attemptHost.trim(),
        account: attemptAccount.trim(),
        page: attemptPage,
        size: 50,
      });
      setAttempts(r.items);
      setAttemptTotal(r.total);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "读取凭据尝试失败");
    } finally {
      setAttemptLoading(false);
    }
  }, [attemptTask, attemptHost, attemptAccount, attemptPage]);

  React.useEffect(() => {
    void loadAttempts();
  }, [loadAttempts]);

  async function exportLoot(format: "csv" | "json") {
    try {
      await api.lootExport({ kind, state, task: task.trim(), company, campaign }, format);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "导出失败");
    }
  }

  async function exportAttempts(format: "csv" | "json") {
    try {
      await api.credentialAttemptsExport(
        { task: attemptTask.trim(), host: attemptHost.trim(), account: attemptAccount.trim() },
        format,
      );
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "导出失败");
    }
  }

  const lootPages = Math.max(1, Math.ceil(total / 50));
  const attemptPages = Math.max(1, Math.ceil(attemptTotal / 50));

  return (
    <div className="flex flex-col gap-4 p-4 md:p-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl">
            <TrophyIcon className="size-5" /> 战果台账
          </h1>
          <p className="mt-1 text-muted-foreground text-sm">
            跨任务查看据点、凭据、遗留物、目标侧痕迹与账号尝试。凭据只显示身份与指纹，明文需在任务内按需 reveal。
          </p>
        </div>
      </div>

      <Tabs defaultValue="loot">
        <TabsList>
          <TabsTrigger value="loot">战果</TabsTrigger>
          <TabsTrigger value="attempts">账号尝试</TabsTrigger>
        </TabsList>

        <TabsContent value="loot" className="mt-3 flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <NativeSelect size="sm" value={kind} onChange={(e) => { setKind(e.target.value); setPage(1); }}>
              <NativeSelectOption value="all">全部类型</NativeSelectOption>
              <NativeSelectOption value="foothold">据点</NativeSelectOption>
              <NativeSelectOption value="credential">凭据</NativeSelectOption>
              <NativeSelectOption value="artifact">遗留物</NativeSelectOption>
              <NativeSelectOption value="footprint">痕迹</NativeSelectOption>
            </NativeSelect>
            <Input
              className="h-8 w-32"
              placeholder="状态，如 active"
              value={state}
              onChange={(e) => { setState(e.target.value); setPage(1); }}
            />
            <Input
              className="h-8 w-28"
              placeholder="任务 id"
              value={task}
              onChange={(e) => { setTask(e.target.value); setPage(1); }}
            />
            <NativeSelect size="sm" value={String(company)} onChange={(e) => { setCompany(Number(e.target.value)); setPage(1); }}>
              <NativeSelectOption value="0">全部公司</NativeSelectOption>
              {companies.map((c) => (
                <NativeSelectOption key={c.id} value={String(c.id)}>{c.name}</NativeSelectOption>
              ))}
            </NativeSelect>
            <NativeSelect size="sm" value={String(campaign)} onChange={(e) => { setCampaign(Number(e.target.value)); setPage(1); }}>
              <NativeSelectOption value="0">全部战役</NativeSelectOption>
              {campaigns.map((c) => (
                <NativeSelectOption key={c.id} value={String(c.id)}>{c.name}</NativeSelectOption>
              ))}
            </NativeSelect>
            <Button size="sm" variant="outline" onClick={() => void loadLoot()} disabled={loading}>
              <RefreshCwIcon /> 刷新
            </Button>
            <Button size="sm" variant="outline" onClick={() => void exportLoot("csv")}>
              <DownloadIcon /> CSV
            </Button>
            <Button size="sm" variant="outline" onClick={() => void exportLoot("json")}>
              <DownloadIcon /> JSON
            </Button>
          </div>

          {error && <Card className="border-red-500/40"><CardContent className="p-4 text-red-600 text-sm dark:text-red-400">{error}</CardContent></Card>}

          <Card>
            <CardContent className="p-0">
              {loading ? (
                <div className="flex items-center justify-center gap-2 p-12 text-muted-foreground text-sm"><Spinner /> 读取中…</div>
              ) : items.length === 0 ? (
                <div className="p-12 text-center text-muted-foreground text-sm">没有匹配的战果。</div>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead className="border-b bg-muted/40 text-muted-foreground text-xs">
                      <tr>
                        <th className="px-4 py-2 text-left font-medium">类型</th>
                        <th className="px-4 py-2 text-left font-medium">摘要 / 身份</th>
                        <th className="px-4 py-2 text-left font-medium">任务</th>
                        <th className="px-4 py-2 text-left font-medium">状态</th>
                        <th className="px-4 py-2 text-left font-medium">目标 / 位置</th>
                        <th className="px-4 py-2 text-left font-medium">时间</th>
                      </tr>
                    </thead>
                    <tbody>
                      {items.map((it) => (
                        <tr key={it.id} className="border-b last:border-0">
                          <td className="px-4 py-2">
                            <span className="rounded border px-1.5 py-0.5 text-[11px] text-muted-foreground">{KIND_LABEL[it.kind] ?? it.kind}</span>
                          </td>
                          <td className="max-w-[24rem] px-4 py-2">
                            <div className="truncate" title={it.summary}>{it.summary || "—"}</div>
                            {it.kind === "credential" && (
                              <div className="mt-0.5 flex flex-wrap gap-2 text-muted-foreground text-xs">
                                {it.username && <span>账号 {it.username}</span>}
                                {it.fingerprint && <span className="font-mono">指纹 {it.fingerprint}</span>}
                                {(it.verified_count ?? 0) > 0 && <span>已验证 {it.verified_count} 个目标</span>}
                              </div>
                            )}
                          </td>
                          <td className="px-4 py-2">
                            <Link className="underline underline-offset-2" href={`/function/tasks/detail?id=${it.task_id}`}>
                              #{it.task_id} {it.task_name || ""}
                            </Link>
                          </td>
                          <td className="px-4 py-2 text-muted-foreground">{it.state}</td>
                          <td className="max-w-[22rem] px-4 py-2 text-muted-foreground" title={targetText(it)}>
                            <span className="line-clamp-2">{targetText(it)}</span>
                          </td>
                          <td className="px-4 py-2 text-muted-foreground">{fmtTime(it.created_at)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              <div className="flex items-center justify-end gap-2 border-t px-4 py-2">
                <Button size="sm" variant="outline" disabled={page <= 1 || loading} onClick={() => setPage((p) => Math.max(1, p - 1))}>上一页</Button>
                <span className="text-muted-foreground text-xs">{page} / {lootPages} · 共 {total} 条</span>
                <Button size="sm" variant="outline" disabled={page >= lootPages || loading} onClick={() => setPage((p) => p + 1)}>下一页</Button>
              </div>
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="attempts" className="mt-3 flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <Input className="h-8 w-28" placeholder="任务 id" value={attemptTask} onChange={(e) => { setAttemptTask(e.target.value); setAttemptPage(1); }} />
            <Input className="h-8 w-40" placeholder="主机" value={attemptHost} onChange={(e) => { setAttemptHost(e.target.value); setAttemptPage(1); }} />
            <Input className="h-8 w-36" placeholder="账号" value={attemptAccount} onChange={(e) => { setAttemptAccount(e.target.value); setAttemptPage(1); }} />
            <Button size="sm" variant="outline" onClick={() => void loadAttempts()} disabled={attemptLoading}><RefreshCwIcon /> 刷新</Button>
            <Button size="sm" variant="outline" onClick={() => void exportAttempts("csv")}><DownloadIcon /> CSV</Button>
            <Button size="sm" variant="outline" onClick={() => void exportAttempts("json")}><DownloadIcon /> JSON</Button>
          </div>
          <Card>
            <CardHeader><CardTitle className="flex items-center gap-2 text-sm"><KeyRoundIcon className="size-4" /> 凭据尝试（按时间倒序）</CardTitle></CardHeader>
            <CardContent className="p-0">
              {attemptLoading ? (
                <div className="flex items-center justify-center gap-2 p-12 text-muted-foreground text-sm"><Spinner /> 读取中…</div>
              ) : attempts.length === 0 ? (
                <div className="p-12 text-center text-muted-foreground text-sm">没有匹配的账号尝试。</div>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead className="border-b bg-muted/40 text-muted-foreground text-xs">
                      <tr>
                        <th className="px-4 py-2 text-left font-medium">时间</th>
                        <th className="px-4 py-2 text-left font-medium">任务</th>
                        <th className="px-4 py-2 text-left font-medium">主机</th>
                        <th className="px-4 py-2 text-left font-medium">账号</th>
                        <th className="px-4 py-2 text-left font-medium">工具</th>
                        <th className="px-4 py-2 text-left font-medium">命令</th>
                        <th className="px-4 py-2 text-left font-medium">成批</th>
                      </tr>
                    </thead>
                    <tbody>
                      {attempts.map((a) => (
                        <tr key={a.id} className="border-b last:border-0">
                          <td className="px-4 py-2 text-muted-foreground">{fmtTime(a.at)}</td>
                          <td className="px-4 py-2">
                            <Link className="underline underline-offset-2" href={`/function/tasks/detail?id=${a.task_id}`}>#{a.task_id}</Link>
                          </td>
                          <td className="px-4 py-2">{a.host}</td>
                          <td className="px-4 py-2">{a.account || "—"}</td>
                          <td className="px-4 py-2 text-muted-foreground">{a.tool || "—"}</td>
                          <td className="max-w-[28rem] truncate px-4 py-2 font-mono text-xs" title={a.command}>{a.command || "—"}</td>
                          <td className="px-4 py-2 text-muted-foreground">{a.spray ? "是" : "否"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              <div className="flex items-center justify-end gap-2 border-t px-4 py-2">
                <Button size="sm" variant="outline" disabled={attemptPage <= 1 || attemptLoading} onClick={() => setAttemptPage((p) => Math.max(1, p - 1))}>上一页</Button>
                <span className="text-muted-foreground text-xs">{attemptPage} / {attemptPages} · 共 {attemptTotal} 条</span>
                <Button size="sm" variant="outline" disabled={attemptPage >= attemptPages || attemptLoading} onClick={() => setAttemptPage((p) => p + 1)}>下一页</Button>
              </div>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}
