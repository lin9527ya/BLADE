"use client";
"use no memo";

import * as React from "react";

import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  type NodeProps,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  type Edge as RFEdge,
  type Node as RFNode,
  useEdgesState,
  useNodesState,
} from "@xyflow/react";

import { collapseDigestGraph, nodeSummary, prioTone, typeMeta, viewKind } from "./_parts/graph-helpers";
import { NodeDetailDialog } from "./_parts/node-detail-dialog";
import "@xyflow/react/dist/style.css";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { toneClasses } from "@/lib/status";
import type { Edge, ExploreKind, TaskNode } from "@/lib/types";
import { cn } from "@/lib/utils";

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
function computeLayout(nodes: TaskNode[], edges: Edge[]): Map<string, { x: number; y: number }> {
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
            <span
              className={cn(
                "shrink-0 rounded-md px-1.5 py-0.5 text-[10px] font-semibold tabular-nums",
                toneClasses[prioTone(n.priority)],
              )}
            >
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
            <StatusBadge
              domain={n.type === "goal" ? "goal" : "intent"}
              value={n.state}
              dot
              className="px-1.5 py-0 text-[10px]"
            />
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
        <Background
          variant={BackgroundVariant.Dots}
          gap={16}
          size={1}
          className="text-neutral-400/50 dark:text-neutral-700/60"
        />
        <Controls
          showInteractive={false}
          className="!rounded-lg !border !shadow-sm [&>button]:!border-border [&>button]:!bg-card [&>button:hover]:!bg-accent [&_svg]:!fill-foreground"
        />
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
