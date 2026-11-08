import assert from "node:assert/strict";
import test from "node:test";
import { workspaceDirty, workspaceExpectedVersions, WorkspaceUploadConflict } from "./workspace-edit.ts";
test("workspace editing compares content against the saved baseline", () => {
  assert.equal(workspaceDirty("a", "a"), false);
  assert.equal(workspaceDirty("changed", "a"), true);
  assert.equal(workspaceDirty("", undefined), false);
});
test("overwrite approval carries each server-observed version", () => {
  assert.deepEqual(workspaceExpectedVersions([{path:"d/a",version:"sha256:old"}]), {"d/a":"sha256:old"});
  assert.deepEqual(workspaceExpectedVersions([]), {});
  const error = new WorkspaceUploadConflict("changed", [{path:"a",version:"new"}]);
  assert.equal(error.conflicts[0].version,"new");
});
