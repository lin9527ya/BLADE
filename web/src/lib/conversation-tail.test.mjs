import assert from "node:assert/strict";
import test from "node:test";
import { pollConversationTail } from "./conversation-tail.ts";

const page = (cursor, running, hasMore) => ({ cursor, running, hasMore });

test("an ended turn drains every cursor page before stopping", async () => {
	let cursor = 0;
	const pages = [page(2, false, true), page(4, false, false)];
	const received = [];
	await new Promise((resolve, reject) => {
		pollConversationTail({
			cursor: () => cursor,
			fetchPage: async (before) => {
				assert.equal(before, cursor);
				return pages.shift();
			},
			receive: (p) => {
				cursor = p.cursor;
				received.push(p.cursor);
			},
			drained: resolve,
			failed: reject,
			intervalMs: 0,
		});
	});
	assert.deepEqual(received, [2, 4]);
});

test("a cursor that does not advance fails instead of hot-looping", async () => {
	let calls = 0;
	const error = await new Promise((resolve, reject) => {
		pollConversationTail({
			cursor: () => 7,
			fetchPage: async () => {
				calls++;
				return page(7, false, true);
			},
			receive: () => {},
			drained: () => reject(new Error("must not drain")),
			failed: resolve,
			intervalMs: 0,
		});
	});
	assert.match(String(error), /游标/);
	assert.equal(calls, 1);
});

test("transport errors are retried a bounded number of times", async () => {
	let calls = 0;
	const error = await new Promise((resolve, reject) => {
		pollConversationTail({
			cursor: () => 0,
			fetchPage: async () => {
				calls++;
				throw new Error("network");
			},
			receive: () => {},
			drained: () => reject(new Error("must not drain")),
			failed: resolve,
			intervalMs: 0,
			maxConsecutiveErrors: 2,
		});
	});
	assert.match(String(error), /network/);
	assert.equal(calls, 2);
});

test("aborting stops callbacks and outstanding work", async () => {
	let resolveFetch;
	let callbacks = 0;
	const stop = pollConversationTail({
		cursor: () => 0,
		fetchPage: () => new Promise((resolve) => (resolveFetch = resolve)),
		receive: () => callbacks++,
		drained: () => callbacks++,
		failed: () => callbacks++,
		intervalMs: 0,
	});
	stop();
	resolveFetch(page(1, false, false));
	await new Promise((resolve) => setTimeout(resolve, 10));
	assert.equal(callbacks, 0);
});
