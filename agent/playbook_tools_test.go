package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

func playbookToolSet(t *testing.T) (*ToolSet, *db.AssetStore, int64, int64) {
	t.Helper()
	d := testDB(t)
	task, err := d.CreateTask("playbook-tools-test", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID := task.ID
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM attack_playbooks WHERE source_task_id=$1`, strconv.FormatInt(taskID, 10))
		_, _ = d.Exec(`DELETE FROM tasks WHERE id=$1`, taskID)
		d.Close()
	})
	as := d.Assets()
	if _, err := as.AddAgentScope(taskID, "root_domain", "pb-test.example", "test scope", "agent"); err != nil {
		t.Fatal(err)
	}
	assetID, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{
		URL: "https://www.pb-test.example/", Technologies: []string{"WordPress", "PHP"},
		FaviconMMH3: "-4242", PageTitle: "Example WP", TaskID: taskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	var expID int64
	if err := d.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, taskID).Scan(&expID); err != nil {
		t.Fatal(err)
	}
	// graphOverviewData 会读探索图（goals/hints/…），所以要给它真实的 ExplorationStore；
	// 只测 lookup 时可以传 nil，这里要断言态势注入，不能省。
	ts := NewToolSet(d.Exploration(expID), "test")
	ts.SetAssetStore(as, d.Companies())
	ts.SetTaskID(taskID)
	return ts, as, taskID, assetID
}

func callPlaybookTool(t *testing.T, tool actool.CoreTool, payload any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	res, err := tool.Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("tool call: %v", err)
	}
	return res.Flatten(), res.IsError
}

// TestRecordPlaybookUsesAssetFingerprint：指纹以资产库为准（技术栈/favicon/端口），
// 并强制 confidence=high + evidence；写入后能被 lookup 按 asset_id 命中并累加使用次数。
func TestRecordPlaybookUsesAssetFingerprint(t *testing.T) {
	ts, as, taskID, assetID := playbookToolSet(t)
	rec := ts.recordPlaybook()

	// 未确认不沉淀。
	if out, isErr := callPlaybookTool(t, rec, map[string]any{
		"title": "wp upload", "steps": "1. test", "evidence": "curl x", "confidence": "medium",
		"fingerprint": map[string]any{"technologies": []string{"wordpress"}},
	}); !isErr {
		t.Fatalf("medium 不该被接收：%s", out)
	}
	if out, isErr := callPlaybookTool(t, rec, map[string]any{
		"title": "wp upload", "steps": "1. test", "evidence": "  ", "confidence": "high",
		"fingerprint": map[string]any{"technologies": []string{"wordpress"}},
	}); !isErr {
		t.Fatalf("空 evidence 不该被接收：%s", out)
	}

	out, isErr := callPlaybookTool(t, rec, map[string]any{
		"title": "test-playbook WP 插件上传", "summary": "WordPress + 某插件 → getshell",
		"technique_key":  "wp-plugin-upload",
		"steps":          "## 识别\n/wp-content/plugins/x\n## 利用\nPOST /x/upload.php",
		"evidence":       "curl -s -X POST ... -> uid=33(www-data)",
		"confidence":     "high",
		"asset_ids":      []int64{assetID},
		"source_node_id": 99,
	})
	if isErr {
		t.Fatalf("沉淀失败：%s", out)
	}
	var recRes struct {
		PlaybookID int64 `json:"playbook_id"`
		Inserted   bool  `json:"inserted"`
	}
	if err := json.Unmarshal([]byte(out), &recRes); err != nil {
		t.Fatal(err)
	}
	if recRes.PlaybookID <= 0 || !recRes.Inserted {
		t.Fatalf("写入回执不对：%s", out)
	}
	saved, err := as.GetPlaybook(recRes.PlaybookID)
	if err != nil || saved == nil {
		t.Fatal(saved, err)
	}
	joined := strings.Join(saved.Fingerprint.Technologies, ",")
	if !strings.Contains(joined, "wordpress") || !strings.Contains(joined, "php") {
		t.Fatalf("指纹没从资产库补齐：%+v", saved.Fingerprint)
	}
	if saved.Fingerprint.FaviconMMH3 != "-4242" || saved.SourceTaskID != strconv.FormatInt(taskID, 10) {
		t.Fatalf("favicon/来源任务不对：%+v", saved)
	}

	// 态势注入：planner/worker 的 graph_overview 里应自动出现 matching_playbooks。
	data := ts.graphOverviewData()
	lines, _ := data["matching_playbooks"].([]string)
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "test-playbook WP") {
		t.Fatalf("态势没注入匹配打法：%+v", data["matching_playbooks"])
	}
	if _, ok := data["matching_playbooks_note"].(string); !ok {
		t.Fatal("态势缺 matching_playbooks_note：模型不知道该怎么用")
	}

	// lookup by asset_id：命中并累加 uses。
	lookup := ts.lookupPlaybooks()
	out, isErr = callPlaybookTool(t, lookup, map[string]any{"asset_id": assetID, "limit": 3})
	if isErr {
		t.Fatalf("lookup 失败：%s", out)
	}
	if !strings.Contains(out, "wp-plugin-upload") && !strings.Contains(out, "test-playbook WP") {
		t.Fatalf("lookup 没命中卡片：%s", out)
	}
	after, _ := as.GetPlaybook(recRes.PlaybookID)
	if after.Uses != 1 {
		t.Fatalf("lookup 没累加 uses：%d", after.Uses)
	}

	// feedback worked：成功率 +1、最近验证有值。
	fb := ts.recordPlaybookFeedback()
	out, isErr = callPlaybookTool(t, fb, map[string]any{"playbook_id": recRes.PlaybookID, "outcome": "worked", "note": "同指纹站点复现"})
	if isErr {
		t.Fatalf("feedback 失败：%s", out)
	}
	after, _ = as.GetPlaybook(recRes.PlaybookID)
	if after.Successes != 1 || after.LastVerifiedAt == nil {
		t.Fatalf("反馈没生效：%+v", after)
	}
}
