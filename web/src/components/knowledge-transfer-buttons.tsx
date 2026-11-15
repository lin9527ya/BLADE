"use client";

import * as React from "react";

import { DownloadIcon, UploadIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { CVE, Playbook } from "@/lib/types";

type KnowledgeKind = "cves" | "playbooks";

// KnowledgeTransferButtons 给 CVE/打法库提供同一套 JSON 导出/导入入口。
export function KnowledgeTransferButtons({ kind, onImported }: { kind: KnowledgeKind; onImported: () => void }) {
  const fileRef = React.useRef<HTMLInputElement>(null);
  const [busy, setBusy] = React.useState(false);
  const label = kind === "cves" ? "CVE" : "打法";

  async function exportJSON() {
    setBusy(true);
    try {
      if (kind === "cves") await api.exportCVEs();
      else await api.exportPlaybooks();
      toast.success(`已导出${label}库`);
    } catch (e) {
      toast.error(`导出失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function importJSON(file: File) {
    setBusy(true);
    try {
      const parsed = JSON.parse(await file.text()) as unknown;
      const items = Array.isArray(parsed)
        ? parsed
        : Array.isArray((parsed as { items?: unknown }).items)
          ? (parsed as { items: unknown[] }).items
          : [];
      if (items.length === 0) throw new Error("文件里没有 items 数组");
      const result =
        kind === "cves" ? await api.importCVEs(items as CVE[]) : await api.importPlaybooks(items as Playbook[]);
      if (result.failed > 0) {
        toast.warning(`导入完成：新增 ${result.created}、更新 ${result.updated}、失败 ${result.failed}`, {
          description: result.errors?.slice(0, 3).join("；"),
          duration: 12000,
        });
      } else {
        toast.success(`导入完成：新增 ${result.created}、更新 ${result.updated}`);
      }
      onImported();
    } catch (e) {
      toast.error(`导入失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  }

  return (
    <div className="flex items-center gap-1">
      <Button size="sm" variant="outline" onClick={() => void exportJSON()} disabled={busy}>
        <DownloadIcon /> 导出
      </Button>
      <Button size="sm" variant="outline" onClick={() => fileRef.current?.click()} disabled={busy}>
        <UploadIcon /> 导入
      </Button>
      <input
        ref={fileRef}
        type="file"
        accept="application/json,.json"
        className="hidden"
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) void importJSON(file);
        }}
      />
    </div>
  );
}
