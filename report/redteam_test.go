package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// redteam 模板是同一批事实的另一种编排：攻击链/检测/凭据尝试都要在，凭据明文不能出现。
func TestRedTeamMarkdownSectionsAndNoSecrets(t *testing.T) {
	credPayload, _ := json.Marshal(map[string]any{"summary": "app 的 ssh 口令", "username": "app", "kind": "password", "secret": "SUPERSECRET-VALUE"})
	footPayload, _ := json.Marshal(map[string]any{"summary": "web01 ssh 据点", "protocol": "ssh", "host": "web01"})
	in := Input{
		Title:       "Acme 外网评估",
		Goal:        "拿到域控",
		GeneratedAt: time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC),
		Findings:    []*db.Node{{ID: 1, Kind: db.KindFinding, Payload: json.RawMessage(`{"name":"SQL 注入","severity":"high","summary":"登录接口可注入"}`)}},
		Credentials: []*db.Node{{ID: 2, Kind: db.KindCredential, State: db.StateLootActive, Payload: credPayload}},
		Footholds:   []*db.Node{{ID: 3, Kind: db.KindFoothold, State: db.StateLootActive, Payload: footPayload}},
		LootUsedBy:  map[int64][]LootUse{3: {{IntentID: 9, Summary: "拿据点开隧道"}}},
		Chains: []AttackChain{{Head: "web01 ssh 据点", Steps: []ChainStep{{
			IntentID: 9, Summary: "经据点横向", Techniques: []string{"T1021.004"}, Produced: []string{"db01 凭据"},
		}}}},
		Detections: []*db.Node{
			{ID: 4, Kind: db.KindDetection, State: db.StateDetectionTriggered},
			{ID: 5, Kind: db.KindDetection, State: db.StateDetectionUnknown},
		},
		CredentialAttempts: []db.CredentialAttempt{{Host: "web01", Account: "app", Tool: "sshpass", Spray: false, At: time.Now()}},
	}
	md := RedTeamMarkdown(in)
	for _, want := range []string{"## 攻击链", "## 检测验证", "无法判定", "## 凭据尝试台账", "SQL 注入", "web01 ssh 据点"} {
		if !strings.Contains(md, want) {
			t.Errorf("redteam 报告缺少 %q", want)
		}
	}
	if strings.Contains(md, "SUPERSECRET-VALUE") {
		t.Fatal("redteam 报告泄露了凭据明文")
	}
}
