import { cacheHitRate, fmtCostUSD } from "./token-stats.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("缓存命中率保留一位小数", () => {
  assert.equal(cacheHitRate(933, 1000), "93.3%");
  assert.equal(cacheHitRate(1, 3), "33.3%");
});

test("没有输入时不伪造命中率", () => {
  assert.equal(cacheHitRate(0, 0), "—");
  assert.equal(cacheHitRate(100, 0), "—");
});

test("全额命中显示 100.0%", () => {
  assert.equal(cacheHitRate(500, 500), "100.0%");
});

test("无效或不可能的缓存数据不伪造百分比", () => {
  assert.equal(cacheHitRate(1, NaN), "—");
  assert.equal(cacheHitRate(Infinity, 100), "—");
  assert.equal(cacheHitRate(-1, 100), "—");
  assert.equal(cacheHitRate(101, 100), "数据异常");
});

test("LLM 成本格式化区分未定价、微小金额与常规金额", () => {
  assert.equal(fmtCostUSD(0, false), "未定价");
  assert.equal(fmtCostUSD(1.5, false), "未定价");
  assert.equal(fmtCostUSD(0, true), "$0.00");
  assert.equal(fmtCostUSD(0.00004, true), "<$0.0001");
  assert.equal(fmtCostUSD(0.0042, true), "$0.0042");
  assert.equal(fmtCostUSD(1.236, true), "$1.24");
  assert.equal(fmtCostUSD(128.4, true), "$128");
  assert.equal(fmtCostUSD(NaN, true), "—");
});
