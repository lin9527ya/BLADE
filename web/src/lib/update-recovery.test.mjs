import assert from "node:assert/strict";
import test from "node:test";
import { abortableDelay, HttpError } from "./request-lifecycle.ts";
import { waitForVersion } from "./update-recovery.ts";

const signal = () => new AbortController().signal;
const noWait = async () => {};

test("an old version during restart is not mistaken for success", async () => {
	const seen = ["v1.0.0", "v1.0.0", "v2.0.0"];
	const version = await waitForVersion({
		from: "v1.0.0",
		target: "v2.0.0",
		signal: signal(),
		probe: async () => seen.shift() ?? null,
		wait: noWait,
		terminalError: () => false,
		timeoutMs: 1000,
	});
	assert.equal(version, "v2.0.0");
});

test("a rollback to a different version fails loudly", async () => {
	await assert.rejects(
		waitForVersion({
			from: "v1.0.0",
			target: "v2.0.0",
			signal: signal(),
			probe: async () => "v3.0.0",
			wait: noWait,
			terminalError: () => false,
			timeoutMs: 1000,
		}),
		/未安装预期版本/,
	);
});

test("authentication failures are terminal, not retried as restarts", async () => {
	await assert.rejects(
		waitForVersion({
			from: "v1.0.0",
			target: "v2.0.0",
			signal: signal(),
			probe: async () => {
				throw new HttpError("未授权", 401);
			},
			wait: noWait,
			terminalError: (error) =>
				error instanceof HttpError && error.status === 401,
			timeoutMs: 1000,
		}),
		/未授权/,
	);
});

test("waiting is bounded by the restart timeout", async () => {
	await assert.rejects(
		waitForVersion({
			from: "v1.0.0",
			signal: signal(),
			probe: async () => "v1.0.0",
			wait: abortableDelay,
			terminalError: () => false,
			timeoutMs: 25,
			intervalMs: 5,
		}),
		/超时/,
	);
});
