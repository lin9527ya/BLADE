"use client";

import * as React from "react";

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
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { CostBudget } from "@/lib/types";

export function CostBudgetDialog({
  open,
  onOpenChange,
  scopeType,
  scopeID,
  scopeName,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  scopeType: "company" | "campaign";
  scopeID: number;
  scopeName?: string;
  onSaved?: () => void;
}) {
  const [budget, setBudget] = React.useState("0");
  const [alertPct, setAlertPct] = React.useState("80");
  const [existing, setExisting] = React.useState<CostBudget | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setLoading(true);
    api
      .costBudgets()
      .then((items) => {
        const found = items.find((item) => item.scope_type === scopeType && item.scope_id === scopeID) ?? null;
        setExisting(found);
        setBudget(String(found?.budget_usd ?? 0));
        setAlertPct(String(found?.alert_pct ?? 80));
      })
      .catch((e: Error) => toast.error(`读取预算失败：${e.message}`))
      .finally(() => setLoading(false));
  }, [open, scopeType, scopeID]);

  async function save() {
    const usd = Number(budget);
    const pct = Number(alertPct);
    if (!Number.isFinite(usd) || usd < 0) {
      toast.error("预算必须是非负数");
      return;
    }
    if (!Number.isInteger(pct) || pct <= 0 || pct > 100) {
      toast.error("告警阈值必须是 1–100 的整数");
      return;
    }
    setSaving(true);
    try {
      const item = await api.saveCostBudget({
        scope_type: scopeType,
        scope_id: scopeID,
        budget_usd: usd,
        alert_pct: pct,
      });
      setExisting(item);
      toast.success(usd > 0 ? "预算已保存" : "预算已设为不启用");
      onOpenChange(false);
      onSaved?.();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function clear() {
    if (!existing) return;
    setSaving(true);
    try {
      await api.deleteCostBudget(scopeType, scopeID);
      setExisting(null);
      setBudget("0");
      setAlertPct("80");
      toast.success("预算已清除");
      onOpenChange(false);
      onSaved?.();
    } catch (e) {
      toast.error(`清除失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{scopeType === "company" ? "公司" : "战役"} LLM 预算</DialogTitle>
          <DialogDescription>
            {scopeName ? `${scopeName} · ` : ""}按该范围的任务成本汇总判断；0 =
            不启用预算。成本归属按任务主公司/首个任务范围。
          </DialogDescription>
        </DialogHeader>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="cost-budget-usd">月度预算（USD）</FieldLabel>
            <Input
              id="cost-budget-usd"
              type="number"
              min={0}
              step="0.01"
              value={budget}
              onChange={(e) => setBudget(e.target.value)}
              disabled={loading || saving}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="cost-budget-alert">告警阈值（%）</FieldLabel>
            <Input
              id="cost-budget-alert"
              type="number"
              min={1}
              max={100}
              value={alertPct}
              onChange={(e) => setAlertPct(e.target.value)}
              disabled={loading || saving}
            />
          </Field>
        </FieldGroup>
        <DialogFooter>
          {existing ? (
            <Button variant="ghost" className="text-destructive" onClick={() => void clear()} disabled={saving}>
              清除预算
            </Button>
          ) : null}
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>
            取消
          </Button>
          <Button onClick={() => void save()} disabled={loading || saving}>
            {saving ? "保存中…" : "保存"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
