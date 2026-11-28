"use client";
import * as React from "react";

import { Button } from "@/components/ui/button";
import { http } from "@/lib/api";
import { isChatImage, pendingImageFiles } from "@/lib/chat-images";
import type { ChatAttachment } from "@/lib/types";

type Status = { status: "unknown" | "supported" | "unsupported"; model: string };
export function HumanImageReview({
  scope,
  id,
  attachments,
}: {
  scope: "session" | "task";
  id: string;
  attachments: ChatAttachment[];
}) {
  const [status, setStatus] = React.useState<Status>();
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [previews, setPreviews] = React.useState<{ src: string; name: string }[]>([]);
  const request = React.useRef<AbortController | null>(null);
  const has = attachments.some(isChatImage);
  const path = `/chat/vision?${new URLSearchParams({ scope, id })}`;
  React.useEffect(() => {
    const urls = attachments.filter(isChatImage).flatMap((a) => {
      const file = pendingImageFiles.get(a);
      return file ? [{ src: URL.createObjectURL(file), name: a.name }] : [];
    });
    setPreviews(urls);
    return () => {
      for (const x of urls) URL.revokeObjectURL(x.src);
    };
  }, [attachments]);
  React.useEffect(() => {
    setStatus(undefined);
    setError("");
    if (!has) return;
    const ctl = new AbortController();
    request.current = ctl;
    setBusy(false);
    http<Status>(path, { signal: ctl.signal })
      .then((s) => {
        if (!ctl.signal.aborted) setStatus(s);
      })
      .catch((e) => {
        if (!ctl.signal.aborted) setError(e.message);
      });
    return () => ctl.abort();
  }, [has, path]);
  if (!has) return null;
  async function probe() {
    const ctl = request.current;
    if (!ctl || ctl.signal.aborted) return;
    setBusy(true);
    setError("");
    try {
      const value = await http<Status>(path, { method: "POST", signal: ctl.signal });
      if (!ctl.signal.aborted) setStatus(value);
    } catch (e) {
      if (!ctl.signal.aborted) setError(String(e));
    } finally {
      if (!ctl.signal.aborted) setBusy(false);
    }
  }
  return (
    <div className="mb-2 flex flex-wrap items-center gap-2 text-xs">
      {previews.map((p) => (
        <img key={p.src} src={p.src} alt={p.name} className="h-16 max-w-28 rounded border object-contain" />
      ))}
      {attachments.some((a) => !isChatImage(a)) && <span>本轮分析所选图片；其他附件保存在会话中。</span>}
      <span aria-live="polite">
        {error ||
          `${status?.model ?? "当前模型"} · ${status?.status === "supported" ? "支持图片" : status?.status === "unsupported" ? "未通过读图测试" : "读图能力待测试"}`}
      </span>
      <Button size="sm" variant="outline" disabled={busy} onClick={probe}>
        {busy ? "测试中…" : "测试读图"}
      </Button>
    </div>
  );
}
