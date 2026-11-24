"use client";

import * as React from "react";

import { MessageSquare, PencilIcon, PlusIcon, RefreshCwIcon, SendIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { IMAllowedUser, IMConnection, IMMessage, IMMeta, IMPlatform } from "@/lib/types";

type ConnForm = {
  name: string;
  platform: IMPlatform;
  app_id: string;
  app_secret: string;
  enabled: boolean;
  allow_group: boolean;
  verification_token: string;
  encrypt_key: string;
};

type UserForm = {
  platform: IMPlatform;
  user_id: string;
  display_name: string;
  role: "viewer" | "operator";
  enabled: boolean;
  note: string;
};

function emptyConn(platform: IMPlatform = "dingtalk"): ConnForm {
  return { name: "", platform, app_id: "", app_secret: "", enabled: true, allow_group: false, verification_token: "", encrypt_key: "" };
}

function emptyUser(platform: IMPlatform = "dingtalk"): UserForm {
  return { platform, user_id: "", display_name: "", role: "viewer", enabled: true, note: "" };
}

function statusVariant(status: string): "default" | "secondary" | "destructive" | "outline" {
  if (status === "connected") return "default";
  if (status === "error") return "destructive";
  if (status === "connecting") return "secondary";
  return "outline";
}

function fmtTime(v?: string): string {
  if (!v) return "—";
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? v : d.toLocaleString();
}

export default function IMPage() {
  const [meta, setMeta] = React.useState<IMMeta | null>(null);
  const [connections, setConnections] = React.useState<IMConnection[]>([]);
  const [users, setUsers] = React.useState<IMAllowedUser[]>([]);
  const [messages, setMessages] = React.useState<IMMessage[]>([]);
  const [tab, setTab] = React.useState("connections");
  const [loadingMessages, setLoadingMessages] = React.useState(false);

  const [connOpen, setConnOpen] = React.useState(false);
  const [editingConn, setEditingConn] = React.useState<IMConnection | null>(null);
  const [connForm, setConnForm] = React.useState<ConnForm>(emptyConn());
  const [savingConn, setSavingConn] = React.useState(false);

  const [userOpen, setUserOpen] = React.useState(false);
  const [editingUser, setEditingUser] = React.useState<IMAllowedUser | null>(null);
  const [userForm, setUserForm] = React.useState<UserForm>(emptyUser());
  const [savingUser, setSavingUser] = React.useState(false);

  const load = React.useCallback(() => {
    api.imMeta().then(setMeta).catch((e) => toast.error(`读取 IM 元数据失败：${(e as Error).message}`));
    api.imConnections().then(setConnections).catch((e) => toast.error(`读取连接失败：${(e as Error).message}`));
    api.imUsers().then(setUsers).catch((e) => toast.error(`读取允许用户失败：${(e as Error).message}`));
  }, []);

  const loadMessages = React.useCallback(() => {
    setLoadingMessages(true);
    api.imMessages(200)
      .then(setMessages)
      .catch((e) => toast.error(`读取消息失败：${(e as Error).message}`))
      .finally(() => setLoadingMessages(false));
  }, []);

  React.useEffect(() => {
    load();
    loadMessages();
  }, [load, loadMessages]);

  function openAddConn() {
    setEditingConn(null);
    setConnForm(emptyConn(meta?.platforms[0]?.key ?? "dingtalk"));
    setConnOpen(true);
  }

  function openEditConn(c: IMConnection) {
    setEditingConn(c);
    setConnForm({
      name: c.name,
      platform: c.platform,
      app_id: c.app_id,
      app_secret: "", // 掩码不回填：留空即不修改
      enabled: c.enabled,
      allow_group: c.extra?.allow_group === true,
      verification_token: c.extra?.verification_token?.startsWith("__masked__") ? "" : (c.extra?.verification_token ?? ""),
      encrypt_key: c.extra?.encrypt_key?.startsWith("__masked__") ? "" : (c.extra?.encrypt_key ?? ""),
    });
    setConnOpen(true);
  }

  async function saveConn() {
    if (!connForm.app_id.trim()) return toast.error("app_id 不能为空");
    if (!editingConn && !connForm.app_secret.trim()) return toast.error("app_secret 不能为空");
    setSavingConn(true);
    try {
      const payload = {
        name: connForm.name,
        platform: connForm.platform,
        app_id: connForm.app_id,
        app_secret: connForm.app_secret,
        enabled: connForm.enabled,
        extra: {
          allow_group: connForm.allow_group,
          verification_token: connForm.verification_token,
          encrypt_key: connForm.encrypt_key,
        },
      };
      if (editingConn) {
        await api.imUpdateConnection(editingConn.id, payload);
        toast.success("连接已更新，正在重新连接");
      } else {
        await api.imCreateConnection(payload);
        toast.success("连接已创建");
      }
      setConnOpen(false);
      load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSavingConn(false);
    }
  }

  async function deleteConn(c: IMConnection) {
    if (!window.confirm(`删除连接「${c.name}」？该连接的消息记录也会一并删除。`)) return;
    try {
      await api.imDeleteConnection(c.id);
      toast.success("连接已删除");
      load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }

  async function testConn(c: IMConnection) {
    try {
      const r = await api.imTestConnection(c.id);
      if (r.ok) toast.success(r.message ?? "凭据校验通过");
      else toast.error(r.error ?? "连接测试失败");
      load();
    } catch (e) {
      toast.error(`测试失败：${(e as Error).message}`);
    }
  }

  function openAddUser() {
    setEditingUser(null);
    setUserForm(emptyUser(meta?.platforms[0]?.key ?? "dingtalk"));
    setUserOpen(true);
  }

  function openEditUser(u: IMAllowedUser) {
    setEditingUser(u);
    setUserForm({
      platform: u.platform,
      user_id: u.user_id,
      display_name: u.display_name,
      role: u.role,
      enabled: u.enabled,
      note: u.note,
    });
    setUserOpen(true);
  }

  async function saveUser() {
    if (!editingUser && !userForm.user_id.trim()) return toast.error("user_id 不能为空");
    setSavingUser(true);
    try {
      if (editingUser) {
        await api.imUpdateUser(editingUser.id, {
          display_name: userForm.display_name,
          role: userForm.role,
          enabled: userForm.enabled,
          note: userForm.note,
        });
        toast.success("允许用户已更新");
      } else {
        await api.imCreateUser({
          platform: userForm.platform,
          user_id: userForm.user_id,
          display_name: userForm.display_name,
          role: userForm.role,
          enabled: userForm.enabled,
          note: userForm.note,
        });
        toast.success("允许用户已添加");
      }
      setUserOpen(false);
      load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSavingUser(false);
    }
  }

  async function deleteUser(u: IMAllowedUser) {
    if (!window.confirm(`移除允许用户「${u.display_name || u.user_id}」？`)) return;
    try {
      await api.imDeleteUser(u.id);
      toast.success("已移除");
      load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }

  return (
    <div className="mx-auto grid w-full max-w-6xl gap-4 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="flex items-center gap-2 text-xl font-semibold">
            <MessageSquare className="size-5" /> IM 控制
          </h1>
          <p className="text-muted-foreground text-sm">
            通过钉钉 Stream / 飞书长连接在手机上创建、查看、暂停/继续和编辑任务。BLADE 仍只监听本机。
          </p>
        </div>
      </div>

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="connections">连接</TabsTrigger>
          <TabsTrigger value="users">允许用户</TabsTrigger>
          <TabsTrigger value="messages">消息日志</TabsTrigger>
        </TabsList>

        <TabsContent value="connections" className="grid gap-3">
          <div className="flex justify-end">
            <Button onClick={openAddConn}><PlusIcon className="size-4" /> 新增连接</Button>
          </div>
          {connections.length === 0 && (
            <Card><CardContent className="text-muted-foreground p-6 text-sm">还没有连接。新增一个钉钉或飞书应用后，长连接会自动建立。</CardContent></Card>
          )}
          {connections.map((c) => (
            <Card key={c.id}>
              <CardHeader className="flex flex-row items-start justify-between gap-2">
                <div className="grid gap-1">
                  <CardTitle className="flex items-center gap-2 text-base">
                    {c.name}
                    <Badge variant={statusVariant(c.status)}>{c.status || "unknown"}</Badge>
                    {!c.enabled && <Badge variant="outline">已停用</Badge>}
                  </CardTitle>
                  <div className="text-muted-foreground text-xs">
                    {meta?.platforms.find((p) => p.key === c.platform)?.label ?? c.platform} · app_id {c.app_id} · 最近连接 {fmtTime(c.last_connected_at)}
                  </div>
                  {c.last_error && <div className="text-destructive text-xs">{c.last_error}</div>}
                </div>
                <div className="flex shrink-0 gap-1">
                  <Button size="sm" variant="outline" onClick={() => testConn(c)}><SendIcon className="size-3.5" /> 测试</Button>
                  <Button size="sm" variant="outline" onClick={() => openEditConn(c)}><PencilIcon className="size-3.5" /> 编辑</Button>
                  <Button size="sm" variant="outline" onClick={() => deleteConn(c)}><Trash2Icon className="size-3.5" /> 删除</Button>
                </div>
              </CardHeader>
              <CardContent className="text-muted-foreground text-xs">
                群聊控制：{c.extra?.allow_group ? "允许（仍需用户在白名单）" : "仅私聊"} · 状态由长连接实时回写
              </CardContent>
            </Card>
          ))}
        </TabsContent>

        <TabsContent value="users" className="grid gap-3">
          <div className="flex items-center justify-between gap-2">
            <p className="text-muted-foreground text-xs">{meta?.note}</p>
            <Button onClick={openAddUser}><PlusIcon className="size-4" /> 添加用户</Button>
          </div>
          {users.length === 0 && (
            <Card><CardContent className="text-muted-foreground p-6 text-sm">还没有允许用户。未登记的用户发消息只会留审计，不会执行任何操作。</CardContent></Card>
          )}
          {users.map((u) => (
            <Card key={u.id}>
              <CardContent className="flex flex-wrap items-center justify-between gap-2 p-4">
                <div className="grid gap-1">
                  <div className="flex items-center gap-2 text-sm font-medium">
                    {u.display_name || u.user_id}
                    <Badge variant={u.role === "operator" ? "default" : "outline"}>{u.role}</Badge>
                    {!u.enabled && <Badge variant="destructive">已停用</Badge>}
                  </div>
                  <div className="text-muted-foreground text-xs">
                    {u.platform} · user_id {u.user_id}{u.note ? ` · ${u.note}` : ""}
                  </div>
                </div>
                <div className="flex gap-1">
                  <Button size="sm" variant="outline" onClick={() => openEditUser(u)}><PencilIcon className="size-3.5" /> 编辑</Button>
                  <Button size="sm" variant="outline" onClick={() => deleteUser(u)}><Trash2Icon className="size-3.5" /> 移除</Button>
                </div>
              </CardContent>
            </Card>
          ))}
        </TabsContent>

        <TabsContent value="messages" className="grid gap-3">
          <div className="flex justify-end">
            <Button variant="outline" onClick={loadMessages} disabled={loadingMessages}>
              <RefreshCwIcon className="size-4" /> 刷新
            </Button>
          </div>
          <Card>
            <CardContent className="overflow-x-auto p-0">
              <table className="w-full text-xs">
                <thead className="bg-muted/50 text-left">
                  <tr>
                    <th className="p-2">时间</th>
                    <th className="p-2">连接</th>
                    <th className="p-2">发送者</th>
                    <th className="p-2">状态</th>
                    <th className="p-2">消息</th>
                    <th className="p-2">回复/错误</th>
                  </tr>
                </thead>
                <tbody>
                  {messages.map((m) => (
                    <tr key={m.id} className="border-t align-top">
                      <td className="whitespace-nowrap p-2">{fmtTime(m.created_at)}</td>
                      <td className="p-2">{m.connection_name || m.connection_id}</td>
                      <td className="p-2">{m.sender_name || m.sender_id}</td>
                      <td className="p-2"><Badge variant={m.state === "replied" ? "default" : m.state === "failed" ? "destructive" : "outline"}>{m.state}</Badge></td>
                      <td className="max-w-[22rem] p-2"><div className="line-clamp-3 whitespace-pre-wrap">{m.text}</div></td>
                      <td className="max-w-[22rem] p-2">
                        <div className="line-clamp-3 whitespace-pre-wrap">{m.reply || m.error || "—"}</div>
                        {m.task_id ? <div className="text-muted-foreground mt-1">task #{m.task_id}</div> : null}
                      </td>
                    </tr>
                  ))}
                  {messages.length === 0 && (
                    <tr><td colSpan={6} className="text-muted-foreground p-6 text-center">还没有消息。</td></tr>
                  )}
                </tbody>
              </table>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <Dialog open={connOpen} onOpenChange={setConnOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{editingConn ? "编辑连接" : "新增连接"}</DialogTitle>
            <DialogDescription>凭据只用于建立平台长连接；保存后不会在接口中回显明文。</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label>名称</Label>
              <Input value={connForm.name} onChange={(e) => setConnForm({ ...connForm, name: e.target.value })} placeholder="例如：钉钉运维群" />
            </div>
            <div className="grid gap-1.5">
              <Label>平台</Label>
              <Select value={connForm.platform} onValueChange={(v) => setConnForm({ ...connForm, platform: v as IMPlatform })}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  {(meta?.platforms ?? [{ key: "dingtalk", label: "钉钉" }, { key: "feishu", label: "飞书" }]).map((p) => (
                    <SelectItem key={p.key} value={p.key}>{p.label}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>App ID / Client ID</Label>
              <Input value={connForm.app_id} onChange={(e) => setConnForm({ ...connForm, app_id: e.target.value })} />
            </div>
            <div className="grid gap-1.5">
              <Label>App Secret / Client Secret</Label>
              <Input type="password" value={connForm.app_secret} onChange={(e) => setConnForm({ ...connForm, app_secret: e.target.value })} placeholder={editingConn ? "已保存，留空表示不修改" : ""} />
            </div>
            <div className="flex items-center justify-between">
              <Label>启用长连接</Label>
              <Switch checked={connForm.enabled} onCheckedChange={(v) => setConnForm({ ...connForm, enabled: v })} />
            </div>
            <div className="flex items-center justify-between">
              <Label>允许群聊控制</Label>
              <Switch checked={connForm.allow_group} onCheckedChange={(v) => setConnForm({ ...connForm, allow_group: v })} />
            </div>
            {connForm.platform === "feishu" && (
              <>
                <div className="grid gap-1.5">
                  <Label>Verification Token（可选）</Label>
                  <Input value={connForm.verification_token} onChange={(e) => setConnForm({ ...connForm, verification_token: e.target.value })} placeholder={editingConn ? "已保存时留空不修改" : "飞书事件订阅启用了验证令牌时填写"} />
                </div>
                <div className="grid gap-1.5">
                  <Label>Encrypt Key（可选）</Label>
                  <Input type="password" value={connForm.encrypt_key} onChange={(e) => setConnForm({ ...connForm, encrypt_key: e.target.value })} placeholder={editingConn ? "已保存时留空不修改" : "飞书事件订阅启用了加密时填写"} />
                </div>
              </>
            )}
            {meta && <p className="text-muted-foreground text-xs">{meta.platforms.find((p) => p.key === connForm.platform)?.hint}</p>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConnOpen(false)}>取消</Button>
            <Button onClick={saveConn} disabled={savingConn}>{savingConn ? "保存中…" : "保存"}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={userOpen} onOpenChange={setUserOpen}>
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{editingUser ? "编辑允许用户" : "添加允许用户"}</DialogTitle>
            <DialogDescription>只有登记且启用的用户才能执行命令；viewer 只读，operator 可改任务。</DialogDescription>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-1.5">
              <Label>平台</Label>
              <Select value={userForm.platform} disabled={!!editingUser} onValueChange={(v) => setUserForm({ ...userForm, platform: v as IMPlatform })}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  {(meta?.platforms ?? [{ key: "dingtalk", label: "钉钉" }, { key: "feishu", label: "飞书" }]).map((p) => (
                    <SelectItem key={p.key} value={p.key}>{p.label}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-1.5">
              <Label>用户 ID</Label>
              <Input value={userForm.user_id} disabled={!!editingUser} onChange={(e) => setUserForm({ ...userForm, user_id: e.target.value })} placeholder="钉钉 staffId / 飞书 open_id" />
            </div>
            <div className="grid gap-1.5">
              <Label>显示名（可选）</Label>
              <Input value={userForm.display_name} onChange={(e) => setUserForm({ ...userForm, display_name: e.target.value })} />
            </div>
            <div className="grid gap-1.5">
              <Label>角色</Label>
              <Select value={userForm.role} onValueChange={(v) => setUserForm({ ...userForm, role: v as "viewer" | "operator" })}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="viewer">viewer（只读）</SelectItem>
                  <SelectItem value="operator">operator（可创建/暂停/编辑）</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between">
              <Label>启用</Label>
              <Switch checked={userForm.enabled} onCheckedChange={(v) => setUserForm({ ...userForm, enabled: v })} />
            </div>
            <div className="grid gap-1.5">
              <Label>备注（可选）</Label>
              <Textarea value={userForm.note} onChange={(e) => setUserForm({ ...userForm, note: e.target.value })} />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setUserOpen(false)}>取消</Button>
            <Button onClick={saveUser} disabled={savingUser}>{savingUser ? "保存中…" : "保存"}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
