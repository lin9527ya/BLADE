import type { Edge, ExploreKind, TaskNode } from "./types";

// 探索链路图的纯计算层（布局、筛选、检索、上下游链路追踪、卡片语义摘要）：
// 不依赖 React / Lucide，便于在 Node 环境下做确定性单元测试。

export const LAYOUT_COL_W = 410;
export const LAYOUT_ROW_H = 172;
export const COMPONENT_GAP_Y = 180;
export const INFRA_AUTO_HIDE_THRESHOLD = 20;

// 凭据/遗留物 kind 词表（卡片摘要与详情弹窗共用）。
export const LOOT_KIND_LABELS: Record<string, string> = {
  password: "口令",
  hash: "哈希",
  key: "私钥/密钥",
  token: "令牌",
  webshell: "Webshell",
  upload: "上传文件",
  account: "账号",
  scheduled_task: "计划任务",
  config_change: "配置变更",
  service: "服务",
  other: "其它",
};

// AD / 云对象 payload.category 中文词表。
export const OBJECT_CATEGORY_LABELS: Record<string, string> = {
  user: "用户",
  group: "组",
  computer: "机器",
  domain: "域",
  ou: "OU",
  gpo: "GPO",
  account: "账号",
  role: "角色",
  policy: "策略",
  service_account: "服务账号",
  namespace: "命名空间",
  cluster: "集群",
  stub: "推断节点",
  unknown: "未知",
};

const INFRA_NODE_KINDS = new Set<ExploreKind>(["ad_object", "cloud_object"]);
const OUTCOME_NODE_KINDS = new Set<ExploreKind>([
  "finding",
  "foothold",
  "credential",
  "detection",
  "artifact",
  "footprint",
]);
const DEAD_INTENT_STATES = new Set(["negative", "out-of-scope", "no_output", "exhausted", "deleted", "stopped"]);

// 正向推进关系（src → dst）：决定左→右时间与因果分层顺序。
const FORWARD_CAUSAL_RELS = new Set([
  "spawns",
  "derived_from",
  "yields",
  "leaves",
  "reaches",
  "covers",
  "member_of",
  "admin_to",
  "session_on",
  "can",
]);

// 布局逆向约束关系（DB 中 src → dst，但时空/前置关系为 dst 应在 src 左侧）：
// - uses：intent --uses--> loot（战果先产出，后被新意图动用）
// - depends_on：downstream --depends_on--> prerequisite（前置意图应排在下游左侧）
const BACKWARD_ORDER_RELS = new Set(["uses", "depends_on"]);

// 回指/对抗对照关系（不推高目标节点或旧事实的层级，避免“目标”被拽到图最右端）。
const REFERENCE_ANNOTATION_RELS = new Set(["proves", "supersedes", "contradicts"]);

const EVIDENCE_FLAG_LABELS: Record<string, string> = {
  superseded: "已替代",
  contested: "有矛盾",
  expired: "已过期",
};

const STALE_EVIDENCE_FLAGS = new Set(["superseded", "contested", "expired"]);
const VALID_SEVERITIES = new Set(["critical", "high", "medium", "low"]);

export function isInfraNode(node: TaskNode): boolean {
  return INFRA_NODE_KINDS.has(node.type);
}

export function edgeSemanticKey(e: Pick<Edge, "src" | "rel" | "dst">): string {
  return `${e.src}\x00${e.rel}\x00${e.dst}`;
}

export function compareNodeId(a: string, b: string): number {
  const na = Number(a);
  const nb = Number(b);
  if (Number.isFinite(na) && Number.isFinite(nb) && na !== nb) {
    return na - nb;
  }
  return a.localeCompare(b);
}

export function parseNodePayload(node: TaskNode): Record<string, unknown> | null {
  const raw = node.payload?.trim();
  if (!raw) return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>;
    }
  } catch {
    /* 非 JSON payload */
  }
  return null;
}

