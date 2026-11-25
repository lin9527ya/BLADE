import { workerOriginHref, workerOriginIntentId } from "./broadcast-format.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("workerOriginIntentId extracts global intent ID from worker#<id> origins", () => {
  assert.equal(workerOriginIntentId("worker#9112"), "9112");
  assert.equal(workerOriginIntentId("  worker#42 "), "42");
  for (const origin of [undefined, "", "system", "planner", "worker#0", "worker#-1", "worker#abc", "work#12"]) {
    assert.equal(workerOriginIntentId(origin), undefined, String(origin));
  }
});

test("workerOriginHref builds session deep link unless inherited or missing taskId", () => {
  assert.equal(
    workerOriginHref("108", "worker#9112"),
    "/function/tasks/detail?id=108&tab=sessions&session=intent%3A9112",
  );
  assert.equal(
    workerOriginHref("108", "worker#9112", false),
    "/function/tasks/detail?id=108&tab=sessions&session=intent%3A9112",
  );
  assert.equal(workerOriginHref("108", "worker#9112", true), undefined);
  assert.equal(workerOriginHref("", "worker#9112"), undefined);
  assert.equal(workerOriginHref("108", "planner"), undefined);
});
