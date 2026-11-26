import {
  effortLevelsForFormat,
  formatThinkingSummary,
  NONE,
} from "../app/(main)/system/llm/_components/llm-helpers.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("effortLevelsForFormat exposes max across anthropic, openai, and openai-responses", () => {
  for (const format of ["anthropic", "openai", "openai-responses"]) {
    const values = effortLevelsForFormat(format).map((o) => o.value);
    assert.equal(values.includes("max"), true, `${format} should offer max reasoning effort`);
    assert.equal(values[0], NONE);
  }
  const openaiValues = effortLevelsForFormat("openai").map((o) => o.value);
  assert.equal(openaiValues.includes("xhigh"), true);
  assert.equal(openaiValues.includes("minimal"), true);
  const anthValues = effortLevelsForFormat("anthropic").map((o) => o.value);
  assert.equal(anthValues.includes("xhigh"), false);
});

test("effortLevelsForFormat preserves unknown legacy values cleanly", () => {
  const values = effortLevelsForFormat("anthropic", "xhigh");
  assert.deepEqual(values.at(-1), { value: "xhigh", label: "xhigh（旧值）" });
});

test("formatThinkingSummary formats max effort cleanly", () => {
  assert.equal(formatThinkingSummary({ thinking_type: "enabled", reasoning_effort: "max" }), "思考 开 · max");
  assert.equal(formatThinkingSummary({ reasoning_effort: "max" }), "思考强度 max");
});
