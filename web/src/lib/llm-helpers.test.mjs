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

import {
  estimateWorstCaseRetry,
  formatBreakerPolicySummary,
  validateRetryRule,
} from "../app/(main)/system/llm/_components/llm-helpers.ts";

test("formatBreakerPolicySummary reflects configured threshold and cooldown ladder", () => {
  assert.match(
    formatBreakerPolicySummary({ soft_trip: 3, cooldown_secs: [60, 300, 1800], fixed_cooldown: false }),
    /连续瞬时失败 3 次.*1min → 5min → 30min/,
  );
  assert.match(
    formatBreakerPolicySummary({ soft_trip: 2, cooldown_secs: [90], fixed_cooldown: true }),
    /连续瞬时失败 2 次.*固定冷却 2min/,
  );
  assert.match(formatBreakerPolicySummary({ soft_trip: 0, cooldown_secs: [60, 300, 1800] }), /瞬时失败不软熔断/);
});

test("validateRetryRule flags out-of-range attempts and interval", () => {
  assert.deepEqual(validateRetryRule({ attempts: 0, interval_ms: 0 }), {});
  assert.deepEqual(validateRetryRule({ attempts: -1, interval_ms: 3600000 }), {});
  const bad = validateRetryRule({ attempts: 25, interval_ms: -5 });
  assert.ok(bad.attemptsError);
  assert.ok(bad.intervalError);
});

test("estimateWorstCaseRetry computes non-multiplying pool upper bound", () => {
  const est = estimateWorstCaseRetry(
    {
      connect: { attempts: 0, interval_ms: 0 },
      stream: { attempts: 0, interval_ms: 0 },
      intent: { attempts: -1, interval_ms: 0 },
    },
    3,
  );
  assert.equal(est.perMember, 12); // (1+3)*(1+2)
  assert.equal(est.perLogicalCall, 36); // 12 * 3 members (Stream not swept over Pool)
  assert.equal(est.totalWithIntent, 36);
});
