"use client";

import * as React from "react";

import { RefreshCwIcon, ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { AttackCoverage, Campaign, DetectionMatrix, Task } from "@/lib/types";

function fmtTime(value?: string): string {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString("zh-CN", { hour12: false });
}

const STATE_LABEL: Record<string, string> = {
  done: "完成",
  running: "进行中",
  open: "未领取",
  blocked: "阻塞",
  exhausted: "穷尽",
  no_output: "无产出",
};

const MATRIX_LABEL: Record<string, string> = {
  triggered: "已检出",
  not_triggered: "未检出",
  unknown: "无法判定",
  unmeasured: "未记录",
  not_attempted: "未尝试",
};

const MATRIX_CLASS: Record<string, string> = {
  triggered: "bg-destructive/15 text-destructive",
  not_triggered: "bg-emerald-500/15 text-emerald-600",
  unknown: "bg-amber-500/15 text-amber-600",
  unmeasured: "bg-muted text-muted-foreground",
  not_attempted: "text-muted-foreground",
};

export default function CoveragePage() {
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [campaigns, setCampaigns] = React.useState<Campaign[]>([]);
  const [mode, setMode] = React.useState<"task" | "campaign">("task");
  const [scopeId, setScopeId] = React.useState("");
  const [coverage, setCoverage] = React.useState<AttackCoverage | null>(null);
  const [matrix, setMatrix] = React.useState<DetectionMatrix | null>(null);
  const [loading, setLoading] = React.useState(false);

  const [formTask, setFormTask] = React.useState("");
  const [form, setForm] = React.useState({
    summary: "",
    technique: "T1046",
    result: "not_triggered",
    evidence: "",
    confidence: "medium",
    detail: "",
    detector: "",
    log_source: "",
    time_window: "",
  });
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    Promise.all([api.tasks(), api.campaigns()])
      .then(([t, c]) => {
        setTasks(t.tasks);
        setCampaigns(c);
      })
      .catch((e: Error) => toast.error(`读取任务/战役失败：${e.message}`));
  }, []);

  const load = React.useCallback(() => {
    if (!scopeId) {
      setCoverage(null);
      setMatrix(null);
      return;
    }
    setLoading(true);
    const params = mode === "task" ? { task: scopeId } : { campaign: Number(scopeId) };
    Promise.all([api.attackCoverage(params), api.detectionMatrix(params)])
      .then(([cov, mat]) => {
        setCoverage(cov);
        setMatrix(mat);
      })
      .catch((e: Error) => toast.error(`读取覆盖失败：${e.message}`))
      .finally(() => setLoading(false));
  }, [mode, scopeId]);

  React.useEffect(() => {
    load();
  }, [load]);

  async function submitDetection() {
    if (!formTask) {
      toast.error("请选择任务");
      return;
    }
    if (!form.summary.trim() || !form.evidence.trim()) {
      toast.error("summary 与 evidence 必填");
      return;
    }
    setSaving(true);
    try {
      await api.recordDetection(formTask, {
        summary: form.summary.trim(),
        technique: form.technique.trim(),
        result: form.result as "triggered" | "not_triggered" | "unknown",
        evidence: form.evidence.trim(),
        confidence: form.confidence as "high" | "medium" | "low",
        detail: form.detail.trim() || undefined,
        detector: form.detector.trim() || undefined,
        log_source: form.log_source.trim() || undefined,
        time_window: form.time_window.trim() || undefined,
      });
      toast.success("检测结果已记录");
      setForm({ ...form, summary: "", evidence: "", detail: "" });
      if ((mode === "task" && scopeId === formTask) || mode === "campaign") load();
    } catch (e) {
      toast.error(`记录失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const matrixTechniques = matrix?.techniques.slice(0, 12) ?? [];

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 font-semibold text-xl tracking-tight">
            <ShieldCheckIcon className="size-5" /> 覆盖与检测
          </h1>
          <p className="text-muted-foreground text-sm">
            只统计已标注 ATT&CK 技术的意图与检测记录；不计算覆盖率，「无法判定」独立存在。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Select value={mode} onValueChange={(v) => { setMode(v as "task" | "campaign"); setScopeId(""); }}>
            <SelectTrigger className="w-28"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="task">按任务</SelectItem>
              <SelectItem value="campaign">按战役</SelectItem>
            </SelectContent>
          </Select>
          <Select value={scopeId} onValueChange={setScopeId}>
            <SelectTrigger className="w-64">
              <SelectValue placeholder={mode === "task" ? "选择任务" : "选择战役"} />
            </SelectTrigger>
            <SelectContent>
              {mode === "task"
                ? tasks.map((t) => <SelectItem key={t.id} value={String(t.id)}>{t.name?.trim() || t.description}</SelectItem>)
                : campaigns.map((c) => <SelectItem key={c.id} value={String(c.id)}>{c.name}</SelectItem>)}
            </SelectContent>
          </Select>
          <Button variant="outline" size="sm" onClick={load} disabled={loading || !scopeId}>
            <RefreshCwIcon /> 刷新
          </Button>
        </div>
      </div>

      <Tabs defaultValue="coverage">
        <TabsList>
          <TabsTrigger value="coverage">ATT&CK 覆盖</TabsTrigger>
          <TabsTrigger value="matrix">检测矩阵</TabsTrigger>
          <TabsTrigger value="record">录入检测</TabsTrigger>
        </TabsList>

        <TabsContent value="coverage" className="mt-3">
          <Card className="overflow-hidden py-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-28">技术</TableHead>
                  <TableHead className="w-32">意图</TableHead>
                  <TableHead className="w-48">意图状态</TableHead>
                  <TableHead className="w-48">检测</TableHead>
                  <TableHead>预计信号</TableHead>
                  <TableHead className="w-24">资产</TableHead>
                  <TableHead className="w-44">最近</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {loading && !coverage ? (
                  <TableRow><TableCell colSpan={7} className="py-10 text-center"><Spinner /></TableCell></TableRow>
                ) : !coverage || coverage.techniques.length === 0 ? (
                  <TableRow><TableCell colSpan={7} className="py-10 text-center text-muted-foreground">
                    {scopeId ? "还没有已标注技术的意图或检测记录。" : "先选择任务或战役。"}
                  </TableCell></TableRow>
                ) : (
                  coverage.techniques.map((row) => (
                    <TableRow key={row.technique}>
                      <TableCell className="font-mono text-xs">{row.technique}</TableCell>
                      <TableCell className="text-xs tabular-nums">{row.intents}</TableCell>
                      <TableCell className="text-xs">
                        {Object.entries(row.intent_states ?? {}).map(([k, n]) => (
                          <Badge key={k} variant="outline" className="mr-1">{STATE_LABEL[k] ?? k} {n}</Badge>
                        ))}
                      </TableCell>
                      <TableCell className="text-xs">
                        {(["triggered", "not_triggered", "unknown"] as const).map((k) => (
                          <Badge key={k} variant="outline" className="mr-1">
                            {MATRIX_LABEL[k]} {row.detections?.[k] ?? 0}
                          </Badge>
                        ))}
                      </TableCell>
                      <TableCell className="text-xs">
                        {row.opsec_notes?.length
                          ? row.opsec_notes.map((n) => (
                              <span key={n.id} className="mr-2" title={`预计可见度：${n.exposure}`}>{n.title}</span>
                            ))
                          : <span className="text-muted-foreground">未收录</span>}
                      </TableCell>
                      <TableCell className="text-xs tabular-nums">{row.assets?.length ?? 0}</TableCell>
                      <TableCell className="text-muted-foreground text-xs">{fmtTime(row.last_at)}</TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </Card>
          <div className="mt-2 flex flex-wrap gap-3 text-muted-foreground text-xs">
            <span>{coverage?.note ?? ""}</span>
            {coverage ? <span>未标注意图：{coverage.unlabeled_intents}</span> : null}
          </div>
          {(coverage?.unlabeled?.length ?? 0) > 0 && (
            <Card className="mt-3">
              <CardHeader>
                <CardTitle className="text-sm">未标注意图与建议</CardTitle>
                <CardDescription>建议只是关键词映射；采纳后会写进该意图的 techniques。</CardDescription>
              </CardHeader>
              <CardContent className="space-y-2">
                {coverage?.unlabeled?.map((u) => (
                  <div key={u.intent_id} className="flex flex-wrap items-center gap-2 rounded border p-2 text-xs">
                    <span className="font-mono text-muted-foreground">#{u.intent_id}</span>
                    <span className="max-w-[28rem] truncate" title={u.summary}>{u.summary || "（无摘要）"}</span>
                    {u.suggested.length === 0 ? (
                      <span className="text-muted-foreground">无建议</span>
                    ) : (
                      u.suggested.map((tech) => <Badge key={tech} variant="outline">{tech}</Badge>)
                    )}
                    {u.suggested.length > 0 && mode === "task" && scopeId && (
                      <Button
                        size="sm"
                        variant="outline"
                        className="ml-auto h-6"
                        onClick={() => {
                          void api
                            .setIntentTechniques(scopeId, u.intent_id, u.suggested)
                            .then(() => {
                              toast.success(`意图 #${u.intent_id} 已补标`);
                              load();
                            })
                            .catch((e: Error) => toast.error(`补标失败：${e.message}`));
                        }}
                      >
                        采纳
                      </Button>
                    )}
                  </div>
                ))}
              </CardContent>
            </Card>
          )}
        </TabsContent>

        <TabsContent value="matrix" className="mt-3">
          <Card className="overflow-auto py-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="sticky left-0 bg-card">资产</TableHead>
                  {matrixTechniques.map((tech) => <TableHead key={tech} className="font-mono text-xs">{tech}</TableHead>)}
                </TableRow>
              </TableHeader>
              <TableBody>
                {!matrix || matrix.rows.length === 0 ? (
                  <TableRow><TableCell colSpan={matrixTechniques.length + 1} className="py-10 text-center text-muted-foreground">
                    {scopeId ? "没有可展示的检测矩阵数据。" : "先选择任务或战役。"}
                  </TableCell></TableRow>
                ) : matrix.rows.map((row) => (
                  <TableRow key={row.asset_id}>
                    <TableCell className="sticky left-0 max-w-[18rem] truncate bg-card text-xs" title={row.label}>{row.label}</TableCell>
                    {matrixTechniques.map((tech) => {
                      const status = row.cells[tech] ?? "not_attempted";
                      return (
                        <TableCell key={tech} className="p-1">
                          <span className={`inline-block rounded px-1.5 py-0.5 text-[10px] ${MATRIX_CLASS[status] ?? ""}`}>
                            {MATRIX_LABEL[status] ?? status}
                          </span>
                        </TableCell>
                      );
                    })}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
          <p className="mt-2 text-muted-foreground text-xs">
            {matrix?.note ?? "「无法判定」不是「未检出」；矩阵不合成检出率。"}
            {matrix && matrix.techniques.length > 12 ? `（仅显示前 12 个技术，共 ${matrix.techniques.length} 个）` : ""}
          </p>
        </TabsContent>

        <TabsContent value="record" className="mt-3">
          <Card>
            <CardHeader>
              <CardTitle className="text-sm">人工录入检测结果</CardTitle>
              <CardDescription>只记录实测观察；unknown 表示无法判定，不是「没被检出」。</CardDescription>
            </CardHeader>
            <CardContent>
              <FieldGroup>
                <div className="grid gap-3 md:grid-cols-2">
                  <Field>
                    <FieldLabel>任务</FieldLabel>
                    <Select value={formTask} onValueChange={setFormTask}>
                      <SelectTrigger><SelectValue placeholder="选择任务" /></SelectTrigger>
                      <SelectContent>
                        {tasks.map((t) => <SelectItem key={t.id} value={String(t.id)}>{t.name?.trim() || t.description}</SelectItem>)}
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="det-tech">ATT&CK 技术</FieldLabel>
                    <Input id="det-tech" value={form.technique} onChange={(e) => setForm({ ...form, technique: e.target.value })} placeholder="T1046 / T1003.001" />
                  </Field>
                  <Field>
                    <FieldLabel>结果</FieldLabel>
                    <Select value={form.result} onValueChange={(v) => setForm({ ...form, result: v })}>
                      <SelectTrigger><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="triggered">triggered（确实响了）</SelectItem>
                        <SelectItem value="not_triggered">not_triggered（确实没响）</SelectItem>
                        <SelectItem value="unknown">unknown（无法判定）</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field>
                    <FieldLabel>置信度</FieldLabel>
                    <Select value={form.confidence} onValueChange={(v) => setForm({ ...form, confidence: v })}>
                      <SelectTrigger><SelectValue /></SelectTrigger>
                      <SelectContent>
                        <SelectItem value="high">high（直接看到日志/告警）</SelectItem>
                        <SelectItem value="medium">medium（间接推断）</SelectItem>
                        <SelectItem value="low">low（仅凭现象）</SelectItem>
                      </SelectContent>
                    </Select>
                  </Field>
                </div>
                <Field>
                  <FieldLabel htmlFor="det-summary">摘要</FieldLabel>
                  <Input id="det-summary" value={form.summary} onChange={(e) => setForm({ ...form, summary: e.target.value })} placeholder="这一手有没有被哪一侧的什么发现" />
                </Field>
                <Field>
                  <FieldLabel htmlFor="det-evidence">证据（必填）</FieldLabel>
                  <Textarea id="det-evidence" value={form.evidence} onChange={(e) => setForm({ ...form, evidence: e.target.value })} placeholder="查询命令 + 关键输出；「该时段无记录」本身也是证据" />
                </Field>
                <div className="grid gap-3 md:grid-cols-3">
                  <Field><FieldLabel htmlFor="det-log">日志源</FieldLabel><Input id="det-log" value={form.log_source} onChange={(e) => setForm({ ...form, log_source: e.target.value })} placeholder="Sysmon EventID 10" /></Field>
                  <Field><FieldLabel htmlFor="det-window">时间窗</FieldLabel><Input id="det-window" value={form.time_window} onChange={(e) => setForm({ ...form, time_window: e.target.value })} placeholder="2026-11-18T10:00Z/PT15M" /></Field>
                  <Field><FieldLabel htmlFor="det-detector">检测器</FieldLabel><Input id="det-detector" value={form.detector} onChange={(e) => setForm({ ...form, detector: e.target.value })} placeholder="EDR / IDS / SIEM" /></Field>
                </div>
                <Field>
                  <FieldLabel htmlFor="det-detail">细节</FieldLabel>
                  <Textarea id="det-detail" value={form.detail} onChange={(e) => setForm({ ...form, detail: e.target.value })} placeholder="查了哪个源、时间窗怎么对、看到了什么" />
                </Field>
                <div>
                  <Button onClick={() => void submitDetection()} disabled={saving}>
                    {saving ? "保存中…" : "记录检测结果"}
                  </Button>
                </div>
              </FieldGroup>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}
