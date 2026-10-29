"use client";

import * as React from "react";

import { BookOpenIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { Markdown } from "@/components/markdown";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
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
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Playbook } from "@/lib/types";

const PAGE_SIZE = 20;

// 人工编辑表单：除状态/统计外，卡片内容与匹配指纹都可改。
type EditForm = {
  title: string;
  summary: string;
  technique_key: string;
  confidence: string;
  technologies: string;
  favicon_mmh3: string;
  service_name: string;
  port: string;
  page_title: string;
  steps: string;
  evidence: string;
};

const EMPTY_EDIT: EditForm = {
  title: "", summary: "", technique_key: "", confidence: "high",
  technologies: "", favicon_mmh3: "", service_name: "", port: "", page_title: "",
  steps: "", evidence: "",
};

function editFormFrom(p: Playbook): EditForm {
  return {
    title: p.title,
    summary: p.summary,
    technique_key: p.technique_key,
    confidence: p.confidence || "high",
    technologies: (p.fingerprint.technologies ?? []).join(", "),
    favicon_mmh3: p.fingerprint.favicon_mmh3 ?? "",
    service_name: p.fingerprint.service_name ?? "",
    port: p.fingerprint.port ? String(p.fingerprint.port) : "",
    page_title: p.fingerprint.page_title ?? "",
    steps: p.steps,
    evidence: p.evidence,
  };
}

function splitTechnologies(raw: string): string[] {
  return raw
    .split(/[,，\n]/)
    .map((t) => t.trim())
    .filter(Boolean);
}

