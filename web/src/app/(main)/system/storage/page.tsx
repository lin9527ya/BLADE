"use client";

import * as React from "react";

import { DatabaseIcon, HardDriveIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

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
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { StorageCategoryReport, StorageReport, StorageTaskItem, StorageTasksReport } from "@/lib/types";

// P28 存储清理（照 CyberStrikeAI 的 Inspect → Clean 两段式）：
//   ① 试算：只读扫描每个分类的单元数/占用/将删数量，**不动盘**；
//   ② 清理：只删「超过各自保留天数、且不在跑的任务产物」，删完重新体检。
// 保留天数默认全 0（= 不清理）——装上平台不会因为你没配就自己删东西。

type RetentionPatch = {
  storage_retention_tasks_days: number;
  storage_retention_sessions_days: number;
  storage_retention_drafts_days: number;
  storage_retention_transcripts_days: number;
  storage_retention_binary_days: number;
  storage_retention_llm_usage_days: number;
  storage_retention_activity_days: number;
  storage_retention_server_logs_days: number;
  storage_retention_llm_records_days: number;
  storage_retention_llm_record_bodies_days: number;
  storage_retention_traffic_days: number;
  storage_retention_tool_executions_days: number;
  storage_retention_credential_attempts_days: number;
  storage_retention_notify_events_days: number;
  storage_retention_conversation_activity_days: number;
  storage_retention_intercept_pending_days: number;
  storage_retention_audit_events_days: number;
  storage_retention_blackboard_events_days: number;
  storage_retention_blackboard_operations_days: number;
  storage_retention_task_timeout_events_days: number;
  storage_retention_aggregate_requests_days: number;
  storage_retention_side_question_sessions_days: number;
};

const RETENTION_KEYS: Record<string, keyof RetentionPatch> = {
  tasks: "storage_retention_tasks_days",
  sessions: "storage_retention_sessions_days",
  drafts: "storage_retention_drafts_days",
  transcripts: "storage_retention_transcripts_days",
  binary: "storage_retention_binary_days",
  db_llm_usage: "storage_retention_llm_usage_days",
  db_activity: "storage_retention_activity_days",
  db_server_logs: "storage_retention_server_logs_days",
  db_llm_record_bodies: "storage_retention_llm_record_bodies_days",
  db_llm_records: "storage_retention_llm_records_days",
  traffic: "storage_retention_traffic_days",
  db_tool_executions: "storage_retention_tool_executions_days",
  db_credential_attempts: "storage_retention_credential_attempts_days",
  db_notify_events: "storage_retention_notify_events_days",
  db_conversation_activity: "storage_retention_conversation_activity_days",
  db_intercept_pending: "storage_retention_intercept_pending_days",
  db_audit_events: "storage_retention_audit_events_days",
  db_blackboard_events: "storage_retention_blackboard_events_days",
  db_blackboard_operations: "storage_retention_blackboard_operations_days",
  db_task_timeout_events: "storage_retention_task_timeout_events_days",
  db_aggregate_requests: "storage_retention_aggregate_requests_days",
  db_side_question_sessions: "storage_retention_side_question_sessions_days",
};

const ZERO_DAYS: RetentionPatch = {
  storage_retention_tasks_days: 0,
  storage_retention_sessions_days: 0,
  storage_retention_drafts_days: 0,
  storage_retention_transcripts_days: 0,
  storage_retention_binary_days: 0,
  storage_retention_llm_usage_days: 0,
  storage_retention_activity_days: 0,
  storage_retention_server_logs_days: 0,
  storage_retention_llm_records_days: 0,
  storage_retention_llm_record_bodies_days: 0,
  storage_retention_traffic_days: 0,
  storage_retention_tool_executions_days: 0,
  storage_retention_credential_attempts_days: 0,
  storage_retention_notify_events_days: 0,
  storage_retention_conversation_activity_days: 0,
  storage_retention_intercept_pending_days: 0,
  storage_retention_audit_events_days: 0,
  storage_retention_blackboard_events_days: 0,
  storage_retention_blackboard_operations_days: 0,
  storage_retention_task_timeout_events_days: 0,
  storage_retention_aggregate_requests_days: 0,
  storage_retention_side_question_sessions_days: 0,
};

type TaskCleanScopeDef = {
  key: string;
  label: string;
  hint: string;
};

const TASK_CLEAN_SCOPES: TaskCleanScopeDef[] = [
  { key: "files", label: "任务产物目录", hint: "data/tasks/<id> 磁盘工作区与下载物" },
  { key: "transcripts", label: "会话转写", hint: "data/transcripts/exp*-*.jsonl" },
  { key: "activity", label: "执行活动流", hint: "数据库 activity 步骤流" },
  { key: "side_questions", label: "旁路问答快照", hint: "数据库 side_question_sessions (/btw 快照)" },
  { key: "llm_record_bodies", label: "LLM 录制载荷", hint: "仅置空请求/响应大文本，保留元数据" },
  { key: "llm_records", label: "LLM 录制整行", hint: "彻底删除该任务 LLM 录制行" },
  { key: "tool_executions", label: "长任务执行台账", hint: "仅删已终态台账记录" },
  { key: "blackboard_logs", label: "黑板变更日志", hint: "历史变更/操作流水（不影响现有节点与发现）" },
  { key: "llm_usage", label: "LLM 用量账单", hint: "慎选：删除后该任务历史 token 统计归零" },
];

const DEFAULT_TASK_SCOPES = ["files", "transcripts", "activity", "side_questions", "llm_record_bodies"];

function fmtBytes(n: number): string {
  if (!n) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

function fmtWhen(ts?: string): string {
  if (!ts) return "—";
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? "—" : d.toLocaleString("zh-CN", { hour12: false });
}

function estimateTaskScopedBytes(task: StorageTaskItem, scopes: string[]): number {
  let sum = 0;
  const set = new Set(scopes);
  if (set.has("files")) sum += task.files_bytes;
  if (set.has("transcripts")) sum += task.transcripts_bytes;
  if (set.has("activity")) sum += task.activity_bytes;
  if (set.has("side_questions")) sum += task.side_question_bytes;
  if (set.has("llm_records")) sum += task.llm_record_bytes;
  else if (set.has("llm_record_bodies")) sum += task.llm_record_body_bytes;
  if (set.has("llm_usage")) sum += task.llm_usage_bytes;
  if (set.has("tool_executions")) sum += task.tool_execution_bytes;
  if (set.has("blackboard_logs")) sum += task.blackboard_log_bytes;
  return sum;
}

function CategoryCard({ cat }: { cat: StorageCategoryReport }) {
  const oldest = (cat.items ?? []).slice(0, 5);
  return (
    <Card className="py-0">
      <CardHeader className="border-b py-3">
        <CardTitle className="flex flex-wrap items-center gap-2 text-sm">
          {cat.label}
          {cat.stats_only ? (
            <Badge variant="outline" className="text-[11px]">
              只统计不清理
            </Badge>
          ) : (
            <Badge variant="secondary" className="text-[11px]">
              保留 {cat.retention_days} 天
            </Badge>
          )}
          {cat.protected_units > 0 && (
            <Badge variant="outline" className="text-[11px]">
              受保护 {cat.protected_units}
            </Badge>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className="grid gap-2 py-3 text-sm">
        <div className="flex flex-wrap gap-x-6 gap-y-1">
          <span>
            单元 <b className="tabular-nums">{cat.units}</b>
          </span>
          <span>
            占用 <b className="tabular-nums">{fmtBytes(cat.bytes)}</b>
          </span>
          {!cat.stats_only && (
            <span className={cat.expired_units > 0 ? "text-amber-600 dark:text-amber-400" : undefined}>
              将删 <b className="tabular-nums">{cat.expired_units}</b> 项 /{" "}
              <b className="tabular-nums">{fmtBytes(cat.expired_bytes)}</b>
            </span>
          )}
        </div>
        {cat.stats_only && <p className="text-muted-foreground text-xs leading-relaxed">{cat.stats_only}</p>}
        {oldest.length > 0 && (
          <div className="grid gap-0.5 text-muted-foreground text-xs">
            <span>最早的几项：</span>
            {oldest.map((u) => (
              <span key={u.path} className="font-mono" title={u.path}>
                {u.name} · {fmtBytes(u.bytes)} · {fmtWhen(u.mod_time)}
                {u.protected ? ` · ${u.protected}` : ""}
              </span>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

export default function StoragePage() {
  const [report, setReport] = React.useState<StorageReport | null>(null);
  const [tasksReport, setTasksReport] = React.useState<StorageTasksReport | null>(null);
  const [lastClean, setLastClean] = React.useState<Record<string, unknown>>({});
  const [loading, setLoading] = React.useState(true);
  const [cleaning, setCleaning] = React.useState(false);
  const [confirmOpen, setConfirmOpen] = React.useState(false);
  const [days, setDays] = React.useState<RetentionPatch>(ZERO_DAYS);
  const [savingDays, setSavingDays] = React.useState(false);

  // Per-task precise cleanup state
  const [selectedTaskIDs, setSelectedTaskIDs] = React.useState<string[]>([]);
  const [taskScopes, setTaskScopes] = React.useState<string[]>(DEFAULT_TASK_SCOPES);
  const [taskConfirmOpen, setTaskConfirmOpen] = React.useState(false);
  const [taskConfirmIDs, setTaskConfirmIDs] = React.useState<string[]>([]);
  const [cleaningTasks, setCleaningTasks] = React.useState(false);

  // Postgres VACUUM state
  const [vacuumOpen, setVacuumOpen] = React.useState(false);
  const [vacuumFull, setVacuumFull] = React.useState(true);
  const [vacuuming, setVacuuming] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const [r, tr, s] = await Promise.all([api.storageReport(), api.storageTasks(), api.settings()]);
      setReport(r.report);
      setLastClean(r.last_clean ?? {});
      setTasksReport(tr.report);
      setSelectedTaskIDs((prev) =>
        prev.filter((id) => tr.report.tasks.some((t) => t.task_id === id && !t.active_running)),
      );
      setDays({
        storage_retention_tasks_days: s.storage_retention_tasks_days ?? 0,
        storage_retention_sessions_days: s.storage_retention_sessions_days ?? 0,
        storage_retention_drafts_days: s.storage_retention_drafts_days ?? 0,
        storage_retention_transcripts_days: s.storage_retention_transcripts_days ?? 0,
        storage_retention_binary_days: s.storage_retention_binary_days ?? 0,
        storage_retention_llm_usage_days: s.storage_retention_llm_usage_days ?? 0,
        storage_retention_activity_days: s.storage_retention_activity_days ?? 0,
        storage_retention_server_logs_days: s.storage_retention_server_logs_days ?? 0,
        storage_retention_llm_records_days: s.storage_retention_llm_records_days ?? 0,
        storage_retention_llm_record_bodies_days: s.storage_retention_llm_record_bodies_days ?? 0,
        storage_retention_traffic_days: s.storage_retention_traffic_days ?? 0,
        storage_retention_tool_executions_days: s.storage_retention_tool_executions_days ?? 0,
        storage_retention_credential_attempts_days: s.storage_retention_credential_attempts_days ?? 0,
        storage_retention_notify_events_days: s.storage_retention_notify_events_days ?? 0,
        storage_retention_conversation_activity_days: s.storage_retention_conversation_activity_days ?? 0,
        storage_retention_intercept_pending_days: s.storage_retention_intercept_pending_days ?? 0,
        storage_retention_audit_events_days: s.storage_retention_audit_events_days ?? 0,
        storage_retention_blackboard_events_days: s.storage_retention_blackboard_events_days ?? 0,
        storage_retention_blackboard_operations_days: s.storage_retention_blackboard_operations_days ?? 0,
        storage_retention_task_timeout_events_days: s.storage_retention_task_timeout_events_days ?? 0,
        storage_retention_aggregate_requests_days: s.storage_retention_aggregate_requests_days ?? 0,
        storage_retention_side_question_sessions_days: s.storage_retention_side_question_sessions_days ?? 0,
      });
    } catch (e) {
      toast.error(`读取存储体检失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function saveDays() {
    setSavingDays(true);
    try {
      await api.setSettings(days);
      toast.success("保留天数已保存（对下一次清理生效）");
      await load();
    } catch (e) {
      toast.error(`保存失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setSavingDays(false);
    }
  }

  async function clean() {
    setCleaning(true);
    try {
      const r = await api.storageClean([]);
      const msg = `已删除 ${r.deleted.length} 项、释放 ${fmtBytes(r.deleted_bytes)}`;
      if (r.failures.length > 0) toast.warning(`${msg}；${r.failures.length} 项失败`);
      else toast.success(msg);
      setReport(r.report);
      setLastClean(r.last_clean ?? {});
      const tr = await api.storageTasks();
      setTasksReport(tr.report);
    } catch (e) {
      toast.error(`清理失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setCleaning(false);
      setConfirmOpen(false);
    }
  }

  function toggleTaskSelection(taskID: string, checked: boolean) {
    setSelectedTaskIDs((prev) => (checked ? [...new Set([...prev, taskID])] : prev.filter((id) => id !== taskID)));
  }

  function toggleTaskScope(scope: string, checked: boolean) {
    setTaskScopes((prev) => (checked ? [...new Set([...prev, scope])] : prev.filter((s) => s !== scope)));
  }

  function openTaskCleanConfirm(ids: string[]) {
    if (ids.length === 0 || taskScopes.length === 0) return;
    setTaskConfirmIDs(ids);
    setTaskConfirmOpen(true);
  }

  async function runCleanTasks() {
    if (taskConfirmIDs.length === 0 || taskScopes.length === 0) return;
    setCleaningTasks(true);
    try {
      const r = await api.storageCleanTasks(taskConfirmIDs, taskScopes);
      const msg = `已精确清理 ${r.cleaned.length} 个任务，释放约 ${fmtBytes(r.freed_bytes)}`;
      if (r.failures.length > 0) toast.warning(`${msg}；${r.failures.length} 项跳过：${r.failures[0]}`);
      else toast.success(msg);
      if (r.tasks_report) setTasksReport(r.tasks_report);
      if (r.global_report) setReport(r.global_report);
      setSelectedTaskIDs((prev) => prev.filter((id) => !taskConfirmIDs.includes(id)));
    } catch (e) {
      toast.error(`按任务清理失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setCleaningTasks(false);
      setTaskConfirmOpen(false);
    }
  }

  async function runVacuum() {
    setVacuuming(true);
    try {
      const r = await api.storageVacuum(vacuumFull);
      if (r.report) setReport(r.report);
      toast.success(
        r.full
          ? `已完成 ${r.vacuumed.length} 张表的空间紧缩（VACUUM FULL），空闲页已归还操作系统`
          : `已完成 ${r.vacuumed.length} 张表的无锁整理（VACUUM ANALYZE）`,
      );
      setVacuumOpen(false);
    } catch (e) {
      toast.error(`数据库空间回收失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setVacuuming(false);
    }
  }

  const cleanable = (report?.categories ?? []).filter((c) => !c.stats_only);
  const pendingUnits = cleanable.reduce((n, c) => n + c.expired_units, 0);
  const pendingBytes = cleanable.reduce((n, c) => n + c.expired_bytes, 0);
  const diskUsedPct =
    report && report.disk_total > 0
      ? Math.round(((report.disk_total - report.disk_free) / report.disk_total) * 100)
      : 0;

  const taskRows = tasksReport?.tasks ?? [];
  const taskMap = new Map(taskRows.map((t) => [t.task_id, t]));
  const confirmEstimatedBytes = taskConfirmIDs.reduce((sum, id) => {
    const item = taskMap.get(id);
    return item ? sum + estimateTaskScopedBytes(item, taskScopes) : sum;
  }, 0);

  return (
    <div className="flex flex-1 flex-col gap-6">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
            <HardDriveIcon className="size-5" /> 存储
          </h1>
          <p className="mt-1 text-muted-foreground text-sm">
            支持按任务精确查看磁盘与数据库占用并定点清理（保留任务记录、漏洞与资产），或按全局分类保留天数清理与回收数据库空洞。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
            <RefreshCwIcon className={loading ? "size-4 animate-spin" : "size-4"} /> 重新试算
          </Button>
          <Button variant="outline" size="sm" onClick={() => setVacuumOpen(true)}>
            <DatabaseIcon className="size-4" /> 回收数据库空洞
          </Button>
          <Button size="sm" variant="destructive" disabled={pendingUnits === 0} onClick={() => setConfirmOpen(true)}>
            <Trash2Icon className="size-4" /> 执行过期清理（{pendingUnits} 项）
          </Button>
        </div>
      </div>

      {report && (
        <Card className="py-0">
          <CardContent className="flex flex-wrap items-center gap-x-8 gap-y-2 py-4 text-sm">
            <span className="text-muted-foreground">
              数据目录 <span className="font-mono text-xs">{report.dir}</span>
            </span>
            <span>
              {report.disk_total > 0 ? (
                <>
                  磁盘：已用 <b className="tabular-nums">{diskUsedPct}%</b>，可用{" "}
                  <b className="tabular-nums">{fmtBytes(report.disk_free)}</b> / 共 {fmtBytes(report.disk_total)}
                </>
              ) : (
                <span className="text-muted-foreground">磁盘用量：未知（本平台未实现）</span>
              )}
            </span>
            <span className="text-muted-foreground">试算于 {fmtWhen(report.scanned_at)}</span>
            {typeof lastClean.at === "string" && (
              <span className="text-muted-foreground">
                上次清理：{fmtWhen(lastClean.at)} 删 {String(lastClean.deleted ?? 0)} 项 / 释放{" "}
                {fmtBytes(Number(lastClean.bytes ?? 0))}
              </span>
            )}
          </CardContent>
        </Card>
      )}

      {/* 按任务存储分布与精确清理 */}
      <Card className="py-0">
        <CardHeader className="border-b py-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <CardTitle className="text-base">按任务存储分布与精确清理</CardTitle>
              {tasksReport && (
                <>
                  <Badge variant="secondary" className="text-xs">
                    {taskRows.length} 个任务/残留 · 合计 {fmtBytes(tasksReport.total_bytes)}
                  </Badge>
                  {tasksReport.orphan_bytes > 0 && (
                    <Badge variant="outline" className="border-amber-500/40 text-amber-600 text-xs dark:text-amber-400">
                      孤儿残留 {fmtBytes(tasksReport.orphan_bytes)}
                    </Badge>
                  )}
                </>
              )}
            </div>
            <Button
              size="sm"
              variant="destructive"
              disabled={selectedTaskIDs.length === 0 || taskScopes.length === 0 || cleaningTasks}
              onClick={() => openTaskCleanConfirm(selectedTaskIDs)}
            >
              <Trash2Icon className="size-4" /> 清理选中任务（{selectedTaskIDs.length}）
            </Button>
          </div>
          <p className="mt-1 text-muted-foreground text-xs">
            仅清理下方勾选的冗余载荷/产物文件，<strong>绝不删除任务本身、漏洞发现、目标资产或知识图谱节点</strong>
            ；正在运行或排队中的任务需先暂停。
          </p>
        </CardHeader>
        <CardContent className="grid gap-4 py-4">
          <div className="flex flex-wrap gap-x-5 gap-y-2 rounded-md border bg-muted/30 p-3">
            {TASK_CLEAN_SCOPES.map((sc) => (
              <label key={sc.key} className="flex cursor-pointer items-center gap-1.5 text-xs" title={sc.hint}>
                <Checkbox
                  checked={taskScopes.includes(sc.key)}
                  onCheckedChange={(c) => toggleTaskScope(sc.key, c === true)}
                />
                <span>{sc.label}</span>
              </label>
            ))}
          </div>

          {taskRows.length === 0 ? (
            <p className="text-muted-foreground text-sm">{loading ? "扫描各任务占用中…" : "暂无任务存储数据。"}</p>
          ) : (
            <div className="grid gap-2">
              {taskRows.map((t) => {
                const checked = selectedTaskIDs.includes(t.task_id);
                const estFreed = estimateTaskScopedBytes(t, taskScopes);
                return (
                  <div
                    key={t.task_id}
                    className="flex flex-wrap items-center gap-3 rounded-md border px-3 py-2.5 text-sm"
                  >
                    <Checkbox
                      checked={checked}
                      disabled={t.active_running || cleaningTasks}
                      onCheckedChange={(c) => toggleTaskSelection(t.task_id, c === true)}
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="font-mono text-muted-foreground text-xs">#{t.task_id}</span>
                        <span className="truncate font-medium">{t.name}</span>
                        <Badge variant={t.orphan ? "outline" : "secondary"} className="text-[11px]">
                          {t.orphan ? "已删任务孤儿" : t.status}
                        </Badge>
                        {t.active_running && (
                          <Badge variant="outline" className="text-[11px] text-amber-600 dark:text-amber-400">
                            运行中 · 需先暂停
                          </Badge>
                        )}
                      </div>
                      <div className="mt-1 flex flex-wrap gap-x-4 gap-y-0.5 text-muted-foreground text-xs">
                        <span>产物 {fmtBytes(t.files_bytes)}</span>
                        <span>
                          转写 {fmtBytes(t.transcripts_bytes)} ({t.transcripts_count})
                        </span>
                        <span>
                          活动流 {fmtBytes(t.activity_bytes)} ({t.activity_rows})
                        </span>
                        <span>
                          旁路快照 {fmtBytes(t.side_question_bytes)} ({t.side_question_rows})
                        </span>
                        {(t.llm_record_bytes > 0 || t.llm_usage_bytes > 0) && (
                          <span>LLM 录制/用量 {fmtBytes(t.llm_record_bytes + t.llm_usage_bytes)}</span>
                        )}
                        {(t.tool_execution_bytes > 0 || t.blackboard_log_bytes > 0) && (
                          <span>台账/黑板日志 {fmtBytes(t.tool_execution_bytes + t.blackboard_log_bytes)}</span>
                        )}
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-3">
                      <div className="text-right">
                        <div className="font-semibold tabular-nums">{fmtBytes(t.total_bytes)}</div>
                        <div className="text-[11px] text-muted-foreground tabular-nums">
                          选中范围约可清 {fmtBytes(estFreed)}
                        </div>
                      </div>
                      <Button
                        size="sm"
                        variant="outline"
                        disabled={t.active_running || estFreed === 0 || taskScopes.length === 0 || cleaningTasks}
                        onClick={() => openTaskCleanConfirm([t.task_id])}
                      >
                        清理
                      </Button>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </CardContent>
      </Card>

      {cleanable.length > 0 && cleanable.every((c) => (days[RETENTION_KEYS[c.key]] ?? 0) === 0) && (
        <p className="text-muted-foreground text-xs">
          当前所有全局保留天数都是 0（<strong>永不自动清理</strong>）：库与磁盘会随使用持续增长。
          生产环境建议至少给「执行活动流」「LLM 用量」「后端日志」「旁路问答快照」设一个天数。
        </p>
      )}
      <div className="flex flex-wrap items-end gap-3">
        {cleanable.map((c) => (
          <div key={c.key} className="grid gap-1.5">
            <Label className="text-muted-foreground text-xs">{c.label}：保留天数</Label>
            <Input
              type="number"
              min={0}
              className="h-8 w-28"
              value={days[RETENTION_KEYS[c.key]] ?? 0}
              onChange={(e) =>
                setDays((d) => ({ ...d, [RETENTION_KEYS[c.key]]: Math.max(0, Number(e.target.value) || 0) }))
              }
            />
          </div>
        ))}
        <Button size="sm" onClick={() => void saveDays()} disabled={savingDays}>
          {savingDays ? "保存中…" : "保存保留天数"}
        </Button>
      </div>

      <div className="grid gap-3 lg:grid-cols-2">
        {(report?.categories ?? []).map((c) => (
          <CategoryCard key={c.key} cat={c} />
        ))}
        {!report && <p className="text-muted-foreground text-sm">{loading ? "体检中…" : "没有数据。"}</p>}
      </div>

      {/* 全局按分类清理弹窗 */}
      <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              确认清理？将删除 {pendingUnits} 项、释放约 {fmtBytes(pendingBytes)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              只删各分类里超过保留天数的单元；在跑/排队/暂停任务的产物一律跳过（执行前会重新判定一次，不是拿这份试算结果去删）。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={cleaning}>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={cleaning}
              onClick={(e) => {
                e.preventDefault();
                void clean();
              }}
            >
              {cleaning ? "清理中…" : "确认清理"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* 按任务精确清理弹窗 */}
      <AlertDialog open={taskConfirmOpen} onOpenChange={setTaskConfirmOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              确认精确清理 {taskConfirmIDs.length} 个任务（预计释放约 {fmtBytes(confirmEstimatedBytes)}）
            </AlertDialogTitle>
            <AlertDialogDescription>
              目标任务：{taskConfirmIDs.map((id) => `#${id}`).join("、")}
              。将清理所选范围，不会删除任务记录、漏洞发现、资产与知识图谱。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={cleaningTasks}>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={cleaningTasks}
              onClick={(e) => {
                e.preventDefault();
                void runCleanTasks();
              }}
            >
              {cleaningTasks ? "清理中…" : "确认清理"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Postgres VACUUM 回收空洞弹窗 */}
      <AlertDialog open={vacuumOpen} onOpenChange={setVacuumOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>回收 PostgreSQL 数据库空洞页</AlertDialogTitle>
            <AlertDialogDescription>
              删除 `server_logs`、`activity`、`side_question_sessions` 等表的旧行后，Postgres
              默认保留空闲页用于复用。紧缩（VACUUM FULL）可将已释放页面的磁盘占用真正还给操作系统。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <label className="flex cursor-pointer items-center gap-2 py-2 text-sm">
            <Checkbox checked={vacuumFull} onCheckedChange={(c) => setVacuumFull(c === true)} />
            <span>使用紧缩模式 VACUUM FULL（会将空洞空间直接还给磁盘，执行期间短暂锁表）</span>
          </label>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={vacuuming}>取消</AlertDialogCancel>
            <AlertDialogAction
              disabled={vacuuming}
              onClick={(e) => {
                e.preventDefault();
                void runVacuum();
              }}
            >
              {vacuuming ? "整理中…" : "开始回收"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
