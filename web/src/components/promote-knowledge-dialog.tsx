"use client";

import * as React from "react";

import { LibraryIcon } from "lucide-react";
import { toast } from "sonner";

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
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Finding, TaskScopeRow } from "@/lib/types";

/**
 * PromoteKnowledgeDialog：在发现详情页把一条已确认的结论沉淀为公司知识（P23）。
 * 与 Agent 的 promote_knowledge 工具同一判定：只允许已确认的发现、公司必须是本任务
 * 关联的企业之一（公司列表从任务的 task_scope 里取）。
 */
export function PromoteKnowledgeDialog({ finding }: { finding: Finding }) {
  const [open, setOpen] = React.useState(false);
  const [companies, setCompanies] = React.useState<TaskScopeRow[]>([]);
  const [companyID, setCompanyID] = React.useState<string>("");
  const [kind, setKind] = React.useState("conclusion");
  const [summary, setSummary] = React.useState("");
  const [detail, setDetail] = React.useState("");
  const [confidence, setConfidence] = React.useState<string>(finding.confidence ?? "");
  const [busy, setBusy] = React.useState(false);

  React.useEffect(() => {
    if (!open || !finding.task_id) return;
    let alive = true;
    void api
      .taskScope(String(finding.task_id))
      .then((r) => {
        if (!alive) return;
        const cs = (r.scope ?? []).filter((s) => s.kind === "company");
        setCompanies(cs);
        setCompanyID(cs[0]?.company_id ? String(cs[0].company_id) : "");
      })
      .catch(() => {});
    setSummary((prev) => prev || finding.summary || finding.name || finding.vulnclass || "");
    return () => {
      alive = false;
    };
  }, [open, finding]);

  async function submit() {
    const cid = Number(companyID);
    if (!cid) {
      toast.error("先选一个公司（该任务没有关联企业时先在任务里关联）");
      return;
    }
    if (!summary.trim()) {
      toast.error("摘要必填");
      return;
    }
    setBusy(true);
    try {
      // finding.id 是任务内节点 id 的字符串形式；独立 findings 表的行 id 在 finding_id 上
      // （状态更新用的就是它）——两个都指向同一条发现，这里优先用后者。
      await api.promoteFindingKnowledge(finding.finding_id ?? finding.id, {
        company_id: cid,
        kind,
        summary: summary.trim(),
        detail: detail.trim(),
        confidence: confidence || undefined,
      });
      toast.success("已沉淀为公司知识：同公司的其它任务会在态势里读到它");
      setOpen(false);
    } catch (e) {
      toast.error("沉淀失败：" + (e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button
          size="sm"
          variant="outline"
          className="h-7 px-2 text-xs"
          title="把这条已确认的结论沉淀为公司级知识（同公司任务当只读先验读到）"
        >
          <LibraryIcon className="size-3.5" />
          沉淀到公司知识
        </Button>
      </DialogTrigger>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>沉淀为公司知识</DialogTitle>
          <DialogDescription>
            只沉淀**已确认**的结论；同公司的其它任务会把它当只读先验读到（不代表它们已测过）。 只读背景：Agent
            改不了，要撤下请到 资产页 → 企业 → 知识 里废弃。
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {companies.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              该任务没有关联任何企业，无法沉淀：先在任务里关联企业（或把公司加进任务范围）。
            </p>
          ) : (
            <>
              <div className="grid gap-2">
                <Label htmlFor="pk-company">公司</Label>
                <NativeSelect id="pk-company" value={companyID} onChange={(e) => setCompanyID(e.target.value)}>
                  {companies.map((c) => (
                    <NativeSelectOption key={c.id} value={String(c.company_id)}>
                      {c.company_name || `#${c.company_id}`}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="pk-kind">类型</Label>
                <NativeSelect id="pk-kind" value={kind} onChange={(e) => setKind(e.target.value)}>
                  <NativeSelectOption value="conclusion">结论</NativeSelectOption>
                  <NativeSelectOption value="fact">事实</NativeSelectOption>
                  <NativeSelectOption value="asset_hint">资产线索</NativeSelectOption>
                  <NativeSelectOption value="playbook_hint">打法提示</NativeSelectOption>
                </NativeSelect>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="pk-summary">一行式结论（后来人先看这句）</Label>
                <Input id="pk-summary" value={summary} onChange={(e) => setSummary(e.target.value)} disabled={busy} />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="pk-detail">展开说明（可选：为什么成立、怎么验证、适用边界）</Label>
                <Textarea
                  id="pk-detail"
                  rows={3}
                  value={detail}
                  onChange={(e) => setDetail(e.target.value)}
                  disabled={busy}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="pk-conf">置信度（没把握就留空，平台不猜）</Label>
                <NativeSelect id="pk-conf" value={confidence} onChange={(e) => setConfidence(e.target.value)}>
                  <NativeSelectOption value="">不标定</NativeSelectOption>
                  <NativeSelectOption value="high">高</NativeSelectOption>
                  <NativeSelectOption value="medium">中</NativeSelectOption>
                  <NativeSelectOption value="low">低</NativeSelectOption>
                </NativeSelect>
              </div>
            </>
          )}
        </div>
        <DialogFooter>
          <Button size="sm" disabled={busy || companies.length === 0} onClick={() => void submit()}>
            沉淀
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