function fmtTime(s?: string) {
  if (!s) return "—";
  return new Date(s).toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** 窄列用：显示到分钟（年-月-日 时:分），完整秒级时间放 title。 */
function fmtMinuteTime(s?: string) {
  if (!s) return "—";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  const p2 = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p2(d.getMonth() + 1)}-${p2(d.getDate())} ${p2(d.getHours())}:${p2(d.getMinutes())}`;
}

/** 指纹摘要：技术栈 + favicon + 服务端口，卡片列表里一行看全。 */
function fingerprintParts(fp: Playbook["fingerprint"]): string[] {
  const parts: string[] = [];
  if (fp.technologies?.length) parts.push(fp.technologies.slice(0, 6).join(" · "));
  if (fp.favicon_mmh3) parts.push(`favicon ${fp.favicon_mmh3}`);
  if (fp.service_name) parts.push(fp.port ? `${fp.service_name}:${fp.port}` : fp.service_name);
  if (fp.page_title) parts.push(`「${fp.page_title}」`);
  return parts;
}

/** 成功率只统计"有实战结论"的次数：lookup 本身不算成功。 */
function winRate(p: Playbook): string {
  const total = p.successes + p.failures;
  if (total === 0) return "尚无反馈";
  return `${p.successes}/${total}（${Math.round((p.successes / total) * 100)}%）`;
}

export default function PlaybooksPage() {
  const [query, setQuery] = React.useState("");
  const [queryQ, setQueryQ] = React.useState("");
  const [status, setStatus] = React.useState("active");
  const [page, setPage] = React.useState(0);

  const [rows, setRows] = React.useState<Playbook[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(false);
  const [selected, setSelected] = React.useState<Playbook | null>(null);
  const [deprecating, setDeprecating] = React.useState<Playbook | null>(null);
  const [reason, setReason] = React.useState("");
  const [editing, setEditing] = React.useState<Playbook | null>(null);
  const [editForm, setEditForm] = React.useState<EditForm>(EMPTY_EDIT);
  const [savingEdit, setSavingEdit] = React.useState(false);

  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // 搜索/筛选变化时回到第一页。
  // biome-ignore lint/correctness/useExhaustiveDependencies: 过滤条件变化时重置分页是刻意的。
  React.useEffect(() => {
    setPage(0);
  }, [queryQ, status]);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .playbooks({ q: queryQ, status: status === "all" ? "" : status, limit: PAGE_SIZE, offset: page * PAGE_SIZE })
      .then((r) => {
        setRows(r.playbooks);
        setTotal(r.total);
      })
      .catch((e: Error) => toast.error(`加载打法库失败：${e.message}`))
      .finally(() => setLoading(false));
  }, [queryQ, status, page]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function restore(p: Playbook) {
    try {
      await api.setPlaybookStatus(p.id, "active");
      toast.success("已恢复启用");
      setSelected(null);
      load();
    } catch (e) {
      toast.error(`恢复失败：${(e as Error).message}`);
    }
  }

  function openEditor(p: Playbook) {
    setEditForm(editFormFrom(p));
    setEditing(p);
    setSelected(null);
  }

  async function saveEdit() {
    if (!editing || savingEdit) return;
    const title = editForm.title.trim();
    const steps = editForm.steps.trim();
    if (!title || !steps) {
      toast.error("标题与打法步骤必填：卡片要能被人照着打");
      return;
    }
    const port = editForm.port.trim() === "" ? 0 : Number(editForm.port);
    if (!Number.isInteger(port) || port < 0 || port > 65535) {
      toast.error("端口必须是 0–65535 的整数");
      return;
    }
    setSavingEdit(true);
    try {
      const updated = await api.updatePlaybook(editing.id, {
        fingerprint: {
          technologies: splitTechnologies(editForm.technologies),
          favicon_mmh3: editForm.favicon_mmh3.trim(),
          service_name: editForm.service_name.trim(),
          port,
          page_title: editForm.page_title.trim(),
        },
        technique_key: editForm.technique_key.trim(),
        title,
        summary: editForm.summary.trim(),
        steps,
        evidence: editForm.evidence.trim(),
        confidence: editForm.confidence,
      });
      toast.success("打法已更新");
      setEditing(null);
      setSelected(updated);
      load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSavingEdit(false);
    }
  }

  const setEditField = <K extends keyof EditForm>(key: K, value: EditForm[K]) =>
    setEditForm((f) => ({ ...f, [key]: value }));

  async function deprecate() {
    if (!deprecating) return;
    if (!reason.trim()) {
      toast.error("请填写废弃理由：后来人最需要的就是这句");
      return;
    }
    try {
      await api.setPlaybookStatus(deprecating.id, "deprecated", reason.trim());
      toast.success("已废弃，不再参与匹配");
      setDeprecating(null);
      setReason("");
      setSelected(null);
      load();
    } catch (e) {
      toast.error(`废弃失败：${(e as Error).message}`);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-lg">
            <BookOpenIcon className="size-5" />
            打法库
          </h1>
          <p className="mt-1 text-muted-foreground text-sm">
            打穿目标后自动沉淀的「目标指纹 → 攻击手法」，全局共享：下次遇到相同指纹，规划者/worker
            会自动看到并按需取完整步骤快速验证。用后的 worked/failed 反馈决定卡片排序。
          </p>
        </div>
        <div className="flex items-center gap-2">
          <div className="relative">
            <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="搜索标题 / 技术栈 / 步骤…"
              className="w-64 pl-8"
            />
          </div>
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="active">启用中</SelectItem>
              <SelectItem value="deprecated">已废弃</SelectItem>
              <SelectItem value="all">全部</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <Card className="overflow-hidden">
        {/* 与 CVE 库列表同款：固定表格布局 + 百分比列宽，内容只在列内截断，
            不再让长标题/长技术栈把「检索次数」「状态」挤成换行。 */}
        <Table className="table-fixed">
          <TableHeader>
            <TableRow>
              <TableHead className="w-[57%]">打法</TableHead>
              <TableHead className="w-[15%]">指纹</TableHead>
              <TableHead className="w-[6%] px-1">实战战绩</TableHead>
              <TableHead className="w-[4%] px-1">检索</TableHead>
              <TableHead className="w-[10%] px-1">最近验证</TableHead>
              <TableHead className="w-[4%] px-1">状态</TableHead>
              <TableHead className="w-[4%] px-1">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow>
                <TableCell colSpan={7} className="py-12 text-center text-muted-foreground">
                  加载中…
                </TableCell>
              </TableRow>
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="py-12 text-center text-muted-foreground">
                  还没有打法卡片：worker 拿到据点（report_foothold）后会自动沉淀。
                </TableCell>
              </TableRow>
            ) : (
              rows.map((p) => {
                const parts = fingerprintParts(p.fingerprint);
                return (
                  <TableRow key={p.id}>
                    <TableCell className="min-w-0">
                      <div className="flex min-w-0 items-baseline gap-2">
                        <button
                          type="button"
                          className="min-w-0 flex-1 truncate text-left font-medium hover:underline"
                          title={p.title}
                          onClick={() => setSelected(p)}
                        >
                          {p.title}
                        </button>
                        {p.confidence && (
                          <Badge variant="outline" className="shrink-0 px-1.5 text-[10px]">
                            {p.confidence}
                          </Badge>
                        )}
                      </div>
                      {p.summary && (
                        <div className="mt-0.5 line-clamp-2 break-words text-muted-foreground text-xs" title={p.summary}>
                          {p.summary}
                        </div>
                      )}
                      {p.technique_key && (
                        <div className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground/70" title={p.technique_key}>
                          {p.technique_key}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="min-w-0">
                      {parts.length === 0 ? (
                        <span className="text-muted-foreground text-xs">—</span>
                      ) : (
                        <div className="flex flex-wrap gap-1" title={parts.join(" · ")}>
                          {parts.map((part) => (
                            <Badge key={part} variant="outline" className="max-w-full truncate" title={part}>
                              {part}
                            </Badge>
                          ))}
                        </div>
                      )}
                    </TableCell>
                    <TableCell
                      className="px-1 text-[11px] leading-tight tabular-nums"
                      title={`成功 ${p.successes} / 失败 ${p.failures}`}
                    >
                      {p.successes + p.failures === 0 ? (
                        <span className="text-muted-foreground">尚无</span>
                      ) : (
                        <>
                          <div className="whitespace-nowrap">
                            {p.successes}/{p.successes + p.failures}
                          </div>
                          <div className="whitespace-nowrap text-muted-foreground">
                            {Math.round((p.successes / (p.successes + p.failures)) * 100)}%
                          </div>
                        </>
                      )}
                    </TableCell>
                    <TableCell className="px-1 text-xs tabular-nums">{p.uses}</TableCell>
                    <TableCell
                      className="px-1 text-[11px] whitespace-nowrap text-muted-foreground tabular-nums"
                      title={fmtTime(p.last_verified_at ?? p.updated_at)}
                    >
                      {fmtMinuteTime(p.last_verified_at ?? p.updated_at)}
                    </TableCell>
                    <TableCell className="px-1">
                      <StatusBadge domain="playbook" value={p.status} className="px-1 text-[10px]" />
                    </TableCell>
                    <TableCell className="px-1">
                      <Button size="sm" variant="ghost" className="px-1 text-xs" onClick={() => setSelected(p)}>
                        详情
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
        <div className="border-t px-3 py-2">
          <TablePagination
            variant="compact"
            page={page + 1}
            pageSize={PAGE_SIZE}
            total={total}
            onPageChange={(p) => setPage(p - 1)}
            busy={loading}
          />
        </div>
      </Card>

      <Dialog open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <DialogContent className="max-h-[85vh] sm:max-w-5xl overflow-hidden">
          <DialogHeader>
            <DialogTitle>{selected?.title}</DialogTitle>
            <DialogDescription>
              {selected?.summary || "无摘要"}
            </DialogDescription>
          </DialogHeader>
          {selected && (
            <div className="max-h-[60vh] space-y-4 overflow-auto pr-1">
              <div className="flex flex-wrap gap-1">
                {fingerprintParts(selected.fingerprint).map((part) => (
                  <Badge key={part} variant="outline">
                    {part}
                  </Badge>
                ))}
              </div>
              <div className="grid grid-cols-2 gap-2 text-sm md:grid-cols-4">
                <div>
                  <div className="text-muted-foreground text-xs">实战战绩</div>
                  <div>{winRate(selected)}</div>
                </div>
                <div>
                  <div className="text-muted-foreground text-xs">检索次数</div>
                  <div>{selected.uses}</div>
                </div>
                <div>
                  <div className="text-muted-foreground text-xs">最近验证</div>
                  <div>{fmtTime(selected.last_verified_at)}</div>
                </div>
                <div>
                  <div className="text-muted-foreground text-xs">来源</div>
                  <div>
                    {selected.source_company || "—"}
                    {selected.source_task_id ? ` · 任务 #${selected.source_task_id}` : ""}
                  </div>
                </div>
              </div>
              {selected.status === "deprecated" && (
                <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm">
                  已废弃：{selected.deprecated_reason || "（未填理由）"}
                </div>
              )}
              <div>
                <div className="mb-1 font-medium text-sm">打法步骤</div>
                <div className="rounded-md border bg-muted/20 p-3">
                  <Markdown text={selected.steps || "_（无步骤）_"} />
                </div>
              </div>
              {selected.evidence && (
                <div>
                  <div className="mb-1 font-medium text-sm">原始证据（脱敏后）</div>
                  <pre className="max-h-60 overflow-auto rounded-md border bg-muted/20 p-3 text-xs">{selected.evidence}</pre>
                </div>
              )}
              {selected.last_note && (
                <div className="text-muted-foreground text-sm">
                  最近反馈（{selected.last_outcome || "—"}）：{selected.last_note}
                </div>
              )}
            </div>
          )}
          <DialogFooter>
            {selected && (
              <Button variant="outline" onClick={() => openEditor(selected)}>
                编辑
              </Button>
            )}
            {selected?.status === "active" ? (
              <Button
                variant="destructive"
                onClick={() => {
                  setDeprecating(selected);
                  setReason("");
                }}
              >
                废弃
              </Button>
            ) : (
              <Button onClick={() => selected && restore(selected)}>恢复启用</Button>
            )}
            <Button variant="outline" onClick={() => setSelected(null)}>
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={editing !== null}
        onOpenChange={(open) => {
          if (!open && !savingEdit) setEditing(null);
        }}
      >
        <DialogContent className="max-h-[85vh] sm:max-w-3xl overflow-hidden">
          <DialogHeader>
            <DialogTitle>编辑打法卡片</DialogTitle>
            <DialogDescription>
              修改标题、步骤、证据与匹配指纹。统计（检索/成功/失败）和「最近验证」时间不会变；
              改指纹或 technique_key 会改变这张卡的匹配范围与去重键。
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-[62vh] space-y-3 overflow-auto pr-1">
            <div className="grid gap-1.5">
              <label className="text-sm font-medium" htmlFor="pb-title">标题</label>
              <Input
                id="pb-title"
                value={editForm.title}
                onChange={(e) => setEditField("title", e.target.value)}
                placeholder="例如：Magento 2.4 未授权 RCE → proc_open 绕过 disable_functions"
              />
            </div>
            <div className="grid gap-1.5">
              <label className="text-sm font-medium" htmlFor="pb-summary">摘要</label>
              <Textarea
                id="pb-summary"
                rows={2}
                value={editForm.summary}
                onChange={(e) => setEditField("summary", e.target.value)}
                placeholder="一两句话说明适用场景与结果"
              />
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <label className="text-sm font-medium" htmlFor="pb-tech">技术栈指纹</label>
                <Input
                  id="pb-tech"
                  value={editForm.technologies}
                  onChange={(e) => setEditField("technologies", e.target.value)}
                  placeholder="逗号分隔，如 magento, php, apache"
                />
              </div>
              <div className="grid gap-1.5">
                <label className="text-sm font-medium" htmlFor="pb-techkey">technique_key</label>
                <Input
                  id="pb-techkey"
                  value={editForm.technique_key}
                  onChange={(e) => setEditField("technique_key", e.target.value)}
                  placeholder="留空则由标题生成；同指纹下用于去重"
                />
              </div>
              <div className="grid gap-1.5">
                <label className="text-sm font-medium" htmlFor="pb-favicon">favicon MMH3</label>
                <Input
                  id="pb-favicon"
                  value={editForm.favicon_mmh3}
                  onChange={(e) => setEditField("favicon_mmh3", e.target.value)}
                  placeholder="如 -123456789（最强指纹信号）"
                />
              </div>
              <div className="grid gap-1.5">
                <label className="text-sm font-medium" htmlFor="pb-service">服务 / 端口</label>
                <div className="flex gap-2">
                  <Input
                    id="pb-service"
                    value={editForm.service_name}
                    onChange={(e) => setEditField("service_name", e.target.value)}
                    placeholder="http / https / ssh"
                  />
                  <Input
                    value={editForm.port}
                    onChange={(e) => setEditField("port", e.target.value)}
                    placeholder="443"
                    inputMode="numeric"
                    className="w-24"
                  />
                </div>
              </div>
              <div className="grid gap-1.5">
                <label className="text-sm font-medium" htmlFor="pb-title-fp">页面标题</label>
                <Input
                  id="pb-title-fp"
                  value={editForm.page_title}
                  onChange={(e) => setEditField("page_title", e.target.value)}
                  placeholder="弱信号，只做指纹加分"
                />
              </div>
              <div className="grid gap-1.5">
                <label className="text-sm font-medium" htmlFor="pb-confidence">置信度</label>
                <Select value={editForm.confidence} onValueChange={(v) => setEditField("confidence", v)}>
                  <SelectTrigger id="pb-confidence">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="high">high</SelectItem>
                    <SelectItem value="medium">medium</SelectItem>
                    <SelectItem value="low">low</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="grid gap-1.5">
              <label className="text-sm font-medium" htmlFor="pb-steps">打法步骤（Markdown，必填）</label>
              <Textarea
                id="pb-steps"
                rows={12}
                value={editForm.steps}
                onChange={(e) => setEditField("steps", e.target.value)}
                className="font-mono text-xs"
                placeholder="入口 → 验证 → 利用 → 结果/权限；写清可复现的请求与判定"
              />
            </div>
            <div className="grid gap-1.5">
              <label className="text-sm font-medium" htmlFor="pb-evidence">原始证据（脱敏后）</label>
              <Textarea
                id="pb-evidence"
                rows={5}
                value={editForm.evidence}
                onChange={(e) => setEditField("evidence", e.target.value)}
                className="font-mono text-xs"
                placeholder="命令/请求/关键回显"
              />
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" disabled={savingEdit} onClick={() => setEditing(null)}>
              取消
            </Button>
            <Button disabled={savingEdit} onClick={() => void saveEdit()}>
              {savingEdit ? "保存中…" : "保存"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={deprecating !== null} onOpenChange={(open) => !open && setDeprecating(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>废弃这张打法卡</DialogTitle>
            <DialogDescription>
              废弃后不再参与指纹匹配；理由会留在卡片上，供后来人判断。
            </DialogDescription>
          </DialogHeader>
          <Textarea
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="例如：该版本已普遍修复 / 指纹太泛，误配严重 / 打法已被 WAF 识别"
            rows={3}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeprecating(null)}>
              取消
            </Button>
            <Button variant="destructive" onClick={deprecate}>
              确认废弃
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
