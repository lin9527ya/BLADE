import test from "node:test";
import assert from "node:assert/strict";
import { canonicalTaskID, failedTaskRead } from "./task-read-state.ts";
import { createLatestRequest, runLatestRequest, HttpError } from "./request-lifecycle.ts";

test("only a confirmed 404 is not-found; network and server errors retain proper state", () => {
  assert.equal(failedTaskRead(new HttpError("offline", 503), false), "error");
  assert.equal(failedTaskRead(new Error("offline"), true), "stale");
  assert.equal(failedTaskRead(new HttpError("missing", 404), true), "notFound");
  assert.equal(canonicalTaskID("0007"), "7");
  assert.equal(canonicalTaskID("9223372036854775807"), "9223372036854775807");
});

test("an accepted SSE snapshot cancels prior HTTP ownership", async () => {
  const owner = createLatestRequest(),
    published = [];
  let release;
  const response = new Promise((r) => (release = r));
  const request = runLatestRequest(owner, () => response, {
    success: (v) => published.push(v),
    error: () => {},
    settled: () => {},
  });
  owner.cancel();
  published.push("sse-current");
  release("http-old");
  await request;
  assert.deepEqual(published, ["sse-current"]);
});

test("remounted A/B/A resource owners cannot publish a departed task", async () => {
  const firstA = createLatestRequest(),
    secondA = createLatestRequest();
  const result = [];
  let release;
  const request = runLatestRequest(firstA, () => new Promise((r) => (release = r)), {
    success: (v) => result.push(v),
    error: () => {},
    settled: () => {},
  });
  firstA.cancel();
  await runLatestRequest(secondA, async () => "new-A", {
    success: (v) => result.push(v),
    error: () => {},
    settled: () => {},
  });
  release("old-A");
  await request;
  assert.deepEqual(result, ["new-A"]);
});
