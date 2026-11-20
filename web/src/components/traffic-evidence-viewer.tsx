"use client";

import * as React from "react";

import { DownloadIcon } from "lucide-react";
import { toast } from "sonner";

import { HttpCodeBlock } from "@/components/http-code-block";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { FindingTrafficDetail, TrafficDetail } from "@/lib/types";

export function TrafficEvidenceViewer({
  findingId,
  bindingId,
  contextTask,
  onClose,
}: {
  findingId: string;
  bindingId: string | null;
  contextTask?: string;
  onClose: () => void;
}) {
  const [detail, setDetail] = React.useState<FindingTrafficDetail | null>(null);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  React.useEffect(() => {
    if (!bindingId) return;
    let active = true;
    setDetail(null);
    setError("");
    api
      .findingTrafficDetail(findingId, bindingId, contextTask)
      .then((d) => {
        if (active) setDetail(d);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [findingId, bindingId, contextTask]);

  async function more(side: "request" | "response") {
    if (!detail || !bindingId) return;
    setBusy(true);
    try {
      const next = await api.findingTrafficBody(findingId, bindingId, side, detail[side].next_offset, contextTask);
      setDetail((current) =>
        current?.binding.id === bindingId
          ? { ...current, [side]: { ...next, content: current[side].content + next.content } }
          : current,
      );
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={bindingId !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle>流量证据 #{bindingId}</DialogTitle>
          <DialogDescription className="break-all">
            {detail?.binding.snapshot.url ?? "查看绑定时保存的请求与响应"}
          </DialogDescription>
        </DialogHeader>
        {error ? (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        ) : detail ? (
          <Tabs defaultValue="request">
            <TabsList>
              <TabsTrigger value="request">请求 Request</TabsTrigger>
              <TabsTrigger value="response">响应 Response</TabsTrigger>
            </TabsList>
            {(["request", "response"] as const).map((side) => (
              <TabsContent key={side} value={side}>
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="text-muted-foreground text-xs">
                    正文 {detail[side].total.toLocaleString()} 字节{detail[side].truncated ? " · 当前为预览" : ""}
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      void api
                        .downloadFindingTrafficBody(findingId, detail.binding.id, side, contextTask)
                        .catch((e: Error) => toast.error(e.message))
                    }
                  >
                    下载完整{side === "request" ? "请求" : "响应"}正文
                  </Button>
                </div>
                <HttpCodeBlock
                  raw={`${(side === "request" ? detail.binding.snapshot.req_head : detail.binding.snapshot.resp_head)?.trimEnd() ?? ""}\n\n${detail[side].content}`}
                />
                {detail[side].truncated && !detail[side].binary ? (
                  <Button variant="outline" size="sm" disabled={busy} onClick={() => void more(side)}>
                    加载更多正文
                  </Button>
                ) : null}
              </TabsContent>
            ))}
          </Tabs>
        ) : (
          <Skeleton className="h-56 w-full" />
        )}
      </DialogContent>
    </Dialog>
  );
}

export function extractTrafficBlobHash(raw: string): string | null {
  const m = raw.match(/@blob sha256:([0-9a-f]{16,128})/i);
  return m?.[1] ?? null;
}

export function TrafficBlobBar({ raw }: { raw: string }) {
  const hash = extractTrafficBlobHash(raw);
  const [downloading, setDownloading] = React.useState(false);
  if (!hash) return null;
  return (
    <div className="mb-2 flex items-center justify-between gap-2 rounded-md border bg-muted/40 px-3 py-1.5 text-xs">
      <span className="text-muted-foreground">正文超出内联预览上限，完整内容已转存至 Blob（sha256:{hash.slice(0, 12)}…）</span>
      <Button
        variant="outline"
        size="sm"
        className="h-7 shrink-0"
        disabled={downloading}
        onClick={() => {
          setDownloading(true);
          void api
            .downloadTrafficBlob(hash)
            .catch((e: Error) => toast.error(e.message))
            .finally(() => setDownloading(false));
        }}
      >
        <DownloadIcon className="size-3.5" />
        {downloading ? "下载中…" : "下载完整正文"}
      </Button>
    </div>
  );
}

export function CapturedTrafficViewer({ id, onClose }: { id: string | null; onClose: () => void }) {
  const [detail, setDetail] = React.useState<TrafficDetail | null>(null);
  const [error, setError] = React.useState("");
  React.useEffect(() => {
    if (!id) return;
    let active = true;
    setDetail(null);
    setError("");
    api
      .trafficExchange(id)
      .then((d) => {
        if (active) setDetail(d);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [id]);
  return (
    <Dialog
      open={id !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-4xl">
        <DialogHeader>
          <DialogTitle>预览流量</DialogTitle>
          <DialogDescription>流量 ID：{id}。绑定时会保存完整正文。</DialogDescription>
        </DialogHeader>
        {error ? (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        ) : detail ? (
          <Tabs defaultValue="request">
            <TabsList>
              <TabsTrigger value="request">请求 Request</TabsTrigger>
              <TabsTrigger value="response">响应 Response</TabsTrigger>
            </TabsList>
            <TabsContent value="request">
              <TrafficBlobBar raw={detail.req} />
              <HttpCodeBlock raw={detail.req} />
            </TabsContent>
            <TabsContent value="response">
              <TrafficBlobBar raw={detail.resp} />
              <HttpCodeBlock raw={detail.resp} />
            </TabsContent>
          </Tabs>
        ) : (
          <Skeleton className="h-56 w-full" />
        )}
      </DialogContent>
    </Dialog>
  );
}
