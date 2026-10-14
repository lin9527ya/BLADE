"use client";

import * as React from "react";

import { FileUpIcon, LoaderIcon, UploadIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { AssetImportPreview } from "@/lib/types";

// 资产 CSV 导入：把一份目标清单批量登记成资产。
//
// 界面上最要紧的一条是**预览作废**：文本一改，上一份预览就清掉——否则「拿旧预览配新文本」
// 就能点下应用，而那正是预览要防的事。应用按钮只在有一份对得上当前文本的预览时才亮。

const TYPE_LABEL: Record<string, string> = {
  root_domain: "根域",
  subdomain: "子域",
  ip: "IP",
  service: "HTTP 服务",
};

const PLACEHOLDER = `每行一个目标即可（也可带表头）：

example.com
api.example.com
10.0.0.1
https://app.example.com:8443/login

带表头时只取被点名的列（domain/host/域名、ip/地址、url/网址）。`;

function typeSummary(byType: Record<string, number>): string {
  return Object.entries(byType)
    .map(([k, v]) => `${TYPE_LABEL[k] ?? k} ${v}`)
    .join(" · ");
}

export function AssetImportDialog({ onImported }: { onImported: () => void }) {
  const [open, setOpen] = React.useState(false);
  const [csv, setCsv] = React.useState("");
  const [fileName, setFileName] = React.useState("");
  const [preview, setPreview] = React.useState<AssetImportPreview | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [applying, setApplying] = React.useState(false);
  const fileRef = React.useRef<HTMLInputElement>(null);

  React.useEffect(() => {
    if (!open) return;
    setCsv("");
    setFileName("");
    setPreview(null);
  }, [open]);

  const setText = (v: string) => {
    setCsv(v);
    setPreview(null); // 文本变了，旧预览作废
  };

  const pickFile = async (f: File | undefined) => {
    if (!f) return;
    try {
      const text = await f.text();
      setFileName(f.name);
      setText(text);
    } catch (e) {
      toast.error(`读取文件失败：${String((e as Error)?.message ?? e)}`);
    }
  };

  const runPreview = async () => {
    setBusy(true);
    try {
      setPreview(await api.previewAssetImport(csv));
    } catch (e) {
      toast.error(`预览失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    setApplying(true);
    try {
      const res = await api.importAssets(csv);
      const failed = res.errors?.length ?? 0;
      const msg = `新增 ${res.created} 条、合并 ${res.merged} 条`;
      if (failed > 0 || res.unrecognized.length > 0) {
        toast.warning(`${msg}；${failed} 条落库失败、${res.unrecognized.length} 行未识别`);
      } else {
        toast.success(msg);
      }
      setOpen(false);
      onImported();
    } catch (e) {
      toast.error(`导入失败：${String((e as Error)?.message ?? e)}`);
    } finally {
      setApplying(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="outline" size="sm">
          <UploadIcon data-icon="inline-start" /> 导入 CSV
        </Button>
      </DialogTrigger>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl">
        <DialogHeader className="border-b p-6">
          <DialogTitle>导入 CSV 目标清单</DialogTitle>
          <DialogDescription>
            先「检查清单」看会导入什么，确认无误再应用。已存在的资产按合并处理，不会重复登记。
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 overflow-y-auto p-6">
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" onClick={() => fileRef.current?.click()} disabled={busy || applying}>
              <FileUpIcon data-icon="inline-start" /> 选择文件
            </Button>
            {fileName && <span className="truncate text-sm text-muted-foreground">{fileName}</span>}
            <input
              ref={fileRef}
              type="file"
              accept=".csv,.txt,text/csv,text/plain"
              className="hidden"
              onChange={(e) => {
                void pickFile(e.target.files?.[0]);
                e.target.value = ""; // 同一个文件能连着选第二次
              }}
            />
          </div>

          <Textarea
            className="mt-3 min-h-40 font-mono text-xs"
            placeholder={PLACEHOLDER}
            value={csv}
            onChange={(e) => setText(e.target.value)}
            disabled={busy || applying}
          />

          {preview && (
            <div className="mt-4 space-y-3 rounded-md border p-4">
              {preview.parse_error && (
                <p className="text-sm text-destructive">文件只解析到一半就被中断：{preview.parse_error}</p>
              )}
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <Badge variant="secondary">共 {preview.total} 条</Badge>
                <Badge variant="default">新增 {preview.new}</Badge>
                {preview.existing > 0 && <Badge variant="outline">已存在 {preview.existing}</Badge>}
                {preview.duplicates > 0 && <Badge variant="outline">表内重复 {preview.duplicates}</Badge>}
              </div>
              {preview.total > 0 && <p className="text-sm text-muted-foreground">{typeSummary(preview.by_type)}</p>}

              {preview.samples.length > 0 && (
                <div className="space-y-1">
                  <p className="text-xs font-medium text-muted-foreground">判定明细</p>
                  <ul className="space-y-0.5 font-mono text-xs">
                    {preview.samples.map((s) => (
                      <li key={`${s.line}-${s.value}`} className="flex items-center gap-2">
                        <span className="w-10 shrink-0 text-right text-muted-foreground">第 {s.line} 行</span>
                        <span className="truncate">{s.value}</span>
                        <span className="shrink-0 text-muted-foreground">
                          {TYPE_LABEL[s.type] ?? s.type}
                          {s.existing ? "（已存在，合并）" : ""}
                        </span>
                      </li>
                    ))}
                  </ul>
                  {preview.samples_truncated ? (
                    <p className="text-xs text-muted-foreground">另有 {preview.samples_truncated} 条未列出</p>
                  ) : null}
                </div>
              )}

              {preview.warnings.length > 0 && (
                <div className="space-y-1">
                  <p className="text-xs font-medium text-muted-foreground">提醒（会导入，但有折扣）</p>
                  <ul className="space-y-0.5 text-xs text-muted-foreground">
                    {preview.warnings.map((n) => (
                      <li key={`w-${n.line}-${n.value}`}>
                        第 {n.line} 行 {n.value}：{n.reason}
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              {preview.unrecognized.length > 0 && (
                <div className="space-y-1">
                  <p className="text-xs font-medium text-muted-foreground">未识别（不会导入）</p>
                  <ul className="space-y-0.5 text-xs text-destructive">
                    {preview.unrecognized.map((n) => (
                      <li key={`u-${n.line}-${n.value}`}>
                        第 {n.line} 行 {n.value || "（空）"}：{n.reason}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </div>
          )}
        </div>

        <DialogFooter className="border-t p-6">
          <Button variant="outline" onClick={() => setOpen(false)} disabled={busy || applying}>
            取消
          </Button>
          <Button variant="secondary" onClick={runPreview} disabled={busy || applying || !csv.trim()}>
            {busy ? <LoaderIcon className="animate-spin" data-icon="inline-start" /> : null}
            检查清单
          </Button>
          <Button onClick={apply} disabled={applying || !preview || preview.total === 0}>
            {applying ? <LoaderIcon className="animate-spin" data-icon="inline-start" /> : null}
            应用导入{preview ? `（新增 ${preview.new} 条）` : ""}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
