// findings.status 中代表「漏洞已确认存在」的集合。report_finding 默认写 pending，
// 候选闭合写 ruled_out/open_proof_gap；这些都不是可展示的漏洞。
const CONFIRMED_FINDING_STATUSES = new Set(["confirmed", "resolved", "fixed", "risk_accepted"]);

export function findingConfirmedForDisplay(status: string | undefined): boolean {
  return !!status && CONFIRMED_FINDING_STATUSES.has(status);
}

// 候选闭合不是漏洞：ruled_out（已排除）/ open_proof_gap（存疑待补证）只留在
// 报告与审计里，不进入发现页 / 案例成员列表。
export function findingCandidateStatus(status: string | undefined): boolean {
  return (
    status === "ruled_out" ||
    status === "open_proof_gap" ||
    status === "false_positive" ||
    status === "duplicate" ||
    status === "retracted" ||
    status === "ignored"
  );
}
