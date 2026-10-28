import assert from "node:assert/strict";
import test from "node:test";
import { remainingSelection } from "./batch-selection.ts";

test("acknowledged successes leave the selection; failures stay retryable", () => {
	const next = remainingSelection(new Set(["a", "b", "c"]), [
		{ id: "a", ok: true },
		{ id: "b", ok: false },
	]);
	assert.deepEqual([...next], ["b", "c"]);
});

test("items selected while the batch is in flight are never dropped", () => {
	const selectedWhileInFlight = new Set(["a", "page2", "later"]);
	const next = remainingSelection(selectedWhileInFlight, [
		{ id: "a", ok: true },
	]);
	assert.deepEqual([...next], ["page2", "later"]);
});

test("empty results leave every selection untouched", () => {
	const selected = new Set(["a", "b"]);
	assert.deepEqual([...remainingSelection(selected, [])], ["a", "b"]);
});
