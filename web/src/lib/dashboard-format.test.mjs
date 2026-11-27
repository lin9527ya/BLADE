import {
  buildDailyUsageSeries,
  buildUsageByProfileMap,
  collectExtraUsageProfiles,
  computePercent,
  daysAgoDateKey,
  fmtBytes,
  fmtDurationSeconds,
  localDateKey,
  matchesTaskFilter,
  profileTabKey,
  resolveTaskElapsedSeconds,
  selectUsageProfileBucket,
  summarizeAllUsageTokens,
  summarizeDailyUsage,
  TOKEN_TAB_ALL,
  utcDateKey,
  utcUsageWindowStartKey,
} from "./dashboard-format.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("localDateKey and daysAgoDateKey use local calendar date without UTC shift", () => {
  const d = new Date(2026, 10, 27, 0, 15, 0);
  assert.equal(localDateKey(d), "2026-11-27");
  assert.equal(daysAgoDateKey(3, d), "2026-11-24");
});

test("utcDateKey and utcUsageWindowStartKey match complete UTC calendar day windows", () => {
  const d = new Date("2026-11-27T16:30:00Z");
  assert.equal(utcDateKey(d), "2026-11-27");
  assert.equal(utcUsageWindowStartKey(7, d), "2026-11-21");
  assert.equal(utcUsageWindowStartKey(1, d), "2026-11-27");
  assert.equal(utcUsageWindowStartKey(3650, d), "");
});

test("llm_usage profile attribution does not fold deleted or unattributed profiles into default", () => {
  const usageStats = {
    by_profile: [
      {
        profile_name: "Claude Opus",
        calls: 10,
        tasks: 2,
        input_tokens: 1000,
        output_tokens: 200,
        cache_read_tokens: 800,
        cache_write_tokens: 50,
      },
      {
        profile_name: "Old Model",
        calls: 5,
        tasks: 1,
        input_tokens: 500,
        output_tokens: 100,
        cache_read_tokens: 100,
        cache_write_tokens: 10,
      },
      {
        profile_name: "",
        calls: 3,
        tasks: 0,
        input_tokens: 300,
        output_tokens: 30,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
      },
    ],
    daily: [
      {
        profile_name: "Claude Opus",
        date: "2026-11-26",
        input_tokens: 1000,
        output_tokens: 200,
        cache_read_tokens: 800,
      },
      { profile_name: "Old Model", date: "2026-11-27", input_tokens: 500, output_tokens: 100, cache_read_tokens: 100 },
      { profile_name: "", date: "2026-11-27", input_tokens: 300, output_tokens: 30, cache_read_tokens: 0 },
      { profile_name: "Claude Opus", date: "2026-11-19", input_tokens: 999, output_tokens: 99, cache_read_tokens: 0 },
    ],
    summary: {
      calls: 18,
      tasks: 2,
      input_tokens: 1800,
      output_tokens: 330,
      cache_read_tokens: 900,
      cache_write_tokens: 60,
    },
  };

  const map = buildUsageByProfileMap(usageStats);
  const all = summarizeAllUsageTokens(usageStats);
  assert.equal(all.calls, 18);
  assert.equal(all.taskCount, 2); // uses summary.tasks without double-counting 2+1+0=3
  assert.equal(all.input, 1800);
  assert.equal(all.output, 330);
  assert.equal(all.cacheWrite, 60);

  // Strict per-profile isolation: Claude Opus MUST NOT absorb Old Model or ""!
  const opus = selectUsageProfileBucket(map, usageStats, profileTabKey("Claude Opus"));
  assert.equal(opus.input, 1000);
  assert.equal(opus.calls, 10);

  const oldBucket = selectUsageProfileBucket(map, usageStats, profileTabKey("Old Model"));
  assert.equal(oldBucket.input, 500);

  const unattributed = selectUsageProfileBucket(map, usageStats, profileTabKey(""));
  assert.equal(unattributed.input, 300);

  const extras = collectExtraUsageProfiles(usageStats, [{ name: "Claude Opus" }]);
  assert.deepEqual(
    extras.map((e) => ({ name: e.profileName, label: e.label, unattr: e.isUnattributed })),
    [
      { name: "Old Model", label: "Old Model", unattr: false },
      { name: "", label: "未归属/默认环境", unattr: true },
    ],
  );

  // 7-day UTC window on 2026-11-27 covers 2026-11-21..2026-11-27, excluding 2026-11-19.
  const now = new Date("2026-11-27T02:00:00Z");
  const allSeries = buildDailyUsageSeries(usageStats, TOKEN_TAB_ALL, 7, now);
  assert.deepEqual(allSeries, [
    { fullDate: "2026-11-26", date: "11-26", input: 1000, miss: 200, output: 200, cacheRead: 800 },
    { fullDate: "2026-11-27", date: "11-27", input: 800, miss: 700, output: 130, cacheRead: 100 },
  ]);
  assert.deepEqual(summarizeDailyUsage(allSeries), {
    input: 1800,
    miss: 900,
    output: 330,
    cacheRead: 900,
  });

  const opusSeries = buildDailyUsageSeries(usageStats, profileTabKey("Claude Opus"), 7, now);
  assert.deepEqual(opusSeries, [
    { fullDate: "2026-11-26", date: "11-26", input: 1000, miss: 200, output: 200, cacheRead: 800 },
  ]);

  // Empty configured profile edge case ("all" literal name wouldn't collide with TOKEN_TAB_ALL).
  assert.notEqual(profileTabKey("all"), TOKEN_TAB_ALL);
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