function pushMap<T>(m: Map<string, T[]>, k: string, v: T) {
  const arr = m.get(k);
  if (arr) arr.push(v);
  else m.set(k, [v]);
}

function layoutOrientation(e: Edge): [string, string] | null {
  if (FORWARD_CAUSAL_RELS.has(e.rel)) return [e.src, e.dst];
  if (BACKWARD_ORDER_RELS.has(e.rel)) return [e.dst, e.src];
  if (REFERENCE_ANNOTATION_RELS.has(e.rel)) return null;
  return [e.src, e.dst];
}

function isOriginLikeNode(n: TaskNode): boolean {
  return n.type === "begin" || n.type === "task" || (n.type === "fact" && n.state === "origin");
}

function layoutComponent(
  compNodes: TaskNode[],
  compEdges: Edge[],
  baseY: number,
): { positions: Map<string, { x: number; y: number }>; height: number } {
  // 根节点与高优先级节点排在遍历起点，减少误断正边。
  const ordered = [...compNodes].sort((a, b) => {
    const ra = isOriginLikeNode(a) ? 0 : 1;
    const rb = isOriginLikeNode(b) ? 0 : 1;
    if (ra !== rb) return ra - rb;
    return compareNodeId(a.id, b.id);
  });
  const orderIds = ordered.map((n) => n.id);
  const nodeById = new Map(ordered.map((n) => [n.id, n]));
  const orientedEdges: Array<[string, string]> = [];
  for (const e of compEdges) {
    const pair = layoutOrientation(e);
    if (pair && pair[0] !== pair[1]) orientedEdges.push(pair);
  }

  // DFS 破环，保证分层拓扑一定终止。
  const out = new Map<string, string[]>();
  for (const [u, v] of orientedEdges) pushMap(out, u, v);
  const seen = new Map<string, 0 | 1 | 2>();
  const back = new Set<string>();
  const pairKey = (u: string, v: string) => `${u}\x00${v}`;

  for (const root of orderIds) {
    if ((seen.get(root) ?? 0) !== 0) continue;
    const stack: Array<{ u: string; i: number }> = [{ u: root, i: 0 }];
    seen.set(root, 1);
    while (stack.length > 0) {
      const top = stack[stack.length - 1];
      const nbrs = out.get(top.u) ?? [];
      if (top.i >= nbrs.length) {
        seen.set(top.u, 2);
        stack.pop();
        continue;
      }
      const v = nbrs[top.i++];
      const st = seen.get(v) ?? 0;
      if (st === 1) {
        back.add(pairKey(top.u, v));
      } else if (st === 0) {
        seen.set(v, 1);
        stack.push({ u: v, i: 0 });
      }
    }
  }

  const dagSucc = new Map<string, string[]>();
  const indeg = new Map<string, number>(orderIds.map((id) => [id, 0]));
  for (const [u, v] of orientedEdges) {
    if (back.has(pairKey(u, v))) continue;
    pushMap(dagSucc, u, v);
    indeg.set(v, (indeg.get(v) ?? 0) + 1);
  }

  const layer = new Map<string, number>(orderIds.map((id) => [id, 0]));
  const queue = orderIds.filter((id) => (indeg.get(id) ?? 0) === 0);
  for (let qi = 0; qi < queue.length; qi++) {
    const u = queue[qi];
    const lu = layer.get(u) ?? 0;
    for (const v of dagSucc.get(u) ?? []) {
      layer.set(v, Math.max(layer.get(v) ?? 0, lu + 1));
      const d = (indeg.get(v) ?? 0) - 1;
      indeg.set(v, d);
      if (d === 0) queue.push(v);
    }
  }

  const maxLayer = Math.max(0, ...layer.values());
  const columns: string[][] = Array.from({ length: maxLayer + 1 }, () => []);
  for (const id of orderIds) columns[layer.get(id) ?? 0].push(id);

  // 加权邻接用于多轮重心法，跨层连线也纳入较弱权重减少长线交叉。
  const colOf = new Map<string, number>();
  columns.forEach((ids, c) => {
    for (const id of ids) colOf.set(id, c);
  });
  const weightedNbrs = new Map<string, Array<{ id: string; col: number; weight: number }>>();
  for (const e of compEdges) {
    const cs = colOf.get(e.src);
    const cd = colOf.get(e.dst);
    if (cs === undefined || cd === undefined || cs === cd) continue;
    const dist = Math.abs(cs - cd);
    const w = dist === 1 ? 1 : 0.45 / dist;
    pushMap(weightedNbrs, e.src, { id: e.dst, col: cd, weight: w });
    pushMap(weightedNbrs, e.dst, { id: e.src, col: cs, weight: w });
  }

  const sweep = (dir: 1 | -1) => {
    const start = dir === 1 ? 1 : columns.length - 2;
    const end = dir === 1 ? columns.length : -1;
    for (let c = start; c !== end; c += dir) {
      const refCol = c - dir;
      const refIdx = new Map(columns[refCol].map((id, idx) => [id, idx]));
      const scored = columns[c].map((id, i) => {
        let sum = 0;
        let totalW = 0;
        for (const nbr of weightedNbrs.get(id) ?? []) {
          if ((dir === 1 && nbr.col >= c) || (dir === -1 && nbr.col <= c)) continue;
          const idx = refIdx.get(nbr.id);
          if (idx !== undefined) {
            sum += idx * nbr.weight;
            totalW += nbr.weight;
          } else {
            // 非紧邻列，取该列中点相对位置
            const colArr = columns[nbr.col];
            const pos = colArr.indexOf(nbr.id);
            if (pos >= 0) {
              sum += pos * nbr.weight;
              totalW += nbr.weight;
            }
          }
        }
        const bary = totalW > 0 ? sum / totalW : i;
        return { id, bary, i };
      });
      scored.sort((a, b) => {
        if (Math.abs(a.bary - b.bary) > 1e-6) return a.bary - b.bary;
        const na = nodeById.get(a.id);
        const nb = nodeById.get(b.id);
        if (na?.ts && nb?.ts && na.ts !== nb.ts) return na.ts.localeCompare(nb.ts);
        return compareNodeId(a.id, b.id);
      });
      columns[c] = scored.map((s) => s.id);
    }
  };

  if (columns.length > 1) {
    for (let iter = 0; iter < 4; iter++) {
      sweep(1);
      sweep(-1);
    }
  }

  const maxCount = Math.max(1, ...columns.map((c) => c.length));
  const totalH = Math.max(1, maxCount) * LAYOUT_ROW_H;
  const midline = baseY + ((maxCount - 1) * LAYOUT_ROW_H) / 2;
  const positions = new Map<string, { x: number; y: number }>();

  columns.forEach((ids, c) => {
    const startY = midline - ((ids.length - 1) * LAYOUT_ROW_H) / 2;
    ids.forEach((id, i) => {
      positions.set(id, { x: c * LAYOUT_COL_W, y: startY + i * LAYOUT_ROW_H });
    });
  });

  return { positions, height: totalH };
}

