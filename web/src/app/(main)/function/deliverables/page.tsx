"use client";

import * as React from "react";

import { FileCheck2Icon, FileDownIcon, GitCompareIcon, PlusIcon, SendIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
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
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Deliverable, DeliverableDetail, DeliverableVersion } from "@/lib/types";

function fmtTime(value: string): string {
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

function CreateDeliverableDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => void;
}) {
  const [name, setName] = React.useState("");
  const [kind, setKind] = React.useState("pentest");
  const [taskID, setTaskID] = React.useState("");
  const [companyID, setCompanyID] = React.useState("");
  const [campaignID, setCampaignID] = React.useState("");
  const [reportMD, setReportMD] = React.useState("");
  const [note, setNote] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [loadingReport, setLoadingReport] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setName("");
    setKind("pentest");
    setTaskID("");
    setCompanyID("");
    setCampaignID("");
    setReportMD("");
    setNote("");
  }, [open]);

  async function fillFromTask() {
    const id = taskID.trim();
    if (!id) {
      toast.error("先填写任务 ID");
      return;
    }
    setLoadingReport(true);
    try {
      setReportMD(await api.report(id));
      toast.success("已读取任务报告，可继续编辑");
    } catch (e) {
      toast.error(`读取任务报告失败：${(e as Error).message}`);
    } finally {
      setLoadingReport(false);
    }
  }

  async function submit() {
    if (!name.trim()) {
      toast.error("请填写交付物名称");
      return;
    }
    setBusy(true);
    try {
      await api.createDeliverable({
        name: name.trim(),
        kind,
        task_id: taskID.trim() ? Number(taskID) : undefined,
        company_id: companyID.trim() ? Number(companyID) : undefined,
        campaign_id: campaignID.trim() ? Number(campaignID) : undefined,
        report_md: reportMD,
        note: note.trim(),
      });
      toast.success("交付物已创建");
      onOpenChange(false);
      onCreated();
    } catch (e) {
      toast.error(`创建失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>新建交付物</DialogTitle>
          <DialogDescription>交付物是发给客户的正式版本；可以从任务报告带入第一版。</DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="deliverable-name">名称</FieldLabel>
              <Input
                id="deliverable-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如：Acme 渗透测试报告"
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="deliverable-kind">类型</FieldLabel>
              <Select value={kind} onValueChange={setKind}>
                <SelectTrigger id="deliverable-kind">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="pentest">渗透测试</SelectItem>
                  <SelectItem value="src">SRC 交付</SelectItem>
                  <SelectItem value="retest">复测</SelectItem>
                  <SelectItem value="other">其他</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field>
              <FieldLabel htmlFor="deliverable-task">任务 ID（可选）</FieldLabel>
              <div className="flex gap-2">
                <Input
                  id="deliverable-task"
                  value={taskID}
                  onChange={(e) => setTaskID(e.target.value)}
                  placeholder="例如 7"
                />
                <Button type="button" variant="outline" onClick={() => void fillFromTask()} disabled={loadingReport}>
                  {loadingReport ? "读取中…" : "读取报告"}
                </Button>
              </div>
            </Field>
            <Field>
              <FieldLabel htmlFor="deliverable-company">企业 ID（可选）</FieldLabel>
              <Input id="deliverable-company" value={companyID} onChange={(e) => setCompanyID(e.target.value)} />
            </Field>
            <Field>
              <FieldLabel htmlFor="deliverable-campaign">战役 ID（可选）</FieldLabel>
              <Input id="deliverable-campaign" value={campaignID} onChange={(e) => setCampaignID(e.target.value)} />
            </Field>
          </div>
          <Field>
            <FieldLabel htmlFor="deliverable-report">第一版正文（Markdown）</FieldLabel>
            <Textarea
              id="deliverable-report"
              value={reportMD}
              onChange={(e) => setReportMD(e.target.value)}
              rows={12}
              placeholder="可粘贴 Markdown，或从任务报告读取"
            />
            <FieldDescription>保存后仍可继续追加版本；签收会绑定到具体版本。</FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor="deliverable-note">版本备注</FieldLabel>
            <Input
              id="deliverable-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="例如：初稿"
            />
          </Field>
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            {busy ? "创建中…" : "创建"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DeliverableDetailDialog({
  id,
  open,
  onOpenChange,
  onChanged,
}: {
  id: number | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onChanged: () => void;
}) {
  const [detail, setDetail] = React.useState<DeliverableDetail | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [reportMD, setReportMD] = React.useState("");
  const [note, setNote] = React.useState("");
  const [fromID, setFromID] = React.useState("");
  const [toID, setToID] = React.useState("");
  const [diff, setDiff] = React.useState("");
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(() => {
    if (!id) return;
    setLoading(true);
    api
      .deliverable(id)
      .then((next) => {
        setDetail(next);
        setReportMD("");
        setNote("");
      })
      .catch((e: Error) => toast.error(`读取交付物失败：${e.message}`))
      .finally(() => setLoading(false));
  }, [id]);

  React.useEffect(() => {
    if (open) load();
  }, [load, open]);

  async function addVersion() {
    if (!id || !reportMD.trim()) return;
    setBusy(true);
    try {
      await api.addDeliverableVersion(id, reportMD, note.trim());
      toast.success("已追加版本");
      load();
      onChanged();
    } catch (e) {
      toast.error(`追加失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function accept(version: DeliverableVersion) {
    if (!id) return;
    setBusy(true);
    try {
      await api.acceptDeliverable(id, version.id, note.trim());
      toast.success(`已签收 v${version.version}`);
      load();
      onChanged();
    } catch (e) {
      toast.error(`签收失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function compare() {
    if (!id || !fromID || !toID) {
      toast.error("选择两个版本再对比");
      return;
    }
    setBusy(true);
    try {
      setDiff(await api.deliverableDiff(id, Number(fromID), Number(toID)));
    } catch (e) {
      toast.error(`对比失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  const versions = detail?.versions ?? [];
  const deliverable = detail?.deliverable;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-5xl">
        <DialogHeader>
          <DialogTitle>{deliverable?.name ?? "交付物"}</DialogTitle>
          <DialogDescription>
            {deliverable
              ? `#${deliverable.id} · ${deliverable.status === "accepted" ? "已签收" : "草稿"} · 最新 v${deliverable.latest_version}`
              : ""}
          </DialogDescription>
        </DialogHeader>
        {loading ? (
          <div className="flex min-h-40 items-center justify-center">
            <Spinner />
          </div>
        ) : !detail ? (
          <p className="text-muted-foreground text-sm">交付物不存在。</p>
        ) : (
          <div className="flex flex-col gap-5">
            <div className="grid gap-3 lg:grid-cols-2">
              <div className="flex flex-col gap-2">
                <h3 className="font-medium text-sm">版本</h3>
                {versions.length === 0 ? <p className="text-muted-foreground text-sm">还没有版本。</p> : null}
                {versions.map((version) => (
                  <div key={version.id} className="rounded-md border px-3 py-2">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="flex items-center gap-2 font-medium text-sm">
                        v{version.version}
                        <span className="text-muted-foreground text-xs">{fmtTime(version.created_at)}</span>
                      </span>
                      <span className="flex items-center gap-1">
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() =>
                            void api
                              .exportDeliverableVersion(detail.deliverable.id, version.id, "md")
                              .catch((e: Error) => toast.error(e.message))
                          }
                        >
                          <FileDownIcon /> md
                        </Button>
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() =>
                            void api
                              .exportDeliverableVersion(detail.deliverable.id, version.id, "docx")
                              .catch((e: Error) => toast.error(e.message))
                          }
                        >
                          <FileDownIcon /> docx
                        </Button>
                        <Button size="sm" onClick={() => void accept(version)} disabled={busy}>
                          <SendIcon /> 签收
                        </Button>
                      </span>
                    </div>
                    {version.note ? <p className="mt-1 text-muted-foreground text-xs">{version.note}</p> : null}
                    <code className="mt-1 block truncate text-muted-foreground text-[10px]">
                      sha256 {version.sha256}
                    </code>
                  </div>
                ))}
              </div>
              <div className="flex flex-col gap-2">
                <h3 className="font-medium text-sm">签收记录</h3>
                {detail.acceptances.length === 0 ? (
                  <p className="text-muted-foreground text-sm">还没有签收记录。</p>
                ) : null}
                {detail.acceptances.map((acceptance) => (
                  <div key={acceptance.id} className="rounded-md border px-3 py-2 text-sm">
                    <div>
                      v{acceptance.version} · {acceptance.accepted_by}
                    </div>
                    <div className="text-muted-foreground text-xs">
                      {fmtTime(acceptance.accepted_at)}
                      {acceptance.note ? ` · ${acceptance.note}` : ""}
                    </div>
                  </div>
                ))}
              </div>
            </div>

            <div className="rounded-md border p-3">
              <h3 className="mb-2 flex items-center gap-1.5 font-medium text-sm">
                <GitCompareIcon className="size-4" /> 版本对比
              </h3>
              <div className="flex flex-wrap items-center gap-2">
                <Select value={fromID} onValueChange={setFromID}>
                  <SelectTrigger size="sm" className="w-28">
                    <SelectValue placeholder="从版本" />
                  </SelectTrigger>
                  <SelectContent>
                    {versions.map((v) => (
                      <SelectItem key={v.id} value={String(v.id)}>
                        v{v.version}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <span className="text-muted-foreground text-xs">→</span>
                <Select value={toID} onValueChange={setToID}>
                  <SelectTrigger size="sm" className="w-28">
                    <SelectValue placeholder="到版本" />
                  </SelectTrigger>
                  <SelectContent>
                    {versions.map((v) => (
                      <SelectItem key={v.id} value={String(v.id)}>
                        v{v.version}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button size="sm" variant="outline" onClick={() => void compare()} disabled={busy}>
                  对比
                </Button>
              </div>
              {diff ? (
                <pre className="mt-2 max-h-60 overflow-auto rounded bg-muted p-2 font-mono text-xs">{diff}</pre>
              ) : null}
            </div>

            <div className="rounded-md border p-3">
              <h3 className="mb-2 font-medium text-sm">追加版本</h3>
              <Textarea
                value={reportMD}
                onChange={(e) => setReportMD(e.target.value)}
                rows={8}
                placeholder="新版本的 Markdown 正文"
              />
              <Input className="mt-2" value={note} onChange={(e) => setNote(e.target.value)} placeholder="版本备注" />
              <Button className="mt-2" size="sm" onClick={() => void addVersion()} disabled={busy || !reportMD.trim()}>
                <PlusIcon /> 保存为新版本
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

export default function DeliverablesPage() {
  const [rows, setRows] = React.useState<Deliverable[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [createOpen, setCreateOpen] = React.useState(false);
  const [detailID, setDetailID] = React.useState<number | null>(null);
  const [deleteTarget, setDeleteTarget] = React.useState<Deliverable | null>(null);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .deliverables()
      .then(setRows)
      .catch((e: Error) => toast.error(`读取交付物失败：${e.message}`))
      .finally(() => setLoading(false));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  async function remove() {
    if (!deleteTarget) return;
    try {
      await api.deleteDeliverable(deleteTarget.id);
      toast.success("交付物已删除");
      setDeleteTarget(null);
      load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
            <FileCheck2Icon className="size-5" /> 交付物
          </h1>
          <p className="text-muted-foreground text-sm">版本、签收与导出；报告正文仍由任务报告生成。</p>
        </div>
        <Button size="sm" onClick={() => setCreateOpen(true)}>
          <PlusIcon /> 新建交付物
        </Button>
      </div>

      <Card className="py-0">
        <CardContent className="px-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead className="w-24">类型</TableHead>
                <TableHead className="w-24">状态</TableHead>
                <TableHead className="w-24 text-right">最新版本</TableHead>
                <TableHead className="w-24 text-right">签收</TableHead>
                <TableHead className="w-40">更新时间</TableHead>
                <TableHead className="w-20" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="py-12 text-center text-muted-foreground">
                    {loading ? "加载中…" : "还没有交付物。"}
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((row) => (
                  <TableRow key={row.id} className="cursor-pointer" onClick={() => setDetailID(row.id)}>
                    <TableCell>
                      <div className="font-medium">{row.name}</div>
                      <div className="text-muted-foreground text-xs">
                        #{row.id}
                        {row.task_id ? ` · 任务 #${row.task_id}` : ""}
                        {row.company_id ? ` · 企业 #${row.company_id}` : ""}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant="outline">{row.kind}</Badge>
                    </TableCell>
                    <TableCell>
                      <StatusBadge domain="delivery" value={row.status} />
                    </TableCell>
                    <TableCell className="text-right text-sm tabular-nums">
                      {row.latest_version === 0 ? "—" : `v${row.latest_version}`}
                    </TableCell>
                    <TableCell className="text-right text-sm tabular-nums">{row.acceptance_count}</TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(row.updated_at)}</TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        className="text-muted-foreground hover:text-destructive"
                        onClick={(e) => {
                          e.stopPropagation();
                          setDeleteTarget(row);
                        }}
                        aria-label="删除交付物"
                      >
                        <Trash2Icon />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <CreateDeliverableDialog open={createOpen} onOpenChange={setCreateOpen} onCreated={load} />
      <DeliverableDetailDialog
        id={detailID}
        open={detailID !== null}
        onOpenChange={(open) => !open && setDetailID(null)}
        onChanged={load}
      />
      <AlertDialog open={deleteTarget !== null} onOpenChange={(open) => !open && setDeleteTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除交付物</AlertDialogTitle>
            <AlertDialogDescription>
              将删除「{deleteTarget?.name}」及其全部版本与签收记录，此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={(e) => {
                e.preventDefault();
                void remove();
              }}
            >
              确认删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
