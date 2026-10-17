"use client";
"use no memo";

import * as React from "react";
import {
  Background,
  BackgroundVariant,
  Controls,
  type Edge as RFEdge,
  Handle,
  MarkerType,
  MiniMap,
  type Node as RFNode,
  type NodeProps,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useEdgesState,
  useNodesState,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  Anchor,
  Bug,
  Compass,
  Flag,
  FlaskConical,
  Key,
  Layers,
  Lightbulb,
  type LucideIcon,
  Network,
  PackageOpen,
  ShieldCheck,
  Target,
} from "lucide-react";

import { toast } from "sonner";

import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { StatusBadge } from "@/components/status-badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { toneClasses, type Tone } from "@/lib/status";
import type { Edge, ExploreKind, TaskNode } from "@/lib/types";

export const relLabel: Record<string, string> = {
  spawns: "派生",
  derived_from: "意图链",
  yields: "产出",
  proves: "证明",
  uses: "动用",
  // AD 域内拓扑（P32）：四条边各有各的走法，报告与页面必须是同一套词（report.ADRelLabel）。
  member_of: "成员",
  admin_to: "本机管理员",
  session_on: "有会话",
  can: "权限",
};

export const relColor: Record<string, string> = {
  spawns: "#10b981",
  derived_from: "#6366f1",
  yields: "#f59e0b",
  proves: "#ef4444",
  uses: "#f97316", // 与据点同色：这条边指向的是战果
  // AD 四边同色系（紫）：它们是一套东西——域内拓扑，与作业边（上面那些）区分开。
  member_of: "#8b5cf6",
  admin_to: "#a855f7",
  session_on: "#c084fc",
  can: "#7c3aed",
};

type TypeMeta = {
  label: string;
  icon: LucideIcon;
  iconBg: string;
  chip: string;
  hex: string;
};