/**
 * 语义感知与分块打包探索图坐标布局：
 * 1) 区分因果推进、逆向依赖（depends_on / uses）与回指对照（proves / supersedes / contradicts），
 *    避免目标或前置节点被拽到最右侧；
 * 2) 按连通分量独立布局并堆叠，主探索树置顶，零散孤立节点排成紧凑矩阵，防止第 0 列垂直堆积。
 */
export function computeExplorationLayout(nodes: TaskNode[], edges: Edge[]): Map<string, { x: number; y: number }> {
  const pos = new Map<string, { x: number; y: number }>();
  if (nodes.length === 0) return pos;

  const nodeById = new Map(nodes.map((n) => [n.id, n]));
  const validEdges = edges.filter((e) => nodeById.has(e.src) && nodeById.has(e.dst));

  // 用全部有效边划分无向连通分量，确保仅靠 proves / supersedes 连接的节点不会拆散。
  const adj = new Map<string, string[]>();
  for (const e of validEdges) {
    pushMap(adj, e.src, e.dst);
    pushMap(adj, e.dst, e.src);
  }

  const visited = new Set<string>();
  const multiComponents: TaskNode[][] = [];
  const singletons: TaskNode[] = [];

  for (const node of nodes) {
    if (visited.has(node.id)) continue;
    const compIds: string[] = [];
    const queue = [node.id];
    visited.add(node.id);
    for (let qi = 0; qi < queue.length; qi++) {
      const u = queue[qi];
      compIds.push(u);
      for (const v of adj.get(u) ?? []) {
        if (!visited.has(v)) {
          visited.add(v);
          queue.push(v);
        }
      }
    }
    const compNodes = compIds.map((id) => nodeById.get(id)).filter((n): n is TaskNode => !!n);
    if (compNodes.length <= 1) {
      singletons.push(...compNodes);
    } else {
      multiComponents.push(compNodes);
    }
  }

  // 主探索连通分量置顶，其次按业务节点权重与规模排序。
  const compMeta = multiComponents.map((comp) => {
    const hasRoot = comp.some(isOriginLikeNode);
    const hasNonInfra = comp.some((n) => !isInfraNode(n));
    let minId = comp[0]?.id ?? "";
    for (const n of comp) {
      if (compareNodeId(n.id, minId) < 0) minId = n.id;
    }
    return { comp, hasRoot, hasNonInfra, minId };
  });

  compMeta.sort((a, b) => {
    if (a.hasRoot !== b.hasRoot) return a.hasRoot ? -1 : 1;
    if (a.hasNonInfra !== b.hasNonInfra) return a.hasNonInfra ? -1 : 1;
    if (a.comp.length !== b.comp.length) return b.comp.length - a.comp.length;
    return compareNodeId(a.minId, b.minId);
  });

  let cursorY = 0;
  for (const { comp } of compMeta) {
    const compSet = new Set(comp.map((n) => n.id));
    const compEdges = validEdges.filter((e) => compSet.has(e.src) && compSet.has(e.dst));
    const res = layoutComponent(comp, compEdges, cursorY);
    for (const [id, pt] of res.positions) {
      pos.set(id, pt);
    }
    cursorY += res.height + COMPONENT_GAP_Y;
  }

  if (singletons.length > 0) {
    const sortedSingletons = [...singletons].sort((a, b) => {
      const ia = isInfraNode(a) ? 1 : 0;
      const ib = isInfraNode(b) ? 1 : 0;
      if (ia !== ib) return ia - ib;
      const ra = isOriginLikeNode(a) ? 0 : 1;
      const rb = isOriginLikeNode(b) ? 0 : 1;
      if (ra !== rb) return ra - rb;
      return compareNodeId(a.id, b.id);
    });
    const cols = Math.min(4, Math.max(2, Math.ceil(Math.sqrt(sortedSingletons.length))));
    sortedSingletons.forEach((node, idx) => {
      const col = idx % cols;
      const row = Math.floor(idx / cols);
      pos.set(node.id, { x: col * LAYOUT_COL_W, y: cursorY + row * LAYOUT_ROW_H });
    });
  }

  return pos;
}

