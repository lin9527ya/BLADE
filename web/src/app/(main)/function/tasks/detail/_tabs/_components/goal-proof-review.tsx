"use client";

import * as React from "react";

import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { GoalProofPage, GoalProofSummary } from "@/lib/types";

const labels: Record<string, string> = {
  source_unavailable: "来源不可用",
  evidence_removed: "证据已删除",
  relation_removed: "证明关系已移除",
  material_changed: "材料已修改",
  finding_record_changed: "发现记录已更换",
  finding_evidence_changed: "发现的证据已换版",
  finding_status_changed: "发现状态已改变",
  expired: "已过期",
  contested: "存在矛盾",
  superseded: "已被替代",
};

export function GoalProofReview({
  taskId,
  goalId,
  summary,
}: {
  taskId: string;
  goalId: string;
  summary: GoalProofSummary;
}) {
  const [open, setOpen] = React.useState(false);
  const [cursors, setCursors] = React.useState<string[]>([""]);
  const [page, setPage] = React.useState<GoalProofPage | null>(null);
  const [error, setError] = React.useState("");

  React.useEffect(() => {
    if (!open || !summary.total) return;
    const controller = new AbortController();
    const before = cursors[cursors.length - 1];
    setError("");
    api.goalProofs(taskId, goalId, before, controller.signal).then(
      (result) => {
        if (!controller.signal.aborted) setPage(result);
      },
      (err: Error) => {
        if (!controller.signal.aborted) {
          setPage(null);
          setError(err.message || "读取证明失败");
        }
      },
    );
    return () => controller.abort();
  }, [taskId, goalId, cursors, open, summary]);

  if (!summary.total) return <p className="text-muted-foreground text-xs">缺少可核对的证明</p>;
  return (
    <div className="text-xs">
      <button
        className="cursor-pointer text-left underline-offset-2 hover:underline"
        type="button"
        aria-expanded={open}
        onClick={() => {
          setCursors([""]);
          setPage(null);
          setOpen(!open);
        }}
      >
        {summary.total} 条证明 · {summary.needs_review} 条待复核
        {summary.history_unknown > 0 && ` · ${summary.history_unknown} 条历史版本未知`}
        <span className="ml-2 text-muted-foreground">{open ? "收起" : "查看"}</span>
      </button>
      {open && (
        <div className="mt-2 border-l pl-3" aria-live="polite">
          {error && (
            <p>
              {error}{" "}
              <Button size="sm" variant="ghost" onClick={() => setCursors([...cursors])}>
                重试
              </Button>
            </p>
          )}
          {!error && !page && <p>正在读取证明…</p>}
          {!error && page && (
            <>
              <ul className="space-y-2">
                {page.proofs.map((proof) => (
                  <li key={proof.evidence_id}>
                    <div className="font-medium">
                      证据 #{proof.evidence_id}
                      {proof.proved_at && (
                        <time className="ml-2 font-normal text-muted-foreground" dateTime={proof.proved_at}>
                          {new Date(proof.proved_at).toLocaleString()}
                        </time>
                      )}
                    </div>
                    {proof.changes.length > 0 && (
                      <p className="text-amber-700 dark:text-amber-400">
                        {proof.changes.map((code) => labels[code] ?? code).join("、")}
                      </p>
                    )}
                    {!!proof.current_flags?.length && (
                      <p>当前状态 · {proof.current_flags.map((code) => labels[code] ?? code).join("、")}</p>
                    )}
                    {proof.history_unknown && <p>历史版本未知</p>}
                    {!proof.changes.length && !proof.current_flags?.length && !proof.history_unknown && (
                      <p>材料版本一致</p>
                    )}
                    {proof.reason && (
                      <p className="whitespace-pre-wrap break-words text-muted-foreground">{proof.reason}</p>
                    )}
                  </li>
                ))}
              </ul>
              {page.proofs.length === 0 && <p>本页暂无证明</p>}
            </>
          )}
          {(cursors.length > 1 || page?.next_before) && (
            <div className="mt-2 flex gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={cursors.length < 2}
                onClick={() => {
                  setPage(null);
                  setCursors(cursors.slice(0, -1));
                }}
              >
                上一页
              </Button>
              <Button
                size="sm"
                variant="outline"
                disabled={!page?.next_before}
                onClick={() => {
                  if (page?.next_before) {
                    setCursors([...cursors, page.next_before]);
                    setPage(null);
                  }
                }}
              >
                下一页
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
