package agent

import (
	"strconv"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 同族打法要在领取意图时**主动推送完整步骤**（sapper 的"一道题解锁一整族"）；
// 卡片整体进围栏，"先确认同族"的守则由代码持有。
func TestPlaybookBriefPushedForMatchingIntent(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	as := d.Assets()

	aid, err := as.UpsertHTTPService(db.UpsertHTTPServiceReq{
		URL: "http://wp-push-test.invalid:8080/", Technologies: []string{"wordpress", "php"},
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM assets WHERE id=$1`, aid) })

	card, _, err := as.UpsertPlaybook(db.Playbook{
		Fingerprint:  db.PlaybookFingerprint{Technologies: []string{"wordpress", "php"}, ServiceName: "http", Port: 8080},
		TechniqueKey: "wp-push-test", Title: "推送测试打法", Summary: "登录口默认口令",
		Steps: "## 识别\n访问 /wp-login.php\n## 利用\n默认口令尝试", Confidence: db.ConfidenceHigh,
	})
	if err != nil {
		t.Fatalf("create playbook: %v", err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM attack_playbooks WHERE id=$1`, card.ID) })

	task, err := d.CreateTaskWithOptions("playbook push", "推送", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	st := d.Exploration(task.ExplorationID)

	intentID, err := st.AddIntent(map[string]any{"summary": "打这个 WP 站点", "asset_ids": []int64{aid}}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := st.NodesByIDs([]int64{intentID})
	if err != nil || len(nodes) == 0 {
		t.Fatalf("load intent: %v", err)
	}

	ts := NewToolSet(st, "work#1")
	ts.SetAssetStore(as, d.Companies())
	brief := ts.playbookBriefForIntent(nodes[0])
	if brief == "" {
		t.Fatal("命中了打法却没有推送")
	}
	for _, want := range []string{
		"【同族打法（代码持有守则）】",
		"先确认确实是同一族",
		"推送测试打法",
		"/wp-login.php",
		"record_playbook_feedback",
		untrustedOpenPrefix, // 卡片内容进围栏
	} {
		if !strings.Contains(brief, want) {
			t.Fatalf("推送内容缺少 %q：\n%s", want, brief)
		}
	}

	// 没有关联资产的意图不推送：否则每条意图都会塞一段无关卡片。
	plainID, err := st.AddIntent(map[string]any{"summary": "纯全局侦察"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := st.NodesByIDs([]int64{plainID})
	if err != nil || len(plain) == 0 {
		t.Fatal(err)
	}
	if got := ts.playbookBriefForIntent(plain[0]); got != "" {
		t.Fatalf("无资产意图不该推送打法：\n%s", got)
	}
}

// 反馈回执要带族内统计（族内多于一张卡时）：让调用方看到"这道打法在别的目标上也成功过"。
func TestRecordPlaybookFeedbackReportsFamily(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { _ = d.Close() })
	as := d.Assets()
	task, err := d.CreateTaskWithOptions("playbook family", "族统计", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	st := d.Exploration(task.ExplorationID)
	tech := "family-feedback-test"
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM attack_playbooks WHERE technique_key=$1`, tech) })

	var ids []int64
	for i, tch := range []string{"family-fp-a", "family-fp-b"} {
		p, _, err := as.UpsertPlaybook(db.Playbook{
			Fingerprint:  db.PlaybookFingerprint{Technologies: []string{tch}},
			TechniqueKey: tech, Title: "族卡片", Summary: "x", Steps: "## 步骤",
			Confidence: db.ConfidenceHigh,
		})
		if err != nil {
			t.Fatalf("upsert #%d: %v", i, err)
		}
		ids = append(ids, p.ID)
	}
	ts := NewToolSet(st, "work#1")
	ts.SetAssetStore(as, d.Companies())
	out := callTool(t, ts.WorkerTools(), "record_playbook_feedback",
		`{"playbook_id":`+strconvFormat(ids[0])+`,"outcome":"worked","note":"A 成功"}`)
	for _, want := range []string{`"family"`, tech, `"cards":2`} {
		if !strings.Contains(out, want) && !strings.Contains(out, `"cards": 2`) {
			t.Fatalf("反馈回执缺少族统计 %q：%s", want, out)
		}
	}
}

func strconvFormat(n int64) string {
	return strconv.FormatInt(n, 10)
}
