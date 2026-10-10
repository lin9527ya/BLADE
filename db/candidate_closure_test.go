package db

import (
	"context"
	"strings"
	"testing"
)

// TestRecordCandidateClosureValidates pins the three-state discipline at the
// storage boundary: a ruled_out candidate must name its control point, an
// open_proof_gap candidate must name the gap, and the confidence label is
// mandatory — a candidate is never closed without a reason.
func TestRecordCandidateClosureValidates(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	st := d.Exploration(expID)
	intent, err := st.AddIntent(map[string]any{"summary": "candidate closure"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	base := CandidateClosureInput{
		TaskID: taskID, IntentID: intent,
		VulnClass: "sqli", Name: "登录口疑似 SQLi", Severity: SeverityHigh, Summary: "登录口疑似 SQLi", Worker: "work#1",
		Outcome: FindingRuledOut, Counterevidence: "服务端走参数化查询（payload 原样出现在错误日志里未参与执行）", Confidence: ConfidenceHigh,
	}
	for name, mutate := range map[string]func(*CandidateClosureInput){
		"missing counterevidence": func(in *CandidateClosureInput) { in.Counterevidence = "  " },
		"missing confidence":      func(in *CandidateClosureInput) { in.Confidence = "" },
		"bad confidence":          func(in *CandidateClosureInput) { in.Confidence = "very-sure" },
		"bad outcome":             func(in *CandidateClosureInput) { in.Outcome = "probably_fine" },
		"gap without note": func(in *CandidateClosureInput) {
			in.Outcome, in.Counterevidence, in.ProofGap = FindingOpenProofGap, "", "  "
		},
	} {
		in := base
		mutate(&in)
		if _, err := st.RecordCandidateClosure(context.Background(), in); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// TestRecordCandidateClosurePersists walks both closure outcomes through the
// graph: the node carries the closure state and payload, the findings row
// carries the note and confidence, and the intent→node edge plus asset anchor
// are in place.
func TestRecordCandidateClosurePersists(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	as := d.Assets()
	asset, err := as.UpsertSubdomain(UpsertSubdomainReq{Domain: "cand.matrix-test.example", TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	// 资产表是跨测试共享的：本用例自建的子域必须在收尾时删掉，
	// 否则会扩进同根域 fixture 用例的「范围内资产」计数。
	t.Cleanup(func() { _, _ = as.DeleteByIDs([]int64{asset}) })
	st := d.Exploration(expID)
	intent, err := st.AddIntent(map[string]any{"summary": "candidate closure"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM findings WHERE task_id=$1`, taskID)
	})
	ruled, err := st.RecordCandidateClosure(context.Background(), CandidateClosureInput{
		TaskID: taskID, IntentID: intent, AssetIDs: []int64{asset},
		VulnClass: "ssrf", Name: "图片代理疑似 SSRF", Severity: SeverityHigh, Summary: "图片代理只允许白名单域，已实测内网地址被 403",
		Worker: "work#1", Outcome: FindingRuledOut,
		Counterevidence: "目标对 url 参数做域名白名单校验：http://127.0.0.1 与 http://169.254.169.254 均返回 403，控制点在应用层且在攻击者路径上", Confidence: ConfidenceHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	node, err := st.GetNode(ruled.NodeID)
	if err != nil || node == nil {
		t.Fatalf("node: %+v err=%v", node, err)
	}
	if node.State != FindingRuledOut || !strings.Contains(string(node.Payload), "counterevidence") {
		t.Fatalf("closure node: state=%s payload=%s", node.State, node.Payload)
	}
	var anchors, edges int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, ruled.NodeID, asset).Scan(&anchors); err != nil || anchors != 1 {
		t.Fatalf("closure must anchor its asset: n=%d err=%v", anchors, err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges WHERE src_id=$1 AND dst_id=$2 AND rel='yields'`, intent, ruled.NodeID).Scan(&edges); err != nil || edges != 1 {
		t.Fatalf("closure must hang off the intent: n=%d err=%v", edges, err)
	}
	f, err := d.GetFinding(ruled.FindingID)
	if err != nil || f == nil {
		t.Fatalf("finding: %+v err=%v", f, err)
	}
	if f.Status != FindingRuledOut || f.Confidence != ConfidenceHigh || !strings.Contains(f.Counterevidence, "白名单") || f.ProofGap != "" {
		t.Fatalf("closure row: %+v", f)
	}
	gap, err := st.RecordCandidateClosure(context.Background(), CandidateClosureInput{
		TaskID: taskID, IntentID: intent,
		VulnClass: "xss", Name: "搜索框疑似存储型 XSS", Severity: SeverityMedium, Summary: "输入被回显但编码方式未证实",
		Worker: "work#1", Outcome: FindingOpenProofGap,
		ProofGap: "需要可触发的前端渲染路径与可用账号：本次未拿到登录态，无法验证 payload 是否在渲染时执行", Confidence: ConfidenceLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	gf, err := d.GetFinding(gap.FindingID)
	if err != nil || gf == nil {
		t.Fatalf("gap finding: %+v err=%v", gf, err)
	}
	if gf.Status != FindingOpenProofGap || gf.Confidence != ConfidenceLow || !strings.Contains(gf.ProofGap, "登录态") || gf.Counterevidence != "" {
		t.Fatalf("gap row: %+v", gf)
	}
	// meta 面：报告依赖它渲染「已排除候选/存疑待补证」两段。
	meta, err := d.FindingMetaByNodeID(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if m := meta[ruled.NodeID]; m.Status != FindingRuledOut || m.Confidence != ConfidenceHigh || !strings.Contains(m.Counterevidence, "白名单") {
		t.Fatalf("ruled_out meta: %+v", m)
	}
	if m := meta[gap.NodeID]; m.Status != FindingOpenProofGap || !strings.Contains(m.ProofGap, "登录态") {
		t.Fatalf("gap meta: %+v", m)
	}
	if !ValidFindingStatus(FindingRuledOut) || !ValidFindingStatus(FindingOpenProofGap) {
		t.Fatal("closure states must be valid finding statuses")
	}
}

// TestSetFindingClosureAndConfidence covers the human-triage path (patchFinding).
func TestSetFindingClosureAndConfidence(t *testing.T) {
	d, taskID, expID := coverageMatrixFixture(t)
	st := d.Exploration(expID)
	rec, err := st.RecordCandidateClosure(context.Background(), CandidateClosureInput{
		TaskID: taskID, VulnClass: "idor", Name: "疑似越权读", Severity: SeverityHigh, Summary: "疑似越权读",
		Worker: "work#1", Outcome: FindingOpenProofGap, ProofGap: "缺另一个账号做对照", Confidence: ConfidenceLow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := d.SetFindingClosure(rec.FindingID, FindingRuledOut, "改为排除：接口有 object 级鉴权，跨账号返回 403", ""); err != nil || !ok {
		t.Fatalf("set closure: ok=%v err=%v", ok, err)
	}
	if ok, err := d.SetFindingConfidence(rec.FindingID, ConfidenceHigh); err != nil || !ok {
		t.Fatalf("set confidence: ok=%v err=%v", ok, err)
	}
	if _, err := d.SetFindingClosure(rec.FindingID, FindingConfirmed, "", ""); err == nil {
		t.Fatal("closure setter must reject non-closure statuses")
	}
	if _, err := d.SetFindingConfidence(rec.FindingID, "certain"); err == nil {
		t.Fatal("confidence setter must reject unknown labels")
	}
	f, err := d.GetFinding(rec.FindingID)
	if err != nil || f == nil {
		t.Fatalf("finding: %+v err=%v", f, err)
	}
	if f.Status != FindingRuledOut || f.Confidence != ConfidenceHigh || !strings.Contains(f.Counterevidence, "403") {
		t.Fatalf("triaged row: %+v", f)
	}
}
