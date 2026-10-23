"use client";

import * as React from "react";

import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";

export function ChangePasswordDialog({
  open,
  onOpenChange,
}: {
  readonly open: boolean;
  readonly onOpenChange: (open: boolean) => void;
}) {
  const [currentUsername, setCurrentUsername] = React.useState("");
  const [newUsername, setNewUsername] = React.useState("");
  const [oldPassword, setOldPassword] = React.useState("");
  const [newPassword, setNewPassword] = React.useState("");
  const [confirmPassword, setConfirmPassword] = React.useState("");
  const [saving, setSaving] = React.useState(false);
  const [savingUsername, setSavingUsername] = React.useState(false);
  const busy = saving || savingUsername;

  // reset fields whenever the dialog closes; load the configured username on open
  React.useEffect(() => {
    if (!open) {
      setOldPassword("");
      setNewPassword("");
      setConfirmPassword("");
      setNewUsername("");
      return;
    }
    api
      .authAccount()
      .then(({ username }) => {
        setCurrentUsername(username);
        setNewUsername(username);
      })
      .catch(() => {
        // 读取失败不影响改密码；用户名那一栏留空，用户可手动输入。
      });
  }, [open]);

  function saveUsername() {
    if (!oldPassword) {
      toast.error("请先输入当前密码");
      return;
    }
    if (!newUsername.trim()) {
      toast.error("请输入用户名");
      return;
    }
    setSavingUsername(true);
    api
      .changeUsername(oldPassword, newUsername.trim())
      .then((res) => {
        // 服务端会换发一个 sub=新用户名 的 token；写入后刷新，让侧栏名称立即更新。
        if (res.token) auth.setToken(res.token);
        setCurrentUsername(res.username);
        toast.success("用户名已修改");
        window.location.reload();
      })
      .catch((err) => toast.error(`修改用户名失败：${(err as Error).message}`))
      .finally(() => setSavingUsername(false));
  }

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!oldPassword || !newPassword) {
      toast.error("请填写当前密码和新密码");
      return;
    }
    if (newPassword !== confirmPassword) {
      toast.error("两次输入的新密码不一致");
      return;
    }
    setSaving(true);
    api
      .changePassword(oldPassword, newPassword)
      .then((res) => {
        // 改密码会轮换签名密钥 → **本次会话的 token 也随失效**，必须送回登录页：
        // 留着它只会让后续每个请求都 401。轮换失败时后端带 warning，如实转达而不假装成功。
        onOpenChange(false);
        if (res?.tokens_revoked === false) {
          toast.error(res.warning ?? "密码已修改，但旧 token 未失效");
          return;
        }
        toast.success("密码已修改，请重新登录");
        auth.clearToken();
        window.location.href = "/login";
      })
      .catch((err) => toast.error(`修改失败：${(err as Error).message}`))
      .finally(() => setSaving(false));
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>账户设置</DialogTitle>
            <DialogDescription>
              当前用户名：<b>{currentUsername || "…"}</b>。修改用户名或密码都需要当前密码；
              修改密码后<b>已签发的登录 token 会立即失效</b>，需要重新登录。
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-3 py-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="cu-name">用户名</Label>
              <div className="flex gap-2">
                <Input
                  id="cu-name"
                  autoComplete="username"
                  value={newUsername}
                  disabled={busy}
                  onChange={(e) => setNewUsername(e.target.value)}
                />
                <Button type="button" variant="outline" onClick={saveUsername} disabled={busy}>
                  {savingUsername ? "保存中…" : "保存用户名"}
                </Button>
              </div>
            </div>
            <div className="border-t pt-3 font-medium text-sm">修改密码</div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="cp-old">当前密码</Label>
              <Input
                id="cp-old"
                type="password"
                autoComplete="current-password"
                value={oldPassword}
                disabled={busy}
                onChange={(e) => setOldPassword(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="cp-new">新密码</Label>
              <Input
                id="cp-new"
                type="password"
                autoComplete="new-password"
                value={newPassword}
                disabled={busy}
                onChange={(e) => setNewPassword(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="cp-confirm">确认新密码</Label>
              <Input
                id="cp-confirm"
                type="password"
                autoComplete="new-password"
                value={confirmPassword}
                disabled={busy}
                onChange={(e) => setConfirmPassword(e.target.value)}
              />
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
              取消
            </Button>
            <Button type="submit" disabled={busy}>
              {saving ? "修改中…" : "修改密码"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
