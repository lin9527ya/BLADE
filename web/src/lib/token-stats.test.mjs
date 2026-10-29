import assert from "node:assert/strict";
import test from "node:test";
import { cacheHitRate } from "./token-stats.ts";

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
