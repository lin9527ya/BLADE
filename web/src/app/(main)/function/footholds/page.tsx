"use client";

import * as React from "react";

import { RefreshCwIcon, RouteIcon, TerminalIcon, XCircleIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { FootholdWorkbenchItem, TunnelItem } from "@/lib/types";

function fmtTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

export default function FootholdsPage() {
  const [footholds, setFootholds] = React.useState<FootholdWorkbenchItem[]>([]);
  const [tunnels, setTunnels] = React.useState<TunnelItem[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [host, setHost] = React.useState("");

  const [lostTarget, setLostTarget] = React.useState<FootholdWorkbenchItem | null>(null);
  const [lostForm, setLostForm] = React.useState({ reason: "", evidence: "" });
  const [cancelTarget, setCancelTarget] = React.useState<TunnelItem | null>(null);
  const [cancelReason, setCancelReason] = React.useState("");

  const load = React.useCallback(() => {
    setLoading(true);
    Promise.all([api.footholdsWorkbench({ host: host.trim() || undefined, size: 100 }), api.tunnels()])
      .then(([f, t]) => {
        setFootholds(f.items);
        setTunnels(t.items);
      })
      .catch((e: Error) => toast.error(`读取失败：${e.message}`))
      .finally(() => setLoading(false));
  }, [host]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function check(item: FootholdWorkbenchItem) {
    try {
      const r = await api.footholdCheckIntent(item.id);
      toast.success(`已生成存活检查意图 #${r.intent_id}${r.queued ? "（任务已排队）" : ""}`);
    } catch (e) {
      toast.error(`生成意图失败：${(e as Error).message}`);
    }
  }

  async function markLost() {
    if (!lostTarget) return;
    if (!lostForm.reason.trim() || !lostForm.evidence.trim()) {
      toast.error("原因与证据都必填");
      return;
    }
    try {
      await api.markFootholdLost(lostTarget.id, lostForm);
      toast.success("已标记失效");
      setLostTarget(null);
      setLostForm({ reason: "", evidence: "" });
      load();
    } catch (e) {
      toast.error(`标记失败：${(e as Error).message}`);
    }
  }

  async function cancelTunnel() {
    if (!cancelTarget) return;
    if (!cancelReason.trim()) {
      toast.error("请填写取消理由");
      return;
    }
    try {
      await api.cancelExecution(cancelTarget.execution_id, cancelReason.trim());
      toast.success("已取消执行");
      setCancelTarget(null);
      setCancelReason("");
      load();
    } catch (e) {
      toast.error(`取消失败：${(e as Error).message}`);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
            <TerminalIcon className="size-5" /> 据点与隧道
          </h1>
          <p className="text-muted-foreground text-sm">
            据点是一条可复用的登录方式，隧道是挂在它上面的长执行；“存活检查”只生成意图，不直接连接目标。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Input className="w-48" value={host} onChange={(e) => setHost(e.target.value)} placeholder="按主机过滤" />
          <Button variant="outline" size="sm" onClick={load} disabled={loading}>
            <RefreshCwIcon /> 刷新
          </Button>
        </div>
      </div>

      <Tabs defaultValue="footholds">
        <TabsList>
          <TabsTrigger value="footholds"><RouteIcon className="size-3.5" /> 据点</TabsTrigger>
          <TabsTrigger value="tunnels"><TerminalIcon className="size-3.5" /> 隧道</TabsTrigger>
        </TabsList>

        <TabsContent value="footholds" className="mt-3">
          <Card className="overflow-hidden py-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>据点</TableHead>
                  <TableHead className="w-40">主机</TableHead>
                  <TableHead className="w-28">协议 / 权限</TableHead>
                  <TableHead className="w-40">凭据</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-44">最近使用</TableHead>
                  <TableHead className="w-32">隧道</TableHead>
                  <TableHead className="w-48" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {loading && footholds.length === 0 ? (
                  <TableRow><TableCell colSpan={8} className="py-10 text-center"><Spinner /></TableCell></TableRow>
                ) : footholds.length === 0 ? (
                  <TableRow><TableCell colSpan={8} className="py-10 text-center text-muted-foreground">还没有据点。</TableCell></TableRow>
                ) : footholds.map((f) => (
                  <TableRow key={f.id}>
                    <TableCell>
                      <div className="font-medium text-sm">{f.summary || `据点 #${f.id}`}</div>
                      <div className="text-muted-foreground text-xs">#{f.id} · {f.task_name}</div>
                    </TableCell>
                    <TableCell className="font-mono text-xs">{f.host || f.url || "—"}{f.port ? `:${f.port}` : ""}</TableCell>
                    <TableCell className="text-xs">{f.protocol || "—"}{f.privilege ? ` · ${f.privilege}` : ""}</TableCell>
                    <TableCell className="font-mono text-xs">{f.credential_id ? `#${f.credential_id} · ${f.credential_fingerprint || "无指纹"}` : "—"}</TableCell>
                    <TableCell><Badge variant={f.state === "active" ? "default" : "destructive"}>{f.state}</Badge></TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(f.last_used_at)}</TableCell>
                    <TableCell className="text-xs">
                      {f.tunnels?.length ? f.tunnels.map((t) => (
                        <div key={t.execution_id}>#{t.execution_id} → :{t.local_port} <Badge variant="secondary">{t.status}</Badge></div>
                      )) : "—"}
                    </TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="outline" onClick={() => void check(f)}>存活检查</Button>
                        <Button size="sm" variant="ghost" className="text-destructive" onClick={() => setLostTarget(f)}>失效</Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>

        <TabsContent value="tunnels" className="mt-3">
          <Card className="overflow-hidden py-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">执行</TableHead>
                  <TableHead>说明</TableHead>
                  <TableHead className="w-32">本地 SOCKS</TableHead>
                  <TableHead className="w-24">状态</TableHead>
                  <TableHead className="w-24">监听</TableHead>
                  <TableHead className="w-44">过期</TableHead>
                  <TableHead className="w-24" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {tunnels.length === 0 ? (
                  <TableRow><TableCell colSpan={7} className="py-10 text-center text-muted-foreground">还没有隧道执行。</TableCell></TableRow>
                ) : tunnels.map((t) => (
                  <TableRow key={t.execution_id}>
                    <TableCell className="text-xs">#{t.execution_id}</TableCell>
                    <TableCell className="max-w-[28rem] truncate text-xs" title={t.description}>{t.description || "—"}</TableCell>
                    <TableCell className="font-mono text-xs">{t.local_port ? `socks5h://127.0.0.1:${t.local_port}` : "—"}</TableCell>
                    <TableCell><Badge variant={t.status === "running" ? "default" : "secondary"}>{t.status}</Badge></TableCell>
                    <TableCell>{t.listening ? <Badge variant="default">listening</Badge> : <span className="text-muted-foreground text-xs">—</span>}</TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(t.expires_at)}</TableCell>
                    <TableCell>
                      {["queued", "running"].includes(t.status) ? (
                        <Button size="sm" variant="ghost" className="text-destructive" onClick={() => { setCancelTarget(t); setCancelReason(""); }}>
                          <XCircleIcon /> 关闭
                        </Button>
                      ) : null}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
        </TabsContent>
      </Tabs>

      <Dialog open={!!lostTarget} onOpenChange={(o) => !o && setLostTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>标记据点失效 #{lostTarget?.id}</DialogTitle>
            <DialogDescription>失效后 foothold_exec 会拒绝再用；已开的隧道不会自动关闭。</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field><FieldLabel>原因</FieldLabel><Input value={lostForm.reason} onChange={(e) => setLostForm({ ...lostForm, reason: e.target.value })} placeholder="连接被拒 / webshell 404 / 主机下线" /></Field>
            <Field><FieldLabel>证据（必填）</FieldLabel><Input value={lostForm.evidence} onChange={(e) => setLostForm({ ...lostForm, evidence: e.target.value })} placeholder="命令与关键输出" /></Field>
          </FieldGroup>
          <DialogFooter>
            <Button variant="outline" onClick={() => setLostTarget(null)}>取消</Button>
            <Button variant="destructive" onClick={() => void markLost()}>确认失效</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!cancelTarget} onOpenChange={(o) => !o && setCancelTarget(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>关闭隧道 #{cancelTarget?.execution_id}</DialogTitle>
            <DialogDescription>会终止该执行（杀进程组），理由会写进台账并回传给任务。</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field><FieldLabel>理由</FieldLabel><Input value={cancelReason} onChange={(e) => setCancelReason(e.target.value)} placeholder="不再需要 / 转发失败" /></Field>
          </FieldGroup>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCancelTarget(null)}>取消</Button>
            <Button variant="destructive" onClick={() => void cancelTunnel()}>确认关闭</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
