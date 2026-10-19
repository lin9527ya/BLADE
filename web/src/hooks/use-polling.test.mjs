import { nextDelay, POLL_MAX_BACKOFF } from "./use-polling.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("成功后回到基础间隔", () => {
  assert.equal(nextDelay(5000, 5000, true), 5000);
  // 退避过一轮之后，一次成功就该清零，而不是继续按退避后的值走
  assert.equal(nextDelay(5000, 40000, true), 5000);
});

test("失败按 2× 翻倍退避", () => {
  assert.equal(nextDelay(5000, 5000, false), 10000);
  assert.equal(nextDelay(5000, 10000, false), 20000);
});

test("退避封顶在 POLL_MAX_BACKOFF 倍，不会无限增长", () => {
  assert.equal(nextDelay(5000, 40000, false), 5000 * POLL_MAX_BACKOFF);
  assert.equal(nextDelay(5000, 5000 * POLL_MAX_BACKOFF, false), 5000 * POLL_MAX_BACKOFF);
  // 上限之下不该被提前截断
  assert.equal(nextDelay(3000, 12000, false), 24000);
});
