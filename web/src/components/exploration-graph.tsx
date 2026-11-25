"use client";
"use no memo";

import * as React from "react";

import {
  Background,
  BackgroundVariant,
  BaseEdge,
  Controls,
  EdgeLabelRenderer,
  type EdgeProps,
  getBezierPath,
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
  useReactFlow,
} from "@xyflow/react";
import {
  ChevronDown,
  ChevronUp,
  Eye,
  Focus,
  GitBranch,
  Layers,
  Maximize2,
  Pause,
  Play,
  RotateCcw,
  Search,
  SkipBack,
  SkipForward,
  Square,
  X,
} from "lucide-react";

import { collapseDigestGraph, nodeSummary, prioTone, typeMeta, viewKind } from "./_parts/graph-helpers";
import { NodeDetailDialog } from "./_parts/node-detail-dialog";
import "@xyflow/react/dist/style.css";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  collectLineageSubgraph,
  computeExplorationLayout,
  edgeSemanticKey,
  extractNodeCardMeta,
  filterExplorationGraph,
  findMatchingNodeIds,
  INFRA_AUTO_HIDE_THRESHOLD,
  isInfraNode,
  LAYOUT_ROW_H,
} from "@/lib/exploration-layout";
import { buildReplayPlan, sliceGraphForReplay } from "@/lib/graph-replay";
import { toneClasses } from "@/lib/status";
import type { Edge, ExploreKind, TaskNode } from "@/lib/types";
import { cn } from "@/lib/utils";

export const relLabel: Record<string, string> = {
  spawns: "派生",
  derived_from: "意图链",
  yields: "产出",
  proves: "证明",
  uses: "动用",
  covers: "压缩",
  depends_on: "依赖",
  reaches: "可达",
  contradicts: "矛盾",
  supersedes: "替代",
  // AD 域内拓扑（P32）：四条边各有各的走法，报告与页面必须是同一套词（report.ADRelLabel）。
  member_of: "成员",
  admin_to: "本机管理员",
  session_on: "有会话",
  can: "权限",
  // 痕迹来源：故意与其它作业边都不同色——它指向的是「我们可能被记下什么」，
  // 与「我们拿到了什么」不是一回事。
  leaves: "留下痕迹",
};

export const relColor: Record<string, string> = {
  spawns: "#10b981",
  derived_from: "#6366f1",
  yields: "#f59e0b",
  proves: "#ef4444",
  uses: "#f97316", // 与据点同色：这条边指向的是战果
  covers: "#14b8a6",
  depends_on: "#64748b",
  reaches: "#06b6d4",
  contradicts: "#d97706", // 与 uses/foothold 的亮橙区分
  supersedes: "#94a3b8",
  // AD 四边同色系（紫）：它们是一套东西——域内拓扑，与作业边（上面那些）区分开。
  member_of: "#8b5cf6",
  admin_to: "#a855f7",
  session_on: "#c084fc",
  can: "#7c3aed",
  // 痕迹来源线用玫红 + 虚线：与实线的因果边区分，一眼看出"这条线连的是痕迹"。
  leaves: "#e11d48",
};

const DASHED_RELS = new Set(["leaves", "contradicts", "supersedes", "depends_on"]);
const REPLAY_SPEEDS = [1, 2, 4] as const;

type ExploreNodeData = {
  node: TaskNode;
  replayHighlight?: boolean;
  focused?: boolean;
  searchActive?: boolean;
  dimmed?: boolean;
};
type ExploreRFNode = RFNode<ExploreNodeData, "explore">;

type ExploreEdgeData = {
  labelText: string;
  color: string;
  dimmed?: boolean;
  emphasized?: boolean;
};
type ExploreRFEdge = RFEdge<ExploreEdgeData, "explore">;

function resolveCardBorderClass(opts: {
  selected?: boolean;
  focused?: boolean;
  searchActive?: boolean;
  replayHighlight?: boolean;
  isLive: boolean;
  isStale: boolean;
}): string {
  if (opts.selected || opts.focused) return "border-blue-500 ring-2 ring-blue-500/25";
  if (opts.searchActive) return "border-amber-500 ring-2 ring-amber-500/35";
  if (opts.replayHighlight) return "border-amber-400 ring-2 ring-amber-400/35";
  if (opts.isLive) return "border-blue-400/70";
  if (opts.isStale) return "border-dashed border-neutral-300 dark:border-neutral-700";
  return "border-transparent";
}

function resolveEdgeOpacity(animated: boolean, dashed: boolean, emphasized?: boolean, dimmed?: boolean): number {
  if (dimmed) return 0.14;
  if (emphasized) return 0.95;
  if (animated) return 0.9;
  if (dashed) return 0.78;
  return 0.6;
}

function resolveEdgeStrokeWidth(inLineage: boolean, dashed: boolean): number {
  if (inLineage) return 2.6;
  return dashed ? 1.8 : 2;
}

