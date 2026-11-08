import assert from "node:assert/strict";
import test from "node:test";
import { createSessionRequests } from "./request-lifecycle.ts";

test("switch A to B (and back to A) invalidates an old request with the same session key", () => {
  const scope = createSessionRequests();
  const a = scope.start("earlier:plan");
  scope.reset();
  const b = scope.start("earlier:plan");
  assert.equal(a.alive(), false);
  assert.equal(a.signal.aborted, true);
  assert.equal(b.alive(), true);
  scope.reset();
  const a2 = scope.start("earlier:plan");
  assert.equal(a.alive(), false);
  assert.equal(b.alive(), false);
  assert.equal(a2.alive(), true);
});

test("latest and older requests are independent but a new request for the same page supersedes the old", () => {
  const scope = createSessionRequests();
  const latest = scope.start("main:0");
  const older = scope.start("earlier:main:0");
  const replacement = scope.start("earlier:main:0");
  assert.equal(latest.alive(), true);
  assert.equal(older.alive(), false);
  assert.equal(replacement.alive(), true);
});

test("late success, failure and scroll callbacks cannot mutate a replacement task", async () => {
  const scope = createSessionRequests();
  const request = scope.start("earlier:main:0");
  let resolve;
  const response = new Promise((r) => { resolve = r; });
  const writes = [];
  const pending = response.then(() => { if (request.alive()) writes.push("old page"); });
  scope.reset();
  scope.start("earlier:main:0");
  resolve();
  await pending;
  if (request.alive()) writes.push("old error", "old scroll");
  assert.deepEqual(writes, []);
});
