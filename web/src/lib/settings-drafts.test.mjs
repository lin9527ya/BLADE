import assert from "node:assert/strict";
import test from "node:test";
import { createSettingsDrafts } from "./settings-drafts.ts";

function fixture() {
  const draft = createSettingsDrafts(),
    values = {};
  for (const [key, initial, dep] of [
    ["budget", "0", "task_token_budget"],
    ["pause", false, "task_token_budget_pause"],
  ]) {
    values[key] = initial;
    draft.register(key, initial, [dep], (value) => (values[key] = value));
  }
  const read = draft.read();
  draft.applyField("budget", "100", read);
  draft.applyField("pause", false, read);
  const apply = (ticket, budget, pause) => {
    draft.applyField("budget", budget, ticket);
    draft.applyField("pause", pause, ticket);
  };
  return { draft, values, apply };
}

test("saving one field does not discard another section's dirty input", async () => {
  const { draft, values, apply } = fixture();
  draft.edit("budget", "200");
  draft.edit("pause", true);
  await draft.submit({ task_token_budget_pause: true }, async (_, ticket) => apply(ticket, "100", true));
  assert.deepEqual(values, { budget: "200", pause: true });
  assert.equal(draft.dirty(), true);
});

test("edits during save survive while the server snapshot is acknowledged", async () => {
  const { draft, values, apply } = fixture();
  draft.edit("budget", "200");
  let resolve;
  const response = new Promise((r) => (resolve = r));
  const save = draft.submit({ task_token_budget: 200 }, async (_, ticket) => {
    await response;
    apply(ticket, "200", false);
  });
  draft.edit("budget", "300");
  resolve();
  await save;
  assert.equal(values.budget, "300");
  assert.equal(draft.dirty(), true);
  await draft.submit({ task_token_budget: 300 }, async (_, ticket) => apply(ticket, "300", false));
  assert.equal(draft.dirty(), false);
});

test("queued writes commit in intent order; a failed write does not block the next", async () => {
  const { draft, values, apply } = fixture();
  const order = [];
  let release;
  const first = new Promise((r) => (release = r));
  draft.edit("budget", "200");
  const a = draft.submit({ task_token_budget: 200 }, async (p, t) => {
    order.push(p.task_token_budget);
    await first;
    apply(t, "200", false);
  });
  draft.edit("budget", "300");
  const b = draft.submit({ task_token_budget: 300 }, async (p, t) => {
    order.push(p.task_token_budget);
    apply(t, "300", false);
  });
  await Promise.resolve();
  assert.deepEqual(order, [200]);
  release();
  await Promise.all([a, b]);
  assert.deepEqual(order, [200, 300]);
  assert.equal(values.budget, "300");
  await assert.rejects(
    draft.submit({ task_token_budget: 400 }, async () => {
      throw Error("offline");
    }),
    /offline/,
  );
  await draft.submit({ task_token_budget: 500 }, async (_, t) => apply(t, "500", false));
  assert.equal(values.budget, "500");
});

test("an old read and a read started during a pending write cannot revert an ack", async () => {
  const { draft, values, apply } = fixture();
  const old = draft.read();
  draft.edit("budget", "200");
  let release;
  const response = new Promise((r) => (release = r));
  const save = draft.submit({ task_token_budget: 200 }, async (_, t) => {
    await response;
    apply(t, "200", false);
  });
  const during = draft.read();
  release();
  await save;
  draft.applyField("budget", "100", old);
  draft.applyField("budget", "100", during);
  assert.equal(values.budget, "200");
  assert.equal(draft.dirty(), false);
});

test("write-only keys clear only the submitted draft, never newer input", async () => {
  const draft = createSettingsDrafts();
  let value = "";
  draft.register("key", "", ["api_key"], (v) => (value = v), true);
  draft.edit("key", "first");
  let release;
  const response = new Promise((r) => (release = r));
  const save = draft.submit({ api_key: "first" }, async () => response);
  draft.edit("key", "second");
  release();
  await save;
  assert.equal(value, "second");
  await draft.submit({ api_key: "" }, async () => {});
  assert.equal(value, "second");
  await draft.submit({ api_key: "second" }, async () => {});
  assert.equal(value, "");
  assert.equal(draft.dirty(), false);
});
