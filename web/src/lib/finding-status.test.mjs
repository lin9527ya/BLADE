import assert from "node:assert/strict";
import test from "node:test";
import { findingCandidateStatus, findingVisibleInChain } from "./finding-status.ts";

test("pending/confirmed findings are displayable by default", () => {
  for (const status of ["pending", "in_progress", "confirmed", "resolved", "fixed", "risk_accepted", undefined]) {
    assert.equal(findingCandidateStatus(status), false, String(status));
    assert.equal(findingVisibleInChain(status, true), true, String(status));
  }
});

test("candidate/closed findings are hidden only when the chain filter is on", () => {
  for (const status of ["ruled_out", "open_proof_gap", "false_positive", "duplicate", "retracted", "ignored"]) {
    assert.equal(findingCandidateStatus(status), true, status);
    assert.equal(findingVisibleInChain(status, false), true, status);
    assert.equal(findingVisibleInChain(status, true), false, status);
  }
});
