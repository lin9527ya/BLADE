"use client";

import * as React from "react";
import { FolderKanbanIcon, PlusIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
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
import { Field } from "@/components/page-sections";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type { Campaign, CampaignSummary, Task } from "@/lib/types";

// 战役：任务之上的一层（一个客户、若干目标、一份交付物）。
//
// 这一页的用处与任务页不同：任务页看「这一个目标打穿了什么」，这里看「这轮作业总共拿到了什么」。
// 归属走声明式接口（一次给出应包含的全部任务），所以这里的加/减都是"算好新列表再提交"。
export default function CampaignsPage() {
  const [rows, setRows] = React.useState<Campaign[]>([]);
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [loading, setLoading] = React.useState(false);
  const [summary, setSummary] = React.useState<CampaignSummary | null>(null);
  const [busy, setBusy] = React.useState(false);

  const [createOpen, setCreateOpen] = React.useState(false);
  const [newName, setNewName] = React.useState("");
  const [newNote, setNewNote] = React.useState("");

  const refresh = React.useCallback(async () => {
    setLoading(true);
    try {
      const [cs, ts] = await Promise.all([api.campaigns(), api.tasks()]);
      setRows(cs);
      setTasks(ts.tasks);
    } catch (e) {
      toast.error("加载失败：" + String((e as Error)?.message ?? e));
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void refresh();
  }, [refresh]);

  const openDetail = async (id: number) => {
    try {
      setSummary(await api.campaign(id));
    } catch (e) {
      toast.error("打开失败：" + String((e as Error)?.message ?? e));
    }
  };

  const create = async () => {
    if (!newName.trim()) {
      toast.error("请填写战役名称");
      return;
    }
    setBusy(true);
    try {
      await api.createCampaign(newName.trim(), newNote.trim());
      toast.success("已创建");
      setCreateOpen(false);
      setNewName("");
      setNewNote("");
      await refresh();
    } catch (e) {
      const msg = String((e as Error)?.message ?? e);
      toast.error(/已存在同名战役/.test(msg) ? "已存在同名战役" : "创建失败：" + msg);
    } finally {
      setBusy(false);
    }
  };

  // 归属是声明式的：算出"应当包含的全部任务"再整体提交，多退少补由服务端处理。
  const setMembers = async (c: CampaignSummary, ids: number[]) => {
    setBusy(true);
    try {
      const res = await api.setCampaignTasks(c.campaign.id, ids);
      if (res.removed.length > 0) toast.info(`已移出 ${res.removed.length} 个任务`);
      await refresh();
      await openDetail(c.campaign.id);
    } catch (e) {
      toast.error("保存归属失败：" + String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!summary) return;
    const c = summary;
    setBusy(true);
    try {
      const res = await api.deleteCampaign(c.campaign.id);
      toast.success(res.tasks_unbound > 0 ? `已删除；${res.tasks_unbound} 个任务已解绑（未删除）` : "已删除");
      setSummary(null);
      await refresh();
    } catch (e) {
      toast.error("删除失败：" + String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  const memberIds = summary ? summary.tasks.map((t) => t.id) : [];
  const candidates = tasks.filter((t) => !memberIds.includes(Number(t.id)));

  return (
    <div className="flex h-[calc(100vh-6rem)] min-h-0 flex-col gap-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">战役</h1>
          <p className="text-muted-foreground text-sm">
            一次作业 = 一个客户 + 若干任务。这里看的是这轮作业<b>总共</b>拿到了什么。
          </p>
        </div>
        <div className="flex items-center gap-3">
          <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={loading}>
            <RefreshCwIcon className={loading ? "size-4 animate-spin" : "size-4"} /> 刷新
          </Button>
          <Button size="sm" onClick={() => setCreateOpen(true)}>
            <PlusIcon className="size-4" /> 新建战役
          </Button>
        </div>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {rows.length === 0 ? (
          <Card className="py-0">
            <CardContent className="text-muted-foreground py-12 text-center text-sm">
              还没有战役。把同一轮作业下的任务归到一起，就能在这里看到汇总的战果与技术覆盖。
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {rows.map((c) => (
              <Card
                key={c.id}
                className="hover:border-foreground/20 cursor-pointer py-0 transition-colors"
                onClick={() => void openDetail(c.id)}
              >
                <CardContent className="p-4">
                  <div className="flex items-start justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <FolderKanbanIcon className="text-muted-foreground size-4" />
                      <span className="font-medium">{c.name}</span>
                    </div>
                    <Badge variant="outline">{c.task_count ?? 0} 个任务</Badge>
                  </div>
                  {c.note && <p className="text-muted-foreground mt-2 line-clamp-2 text-xs">{c.note}</p>}
                </CardContent>
              </Card>
            ))}
          </div>
        )}
      </div>

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>新建战役</DialogTitle>
            <DialogDescription>给这一轮作业起个名字。同名（忽略空格与大小写）会被拒绝。</DialogDescription>
          </DialogHeader>
          <Field label="名称" htmlFor="campaign-name">
            <Input
              id="campaign-name"
              autoComplete="off"
              placeholder="如 2026Q4 Acme 外部评估"
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
            />
          </Field>
          <Field label="备注（可选）" htmlFor="campaign-note">
            <Input
              id="campaign-note"
              autoComplete="off"
              placeholder="授权书编号、对接人、窗口…"
              value={newNote}
              onChange={(e) => setNewNote(e.target.value)}
            />
          </Field>
          <DialogFooter>
            <Button variant="outline" onClick={() => setCreateOpen(false)} disabled={busy}>
              取消
            </Button>
            <Button onClick={() => void create()} disabled={busy}>
              创建
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={!!summary} onOpenChange={(v) => !v && setSummary(null)}>
        <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
          {summary && (
            <>
              <DialogHeader className="border-b p-6">
                <DialogTitle>{summary.campaign.name}</DialogTitle>
                <DialogDescription>
                  {summary.campaign.note || "（无备注）"} · 删战役只解绑任务，不删任务本身。
                </DialogDescription>
              </DialogHeader>
              <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-6">
                <div className="flex flex-wrap gap-2">
                  <Badge variant="secondary">任务 {summary.totals.tasks}</Badge>
                  <Badge variant="default">漏洞 {summary.totals.findings}</Badge>
                  <Badge variant="outline">据点 {summary.totals.footholds}</Badge>
                  <Badge variant="outline">凭据 {summary.totals.credentials}</Badge>
                  <Badge variant="outline">检测验证 {summary.totals.detections}</Badge>
                </div>

                {summary.techniques && Object.keys(summary.techniques).length > 0 && (
                  <div>
                    <p className="mb-1 text-sm font-medium">技术覆盖（ATT&CK）</p>
                    <div className="flex flex-wrap gap-1.5">
                      {Object.entries(summary.techniques)
                        .sort(([a], [b]) => a.localeCompare(b))
                        .map(([id, n]) => (
                          <Badge key={id} variant="outline">
                            {id} · {n}
                          </Badge>
                        ))}
                    </div>
                  </div>
                )}

                {summary.detection_results && Object.keys(summary.detection_results).length > 0 && (
                  <div>
                    <p className="mb-1 text-sm font-medium">检测验证</p>
                    <div className="flex flex-wrap gap-1.5">
                      {(
                        [
                          ["triggered", "已检出"],
                          ["not_triggered", "未检出"],
                          ["unknown", "无法判定"],
                        ] as const
                      ).map(([k, label]) => (
                        <Badge key={k} variant="outline">
                          {label} {summary.detection_results?.[k] ?? 0}
                        </Badge>
                      ))}
                    </div>
                    <p className="text-muted-foreground mt-1 text-xs">
                      「无法判定」不等于「未被检出」——三态分列，不合成检出率。
                    </p>
                  </div>
                )}

                <div>
                  <div className="mb-2 flex items-center justify-between">
                    <p className="text-sm font-medium">任务（{summary.tasks.length}）</p>
                    {candidates.length > 0 && (
                      <Select
                        value=""
                        onValueChange={(v) => void setMembers(summary, [...memberIds, Number(v)])}
                        disabled={busy}
                      >
                        <SelectTrigger className="h-7 w-56">
                          <SelectValue placeholder="加入任务…" />
                        </SelectTrigger>
                        <SelectContent>
                          {candidates.map((t) => (
                            <SelectItem key={t.id} value={String(t.id)}>
                              {t.name?.trim() || t.description}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    )}
                  </div>
                  {summary.tasks.length === 0 ? (
                    <p className="text-muted-foreground text-sm">还没有任务归属到这个战役。</p>
                  ) : (
                    <ul className="space-y-1.5">
                      {summary.tasks.map((t) => (
                        <li key={t.id} className="flex items-center justify-between gap-3 rounded-md border px-3 py-2">
                          <div className="min-w-0">
                            <div className="truncate text-sm">{t.name || `任务 ${t.id}`}</div>
                            <div className="text-muted-foreground text-xs">
                              {t.status} · 漏洞 {t.findings} · 据点 {t.footholds} · 凭据 {t.credentials} · 检测{" "}
                              {t.detections}
                            </div>
                          </div>
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={busy}
                            onClick={() => void setMembers(summary, memberIds.filter((id) => id !== t.id))}
                          >
                            移出
                          </Button>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              </div>
              <DialogFooter className="border-t p-6">
                <Button variant="destructive" onClick={() => void remove()} disabled={busy}>
                  <Trash2Icon className="size-4" /> 删除战役
                </Button>
                <Button variant="outline" onClick={() => setSummary(null)} disabled={busy}>
                  关闭
                </Button>
              </DialogFooter>
            </>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
