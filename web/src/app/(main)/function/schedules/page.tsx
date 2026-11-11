"use client";

import * as React from "react";
import Link from "next/link";

import { CalendarClockIcon, HistoryIcon, PauseIcon, PlayIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { ScheduleTasksDialog } from "@/components/schedule-tasks-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import type { ScheduleRun, TaskCategory, TaskSchedule } from "@/lib/types";

function fmtLocal(ts?: string): string {
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

const RUN_STATE: Record<string, { label: string; cls: string }> = {
  created: { label: "已建任务", cls: "border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400" },
  failed: { label: "失败", cls: "border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-400" },
  claimed: { label: "认领中", cls: "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400" },
};

export default function SchedulesPage() {
  const [rows, setRows] = React.useState<TaskSchedule[]>([]);
  const [categories, setCategories] = React.useState<TaskCategory[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState<number | null>(null);

  const [historyOf, setHistoryOf] = React.useState<TaskSchedule | null>(null);
  const [history, setHistory] = React.useState<ScheduleRun[]>([]);
  const [historyTotal, setHistoryTotal] = React.useState(0);
  const [historyPage, setHistoryPage] = React.useState(1);
  const [historyLoading, setHistoryLoading] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [schedules, cats] = await Promise.all([api.taskSchedules(), api.taskCategories()]);
      setRows(schedules);
      setCategories(cats);
    } catch (e) {
      setError(e instanceof Error ? e.message : "读取计划任务失败");
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  React.useEffect(() => {
    if (!historyOf) return;
    let alive = true;
    setHistoryLoading(true);
    api
      .taskScheduleRuns(historyOf.id, historyPage, 20)
      .then((r) => {
        if (!alive) return;
        setHistory(r.runs);
        setHistoryTotal(r.total);
      })
      .catch((e) => {
        if (alive) toast.error(e instanceof Error ? e.message : "读取运行历史失败");
      })
      .finally(() => {
        if (alive) setHistoryLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [historyOf, historyPage]);

  function openHistory(s: TaskSchedule) {
    setHistoryOf(s);
    setHistoryPage(1);
    setHistory([]);
    setHistoryTotal(0);
  }

  async function toggle(s: TaskSchedule) {
    setBusy(s.id);
    try {
      const updated = await api.updateTaskSchedule(s.id, { enabled: !s.enabled });
      setRows((prev) => prev.map((x) => (x.id === s.id ? updated : x)));
      toast.success(updated.enabled ? `已启用（下次 ${fmtLocal(updated.next_run_at)}）` : "已停用（排期已清空）");
    } catch (e) {
      toast.error(`切换失败：${(e as Error).message}`);
    } finally {
      setBusy(null);
    }
  }

  async function runNow(s: TaskSchedule) {
    setBusy(s.id);
    try {
      const r = await api.runTaskScheduleNow(s.id);
      toast.success(`已建任务 #${r.task_id}（不改排期）`);
      await load();
    } catch (e) {
      toast.error(`运行失败：${(e as Error).message}`);
    } finally {
      setBusy(null);
    }
  }

  async function remove(s: TaskSchedule) {
    if (!window.confirm(`删除计划任务「${s.name}」？已建出的任务不受影响。`)) return;
    setBusy(s.id);
    try {
      await api.deleteTaskSchedule(s.id);
      setRows((prev) => prev.filter((x) => x.id !== s.id));
      toast.success("计划任务已删除");
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    } finally {
      setBusy(null);
    }
  }

  const totalPages = Math.max(1, Math.ceil(historyTotal / 20));

  return (
    <div className="flex flex-col gap-4 p-4 md:p-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl">
            <CalendarClockIcon className="size-5" /> 计划任务
          </h1>
          <p className="mt-1 text-muted-foreground text-sm">
            按固定节奏自动建任务。立即运行是额外一次、不改排期；停机期间错过的次数不追补。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" onClick={() => void load()} disabled={loading}>
            <RefreshCwIcon /> 刷新
          </Button>
          <ScheduleTasksDialog categories={categories} onCreated={load} triggerLabel="新建 / 编辑" />
        </div>
      </div>

      {error && (
        <Card className="border-red-500/40">
          <CardContent className="p-4 text-red-600 text-sm dark:text-red-400">{error}</CardContent>
        </Card>
      )}

      <Card>
        <CardContent className="p-0">
          {loading ? (
            <div className="flex items-center justify-center gap-2 p-12 text-muted-foreground text-sm">
              <Spinner /> 读取中…
            </div>
          ) : rows.length === 0 ? (
            <div className="p-12 text-center text-muted-foreground text-sm">
              暂无计划任务。点击右上角「新建 / 编辑」创建。
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="border-b bg-muted/40 text-muted-foreground text-xs">
                  <tr>
                    <th className="px-4 py-2 text-left font-medium">名称 / 任务</th>
                    <th className="px-4 py-2 text-left font-medium">频率</th>
                    <th className="px-4 py-2 text-left font-medium">状态</th>
                    <th className="px-4 py-2 text-left font-medium">下次运行</th>
                    <th className="px-4 py-2 text-left font-medium">上次运行</th>
                    <th className="px-4 py-2 text-right font-medium">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((s) => (
                    <tr key={s.id} className="border-b last:border-0">
                      <td className="max-w-[22rem] px-4 py-3">
                        <div className="truncate font-medium">{s.name}</div>
                        <div className="truncate text-muted-foreground text-xs" title={s.task_spec.description}>
                          {s.task_spec.description}
                        </div>
                        {s.last_error && <div className="mt-1 text-red-600 text-xs dark:text-red-400">{s.last_error}</div>}
                      </td>
                      <td className="px-4 py-3 text-muted-foreground">{s.frequency_text}</td>
                      <td className="px-4 py-3">
                        <span
                          className={
                            s.enabled
                              ? "rounded border border-emerald-500/40 bg-emerald-500/10 px-1.5 py-0.5 text-[11px] text-emerald-700 dark:text-emerald-400"
                              : "rounded border px-1.5 py-0.5 text-[11px] text-muted-foreground"
                          }
                        >
                          {s.enabled ? "启用中" : "已停用"}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-muted-foreground">{fmtLocal(s.next_run_at)}</td>
                      <td className="px-4 py-3 text-muted-foreground">
                        {fmtLocal(s.last_run_at)}
                        {s.last_task_id && (
                          <Link
                            className="ml-1 underline underline-offset-2"
                            href={`/function/tasks/detail?id=${encodeURIComponent(s.last_task_id)}`}
                          >
                            #{s.last_task_id}
                          </Link>
                        )}
                      </td>
                      <td className="px-4 py-3">
                        <div className="flex flex-wrap justify-end gap-1">
                          <Button
                            size="sm"
                            variant="ghost"
                            disabled={busy === s.id}
                            onClick={() => void toggle(s)}
                            title={s.enabled ? "停用（清空排期）" : "启用（按当前频率重排）"}
                          >
                            {s.enabled ? <PauseIcon /> : <PlayIcon />}
                            {s.enabled ? "停用" : "启用"}
                          </Button>
                          <Button size="sm" variant="ghost" disabled={busy === s.id} onClick={() => void runNow(s)}>
                            <PlayIcon /> 立即运行
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => openHistory(s)}>
                            <HistoryIcon /> 历史
                          </Button>
                          <Button size="sm" variant="ghost" disabled={busy === s.id} onClick={() => void remove(s)}>
                            <Trash2Icon /> 删除
                          </Button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      <Dialog open={historyOf !== null} onOpenChange={(open) => !open && setHistoryOf(null)}>
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <HistoryIcon className="size-4" /> 运行历史
            </DialogTitle>
            <DialogDescription>
              {historyOf ? `${historyOf.name} · ${historyOf.frequency_text} · 共 ${historyTotal} 条` : ""}
            </DialogDescription>
          </DialogHeader>
          {historyLoading ? (
            <div className="flex items-center justify-center gap-2 py-10 text-muted-foreground text-sm">
              <Spinner /> 读取中…
            </div>
          ) : history.length === 0 ? (
            <div className="py-10 text-center text-muted-foreground text-sm">还没有触发记录。</div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead className="border-b text-muted-foreground text-xs">
                  <tr>
                    <th className="py-2 text-left font-medium">计划时间</th>
                    <th className="py-2 text-left font-medium">状态</th>
                    <th className="py-2 text-left font-medium">任务</th>
                    <th className="py-2 text-left font-medium">错误</th>
                  </tr>
                </thead>
                <tbody>
                  {history.map((r) => {
                    const st = RUN_STATE[r.state] ?? { label: r.state, cls: "border-border text-muted-foreground" };
                    return (
                      <tr key={r.id} className="border-b last:border-0">
                        <td className="py-2 text-muted-foreground">{fmtLocal(r.scheduled_at)}</td>
                        <td className="py-2">
                          <span className={`rounded border px-1.5 py-0.5 text-[11px] ${st.cls}`}>{st.label}</span>
                        </td>
                        <td className="py-2">
                          {r.task_id ? (
                            <Link
                              className="underline underline-offset-2"
                              href={`/function/tasks/detail?id=${encodeURIComponent(r.task_id)}`}
                            >
                              #{r.task_id}
                            </Link>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </td>
                        <td className="max-w-[18rem] py-2 text-red-600 text-xs dark:text-red-400">{r.error ?? ""}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
          <div className="flex items-center justify-end gap-2">
            <Button
              size="sm"
              variant="outline"
              disabled={historyPage <= 1 || historyLoading}
              onClick={() => setHistoryPage((p) => Math.max(1, p - 1))}
            >
              上一页
            </Button>
            <span className="text-muted-foreground text-xs">
              {historyPage} / {totalPages}
            </span>
            <Button
              size="sm"
              variant="outline"
              disabled={historyPage >= totalPages || historyLoading}
              onClick={() => setHistoryPage((p) => p + 1)}
            >
              下一页
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
