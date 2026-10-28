import assert from "node:assert/strict";
import test from "node:test";
import { COLD_TAIL_KEEP, capSessionStore } from "./session-cache.ts";

const activity = (seq, kind = "tool_use") => ({ seq, kind, ts: String(seq) });
const session = (count, lastTs, extra = {}) => ({
	items: Array.from({ length: count }, (_, i) =>
		activity(i + 1, i === count - 1 ? "result" : "tool_use"),
	),
	loaded: true,
	loading: false,
	loadingMore: false,
	hasMore: false,
	earliestSeq: 1,
	firstTs: "1",
	lastTs,
	unread: 3,
	...extra,
});

test("under budget leaves the store untouched", () => {
	const store = { main: session(5, "2026-01-01") };
	assert.equal(capSessionStore(store, "main", 10), store);
});

test("oldest cold sessions are reduced to an accounting tail", () => {
	const store = {
		main: session(10, "2026-03-01"),
		plan: session(10, "2026-02-01"),
		"slot:1": session(10, "2026-01-01"),
	};
	const next = capSessionStore(store, "main", 15);
	assert.equal(next.main, store.main); // active session is never touched
	assert.equal(next.plan.items.length, 1);
	assert.equal(next["slot:1"].items.length, 1);
	assert.equal(next["slot:1"].items[0].kind, "result");
	assert.equal(next["slot:1"].loaded, false);
	assert.equal(next["slot:1"].hasMore, true);
	assert.equal(next["slot:1"].unread, 3);
});

test("in-flight loads are not evicted, and the tail is bounded", () => {
	const store = {
		main: session(10, "2026-03-01"),
		loading: session(50, "2025-01-01", { loading: true }),
		cold: session(50, "2026-01-01"),
	};
	const next = capSessionStore(store, "main", 20);
	assert.equal(next.loading, store.loading);
	assert.ok(next.cold.items.length <= COLD_TAIL_KEEP);
});

test("returns the same object when no candidate can shrink", () => {
	const store = {
		main: session(10, "2026-03-01"),
		cold: session(3, "2026-01-01"),
	};
	assert.equal(capSessionStore(store, "main", 5), store);
});
