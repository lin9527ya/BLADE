"use client";

import * as React from "react";

import { HardDriveIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { StorageCategoryReport, StorageReport } from "@/lib/types";

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
};

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
          <div className="text-muted-foreground grid gap-0.5 text-xs">
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
  const [lastClean, setLastClean] = React.useState<Record<string, unknown>>({});
  const [loading, setLoading] = React.useState(true);
  const [cleaning, setCleaning] = React.useState(false);
  const [confirmOpen, setConfirmOpen] = React.useState(false);
  // 保留天数的本地草稿：改完点「保存」才落库（避免边打字边触发清理判定）。
  const [days, setDays] = React.useState<RetentionPatch>(ZERO_DAYS);
  const [savingDays, setSavingDays] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const r = await api.storageReport();
      setReport(r.report);
      setLastClean(r.last_clean ?? {});
      const s = await api.settings();
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
      });
    } catch (e) {
      toast.error("读取存储体检失败：" + String((e as Error)?.message ?? e));
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
      toast.error("保存失败：" + String((e as Error)?.message ?? e));
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
    } catch (e) {
      toast.error("清理失败：" + String((e as Error)?.message ?? e));
    } finally {
      setCleaning(false);
      setConfirmOpen(false);
    }
  }

  const cleanable = (report?.categories ?? []).filter((c) => !c.stats_only);
  const pendingUnits = cleanable.reduce((n, c) => n + c.expired_units, 0);
  const pendingBytes = cleanable.reduce((n, c) => n + c.expired_bytes, 0);
  const diskUsedPct =
    report && report.disk_total > 0
      ? Math.round(((report.disk_total - report.disk_free) / report.disk_total) * 100)
      : 0;

  return (
    <div className="flex flex-1 flex-col gap-6">
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1">
          <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight">
            <HardDriveIcon className="size-5" /> 存储
          </h1>
          <p className="text-muted-foreground mt-1 text-sm">
            体检数据目录下的产物占用，按分类设保留天数后再清。只删超过保留期、且不在跑的任务产物； 保留天数 0 =
            不清理（默认全 0，装上什么都不会动）。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
            <RefreshCwIcon className={loading ? "size-4 animate-spin" : "size-4"} /> 重新试算
          </Button>
          <Button size="sm" variant="destructive" disabled={pendingUnits === 0} onClick={() => setConfirmOpen(true)}>
            <Trash2Icon className="size-4" /> 执行清理（{pendingUnits} 项）
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
                // 本平台没实现磁盘用量（见 server/storage_usage_windows.go）——如实说"未知"，
                // 不显示一个假的 0%。清理判定本来就不依赖它。
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
    </div>
  );
}
