"use client";

import * as React from "react";

import { DownloadIcon, FolderKanbanIcon, PlusIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Field } from "@/components/page-sections";
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
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api } from "@/lib/api";
import type {
  Campaign,
  CampaignAttackChains,
  CampaignPhase,
  CampaignSummary,
  DetectionMatrix,
  Task,
} from "@/lib/types";

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
  const [phases, setPhases] = React.useState<CampaignPhase[]>([]);
  const [chains, setChains] = React.useState<CampaignAttackChains | null>(null);
  const [matrix, setMatrix] = React.useState<DetectionMatrix | null>(null);
  const [planOpen, setPlanOpen] = React.useState(false);
  const [planBusy, setPlanBusy] = React.useState(false);
  const [plan, setPlan] = React.useState({ goal: "", phase: "", status: "active", starts_at: "", ends_at: "" });
  const [newPhase, setNewPhase] = React.useState("");

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
      toast.error(`加载失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void refresh();
  }, [refresh]);

  const openDetail = async (id: number) => {
    try {
      const next = await api.campaign(id);
      setSummary(next);
      setPlan({
        goal: next.campaign.goal ?? "",
        phase: next.campaign.phase ?? "",
        status: next.campaign.status || "active",
        starts_at: next.campaign.starts_at ? next.campaign.starts_at.slice(0, 16) : "",
        ends_at: next.campaign.ends_at ? next.campaign.ends_at.slice(0, 16) : "",
      });
      setPhases(next.phases ?? []);
      setChains(null);
      setMatrix(null);
      void api
        .campaignPhases(id)
        .then(setPhases)
        .catch(() => undefined);
      void api
        .campaignAttackChains(id)
        .then(setChains)
        .catch(() => undefined);
      void api
        .detectionMatrix({ campaign: id })
        .then(setMatrix)
        .catch(() => undefined);
    } catch (e) {
      toast.error(`打开失败：${String((e as Error)?.message ?? e)}`);
    }
  };

  const savePlan = async () => {
    if (!summary) return;
    setPlanBusy(true);
    try {
      await api.updateCampaign(summary.campaign.id, {
        goal: plan.goal,
        phase: plan.phase,
        status: plan.status,
        starts_at: plan.starts_at,
        ends_at: plan.ends_at,
      });
      toast.success("计划已保存");
      setPlanOpen(false);
      await openDetail(summary.campaign.id);
      await refresh();
    } catch (e) {
      toast.error(`保存失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setPlanBusy(false);
    }
  };

  const addPhase = async () => {
    if (!summary || !newPhase.trim()) return;
    try {
      await api.createCampaignPhase(summary.campaign.id, { name: newPhase.trim(), position: phases.length });
      setNewPhase("");
      setPhases(await api.campaignPhases(summary.campaign.id));
      toast.success("阶段已添加");
    } catch (e) {
      toast.error(`添加阶段失败：${String((e as Error)?.message ?? e)}`);
    }
  };

  const dropPhase = async (pid: number) => {
    if (!summary) return;
    try {
      await api.deleteCampaignPhase(summary.campaign.id, pid);
      setPhases(await api.campaignPhases(summary.campaign.id));
      await openDetail(summary.campaign.id);
    } catch (e) {
      toast.error(`删除阶段失败：${String((e as Error)?.message ?? e)}`);
    }
  };

  const assignPhase = async (tid: number, phaseId: number) => {
    if (!summary) return;
    try {
      await api.setCampaignTaskPhase(summary.campaign.id, tid, phaseId);
      await openDetail(summary.campaign.id);
    } catch (e) {
      toast.error(`调整阶段失败：${String((e as Error)?.message ?? e)}`);
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
      toast.error(/已存在同名战役/.test(msg) ? "已存在同名战役" : `创建失败：${msg}`);
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
      toast.error(`保存归属失败：${String((e as Error)?.message ?? e)}`);
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
      toast.error(`删除失败：${String((e as Error)?.message ?? e)}`);
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
          <h1 className="font-semibold text-xl tracking-tight">战役</h1>
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
            <CardContent className="py-12 text-center text-muted-foreground text-sm">
              还没有战役。把同一轮作业下的任务归到一起，就能在这里看到汇总的战果与技术覆盖。
            </CardContent>
          </Card>
        ) : (
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
            {rows.map((c) => (
              <Card
                key={c.id}
                className="cursor-pointer py-0 transition-colors hover:border-foreground/20"
                onClick={() => void openDetail(c.id)}
              >
                <CardContent className="p-4">
                  <div className="flex items-start justify-between gap-2">
                    <div className="flex items-center gap-2">
                      <FolderKanbanIcon className="size-4 text-muted-foreground" />
                      <span className="font-medium">{c.name}</span>
                    </div>
                    <div className="flex items-center gap-1">
                      <Badge variant={c.status === "active" ? "default" : "secondary"}>{c.status || "active"}</Badge>
                      <Badge variant="outline">{c.task_count ?? 0} 个任务</Badge>
                    </div>
                  </div>
                  {(c.goal || c.phase) && (
                    <p className="mt-2 line-clamp-2 text-muted-foreground text-xs">
                      {c.phase ? `[${c.phase}] ` : ""}
                      {c.goal || ""}
                    </p>
                  )}
                  {c.note && <p className="mt-2 line-clamp-2 text-muted-foreground text-xs">{c.note}</p>}
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

      <Dialog open={planOpen} onOpenChange={setPlanOpen}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>编辑战役计划</DialogTitle>
            <DialogDescription>目标/阶段/时间窗只用于展示与交接，不参与 RoE，也不会自动写任务范围。</DialogDescription>
          </DialogHeader>
          <Field label="目标" htmlFor="campaign-goal">
            <Input
              id="campaign-goal"
              value={plan.goal}
              onChange={(e) => setPlan({ ...plan, goal: e.target.value })}
              placeholder="本轮作业要达成什么"
            />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="阶段" htmlFor="campaign-phase">
              <Input
                id="campaign-phase"
                value={plan.phase}
                onChange={(e) => setPlan({ ...plan, phase: e.target.value })}
                placeholder="如 外网打点"
              />
            </Field>
            <Field label="状态" htmlFor="campaign-status">
              <select
                id="campaign-status"
                className="h-9 rounded-md border bg-transparent px-2 text-sm"
                value={plan.status}
                onChange={(e) => setPlan({ ...plan, status: e.target.value })}
              >
                {["active", "paused", "done", "archived"].map((v) => (
                  <option key={v} value={v}>
                    {v}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="开始（RFC3339，可空）" htmlFor="campaign-starts">
              <Input
                id="campaign-starts"
                value={plan.starts_at}
                onChange={(e) => setPlan({ ...plan, starts_at: e.target.value })}
                placeholder="2026-11-17T09:00:00+08:00"
              />
            </Field>
            <Field label="结束（RFC3339，可空）" htmlFor="campaign-ends">
              <Input
                id="campaign-ends"
                value={plan.ends_at}
                onChange={(e) => setPlan({ ...plan, ends_at: e.target.value })}
                placeholder="2026-11-20T18:00:00+08:00"
              />
            </Field>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPlanOpen(false)} disabled={planBusy}>
              取消
            </Button>
            <Button onClick={() => void savePlan()} disabled={planBusy}>
              {planBusy ? "保存中…" : "保存"}
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
                <div className="flex flex-wrap items-center gap-2 pt-2">
                  <Badge variant={summary.campaign.status === "active" ? "default" : "secondary"}>
                    {summary.campaign.status || "active"}
                  </Badge>
                  {summary.campaign.phase && <Badge variant="outline">阶段 {summary.campaign.phase}</Badge>}
                  {summary.campaign.starts_at && (
                    <span className="text-muted-foreground text-xs">
                      开始 {summary.campaign.starts_at.slice(0, 16)}
                    </span>
                  )}
                  {summary.campaign.ends_at && (
                    <span className="text-muted-foreground text-xs">结束 {summary.campaign.ends_at.slice(0, 16)}</span>
                  )}
                  <Button size="sm" variant="outline" className="ml-auto" onClick={() => setPlanOpen(true)}>
                    编辑计划
                  </Button>
                </div>
                {summary.campaign.goal && (
                  <p className="rounded-md border bg-muted/30 p-2 text-sm">目标：{summary.campaign.goal}</p>
                )}
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
                    <p className="mb-1 font-medium text-sm">技术覆盖（ATT&CK）</p>
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
                    <p className="mb-1 font-medium text-sm">检测验证</p>
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
                    <p className="mt-1 text-muted-foreground text-xs">
                      「无法判定」不等于「未被检出」——三态分列，不合成检出率。
                    </p>
                  </div>
                )}

                <div>
                  <p className="mb-2 font-medium text-sm">阶段</p>
                  <div className="flex flex-wrap items-center gap-2">
                    {phases.length === 0 && (
                      <span className="text-muted-foreground text-xs">还没有阶段（可选，仅用于归组与交接）。</span>
                    )}
                    {phases.map((p) => (
                      <div key={p.id} className="flex items-center gap-1 rounded-md border px-2 py-1 text-xs">
                        <span>{p.name}</span>
                        <Badge variant="outline">{p.status}</Badge>
                        <button
                          className="text-muted-foreground hover:text-destructive"
                          onClick={() => void dropPhase(p.id)}
                        >
                          ×
                        </button>
                      </div>
                    ))}
                    <Input
                      className="h-7 w-40"
                      value={newPhase}
                      onChange={(e) => setNewPhase(e.target.value)}
                      placeholder="新阶段名"
                      onKeyDown={(e) => e.key === "Enter" && void addPhase()}
                    />
                    <Button size="sm" variant="outline" onClick={() => void addPhase()} disabled={!newPhase.trim()}>
                      添加
                    </Button>
                  </div>
                </div>

                {chains && (
                  <div>
                    <div className="mb-2 flex items-center justify-between gap-2">
                      <p className="font-medium text-sm">攻击链（逐任务复用，不合成跨任务边）</p>
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() =>
                          void api
                            .downloadCampaignAttackChainsCSV(summary.campaign.id)
                            .catch((e: Error) => toast.error(e.message))
                        }
                      >
                        <DownloadIcon /> 导出 CSV
                      </Button>
                    </div>
                    {chains.tasks.length === 0 ? (
                      <p className="text-muted-foreground text-xs">暂无可用任务。</p>
                    ) : (
                      <div className="space-y-2">
                        {chains.tasks.map((t) => (
                          <div key={t.task_id} className="rounded-md border p-2 text-xs">
                            <div className="font-medium">{t.name || `任务 ${t.task_id}`}</div>
                            {t.error ? (
                              <div className="text-destructive">{t.error}</div>
                            ) : (t.chains?.length ?? 0) === 0 ? (
                              <div className="text-muted-foreground">还没有走出攻击链（需要 uses/yields 边）。</div>
                            ) : (
                              t.chains?.map((ch, i) => (
                                <div key={i} className="mt-1">
                                  <span className="font-mono">{ch.head}</span>
                                  {ch.steps.map((st, j) => (
                                    <span key={j} className="text-muted-foreground">
                                      {" → "}#{st.intent_id} {st.summary}
                                      {st.produced?.length ? `（产出 ${st.produced.join(", ")}）` : ""}
                                    </span>
                                  ))}
                                </div>
                              ))
                            )}
                          </div>
                        ))}
                      </div>
                    )}
                    {chains.missing_tasks.length > 0 && (
                      <p className="mt-1 text-muted-foreground text-xs">
                        未加载任务：{chains.missing_tasks.map((m) => `#${m.task_id}`).join(", ")}
                      </p>
                    )}
                    {chains.shared_assets.length > 0 && (
                      <p className="mt-1 text-muted-foreground text-xs">
                        共享资产：
                        {chains.shared_assets
                          .slice(0, 8)
                          .map((a) => a.label)
                          .join(", ")}
                      </p>
                    )}
                  </div>
                )}

                {matrix && matrix.techniques.length > 0 && (
                  <div>
                    <p className="mb-1 font-medium text-sm">检测矩阵</p>
                    <div className="flex flex-wrap gap-1.5 text-xs">
                      {[
                        ["triggered", "已检出"],
                        ["not_triggered", "未检出"],
                        ["unknown", "无法判定"],
                        ["unmeasured", "未记录"],
                      ].map(([k, label]) => (
                        <Badge key={k} variant="outline">
                          {label} {matrix.counts[k] ?? 0}
                        </Badge>
                      ))}
                    </div>
                    <p className="mt-1 text-muted-foreground text-xs">{matrix.note}</p>
                    <Button
                      size="sm"
                      variant="outline"
                      className="mt-2"
                      onClick={() =>
                        void api
                          .downloadDetectionMatrixCSV({ campaign: summary.campaign.id })
                          .catch((e: Error) => toast.error(e.message))
                      }
                    >
                      <DownloadIcon /> 导出 CSV
                    </Button>
                  </div>
                )}

                <div>
                  <div className="mb-2 flex items-center justify-between">
                    <p className="font-medium text-sm">任务（{summary.tasks.length}）</p>
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
                          {phases.length > 0 && (
                            <Select
                              value={String(t.phase_id ?? 0)}
                              onValueChange={(v) => void assignPhase(t.id, Number(v))}
                            >
                              <SelectTrigger className="h-7 w-36">
                                <SelectValue placeholder="阶段" />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value="0">未分配</SelectItem>
                                {phases.map((p) => (
                                  <SelectItem key={p.id} value={String(p.id)}>
                                    {p.name}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          )}
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={busy}
                            onClick={() =>
                              void setMembers(
                                summary,
                                memberIds.filter((id) => id !== t.id),
                              )
                            }
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
