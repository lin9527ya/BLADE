import {
  computePercent,
  daysAgoDateKey,
  fmtBytes,
  fmtDurationSeconds,
  localDateKey,
  matchesTaskFilter,
  resolveTaskElapsedSeconds,
} from "./dashboard-format.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("localDateKey and daysAgoDateKey use local calendar date without UTC shift", () => {
  const d = new Date(2026, 10, 27, 0, 15, 0);
  assert.equal(localDateKey(d), "2026-11-27");
  assert.equal(daysAgoDateKey(3, d), "2026-11-24");
});

test("fmtDurationSeconds formats seconds, minutes, hours, and days", () => {
  assert.equal(fmtDurationSeconds(0), "—");
  assert.equal(fmtDurationSeconds(42), "42s");
  assert.equal(fmtDurationSeconds(150), "2m 30s");
  assert.equal(fmtDurationSeconds(3600), "1h");
  assert.equal(fmtDurationSeconds(3665), "1h 1m");
  assert.equal(fmtDurationSeconds(90000), "1d 1h");
});

test("fmtBytes formats memory sizes consistently", () => {
  assert.equal(fmtBytes(0), "0 B");
  assert.equal(fmtBytes(1536), "1.5 KiB");
  assert.equal(fmtBytes(68 * 1024 * 1024), "68 MiB");
});

test("computePercent clamps and avoids NaN", () => {
  assert.equal(computePercent(0, 0), 0);
  assert.equal(computePercent(25, 100), 25);
  assert.equal(computePercent(150, 100), 100);
});

test("matchesTaskFilter classifies running, paused, done, and mobile tasks", () => {
  const runningMob = {
    id: "1",
    description: "mob",
    goal: "g",
    status: "running",
    created_at: "2026-11-27",
    app_mode: "mobile",
  };
  const pausedStd = {
    id: "2",
    description: "web",
    goal: "g",
    status: "paused",
    created_at: "2026-11-27",
    app_mode: "standard",
  };
  const failedStd = {
    id: "3",
    description: "err",
    goal: "g",
    status: "failed",
    created_at: "2026-11-27",
  };
  assert.equal(matchesTaskFilter(runningMob, "all"), true);
  assert.equal(matchesTaskFilter(runningMob, "running"), true);
  assert.equal(matchesTaskFilter(runningMob, "mobile"), true);
  assert.equal(matchesTaskFilter(pausedStd, "paused"), true);
  assert.equal(matchesTaskFilter(pausedStd, "mobile"), false);
  assert.equal(matchesTaskFilter(failedStd, "done"), true);
});

test("resolveTaskElapsedSeconds sums settled and active segment runtime", () => {
  const t = {
    id: "1",
    description: "t",
    goal: "g",
    status: "running",
    created_at: "2026-11-27",
    active_run_seconds: 120,
    run_segment_started_unix: 1000,
  };
  assert.equal(resolveTaskElapsedSeconds(t, 1045), 165);
  assert.equal(resolveTaskElapsedSeconds({ ...t, run_segment_started_unix: 0 }, 1045), 120);
});
