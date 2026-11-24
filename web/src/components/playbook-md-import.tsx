"use client";

import * as React from "react";

import { FileTextIcon, Loader2Icon, Trash2Icon } from "lucide-react";
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
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { PlaybookDraft } from "@/lib/types";

type EditableDraft = PlaybookDraft & { key: string; selected: boolean };

const MAX_MD_BYTES = 512 * 1024;

/** 打法库的 Markdown 导入：LLM 总结 → 人工预览/编辑 → 只插入低置信度草稿。 */
export function PlaybookMDImport({ onImported }: { onImported: () => void }) {
  const fileRef = React.useRef<HTMLInputElement>(null);
  const [busy, setBusy] = React.useState(false);
  const [open, setOpen] = React.useState(false);
  const [importing, setImporting] = React.useState(false);
  const [filename, setFilename] = React.useState("");
  const [warnings, setWarnings] = React.useState<string[]>([]);
  const [drafts, setDrafts] = React.useState<EditableDraft[]>([]);

  async function pick(file: File) {
    if (file.size > MAX_MD_BYTES) {
      toast.error("Markdown 文件不能超过 512KB");
      return;
    }
    if (
      !window.confirm(
        "Markdown 内容会发送到当前激活的 LLM 配置进行总结，导入卡会作为低置信度草稿。是否继续？",
      )
    ) {
      if (fileRef.current) fileRef.current.value = "";
      return;
    }
    setBusy(true);
    try {
      const content = await file.text();
      const result = await api.playbooksImportMDPreview({ filename: file.name, content });
      setFilename(file.name);
      setWarnings(result.warnings ?? []);
      setDrafts(
        (result.drafts ?? []).map((d, i) => ({
          ...d,
          key: `${i}-${d.technique_key || d.title}`,
          selected: true,
        })),
      );
      setOpen(true);
    } catch (e) {
      toast.error(`Markdown 总结失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  }

  function updateDraft(key: string, patch: Partial<EditableDraft>) {
    setDrafts((prev) => prev.map((d) => (d.key === key ? { ...d, ...patch } : d)));
  }

  function removeDraft(key: string) {
    setDrafts((prev) => prev.filter((d) => d.key !== key));
  }

  async function commit() {
    const selected = drafts.filter((d) => d.selected && d.title.trim() && d.steps.trim());
    if (selected.length === 0) {
      toast.error("请至少保留一张有标题和步骤的卡片");
      return;
    }
    setImporting(true);
    try {
      const result = await api.playbooksImportMDCommit(
        selected.map((d) => ({
          title: d.title,
          summary: d.summary,
          technique_key: d.technique_key,
          steps: d.steps,
          evidence: d.evidence,
          confidence: "low" as const,
        })),
      );
      if (result.failed > 0) {
        toast.warning(`导入完成：新增 ${result.created}、跳过 ${result.skipped}、失败 ${result.failed}`, {
          description: result.errors?.slice(0, 3).join("；"),
          duration: 12000,
        });
      } else {
        toast.success(`导入完成：新增 ${result.created}、跳过重复 ${result.skipped}`);
      }
      setOpen(false);
      onImported();
    } catch (e) {
      toast.error(`导入失败：${(e as Error).message}`);
    } finally {
      setImporting(false);
    }
  }

  return (
    <>
      <Button size="sm" variant="outline" onClick={() => fileRef.current?.click()} disabled={busy}>
        {busy ? <Loader2Icon className="size-4 animate-spin" /> : <FileTextIcon className="size-4" />}
        导入 MD
      </Button>
      <input
        ref={fileRef}
        type="file"
        accept=".md,.markdown,text/markdown,text/plain"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) void pick(file);
        }}
      />
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-h-[85vh] max-w-4xl overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Markdown 打法总结预览</DialogTitle>
            <DialogDescription>
              已用当前激活 LLM 总结「{filename || "Markdown 文档"}」。导入卡固定为 low 置信度草稿，
              不会自动匹配目标指纹；请核对内容后再导入。
            </DialogDescription>
          </DialogHeader>
          {warnings.length > 0 && (
            <div className="rounded-md border bg-muted/60 p-3 text-xs">
              <div className="mb-1 font-medium">提示</div>
              <ul className="list-disc space-y-0.5 pl-5 text-muted-foreground">
                {warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </div>
          )}
          <div className="grid gap-3">
            {drafts.map((d) => (
              <div key={d.key} className="grid gap-2 rounded-lg border p-3">
                <div className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={d.selected}
                    onChange={(e) => updateDraft(d.key, { selected: e.target.checked })}
                    aria-label="选择此卡片"
                  />
                  <Input
                    value={d.title}
                    onChange={(e) => updateDraft(d.key, { title: e.target.value })}
                    placeholder="打法名"
                  />
                  <Badge variant="outline">low</Badge>
                  <Button size="icon" variant="ghost" onClick={() => removeDraft(d.key)} aria-label="移除卡片">
                    <Trash2Icon className="size-4" />
                  </Button>
                </div>
                <div className="grid gap-2 md:grid-cols-2">
                  <div className="grid gap-1.5">
                    <Label>摘要</Label>
                    <Textarea
                      rows={2}
                      value={d.summary}
                      onChange={(e) => updateDraft(d.key, { summary: e.target.value })}
                      placeholder="适用条件 + 效果"
                    />
                  </div>
                  <div className="grid gap-1.5">
                    <Label>technique_key</Label>
                    <Input
                      value={d.technique_key}
                      onChange={(e) => updateDraft(d.key, { technique_key: e.target.value })}
                      placeholder="例如 shiro-rememberme-deser"
                    />
                  </div>
                </div>
                <div className="grid gap-1.5">
                  <Label>步骤（Markdown）</Label>
                  <Textarea
                    rows={6}
                    className="font-mono text-xs"
                    value={d.steps}
                    onChange={(e) => updateDraft(d.key, { steps: e.target.value })}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label>证据（文档中明确给出的命令/请求/回显）</Label>
                  <Textarea
                    rows={2}
                    value={d.evidence}
                    onChange={(e) => updateDraft(d.key, { evidence: e.target.value })}
                  />
                </div>
              </div>
            ))}
            {drafts.length === 0 && <p className="text-muted-foreground text-sm">没有可导入的卡片。</p>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>取消</Button>
            <Button onClick={commit} disabled={importing || drafts.length === 0}>
              {importing ? "导入中…" : `导入选中 ${drafts.filter((d) => d.selected).length} 张`}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
