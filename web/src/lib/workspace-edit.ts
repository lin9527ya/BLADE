export type WorkspaceConflict = { path: string; version: string };

export class WorkspaceUploadConflict extends Error {
  readonly conflicts: WorkspaceConflict[];
  constructor(message: string, conflicts: WorkspaceConflict[]) {
    super(message);
    this.name = "WorkspaceUploadConflict";
    this.conflicts = conflicts;
  }
}

export function workspaceExpectedVersions(conflicts: WorkspaceConflict[]): Record<string, string> {
  return Object.fromEntries(conflicts.map((item) => [item.path, item.version]));
}

export function workspaceDirty(content: string, original: string | undefined): boolean {
  return content !== (original ?? "");
}
