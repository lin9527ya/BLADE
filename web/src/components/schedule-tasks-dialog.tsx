"use client";

import * as React from "react";

import { CalendarClockIcon, PencilIcon, PlayIcon, PlusIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { ScheduleFrequency, Task, TaskCategory, TaskSchedule, TaskTemplate } from "@/lib/types";

/**
 * ScheduleTasksDialog 管理「计划任务」（P22）：按结构化频率到点自动建任务。
 *
 * 语义（与后端 server/schedules.go 一致）：
 *  · 一份计划任务 = 一份**确定的可重放规格**（描述/目标/分类/拦截规则…）+ 一个频率；
 *    到点按规格原样建一个普通任务，之后的推送/交付全走既有链路。
 *  · 间隔型首次运行 = 创建后一个间隔（不在创建瞬间就跑；急就跑「立即运行」）。
 *  · 停机期间错过的次数**不追补**（开机只跑一次）。
 *  · 「立即运行」是额外一次，不改排期。
 */

const TIMEZONES = ["Asia/Shanghai", "Asia/Tokyo", "Asia/Singapore", "UTC", "Europe/London", "America/New_York"];

function dateInZone(zone: string): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: zone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  })
    .formatToParts(new Date())
    .filter((p) => p.type !== "literal");
  const get = (t: string) => parts.find((p) => p.type === t)?.value ?? "";
  return `${get("year")}-${get("month")}-${get("day")}`;
}

const emptyForm = () => ({
  name: "",
  description: "",
  goal: "",
  categoryID: undefined as number | undefined,
  enabled: true,
  action: "create" as "create" | "start" | "pause" | "resume",
  targetTaskID: "",
  kind: "every_hours" as ScheduleFrequency["kind"],
  everyMinutes: "30",
  everyHours: "6",
  at: "09:30",
  date: dateInZone("Asia/Shanghai"),
  weekday: 1,
  timezone: "Asia/Shanghai",
});

type FormState = ReturnType<typeof emptyForm>;

function fmtLocal(ts?: string, timeZone = "Asia/Shanghai"): string {
  if (!ts) return "—";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "—";
  try {
    return d.toLocaleString("zh-CN", {
      timeZone,
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    });
  } catch {
    return d.toLocaleString("zh-CN", { hour12: false });
  }
}

