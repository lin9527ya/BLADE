import type { Edge, TaskNode } from "@/lib/types";

// 探索图时间轴回放（纯函数）：按节点创建时间（ts）升序构造逐步揭示序列，
// 起点/继承节点作为第 0 帧基座常驻画布，保证回放期间已有卡片位置零跳动。

export interface ReplayPlan {
  /** 第 0 帧常驻节点（begin / task / inherited） */
  baseIds: string[];
  /** 按创建时间升序排列的待逐步揭示节点 */
  stepIds: string[];
}

export interface ReplaySlice {
  visibleNodes: TaskNode[];
  visibleEdges: Edge[];
  latestId: string | null;
  clampedCount: number;
  totalSteps: number;
}

function compareNodeID(a: string, b: string): number {
  const na = Number(a);
  const nb = Number(b);
  if (Number.isFinite(na) && Number.isFinite(nb) && na !== nb) {
    return na - nb;
  }
  return a.localeCompare(b);
}

function parsedTimestamp(ts: string | undefined): number {
  if (!ts) return Number.POSITIVE_INFINITY;
  const ms = Date.parse(ts);
  return Number.isFinite(ms) ? ms : Number.POSITIVE_INFINITY;
}

function isBaseReplayNode(node: TaskNode): boolean {
  return node.type === "begin" || node.type === "task" || Boolean(node.inherited);
}

export function buildReplayPlan(nodes: TaskNode[]): ReplayPlan {
  const base: TaskNode[] = [];
  const steps: TaskNode[] = [];

  for (const node of nodes) {
    if (isBaseReplayNode(node)) {
      base.push(node);
    } else {
      steps.push(node);
    }
  }

  base.sort((a, b) => compareNodeID(a.id, b.id));
  steps.sort((a, b) => {
    const ta = parsedTimestamp(a.ts);
    const tb = parsedTimestamp(b.ts);
    if (ta !== tb) return ta - tb;
    return compareNodeID(a.id, b.id);
  });

  return {
    baseIds: base.map((n) => n.id),
    stepIds: steps.map((n) => n.id),
  };
}

export function sliceGraphForReplay(
  nodes: TaskNode[],
  edges: Edge[],
  plan: ReplayPlan,
  revealedCount: number,
): ReplaySlice {
  const totalSteps = plan.stepIds.length;
  const clampedCount = Math.max(0, Math.min(Math.trunc(revealedCount), totalSteps));
  const revealed = new Set<string>(plan.baseIds);
  for (let i = 0; i < clampedCount; i++) {
    revealed.add(plan.stepIds[i]);
  }
  const latestId = clampedCount > 0 ? plan.stepIds[clampedCount - 1] : null;
  const visibleNodes = nodes.filter((n) => revealed.has(n.id));
  const visibleEdges = edges.filter((e) => revealed.has(e.src) && revealed.has(e.dst));

  return {
    visibleNodes,
    visibleEdges,
    latestId,
    clampedCount,
    totalSteps,
  };
}