export interface ExplorationFilterOptions {
  hideInfra?: boolean;
  focusOutcomesOnly?: boolean;
  hideDeadLeaves?: boolean;
}

/**
 * 纯函数前端视图筛选：支持折叠 AD/云对象、裁剪无产出终态叶意图、聚焦核心战果/漏洞链路。
 */
export function filterExplorationGraph(
  nodes: TaskNode[],
  edges: Edge[],
  opts: ExplorationFilterOptions,
): { nodes: TaskNode[]; edges: Edge[] } {
  let workingNodes = opts.hideInfra ? nodes.filter((n) => !isInfraNode(n)) : [...nodes];
  let keepIds = new Set(workingNodes.map((n) => n.id));
  let workingEdges = edges.filter((e) => keepIds.has(e.src) && keepIds.has(e.dst));

  if (opts.hideDeadLeaves && workingNodes.length > 0) {
    // 迭代剥离无任何后续产出、无下游依赖且本身已进入负向/空结束状态的叶意图。
    const nodeById = new Map(workingNodes.map((n) => [n.id, n]));
    let changed = true;
    while (changed) {
      changed = false;
      const hasDownstream = new Set<string>();
      for (const e of workingEdges) {
        if (!keepIds.has(e.src) || !keepIds.has(e.dst)) continue;
        if (BACKWARD_ORDER_RELS.has(e.rel)) {
          // prerequisite (dst) 被 downstream (src) 依赖/使用，不能删 prerequisite
          hasDownstream.add(e.dst);
        } else if (e.rel === "proves") {
          hasDownstream.add(e.src);
        } else {
          hasDownstream.add(e.src);
        }
      }
      for (const id of keepIds) {
        const n = nodeById.get(id);
        if (!n) continue;
        if (n.type === "intent" && DEAD_INTENT_STATES.has(n.state) && !hasDownstream.has(id)) {
          keepIds.delete(id);
          changed = true;
        }
      }
    }
    workingNodes = workingNodes.filter((n) => keepIds.has(n.id));
    workingEdges = workingEdges.filter((e) => keepIds.has(e.src) && keepIds.has(e.dst));
  }

  if (opts.focusOutcomesOnly && workingNodes.length > 0) {
    const upstreamAdj = new Map<string, string[]>();
    for (const e of workingEdges) {
      const pair = layoutOrientation(e);
      if (pair) pushMap(upstreamAdj, pair[1], pair[0]);
      else if (e.rel === "proves") pushMap(upstreamAdj, e.dst, e.src);
    }
    const coreIds = new Set<string>();
    const queue: string[] = [];
    for (const n of workingNodes) {
      if (
        isOriginLikeNode(n) ||
        n.type === "goal" ||
        OUTCOME_NODE_KINDS.has(n.type) ||
        (n.type === "intent" && n.state === "running")
      ) {
        if (!coreIds.has(n.id)) {
          coreIds.add(n.id);
          queue.push(n.id);
        }
      }
    }
    for (let qi = 0; qi < queue.length; qi++) {
      const u = queue[qi];
      for (const p of upstreamAdj.get(u) ?? []) {
        if (!coreIds.has(p)) {
          coreIds.add(p);
          queue.push(p);
        }
      }
    }
    keepIds = coreIds;
    workingNodes = workingNodes.filter((n) => keepIds.has(n.id));
    workingEdges = workingEdges.filter((e) => keepIds.has(e.src) && keepIds.has(e.dst));
  }

  return { nodes: workingNodes, edges: workingEdges };
}

