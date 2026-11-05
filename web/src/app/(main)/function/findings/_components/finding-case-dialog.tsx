"use client";

import * as React from "react";

import { DownloadIcon, FolderIcon, SaveIcon, ThumbsDownIcon, ThumbsUpIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { FindingCase, FindingCaseStats, FindingCaseSuggestion, Severity } from "@/lib/types";

// 漏洞案例：把同一根因/同一资产的重复上报归并成一个独立缺陷。
// 原始发现保留为成员；案例持有统一报告与版本（保存时用 report_version 做 CAS）。
export function FindingCaseDialog({
  open,
  onOpenChange,
  taskID,
  selectedFindingIDs,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  taskID: string;
  selectedFindingIDs: number[];
}) {
  const [loading, setLoading] = React.useState(false);
  const [cases, setCases] = React.useState<FindingCase[]>([]);
  const [suggestions, setSuggestions] = React.useState<FindingCaseSuggestion[]>([]);
  const [stats, setStats] = React.useState<FindingCaseStats | null>(null);
  const [error, setError] = React.useState("");
  const [title, setTitle] = React.useState("");
  const [reason, setReason] = React.useState("");
  const [severity, setSeverity] = React.useState<Severity>("high");
  const [creating, setCreating] = React.useState(false);
  const [detail, setDetail] = React.useState<FindingCase | null>(null);
  const [report, setReport] = React.useState("");
  const [savingReport, setSavingReport] = React.useState(false);
  const [generating, setGenerating] = React.useState(false);
  const [busySuggestion, setBusySuggestion] = React.useState<number | null>(null);

  const load = React.useCallback(async () => {
    if (!taskID) return;
    setLoading(true);
    setError("");
    try {
      const [list, st, sug] = await Promise.all([
        api.findingCases(taskID),
        api.findingCaseStats(taskID),
        api.findingCaseSuggestions(taskID),
      ]);
      setCases(list);
      setStats(st);
      setSuggestions(sug);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [taskID]);

  React.useEffect(() => {
    if (open) void load();
  }, [open, load]);

  React.useEffect(() => {
    if (!open) {
      setDetail(null);
      setTitle("");
      setReason("");
      setReport("");
    }
  }, [open]);

  async function createCase() {
    if (!taskID || selectedFindingIDs.length === 0 || !title.trim()) return;
    setCreating(true);
    try {
      await api.createFindingCase({
        task_id: Number(taskID),
        title: title.trim(),
        reason: reason.trim(),
        severity,
        finding_ids: selectedFindingIDs,
      });
      toast.success("案例已创建");
      setTitle("");
      setReason("");
      await load();
    } catch (e) {
      toast.error(`创建失败：${(e as Error).message}`);
    } finally {
      setCreating(false);
    }
  }

  async function generateSuggestions() {
    if (!taskID) return;
    setGenerating(true);
    try {
      const r = await api.generateFindingCaseSuggestions(taskID);
      toast.success(`已生成 ${r.created} 条疑似分组建议`);
      await load();
    } catch (e) {
      toast.error(`生成建议失败：${(e as Error).message}`);
    } finally {
      setGenerating(false);
    }
  }

  async function acceptSuggestion(id: number) {
    setBusySuggestion(id);
    try {
      await api.acceptFindingCaseSuggestion(id);
      toast.success("已采纳建议并创建案例");
      await load();
    } catch (e) {
      toast.error(`采纳失败：${(e as Error).message}`);
    } finally {
      setBusySuggestion(null);
    }
  }

  async function dismissSuggestion(id: number, block: boolean, findingIDs: number[]) {
    if (!taskID) return;
    setBusySuggestion(id);
    try {
      if (block) {
        await api.blockFindingCasePair({ task_id: Number(taskID), finding_ids: findingIDs, reason: "人工否决：不是同一漏洞" });
      }
      await api.dismissFindingCaseSuggestion(id, block ? "人工否决" : "人工忽略");
      await load();
    } catch (e) {
      toast.error(`处理建议失败：${(e as Error).message}`);
    } finally {
      setBusySuggestion(null);
    }
  }

  async function removeMember(findingID: number) {
    if (!detail) return;
    try {
      await api.removeFindingCaseMember(detail.id, findingID, "人工移除");
      const c = await api.getFindingCase(detail.id);
      setDetail(c);
      setReport(c.report ?? "");
      await load();
    } catch (e) {
      toast.error(`移除失败：${(e as Error).message}`);
    }
  }

  function downloadReport() {
    if (!detail) return;
    const body = report || detail.report || "";
    const blob = new Blob([body], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `finding-case-${detail.id}.md`;
    a.click();
    URL.revokeObjectURL(url);
  }

  async function openCase(id: number) {
    try {
      const c = await api.getFindingCase(id);
      setDetail(c);
      setReport(c.report ?? "");
    } catch (e) {
      toast.error(`读取案例失败：${(e as Error).message}`);
    }
  }

  async function saveReport() {
    if (!detail) return;
    setSavingReport(true);
    try {
      const updated = await api.updateFindingCaseReport(detail.id, report, detail.report_version);
      setDetail(updated);
      setReport(updated.report ?? "");
      toast.success("统一报告已保存");
      await load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSavingReport(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <FolderIcon className="size-4" /> 漏洞案例
          </DialogTitle>
          <DialogDescription>
            把同一根因/同一资产的重复上报归并成一个独立缺陷。原始发现保留为成员，案例持有统一报告与版本。
          </DialogDescription>
        </DialogHeader>

        {!taskID ? (
          <p className="text-muted-foreground text-sm">请先在上方筛选一个任务——案例按任务归档。</p>
        ) : loading ? (
          <div className="flex items-center gap-2 text-muted-foreground text-sm">
            <Spinner /> 加载案例…
          </div>
        ) : error ? (
          <p className="text-destructive text-sm">读取失败：{error}</p>
        ) : (
          <div className="flex flex-col gap-4">
            {stats ? (
              <p className="text-muted-foreground text-xs">
                独立缺陷 {stats.distinct}（案例 {stats.cases} + 未归并 {stats.ungrouped}） · 上报记录 {stats.reports}
              </p>
            ) : null}

            <div className="flex flex-col gap-2 rounded-lg border p-3">
              <div className="flex items-center justify-between">
                <div className="text-sm font-medium">疑似分组建议</div>
                <Button size="sm" variant="outline" onClick={() => void generateSuggestions()} disabled={generating}>
                  {generating ? <Spinner data-icon="inline-start" /> : null}
                  生成建议
                </Button>
              </div>
              {suggestions.length === 0 ? (
                <p className="text-muted-foreground text-xs">暂无待处理建议；生成后按「同类型/资产重叠/名称相近」评分。</p>
              ) : (
                suggestions.map((sg) => (
                  <div key={sg.id} className="flex items-start justify-between gap-2 rounded-md bg-muted/40 px-2 py-1.5">
                    <div className="min-w-0">
                      <p className="truncate text-sm">{sg.title}</p>
                      <p className="text-muted-foreground text-xs">
                        {sg.reason} · 评分 {sg.score}
                      </p>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button size="sm" variant="outline" onClick={() => void acceptSuggestion(sg.id)} disabled={busySuggestion === sg.id}>
                        <ThumbsUpIcon /> 采纳
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => void dismissSuggestion(sg.id, false, sg.finding_ids)} disabled={busySuggestion === sg.id}>
                        <ThumbsDownIcon /> 忽略
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => void dismissSuggestion(sg.id, true, sg.finding_ids)} disabled={busySuggestion === sg.id}>
                        不是同一漏洞
                      </Button>
                    </div>
                  </div>
                ))
              )}
            </div>

            <div className="flex flex-col gap-2 rounded-lg border p-3">
              <div className="text-sm font-medium">新建案例</div>
              <div className="grid gap-2 md:grid-cols-[minmax(0,1fr)_10rem]">
                <div className="flex flex-col gap-1">
                  <Label htmlFor="case-title">标题</Label>
                  <Input
                    id="case-title"
                    value={title}
                    placeholder="例如：登录与后台 SQL 注入（同根因）"
                    onChange={(e) => setTitle(e.target.value)}
                  />
                </div>
                <div className="flex flex-col gap-1">
                  <Label htmlFor="case-severity">严重度</Label>
                  <Select value={severity} onValueChange={(v) => setSeverity(v as Severity)}>
                    <SelectTrigger id="case-severity">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="critical">严重</SelectItem>
                      <SelectItem value="high">高危</SelectItem>
                      <SelectItem value="medium">中危</SelectItem>
                      <SelectItem value="low">低危</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="case-reason">归并理由</Label>
                <Input
                  id="case-reason"
                  value={reason}
                  placeholder="为什么这些发现是同一件事（同根因/同入口/同资产）"
                  onChange={(e) => setReason(e.target.value)}
                />
              </div>
              <div className="flex items-center gap-2">
                <Button
                  size="sm"
                  onClick={() => void createCase()}
                  disabled={creating || !title.trim() || selectedFindingIDs.length === 0}
                >
                  {creating ? <Spinner data-icon="inline-start" /> : null}
                  用勾选的 {selectedFindingIDs.length} 条发现创建
                </Button>
                {selectedFindingIDs.length === 0 ? (
                  <span className="text-muted-foreground text-xs">先在列表勾选至少 1 条发现。</span>
                ) : null}
              </div>
            </div>

            <div className="flex max-h-56 flex-col gap-1 overflow-auto">
              {cases.length === 0 ? (
                <p className="text-muted-foreground text-sm">还没有案例。</p>
              ) : (
                cases.map((c) => (
                  <button
                    key={c.id}
                    type="button"
                    className="flex items-center justify-between rounded-md px-2 py-1.5 text-left text-sm hover:bg-muted"
                    onClick={() => void openCase(c.id)}
                  >
                    <span className="truncate">{c.title}</span>
                    <span className="shrink-0 text-muted-foreground text-xs">
                      {c.member_count} 条 · {c.severity || "未定级"} · v{c.report_version}
                    </span>
                  </button>
                ))
              )}
            </div>

            {detail ? (
              <div className="flex flex-col gap-2 border-t pt-3">
                <div className="flex items-center justify-between">
                  <span className="text-sm font-medium">{detail.title}</span>
                  <span className="text-muted-foreground text-xs">{detail.member_count} 条发现</span>
                </div>
                <Textarea
                  rows={10}
                  value={report}
                  placeholder="统一报告（Markdown）：概述、影响、复现、证据、修复建议…"
                  onChange={(e) => setReport(e.target.value)}
                />
                <div className="flex flex-wrap items-center gap-2">
                  <Button size="sm" onClick={() => void saveReport()} disabled={savingReport}>
                    {savingReport ? <Spinner data-icon="inline-start" /> : <SaveIcon />}
                    保存统一报告（v{detail.report_version}）
                  </Button>
                  <Button size="sm" variant="outline" onClick={downloadReport}>
                    <DownloadIcon /> 下载报告
                  </Button>
                </div>
                <div className="flex flex-col gap-1">
                  {(detail.members ?? []).length === 0 ? (
                    <p className="text-muted-foreground text-xs">没有成员。</p>
                  ) : (
                    (detail.members ?? []).map((m) => {
                      const fid = Number(m.finding_id ?? m.id);
                      return (
                        <div key={`${fid}`} className="flex items-center justify-between gap-2 text-xs">
                          <span className="min-w-0 truncate">
                            #{fid} {m.name || m.vulnclass}
                          </span>
                          <Button size="sm" variant="ghost" onClick={() => void removeMember(fid)}>
                            <Trash2Icon /> 移除
                          </Button>
                        </div>
                      );
                    })
                  )}
                </div>
              </div>
            ) : null}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
