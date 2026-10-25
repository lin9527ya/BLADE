"use client";

import * as React from "react";

import { Loader2Icon, ShieldAlertIcon } from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";
import type { TaskArtifact } from "@/lib/types";

// 遗留物人工销账：与 agent 的 mark_artifact_cleared / accept_artifact_risk 同一套规矩。
// 这里不提供"一键标已清理"——证据必填，否则"清掉了"只是个说法。

function artifactRank(a: TaskArtifact): number {
  if (a.state === "active") return 0;
  if (a.state === "unknown") return 1;
  return 2;
}

export function ArtifactPanel({
  artifacts,
  riskAccepted,
  onClear,
  onAcceptRisk,
}: {
  artifacts: TaskArtifact[];
  riskAccepted: boolean;
  onClear: (a: TaskArtifact, evidence: string, note?: string) => Promise<void>;
  onAcceptRisk: (reason: string) => Promise<void>;
}) {
  const [clearing, setClearing] = React.useState<TaskArtifact | null>(null);
  const [riskOpen, setRiskOpen] = React.useState(false);
  const [evidence, setEvidence] = React.useState("");
  const [note, setNote] = React.useState("");
  const [reason, setReason] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");

  const ordered = React.useMemo(
    () => [...artifacts].sort((a, b) => artifactRank(a) - artifactRank(b) || b.ts.localeCompare(a.ts)),
    [artifacts],
  );
  const openCount = ordered.filter((a) => a.state === "active").length;

  const closeClear = () => {
    if (busy) return;
    setClearing(null);
    setEvidence("");
    setNote("");
    setError("");
  };

  const submitClear = async () => {
    if (!clearing || !evidence.trim()) return;
    setBusy(true);
    setError("");
    try {
      await onClear(clearing, evidence.trim(), note.trim() || undefined);
      setClearing(null);
      setEvidence("");
      setNote("");
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "销账失败");
    } finally {
      setBusy(false);
    }
  };

  const submitRisk = async () => {
    if (!reason.trim()) return;
    setBusy(true);
    setError("");
    try {
      await onAcceptRisk(reason.trim());
      setRiskOpen(false);
      setReason("");
    } catch (e) {
      setError(e instanceof Error ? e.message : "提交失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      {ordered.length > 0 ? (
        <div className="flex max-h-80 flex-col gap-1.5 overflow-y-auto pr-1">
          {ordered.map((a) => (
            <div
              key={a.id}
              className={`rounded-md border px-2.5 py-1.5 text-sm ${
                a.state === "removed" ? "opacity-60" : ""
              }`}
            >
              <div className="flex flex-wrap items-center gap-2">
                <StatusBadge domain="artifact" value={a.state} />
                {a.kind_label && <span className="text-xs font-medium">{a.kind_label}</span>}
                <span className="min-w-0 flex-1 break-words">{a.summary || a.locator || `#${a.id}`}</span>
                {a.state !== "removed" && (
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-6 shrink-0 px-2 text-xs"
                    onClick={() => {
                      setError("");
                      setClearing(a);
                    }}
                  >
                    标记已清理
                  </Button>
                )}
              </div>
              <div className="text-muted-foreground mt-0.5 flex flex-wrap gap-x-3 gap-y-0.5 text-[11px]">
                {a.locator && <span title="目标上的位置">位置：{a.locator}</span>}
                {a.host && <span>主机：{a.host}</span>}
                {a.url && (
                  <span className="break-all" title="访问入口">
                    入口：{a.url}
                  </span>
                )}
                {a.cleanup && (
                  <span className="break-all" title="建议的清理方式">
                    清理方式：{a.cleanup}
                  </span>
                )}
              </div>
              {a.removed_evidence && (
                <p className="mt-1 text-[11px] break-words">
                  <span className="text-muted-foreground">清理证据：</span>
                  {a.removed_evidence}
                </p>
              )}
              {a.removed_note && (
                <p className="text-muted-foreground text-[11px] break-words">清理备注：{a.removed_note}</p>
              )}
            </div>
          ))}
        </div>
      ) : (
        <p className="text-muted-foreground text-sm">暂无遗留物记录。</p>
      )}

      {riskAccepted && (
        <p className="text-muted-foreground text-xs">已接受遗留物风险：报告里会写明「我们知道留了什么、为什么没清」。</p>
      )}
      {!riskAccepted && openCount > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="ghost" className="text-xs" onClick={() => setRiskOpen(true)}>
            <ShieldAlertIcon className="size-3.5" /> 接受风险（带着遗留物交付）
          </Button>
          <span className="text-muted-foreground text-xs">优先清理：清不掉或客户要求保留时才走这条，并写清理由。</span>
        </div>
      )}

      <Dialog open={clearing !== null} onOpenChange={(o) => !o && closeClear()}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>标记遗留物已清理</DialogTitle>
            <DialogDescription>
              必须给清理命令 + 关键输出行。没有证据，「清掉了」只是个说法——而它决定的是客户系统上还有没有后门。
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2 text-sm">
            <p className="rounded-md bg-muted/50 p-2 text-xs break-words">
              {clearing?.summary || clearing?.locator || `#${clearing?.id}`}
            </p>
            <Textarea
              value={evidence}
              onChange={(e) => setEvidence(e.target.value)}
              placeholder="清理命令 + 关键输出行，如：rm -f /var/www/html/x.php && echo removed"
              rows={4}
              disabled={busy}
              autoFocus
            />
            <Textarea
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="备注（可选）：残留、需人工确认的部分等"
              rows={2}
              disabled={busy}
            />
            {error && <p className="text-destructive text-xs">{error}</p>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeClear} disabled={busy}>
              取消
            </Button>
            <Button onClick={() => void submitClear()} disabled={busy || !evidence.trim()}>
              {busy ? <Loader2Icon className="animate-spin" data-icon="inline-start" /> : null}
              确认已清理
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={riskOpen}
        onOpenChange={(o) => {
          if (busy) return;
          setRiskOpen(o);
          if (!o) {
            setReason("");
            setError("");
          }
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>接受遗留物风险</DialogTitle>
            <DialogDescription>
              这会在图上写一条结论并进入报告语境：客户复盘时能看到「我们知道自己留了什么、为什么没清」。
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2 text-sm">
            <Textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="为什么不能清理：客户要求保留 / 已失去访问 / 清理会造成业务中断…"
              rows={3}
              disabled={busy}
              autoFocus
            />
            {error && <p className="text-destructive text-xs">{error}</p>}
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setRiskOpen(false);
                setReason("");
                setError("");
              }}
              disabled={busy}
            >
              取消
            </Button>
            <Button variant="destructive" onClick={() => void submitRisk()} disabled={busy || !reason.trim()}>
              {busy ? <Loader2Icon className="animate-spin" data-icon="inline-start" /> : null}
              接受风险
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
