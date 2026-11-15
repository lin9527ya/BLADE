"use client";

import * as React from "react";

import { KeyRoundIcon, PlusIcon, ScrollTextIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { CopyButton } from "@/components/copy-button";
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
  const [loading, setLoading] = React.useState(true);
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [creating, setCreating] = React.useState(false);
  const [issued, setIssued] = React.useState<{ token: APIToken; plaintext: string } | null>(null);

  const load = React.useCallback(() => {
    setLoading(true);
    Promise.all([api.apiTokens(), api.auditEvents(100)])
      .then(([nextTokens, nextEvents]) => {
        setTokens(nextTokens);
        setEvents(nextEvents);
      })
      .catch((e: Error) => toast.error(`读取失败：${e.message}`))
      .finally(() => setLoading(false));
  }, []);

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
      const result = await api.createAPIToken(name.trim());
      setIssued(result);
      setName("");
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
                    <TableHead className="w-24">状态</TableHead>
                    <TableHead className="w-44">创建时间</TableHead>
                    <TableHead className="w-44">最后使用</TableHead>
                    <TableHead className="w-20" />
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {loading && tokens.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={5} className="py-10 text-center">
                        <Spinner />
                      </TableCell>
                    </TableRow>
                  ) : tokens.length === 0 ? (
                    <TableRow>
                      <TableCell colSpan={5} className="py-10 text-center text-muted-foreground">
                        还没有 API 令牌。
                      </TableCell>
                    </TableRow>
                  ) : (
                    tokens.map((token) => (
                      <TableRow key={token.id}>
                        <TableCell className="font-medium">{token.name}</TableCell>
                        <TableCell>
                          <Badge variant="outline">{token.revoked_at ? "已撤销" : "有效"}</Badge>
                        </TableCell>
                        <TableCell className="text-muted-foreground text-xs">{fmtTime(token.created_at)}</TableCell>
                        <TableCell className="text-muted-foreground text-xs">{fmtTime(token.last_used_at)}</TableCell>
                        <TableCell>
                          {!token.revoked_at ? (
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              className="text-muted-foreground hover:text-destructive"
                              onClick={() => void revoke(token)}
                              aria-label="撤销令牌"
                            >
                              <Trash2Icon />
                            </Button>
                          ) : null}
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
                        还没有审计事件。
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
            <DialogDescription>用于 Prometheus 抓取或自动化调用；权限等同管理员。</DialogDescription>
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
    </div>
  );
}
