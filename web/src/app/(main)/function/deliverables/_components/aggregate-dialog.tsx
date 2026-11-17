"use client";

import * as React from "react";

import { LayersIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Campaign, Company } from "@/lib/types";

const MAX_TASKS = 200;

export function AggregateDeliverableDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => void;
}) {
  const [scopeKind, setScopeKind] = React.useState<"campaign" | "company">("campaign");
  const [scopeID, setScopeID] = React.useState("");
  const [name, setName] = React.useState("");
  const [kind, setKind] = React.useState("pentest");
  const [note, setNote] = React.useState("");
  const [campaigns, setCampaigns] = React.useState<Campaign[]>([]);
  const [companies, setCompanies] = React.useState<Company[]>([]);
  const [busy, setBusy] = React.useState(false);
  const [progress, setProgress] = React.useState(0);
  const [progressText, setProgressText] = React.useState("");

  React.useEffect(() => {
    if (!open) return;
    setScopeID("");
    setName("");
    setKind("pentest");
    setNote("");
    setProgress(0);
    setProgressText("");
    Promise.all([api.campaigns(), api.companies()])
      .then(([nextCampaigns, nextCompanies]) => {
        setCampaigns(nextCampaigns);
        setCompanies(nextCompanies);
      })
      .catch(() => {
        // Generation reports missing scope explicitly.
      });
  }, [open]);

  async function generate() {
    const id = Number(scopeID);
    if (!Number.isInteger(id) || id <= 0) {
      toast.error("请选择战役或公司");
      return;
    }
    if (!name.trim()) {
      toast.error("请填写交付物名称");
      return;
    }
    setBusy(true);
    setProgress(20);
    setProgressText("服务端生成中…");
    try {
      await api.aggregateDeliverable({
        scope_kind: scopeKind,
        scope_id: id,
        name: name.trim(),
        kind,
        note: note.trim(),
      });
      setProgress(100);
      toast.success("已生成聚合交付物");
      onOpenChange(false);
      onCreated();
    } catch (e) {
      toast.error(`生成失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
      setProgressText("");
    }
  }

  const options =
    scopeKind === "campaign"
      ? campaigns.map((c) => ({ id: c.id, label: c.name }))
      : companies.map((c) => ({ id: c.id, label: c.name }));

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <LayersIcon className="size-4" /> 从战役/公司生成交付物
          </DialogTitle>
          <DialogDescription>
            由服务端读取范围内任务报告并拼接为带任务清单的 Markdown，保存为交付物第一版。单次最多 {MAX_TASKS}{" "}
            个任务；报告会带上复测结论与人工整改建议。
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="aggregate-scope">范围类型</FieldLabel>
              <Select
                value={scopeKind}
                onValueChange={(v) => {
                  setScopeKind(v as "campaign" | "company");
                  setScopeID("");
                }}
              >
                <SelectTrigger id="aggregate-scope">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="campaign">战役</SelectItem>
                  <SelectItem value="company">公司</SelectItem>
                </SelectContent>
              </Select>
            </Field>
            <Field>
              <FieldLabel htmlFor="aggregate-id">{scopeKind === "campaign" ? "战役" : "公司"}</FieldLabel>
              <Select value={scopeID} onValueChange={setScopeID}>
                <SelectTrigger id="aggregate-id">
                  <SelectValue placeholder="选择…" />
                </SelectTrigger>
                <SelectContent>
                  {options.map((item) => (
                    <SelectItem key={item.id} value={String(item.id)}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field>
              <FieldLabel htmlFor="aggregate-name">交付物名称</FieldLabel>
              <Input
                id="aggregate-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="例如：Acme 2026 Q4 汇总报告"
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="aggregate-kind">类型</FieldLabel>
              <Select value={kind} onValueChange={setKind}>
                <SelectTrigger id="aggregate-kind">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="pentest">渗透测试</SelectItem>
                  <SelectItem value="src">SRC 交付</SelectItem>
                  <SelectItem value="retest">复测</SelectItem>
                  <SelectItem value="other">其他</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field>
            <FieldLabel htmlFor="aggregate-note">版本备注</FieldLabel>
            <Textarea
              id="aggregate-note"
              value={note}
              onChange={(e) => setNote(e.target.value)}
              rows={2}
              placeholder="例如：由战役任务报告聚合"
            />
            <FieldDescription>报告读取失败的任务会写入任务清单并注明原因，不会静默丢弃。</FieldDescription>
          </Field>
          {busy ? (
            <div className="flex flex-col gap-1.5">
              <Progress value={progress} className="h-1.5" />
              <span className="text-muted-foreground text-xs">{progressText}</span>
            </div>
          ) : null}
        </FieldGroup>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={busy}>
            取消
          </Button>
          <Button onClick={() => void generate()} disabled={busy || !scopeID || !name.trim()}>
            {busy ? "生成中…" : "生成交付物"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
