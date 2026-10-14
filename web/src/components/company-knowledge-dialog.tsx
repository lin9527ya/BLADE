"use client";

import * as React from "react";

import { BookOpenIcon, RotateCcwIcon, Trash2Icon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { Company, CompanyKnowledge } from "@/lib/types";

const KIND_LABELS: Record<string, string> = {
  fact: "事实",
  conclusion: "结论",
  asset_hint: "资产线索",
  playbook_hint: "打法提示",
};

/**
 * CompanyKnowledgeDialog 查看/废弃/恢复某公司的知识（P23）。
 * Agent 只能沉淀（promote_knowledge）；废弃与恢复是人的动作，所以只在这里做。
 */
export function CompanyKnowledgeDialog({ company, onChanged }: { company: Company; onChanged?: () => void }) {
  const [open, setOpen] = React.useState(false);
  const [rows, setRows] = React.useState<CompanyKnowledge[]>([]);
  const [busy, setBusy] = React.useState(false);
  const [deprecatingID, setDeprecatingID] = React.useState<number | null>(null);
  const [reason, setReason] = React.useState("");

  const load = React.useCallback(async () => {
    try {
      const r = await api.companyKnowledge(company.id);
      setRows(r.knowledge);
    } catch {
      // 瞬时错误忽略
    }
  }, [company.id]);

  React.useEffect(() => {
    if (!open) return;
    void load();
  }, [open, load]);

  async function deprecate(id: number) {
    if (!reason.trim()) {
      toast.error("废弃必须写理由（后来人最需要的就是这句）");
      return;
    }
    setBusy(true);
    try {
      await api.deprecateCompanyKnowledge(company.id, id, reason.trim());
      toast.success("已废弃：同公司任务不再把它当先验");
      setDeprecatingID(null);
      setReason("");
      await load();
      onChanged?.();
    } catch (e) {
      toast.error("废弃失败：" + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function restore(id: number) {
    setBusy(true);
    try {
      await api.restoreCompanyKnowledge(company.id, id);
      toast.success("已恢复");
      await load();
      onChanged?.();
    } catch (e) {
      toast.error("恢复失败：" + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const active = rows.filter((r) => r.status === "active").length;

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button variant="ghost" size="sm" className="h-7 gap-1 px-2 text-xs" title="查看公司级知识（跨任务只读先验）">
          <BookOpenIcon className="size-3.5" />
          知识
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{company.name} · 公司知识</DialogTitle>
          <DialogDescription>
            跨任务沉淀的结论，同公司任务会在态势里当**只读先验**读到（不代表那些任务已测过）。
            共 {rows.length} 条，生效中 {active} 条。Agent 只能沉淀；废弃与恢复在这里做。
          </DialogDescription>
        </DialogHeader>
        <div className="flex max-h-[60vh] flex-col gap-2 overflow-y-auto">
          {rows.length === 0 && (
            <p className="text-muted-foreground py-6 text-center text-sm">
              还没有沉淀。任务里可用 promote_knowledge 工具，或在发现详情页点「沉淀到公司知识」。
            </p>
          )}
          {rows.map((k) => (
            <div key={k.id} className="flex flex-col gap-1 rounded-md border px-3 py-2">
              <div className="flex flex-wrap items-center gap-2">
                <span className="bg-muted text-muted-foreground rounded px-1.5 py-0.5 text-[11px]">
                  {KIND_LABELS[k.kind] ?? k.kind}
                </span>
                <span className={`min-w-0 flex-1 text-sm ${k.status === "deprecated" ? "text-muted-foreground line-through" : ""}`}>
                  {k.summary}
                </span>
                {k.confidence && (
                  <span className="text-muted-foreground text-[11px]">置信度 {k.confidence}</span>
                )}
                {k.status === "deprecated" && <span className="text-[11px] text-amber-600">已废弃</span>}
              </div>
              {k.detail && <p className="text-muted-foreground text-xs whitespace-pre-wrap">{k.detail}</p>}
              <div className="text-muted-foreground flex flex-wrap items-center gap-x-3 text-[11px]">
                {k.source_task_id && (
                  <a className="hover:underline" href={`/function/tasks/detail?id=${encodeURIComponent(k.source_task_id)}`}>
                    来源任务 #{k.source_task_id}
                  </a>
                )}
                <span>{new Date(k.created_at).toLocaleString("zh-CN", { hour12: false })}</span>
                {k.status === "deprecated" && k.deprecated_reason && <span>废弃理由：{k.deprecated_reason}</span>}
              </div>
              {deprecatingID === k.id ? (
                <div className="flex items-center gap-1.5">
                  <Input
                    className="h-7 flex-1 text-xs"
                    placeholder="为什么不算数了（必填）"
                    value={reason}
                    onChange={(e) => setReason(e.target.value)}
                    disabled={busy}
                  />
                  <Button size="sm" className="h-7 px-2 text-xs" disabled={busy} onClick={() => void deprecate(k.id)}>
                    确认废弃
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    className="h-7 px-2 text-xs"
                    disabled={busy}
                    onClick={() => {
                      setDeprecatingID(null);
                      setReason("");
                    }}
                  >
                    取消
                  </Button>
                </div>
              ) : (
                <div className="flex items-center gap-1.5">
                  {k.status === "active" ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-6 px-1.5 text-xs"
                      disabled={busy}
                      onClick={() => setDeprecatingID(k.id)}
                    >
                      <Trash2Icon className="size-3.5" /> 废弃
                    </Button>
                  ) : (
                    <Button size="sm" variant="ghost" className="h-6 px-1.5 text-xs" disabled={busy} onClick={() => void restore(k.id)}>
                      <RotateCcwIcon className="size-3.5" /> 恢复
                    </Button>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
