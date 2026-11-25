import {
  collectLineageSubgraph,
  computeExplorationLayout,
  extractNodeCardMeta,
  filterExplorationGraph,
  findMatchingNodeIds,
  LAYOUT_COL_W,
} from "./exploration-layout.ts";
import assert from "node:assert/strict";
import test from "node:test";

const node = (id, type, extra = {}) => ({
  id: String(id),
  type,
  priority: 5,
  state: "done",
  origin: "planner",
  ts: "2026-11-25T10:00:00Z",
  ...extra,
});

test("computeExplorationLayout 不受 proves/supersedes 拖累并正确反转 uses/depends_on 顺序", () => {
  const nodes = [
    node(1, "fact", { state: "origin" }),
    node(2, "goal", { state: "open" }),
    node(3, "intent"),
    node(4, "foothold"),
    node(5, "intent"),
    node(6, "intent"),
    node(7, "finding", { state: "confirmed" }),
    node(8, "fact", { state: "confirmed" }),
  ];
  const edges = [
    { src: "1", dst: "2", rel: "spawns" },
    { src: "1", dst: "3", rel: "derived_from" },
    { src: "3", dst: "4", rel: "yields" },
    // intent 5 动用 foothold 4：应排在 foothold 4 右侧
    { src: "5", dst: "4", rel: "uses" },
    // intent 6 依赖 intent 5：应排在 intent 5 右侧
    { src: "6", dst: "5", rel: "depends_on" },
    { src: "6", dst: "7", rel: "yields" },
    // finding 7 证明 goal 2：不能把 goal 2 拉到 finding 7 右侧
    { src: "7", dst: "2", rel: "proves" },
    // 新事实 8 替代旧事实 1：不能把起点 1 推到 8 右侧
    { src: "8", dst: "1", rel: "supersedes" },
  ];

  const pos = computeExplorationLayout(nodes, edges);
  assert.ok(pos.get("2").x < pos.get("7").x, "proves 不应把目标拽到漏洞右侧");
  assert.equal(pos.get("2").x, LAYOUT_COL_W, "目标保持在第 1 层");
  assert.ok(pos.get("3").x < pos.get("4").x, "产出战果排在来源意图右侧");
  assert.ok(pos.get("4").x < pos.get("5").x, "动用战果的意图排在战果右侧");
  assert.ok(pos.get("5").x < pos.get("6").x, "downstream 意图排在 prerequisite 右侧");
  assert.equal(pos.get("1").x, 0, "supersedes 不应将旧节点向右推");
});

test("computeExplorationLayout 按连通分量堆叠且零散节点紧凑排版无重叠", () => {
  const nodes = [
    node(1, "fact", { state: "origin" }),
    node(2, "intent"),
    node(3, "ad_object", { state: "active", payload: JSON.stringify({ category: "user" }) }),
    node(4, "ad_object", { state: "active", payload: JSON.stringify({ category: "group" }) }),
    node(5, "hint", { state: "active" }),
    node(6, "hint", { state: "active" }),
  ];
  const edges = [
    { src: "1", dst: "2", rel: "derived_from" },
    { src: "3", dst: "4", rel: "member_of" },
  ];

  const pos = computeExplorationLayout(nodes, edges);
  // 主探索树在最上方（y=0），AD 子分量在下方，零散 hint 在最下方两列矩阵
  assert.ok(pos.get("1").y < pos.get("3").y);
  assert.ok(pos.get("3").y < pos.get("5").y);
  assert.notEqual(pos.get("5").x, pos.get("6").x);
});

test("filterExplorationGraph 支持折叠 AD/云对象、裁剪无产出叶分支与聚焦战果链", () => {
  const nodes = [
    node(1, "fact", { state: "origin" }),
    node(2, "intent", { state: "negative" }),
    node(3, "intent", { state: "done" }),
    node(4, "finding", { state: "confirmed", finding_status: "confirmed" }),
    node(5, "ad_object", { state: "active" }),
  ];
  const edges = [
    { src: "1", dst: "2", rel: "derived_from" },
    { src: "1", dst: "3", rel: "derived_from" },
    { src: "3", dst: "4", rel: "yields" },
  ];

  const noInfra = filterExplorationGraph(nodes, edges, { hideInfra: true });
  assert.deepEqual(
    noInfra.nodes.map((n) => n.id),
    ["1", "2", "3", "4"],
  );

  const noDead = filterExplorationGraph(nodes, edges, { hideInfra: true, hideDeadLeaves: true });
  assert.deepEqual(
    noDead.nodes.map((n) => n.id),
    ["1", "3", "4"],
  );

  const core = filterExplorationGraph(nodes, edges, { hideInfra: true, focusOutcomesOnly: true });
  assert.deepEqual(
    core.nodes.map((n) => n.id),
    ["1", "3", "4"],
  );
});

test("collectLineageSubgraph 追踪因果上下游及直连 proves 目标", () => {
  const nodes = [
    node(1, "fact", { state: "origin" }),
    node(2, "goal", { state: "met" }),
    node(3, "intent"),
    node(4, "foothold"),
    node(5, "intent"),
    node(6, "finding"),
    node(7, "intent"), // 旁支
  ];
  const edges = [
    { src: "1", dst: "2", rel: "spawns" },
    { src: "1", dst: "3", rel: "derived_from" },
    { src: "3", dst: "4", rel: "yields" },
    { src: "5", dst: "4", rel: "uses" },
    { src: "5", dst: "6", rel: "yields" },
    { src: "6", dst: "2", rel: "proves" },
    { src: "1", dst: "7", rel: "derived_from" },
  ];

  const hl = collectLineageSubgraph("4", nodes, edges);
  assert.equal(hl.upstreamCount, 2); // 3, 1
  assert.equal(hl.downstreamCount, 2); // 5, 6
  assert.ok(hl.nodeIds.has("1"));
  assert.ok(hl.nodeIds.has("6"));
  assert.equal(hl.nodeIds.has("7"), false);

  const findingHl = collectLineageSubgraph("6", nodes, edges);
  assert.ok(findingHl.nodeIds.has("2"), "漏洞聚焦应顺带包含其 proves 的目标");
});

test("findMatchingNodeIds 与 extractNodeCardMeta 提取准确信号", () => {
  const nodes = [
    node(12, "finding", {
      state: "confirmed",
      finding_status: "confirmed",
      payload: JSON.stringify({ severity: "high", name: "SQL 注入" }),
    }),
    node(15, "artifact", {
      state: "active",
      payload: JSON.stringify({ kind: "webshell", summary: "落地 shell" }),
    }),
    node(18, "fact", {
      evidence_state: { flags: ["superseded"] },
      payload: JSON.stringify({ summary: "旧版本指纹" }),
    }),
  ];

  const matches = findMatchingNodeIds(nodes, "#15", () => "");
  assert.deepEqual(matches, ["15"]);

  const fMeta = extractNodeCardMeta(nodes[0]);
  assert.equal(fMeta.severity, "high");
  assert.equal(fMeta.statusDomain, "finding");

  const aMeta = extractNodeCardMeta(nodes[1]);
  assert.equal(aMeta.statusDomain, "artifact");
  assert.equal(aMeta.subTag, "Webshell");

  const oldFactMeta = extractNodeCardMeta(nodes[2]);
  assert.equal(oldFactMeta.isStale, true);
  assert.deepEqual(oldFactMeta.evidenceBadges, ["已替代"]);
});
