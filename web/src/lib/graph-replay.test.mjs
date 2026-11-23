import { buildReplayPlan, sliceGraphForReplay } from "./graph-replay.ts";
import assert from "node:assert/strict";
import test from "node:test";

const node = (id, type, ts, extra = {}) => ({
  id: String(id),
  type,
  priority: 5,
  state: "done",
  origin: "planner",
  ts,
  ...extra,
});

test("buildReplayPlan pins begin/inherited nodes in baseIds and sorts steps chronologically with numeric id tie-break", () => {
  const nodes = [
    node(12, "fact", "2026-11-23T10:02:00Z"),
    node(2, "intent", "2026-11-23T10:00:00Z"),
    node(10, "intent", "2026-11-23T10:00:00Z"),
    node(1, "begin", "2026-11-23T09:59:00Z"),
    node(99, "fact", "2026-11-23T09:00:00Z", { inherited: true }),
    node(15, "finding", "invalid-ts"),
  ];

  const plan = buildReplayPlan(nodes);
  assert.deepEqual(plan.baseIds, ["1", "99"]);
  assert.deepEqual(plan.stepIds, ["2", "10", "12", "15"]);
});

test("sliceGraphForReplay reveals nodes progressively, filters edges to revealed endpoints, and clamps bounds", () => {
  const nodes = [
    node(1, "begin", "2026-11-23T09:59:00Z"),
    node(2, "intent", "2026-11-23T10:00:00Z"),
    node(3, "fact", "2026-11-23T10:01:00Z"),
    node(4, "finding", "2026-11-23T10:02:00Z"),
  ];
  const edges = [
    { src: "1", dst: "2", rel: "spawns" },
    { src: "2", dst: "3", rel: "yields" },
    { src: "3", dst: "4", rel: "proves" },
  ];

  const plan = buildReplayPlan(nodes);

  const step0 = sliceGraphForReplay(nodes, edges, plan, 0);
  assert.deepEqual(
    step0.visibleNodes.map((n) => n.id),
    ["1"],
  );
  assert.deepEqual(step0.visibleEdges, []);
  assert.equal(step0.latestId, null);

  const step2 = sliceGraphForReplay(nodes, edges, plan, 2);
  assert.deepEqual(
    step2.visibleNodes.map((n) => n.id),
    ["1", "2", "3"],
  );
  assert.deepEqual(
    step2.visibleEdges.map((e) => `${e.src}->${e.dst}`),
    ["1->2", "2->3"],
  );
  assert.equal(step2.latestId, "3");
  assert.equal(step2.clampedCount, 2);
  assert.equal(step2.totalSteps, 3);

  const over = sliceGraphForReplay(nodes, edges, plan, 99);
  assert.equal(over.clampedCount, 3);
  assert.equal(over.latestId, "4");
  assert.equal(over.visibleEdges.length, 3);
});
