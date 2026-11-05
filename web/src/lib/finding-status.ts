// findings.status 中代表「漏洞已确认存在」的集合。report_finding 默认写 pending，
// 候选闭合写 ruled_out/open_proof_gap；这些都不是可展示的漏洞。
const CONFIRMED_FINDING_STATUSES = new Set(["confirmed", "resolved", "fixed", "risk_accepted"]);

export function findingConfirmedForDisplay(status: string | undefined): boolean {
  return !!status && CONFIRMED_FINDING_STATUSES.has(status);
}