function NodeCardFooter({
  node,
  cardMeta,
  fallbackLabel,
}: {
  node: TaskNode;
  cardMeta: ReturnType<typeof extractNodeCardMeta>;
  fallbackLabel: string;
}) {
  if (node.inherited && node.source_task_id) {
    return (
      <Badge variant="outline" className="max-w-36 truncate" title={`继承自任务 #${node.source_task_id}，只读`}>
        来源 #{node.source_task_id}
      </Badge>
    );
  }
  if (cardMeta.statusDomain) {
    return (
      <>
        <StatusBadge
          domain={cardMeta.statusDomain}
          value={cardMeta.statusValue}
          dot
          className="shrink-0 px-1.5 py-0 text-[10px]"
        />
        {cardMeta.subTag && (
          <span className="truncate text-[10px] text-neutral-500 dark:text-neutral-400" title={cardMeta.subTag}>
            {cardMeta.subTag}
          </span>
        )}
      </>
    );
  }
  return (
    <span className="truncate font-mono text-[10px] text-neutral-500 dark:text-neutral-400" title={cardMeta.subTag}>
      {cardMeta.subTag ?? fallbackLabel}
    </span>
  );
}

// ExploreEdge：正向边沿标准贝塞尔曲线渲染；回指/对抗边（如 proves/supersedes/depends_on 等
// src 位于 dst 右侧时）向上平滑拱起越过卡片上方，避免直线横穿节点正文，并以主题自适应圆角胶囊显示边标签。
function ExploreEdge({
  id,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
  style,
  data,
}: EdgeProps<ExploreRFEdge>) {
  let edgePath = "";
  let labelX = 0;
  let labelY = 0;

  if (sourceX >= targetX - 40) {
    const dist = Math.max(80, Math.abs(sourceX - targetX));
    const lift = Math.min(180, 68 + dist * 0.12);
    const apexY = Math.min(sourceY, targetY) - lift;
    edgePath = `M ${sourceX},${sourceY} C ${sourceX + 70},${apexY} ${targetX - 70},${apexY} ${targetX},${targetY}`;
    labelX = (sourceX + targetX) / 2;
    labelY = 0.125 * sourceY + 0.75 * apexY + 0.125 * targetY;
  } else {
    [edgePath, labelX, labelY] = getBezierPath({
      sourceX,
      sourceY,
      sourcePosition,
      targetX,
      targetY,
      targetPosition,
    });
  }

  return (
    <>
      <BaseEdge id={id} path={edgePath} markerEnd={markerEnd} style={style} />
      {data?.labelText && !data.dimmed && (
        <EdgeLabelRenderer>
          <div
            style={{
              transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
              color: data.color,
            }}
            className={cn(
              "nodrag nopan pointer-events-none absolute rounded border border-border/60 bg-card/90 px-1.5 py-0.5 font-semibold text-[10px] leading-none shadow-xs backdrop-blur-[2px]",
              data.emphasized && "ring-1 ring-current/30",
            )}
          >
            {data.labelText}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  );
}

// Dify-style node card.
function ExploreNode({ data, selected }: NodeProps<ExploreRFNode>) {
  const n = data.node;
  const kind = viewKind(n);
  const meta = typeMeta[kind] ?? typeMeta.intent;
  const Icon = meta.icon;
  const cardMeta = extractNodeCardMeta(n);
  const isLive = !n.inherited && n.type === "intent" && n.state === "running";
  const showPriority = (n.type === "goal" || n.type === "intent") && n.priority > 0;
  const borderClass = resolveCardBorderClass({
    selected,
    focused: data.focused,
    searchActive: data.searchActive,
    replayHighlight: data.replayHighlight,
    isLive,
    isStale: cardMeta.isStale,
  });

  return (
    <div
      className={cn(
        "w-[252px] cursor-pointer rounded-2xl border-2 transition-all",
        borderClass,
        data.dimmed && "opacity-25 hover:opacity-85",
      )}
    >
      <div
        className={cn(
          "group relative rounded-[14px] border border-black/[0.06] bg-white shadow-sm transition-shadow hover:shadow-lg dark:border-white/10 dark:bg-neutral-900",
          cardMeta.isStale && !data.focused && !selected && "bg-white/85 dark:bg-neutral-900/80",
        )}
      >
        <Handle
          type="target"
          position={Position.Left}
          className="!size-2 !border-2 !border-neutral-300 !bg-white dark:!border-neutral-600 dark:!bg-neutral-800 opacity-0 transition-opacity group-hover:opacity-100"
        />
        <div className="flex items-center gap-2 px-3 pt-2.5 pb-1.5">
          <span className={cn("flex size-6 shrink-0 items-center justify-center rounded-lg shadow-sm", meta.iconBg)}>
            <Icon className="size-3.5 text-white" />
          </span>
          <span className="grow truncate font-semibold text-[13px] text-neutral-700 dark:text-neutral-100">
            {meta.label}
          </span>
          {isLive && (
            <span className="relative flex size-2 shrink-0">
              <span className="absolute inline-flex size-full animate-ping rounded-full bg-blue-400 opacity-70" />
              <span className="relative inline-flex size-2 rounded-full bg-blue-500" />
            </span>
          )}
          {cardMeta.severity && (
            <StatusBadge domain="severity" value={cardMeta.severity} dot className="shrink-0 px-1.5 py-0 text-[10px]" />
          )}
          {showPriority && (
            <span
              className={cn(
                "shrink-0 rounded-md px-1.5 py-0.5 font-semibold text-[10px] tabular-nums",
                toneClasses[prioTone(n.priority)],
              )}
            >
              P{n.priority}
            </span>
          )}
        </div>
        <div className="line-clamp-3 px-3 pb-2 text-[13px] text-neutral-500 leading-snug dark:text-neutral-400">
          {nodeSummary(n) || meta.label}
        </div>
        {(cardMeta.evidenceBadges.length > 0 || cardMeta.blockedHint) && (
          <div className="flex flex-wrap items-center gap-1 px-3 pb-2">
            {cardMeta.evidenceBadges.map((lbl) => (
              <span
                key={lbl}
                className="rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0 text-[10px] text-amber-700 dark:text-amber-300"
              >
                {lbl}
              </span>
            ))}
            {cardMeta.blockedHint && (
              <span
                className="max-w-full truncate rounded border border-amber-500/30 bg-amber-500/10 px-1.5 py-0 text-[10px] text-amber-700 dark:text-amber-300"
                title={cardMeta.blockedHint}
              >
                {cardMeta.blockedHint}
              </span>
            )}
          </div>
        )}
        <div className="flex items-center justify-between gap-2 px-3 pb-2.5">
          <div className="flex min-w-0 items-center gap-1.5">
            <NodeCardFooter node={n} cardMeta={cardMeta} fallbackLabel={meta.label} />
          </div>
          <span className="shrink-0 font-mono text-[10px] text-neutral-400 dark:text-neutral-500">#{n.id}</span>
        </div>
        <Handle
          type="source"
          position={Position.Right}
          className="!size-2 !border-2 !border-neutral-300 !bg-white dark:!border-neutral-600 dark:!bg-neutral-800 opacity-0 transition-opacity group-hover:opacity-100"
        />
      </div>
    </div>
  );
}

const nodeTypes = { explore: ExploreNode };
const edgeTypes = { explore: ExploreEdge };

// ExplorationGraphInner renders the given nodes/edges as a React Flow canvas.
// Positions are auto-laid-out and preserved across live polling updates (so a
// polling caller's graph doesn't jump); single-click highlights causal lineage,
// double-click (or inspector button) opens NodeDetailDialog.
function ExplorationGraphInner({
  nodes,
  edges,
  emptyHint = "暂无探索数据",
  taskId,
  toolbarExtra,
}: {
  nodes: TaskNode[];
  edges: Edge[];
  emptyHint?: string;
  taskId?: string;
  toolbarExtra?: React.ReactNode;
}) {
  const { fitView } = useReactFlow<ExploreRFNode, ExploreRFEdge>();
  const [rfNodes, setRfNodes, onNodesChange] = useNodesState<ExploreRFNode>([]);
  const [rfEdges, setRfEdges, onEdgesChange] = useEdgesState<ExploreRFEdge>([]);
  const [selected, setSelected] = React.useState<TaskNode | null>(null);
  const [focusedNodeId, setFocusedNodeId] = React.useState<string | null>(null);
  const [highlightedKind, setHighlightedKind] = React.useState<ExploreKind | null>(null);

  // 工具栏筛选/检索/视觉状态
  const [searchQuery, setSearchQuery] = React.useState("");
  const [searchIndex, setSearchIndex] = React.useState(0);
  const [infraFoldOverride, setInfraFoldOverride] = React.useState<boolean | null>(null);
  const [focusOutcomesOnly, setFocusOutcomesOnly] = React.useState(false);
  const [hideDeadLeaves, setHideDeadLeaves] = React.useState(false);
  const [layoutVersion, setLayoutVersion] = React.useState(0);

  // 缩略图与图例默认收起，确保初始 fitView 后不遮挡上方/右下节点。
  const [showMiniMap, setShowMiniMap] = React.useState(false);
  const [legendExpanded, setLegendExpanded] = React.useState(false);

  // 时间回放状态
  const [replayActive, setReplayActive] = React.useState(false);
  const [replayPaused, setReplayPaused] = React.useState(false);
  const [replayCount, setReplayCount] = React.useState(0);
  const [replaySpeed, setReplaySpeed] = React.useState<(typeof REPLAY_SPEEDS)[number]>(1);
  const [followReplay, setFollowReplay] = React.useState(true);

  const prevLayoutKeyRef = React.useRef("");
  const hadNodesRef = React.useRef(false);
  const lastAutoSearchRef = React.useRef("");

  // Full node index (incl. folded members) so the digest drawer can resolve
  // covered members even though they are not rendered on the canvas.
  const nodesById = React.useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  // Collapsed view: folded members removed, their edges rewired onto the digest.
  const { viewNodes, viewEdges } = React.useMemo(() => collapseDigestGraph(nodes, edges), [nodes, edges]);

  const infraCount = React.useMemo(() => viewNodes.filter(isInfraNode).length, [viewNodes]);
  const hideInfra = infraFoldOverride ?? infraCount > INFRA_AUTO_HIDE_THRESHOLD;

  const { nodes: filteredNodes, edges: filteredEdges } = React.useMemo(
    () => filterExplorationGraph(viewNodes, viewEdges, { hideInfra, focusOutcomesOnly, hideDeadLeaves }),
    [viewNodes, viewEdges, hideInfra, focusOutcomesOnly, hideDeadLeaves],
  );

  const filteredNodesById = React.useMemo(() => new Map(filteredNodes.map((n) => [n.id, n])), [filteredNodes]);

  const kindCounts = React.useMemo(() => {
    const counts = new Map<ExploreKind, number>();
    for (const n of filteredNodes) {
      const k = viewKind(n);
      if (typeMeta[k]) counts.set(k, (counts.get(k) ?? 0) + 1);
    }
    return counts;
  }, [filteredNodes]);

  const legendKinds = React.useMemo(
    () => (Object.keys(typeMeta) as ExploreKind[]).filter((k) => (kindCounts.get(k) ?? 0) > 0),
    [kindCounts],
  );

  const activeRels = React.useMemo(() => {
    const seen = new Set<string>();
    for (const e of filteredEdges) seen.add(e.rel);
    const known = Object.keys(relLabel).filter((r) => seen.has(r));
    const extras = [...seen].filter((r) => !(r in relLabel));
    return [...known, ...extras];
  }, [filteredEdges]);

  // 始终基于筛选后的完整图预计算节点坐标：时间轴回放逐步揭示卡片时，旧卡片与整体位置零跳动。
  const fullLayout = React.useMemo(
    () => computeExplorationLayout(filteredNodes, filteredEdges),
    [filteredNodes, filteredEdges],
  );

  const replayPlan = React.useMemo(() => buildReplayPlan(filteredNodes), [filteredNodes]);
  const totalReplaySteps = replayPlan.stepIds.length;
  const replaySlice = React.useMemo(
    () => (replayActive ? sliceGraphForReplay(filteredNodes, filteredEdges, replayPlan, replayCount) : null),
    [replayActive, filteredNodes, filteredEdges, replayPlan, replayCount],
  );
  const displayNodes = replaySlice ? replaySlice.visibleNodes : filteredNodes;
  const displayEdges = replaySlice ? replaySlice.visibleEdges : filteredEdges;
  const latestReplayId = replaySlice ? replaySlice.latestId : null;
  const replayFinished = Boolean(replaySlice && replaySlice.clampedCount >= totalReplaySteps);
  let replayActionLabel = "暂停";
  if (replayFinished) replayActionLabel = "重播";
  else if (replayPaused) replayActionLabel = "继续";

  const searchMatches = React.useMemo(
    () => findMatchingNodeIds(displayNodes, searchQuery, nodeSummary),
    [displayNodes, searchQuery],
  );
  const displaySearchIndex =
    searchMatches.length > 0 ? ((searchIndex % searchMatches.length) + searchMatches.length) % searchMatches.length : 0;
  const activeSearchId = searchMatches.length > 0 ? searchMatches[displaySearchIndex] : null;
  const searchMatchSet = React.useMemo(() => new Set(searchMatches), [searchMatches]);

  // 筛选后节点变化若不包含当前聚焦节点，则自动清理聚焦。
  const activeFocusedId = focusedNodeId && filteredNodesById.has(focusedNodeId) ? focusedNodeId : null;
  const focusedNode = activeFocusedId ? (filteredNodesById.get(activeFocusedId) ?? null) : null;
  const activeHighlightedKind = highlightedKind && (kindCounts.get(highlightedKind) ?? 0) > 0 ? highlightedKind : null;

  const lineage = React.useMemo(
    () => (activeFocusedId ? collectLineageSubgraph(activeFocusedId, displayNodes, displayEdges) : null),
    [activeFocusedId, displayNodes, displayEdges],
  );

  React.useEffect(() => {
    if (!replayActive || replayPaused) return;
    if (totalReplaySteps === 0 || replayCount >= totalReplaySteps) {
      setReplayPaused(true);
      return;
    }
    const intervalMs = Math.max(90, Math.round(450 / replaySpeed));
    const timer = window.setInterval(() => {
      setReplayCount((prev) => {
        if (prev + 1 >= totalReplaySteps) {
          setReplayPaused(true);
          return totalReplaySteps;
        }
        return prev + 1;
      });
    }, intervalMs);
    return () => window.clearInterval(timer);
  }, [replayActive, replayPaused, replayCount, replaySpeed, totalReplaySteps]);

  // 回放自动跟随：每步平滑聚焦到刚揭示的节点。
  React.useEffect(() => {
    if (!replayActive || !followReplay || !latestReplayId) return;
    const raf = window.requestAnimationFrame(() => {
      void fitView({ nodes: [{ id: latestReplayId }], duration: 220, maxZoom: 1.05, padding: 0.45 });
    });
    return () => window.cancelAnimationFrame(raf);
  }, [replayActive, followReplay, latestReplayId, fitView]);

  const startReplay = React.useCallback(() => {
    if (totalReplaySteps === 0) return;
    setFocusedNodeId(null);
    setHighlightedKind(null);
    setReplayActive(true);
    setReplayPaused(false);
    setReplayCount(0);
  }, [totalReplaySteps]);

  const stopReplay = React.useCallback(() => {
    setReplayActive(false);
    setReplayPaused(false);
    setReplayCount(0);
  }, []);

  const focusViewportOn = React.useCallback(
    (ids: string[], maxZoom = 1.15) => {
      if (ids.length === 0) return;
      window.requestAnimationFrame(() => {
        void fitView({
          nodes: ids.map((id) => ({ id })),
          duration: 260,
          maxZoom,
          padding: ids.length === 1 ? 0.55 : 0.22,
        });
      });
    },
    [fitView],
  );

  const resetLayout = React.useCallback(() => {
    setLayoutVersion((v) => v + 1);
  }, []);

  const stepSearch = React.useCallback(
    (delta: number) => {
      if (searchMatches.length === 0) return;
      const next =
        (((displaySearchIndex + delta) % searchMatches.length) + searchMatches.length) % searchMatches.length;
      setSearchIndex(next);
      focusViewportOn([searchMatches[next]], 1.15);
    },
    [searchMatches, displaySearchIndex, focusViewportOn],
  );

  // 仅在搜索词变化后首跳到第一个命中节点，避免 20s 后台轮询强行拉回用户视角。
  React.useEffect(() => {
    const trimmed = searchQuery.trim();
    if (!trimmed) {
      lastAutoSearchRef.current = "";
      setSearchIndex(0);
      return;
    }
    if (lastAutoSearchRef.current !== trimmed && searchMatches.length > 0) {
      lastAutoSearchRef.current = trimmed;
      setSearchIndex(0);
      focusViewportOn([searchMatches[0]], 1.15);
    }
  }, [searchMatches, searchQuery, focusViewportOn]);

  const layoutKey = `${hideInfra ? 1 : 0}|${focusOutcomesOnly ? 1 : 0}|${hideDeadLeaves ? 1 : 0}|${layoutVersion}`;

  React.useEffect(() => {
    setSelected((cur) => (cur ? (nodesById.get(cur.id) ?? cur) : cur));
    const layoutReset = prevLayoutKeyRef.current !== layoutKey;
    prevLayoutKeyRef.current = layoutKey;
    const initialPopulate = !hadNodesRef.current && displayNodes.length > 0;
    hadNodesRef.current = displayNodes.length > 0;

    const liveTargets = new Set(
      displayNodes.filter((n) => !n.inherited && n.type === "intent" && n.state === "running").map((n) => n.id),
    );
    const hasSearchSpotlight = Boolean(searchQuery.trim());
    const hasAnySpotlight = Boolean(lineage || activeHighlightedKind || hasSearchSpotlight);

    setRfNodes((prev) => {
      const prevPos = layoutReset
        ? new Map<string, { x: number; y: number }>()
        : new Map(prev.map((p) => [p.id, p.position]));
      const occupied: Array<{ x: number; y: number }> = [];
      for (const n of displayNodes) {
        const kept = prevPos.get(n.id);
        if (kept) occupied.push(kept);
      }

      return displayNodes.map((n) => {
        let position = prevPos.get(n.id);
        if (!position) {
          const candidate = { ...(fullLayout.get(n.id) ?? { x: 0, y: 0 }) };
          // 增量轮询加入新节点时，避开已有手动/保留节点位置防止重叠。
          while (occupied.some((p) => Math.abs(p.x - candidate.x) < 220 && Math.abs(p.y - candidate.y) < 120)) {
            candidate.y += LAYOUT_ROW_H;
          }
          occupied.push(candidate);
          position = candidate;
        }

        let emphasized = true;
        if (lineage) emphasized = lineage.nodeIds.has(n.id);
        else if (activeHighlightedKind) emphasized = viewKind(n) === activeHighlightedKind;
        else if (hasSearchSpotlight) emphasized = searchMatchSet.has(n.id);

        return {
          id: n.id,
          type: "explore" as const,
          position,
          data: {
            node: n,
            replayHighlight: latestReplayId === n.id,
            focused: activeFocusedId === n.id,
            searchActive: activeSearchId === n.id,
            dimmed: hasAnySpotlight && !emphasized,
          },
          sourcePosition: Position.Right,
          targetPosition: Position.Left,
        };
      });
    });

    setRfEdges(
      displayEdges.map((e, i) => {
        const color = relColor[e.rel] ?? "#94a3b8";
        const animated = liveTargets.has(e.dst);
        const dashed = DASHED_RELS.has(e.rel);
        const key = edgeSemanticKey(e);
        const inLineage = Boolean(lineage?.edgeKeys.has(key));
        const edgeDimmed = Boolean(lineage ? !inLineage : activeHighlightedKind || hasSearchSpotlight);
        const strokeWidth = resolveEdgeStrokeWidth(inLineage, dashed);
        return {
          id: `e${i}-${e.src}-${e.dst}`,
          source: e.src,
          target: e.dst,
          type: "explore" as const,
          animated,
          data: {
            labelText: relLabel[e.rel] ?? e.rel,
            color,
            dimmed: edgeDimmed,
            emphasized: inLineage,
          },
          style: {
            stroke: color,
            strokeWidth,
            strokeOpacity: resolveEdgeOpacity(animated, dashed, inLineage, edgeDimmed),
            strokeDasharray: dashed ? "6 4" : undefined,
          },
          markerEnd: { type: MarkerType.ArrowClosed, color, width: 14, height: 14 },
        } satisfies ExploreRFEdge;
      }),
    );

    if ((layoutReset || initialPopulate) && displayNodes.length > 0) {
      window.requestAnimationFrame(() => {
        void fitView({ padding: 0.16, duration: initialPopulate ? 180 : 240 });
      });
    }
  }, [
    displayNodes,
    displayEdges,
    fullLayout,
    layoutKey,
    latestReplayId,
    nodesById,
    lineage,
    activeFocusedId,
    activeHighlightedKind,
    searchQuery,
    searchMatchSet,
    activeSearchId,
    setRfNodes,
    setRfEdges,
    fitView,
  ]);

  const hasFiltersActive = focusOutcomesOnly || hideDeadLeaves || Boolean(searchQuery.trim());

  return (
    <>
      {/* 顶部画布外紧凑工具栏：避免浮层过大遮挡画布 */}
      <div className="flex flex-wrap items-center gap-2 border-border/60 border-b bg-card/60 px-3 py-2 text-xs">
        {toolbarExtra}

        <div className="relative flex items-center">
          <Search className="pointer-events-none absolute left-2 size-3.5 text-muted-foreground" />
          <Input
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                stepSearch(e.shiftKey ? -1 : 1);
              } else if (e.key === "Escape") {
                setSearchQuery("");
              }
            }}
            placeholder="搜 #ID / 摘要 / 来源…"
            className="h-7 w-44 pr-6 pl-7 text-xs sm:w-52"
          />
          {searchQuery && (
            <button
              type="button"
              onClick={() => setSearchQuery("")}
              className="absolute right-1.5 text-muted-foreground hover:text-foreground"
              aria-label="清空搜索"
            >
              <X className="size-3" />
            </button>
          )}
        </div>

        {searchQuery.trim() && (
          <div className="flex items-center gap-1">
            <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
              {searchMatches.length > 0 ? `${displaySearchIndex + 1}/${searchMatches.length}` : "0/0"}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              disabled={searchMatches.length === 0}
              onClick={() => stepSearch(-1)}
              title="上一个匹配"
            >
              <ChevronUp className="size-3.5" />
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              disabled={searchMatches.length === 0}
              onClick={() => stepSearch(1)}
              title="下一个匹配"
            >
              <ChevronDown className="size-3.5" />
            </Button>
          </div>
        )}

        <div className="flex flex-wrap items-center gap-1">
          <Button
            type="button"
            variant={focusOutcomesOnly ? "secondary" : "ghost"}
            size="xs"
            onClick={() => setFocusOutcomesOnly((v) => !v)}
            title="只保留漏洞、战果、目标、遗留物、痕迹及它们的上游成因链"
          >
            <Focus className="size-3" />
            核心战果链
          </Button>
          <Button
            type="button"
            variant={hideDeadLeaves ? "secondary" : "ghost"}
            size="xs"
            onClick={() => setHideDeadLeaves((v) => !v)}
            title="隐藏已结束且无任何下游产出的阴性/空结果分支"
          >
            <GitBranch className="size-3" />
            隐藏无产出
          </Button>
          {infraCount > 0 && (
            <Button
              type="button"
              variant={hideInfra ? "outline" : "secondary"}
              size="xs"
              onClick={() => setInfraFoldOverride(!hideInfra)}
              title={hideInfra ? "点击展开导入的 AD/云对象拓扑" : "点击折叠隐藏导入的 AD/云对象"}
            >
              <Layers className="size-3" />
              {hideInfra ? `已折叠 AD/云 (${infraCount})` : `显示 AD/云 (${infraCount})`}
            </Button>
          )}
        </div>

        <div className="ml-auto flex flex-wrap items-center gap-1">
          {totalReplaySteps > 0 && (
            <div className="mr-1 flex items-center gap-1 border-border/60 border-r pr-2">
              {!replayActive ? (
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  onClick={startReplay}
                  title="按节点创建时间顺序回放图谱演化"
                >
                  <Play className="size-3" />
                  时间回放
                </Button>
              ) : (
                <>
                  <Button
                    type="button"
                    variant="secondary"
                    size="xs"
                    onClick={() => {
                      if (replaySlice && replaySlice.clampedCount >= totalReplaySteps) {
                        setReplayCount(0);
                        setReplayPaused(false);
                      } else {
                        setReplayPaused((p) => !p);
                      }
                    }}
                    title={replayPaused ? "继续/重播" : "暂停"}
                  >
                    {replayPaused ? <Play className="size-3" /> : <Pause className="size-3" />}
                    {replayActionLabel}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    onClick={() => {
                      setReplayPaused(true);
                      setReplayCount((c) => Math.max(0, c - 1));
                    }}
                    disabled={!replaySlice || replaySlice.clampedCount <= 0}
                    title="上一步"
                  >
                    <SkipBack className="size-3" />
                  </Button>
                  <span className="px-1 font-mono text-[11px] text-muted-foreground tabular-nums">
                    {replaySlice ? replaySlice.clampedCount : 0}/{totalReplaySteps}
                  </span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    onClick={() => {
                      setReplayPaused(true);
                      setReplayCount((c) => Math.min(totalReplaySteps, c + 1));
                    }}
                    disabled={!replaySlice || replaySlice.clampedCount >= totalReplaySteps}
                    title="下一步"
                  >
                    <SkipForward className="size-3" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="xs"
                    onClick={() => {
                      const idx = REPLAY_SPEEDS.indexOf(replaySpeed);
                      setReplaySpeed(REPLAY_SPEEDS[(idx + 1) % REPLAY_SPEEDS.length]);
                    }}
                    title="切换回放倍速"
                    className="font-mono tabular-nums"
                  >
                    {replaySpeed}x
                  </Button>
                  <Button
                    type="button"
                    variant={followReplay ? "secondary" : "ghost"}
                    size="xs"
                    onClick={() => setFollowReplay((v) => !v)}
                    title="回放时自动跟踪镜头至最新节点"
                  >
                    跟随
                  </Button>
                  <Button type="button" variant="ghost" size="xs" onClick={stopReplay} title="结束回放并显示完整图谱">
                    <Square className="size-2.5" />
                    退出
                  </Button>
                </>
              )}
            </div>
          )}

          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={() => void fitView({ padding: 0.16, duration: 240 })}
            title="适配全图到当前视口"
          >
            <Maximize2 className="size-3" />
            适配
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={resetLayout}
            title="清除手动拖拽位置并按最新拓扑重新自动排版"
          >
            <RotateCcw className="size-3" />
            重排
          </Button>
          <Button
            type="button"
            variant={showMiniMap ? "secondary" : "ghost"}
            size="xs"
            onClick={() => setShowMiniMap((v) => !v)}
          >
            缩略图
          </Button>
        </div>
      </div>

      <div className="relative min-h-0 w-full flex-1">
        <ReactFlow
          nodes={rfNodes}
          edges={rfEdges}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeClick={(_, node) => {
            setHighlightedKind(null);
            setFocusedNodeId((cur) => (cur === node.id ? null : node.id));
          }}
          onNodeDoubleClick={(_, node) => setSelected(node.data.node)}
          onPaneClick={() => {
            setFocusedNodeId(null);
            setHighlightedKind(null);
          }}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          fitView
          fitViewOptions={{ padding: 0.16 }}
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
              position="bottom-right"
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

          {/* 左上角动态可折叠图例 */}
          {filteredNodes.length > 0 && (
            <Panel position="top-left">
              <div
                className={cn(
                  "rounded-lg border bg-card/95 text-xs shadow-sm backdrop-blur",
                  legendExpanded ? "flex max-w-[min(560px,calc(100vw-4rem))] flex-col gap-2 p-2.5" : "px-2.5 py-1.5",
                )}
              >
                <div className="flex items-center gap-2">
                  <span className="font-medium text-foreground">图例</span>
                  <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                    {filteredNodes.length} 节点 · {filteredEdges.length} 连线
                  </span>
                  <button
                    type="button"
                    onClick={() => setLegendExpanded((v) => !v)}
                    className="ml-1 inline-flex items-center gap-0.5 text-muted-foreground hover:text-foreground"
                  >
                    {legendExpanded ? (
                      <>
                        收起
                        <ChevronUp className="size-3.5" />
                      </>
                    ) : (
                      <>
                        展开
                        <ChevronDown className="size-3.5" />
                      </>
                    )}
                  </button>
                </div>

                {legendExpanded && (
                  <>
                    <div className="flex flex-wrap gap-1.5">
                      {legendKinds.map((k) => {
                        const m = typeMeta[k];
                        const Icon = m.icon;
                        const cnt = kindCounts.get(k) ?? 0;
                        const active = activeHighlightedKind === k;
                        return (
                          <button
                            key={k}
                            type="button"
                            onClick={() => {
                              setFocusedNodeId(null);
                              setHighlightedKind((cur) => (cur === k ? null : k));
                            }}
                            className={cn(
                              "inline-flex items-center gap-1.5 rounded-md border px-1.5 py-0.5 text-foreground transition-colors",
                              active
                                ? "border-primary bg-primary/10 font-medium"
                                : "border-transparent hover:border-border hover:bg-accent/60",
                            )}
                            title="点击在画布中高亮此类型节点"
                          >
                            <span className={cn("flex size-4 items-center justify-center rounded", m.iconBg)}>
                              <Icon className="size-2.5 text-white" />
                            </span>
                            <span>{m.label}</span>
                            <span className="font-mono text-[10px] text-muted-foreground tabular-nums">{cnt}</span>
                          </button>
                        );
                      })}
                    </div>

                    {activeRels.length > 0 && (
                      <div className="flex flex-wrap gap-x-3 gap-y-1.5 border-border/60 border-t pt-2 text-muted-foreground">
                        {activeRels.map((r) => {
                          const dashed = DASHED_RELS.has(r);
                          return (
                            <span key={r} className="inline-flex items-center gap-1.5">
                              <span
                                className={cn("w-4", dashed ? "border-t-2 border-dashed" : "h-0.5 rounded-full")}
                                style={
                                  dashed
                                    ? { borderColor: relColor[r] ?? "#94a3b8" }
                                    : { backgroundColor: relColor[r] ?? "#94a3b8" }
                                }
                              />
                              {relLabel[r] ?? r}
                            </span>
                          );
                        })}
                      </div>
                    )}
                    <div className="text-[11px] text-muted-foreground">单击卡片高亮因果链路 · 双击打开详情</div>
                  </>
                )}
              </div>
            </Panel>
          )}

          {/* 右上角聚焦链路/高亮类型摘要浮条 */}
          {(focusedNode || activeHighlightedKind) && (
            <Panel position="top-right">
              <div className="flex max-w-[min(360px,calc(100vw-3rem))] flex-col gap-2 rounded-lg border bg-card/95 p-2.5 text-xs shadow-sm backdrop-blur">
                {focusedNode && lineage ? (
                  <>
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="flex items-center gap-1.5 font-semibold text-foreground">
                          <span>{(typeMeta[viewKind(focusedNode)] ?? typeMeta.intent).label}</span>
                          <span className="font-mono text-muted-foreground">#{focusedNode.id}</span>
                        </div>
                        <p className="mt-0.5 line-clamp-2 text-[11px] text-muted-foreground">
                          {nodeSummary(focusedNode) || "（无摘要）"}
                        </p>
                      </div>
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon-xs"
                        onClick={() => setFocusedNodeId(null)}
                        aria-label="清除聚焦"
                      >
                        <X className="size-3.5" />
                      </Button>
                    </div>
                    <div className="flex items-center justify-between gap-2 border-border/60 border-t pt-1.5">
                      <span className="font-mono text-[11px] text-muted-foreground tabular-nums">
                        上游 {lineage.upstreamCount} · 下游 {lineage.downstreamCount}
                      </span>
                      <div className="flex items-center gap-1">
                        <Button
                          type="button"
                          variant="ghost"
                          size="xs"
                          onClick={() => focusViewportOn([...lineage.nodeIds], 1.1)}
                          title="缩放视图以容纳整条高亮链路"
                        >
                          <Focus className="size-3" />
                          框选链路
                        </Button>
                        <Button type="button" variant="secondary" size="xs" onClick={() => setSelected(focusedNode)}>
                          <Eye className="size-3" />
                          查看详情
                        </Button>
                      </div>
                    </div>
                  </>
                ) : (
                  activeHighlightedKind && (
                    <div className="flex items-center gap-2">
                      <span className="text-foreground">
                        已高亮 <strong>{typeMeta[activeHighlightedKind].label}</strong>（
                        {kindCounts.get(activeHighlightedKind) ?? 0}）
                      </span>
                      <Button type="button" variant="ghost" size="xs" onClick={() => setHighlightedKind(null)}>
                        清除
                      </Button>
                    </div>
                  )
                )}
              </div>
            </Panel>
          )}
        </ReactFlow>

        {filteredNodes.length === 0 && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center p-4">
            <div className="pointer-events-auto flex flex-col items-center gap-2 rounded-lg border bg-card/95 px-4 py-3 text-center text-xs shadow-sm backdrop-blur">
              <span className="text-muted-foreground">
                {nodes.length === 0 ? emptyHint : "当前筛选条件下无可显示节点"}
              </span>
              {nodes.length > 0 && (hasFiltersActive || hideInfra) && (
                <Button
                  type="button"
                  variant="outline"
                  size="xs"
                  onClick={() => {
                    setFocusOutcomesOnly(false);
                    setHideDeadLeaves(false);
                    setInfraFoldOverride(false);
                    setSearchQuery("");
                  }}
                >
                  重置筛选并显示全部（{viewNodes.length}）
                </Button>
              )}
            </div>
          </div>
        )}
      </div>

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
  toolbarExtra,
}: {
  nodes: TaskNode[];
  edges: Edge[];
  className?: string;
  emptyHint?: string;
  /** 传了才给凭据节点开「显示明文」——明文入口是任务内的动作（服务端按任务判定）。 */
  taskId?: string;
  toolbarExtra?: React.ReactNode;
}) {
  return (
    <div className={cn("flex w-full flex-col overflow-hidden rounded-xl", className)}>
      <ReactFlowProvider>
        <ExplorationGraphInner
          nodes={nodes}
          edges={edges}
          emptyHint={emptyHint}
          taskId={taskId}
          toolbarExtra={toolbarExtra}
        />
      </ReactFlowProvider>
    </div>
  );
}
