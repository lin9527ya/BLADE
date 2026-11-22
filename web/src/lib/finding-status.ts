// findings.status 中属于「候选闭合/已被否定」的状态：这些记录只留在发现页/审计里，
// 不作为当前漏洞进入案例成员、播报板或探索链路的默认视图。
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

// 探索链路的显示口径：默认展示全部 finding（pending 也是真实上报），
// 只有用户显式勾选“隐藏候选/已排除”时才排除候选闭合项。
export function findingVisibleInChain(status: string | undefined, hideCandidates: boolean): boolean {
  return !hideCandidates || !findingCandidateStatus(status);
}
