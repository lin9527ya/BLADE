"use client";

import * as React from "react";

import { BookOpenIcon, Loader2Icon, PlusIcon, SearchIcon, ShieldAlertIcon, Trash2Icon, UploadIcon } from "lucide-react";
import { toast } from "sonner";

import { CopyButton } from "@/components/copy-button";
import { Markdown } from "@/components/markdown";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { CVE, CVEInput, PlaybookFingerprint, Severity } from "@/lib/types";

const PAGE_SIZE = 20;
const SEVERITIES: Severity[] = ["critical", "high", "medium", "low"];

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

function fingerprintParts(fp?: PlaybookFingerprint): string[] {
  if (!fp) return [];
  const parts: string[] = [];
  if (fp.technologies?.length) parts.push(fp.technologies.slice(0, 8).join(" · "));
  if (fp.favicon_mmh3) parts.push(`favicon ${fp.favicon_mmh3}`);
  if (fp.service_name) parts.push(fp.port ? `${fp.service_name}:${fp.port}` : fp.service_name);
  if (fp.page_title) parts.push(`「${fp.page_title}」`);
  return parts;
}

type FormState = {
  cve_id: string;
  title: string;
  severity: Severity;
  cvss_score: string;
  vendor: string;
  description: string;
  productsText: string;
  version_constraint: string;
  technologiesText: string;
  favicon_mmh3: string;
  service_name: string;
  port: string;
  page_title: string;
  probe_guide: string;
  refsText: string;
  exploit_language: string;
  exploit_filename: string;
  exploit_usage: string;
  exploit_script: string;
};

const EMPTY_FORM: FormState = {
  cve_id: "",
  title: "",
  severity: "high",
  cvss_score: "",
  vendor: "",
  description: "",
  productsText: "",
  version_constraint: "",
  technologiesText: "",
  favicon_mmh3: "",
  service_name: "",
  port: "",
  page_title: "",
  probe_guide: "",
  refsText: "",
  exploit_language: "python3",
  exploit_filename: "",
  exploit_usage: "",
  exploit_script: "",
};

function formFromCVE(c: CVE): FormState {
  return {
    cve_id: c.cve_id,
    title: c.title,
    severity: (SEVERITIES.includes(c.severity as Severity) ? c.severity : "high") as Severity,
    cvss_score: c.cvss_score ? String(c.cvss_score) : "",
    vendor: c.vendor ?? "",
    description: c.description ?? "",
    productsText: (c.affected_products ?? []).join(", "),
    version_constraint: c.version_constraint ?? "",
    technologiesText: (c.fingerprint?.technologies ?? []).join(", "),
    favicon_mmh3: c.fingerprint?.favicon_mmh3 ?? "",
    service_name: c.fingerprint?.service_name ?? "",
    port: c.fingerprint?.port ? String(c.fingerprint.port) : "",
    page_title: c.fingerprint?.page_title ?? "",
    probe_guide: c.probe_guide ?? "",
    refsText: (c.refs ?? []).join("\n"),
    exploit_language: c.exploit_language || "python3",
    exploit_filename: c.exploit_filename ?? "",
    exploit_usage: c.exploit_usage ?? "",
    exploit_script: c.exploit_script ?? "",
  };
}

function splitCSV(s: string): string[] {
  return s
    .split(/[,，\n]/)
    .map((x) => x.trim())
    .filter(Boolean);
}

function splitLines(s: string): string[] {
  return s
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);
}

function toInput(f: FormState): CVEInput {
  const port = Number.parseInt(f.port, 10);
  return {
    cve_id: f.cve_id.trim(),
    title: f.title.trim(),
    severity: f.severity,
    cvss_score: f.cvss_score.trim() ? Number(f.cvss_score) : 0,
    vendor: f.vendor.trim() || undefined,
    description: f.description,
    affected_products: splitCSV(f.productsText),
    version_constraint: f.version_constraint.trim(),
    fingerprint: {
      technologies: splitCSV(f.technologiesText),
      favicon_mmh3: f.favicon_mmh3.trim() || undefined,
      service_name: f.service_name.trim() || undefined,
      port: Number.isFinite(port) && port > 0 ? port : undefined,
      page_title: f.page_title.trim() || undefined,
    },
    probe_guide: f.probe_guide,
    refs: splitLines(f.refsText),
    exploit_script: f.exploit_script,
    exploit_language: f.exploit_script.trim() ? f.exploit_language : "",
    exploit_filename: f.exploit_filename.trim() || undefined,
    exploit_usage: f.exploit_usage.trim() || undefined,
  };
}

