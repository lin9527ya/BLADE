import { buildSessionList, isDeletedIntent, visibleWorkerIntents } from "./session-list.ts";
import assert from "node:assert/strict";
import test from "node:test";

const intentNode = (id, state) => ({
  id: String(id),
  type: "intent",
  priority: 5,
  state,
  origin: "planner",
  ts: "2026-11-26T12:00:00Z",
});

const workerSession = (id, status) => ({
  id: String(id),
  role: "worker",
  intent_id: String(id),
  title: `Worker #${id}`,
  status,
  live: status === "running",
  last_activity: "2026-11-26T12:00:00Z",
});

const fixedBase = () => ({
  mainSessions: [
    {
      id: "s-main-0",
      role: "mainagent",
      title: "主 Agent · 会话 #1",
      status: "running",
      live: true,
      last_activity: "",
      seg: 0,
    },
  ],
  plannerSession: {
    id: "s-planner",
    role: "planner",
    title: "规划 Planner · 态势研判",
    status: "running",
    live: false,
    last_activity: "",
  },
  systemSession: {
    id: "s-system",
    role: "system",
    title: "系统事件 · LLM 故障转移",
    status: "done",
    live: false,
    last_activity: "",
  },
});

test("visibleWorkerIntents filters out deleted intents and preserves other terminal states", () => {
  const intents = [
    intentNode(10, "running"),
    intentNode(9, "deleted"),
    intentNode(8, "done"),
    intentNode(7, "stopped"),
    intentNode(6, "deleted"),
    intentNode(5, "open"),
  ];
  assert.equal(isDeletedIntent(intents[1]), true);
  assert.equal(isDeletedIntent(intents[3]), false);
  assert.deepEqual(
    visibleWorkerIntents(intents).map((n) => n.id),
    ["10", "8", "7", "5"],
  );
});

test("buildSessionList hides deleted workers from left grouped.worker while preserving deep-linked active resolution", () => {
  const { mainSessions, plannerSession, systemSession } = fixedBase();
  const deepLinkedDeleted = workerSession("99", "deleted");

  const result = buildSessionList({
    mainSessions,
    plannerSession,
    workerSessions: [workerSession("101", "running"), workerSession("100", "deleted"), workerSession("98", "done")],
    systemSession,
    locatedSession: deepLinkedDeleted,
  });

  // 左侧 Workers 仅展示未删除项，不包含 #100 和深链打开的已删除 #99。
  assert.deepEqual(
    result.grouped.worker.map((s) => s.id),
    ["101", "98"],
  );
  // 右栏激活查询集 sessions 仍包含深链的 #99，以便打开播报板/审计跳转。
  assert.equal(
    result.sessions.some((s) => s.id === "99"),
    true,
  );
  assert.equal(
    result.sessions.some((s) => s.id === "100"),
    false,
  );
});

test("buildSessionList retains non-deleted archived worker deep links in left worker list", () => {
  const { mainSessions, plannerSession, systemSession } = fixedBase();
  const archivedDone = workerSession("12", "done");

  const result = buildSessionList({
    mainSessions,
    plannerSession,
    workerSessions: [workerSession("101", "running")],
    systemSession,
    locatedSession: archivedDone,
  });

  assert.deepEqual(
    result.grouped.worker.map((s) => s.id),
    ["101", "12"],
  );
});
