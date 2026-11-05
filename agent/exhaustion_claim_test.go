package agent

import "testing"

// 穷尽复核的输入要带上通路状态：手上握着没推进的据点却宣称穷尽，是深度没走完。
func TestExhaustionClaimIncludesChains(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	if out := callTool(t, tools.WorkerTools(), "report_foothold",
		`{"risk_area":"other","summary":"web01 shell","access":"shell","privilege":"user",`+
			`"evidence":"id","confidence":"high"}`); out == "" {
		t.Fatal("据点没记上")
	}
	claim := BuildExhaustionClaim(ts, nil, 0, "范围内已无可行方向")
	if claim.Coverage.ChainsOpen != 1 {
		t.Fatalf("ChainsOpen = %d，want 1", claim.Coverage.ChainsOpen)
	}
	_ = intent
}