function productsLabel(c: CVE): string {
  const products = c.affected_products?.length ? c.affected_products.join(", ") : "—";
  return c.version_constraint ? `${products} · ${c.version_constraint}` : products;
}

export default function CVEPage() {
  const [query, setQuery] = React.useState("");
  const [queryQ, setQueryQ] = React.useState("");
  const [status, setStatus] = React.useState("active");
  const [severity, setSeverity] = React.useState("all");
  const [scriptFilter, setScriptFilter] = React.useState("all");
  const [page, setPage] = React.useState(0);

  const [rows, setRows] = React.useState<CVE[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(false);
  const [selected, setSelected] = React.useState<CVE | null>(null);
  const [editing, setEditing] = React.useState<CVE | null>(null);
  const [creating, setCreating] = React.useState(false);
  const [form, setForm] = React.useState<FormState>(EMPTY_FORM);
  const [saving, setSaving] = React.useState(false);
  const [deprecating, setDeprecating] = React.useState<CVE | null>(null);
  const [reason, setReason] = React.useState("");
  const [deleting, setDeleting] = React.useState<CVE | null>(null);
  const [uploading, setUploading] = React.useState(false);
  const fileRef = React.useRef<HTMLInputElement>(null);
  const detailFileRef = React.useRef<HTMLInputElement>(null);

  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: filter changes reset pagination intentionally.
  React.useEffect(() => {
    setPage(0);
  }, [queryQ, status, severity, scriptFilter]);

  const load = React.useCallback(() => {
    setLoading(true);
    api
      .cves({
        q: queryQ,
        status,
        severity: severity === "all" ? undefined : severity,
        has_exploit: scriptFilter === "yes" ? true : scriptFilter === "no" ? false : undefined,
        limit: PAGE_SIZE,
        offset: page * PAGE_SIZE,
      })
      .then((r) => {
        setRows(r.cves);
        setTotal(r.total);
      })
      .catch((e: Error) => toast.error(`加载 CVE 库失败：${e.message}`))
      .finally(() => setLoading(false));
  }, [queryQ, status, severity, scriptFilter, page]);

  React.useEffect(() => {
    load();
  }, [load]);

  const openCreate = () => {
    setEditing(null);
    setForm(EMPTY_FORM);
    setCreating(true);
  };

  // 列表接口刻意不返回脚本/完整描述；详情和编辑都必须按 id 拉全量，否则会出现
  // "详情里没有脚本" 以及 "编辑保存把脚本清掉"。
  const openDetail = async (c: CVE) => {
    try {
      setSelected(await api.cve(c.id));
    } catch (e) {
      toast.error(`读取 CVE 详情失败：${(e as Error).message}`);
    }
  };

  const openEdit = async (c: CVE) => {
    try {
      const full = await api.cve(c.id);
      setForm(formFromCVE(full));
      setEditing(full);
    } catch (e) {
      toast.error(`读取 CVE 详情失败：${(e as Error).message}`);
    }
  };

  const closeEditor = () => {
    if (saving) return;
    setCreating(false);
    setEditing(null);
  };

  const save = async () => {
    if (!form.cve_id.trim() || !form.title.trim()) {
      toast.error("CVE 编号和标题必填");
      return;
    }
    setSaving(true);
    try {
      const input = toInput(form);
      const saved = editing ? await api.updateCVE(editing.id, input) : await api.createCVE(input);
      toast.success(editing ? "CVE 已更新" : "CVE 已创建");
      setCreating(false);
      setEditing(null);
      setSelected(saved);
      load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const readScriptFile = async (file: File | undefined, direct = false) => {
    if (!file) return;
    if (file.size > 1 << 20) {
      toast.error("脚本不能超过 1 MiB");
      return;
    }
    if (direct && selected) {
      setUploading(true);
      try {
        const updated = await api.uploadCVEScript(selected.id, file);
        setSelected(updated);
        toast.success("脚本已上传");
        load();
      } catch (e) {
        toast.error(`上传失败：${(e as Error).message}`);
      } finally {
        setUploading(false);
      }
      return;
    }
    try {
      const text = await file.text();
      const lower = file.name.toLowerCase();
      const language = lower.endsWith(".py")
        ? "python3"
        : lower.endsWith(".sh")
          ? "bash"
          : lower.endsWith(".rb")
            ? "ruby"
            : lower.endsWith(".pl")
              ? "perl"
              : lower.endsWith(".php")
                ? "php"
                : lower.endsWith(".js")
                  ? "node"
                  : lower.endsWith(".ps1")
                    ? "pwsh"
                    : form.exploit_language;
      setForm((f) => ({ ...f, exploit_script: text, exploit_language: language, exploit_filename: file.name }));
    } catch (e) {
      toast.error(`读取脚本失败：${(e as Error).message}`);
    }
  };

  async function restore(c: CVE) {
    try {
      const updated = await api.setCVEStatus(c.id, "active");
      toast.success("已恢复启用");
      setSelected(updated);
      load();
    } catch (e) {
      toast.error(`恢复失败：${(e as Error).message}`);
    }
  }

  async function deprecate() {
    if (!deprecating) return;
    if (!reason.trim()) {
      toast.error("请填写废弃理由：后来人最需要的就是这句");
      return;
    }
    try {
      const updated = await api.setCVEStatus(deprecating.id, "deprecated", reason.trim());
      toast.success("已废弃，不再参与匹配");
      setDeprecating(null);
      setReason("");
      setSelected(updated);
      load();
    } catch (e) {
      toast.error(`废弃失败：${(e as Error).message}`);
    }
  }

  async function clearScript(c: CVE) {
    if (!window.confirm("清除这条 CVE 的利用脚本？描述/探测指引会保留。")) return;
    try {
      const updated = await api.clearCVEScript(c.id);
      setSelected(updated);
      toast.success("脚本已清除");
      load();
    } catch (e) {
      toast.error(`清除失败：${(e as Error).message}`);
    }
  }

  async function remove() {
    if (!deleting) return;
    try {
      await api.deleteCVE(deleting.id);
      toast.success("CVE 已删除");
      setDeleting(null);
      setSelected(null);
      load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }

  const editorOpen = creating || editing !== null;
  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((f) => ({ ...f, [key]: value }));

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-lg">
            <ShieldAlertIcon className="size-5" />
            CVE库
          </h1>
          <p className="mt-1 text-muted-foreground text-sm">
            人工维护的 CVE 知识：详细描述、受影响产品/版本、指纹与探测指引。任务运行时按资产指纹匹配；
            有条目保存了利用脚本时，worker/planner 可在通过授权/审批门后用 cve_exploit 直接触发。匹配只是线索，不是漏洞证据。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative">
            <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="搜索 CVE 编号 / 标题 / 产品…"
              className="w-64 pl-8"
            />
          </div>
          <Select value={status} onValueChange={setStatus}>
            <SelectTrigger className="w-28">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="active">启用中</SelectItem>
              <SelectItem value="deprecated">已废弃</SelectItem>
              <SelectItem value="all">全部</SelectItem>
            </SelectContent>
          </Select>
          <Select value={severity} onValueChange={setSeverity}>
            <SelectTrigger className="w-28">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部等级</SelectItem>
              {SEVERITIES.map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={scriptFilter} onValueChange={setScriptFilter}>
            <SelectTrigger className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部脚本</SelectItem>
              <SelectItem value="yes">有脚本</SelectItem>
              <SelectItem value="no">无脚本</SelectItem>
            </SelectContent>
          </Select>
          <Button size="sm" onClick={openCreate}>
            <PlusIcon data-icon="inline-start" />
            新建 CVE
          </Button>
        </div>
      </div>

      <Card className="overflow-hidden">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="min-w-[280px]">CVE</TableHead>
              <TableHead className="w-24">等级</TableHead>
              <TableHead className="min-w-[220px]">受影响产品 / 版本</TableHead>
              <TableHead className="min-w-[220px]">指纹</TableHead>
              <TableHead className="w-32">利用脚本</TableHead>
              <TableHead className="w-28">使用 / 执行</TableHead>
              <TableHead className="w-36">最近更新</TableHead>
              <TableHead className="w-24">状态</TableHead>
              <TableHead className="w-24">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading ? (
              <TableRow>
                <TableCell colSpan={9} className="py-12 text-center text-muted-foreground">
                  加载中…
                </TableCell>
              </TableRow>
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={9} className="py-12 text-center text-muted-foreground">
                  还没有 CVE 记录。点击右上角「新建 CVE」，录入描述、受影响产品和可选利用脚本。
                </TableCell>
              </TableRow>
            ) : (
              rows.map((c) => {
                const fp = fingerprintParts(c.fingerprint);
                return (
                  <TableRow key={c.id}>
                    <TableCell>
                      <button type="button" className="text-left font-medium hover:underline" onClick={() => void openDetail(c)}>
                        <span className="font-mono text-xs">{c.cve_id}</span>
                        <span className="ml-2">{c.title}</span>
                      </button>
                      {c.description && (
                        <div className="mt-0.5 line-clamp-2 text-muted-foreground text-xs">{c.description}</div>
                      )}
                    </TableCell>
                    <TableCell>
                      {c.severity ? <StatusBadge domain="severity" value={c.severity} /> : <span className="text-muted-foreground">—</span>}
                    </TableCell>
                    <TableCell className="text-xs">{productsLabel(c)}</TableCell>
                    <TableCell>
                      {fp.length === 0 ? (
                        <span className="text-muted-foreground text-xs">—</span>
                      ) : (
                        <div className="flex flex-wrap gap-1">
                          {fp.map((part) => (
                            <Badge key={part} variant="outline" className="max-w-[200px] truncate">
                              {part}
                            </Badge>
                          ))}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="text-xs">
                      {c.has_exploit ? (
                        <Badge variant="secondary">{c.exploit_language || "有"}</Badge>
                      ) : (
                        <span className="text-muted-foreground">无</span>
                      )}
                    </TableCell>
                    <TableCell className="text-xs tabular-nums">
                      {c.uses} / {c.executions}
                    </TableCell>
                    <TableCell className="text-muted-foreground text-xs">{fmtTime(c.updated_at)}</TableCell>
                    <TableCell>
                      <StatusBadge domain="cve" value={c.status} />
                    </TableCell>
                    <TableCell>
                      <Button size="sm" variant="ghost" onClick={() => void openDetail(c)}>
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

      {/* 详情 */}
      <Dialog open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <DialogContent className="max-h-[88vh] max-w-4xl overflow-hidden sm:max-w-6xl">
          <DialogHeader>
            <DialogTitle className="flex flex-wrap items-center gap-2">
              <span className="font-mono text-sm">{selected?.cve_id}</span>
              <span>{selected?.title}</span>
              {selected?.severity ? <StatusBadge domain="severity" value={selected.severity} /> : null}
              {selected ? <StatusBadge domain="cve" value={selected.status} /> : null}
            </DialogTitle>
            <DialogDescription>
              {selected ? productsLabel(selected) : ""} {selected?.vendor ? `· ${selected.vendor}` : ""}
            </DialogDescription>
          </DialogHeader>
          {selected && (
            <div className="max-h-[64vh] space-y-4 overflow-auto pr-1">
              {selected.status === "deprecated" && (
                <div className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm">
                  已废弃：{selected.deprecated_reason || "（未填理由）"}
                </div>
              )}
              <div className="grid grid-cols-2 gap-2 text-sm md:grid-cols-4">
                <div>
                  <div className="text-muted-foreground text-xs">CVSS</div>
                  <div>{selected.cvss_score || "—"}</div>
                </div>
                <div>
                  <div className="text-muted-foreground text-xs">版本约束</div>
                  <div>{selected.version_constraint || "无"}</div>
                </div>
                <div>
                  <div className="text-muted-foreground text-xs">使用 / 执行</div>
                  <div>
                    {selected.uses} / {selected.executions}
                  </div>
                </div>
                <div>
                  <div className="text-muted-foreground text-xs">最近更新</div>
                  <div>{fmtTime(selected.updated_at)}</div>
                </div>
              </div>
              {fingerprintParts(selected.fingerprint).length > 0 && (
                <div className="flex flex-wrap gap-1">
                  {fingerprintParts(selected.fingerprint).map((part) => (
                    <Badge key={part} variant="outline">
                      {part}
                    </Badge>
                  ))}
                </div>
              )}
              <section>
                <h3 className="mb-1 font-medium text-sm">详细描述</h3>
                <div className="rounded-md border bg-muted/20 p-3">
                  <Markdown text={selected.description || "_（无描述）_"} />
                </div>
              </section>
              <section>
                <h3 className="mb-1 font-medium text-sm">快速探测 / 手工验证</h3>
                <div className="rounded-md border bg-muted/20 p-3">
                  <Markdown text={selected.probe_guide || "_（无探测指引；脚本不可用时请按 CVE 描述自行验证）_"} />
                </div>
              </section>
              {selected.refs?.length ? (
                <section>
                  <h3 className="mb-1 font-medium text-sm">参考链接</h3>
                  <ul className="list-disc space-y-0.5 pl-5 text-sm">
                    {selected.refs.map((ref) => (
                      <li key={ref} className="break-all">
                        {/^https?:\/\//i.test(ref) ? (
                          <a className="text-primary underline underline-offset-2" href={ref} target="_blank" rel="noreferrer">
                            {ref}
                          </a>
                        ) : (
                          ref
                        )}
                      </li>
                    ))}
                  </ul>
                </section>
              ) : null}
              <section>
                <div className="mb-1 flex items-center justify-between">
                  <h3 className="font-medium text-sm">利用脚本</h3>
                  {selected.has_exploit && <CopyButton text={selected.exploit_script ?? ""} successMessage="已复制脚本" />}
                </div>
                {selected.has_exploit ? (
                  <div className="space-y-1">
                    <div className="text-muted-foreground text-xs">
                      {selected.exploit_language} · {selected.exploit_filename || "—"} · {selected.exploit_bytes} bytes · SHA256{" "}
                      <span className="font-mono">{selected.exploit_sha256?.slice(0, 16)}…</span>
                    </div>
                    {selected.exploit_usage && <div className="text-muted-foreground text-xs">用法：{selected.exploit_usage}</div>}
                    <pre className="max-h-72 overflow-auto rounded-md border bg-muted/20 p-3 font-mono text-xs whitespace-pre-wrap">
                      {selected.exploit_script}
                    </pre>
                  </div>
                ) : (
                  <p className="text-muted-foreground text-sm">没有保存脚本；Agent 只能按上面的探测指引手工验证。</p>
                )}
              </section>
            </div>
          )}
          <DialogFooter className="flex-wrap gap-2">
            {selected && (
              <>
                <input
                  ref={detailFileRef}
                  type="file"
                  accept=".py,.sh,.rb,.pl,.php,.js,.ps1,.txt"
                  className="hidden"
                  onChange={(e) => void readScriptFile(e.target.files?.[0], true)}
                />
                <Button variant="outline" onClick={() => void openEdit(selected)}>
                  编辑
                </Button>
                <Button variant="outline" disabled={uploading} onClick={() => detailFileRef.current?.click()}>
                  {uploading ? <Loader2Icon className="animate-spin" data-icon="inline-start" /> : <UploadIcon data-icon="inline-start" />}
                  上传脚本
                </Button>
                {selected.has_exploit && (
                  <Button variant="ghost" onClick={() => void clearScript(selected)}>
                    清除脚本
                  </Button>
                )}
                {selected.status === "active" ? (
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
                  <Button onClick={() => void restore(selected)}>恢复启用</Button>
                )}
                <Button variant="ghost" className="text-destructive" onClick={() => setDeleting(selected)}>
                  <Trash2Icon data-icon="inline-start" />
                  删除
                </Button>
              </>
            )}
            <Button variant="outline" onClick={() => setSelected(null)}>
              关闭
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 新建 / 编辑 */}
      <Dialog open={editorOpen} onOpenChange={(open) => !open && closeEditor()}>
        <DialogContent className="max-h-[92vh] max-w-5xl overflow-hidden sm:max-w-[min(1440px,96vw)]">
          <DialogHeader>
            <DialogTitle>{editing ? `编辑 ${editing.cve_id}` : "新建 CVE"}</DialogTitle>
            <DialogDescription>
              至少提供一组产品，或 favicon/技术栈/服务之一作为匹配信号。脚本只从本机上传/粘贴，平台不会自动下载。
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-[72vh] space-y-5 overflow-auto pr-1">
            <section className="grid grid-cols-1 gap-3 md:grid-cols-4">
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">CVE 编号 *</span>
                <Input value={form.cve_id} onChange={(e) => set("cve_id", e.target.value)} placeholder="CVE-2021-44228" />
              </label>
              <label className="flex flex-col gap-1 text-xs md:col-span-2">
                <span className="text-muted-foreground">标题 *</span>
                <Input value={form.title} onChange={(e) => set("title", e.target.value)} placeholder="Log4j2 JNDI 远程代码执行" />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">厂商</span>
                <Input value={form.vendor} onChange={(e) => set("vendor", e.target.value)} placeholder="Apache" />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">严重度</span>
                <Select value={form.severity} onValueChange={(v) => set("severity", v as Severity)}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SEVERITIES.map((s) => (
                      <SelectItem key={s} value={s}>
                        {s}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">CVSS</span>
                <Input type="number" min={0} max={10} step={0.1} value={form.cvss_score} onChange={(e) => set("cvss_score", e.target.value)} placeholder="10.0" />
              </label>
              <label className="flex flex-col gap-1 text-xs md:col-span-2">
                <span className="text-muted-foreground">受影响产品（逗号分隔，与资产 technologies 匹配）</span>
                <Input value={form.productsText} onChange={(e) => set("productsText", e.target.value)} placeholder="log4j, log4j-core" />
              </label>
              <label className="flex flex-col gap-1 text-xs md:col-span-2">
                <span className="text-muted-foreground">版本约束（单组 AND；如 &gt;= 2.0.0, &lt; 2.15.0）</span>
                <Input value={form.version_constraint} onChange={(e) => set("version_constraint", e.target.value)} placeholder=">= 2.0.0, < 2.15.0" />
              </label>
              <label className="flex flex-col gap-1 text-xs md:col-span-2">
                <span className="text-muted-foreground">指纹技术栈（逗号分隔）</span>
                <Input value={form.technologiesText} onChange={(e) => set("technologiesText", e.target.value)} placeholder="log4j, java" />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">favicon MMH3</span>
                <Input value={form.favicon_mmh3} onChange={(e) => set("favicon_mmh3", e.target.value)} placeholder="-123456" />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">服务名</span>
                <Input value={form.service_name} onChange={(e) => set("service_name", e.target.value)} placeholder="http" />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">端口</span>
                <Input type="number" min={1} max={65535} value={form.port} onChange={(e) => set("port", e.target.value)} placeholder="8080" />
              </label>
              <label className="flex flex-col gap-1 text-xs md:col-span-2">
                <span className="text-muted-foreground">页面标题（弱信号）</span>
                <Input value={form.page_title} onChange={(e) => set("page_title", e.target.value)} placeholder="Example Admin" />
              </label>
            </section>

            <section className="grid grid-cols-1 gap-3 md:grid-cols-2">
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">详细描述（Markdown）</span>
                <Textarea rows={6} value={form.description} onChange={(e) => set("description", e.target.value)} placeholder="影响、触发条件、危害…" />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-muted-foreground">快速探测 / 手工验证（Markdown）</span>
                <Textarea rows={6} value={form.probe_guide} onChange={(e) => set("probe_guide", e.target.value)} placeholder="没有脚本或版本不匹配时，Agent 按这里继续验证" />
              </label>
              <label className="flex flex-col gap-1 text-xs md:col-span-2">
                <span className="text-muted-foreground">参考链接（一行一个）</span>
                <Textarea rows={2} value={form.refsText} onChange={(e) => set("refsText", e.target.value)} placeholder="https://..." />
              </label>
            </section>

            <section className="space-y-3 rounded-lg border p-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <h3 className="flex items-center gap-1.5 font-medium text-sm">
                  <BookOpenIcon className="size-4" /> 利用脚本（可选）
                </h3>
                <div className="flex items-center gap-2">
                  <input
                    ref={fileRef}
                    type="file"
                    accept=".py,.sh,.rb,.pl,.php,.js,.ps1,.txt"
                    className="hidden"
                    onChange={(e) => void readScriptFile(e.target.files?.[0])}
                  />
                  <Button type="button" size="sm" variant="outline" onClick={() => fileRef.current?.click()}>
                    <UploadIcon data-icon="inline-start" /> 选择脚本文件
                  </Button>
                  {form.exploit_script && (
                    <Button type="button" size="sm" variant="ghost" onClick={() => set("exploit_script", "")}>
                      清除
                    </Button>
                  )}
                </div>
              </div>
              <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
                <label className="flex flex-col gap-1 text-xs">
                  <span className="text-muted-foreground">语言</span>
                  <Select value={form.exploit_language} onValueChange={(v) => set("exploit_language", v)}>
                    <SelectTrigger className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {["python3", "bash", "sh", "ruby", "perl", "php", "node", "pwsh"].map((l) => (
                        <SelectItem key={l} value={l}>
                          {l}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </label>
                <label className="flex flex-col gap-1 text-xs">
                  <span className="text-muted-foreground">文件名（仅展示）</span>
                  <Input value={form.exploit_filename} onChange={(e) => set("exploit_filename", e.target.value)} placeholder="exploit.py" />
                </label>
                <label className="flex flex-col gap-1 text-xs">
                  <span className="text-muted-foreground">用法 / 参数说明</span>
                  <Input value={form.exploit_usage} onChange={(e) => set("exploit_usage", e.target.value)} placeholder="目标在 BLADE_TARGET；args 追加为 argv" />
                </label>
              </div>
              <Textarea
                rows={10}
                className="font-mono text-xs"
                value={form.exploit_script}
                onChange={(e) => set("exploit_script", e.target.value)}
                placeholder="粘贴脚本内容，或点「选择脚本文件」读取本地文本（不会上传到任意路径）"
              />
            </section>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeEditor} disabled={saving}>
              取消
            </Button>
            <Button onClick={() => void save()} disabled={saving}>
              {saving ? <Loader2Icon className="animate-spin" data-icon="inline-start" /> : null}
              保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 废弃理由 */}
      <Dialog open={deprecating !== null} onOpenChange={(open) => !open && setDeprecating(null)}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>废弃这条 CVE</DialogTitle>
            <DialogDescription>废弃后不再参与匹配，也不能用 cve_exploit 执行；理由会留在库里。</DialogDescription>
          </DialogHeader>
          <Textarea value={reason} onChange={(e) => setReason(e.target.value)} rows={3} placeholder="例如：版本已普遍修复 / 指纹太泛，误配严重" />
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeprecating(null)}>
              取消
            </Button>
            <Button variant="destructive" onClick={() => void deprecate()}>
              确认废弃
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 删除 */}
      <AlertDialog open={deleting !== null} onOpenChange={(open) => !open && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除 CVE {deleting?.cve_id}？</AlertDialogTitle>
            <AlertDialogDescription>
              这会硬删除库里的条目（含脚本），不可撤销。通常优先使用「废弃」保留记录。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={() => void remove()}>确认删除</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
