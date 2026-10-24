package server

import (
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 报告分区：只有**当前仍然成立**的发现进正文。误报/重复/已忽略/已修复/已解决此前落进
// default 分支，被当成「确认发现」印进正文与计数——这条测试把每个状态钉死在一个桶里。
func TestPartitionReportFindings(t *testing.T) {
	cases := []struct {
		name   string
		state  string
		status string
		hasRow bool
		want   string
	}{
		{"没有 findings 行的老发现 → 正文", "confirmed", "", false, "live"},
		{"pending → 正文", "confirmed", db.FindingPending, true, "live"},
		{"in_progress → 正文", "confirmed", db.FindingInProgress, true, "live"},
		{"confirmed → 正文", "confirmed", db.FindingConfirmed, true, "live"},
		{"risk_accepted → 正文", "confirmed", db.FindingRiskAccepted, true, "live"},
		{"false_positive → 关闭", "confirmed", db.FindingFalsePositive, true, "closed"},
		{"duplicate → 关闭", "confirmed", db.FindingDuplicate, true, "closed"},
		{"ignored → 关闭", "confirmed", db.FindingIgnored, true, "closed"},
		{"fixed → 关闭", "confirmed", db.FindingFixed, true, "closed"},
		{"resolved → 关闭", "confirmed", db.FindingResolved, true, "closed"},
		{"retracted → 撤回", "confirmed", db.FindingRetracted, true, "retracted"},
		{"ruled_out → 排除", "confirmed", db.FindingRuledOut, true, "ruledOut"},
		{"open_proof_gap → 存疑", "confirmed", db.FindingOpenProofGap, true, "proofGaps"},
		{"节点状态 ruled_out（无行）→ 排除", db.FindingRuledOut, "", false, "ruledOut"},
		{"节点状态 open_proof_gap（无行）→ 存疑", db.FindingOpenProofGap, "", false, "proofGaps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := &db.Node{ID: 7, Kind: db.KindFinding, State: tc.state}
			meta := map[int64]db.FindingMeta{}
			if tc.hasRow {
				meta[7] = db.FindingMeta{ID: 1, Status: tc.status}
			}
			b := partitionReportFindings([]*db.Node{n}, meta)
			got := map[string]int{
				"live": len(b.live), "retracted": len(b.retracted), "ruledOut": len(b.ruledOut),
				"proofGaps": len(b.proofGaps), "closed": len(b.closed),
			}
			if got[tc.want] != 1 {
				t.Fatalf("status=%q state=%q 应进 %s，实际分桶=%v", tc.status, tc.state, tc.want, got)
			}
			for k, v := range got {
				if k != tc.want && v != 0 {
					t.Fatalf("status=%q 同时进了 %s：%v", tc.status, k, got)
				}
			}
		})
	}
}

// 误报可以出现在「已关闭」段，但绝不能还带着"确认发现"的身份。关闭段要能说出状态。
func TestClosedNoteKeepsTriageStatus(t *testing.T) {
	n := &db.Node{ID: 9, Kind: db.KindFinding, State: "confirmed"}
	meta := map[int64]db.FindingMeta{9: {ID: 1, Status: db.FindingFalsePositive}}
	b := partitionReportFindings([]*db.Node{n}, meta)
	if len(b.live) != 0 {
		t.Fatalf("误报不该进正文：%d", len(b.live))
	}
	if len(b.closed) != 1 || b.closed[0].Status != db.FindingFalsePositive {
		t.Fatalf("关闭桶应保留 triage 状态：%+v", b.closed)
	}
}