export interface LineageHighlight {
  nodeIds: Set<string>;
  edgeKeys: Set<string>;
  upstreamCount: number;
  downstreamCount: number;
}

/**
 * 从 focusId 出发计算其完整上游成因链与下游产物/影响链，以及直接相连的 proves/contradicts/supersedes 对照节点。
 */
export function collectLineageSubgraph(focusId: string, nodes: TaskNode[], edges: Edge[]): LineageHighlight {
  const idSet = new Set(nodes.map((n) => n.id));
  if (!idSet.has(focusId)) {
    return { nodeIds: new Set(), edgeKeys: new Set(), upstreamCount: 0, downstreamCount: 0 };
  }

  const upstreamAdj = new Map<string, string[]>();
  const downstreamAdj = new Map<string, string[]>();
  const peerNeighbors: string[] = [];

  for (const e of edges) {
    if (!idSet.has(e.src) || !idSet.has(e.dst)) continue;
    const pair = layoutOrientation(e);
    if (pair) {
      const [u, v] = pair;
      pushMap(downstreamAdj, u, v);
      pushMap(upstreamAdj, v, u);
    } else if (REFERENCE_ANNOTATION_RELS.has(e.rel)) {
      if (e.src === focusId) peerNeighbors.push(e.dst);
      else if (e.dst === focusId) peerNeighbors.push(e.src);
    }
  }

  const bfs = (start: string, adj: Map<string, string[]>): Set<string> => {
    const out = new Set<string>();
    const q = [start];
    for (let i = 0; i < q.length; i++) {
      const cur = q[i];
      for (const nxt of adj.get(cur) ?? []) {
        if (nxt !== focusId && !out.has(nxt)) {
          out.add(nxt);
          q.push(nxt);
        }
      }
    }
    return out;
  };

  const upstream = bfs(focusId, upstreamAdj);
  const downstream = bfs(focusId, downstreamAdj);

  const nodeIds = new Set<string>([focusId, ...upstream, ...downstream, ...peerNeighbors]);
  const edgeKeys = new Set<string>();
  const isUpstreamSide = (id: string) => id === focusId || upstream.has(id);
  const isDownstreamSide = (id: string) => id === focusId || downstream.has(id);

  for (const e of edges) {
    if (!nodeIds.has(e.src) || !nodeIds.has(e.dst)) continue;
    const pair = layoutOrientation(e);
    if (pair) {
      const [u, v] = pair;
      if ((isUpstreamSide(u) && isUpstreamSide(v)) || (isDownstreamSide(u) && isDownstreamSide(v))) {
        edgeKeys.add(edgeSemanticKey(e));
      }
    } else if (e.src === focusId || e.dst === focusId) {
      edgeKeys.add(edgeSemanticKey(e));
    }
  }

  return {
    nodeIds,
    edgeKeys,
    upstreamCount: upstream.size,
    downstreamCount: downstream.size,
  };
}

