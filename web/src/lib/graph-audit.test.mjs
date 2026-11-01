import { graphAuditHeadline, graphAuditIssueLabel, graphAuditRefs, graphAuditSummary } from "./graph-audit.ts";
import assert from "node:assert/strict";
import test from "node:test";

const report = (issues, counts = {}) => ({
  exploration_id: 1,
  checked_at: "2026-11-01T00:00:00Z",
  counts,
  issues,
});

test("known codes get a Chinese label; unknown codes pass through", () => {
  assert.equal(graphAuditIssueLabel("dependency_cycle"), "依赖成环");
  assert.equal(graphAuditIssueLabel("future_code"), "future_code");
});

test("summary splits errors and warnings and reports clean", () => {
  assert.deepEqual(graphAuditSummary(report([])), { errors: 0, warnings: 0, clean: true });
  assert.deepEqual(graphAuditSummary(null), { errors: 0, warnings: 0, clean: true });
  const s = graphAuditSummary(
    report([
      { code: "dependency_cycle", severity: "error" },
      { code: "expired_running_lease", severity: "warn" },
      { code: "expired_running_lease", severity: "warn" },
    ]),
  );
  assert.deepEqual(s, { errors: 1, warnings: 2, clean: false });
});

test("headline uses counts (totals) and sorts by count then code", () => {
  const headline = graphAuditHeadline(
    report([], { expired_running_lease: 1, dependency_cycle: 2, digest_covers_deleted: 0 }),
  );
  assert.equal(headline, "3 个问题：依赖成环 ×2 · 意图租约已过期 ×1");
  // 超过上限时用省略号，而不是把整张表铺在标题上。
  const many = graphAuditHeadline(
    report([], { a_special: 1, b: 1, c: 1, d: 1 }),
    2,
  );
  assert.ok(many.endsWith(" …"));
  assert.equal(graphAuditHeadline(report([], {})), "");
});

test("refs merge node_ids with from/to and dedupe", () => {
  assert.deepEqual(graphAuditRefs({ node_ids: [7, 8, 7], from: 8, to: 9 }), ["#7", "#8", "#9"]);
  assert.deepEqual(graphAuditRefs({}), []);
});
