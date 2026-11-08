import assert from "node:assert/strict";
import test from "node:test";
import { acceptTokenEvent, acceptTokenSnapshot, emptyTokenAccounting, projectTokenAccounting } from "./token-accounting.ts";

const counts = (input, cache = 0, output = 0) => ({ input_tokens: input, cache_read_tokens: cache, output_tokens: output, cache_write_tokens: 0 });
const snapshot = (cursor, input = 1000, cache = 900, live) => ({
  snapshot_cursor: cursor, total: counts(input, cache), workers: [],
  sessions: [{ session: "plan", ...counts(input, cache), last_result_seq: cursor, live }],
});
const event = (seq, kind, input = 100, cache = 0, session = "plan") => ({ seq, kind, session, ...counts(input, cache) });

test("poll includes result before SSE: usage is not counted twice", () => {
  let s = acceptTokenSnapshot(emptyTokenAccounting(), snapshot(10));
  s = acceptTokenEvent(s, event(11, "usage"));
  assert.equal(projectTokenAccounting(s).total.input_tokens, 1100);
  s = acceptTokenSnapshot(s, snapshot(12, 1100, 900));
  assert.equal(projectTokenAccounting(s).total.input_tokens, 1100);
  s = acceptTokenEvent(s, event(12, "result"));
  assert.equal(projectTokenAccounting(s).total.input_tokens, 1100);
});

test("SSE result before poll: completed usage stays counted", () => {
  let s = acceptTokenSnapshot(emptyTokenAccounting(), snapshot(10));
  s = acceptTokenEvent(s, event(11, "usage"));
  s = acceptTokenEvent(s, event(12, "result"));
  assert.deepEqual(projectTokenAccounting(s).total, counts(1100, 900));
  s = acceptTokenSnapshot(s, snapshot(12, 1100, 900));
  assert.deepEqual(projectTokenAccounting(s).total, counts(1100, 900));
});

test("refresh restores a cold session's latest cumulative usage", () => {
  const snap = snapshot(20);
  snap.sessions.push({ session: "slot:1", ...counts(0), last_result_seq: 0, live: { seq: 18, ...counts(300, 200) } });
  const s = acceptTokenSnapshot(emptyTokenAccounting(), snap);
  assert.deepEqual(projectTokenAccounting(s).total, counts(1300, 1100));
  assert.deepEqual(projectTokenAccounting(s).sessions["slot:1"], counts(300, 200));
});

test("new usage replaces cumulative usage; result resets a run", () => {
  let s = acceptTokenSnapshot(emptyTokenAccounting(), snapshot(10, 1000, 900, { seq: 9, ...counts(100) }));
  s = acceptTokenEvent(s, event(11, "usage", 150, 40));
  assert.deepEqual(projectTokenAccounting(s).total, counts(1150, 940));
  s = acceptTokenEvent(s, event(12, "result", 150, 40));
  s = acceptTokenEvent(s, event(13, "usage", 50, 20));
  assert.deepEqual(projectTokenAccounting(s).total, counts(1200, 960));
  s = acceptTokenSnapshot(s, snapshot(12, 1150, 940));
  assert.deepEqual(projectTokenAccounting(s).total, counts(1200, 960));
});

test("duplicate and out-of-order events plus stale snapshots do not regress totals", () => {
  let s = acceptTokenSnapshot(emptyTokenAccounting(), snapshot(10));
  s = acceptTokenEvent(s, event(12, "result"));
  s = acceptTokenEvent(s, event(11, "usage"));
  s = acceptTokenEvent(s, event(12, "result"));
  assert.deepEqual(projectTokenAccounting(s).total, counts(1100, 900));
  const newer = acceptTokenSnapshot(s, snapshot(12, 1100, 900));
  assert.equal(acceptTokenSnapshot(newer, snapshot(10)), newer);
});

test("events received before the first snapshot are reconciled with its cursor", () => {
  let s = acceptTokenEvent(emptyTokenAccounting(), event(11, "usage"));
  assert.equal(projectTokenAccounting(s).total, null);
  s = acceptTokenSnapshot(s, snapshot(10));
  assert.equal(projectTokenAccounting(s).total.input_tokens, 1100);
});