const typeMeta: Record<ExploreKind, TypeMeta> = {
  task: { label: "根任务", icon: Flag, iconBg: "bg-slate-500", chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300", hex: "#64748b" },
  begin: { label: "起点", icon: Flag, iconBg: "bg-slate-500", chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300", hex: "#64748b" },
  goal: { label: "目标", icon: Target, iconBg: "bg-emerald-500", chip: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400", hex: "#10b981" },
  hint: { label: "提示", icon: Lightbulb, iconBg: "bg-violet-500", chip: "bg-violet-500/15 text-violet-600 dark:text-violet-400", hex: "#8b5cf6" },
  intent: { label: "意图", icon: Compass, iconBg: "bg-blue-500", chip: "bg-blue-500/15 text-blue-600 dark:text-blue-400", hex: "#3b82f6" },
  fact: { label: "事实", icon: FlaskConical, iconBg: "bg-amber-500", chip: "bg-amber-500/15 text-amber-600 dark:text-amber-400", hex: "#f59e0b" },
  finding: { label: "漏洞", icon: Bug, iconBg: "bg-rose-500", chip: "bg-rose-500/15 text-rose-600 dark:text-rose-400", hex: "#f43f5e" },
  digest: { label: "压缩", icon: Layers, iconBg: "bg-teal-500", chip: "bg-teal-500/15 text-teal-600 dark:text-teal-400", hex: "#14b8a6" },
  // 战果：利用成功之后的产物。单独配色是必须的——它们此前会落进兜底显示成「事实」，
  // 人就看不出这个任务已经打通到哪一步了。
  foothold: { label: "据点", icon: Anchor, iconBg: "bg-orange-500", chip: "bg-orange-500/15 text-orange-600 dark:text-orange-400", hex: "#f97316" },
  credential: { label: "凭据", icon: Key, iconBg: "bg-cyan-500", chip: "bg-cyan-500/15 text-cyan-600 dark:text-cyan-400", hex: "#06b6d4" },
  detection: { label: "检测验证", icon: ShieldCheck, iconBg: "bg-sky-500", chip: "bg-sky-500/15 text-sky-600 dark:text-sky-400", hex: "#0ea5e9" },
  // 遗留物：我们留在目标上的东西。它跟其它战果是反的——那些是"拿到了什么"，这个是"留下了什么"。
  artifact: { label: "遗留物", icon: PackageOpen, iconBg: "bg-violet-500", chip: "bg-violet-500/15 text-violet-600 dark:text-violet-400", hex: "#8b5cf6" },
  // AD 对象：域内采集来的对象（用户/组/机器/域…），具体是哪一类写在 payload.category 上。
  ad_object: { label: "AD 对象", icon: Network, iconBg: "bg-purple-500", chip: "bg-purple-500/15 text-purple-600 dark:text-purple-400", hex: "#a855f7" },
};

// The task root is an origin fact (kind='fact', state='origin'); render it as "起点".
function viewKind(n: TaskNode): ExploreKind {
  return n.type === "fact" && n.state === "origin" ? "begin" : n.type;
}

function prioTone(p: number): Tone {
  return p >= 8 ? "red" : p >= 5 ? "amber" : "slate";
}

const summaryFields: Record<ExploreKind, string[]> = {
  task: ["summary", "description"],
  begin: ["summary", "description"],
  goal: ["text"],
  hint: [],
  intent: ["summary"],
  fact: ["summary"],
  finding: ["name", "summary"],
  digest: ["body"],
  // 战果的摘要都在 summary 里（access/privilege/kind/username 是补充元信息，不进卡片摘要）。
  foothold: ["summary"],
  credential: ["summary"],
  detection: ["summary"],
  artifact: ["summary"],
  // AD 对象没有 summary：它的"名字"就是它的身份（JDOE@CORP.LOCAL / DC.CORP.LOCAL）。
  ad_object: ["name", "sam"],
};

function nodeSummary(n: TaskNode): string {
  const raw = n.payload ?? "";
  const fields = summaryFields[viewKind(n)] ?? [];
  if (fields.length && raw.trim()) {
    try {
      const obj: unknown = JSON.parse(raw);
      if (obj && typeof obj === "object") {
        for (const f of fields) {
          const v = (obj as Record<string, unknown>)[f];
          if (typeof v === "string" && v.trim()) return v;
        }
      }
    } catch {
      /* 非 JSON：原样显示 */
    }
  }
  return raw;
}

// digestMemberIds reads the fact/intent ids a digest folds from its payload
// (member_ids.{facts,intents}); ids are numbers in JSON, node ids are strings.
function digestMemberIds(n: TaskNode): string[] {
  if (n.type !== "digest" || !n.payload) return [];
  try {
    const p = JSON.parse(n.payload) as {
      member_ids?: { facts?: Array<string | number>; intents?: Array<string | number> };
    };
    return [...(p.member_ids?.facts ?? []), ...(p.member_ids?.intents ?? [])].map(String);
  } catch {
    return [];
  }
}

// collapseDigestGraph contracts each active digest's covered members into the
// digest super-node: the members are dropped from the rendered set and every
// edge touching a member is rewired onto its digest (member→member internal
// edges collapse to a self-loop and are dropped; the resulting edges are
// deduped by src+rel+dst). covers edges are structural and never drawn.
// Superseded digests (no covers edges) are dropped entirely. A graph with no
// active digest passes through unchanged. The full node list is kept by the
// caller so the detail drawer can still resolve a folded member by id.
function collapseDigestGraph(nodes: TaskNode[], edges: Edge[]): { viewNodes: TaskNode[]; viewEdges: Edge[] } {
  const activeDigests = new Set(
    nodes.filter((n) => n.type === "digest" && n.state === "active").map((n) => n.id),
  );
  const memberToDigest = new Map<string, string>();
  for (const e of edges) {
    if (e.rel === "covers" && activeDigests.has(e.src)) memberToDigest.set(e.dst, e.src);
  }
  const viewNodes = nodes.filter((n) =>
    n.type === "digest" ? n.state === "active" : !memberToDigest.has(n.id),
  );
  const remap = (id: string) => memberToDigest.get(id) ?? id;
  const seen = new Set<string>();
  const rewired: Edge[] = [];
  // src+dst of every rewired yields edge, so the inverse derived_from can be dropped.
  const produced = new Set<string>();
  for (const e of edges) {
    if (e.rel === "covers") continue;
    const src = remap(e.src);
    const dst = remap(e.dst);
    if (src === dst) continue;
    const key = `${src}\x00${e.rel}\x00${dst}`;
    if (seen.has(key)) continue;
    seen.add(key);
    if (e.rel === "yields") produced.add(`${src} ${dst}`);
    rewired.push(src === e.src && dst === e.dst ? e : { ...e, src, dst });
  }
  // yields(A→B) and derived_from(B→A) are inverse relations: a finding is
  // yielded by one intent but derived_from other intents, so the raw graph has
  // no 2-cycle. Folding those distinct intents into one digest collapses both
  // onto the digest↔member pair, leaving a redundant reverse edge the layered
  // layout draws as an overlapping back-edge. Drop each derived_from whose
  // reverse yields exists, keeping the production direction (→member).
  const viewEdges = rewired.filter(
    (e) => !(e.rel === "derived_from" && produced.has(`${e.dst} ${e.src}`)),
  );
  return { viewNodes, viewEdges };
}

const COL_W = 360;
const ROW_H = 140;

type ExploreNodeData = { node: TaskNode };
type ExploreRFNode = RFNode<ExploreNodeData, "explore">;

function pushMap(m: Map<string, string[]>, k: string, v: string) {
  const a = m.get(k);
  if (a) a.push(v);
  else m.set(k, [v]);
}

// computeLayout auto-layers nodes by longest path along edges (cycle-safe DFS
// break + barycenter ordering), so origin → … → finding reads left-to-right.
function computeLayout(
  nodes: TaskNode[],
  edges: Edge[],
): Map<string, { x: number; y: number }> {
  const order = nodes.map((n) => n.id);
  const idset = new Set(order);
  const valid = edges.filter((e) => idset.has(e.src) && idset.has(e.dst));
  const key = (s: string, d: string) => `${s}->${d}`;

  const out = new Map<string, string[]>();
  for (const e of valid) pushMap(out, e.src, e.dst);

  const seen = new Map<string, 0 | 1 | 2>();
  const back = new Set<string>();
  for (const root of order) {
    if ((seen.get(root) ?? 0) !== 0) continue;
    const stack: Array<{ u: string; i: number }> = [{ u: root, i: 0 }];
    seen.set(root, 1);
    while (stack.length) {
      const top = stack[stack.length - 1];
      const nbrs = out.get(top.u) ?? [];
      if (top.i >= nbrs.length) {
        seen.set(top.u, 2);
        stack.pop();
        continue;
      }
      const v = nbrs[top.i++];
      const s = seen.get(v) ?? 0;
      if (s === 1) back.add(key(top.u, v));
      else if (s === 0) {
        seen.set(v, 1);
        stack.push({ u: v, i: 0 });
      }
    }
  }

  const dagSucc = new Map<string, string[]>();
  const indeg = new Map<string, number>(order.map((id) => [id, 0]));
  for (const e of valid) {
    if (back.has(key(e.src, e.dst))) continue;
    pushMap(dagSucc, e.src, e.dst);
    indeg.set(e.dst, (indeg.get(e.dst) ?? 0) + 1);
  }
  const layer = new Map<string, number>(order.map((id) => [id, 0]));
  const queue = order.filter((id) => (indeg.get(id) ?? 0) === 0);
  for (let qi = 0; qi < queue.length; qi++) {
    const u = queue[qi];
    for (const v of dagSucc.get(u) ?? []) {
      layer.set(v, Math.max(layer.get(v) ?? 0, (layer.get(u) ?? 0) + 1));
      const d = (indeg.get(v) ?? 0) - 1;
      indeg.set(v, d);
      if (d === 0) queue.push(v);
    }
  }

  const maxLayer = Math.max(0, ...layer.values());
  const columns: string[][] = Array.from({ length: maxLayer + 1 }, () => []);
  for (const id of order) columns[layer.get(id) ?? 0].push(id);

  const colIndexOf = new Map<string, number>();
  columns.forEach((ids, c) => ids.forEach((id) => colIndexOf.set(id, c)));
  const neighbors = new Map<string, string[]>();
  for (const e of valid) {
    const cs = colIndexOf.get(e.src);
    const cd = colIndexOf.get(e.dst);
    if (cs === undefined || cd === undefined || Math.abs(cs - cd) !== 1) continue;
    pushMap(neighbors, e.src, e.dst);
    pushMap(neighbors, e.dst, e.src);
  }
  const sweep = (dir: 1 | -1) => {
    const start = dir === 1 ? 1 : columns.length - 2;
    const end = dir === 1 ? columns.length : -1;
    for (let c = start; c !== end; c += dir) {
      const ref = c - dir;
      const refIndex = new Map(columns[ref].map((id, i) => [id, i]));
      const scored = columns[c].map((id, i) => {
        const ns = (neighbors.get(id) ?? [])
          .map((nid) => refIndex.get(nid))
          .filter((v): v is number => v !== undefined);
        const bary = ns.length ? ns.reduce((a, b) => a + b, 0) / ns.length : i;
        return { id, bary, i };
      });
      scored.sort((a, b) => a.bary - b.bary || a.i - b.i);
      columns[c] = scored.map((s) => s.id);
    }
  };
  for (let iter = 0; iter < 4; iter++) {
    sweep(1);
    sweep(-1);
  }

  const maxCount = Math.max(1, ...columns.map((c) => c.length));
  const midline = ((maxCount - 1) * ROW_H) / 2;
  const pos = new Map<string, { x: number; y: number }>();
  columns.forEach((ids, c) => {
    const startY = midline - ((ids.length - 1) * ROW_H) / 2;
    ids.forEach((id, i) => pos.set(id, { x: c * COL_W, y: startY + i * ROW_H }));
  });
  return pos;
}

// Dify-style node card.
function ExploreNode({ data, selected }: NodeProps<ExploreRFNode>) {
  const n = data.node;
  const kind = viewKind(n);
  const meta = typeMeta[kind] ?? typeMeta.intent;
  const Icon = meta.icon;
  const showState = n.type === "goal" || n.type === "intent";
  const showPriority = (n.type === "goal" || n.type === "intent") && n.priority > 0;
  const isLive = !n.inherited && n.type === "intent" && n.state === "running";

  return (
    <div
      className={cn(
        "w-[240px] cursor-pointer rounded-2xl border-2 transition-colors",
        selected ? "border-blue-500" : isLive ? "border-blue-400/70" : "border-transparent",
      )}
    >
      <div className="group relative rounded-[14px] border border-black/[0.06] bg-white shadow-sm transition-shadow hover:shadow-lg dark:border-white/10 dark:bg-neutral-900">
        <Handle
          type="target"
          position={Position.Left}
          className="!size-2 !border-2 !border-neutral-300 !bg-white opacity-0 transition-opacity group-hover:opacity-100 dark:!border-neutral-600 dark:!bg-neutral-800"
        />
        <div className="flex items-center gap-2 px-3 pt-3 pb-1.5">
          <span className={cn("flex size-6 shrink-0 items-center justify-center rounded-lg shadow-sm", meta.iconBg)}>
            <Icon className="size-3.5 text-white" />
          </span>
          <span className="grow truncate text-[13px] font-semibold text-neutral-700 dark:text-neutral-100">
            {meta.label}
          </span>
          {isLive && (
            <span className="relative flex size-2 shrink-0">
              <span className="absolute inline-flex size-full animate-ping rounded-full bg-blue-400 opacity-70" />
              <span className="relative inline-flex size-2 rounded-full bg-blue-500" />
            </span>
          )}
          {showPriority && (
            <span className={cn("shrink-0 rounded-md px-1.5 py-0.5 text-[10px] font-semibold tabular-nums", toneClasses[prioTone(n.priority)])}>
              P{n.priority}
            </span>
          )}
        </div>
        <div className="line-clamp-3 px-3 pb-2 text-[13px] leading-snug text-neutral-500 dark:text-neutral-400">
          {nodeSummary(n) || meta.label}
        </div>
        <div className="flex items-center justify-between gap-2 px-3 pb-2.5">
          {n.inherited && n.source_task_id ? (
            <Badge variant="outline" className="max-w-32 truncate" title={`继承自任务 #${n.source_task_id}，只读`}>
              来源 #{n.source_task_id}
            </Badge>
          ) : showState ? (
            <StatusBadge domain={n.type === "goal" ? "goal" : "intent"} value={n.state} dot className="px-1.5 py-0 text-[10px]" />
          ) : (
            <span className="text-[10px] text-neutral-400 dark:text-neutral-500">{meta.label}</span>
          )}
          <span className="shrink-0 font-mono text-[10px] text-neutral-400 dark:text-neutral-500">#{n.id}</span>
        </div>
        <Handle
          type="source"
          position={Position.Right}
          className="!size-2 !border-2 !border-neutral-300 !bg-white opacity-0 transition-opacity group-hover:opacity-100 dark:!border-neutral-600 dark:!bg-neutral-800"
        />
      </div>
    </div>
  );
}

const nodeTypes = { explore: ExploreNode };

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 py-1.5 text-sm">
      <span className="text-muted-foreground w-14 shrink-0">{label}</span>
      <span className="text-foreground min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

// ── 美化数据：把节点 payload 的 JSON 渲染成人类可读的字段视图 ────────────────
// 原始 JSON 里 payload 还是被二次转义的字符串，长证据/摘要混在引号转义里很难读；
// 这里按字段语义直接给出内容：严重度/置信度用徽标、id 数组用 chips、长文本用段落、
// PoC/证据用等宽块、嵌套对象（evidence / closure / member_ids）递归展开。
const payloadFieldLabels: Record<string, string> = {
  summary: "摘要", detail: "详情", evidence: "证据", poc: "PoC", text: "目标文本",
  goal: "目标", description: "描述", name: "名称", vulnclass: "漏洞类别",
  severity: "严重度", confidence: "置信度", risk_area: "风险类", intent_id: "意图",
  asset_ids: "关联资产", by: "记录者", reason: "原因", note: "备注",
  closure: "闭合信息", outcome: "闭合结论", counterevidence: "反证", proof_gap: "待补证缺口",
  member_ids: "折叠成员", member_count: "成员数", facts: "事实", intents: "意图",
  source_intent: "来源意图", kind: "种类", value: "值", url: "URL", host: "主机",
  // 战果与凭据（P27/P36）
  access: "通路", privilege: "权限", username: "账号", secret: "凭据本体",
  targets: "可登目标", verified: "已验证", service: "服务", port: "端口",
};

// 字段展示顺序：语义在前（摘要/目标/结论/证据…），其余按原 JSON 顺序落尾。
const payloadFieldOrder = [
  "summary", "text", "goal", "name", "vulnclass", "severity", "confidence", "risk_area",
  "detail", "evidence", "poc", "closure", "outcome", "counterevidence", "proof_gap",
  "intent_id", "source_intent", "asset_ids", "member_ids", "member_count", "by", "reason", "note",
];

const confidenceLabel: Record<string, string> = { high: "高（可复现触发链）", medium: "中（强异常，未完整对照）", low: "低（据指纹/版本推断）" };

function PayloadValue({ k, v }: { k: string; v: unknown }) {
  if (v === null || v === undefined || v === "") {
    return <span className="text-muted-foreground text-xs">—</span>;
  }
  if (k === "severity" && typeof v === "string") {
    return <StatusBadge domain="severity" value={v} dot className="px-1.5 py-0 text-[10px]" />;
  }
  if (k === "confidence" && typeof v === "string") {
    return <span className="text-xs">{confidenceLabel[v] ?? v}</span>;
  }
  if (k === "risk_area" && typeof v === "string") {
    return <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">{v}</code>;
  }
  // 凭据的可登目标（P36）：逐条列 主机:端口（服务），并**分开标已验证/待验证**——
  // 列在一起会让人以为"列出来的都能登"。
  if (k === "targets" && Array.isArray(v)) {
    const rows = v.filter((x): x is Record<string, unknown> => !!x && typeof x === "object");
    if (rows.length === 0) return <span className="text-muted-foreground text-xs">—</span>;
    return (
      <span className="flex flex-col gap-1">
        {rows.map((t, i) => {
          const host = typeof t.host === "string" ? t.host : "";
          const port = typeof t.port === "number" && t.port > 0 ? `:${t.port}` : "";
          const svc = typeof t.service === "string" && t.service ? `（${t.service}）` : "";
          const verified = t.verified === true;
          return (
            <span key={`${host}${port}${i}`} className="flex flex-wrap items-center gap-2 text-xs">
              <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-[10px]">{`${host}${port}${svc}`}</code>
              <span className={verified ? "text-emerald-600 dark:text-emerald-400" : "text-muted-foreground"}>
                {verified ? "已验证可登" : "待验证"}
              </span>
              {typeof t.evidence === "string" && t.evidence !== "" && (
                <span className="text-muted-foreground truncate">{t.evidence}</span>
              )}
            </span>
          );
        })}
      </span>
    );
  }
  if (Array.isArray(v)) {
    if (v.length === 0) return <span className="text-muted-foreground text-xs">—</span>;
    if (v.every((x) => typeof x === "number")) {
      return (
        <span className="flex flex-wrap gap-1">
          {v.map((x) => (
            <code key={String(x)} className="bg-muted rounded px-1.5 py-0.5 font-mono text-[10px]">
              #{String(x)}
            </code>
          ))}
        </span>
      );
    }
    return (
      <span className="text-xs break-words whitespace-pre-wrap">
        {v.map((x) => (typeof x === "string" ? x : JSON.stringify(x))).join("、")}
      </span>
    );
  }
  if (typeof v === "object") {
    return (
      <div className="flex flex-col gap-1.5 border-l pl-2.5">
        {Object.entries(v as Record<string, unknown>).map(([kk, vv]) => (
          <div key={kk} className="flex items-start gap-2">
            <span className="text-muted-foreground w-16 shrink-0 pt-0.5 text-[11px]">
              {payloadFieldLabels[kk] ?? kk}
            </span>
            <span className="min-w-0 flex-1">
              <PayloadValue k={kk} v={vv} />
            </span>
          </div>
        ))}
      </div>
    );
  }
  const text = String(v);
  if (k === "poc" || k === "evidence" || text.includes("\n") || text.length > 120) {
    return (
      <pre className="bg-muted/40 max-h-72 overflow-auto rounded-md border p-2 font-mono text-[11px] leading-relaxed break-all whitespace-pre-wrap">
        {text}
      </pre>
    );
  }
  return <span className="text-xs break-words whitespace-pre-wrap">{text}</span>;
}

// withRevealedSecret 把"这次点开看到的明文"注进待展示的节点（P36）。
//
// 服务端下发的 payload 里**本来就没有** secret（见 server/dto.go 的 redactCredentialSecret），
// 只有点过「显示明文」之后才由受控入口取回、并只对当前这条节点生效——所以换节点自动回到脱敏，
// 不需要额外的清理逻辑。明文入口那一次服务端会写留痕（谁、什么时候看了哪条凭据的明文）。
function withRevealedSecret(n: TaskNode | null, revealed: string | null): TaskNode | null {
  if (!n || n.type !== "credential" || revealed === null) return n;
  let data: Record<string, unknown> | null = null;
  try {
    const parsed: unknown = JSON.parse(n.payload ?? "");
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      data = parsed as Record<string, unknown>;
    }
  } catch {
    return n; // 非 JSON payload：原样展示（没有可注入的结构）
  }
  if (!data) return n;
  delete data.secret_redacted;
  return { ...n, payload: JSON.stringify({ ...data, secret: revealed }) };
}

function PayloadBeautified({ node }: { node: TaskNode }) {
  let data: Record<string, unknown> | null = null;
  try {
    const parsed: unknown = JSON.parse(node.payload ?? "");
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      data = parsed as Record<string, unknown>;
    }
  } catch {
    /* 非 JSON payload：原样展示 */
  }
  if (!data) {
    return (
      <p className="text-muted-foreground text-xs break-words whitespace-pre-wrap">
        {node.payload?.trim() || "（无 payload）"}
      </p>
    );
  }
  // secret_redacted 是服务端的脱敏提示（不是数据）：属性区已有「明文」一行带查看入口，
  // 在这里再渲染一个裸布尔值只会让人以为它是业务字段。
  const keys = Object.keys(data).filter((k) => k !== "secret_redacted");
  const ordered = [
    ...payloadFieldOrder.filter((k) => keys.includes(k)),
    ...keys.filter((k) => !payloadFieldOrder.includes(k)),
  ];
  return (
    <div className="flex flex-col gap-2.5">
      {ordered.map((k) => (
        <div key={k} className="flex flex-col gap-1">
          <span className="text-muted-foreground text-[11px] font-medium">{payloadFieldLabels[k] ?? k}</span>
          <PayloadValue k={k} v={data[k]} />
        </div>
      ))}
    </div>
  );
}

// 节点详情。原先是右侧抽屉，改成居中弹窗——全站统一：从右侧滑出的界面只剩移动端侧栏
// 那一个（用户明确要求过），详情面板留着抽屉会显得像另一个应用。
function NodeDetailDialog({
  node,
  nodesById,
  onSelect,
  onOpenChange,
  taskId,
}: {
  node: TaskNode | null;
  nodesById: Map<string, TaskNode>;
  onSelect: (n: TaskNode) => void;
  onOpenChange: (open: boolean) => void;
  taskId?: string;
}) {
  const [copied, setCopied] = React.useState(false);
  // 明文只对"当前这条、且这次明确点过"的凭据放行：换节点自动回到脱敏（与下面的
  // rawNodeId 同一个套路——用派生状态而不是 effect）。
  const [reveal, setReveal] = React.useState<{ id: string; secret: string } | null>(null);
  const [revealing, setRevealing] = React.useState(false);
  const revealedSecret = node && reveal && reveal.id === node.id ? reveal.secret : null;
  const displayNode = React.useMemo(() => withRevealedSecret(node, revealedSecret), [node, revealedSecret]);
  const doReveal = () => {
    if (!node || !taskId) return;
    setRevealing(true);
    api
      .revealCredential(node.id, taskId)
      .then((r) => {
        if (!r.secret) {
          toast.message("这条凭据没有登记本体，没有可显示的明文");
          return;
        }
        setReveal({ id: node.id, secret: r.secret });
        toast.success("已显示明文；本次查看已记入任务时间线");
      })
      .catch((e: unknown) => toast.error("读取明文失败：" + String((e as Error)?.message ?? e)))
      .finally(() => setRevealing(false));
  };
  // 数据展示默认「美化」，手动切到「原始」。用「记住哪个节点切到了原始」这个派生
  // 状态实现：换节点自动回到美化，也省掉一个 effect。
  const [rawNodeId, setRawNodeId] = React.useState<string | null>(null);
  const showRaw = !!node && rawNodeId === node.id;
  const meta = node ? (typeMeta[viewKind(node)] ?? typeMeta.intent) : null;
  const Icon = meta?.icon;
  const raw = displayNode ? JSON.stringify(displayNode, null, 2) : "";
  const memberIds = node ? digestMemberIds(node) : [];
  const members = memberIds.map((id) => nodesById.get(id)).filter((m): m is TaskNode => !!m);

  const copy = () => {
    void navigator.clipboard?.writeText(raw).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <Dialog open={node !== null} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[88vh] flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl">
        {node && meta && Icon && (
          <>
            <DialogHeader className="border-b p-4">
              <div className="flex items-center gap-2.5 pr-8">
                <span className={cn("flex size-8 shrink-0 items-center justify-center rounded-lg shadow-sm", meta.iconBg)}>
                  <Icon className="size-4 text-white" />
                </span>
                <div className="min-w-0">
                  <DialogTitle className="leading-tight">{meta.label}</DialogTitle>
                  <span className="text-muted-foreground font-mono text-xs">{node.id}</span>
                </div>
                {node.inherited && node.source_task_id && (
                  <Badge variant="outline" className="ml-auto shrink-0">
                    来源任务 #{node.source_task_id} · 只读
                  </Badge>
                )}
              </div>
            </DialogHeader>

            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-4 p-4">
                <section>
                  <h4 className="text-muted-foreground mb-1 text-xs font-medium">属性</h4>
                  <DetailRow label="类型">{meta.label}</DetailRow>
                  <DetailRow label="状态">
                    {node.type === "goal" || node.type === "intent" ? (
                      <StatusBadge domain={node.type === "goal" ? "goal" : "intent"} value={node.state} dot className="px-1.5 py-0 text-[10px]" />
                    ) : (
                      <span className="font-mono text-xs">{node.state}</span>
                    )}
                  </DetailRow>
                  <DetailRow label="优先级">
                    <span className="tabular-nums">{node.priority}</span>
                  </DetailRow>
                  <DetailRow label="来源">
                    <span className="font-mono text-xs">{node.origin}</span>
                  </DetailRow>
                  {node.inherited && node.source_task_id && (
                    <DetailRow label="来源任务">
                      <span className="font-mono text-xs">#{node.source_task_id}（继承，只读）</span>
                    </DetailRow>
                  )}
                  <DetailRow label="时间">{new Date(node.ts).toLocaleString("zh-CN")}</DetailRow>
                  {node.type === "credential" && (
                    <>
                      <DetailRow label="指纹">
                        <span className="font-mono text-xs">{node.fingerprint || "（无本体，无指纹）"}</span>
                      </DetailRow>
                      <DetailRow label="明文">
                        {revealedSecret !== null ? (
                          <button
                            type="button"
                            onClick={() => setReveal(null)}
                            className="text-muted-foreground hover:text-foreground text-xs underline underline-offset-2"
                          >
                            已显示 · 收起
                          </button>
                        ) : taskId ? (
                          <button
                            type="button"
                            onClick={doReveal}
                            disabled={revealing}
                            className="text-xs underline underline-offset-2"
                          >
                            {revealing ? "读取中…" : "显示明文（会留痕）"}
                          </button>
                        ) : (
                          <span className="text-muted-foreground text-xs">在任务页的链路图里可查看（会留痕）</span>
                        )}
                      </DetailRow>
                    </>
                  )}
                </section>

                {node.type === "digest" && (
                  <>
                    {nodeSummary(node) && (
                      <section className="border-t pt-3">
                        <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">压缩结论</h4>
                        <p className="text-foreground text-sm leading-relaxed break-words whitespace-pre-wrap">
                          {nodeSummary(node)}
                        </p>
                      </section>
                    )}
                    <section className="border-t pt-3">
                      <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">
                        压缩的节点（{memberIds.length}）
                      </h4>
                      <div className="flex flex-col gap-1.5">
                        {members.map((m) => {
                          const mMeta = typeMeta[viewKind(m)] ?? typeMeta.intent;
                          const MIcon = mMeta.icon;
                          return (
                            <button
                              key={m.id}
                              type="button"
                              onClick={() => onSelect(m)}
                              className="hover:bg-accent flex items-start gap-2 rounded-md border p-2 text-left transition-colors"
                            >
                              <span className={cn("mt-0.5 flex size-5 shrink-0 items-center justify-center rounded", mMeta.iconBg)}>
                                <MIcon className="size-3 text-white" />
                              </span>
                              <span className="min-w-0 flex-1">
                                <span className="text-foreground line-clamp-2 text-xs leading-snug">
                                  {nodeSummary(m) || mMeta.label}
                                </span>
                                <span className="text-muted-foreground block font-mono text-[10px]">
                                  {mMeta.label} · #{m.id}
                                </span>
                              </span>
                            </button>
                          );
                        })}
                        {members.length < memberIds.length && (
                          <span className="text-muted-foreground text-[10px]">
                            另有 {memberIds.length - members.length} 个成员节点未随当前图加载
                          </span>
                        )}
                      </div>
                    </section>
                  </>
                )}

                <section className="border-t pt-3">
                  <div className="mb-2 flex items-center justify-between gap-2">
                    <div className="bg-muted inline-flex rounded-md p-0.5">
                      <button
                        type="button"
                        onClick={() => setRawNodeId(null)}
                        className={cn(
                          "rounded px-2.5 py-1 text-xs transition-colors",
                          !showRaw ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        美化数据
                      </button>
                      <button
                        type="button"
                        onClick={() => setRawNodeId(node.id)}
                        className={cn(
                          "rounded px-2.5 py-1 text-xs transition-colors",
                          showRaw ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                        )}
                      >
                        原始数据
                      </button>
                    </div>
                    {showRaw && (
                      <button type="button" onClick={copy} className="text-muted-foreground hover:text-foreground text-xs transition-colors">
                        {copied ? "已复制" : "复制"}
                      </button>
                    )}
                  </div>
                  {showRaw ? (
                    <pre className="bg-muted/50 text-foreground max-h-[60vh] max-w-full overflow-auto rounded-md border p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
                      {raw}
                    </pre>
                  ) : (
                    <div className="max-h-[60vh] overflow-auto pr-1">
                      <PayloadBeautified node={displayNode ?? node} />
                    </div>
                  )}
                </section>
              </div>
            </ScrollArea>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ExplorationGraphInner renders the given nodes/edges as a React Flow canvas.
// Positions are auto-laid-out and preserved across prop updates (so a polling
// caller's graph doesn't jump); click a node to open its detail drawer.
function ExplorationGraphInner({
  nodes,
  edges,
  emptyHint = "暂无探索数据",
  taskId,
}: {
  nodes: TaskNode[];
  edges: Edge[];
  emptyHint?: string;
  taskId?: string;
}) {
  const [rfNodes, setRfNodes, onNodesChange] = useNodesState<ExploreRFNode>([]);
  const [rfEdges, setRfEdges, onEdgesChange] = useEdgesState<RFEdge>([]);
  const [selected, setSelected] = React.useState<TaskNode | null>(null);
  // 缩略图默认收起：它是绝对定位的浮层，贴住右下角就会在 pan 之后压住节点卡片（截图里就压着一张
  // 漏洞卡）。图小的时候它本来也没用——需要时一键展开，而不是让它常驻遮挡内容。
  const [showMiniMap, setShowMiniMap] = React.useState(false);
  // Full node index (incl. folded members) so the digest drawer can resolve
  // covered members even though they are not rendered on the canvas.
  const nodesById = React.useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  // Collapsed view: folded members removed, their edges rewired onto the digest.
  const { viewNodes, viewEdges } = React.useMemo(() => collapseDigestGraph(nodes, edges), [nodes, edges]);
  const isEmpty = viewNodes.length === 0;

  React.useEffect(() => {
    setSelected((cur) => (cur ? (nodesById.get(cur.id) ?? cur) : cur));
    const liveTargets = new Set(
      viewNodes.filter((n) => !n.inherited && n.type === "intent" && n.state === "running").map((n) => n.id),
    );
    const layout = computeLayout(viewNodes, viewEdges);
    setRfNodes((prev) => {
      const prevPos = new Map(prev.map((p) => [p.id, p.position]));
      return viewNodes.map((n) => ({
        id: n.id,
        type: "explore" as const,
        position: prevPos.get(n.id) ?? layout.get(n.id) ?? { x: 0, y: 0 },
        data: { node: n },
        sourcePosition: Position.Right,
        targetPosition: Position.Left,
      }));
    });
    setRfEdges(
      viewEdges.map((e, i) => {
        const color = relColor[e.rel] ?? "#94a3b8";
        const animated = liveTargets.has(e.dst);
        return {
          id: `e${i}-${e.src}-${e.dst}`,
          source: e.src,
          target: e.dst,
          label: relLabel[e.rel] ?? e.rel,
          type: "default",
          animated,
          labelShowBg: false,
          labelStyle: { fontSize: 10, fontWeight: 600, fill: color },
          style: { stroke: color, strokeWidth: 2, strokeOpacity: animated ? 0.9 : 0.55 },
          markerEnd: { type: MarkerType.ArrowClosed, color, width: 14, height: 14 },
        } satisfies RFEdge;
      }),
    );
  }, [viewNodes, viewEdges, nodesById, setRfNodes, setRfEdges]);

  return (
    <>
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={(_, node) => setSelected(node.data.node)}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        proOptions={{ hideAttribution: true }}
        onlyRenderVisibleElements
        minZoom={0.1}
        className="!bg-[#f0f2f7] dark:!bg-neutral-950"
      >
        <Background variant={BackgroundVariant.Dots} gap={16} size={1} className="text-neutral-400/50 dark:text-neutral-700/60" />
        <Controls showInteractive={false} className="!rounded-lg !border !shadow-sm [&>button]:!border-border [&>button]:!bg-card [&>button:hover]:!bg-accent [&_svg]:!fill-foreground" />
        {showMiniMap && (
          <MiniMap
            pannable
            zoomable
            className="!bg-card !rounded-lg !border !shadow-sm"
            maskColor="rgb(148 163 184 / 0.18)"
            nodeColor={(node) => {
              const data = node.data as ExploreNodeData | undefined;
              const k = data?.node ? viewKind(data.node) : "intent";
              return typeMeta[k]?.hex ?? "#94a3b8";
            }}
            nodeStrokeWidth={0}
            nodeBorderRadius={4}
          />
        )}
        <Panel position="top-left">
          <div className="bg-card/95 flex flex-col gap-2.5 rounded-lg border p-3 text-xs shadow-sm backdrop-blur">
            {isEmpty && <span className="text-muted-foreground">{emptyHint}</span>}
            <div className="flex flex-wrap gap-x-3 gap-y-1.5">
              {(["begin", "goal", "intent", "fact", "finding", "hint", "digest"] as ExploreKind[]).map((k) => {
                const m = typeMeta[k];
                const Icon = m.icon;
                return (
                  <span key={k} className="text-foreground inline-flex items-center gap-1.5">
                    <span className={cn("flex size-4 items-center justify-center rounded", m.iconBg)}>
                      <Icon className="size-2.5 text-white" />
                    </span>
                    {m.label}
                  </span>
                );
              })}
            </div>
            <div className="border-border/60 text-muted-foreground flex flex-wrap gap-x-3 gap-y-1.5 border-t pt-2">
              {Object.entries(relLabel).map(([k, v]) => (
                <span key={k} className="inline-flex items-center gap-1.5">
                  <span className="h-0.5 w-4 rounded-full" style={{ backgroundColor: relColor[k] }} />
                  {v}
                </span>
              ))}
            </div>
            <button
              type="button"
              onClick={() => setShowMiniMap((v) => !v)}
              className="text-muted-foreground hover:text-foreground self-start underline-offset-2 hover:underline"
            >
              {showMiniMap ? "隐藏缩略图" : "显示缩略图"}
            </button>
          </div>
        </Panel>
      </ReactFlow>
      <NodeDetailDialog
        node={selected}
        nodesById={nodesById}
        onSelect={setSelected}
        onOpenChange={(o) => !o && setSelected(null)}
        taskId={taskId}
      />
    </>
  );
}

// ExplorationGraph is the shared探索图/攻击链路图 canvas — used by the task graph
// tab (full graph, polled) and the finding lineage view (origin→finding subgraph).
export function ExplorationGraph({
  nodes,
  edges,
  className,
  emptyHint,
  taskId,
}: {
  nodes: TaskNode[];
  edges: Edge[];
  className?: string;
  emptyHint?: string;
  /** 传了才给凭据节点开「显示明文」——明文入口是任务内的动作（服务端按任务判定）。 */
  taskId?: string;
}) {
  return (
    <div className={cn("w-full overflow-hidden rounded-xl", className)}>
      <ReactFlowProvider>
        <ExplorationGraphInner nodes={nodes} edges={edges} emptyHint={emptyHint} taskId={taskId} />
      </ReactFlowProvider>
    </div>
  );
}
