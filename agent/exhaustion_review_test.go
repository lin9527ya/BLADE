package agent

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 复核员输出必须严格解析：字段缺失或不是 JSON 时返回错误——调用方据此走
// 「证据不足」的失败策略，而不是悄悄当成通过。
func TestParseExhaustionVerdict(t *testing.T) {
	ok, err := ParseExhaustionVerdict(`前言
{"accurate": false, "reason": "三个方向都只有工具失败记录", "gaps": ["#12 是超时，不是否定结论"]}
后记`)
	if err != nil {
		t.Fatalf("应容忍前后多余文字: %v", err)
	}
	if ok.Accurate || len(ok.Gaps) != 1 || !strings.Contains(ok.Gaps[0], "超时") {
		t.Fatalf("解析结果不对: %+v", ok)
	}

	if _, err := ParseExhaustionVerdict(`not json`); err == nil {
		t.Fatal("非 JSON 必须报错")
	}
	if _, err := ParseExhaustionVerdict(`{"accurate": true}`); err == nil {
		t.Fatal("缺少 reason 必须报错")
	}
}

// 复核证据取自**图**：方向、终态、以及该意图实际产出的事实。planner 的自述只是
// 待核对的结论，不是证据本身。
func TestBuildExhaustionClaimUsesGraphEvidence(t *testing.T) {
	tools, ts, intent := newToolWorker(t)
	as := testDB(t).Assets()
	tools.SetAssetStore(as, as.Companies())
	taskID := tools.taskID
	root := "claim-" + strings.Repeat("x", 1) + "test.example"
	if _, err := as.AddAgentScope(taskID, "subdomain", "www."+root, "目标", "agent"); err != nil {
		t.Fatal(err)
	}

	// 一条"有证据"的已终结意图：写了 fact。
	factID, err := ts.AddNode("fact", map[string]any{"summary": "登录接口对 5 个 payload 均返回 500 且无差异"}, 0, "confirmed", "work#1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Link(intent, db.RelYields, factID); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.CompareAndSetIntentState(intent, "open", "negative"); err != nil {
		t.Fatal(err)
	}
	// 一条"没证据"的已终结意图：blocked，无产出。
	blocked, err := ts.AddIntent(map[string]any{"summary": "试上传绕过"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.CompareAndSetIntentState(blocked, "open", "blocked"); err != nil {
		t.Fatal(err)
	}

	claim := BuildExhaustionClaim(ts, as, taskID, "策略内已无可行方向")
	if claim.Reason == "" || len(claim.Directions) < 2 {
		t.Fatalf("应带上已终结方向：%+v", claim)
	}
	var withEvidence, withoutEvidence int
	for _, d := range claim.Directions {
		if d.Evidence != "" {
			withEvidence++
			if !strings.Contains(d.Evidence, "payload") {
				t.Fatalf("证据摘要应来自 fact：%+v", d)
			}
		} else {
			withoutEvidence++
		}
	}
	if withEvidence == 0 || withoutEvidence == 0 {
		t.Fatalf("有证据与无证据的方向都应如实带上（证据缺失正是复核要挑的）：%+v", claim.Directions)
	}
	if claim.Coverage.DepthThreshold != 3 {
		t.Fatalf("应带上深度阈值：%+v", claim.Coverage)
	}
}
