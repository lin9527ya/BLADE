import assert from "node:assert/strict";
import test from "node:test";
import { editScheduleSpec, applyScheduleTemplate } from "./schedule-spec.ts";

test("simple schedule edits preserve complete hidden specifications", () => {
  const original = {
    description: "d",
    goal: "g",
    category_id: 1,
    company_ids: [2],
    llm_profile_ids: [3, 4],
    timeout_seconds: 20,
    asset_scope_policy: "exact",
    coverage_enabled: false,
    seed_first_intent: false,
    intercept_rules: [{ pattern: "fixture", action: "deny" }],
    future_field: "keep",
  };
  const result = editScheduleSpec(original, " d ", " g ", 1);
  assert.deepEqual(result, original);
  assert.deepEqual(editScheduleSpec(original, "new", "g", undefined), {
    ...original,
    description: "new",
    category_id: null,
  });
  assert.deepEqual(
    editScheduleSpec({ ...original, llm_profile_ids: [], intercept_rules: [] }, "d", "g", 1).intercept_rules,
    [],
  );
});

test("template rules are copied as a snapshot without losing unrelated fields", () => {
  const template = {
    id: 1,
    name: "fixture",
    description: "d",
    goal: "g",
    intercept_rules: [{ pattern: "fixture", action: "deny" }],
  };
  const result = applyScheduleTemplate({ asset_scope_policy: "exact", coverage_enabled: false }, template);
  template.intercept_rules[0].pattern = "changed later";
  assert.equal(result.intercept_rules[0].pattern, "fixture");
  assert.equal(result.asset_scope_policy, "exact");
  assert.deepEqual(applyScheduleTemplate(result, { ...template, intercept_rules: undefined }).intercept_rules, []);
});