export function ScheduleTasksDialog({
  categories,
  onCreated,
  triggerLabel = "计划任务",
}: {
  categories: TaskCategory[];
  onCreated?: () => void;
  triggerLabel?: string;
}) {
  const [open, setOpen] = React.useState(false);
  const [rows, setRows] = React.useState<TaskSchedule[]>([]);
  const [templates, setTemplates] = React.useState<TaskTemplate[]>([]);
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [editingID, setEditingID] = React.useState<number | null>(null); // null = 新建
  const [formOpen, setFormOpen] = React.useState(false);
  const [form, setForm] = React.useState<FormState>(emptyForm);
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      setRows(await api.taskSchedules());
    } catch {
      // 瞬时错误忽略
    }
  }, []);

  React.useEffect(() => {
    if (!open) return;
    void load();
    void api
      .taskTemplates()
      .then(setTemplates)
      .catch(() => {});
    void api
      .tasks({ size: 200 })
      .then((r) => setTasks(r.tasks))
      .catch(() => {});
  }, [open, load]);

  function startCreate() {
    setEditingID(null);
    setForm(emptyForm());
    setFormOpen(true);
  }

  function startEdit(s: TaskSchedule) {
    setEditingID(s.id);
    setForm({
      name: s.name,
      description: s.task_spec.description ?? "",
      goal: s.task_spec.goal ?? "",
      categoryID: s.task_spec.category_id,
      enabled: s.enabled,
      action: s.action ?? "create",
      targetTaskID: s.target_task_id ?? "",
      kind: s.frequency.kind,
      everyMinutes: String(s.frequency.every_minutes ?? 30),
      everyHours: String(s.frequency.every_hours ?? 6),
      at: s.frequency.at ?? "09:30",
      date: s.frequency.date ?? dateInZone(s.timezone || "Asia/Shanghai"),
      weekday: s.frequency.weekday ?? 1,
      timezone: s.timezone || "Asia/Shanghai",
    });
    setFormOpen(true);
  }

  function applyTemplate(id: number) {
    const t = templates.find((x) => x.id === id);
    if (!t) return;
    setForm((f) => ({
      ...f,
      name: f.name || t.name,
      description: t.description,
      goal: t.goal,
      categoryID: t.category_id ?? f.categoryID,
    }));
  }

  function buildFrequency(): ScheduleFrequency {
    switch (form.kind) {
      case "every_minutes":
        return { kind: "every_minutes", every_minutes: Number(form.everyMinutes) || 0 };
      case "every_hours":
        return { kind: "every_hours", every_hours: Number(form.everyHours) || 0 };
      case "daily_at":
        return { kind: "daily_at", at: form.at };
      case "once_at":
        return { kind: "once_at", date: form.date, at: form.at };
      default:
        return { kind: "weekly_at", at: form.at, weekday: form.weekday };
    }
  }

  async function save() {
    if (form.action === "create" && (!form.description.trim() || !form.goal.trim())) {
      toast.error("创建任务时描述与目标必填");
      return;
    }
    if (form.action !== "create" && !form.targetTaskID) {
      toast.error("请选择要暂停/继续的任务");
      return;
    }
    if (form.kind === "once_at" && !form.date) {
      toast.error("请选择一次性执行的日期");
      return;
    }
    setBusy(true);
    try {
      const payload: Parameters<typeof api.createTaskSchedule>[0] = {
        name:
          form.name.trim() ||
          (form.action === "create" ? form.description.trim().slice(0, 40) : `${form.action === "pause" ? "暂停" : "继续"} #${form.targetTaskID}`),
        enabled: form.enabled,
        action: form.action,
        target_task_id: form.action === "create" ? "" : form.targetTaskID,
        timezone: form.timezone,
        frequency: buildFrequency(),
      };
      if (form.action === "create") {
        payload.task_spec = {
          description: form.description.trim(),
          goal: form.goal.trim(),
          category_id: form.categoryID,
        };
      }
      if (editingID == null) {
        await api.createTaskSchedule(payload);
        toast.success(form.enabled ? "计划任务已创建（到点自动建任务）" : "计划任务已创建（当前停用）");
      } else {
        await api.updateTaskSchedule(editingID, payload);
        toast.success("计划任务已更新");
      }
      setFormOpen(false);
      await load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function toggle(s: TaskSchedule, enabled: boolean) {
    setRows((prev) => prev.map((x) => (x.id === s.id ? { ...x, enabled } : x)));
    try {
      const updated = await api.updateTaskSchedule(s.id, { enabled });
      setRows((prev) => prev.map((x) => (x.id === s.id ? updated : x)));
      toast.success(
        enabled ? `已启用：${updated.frequency_text}（下次 ${fmtLocal(updated.next_run_at, updated.timezone)}）` : "已停用（排期已清空）",
      );
    } catch (e) {
      toast.error(`切换失败：${(e as Error).message}`);
      await load();
    }
  }

  async function runNow(s: TaskSchedule) {
    setBusy(true);
    try {
      const r = await api.runTaskScheduleNow(s.id);
      toast.success(s.action === "create" ? `已建任务 #${r.task_id}（不改排期）` : `已执行：${s.action_text} #${r.task_id}（不改排期）`);
      onCreated?.();
      await load();
    } catch (e) {
      toast.error(`运行失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function remove(s: TaskSchedule) {
    setBusy(true);
    try {
      await api.deleteTaskSchedule(s.id);
      toast.success("计划任务已删除（已建出的任务不受影响）");
      await load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          <CalendarClockIcon data-icon="inline-start" />
          {triggerLabel}
        </Button>
      </DialogTrigger>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl">
        <DialogHeader className="border-b p-6">
          <DialogTitle>计划任务</DialogTitle>
          <DialogDescription>
            到点自动创建并启动任务，或暂停/继续指定任务。支持每天/每周/指定日期时间；
            默认按北京时间（Asia/Shanghai）解释，可切换时区。停机期间错过的次数不追补。
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto p-6">
          {formOpen ? (
            <div className="grid gap-4">
              {form.action === "create" ? (
                <>
                <div className="grid gap-2">
                  <Label className="font-normal text-muted-foreground text-sm">从模板预填（可选）</Label>
                  <NativeSelect
                    size="sm"
                    value=""
                    onChange={(e) => {
                      const id = Number(e.target.value);
                      if (id) applyTemplate(id);
                    }}
                  >
                    <NativeSelectOption value="">不预填</NativeSelectOption>
                    {templates.map((t) => (
                      <NativeSelectOption key={t.id} value={String(t.id)}>
                        {t.name}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </div>
                </>
              ) : null}
              <div className="grid gap-2">
                <Label htmlFor="sched-name">名称</Label>
                <Input
                  id="sched-name"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="如：每周一 09:30 母公司资产巡检"
                  disabled={busy}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="sched-action">到点执行</Label>
                <NativeSelect
                  size="sm"
                  id="sched-action"
                  value={form.action}
                  onChange={(e) => setForm({ ...form, action: e.target.value as FormState["action"] })}
                >
                  <NativeSelectOption value="create">创建并启动任务</NativeSelectOption>
                  <NativeSelectOption value="start">启动指定任务</NativeSelectOption>
                  <NativeSelectOption value="pause">暂停指定任务</NativeSelectOption>
                  <NativeSelectOption value="resume">继续指定任务</NativeSelectOption>
                </NativeSelect>
              </div>
              {form.action !== "create" ? (
                <div className="grid gap-2">
                  <Label htmlFor="sched-target">目标任务</Label>
                  <NativeSelect
                    size="sm"
                    id="sched-target"
                    value={form.targetTaskID}
                    onChange={(e) => setForm({ ...form, targetTaskID: e.target.value })}
                  >
                    <NativeSelectOption value="">选择任务…</NativeSelectOption>
                    {tasks.map((t) => (
                      <NativeSelectOption key={t.id} value={t.id}>
                        #{t.id} {t.name || t.description}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                  <p className="text-muted-foreground text-xs">暂停要求任务正在运行；继续要求任务处于暂停状态。</p>
                </div>
              ) : null}
              {form.action === "create" ? (
                <>
                <div className="grid gap-2">
                  <Label htmlFor="sched-desc">任务描述（必填）</Label>
                  <Textarea
                    id="sched-desc"
                    rows={2}
                    value={form.description}
                    onChange={(e) => setForm({ ...form, description: e.target.value })}
                    placeholder="如：example.com 主站与 API 只读复测"
                    disabled={busy}
                  />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="sched-goal">任务目标（必填）</Label>
                  <Textarea
                    id="sched-goal"
                    rows={2}
                    value={form.goal}
                    onChange={(e) => setForm({ ...form, goal: e.target.value })}
                    placeholder="如：确认上周的越权读取已修复，并检查新上线的接口"
                    disabled={busy}
                  />
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="sched-cat">分类（可选）</Label>
                  <NativeSelect
                    size="sm"
                    id="sched-cat"
                    value={form.categoryID == null ? "" : String(form.categoryID)}
                    onChange={(e) =>
                      setForm({ ...form, categoryID: e.target.value ? Number(e.target.value) : undefined })
                    }
                  >
                    <NativeSelectOption value="">无分类</NativeSelectOption>
                    {categories.map((c) => (
                      <NativeSelectOption key={c.id} value={String(c.id)}>
                        {c.name}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </div>
                </>
              ) : null}
              <div className="grid gap-2">
                <Label htmlFor="sched-kind">频率</Label>
                <div className="flex flex-wrap items-center gap-2">
                  <NativeSelect
                    size="sm"
                    id="sched-kind"
                    value={form.kind}
                    onChange={(e) => setForm({ ...form, kind: e.target.value as FormState["kind"] })}
                  >
                    <NativeSelectOption value="every_minutes">每 N 分钟</NativeSelectOption>
                    <NativeSelectOption value="every_hours">每 N 小时</NativeSelectOption>
                    <NativeSelectOption value="daily_at">每天 HH:MM</NativeSelectOption>
                    <NativeSelectOption value="weekly_at">每周某天 HH:MM</NativeSelectOption>
                    <NativeSelectOption value="once_at">指定日期时间（仅一次）</NativeSelectOption>
                  </NativeSelect>
                  {form.kind === "every_minutes" && (
                    <Input
                      className="h-8 w-20"
                      inputMode="numeric"
                      value={form.everyMinutes}
                      onChange={(e) => setForm({ ...form, everyMinutes: e.target.value.replace(/[^0-9]/g, "") })}
                      disabled={busy}
                    />
                  )}
                  {form.kind === "every_hours" && (
                    <Input
                      className="h-8 w-20"
                      inputMode="numeric"
                      value={form.everyHours}
                      onChange={(e) => setForm({ ...form, everyHours: e.target.value.replace(/[^0-9]/g, "") })}
                      disabled={busy}
                    />
                  )}
                  {form.kind === "weekly_at" && (
                    <NativeSelect
                      size="sm"
                      value={String(form.weekday)}
                      onChange={(e) => setForm({ ...form, weekday: Number(e.target.value) })}
                    >
                      {["周日", "周一", "周二", "周三", "周四", "周五", "周六"].map((w, i) => (
                        <NativeSelectOption key={w} value={String(i)}>
                          {w}
                        </NativeSelectOption>
                      ))}
                    </NativeSelect>
                  )}
                  {form.kind === "once_at" && (
                    <Input
                      className="h-8 w-40"
                      type="date"
                      value={form.date}
                      onChange={(e) => setForm({ ...form, date: e.target.value })}
                      disabled={busy}
                    />
                  )}
                  {(form.kind === "daily_at" || form.kind === "weekly_at" || form.kind === "once_at") && (
                    <Input
                      className="h-8 w-28"
                      type="time"
                      value={form.at}
                      onChange={(e) => setForm({ ...form, at: e.target.value })}
                      disabled={busy}
                    />
                  )}
                </div>
                <p className="text-muted-foreground text-xs">
                  {form.kind === "every_minutes" || form.kind === "every_hours"
                    ? "间隔型从创建时刻起算（首次运行 = 创建后一个间隔）。"
                    : form.kind === "once_at"
                      ? "只执行一次；执行后计划任务自动停用。"
                      : "按下方时区的墙钟时间触发。"}
                </p>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="sched-timezone">时区</Label>
                <NativeSelect
                  size="sm"
                  id="sched-timezone"
                  value={form.timezone}
                  onChange={(e) => setForm({ ...form, timezone: e.target.value })}
                >
                  {[...new Set([form.timezone, ...TIMEZONES])].map((tz) => (
                    <NativeSelectOption key={tz} value={tz}>
                      {tz}
                      {tz === "Asia/Shanghai" ? "（北京时间，默认）" : ""}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
                <p className="text-muted-foreground text-xs">
                  当前 {form.timezone} 时间：{new Date().toLocaleString("zh-CN", { timeZone: form.timezone, hour12: false })}
                </p>
              </div>
              <div className="flex items-center justify-between gap-4">
                <Label htmlFor="sched-enabled" className="font-normal text-muted-foreground text-sm">
                  创建后立即启用{form.enabled ? "" : "（保持停用，之后再开）"}
                </Label>
                <Switch
                  id="sched-enabled"
                  checked={form.enabled}
                  onCheckedChange={(v) => setForm({ ...form, enabled: v })}
                />
              </div>
              <div className="flex gap-2">
                <Button size="sm" onClick={() => void save()} disabled={busy}>
                  {editingID == null ? "创建" : "保存"}
                </Button>
                <Button size="sm" variant="outline" onClick={() => setFormOpen(false)} disabled={busy}>
                  取消
                </Button>
              </div>
            </div>
          ) : (
            <div className="flex flex-col gap-3">
              <Button size="sm" onClick={startCreate}>
                <PlusIcon className="size-3.5" /> 新建计划任务
              </Button>
              {rows.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  还没有计划任务。适合「每周复测一遍这批资产」「每天看一眼有没有新上线资产」这类固定节奏的活。
                </p>
              ) : (
                rows.map((s) => (
                  <div key={s.id} className="flex flex-col gap-1.5 rounded-md border px-3 py-2">
                    <div className="flex items-center gap-2">
                      <span className="min-w-0 flex-1 truncate font-medium text-sm" title={s.name}>
                        {s.name}
                      </span>
                      <Switch checked={s.enabled} onCheckedChange={(v) => void toggle(s, v)} disabled={busy} />
                    </div>
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-muted-foreground text-xs">
                      <span>{s.action_text}{s.target_task_id ? ` #${s.target_task_id}` : ""} · {s.frequency_text} · {s.timezone}</span>
                      <span>下次 {s.enabled ? fmtLocal(s.next_run_at, s.timezone) : "（停用）"}</span>
                      <span>上次 {fmtLocal(s.last_run_at, s.timezone)}</span>
                      {s.last_task_id && (
                        <a
                          className="hover:underline"
                          href={`/function/tasks/detail?id=${encodeURIComponent(s.last_task_id)}`}
                        >
                          任务 #{s.last_task_id}
                        </a>
                      )}
                    </div>
                    {s.last_error && <span className="text-red-500 text-xs">上次失败：{s.last_error}</span>}
                    <div className="flex items-center gap-1.5">
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 px-1.5"
                        disabled={busy}
                        onClick={() => void runNow(s)}
                      >
                        <PlayIcon className="size-3.5" /> 立即运行
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 px-1.5"
                        disabled={busy}
                        onClick={() => startEdit(s)}
                      >
                        <PencilIcon className="size-3.5" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-6 px-1.5"
                        disabled={busy}
                        onClick={() => void remove(s)}
                      >
                        <Trash2Icon className="size-3.5 text-red-500" />
                      </Button>
                    </div>
                  </div>
                ))
              )}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
