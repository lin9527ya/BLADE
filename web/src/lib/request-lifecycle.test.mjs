import assert from "node:assert/strict";
import test from "node:test";
import {
	createLatestRequest,
	HttpError,
	retryRequest,
	runLatestRequest,
} from "./request-lifecycle.ts";

test("a superseded response cannot publish over its replacement", async () => {
	const owner = createLatestRequest();
	const published = [];
	const first = runLatestRequest(
		owner,
		() => new Promise((resolve) => setTimeout(() => resolve("slow-old"), 30)),
		{ success: (v) => published.push(v), error: () => {}, settled: () => {} },
	);
	const second = runLatestRequest(owner, async () => "fast-new", {
		success: (v) => published.push(v),
		error: () => {},
		settled: () => {},
	});
	await Promise.all([first, second]);
	assert.deepEqual(published, ["fast-new"]);
});

test("starting a request aborts the previous signal", () => {
	const owner = createLatestRequest();
	const first = owner.start();
	const second = owner.start();
	assert.equal(first.signal.aborted, true);
	assert.equal(first.alive(), false);
	assert.equal(second.alive(), true);
});

test("retryRequest retries server failures and then succeeds", async () => {
	let calls = 0;
	const value = await retryRequest(
		async () => {
			calls++;
			if (calls < 3) throw new HttpError("busy", 503);
			return "ok";
		},
		new AbortController().signal,
		{ delayMs: 1 },
	);
	assert.equal(value, "ok");
	assert.equal(calls, 3);
});

test("retryRequest does not retry client errors", async () => {
	let calls = 0;
	await assert.rejects(
		retryRequest(
			async () => {
				calls++;
				throw new HttpError("missing", 404);
			},
			new AbortController().signal,
			{ delayMs: 1 },
		),
		/missing/,
	);
	assert.equal(calls, 1);
});

test("retryRequest stops when the caller aborts", async () => {
	const controller = new AbortController();
	controller.abort(new Error("cancelled"));
	await assert.rejects(
		retryRequest(async () => "never", controller.signal, { delayMs: 1 }),
		/cancelled/,
	);
});
