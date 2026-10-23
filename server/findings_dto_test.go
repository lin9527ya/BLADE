package server

import (
	"encoding/json"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// TestFindingDTOIncludesClosureReason 钉住"列表页也能直接看到为什么被排除/存疑"：
// 反证与缺口必须随列表 DTO 下发，而不是只有详情页才有。
func TestFindingDTOIncludesClosureReason(t *testing.T) {
	// 全局发现列表（DBFinding → DTO）
	d := findingFromDB(&db.DBFinding{
		ID: 7, Status: db.FindingRuledOut, Counterevidence: "服务端走参数化查询，payload 未参与执行",
	}, nil)
	if d.Counterevidence == "" || d.Status != db.FindingRuledOut {
		t.Fatalf("全局列表 DTO 丢了反证：%+v", d)
	}

	// 任务发现列表（exploration node + FindingMeta → DTO）
	payload, _ := json.Marshal(map[string]any{
		"vulnclass": "sqli", "severity": "high", "summary": "登录口疑似注入",
	})
	n := &db.Node{ID: 9, Kind: db.KindFinding, Payload: payload}
	meta := map[int64]db.FindingMeta{
		9: {ID: 70, Status: db.FindingRuledOut, Counterevidence: "应用层域名白名单：内网地址 403"},
	}
	out := findingDTOsForOwner("1", "", []*db.Node{n}, meta, nil)
	if len(out) != 1 || out[0].Counterevidence != "应用层域名白名单：内网地址 403" {
		t.Fatalf("任务发现列表 DTO 丢了反证：%+v", out)
	}

	// 存疑缺口同理。
	meta[9] = db.FindingMeta{ID: 70, Status: db.FindingOpenProofGap, ProofGap: "缺登录态，无法验证渲染执行"}
	out = findingDTOsForOwner("1", "", []*db.Node{n}, meta, nil)
	if len(out) != 1 || out[0].ProofGap != "缺登录态，无法验证渲染执行" {
		t.Fatalf("任务发现列表 DTO 丢了缺口：%+v", out)
	}
}
