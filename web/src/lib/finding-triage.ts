import type { FindingStatus } from "./types";

export type FindingTriageFields = {
  status: FindingStatus;
  confidence: string;
  counterevidence?: string;
  proof_gap?: string;
  retract_reason?: string;
};

export function triageNoteLabel(status: FindingStatus): string | null {
  if (status === "ruled_out") return "反证（点名阻断该候选的控制点）";
  if (status === "open_proof_gap") return "补证缺口（说明还缺什么信息）";
  if (status === "retracted") return "撤回原因";
  return null;
}

export function buildFindingTriage(status: FindingStatus, confidence: string, note: string): FindingTriageFields {
  if (!["", "high", "medium", "low"].includes(confidence)) throw new Error("请选择有效的置信度");
  const label = triageNoteLabel(status);
  if (label && !note.trim()) throw new Error(`请填写${label}`);
  const fields: FindingTriageFields = { status, confidence };
  if (status === "ruled_out") fields.counterevidence = note.trim();
  if (status === "open_proof_gap") fields.proof_gap = note.trim();
  if (status === "retracted") fields.retract_reason = note.trim();
  return fields;
}
