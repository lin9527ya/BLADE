"use client";

import * as React from "react";

import { CalendarClockIcon, MessageSquareIcon, Trash2Icon, UserRoundIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { Finding } from "@/lib/types";

function dateOnly(value?: string): string {
  if (!value) return "";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "";
  const month = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${d.getFullYear()}-${month}-${day}`;
}

function fmtTime(value: string): string {
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

// FindingCollaborationPanel 把「谁能跟进、什么时候到期、关联哪个 CVE、怎么修」
// 与评论留痕放在一起；只改发现行，不触碰探索图或报告正文。
export function FindingCollaborationPanel({ finding, onChanged }: { finding: Finding; onChanged: () => void }) {
  const id = finding.finding_id ?? finding.id;
  const readOnly = Boolean(finding.inherited);
  const [assignee, setAssignee] = React.useState(finding.assignee ?? "");
  const [dueAt, setDueAt] = React.useState(dateOnly(finding.due_at));
  const [cvss, setCVSS] = React.useState(finding.cvss_score ? String(finding.cvss_score) : "");
  const [cveText, setCVEText] = React.useState((finding.cve_ids ?? []).join(", "));
  const [remediation, setRemediation] = React.useState(finding.remediation ?? "");
  const [saving, setSaving] = React.useState(false);
  const [comment, setComment] = React.useState("");
  const [commenting, setCommenting] = React.useState(false);

  React.useEffect(() => {
    setAssignee(finding.assignee ?? "");
    setDueAt(dateOnly(finding.due_at));
    setCVSS(finding.cvss_score ? String(finding.cvss_score) : "");
    setCVEText((finding.cve_ids ?? []).join(", "));
    setRemediation(finding.remediation ?? "");
  }, [finding.assignee, finding.cve_ids, finding.cvss_score, finding.due_at, finding.remediation]);

  async function save() {
    const score = cvss.trim() === "" ? 0 : Number(cvss);
    if (!Number.isFinite(score) || score < 0 || score > 10) {
      toast.error("CVSS 分数必须在 0–10 之间");
      return;
    }
    setSaving(true);
    try {
      await api.updateFinding(id, {
        assignee: assignee.trim(),
        due_at: dueAt,
        cvss_score: score,
        cve_ids: cveText
          .split(/[,，\s]+/)
          .map((item) => item.trim())
          .filter(Boolean),
        remediation: remediation.trim(),
      });
      toast.success("协作信息已保存");
      onChanged();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function addComment() {
    const body = comment.trim();
    if (!body || commenting) return;
    setCommenting(true);
    try {
      await api.addFindingComment(id, body);
      setComment("");
      toast.success("评论已添加");
      onChanged();
    } catch (e) {
      toast.error(`评论失败：${(e as Error).message}`);
    } finally {
      setCommenting(false);
    }
  }

  async function removeComment(commentID: number) {
    try {
      await api.deleteFindingComment(id, commentID);
      toast.success("评论已删除");
      onChanged();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }

  const comments = finding.comments ?? [];

  return (
    <Card className="h-fit">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-sm">
          <UserRoundIcon className="size-4" /> 协作与跟进
        </CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="finding-assignee">负责人</FieldLabel>
            <Input
              id="finding-assignee"
              value={assignee}
              onChange={(e) => setAssignee(e.target.value)}
              placeholder="例如：张三"
              disabled={readOnly || saving}
              maxLength={120}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="finding-due">截止日期</FieldLabel>
            <Input
              id="finding-due"
              type="date"
              value={dueAt}
              onChange={(e) => setDueAt(e.target.value)}
              disabled={readOnly || saving}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="finding-cvss">CVSS</FieldLabel>
            <Input
              id="finding-cvss"
              type="number"
              min={0}
              max={10}
              step={0.1}
              value={cvss}
              onChange={(e) => setCVSS(e.target.value)}
              placeholder="0–10"
              disabled={readOnly || saving}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="finding-cves">关联 CVE</FieldLabel>
            <Input
              id="finding-cves"
              value={cveText}
              onChange={(e) => setCVEText(e.target.value)}
              placeholder="CVE-2026-12345, blade-2026-0001"
              disabled={readOnly || saving}
            />
          </Field>
        </div>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor="finding-remediation">修复建议 / 处置记录</FieldLabel>
            <Textarea
              id="finding-remediation"
              value={remediation}
              onChange={(e) => setRemediation(e.target.value)}
              placeholder="写清修复动作、责任边界与复测结果"
              disabled={readOnly || saving}
              rows={5}
              maxLength={8000}
            />
            <FieldDescription className="flex justify-between gap-2">
              <span className="inline-flex items-center gap-1">
                <CalendarClockIcon className="size-3.5" />
                {finding.due_at ? `截止 ${fmtTime(finding.due_at)}` : "未设置截止时间"}
              </span>
              <span className="tabular-nums">{remediation.length} / 8000</span>
            </FieldDescription>
          </Field>
        </FieldGroup>
        {!readOnly ? (
          <Button size="sm" onClick={() => void save()} disabled={saving}>
            {saving ? "保存中…" : "保存协作信息"}
          </Button>
        ) : null}

        <div className="border-t pt-4">
          <div className="mb-2 flex items-center justify-between">
            <span className="flex items-center gap-1.5 font-medium text-sm">
              <MessageSquareIcon className="size-4" /> 评论
              <span className="text-muted-foreground text-xs">({comments.length})</span>
            </span>
          </div>
          <div className="flex flex-col gap-2">
            {comments.map((item) => (
              <div key={item.id} className="rounded-md border bg-muted/20 px-3 py-2">
                <div className="flex items-center justify-between gap-2 text-muted-foreground text-xs">
                  <span>
                    {item.author || "operator"} · {fmtTime(item.created_at)}
                  </span>
                  {!readOnly ? (
                    <Button
                      variant="ghost"
                      size="icon-xs"
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => void removeComment(item.id)}
                      aria-label="删除评论"
                    >
                      <Trash2Icon />
                    </Button>
                  ) : null}
                </div>
                <p className="mt-1 whitespace-pre-wrap break-words text-sm">{item.body}</p>
              </div>
            ))}
            {comments.length === 0 ? <p className="text-muted-foreground text-xs">还没有评论。</p> : null}
          </div>
          {!readOnly ? (
            <div className="mt-3 flex flex-col gap-2">
              <Textarea
                value={comment}
                onChange={(e) => setComment(e.target.value)}
                placeholder="记录跟进动作、沟通结论或复测结果…"
                rows={3}
                maxLength={4000}
                disabled={commenting}
              />
              <Button
                size="sm"
                variant="outline"
                className="self-end"
                onClick={() => void addComment()}
                disabled={commenting || !comment.trim()}
              >
                {commenting ? "提交中…" : "添加评论"}
              </Button>
            </div>
          ) : null}
        </div>
      </CardContent>
    </Card>
  );
}
