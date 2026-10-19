"use client";

import * as React from "react";

import { type Tone } from "@/lib/status";
import { Edge, ExploreKind, TaskNode } from "@/lib/types";
import {
  Anchor,
  Bug,
  Compass,
  Flag,
  FlaskConical,
  Key,
  Layers,
  Lightbulb,
  Network,
  PackageOpen,
  ShieldCheck,
  Target,
  type LucideIcon,
} from "lucide-react";

// 探索图的数据整形：类型元信息、摘要文本、digest 折叠与视图类型判定。
// 图本体与节点详情弹窗共用。

export type TypeMeta = {
  label: string;
  icon: LucideIcon;
  iconBg: string;
  chip: string;
  hex: string;
};

export const typeMeta: Record<ExploreKind, TypeMeta> = {
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
export function viewKind(n: TaskNode): ExploreKind {
  return n.type === "fact" && n.state === "origin" ? "begin" : n.type;
}

export function prioTone(p: number): Tone {
  return p >= 8 ? "red" : p >= 5 ? "amber" : "slate";
}

export const summaryFields: Record<ExploreKind, string[]> = {
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

export function nodeSummary(n: TaskNode): string {
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
export function digestMemberIds(n: TaskNode): string[] {
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
export function collapseDigestGraph(nodes: TaskNode[], edges: Edge[]): { viewNodes: TaskNode[]; viewEdges: Edge[] } {
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
