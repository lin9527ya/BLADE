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
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { buildFindingTriage, triageNoteLabel } from "@/lib/finding-triage";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingStatus } from "@/lib/types";

function FindingTriageDialog({
  finding,
  status,
  onClose,
  onSaved,
}: {
  finding: Finding;
  status: FindingStatus;
  onClose: () => void;
  onSaved: (finding: Finding, updated: Finding) => void;
}) {
  const [confidence, setConfidence] = React.useState(finding.confidence ?? "");
  const notes: Partial<Record<FindingStatus, string>> = {
    ruled_out: finding.counterevidence ?? "",
    open_proof_gap: finding.proof_gap ?? "",
    retracted: finding.retract_reason ?? "",
  };
  const [note, setNote] = React.useState(notes[status] ?? "");
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState("");
  const label = triageNoteLabel(status);

  async function save() {
    if (saving) return;
    setError("");
    let fields: ReturnType<typeof buildFindingTriage>;
    try {
      fields = buildFindingTriage(status, confidence, note);
    } catch (e) {
      setError((e as Error).message);
      return;
    }
    setSaving(true);
    try {
      const updated = await api.updateFinding(finding.finding_id ?? finding.id, fields);
      onSaved(finding, updated);
      toast.success(`处置信息已保存：${statusMeta("finding", status).label}`);
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && !saving && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>发现处置 · {statusMeta("finding", status).label}</DialogTitle>
          <DialogDescription>保存状态与置信度；排除、补证或撤回必须说明依据。不修改原始证据。</DialogDescription>
        </DialogHeader>
        <div className="space-y-2">
          <Label htmlFor="finding-confidence">置信度</Label>
          <Select
            value={confidence || "unset"}
            onValueChange={(v) => {
              setConfidence(v === "unset" ? "" : v);
              setError("");
            }}
            disabled={saving}
          >
            <SelectTrigger id="finding-confidence">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="unset">未标定</SelectItem>
              <SelectItem value="high">高 · 可复现触发链</SelectItem>
              <SelectItem value="medium">中 · 强异常，尚缺完整对照</SelectItem>
              <SelectItem value="low">低 · 据指纹、版本或现象推断</SelectItem>
            </SelectContent>
          </Select>
        </div>
        {label && (
          <div className="space-y-2">
            <Label htmlFor="finding-triage-note">
              {label} <span className="text-destructive">*</span>
            </Label>
            <Textarea
              id="finding-triage-note"
              value={note}
              onChange={(e) => {
                setNote(e.target.value);
                setError("");
              }}
              disabled={saving}
              maxLength={16000}
              rows={5}
            />
          </div>
        )}
        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button onClick={save} disabled={saving}>
            {saving ? "保存中…" : "保存处置信息"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export function useFindingTriage(onSaved: (finding: Finding, updated: Finding) => void) {
  const [selection, setSelection] = React.useState<{ finding: Finding; status: FindingStatus } | null>(null);
  const openTriage = React.useCallback((finding: Finding, status: FindingStatus) => {
    if (!finding.inherited) setSelection({ finding, status });
  }, []);
  return {
    openTriage,
    dialog: selection && (
      <FindingTriageDialog
        key={`${selection.finding.finding_id ?? selection.finding.id}:${selection.status}`}
        {...selection}
        onClose={() => setSelection(null)}
        onSaved={onSaved}
      />
    ),
  };
}