/**
 * 节点关键词/ID 匹配，优先返回精确 ID 命中结果。
 */
export function findMatchingNodeIds(nodes: TaskNode[], query: string, summaryFn: (n: TaskNode) => string): string[] {
  const raw = query.trim().toLowerCase();
  if (!raw) return [];
  const cleanIdQuery = raw.startsWith("#") ? raw.slice(1).trim() : raw;
  const exact: string[] = [];
  const rest: string[] = [];

  for (const n of nodes) {
    if (cleanIdQuery && n.id.toLowerCase() === cleanIdQuery) {
      exact.push(n.id);
      continue;
    }
    const haystack = [
      `#${n.id}`,
      n.id,
      summaryFn(n),
      n.origin,
      n.state,
      n.finding_status,
      n.finding_id ? `#${n.finding_id}` : "",
      n.source_task_id ? `#${n.source_task_id}` : "",
      n.fingerprint,
      n.blocked_reason,
      n.semantic_outcome,
      n.payload,
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    if (haystack.includes(raw) || (cleanIdQuery && haystack.includes(cleanIdQuery))) {
      rest.push(n.id);
    }
  }

  return [...exact, ...rest];
}

export type CardStatusDomain = "goal" | "intent" | "footprint" | "finding" | "hint" | "artifact";

export interface NodeCardMeta {
  statusDomain: CardStatusDomain | null;
  statusValue: string;
  severity?: string;
  evidenceBadges: string[];
  isStale: boolean;
  blockedHint?: string;
  subTag?: string;
}

/**
 * 提取节点卡片表面的简洁高价值状态信号，替代底部重复的 {meta.label} 兜底文案。
 */
export function extractNodeCardMeta(node: TaskNode): NodeCardMeta {
  const payload = parseNodePayload(node);
  const isOrigin = node.type === "fact" && node.state === "origin";

  let statusDomain: CardStatusDomain | null = null;
  if (
    node.type === "goal" ||
    node.type === "intent" ||
    node.type === "footprint" ||
    node.type === "finding" ||
    node.type === "hint" ||
    node.type === "artifact"
  ) {
    statusDomain = node.type;
  }
  const statusValue = node.type === "finding" ? node.finding_status || node.state : node.state;

  const flags = node.evidence_state ? node.evidence_state.flags : [];
  const evidenceBadges = flags.map((f) => EVIDENCE_FLAG_LABELS[f]).filter((v): v is string => Boolean(v));
  const isStale =
    flags.some((f) => STALE_EVIDENCE_FLAGS.has(f)) || node.state === "tombstoned" || node.state === "deleted";

  let severity: string | undefined;
  if (node.type === "finding" && typeof payload?.severity === "string") {
    const sev = payload.severity.toLowerCase();
    if (VALID_SEVERITIES.has(sev)) severity = sev;
  }

  let blockedHint: string | undefined;
  if (node.type === "intent") {
    if (node.requires_missing && node.requires_missing.length > 0) {
      blockedHint = `缺 ${node.requires_missing.length} 项前提`;
    } else if (node.blocked_reason) {
      blockedHint = node.blocked_reason;
    }
  }

  let subTag: string | undefined;
  if (isOrigin || node.type === "begin" || node.type === "task") {
    subTag = "探索起点";
  } else if (node.type === "digest") {
    const memberObj = payload?.member_ids as { facts?: unknown[]; intents?: unknown[] } | undefined;
    const cnt =
      (Array.isArray(memberObj?.facts) ? memberObj.facts.length : 0) +
      (Array.isArray(memberObj?.intents) ? memberObj.intents.length : 0);
    subTag = cnt > 0 ? `折叠 ${cnt} 项` : "阶段摘要";
  } else if (node.type === "fact") {
    if (node.state === "observed") subTag = "待确认观测";
    else if (node.state === "tombstoned") subTag = "已废弃";
    else subTag = "已确认事实";
  } else if (node.type === "credential") {
    const kindWord = typeof payload?.kind === "string" ? (LOOT_KIND_LABELS[payload.kind] ?? payload.kind) : undefined;
    const targets = Array.isArray(payload?.targets) ? payload.targets : [];
    const verifiedCount = targets.filter((t) =>
      Boolean(t && typeof t === "object" && (t as Record<string, unknown>).verified === true),
    ).length;
    if (verifiedCount > 0) subTag = `${kindWord ? `${kindWord} · ` : ""}已登 ${verifiedCount} 台`;
    else if (node.fingerprint) subTag = `${kindWord ? `${kindWord} · ` : ""}${node.fingerprint}`;
    else subTag = kindWord ?? "凭据";
  } else if (node.type === "artifact") {
    const kindWord = typeof payload?.kind === "string" ? (LOOT_KIND_LABELS[payload.kind] ?? payload.kind) : undefined;
    subTag = kindWord;
  } else if (node.type === "foothold") {
    const access = typeof payload?.access === "string" ? payload.access : "";
    const priv = typeof payload?.privilege === "string" ? payload.privilege : "";
    subTag =
      [access, priv].filter(Boolean).join(" · ") || (typeof payload?.host === "string" ? payload.host : "已控通路");
  } else if (node.type === "detection") {
    subTag = typeof payload?.result === "string" && payload.result ? payload.result : node.state;
  } else if (node.type === "ad_object" || node.type === "cloud_object") {
    const cat =
      typeof payload?.category === "string"
        ? (OBJECT_CATEGORY_LABELS[payload.category] ?? payload.category)
        : undefined;
    const hv = payload?.highvalue === true ? "高价值" : undefined;
    subTag = [cat, hv].filter(Boolean).join(" · ") || undefined;
  }

  return {
    statusDomain,
    statusValue,
    severity,
    evidenceBadges,
    isStale,
    blockedHint,
    subTag,
  };
}
