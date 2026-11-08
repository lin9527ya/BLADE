import assert from "node:assert/strict";
import test from "node:test";
import { buildFindingTriage, triageNoteLabel } from "./finding-triage.ts";

test("triage requires the backend-specific note and trims it", () => {
  for (const [status, field] of [["ruled_out", "counterevidence"], ["open_proof_gap", "proof_gap"], ["retracted", "retract_reason"]]) {
    assert.ok(triageNoteLabel(status));
    assert.throws(() => buildFindingTriage(status, "high", "  "));
    assert.deepEqual(buildFindingTriage(status, "low", "  observed reason  "), { status, confidence: "low", [field]: "observed reason" });
  }
});
test("ordinary status edits do not smuggle an unrelated note", () => {
  assert.deepEqual(buildFindingTriage("confirmed", "", "unrelated"), { status: "confirmed", confidence: "" });
  assert.throws(() => buildFindingTriage("confirmed", "invalid", ""));
});
