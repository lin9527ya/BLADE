"use client";

import * as React from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { api } from "@/lib/api";
import type { FindingSrcCheck } from "@/lib/types";

export function FindingSrcExportDialog({
  id,
  format,
  onClose,
}: {
  id: string;
  format: "md" | "docx";
  onClose: () => void;
}) {
  const [check, setCheck] = React.useState<FindingSrcCheck | null>(null);
  const [error, setError] = React.useState("");
  const [reload, setReload] = React.useState(0);
  const [acknowledged, setAcknowledged] = React.useState(false);
  const [downloading, setDownloading] = React.useState(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: reload 是手动重新预检的触发器。
  React.useEffect(() => {
    const controller = new AbortController();
    setCheck(null);
    setError("");
    setAcknowledged(false);
    api
      .findingSrcCheck(id, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setCheck(value);
      })
      .catch((e) => {
        if (!controller.signal.aborted) setError((e as Error).message);
      });
    return () => controller.abort();
  }, [id, reload]);

  async function download() {
    if (!check || downloading || (!check.ready && !acknowledged)) return;
    setDownloading(true);
    setError("");
    try {
      await api.downloadSrcReport(id, format);
      toast.success(check.ready ? "已下载 SRC 交付件" : "已下载草稿，请补全缺项后再对外提交");
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setDownloading(false);
    }
  }

  const downloadLabel = check?.ready ? "下载交付件" : "下载草稿";
  return (
    <Dialog open onOpenChange={(open) => !open && !downloading && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>SRC 下载前预检 · {format.toUpperCase()}</DialogTitle>
          <DialogDescription>
            复核证据与报告缺项。原有导出不强制阻断；有缺项时须确认仅下载草稿。交付件可能包含原始证据，请复核敏感内容。
          </DialogDescription>
        </DialogHeader>
        {!check && !error && <p className="text-muted-foreground text-sm">检查证据与报告中…</p>}
        {check && (
          <div className="space-y-3 text-sm">
            <p>
              复现步骤：{check.steps} · 涉及接口：{check.endpoints}
            </p>
            {check.ready ? (
              <p>预检未发现缺项；仍需人工确认报告结论。</p>
            ) : (
              <>
                <ul className="max-h-64 list-disc space-y-2 overflow-auto pl-5">
                  {Array.from(new Set(check.warnings)).map((warning) => (
                    <li key={warning}>{warning}</li>
                  ))}
                </ul>
                <label className="flex items-start gap-2">
                  <Checkbox
                    checked={acknowledged}
                    onCheckedChange={(v) => setAcknowledged(v === true)}
                    disabled={downloading}
                  />
                  我已了解上述缺项，仅下载草稿，不作为已完成交付件。
                </label>
              </>
            )}
          </div>
        )}
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => setReload((v) => v + 1)} disabled={downloading}>
            重新预检
          </Button>
          <Button variant="outline" onClick={onClose} disabled={downloading}>
            取消
          </Button>
          <Button onClick={download} disabled={!check || downloading || (!check.ready && !acknowledged)}>
            {downloading ? "下载中…" : downloadLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
