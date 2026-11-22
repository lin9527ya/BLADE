import test from "node:test";
import assert from "node:assert/strict";
import { trackingDuration, schedulingScore, scheduleWaitLabel, LEAD_TRACE_LABELS, LEAD_TRACE_PAGE_SIZE } from "./exploration-tracking.ts";

test("unknown historical latency is not rendered as zero", () => {
  assert.equal(trackingDuration(undefined), "未知");
  assert.equal(trackingDuration(-1), "未知");
  assert.equal(trackingDuration(0), "0.0 秒");
});
test("blocked and unevaluated candidates do not masquerade as zero score", () => {
  assert.equal(schedulingScore({ evaluated: false, score: 0 }), "未评分");
  assert.equal(schedulingScore({ evaluated: true, score: 0 }), "0.0");
  assert.match(scheduleWaitLabel("strict_dependency"), /strict/);
});
test("completed branch means associated execution ended, not proved vulnerability", () => {
  assert.equal(LEAD_TRACE_LABELS.completed, "关联执行结束");
  assert.match(LEAD_TRACE_LABELS.needs_follow_up, /跟进/);
  assert.match(LEAD_TRACE_LABELS.source_changed, /复核/);
});

test("lead trace pagination uses a compact five-row default", () => {
  assert.equal(LEAD_TRACE_PAGE_SIZE, 5);
});
