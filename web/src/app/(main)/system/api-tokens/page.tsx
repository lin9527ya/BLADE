"use client";

import * as React from "react";

import { BanIcon, KeyRoundIcon, PlusIcon, ScrollTextIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { CopyButton } from "@/components/copy-button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
import type { APIToken, AuditEvent } from "@/lib/types";

function fmtTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

export default function APITokensPage() {
  const [tokens, setTokens] = React.useState<APIToken[]>([]);
  const [events, setEvents] = React.useState<AuditEvent[]>([]);
  const [auditActor, setAuditActor] = React.useState("");
  const [auditMethod, setAuditMethod] = React.useState("");
  const [auditQuery, setAuditQuery] = React.useState("");
  const [exporting, setExporting] = React.useState(false);
  const [loading, setLoading] = React.useState(true);
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [scopes, setScopes] = React.useState("admin");
  const [creating, setCreating] = React.useState(false);
  const [issued, setIssued] = React.useState<{ token: APIToken; plaintext: string } | null>(null);
  const [deleteTarget, setDeleteTarget] = React.useState<APIToken | null>(null);
  const [deleting, setDeleting] = React.useState(false);

  const load = React.useCallback(() => {
    setLoading(true);
    Promise.all([api.apiTokens(), api.auditEvents(200, { actor: auditActor, method: auditMethod, q: auditQuery })])
      .then(([nextTokens, nextEvents]) => {
        setTokens(nextTokens);
        setEvents(nextEvents);
      })
      .catch((e: Error) => toast.error(`读取失败：${e.message}`))
      .finally(() => setLoading(false));
  }, [auditActor, auditMethod, auditQuery]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function create() {
    if (!name.trim()) {
      toast.error("请填写令牌名称");
      return;
    }
    setCreating(true);
    try {
      const result = await api.createAPIToken(name.trim(), scopes);
      setIssued(result);
      setName("");
      setScopes("admin");
      setOpen(false);
      load();
    } catch (e) {
      toast.error(`创建失败：${(e as Error).message}`);
    } finally {
      setCreating(false);
    }
  }

  async function revoke(token: APIToken) {
    try {
      await api.revokeAPIToken(token.id);
      toast.success(`已撤销「${token.name}」`);
      load();
    } catch (e) {
      toast.error(`撤销失败：${(e as Error).message}`);
    }
  }

  async function purge() {
    if (!deleteTarget) return;
    setDeleting(true);
    try {
      await api.purgeAPIToken(deleteTarget.id);
      toast.success(`已删除「${deleteTarget.name}」`);
      setDeleteTarget(null);
      load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    } finally {
      setDeleting(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
            <KeyRoundIcon className="size-5" /> API 令牌与审计
          </h1>
          <p className="text-muted-foreground text-sm">长期令牌供 Prometheus/自动化使用；审计记录只覆盖变更类请求。</p>
        </div>
        <Button size="sm" onClick={() => setOpen(true)}>
          <PlusIcon /> 新建令牌
        </Button>
      </div>

      {issued ? (
        <Card className="border-amber-500/40 bg-amber-500/5">
          <CardHeader>
            <CardTitle className="text-sm">令牌只显示一次</CardTitle>
            <CardDescription>关闭后无法再次查看明文；请立即保存到安全位置。</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-wrap items-center gap-2">
            <code className="max-w-full break-all rounded bg-muted px-2 py-1 font-mono text-xs">
              {issued.plaintext}
            </code>
            <CopyButton text={issued.plaintext} successMessage="令牌已复制" />
            <Button variant="ghost" size="sm" onClick={() => setIssued(null)}>
              我已保存
            </Button>
          </CardContent>
        </Card>
      ) : null}

      <Tabs defaultValue="tokens">
        <TabsList>
          <TabsTrigger value="tokens">令牌</TabsTrigger>
          <TabsTrigger value="audit">
            <ScrollTextIcon className="size-3.5" /> 审计事件
          </TabsTrigger>
        </TabsList>
        <TabsContent value="tokens" className="mt-3">
          <Card className="py-0">
            <CardContent className="px-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>名称</TableHead>
                    <TableHead className="w-28">权限</TableHead>
                    <TableHead className="w-24">状态</TableHead>
                    <TableHead className="w-44">创建时间</TableHead>
                    <TableHead className="w-44">最后使用</TableHead>
                    <TableHead className="w-24" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {loading && tokens.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={6} className="py-10 text-center">
                        <Spinner />
                      </TableCell>
                    </TableRow>
                  ) : tokens.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={6} className="py-10 text-center text-muted-foreground">
                        还没有 API 令牌。
                      </TableCell>
                    </TableRow>
                  ) : (
                    tokens.map((token) => (
                      <TableRow key={token.id}>
                        <TableCell className="font-medium">{token.name}</TableCell>
                        <TableCell>
                          <Badge variant="outline">
                            {token.scopes === "read" ? "只读" : token.scopes === "operator" ? "操作员" : "管理员"}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant="outline">{token.revoked_at ? "已撤销" : "有效"}</Badge>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs">{fmtTime(token.created_at)}</TableCell>
                        <TableCell className="text-muted-foreground text-xs">{fmtTime(token.last_used_at)}</TableCell>
                        <TableCell>
                          <div className="flex justify-end gap-1">
                            {!token.revoked_at ? (
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                className="text-muted-foreground hover:text-destructive"
                                onClick={() => void revoke(token)}
                                aria-label="撤销令牌"
                                title="撤销（保留记录）"
                              >
                                <BanIcon />
                              </Button>
                            ) : null}
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              className="text-muted-foreground hover:text-destructive"
                              onClick={() => setDeleteTarget(token)}
                              aria-label="删除令牌"
                              title="永久删除"
                            >
                              <Trash2Icon />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="audit" className="mt-3">
          <Card className="py-0">
            <CardContent className="px-0">
              <div className="flex flex-wrap items-end gap-2 border-b px-3 py-2">
                <Input
                  value={auditActor}
                  onChange={(e) => setAuditActor(e.target.value)}
                  placeholder="操作者，如 token:prometheus"
                  className="h-8 w-56 text-xs"
                />
                <select
                  value={auditMethod}
                  onChange={(e) => setAuditMethod(e.target.value)}
                  className="h-8 rounded-md border bg-transparent px-2 text-xs"
                  aria-label="审计方法筛选"
                >
                  <option value="">全部方法</option>
                  {["POST", "PUT", "PATCH", "DELETE"].map((m) => (
                    <option key={m} value={m}>
                      {m}
                    </option>
                  ))}
                </select>
                <Input
                  value={auditQuery}
                  onChange={(e) => setAuditQuery(e.target.value)}
                  placeholder="路径包含…"
                  className="h-8 w-56 font-mono text-xs"
                />
                <Button
                  variant="outline"
                  size="sm"
                  disabled={exporting}
                  onClick={() => {
                    setExporting(true);
                    void api
                      .downloadAuditEventsCSV({ actor: auditActor, method: auditMethod, q: auditQuery })
                      .catch((e: Error) => toast.error(e.message))
                      .finally(() => setExporting(false));
                  }}
                >
                  <ScrollTextIcon /> {exporting ? "导出中…" : "导出 CSV"}
                </Button>
              </div>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-44">时间</TableHead>
                    <TableHead className="w-36">操作者</TableHead>
                    <TableHead className="w-20">方法</TableHead>
                    <TableHead>路径</TableHead>
                    <TableHead className="w-16 text-right">状态</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {events.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={5} className="py-10 text-center text-muted-foreground">
                        没有匹配的审计事件。
                      </TableCell>
                    </TableRow>
                  ) : (
                    events.map((event) => (
                      <TableRow key={event.id}>
                        <TableCell className="text-muted-foreground text-xs">{fmtTime(event.created_at)}</TableCell>
                        <TableCell className="text-xs">{event.actor}</TableCell>
                        <TableCell>
                          <Badge variant="outline">{event.method}</Badge>
                        </TableCell>
                        <TableCell className="max-w-md truncate font-mono text-xs" title={event.path}>
                          {event.path}
                        </TableCell>
                        <TableCell className="text-right text-xs tabular-nums">{event.status}</TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>新建 API 令牌</DialogTitle>
            <DialogDescription>用于 Prometheus 抓取或自动化调用。只读令牌只能发 GET/HEAD 请求。</DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="api-token-name">名称</FieldLabel>
              <Input
                id="api-token-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如：prometheus"
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="api-token-scopes">权限</FieldLabel>
              <select
                id="api-token-scopes"
                value={scopes}
                onChange={(e) => setScopes(e.target.value)}
                className="h-9 rounded-md border bg-transparent px-2 text-sm"
              >
                <option value="admin">管理员（读写）</option>
                <option value="operator">操作员（业务读写，系统配置只读）</option>
                <option value="read">只读</option>
              </select>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)} disabled={creating}>
              取消
            </Button>
            <Button onClick={() => void create()} disabled={creating}>
              {creating ? "创建中…" : "创建"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog open={!!deleteTarget} onOpenChange={(o) => !o && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>永久删除令牌</AlertDialogTitle>
            <AlertDialogDescription>
              将删除「{deleteTarget?.name}」及其记录，使用该令牌的自动化会立即失效，且不可恢复
              {deleteTarget?.revoked_at ? "。" : "。若只是想停用，请先「撤销」保留审计记录。"}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                void purge();
              }}
              disabled={deleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleting ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
