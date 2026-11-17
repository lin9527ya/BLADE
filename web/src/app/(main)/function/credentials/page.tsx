"use client";

import * as React from "react";

import { KeyRoundIcon, RefreshCwIcon, ShieldCheckIcon, ShieldXIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { CredentialCheckItem, CredentialWorkbenchItem } from "@/lib/types";

function fmtTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

const LIFECYCLE_LABEL: Record<string, string> = {
  lost: "已失效",
  locked: "账号锁定",
  invalid: "口令失效",
  expired: "已过期",
  expiring: "即将过期",
  stale: "待复核",
  verified: "已验证",
  unverified: "未验证",
  unknown: "无目标",
};

function lifecycleVariant(lifecycle: string): "default" | "secondary" | "destructive" | "outline" {
  switch (lifecycle) {
    case "verified":
      return "default";
    case "lost":
    case "locked":
    case "invalid":
    case "expired":
      return "destructive";
    case "expiring":
    case "stale":
      return "secondary";
    default:
      return "outline";
  }
}

export default function CredentialsPage() {
  const [items, setItems] = React.useState<CredentialWorkbenchItem[]>([]);
  const [total, setTotal] = React.useState(0);
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [loading, setLoading] = React.useState(true);
  const [host, setHost] = React.useState("");
  const [lifecycle, setLifecycle] = React.useState("");

  const [checkTarget, setCheckTarget] = React.useState<CredentialWorkbenchItem | null>(null);
  const [checks, setChecks] = React.useState<CredentialCheckItem[]>([]);
  const [checkForm, setCheckForm] = React.useState({
    host: "",
    port: "",
    service: "",
    result: "failed",
    evidence: "",
    note: "",
  });
  const [saving, setSaving] = React.useState(false);

  const [lostTarget, setLostTarget] = React.useState<CredentialWorkbenchItem | null>(null);
  const [lostForm, setLostForm] = React.useState({ reason: "", evidence: "" });
  const [selected, setSelected] = React.useState<Set<number>>(new Set());
  const [batchLostOpen, setBatchLostOpen] = React.useState(false);
  const [batchLostForm, setBatchLostForm] = React.useState({ reason: "", evidence: "" });
  const [batchBusy, setBatchBusy] = React.useState(false);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .credentialsWorkbench({ host: host.trim() || undefined, lifecycle: lifecycle || undefined, page: page + 1, size })
      .then((r) => {
        setItems(r.items);
        setTotal(r.total);
      })
      .catch((e: Error) => toast.error(`读取失败：${e.message}`))
      .finally(() => setLoading(false));
  }, [host, lifecycle, page, size]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function verify(item: CredentialWorkbenchItem) {
    try {
      const r = await api.verifyCredentialIntent(item.id);
      toast.success(`已生成验证意图 #${r.intent_id}${r.queued ? "（任务已排队）" : ""}`);
      load();
    } catch (e) {
      toast.error(`生成意图失败：${(e as Error).message}`);
    }
  }

  async function openChecks(item: CredentialWorkbenchItem) {
    setCheckTarget(item);
    setCheckForm({
      host: item.targets?.[0]?.host ?? "",
      port: item.targets?.[0]?.port ? String(item.targets[0].port) : "",
      service: item.targets?.[0]?.service ?? "",
      result: "failed",
      evidence: "",
      note: "",
    });
    try {
      const r = await api.credentialChecks(item.id);
      setChecks(r.items);
    } catch (e) {
      toast.error(`读取复核记录失败：${(e as Error).message}`);
    }
  }

  async function saveCheck() {
    if (!checkTarget) return;
    setSaving(true);
    try {
      await api.recordCredentialCheck(checkTarget.id, {
        host: checkForm.host,
        port: checkForm.port ? Number(checkForm.port) : undefined,
        service: checkForm.service || undefined,
        result: checkForm.result as "verified" | "failed" | "locked" | "expired" | "unknown",
        evidence: checkForm.evidence || undefined,
        note: checkForm.note || undefined,
      });
      toast.success("复核结果已记录");
      const r = await api.credentialChecks(checkTarget.id);
      setChecks(r.items);
      load();
    } catch (e) {
      toast.error(`记录失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  function toggleSelected(id: number) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function toggleAllOnPage() {
    setSelected((prev) => {
      const all = items.length > 0 && items.every((item) => prev.has(item.id));
      const next = new Set(prev);
      if (all) items.forEach((item) => next.delete(item.id));
      else items.forEach((item) => next.add(item.id));
      return next;
    });
  }

  async function batchMarkLost() {
    if (!batchLostForm.reason.trim() || !batchLostForm.evidence.trim()) {
      toast.error("原因与证据都必填");
      return;
    }
    setBatchBusy(true);
    try {
      const r = await api.batchMarkCredentialsLost(Array.from(selected), batchLostForm);
      toast.success(`已标记 ${r.updated} 条失效${r.errors.length ? `，${r.errors.length} 条失败` : ""}`);
      setBatchLostOpen(false);
      setBatchLostForm({ reason: "", evidence: "" });
      setSelected(new Set());
      load();
    } catch (e) {
      toast.error(`批量标记失败：${(e as Error).message}`);
    } finally {
      setBatchBusy(false);
    }
  }

  async function markLost() {
    if (!lostTarget) return;
    if (!lostForm.reason.trim() || !lostForm.evidence.trim()) {
      toast.error("原因与证据都必填");
      return;
    }
    try {
      await api.markCredentialLost(lostTarget.id, lostForm);
      toast.success("已标记失效");
      setLostTarget(null);
      setLostForm({ reason: "", evidence: "" });
      load();
    } catch (e) {
      toast.error(`标记失败：${(e as Error).message}`);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
            <KeyRoundIcon className="size-5" /> 凭据工作台
          </h1>
          <p className="text-muted-foreground text-sm">
            跨任务凭据、可登目标与复核状态；“验证”只生成意图，实际登录仍由 worker 在审批链下发起。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {selected.size > 0 && (
            <Button variant="destructive" size="sm" onClick={() => setBatchLostOpen(true)}>
              <ShieldXIcon /> 批量失效 ({selected.size})
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={load} disabled={loading}>
            <RefreshCwIcon /> 刷新
          </Button>
        </div>
      </div>

      <Card className="py-0">
        <CardContent className="flex flex-wrap items-end gap-2 px-4 py-3">
          <FieldGroup className="flex-row items-end gap-2">
            <Field className="w-44">
              <FieldLabel htmlFor="cred-host">目标主机</FieldLabel>
              <Input
                id="cred-host"
                value={host}
                onChange={(e) => {
                  setHost(e.target.value);
                  setPage(0);
                }}
                placeholder="web01 / 10.0.0.5"
              />
            </Field>
            <Field className="w-40">
              <FieldLabel htmlFor="cred-lifecycle">生命周期</FieldLabel>
              <select
                id="cred-lifecycle"
                className="h-9 rounded-md border bg-transparent px-2 text-sm"
                value={lifecycle}
                onChange={(e) => {
                  setLifecycle(e.target.value);
                  setPage(0);
                }}
              >
                <option value="">全部</option>
                {Object.entries(LIFECYCLE_LABEL).map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
            </Field>
          </FieldGroup>
        </CardContent>
      </Card>

      <Card className="overflow-hidden py-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-10">
                <Checkbox
                  checked={items.length > 0 && items.every((item) => selected.has(item.id))}
                  onCheckedChange={toggleAllOnPage}
                  aria-label="选择当前页全部凭据"
                />
              </TableHead>
              <TableHead>凭据</TableHead>
              <TableHead>账号</TableHead>
              <TableHead>可登目标</TableHead>
              <TableHead className="w-24">生命周期</TableHead>
              <TableHead className="w-40">最近复核</TableHead>
              <TableHead className="w-20 text-right">动用</TableHead>
              <TableHead className="w-64" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading && items.length === 0 ? (
              <TableRow>
                <TableCell colSpan={8} className="py-10 text-center">
                  <Spinner />
                </TableCell>
              </TableRow>
            ) : items.length === 0 ? (
              <TableRow>
                <TableCell colSpan={8} className="py-10 text-center text-muted-foreground">
                  没有匹配的凭据。
                </TableCell>
              </TableRow>
            ) : (
              items.map((item) => (
                <TableRow key={item.id}>
                  <TableCell>
                    <Checkbox
                      checked={selected.has(item.id)}
                      onCheckedChange={() => toggleSelected(item.id)}
                      aria-label={`选择凭据 #${item.id}`}
                    />
                  </TableCell>
                  <TableCell>
                    <div className="font-medium text-sm">{item.summary || item.kind || "凭据"}</div>
                    <div className="font-mono text-muted-foreground text-xs">
                      #{item.id} · {item.fingerprint || "无指纹"} · {item.task_name}
                    </div>
                  </TableCell>
                  <TableCell className="text-sm">{item.username || "—"}</TableCell>
                  <TableCell className="max-w-[20rem] text-xs">
                    {item.targets?.length ? (
                      <div className="flex flex-wrap gap-1">
                        {item.targets.slice(0, 4).map((t, i) => (
                          <Badge key={i} variant={t.verified ? "default" : "outline"} className="text-[10px]">
                            {t.host}
                            {t.port ? `:${t.port}` : ""}
                            {t.service ? `/${t.service}` : ""}
                          </Badge>
                        ))}
                        {(item.targets.length ?? 0) > 4 ? (
                          <span className="text-muted-foreground">+{(item.targets?.length ?? 0) - 4}</span>
                        ) : null}
                      </div>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                  <TableCell>
                    <Badge variant={lifecycleVariant(item.lifecycle)}>
                      {LIFECYCLE_LABEL[item.lifecycle] ?? item.lifecycle}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">
                    {fmtTime(item.last_checked_at)}
                    {item.last_result ? ` · ${item.last_result}` : ""}
                  </TableCell>
                  <TableCell className="text-right text-xs tabular-nums">{item.use_count}</TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="outline" onClick={() => void verify(item)}>
                        <ShieldCheckIcon /> 验证
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => void openChecks(item)}>
                        复核
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-destructive"
                        onClick={() => setLostTarget(item)}
                      >
                        <ShieldXIcon /> 失效
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>

      <div className="flex items-center justify-between text-muted-foreground text-xs">
        <span>共 {total} 条</span>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="outline" disabled={page === 0} onClick={() => setPage((p) => Math.max(0, p - 1))}>
            上一页
          </Button>
          <span>{page + 1}</span>
          <Button
            size="sm"
            variant="outline"
            disabled={(page + 1) * size >= total}
            onClick={() => setPage((p) => p + 1)}
          >
            下一页
          </Button>
          <select
            className="h-8 rounded border bg-transparent px-1"
            value={size}
            onChange={(e) => {
              setSize(Number(e.target.value));
              setPage(0);
            }}
          >
            {[20, 50, 100, 200].map((n) => (
              <option key={n} value={n}>
                {n}/页
              </option>
            ))}
          </select>
        </div>
      </div>

      <Dialog open={!!checkTarget} onOpenChange={(o) => !o && setCheckTarget(null)}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle>复核凭据 #{checkTarget?.id}</DialogTitle>
            <DialogDescription>只记录结果，不会自动登录目标；实际尝试用 loot_exec / foothold_exec。</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <div className="grid grid-cols-3 gap-2">
              <Field>
                <FieldLabel>主机</FieldLabel>
                <Input value={checkForm.host} onChange={(e) => setCheckForm({ ...checkForm, host: e.target.value })} />
              </Field>
              <Field>
                <FieldLabel>端口</FieldLabel>
                <Input value={checkForm.port} onChange={(e) => setCheckForm({ ...checkForm, port: e.target.value })} />
              </Field>
              <Field>
                <FieldLabel>服务</FieldLabel>
                <Input
                  value={checkForm.service}
                  onChange={(e) => setCheckForm({ ...checkForm, service: e.target.value })}
                />
              </Field>
            </div>
            <Field>
              <FieldLabel>结果</FieldLabel>
              <select
                className="h-9 rounded-md border bg-transparent px-2 text-sm"
                value={checkForm.result}
                onChange={(e) => setCheckForm({ ...checkForm, result: e.target.value })}
              >
                <option value="verified">verified（实测成功）</option>
                <option value="failed">failed（口令/密钥失效）</option>
                <option value="locked">locked（账号锁定）</option>
                <option value="expired">expired（已过期）</option>
                <option value="unknown">unknown</option>
              </select>
            </Field>
            <Field>
              <FieldLabel>证据（result 非 unknown 时必填）</FieldLabel>
              <Input
                value={checkForm.evidence}
                onChange={(e) => setCheckForm({ ...checkForm, evidence: e.target.value })}
                placeholder="ssh app@web01 → Permission denied"
              />
            </Field>
            <Field>
              <FieldLabel>备注</FieldLabel>
              <Input value={checkForm.note} onChange={(e) => setCheckForm({ ...checkForm, note: e.target.value })} />
            </Field>
          </FieldGroup>
          <div className="max-h-48 overflow-auto rounded border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>时间</TableHead>
                  <TableHead>目标</TableHead>
                  <TableHead>结果</TableHead>
                  <TableHead>证据</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {checks.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={4} className="py-4 text-center text-muted-foreground text-xs">
                      还没有复核记录
                    </TableCell>
                  </TableRow>
                ) : (
                  checks.map((c) => (
                    <TableRow key={c.id}>
                      <TableCell className="text-xs">{fmtTime(c.created_at)}</TableCell>
                      <TableCell className="text-xs">
                        {c.host}
                        {c.port ? `:${c.port}` : ""}
                        {c.service ? `/${c.service}` : ""}
                      </TableCell>
                      <TableCell>
                        <Badge variant={c.result === "verified" ? "default" : "destructive"}>{c.result}</Badge>
                      </TableCell>
                      <TableCell className="max-w-[16rem] truncate text-xs" title={c.evidence}>
                        {c.evidence || "—"}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCheckTarget(null)} disabled={saving}>
              关闭
            </Button>
            <Button onClick={() => void saveCheck()} disabled={saving}>
              {saving ? "保存中…" : "记录复核"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!lostTarget} onOpenChange={(o) => !o && setLostTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>标记凭据失效 #{lostTarget?.id}</DialogTitle>
            <DialogDescription>失效状态会进报告；之后 loot_exec / foothold_exec 会拒绝再用这条凭据。</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel>原因</FieldLabel>
              <Input
                value={lostForm.reason}
                onChange={(e) => setLostForm({ ...lostForm, reason: e.target.value })}
                placeholder="口令已改 / 账号锁定 / 主机下线"
              />
            </Field>
            <Field>
              <FieldLabel>证据（必填）</FieldLabel>
              <Input
                value={lostForm.evidence}
                onChange={(e) => setLostForm({ ...lostForm, evidence: e.target.value })}
                placeholder="ssh 输出 / 登录报错"
              />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button variant="outline" onClick={() => setLostTarget(null)}>
              取消
            </Button>
            <Button variant="destructive" onClick={() => void markLost()}>
              确认失效
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={batchLostOpen} onOpenChange={setBatchLostOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>批量标记失效（{selected.size} 条）</DialogTitle>
            <DialogDescription>
              同一条原因与证据会写入每条凭据的活动记录；HTTP 目标不会再被这些凭据使用。
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel>原因</FieldLabel>
              <Input
                value={batchLostForm.reason}
                onChange={(e) => setBatchLostForm({ ...batchLostForm, reason: e.target.value })}
                placeholder="口令已改 / 账号锁定 / 主机下线"
              />
            </Field>
            <Field>
              <FieldLabel>证据（必填）</FieldLabel>
              <Input
                value={batchLostForm.evidence}
                onChange={(e) => setBatchLostForm({ ...batchLostForm, evidence: e.target.value })}
                placeholder="批量复核输出 / 登录报错"
              />
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button variant="outline" onClick={() => setBatchLostOpen(false)} disabled={batchBusy}>
              取消
            </Button>
            <Button variant="destructive" onClick={() => void batchMarkLost()} disabled={batchBusy}>
              {batchBusy ? "标记中…" : "确认批量失效"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
