package db

import (
	"strings"
	"testing"
)

func playbookFixture(t *testing.T) (*DB, *AssetStore, int64) {
	t.Helper()
	d, taskID, _ := coverageMatrixFixture(t)
	as := d.Assets()
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM attack_playbooks WHERE title LIKE 'test-playbook-%'`)
	})
	return d, as, taskID
}

func addPlaybookService(t *testing.T, as *AssetStore, taskID int64, url string, techs []string, favicon, title string) {
	t.Helper()
	if _, err := as.UpsertHTTPService(UpsertHTTPServiceReq{
		URL: url, Technologies: techs, FaviconMMH3: favicon, PageTitle: title, TaskID: taskID,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPlaybookFingerprintKeyNormalizes：同一组指纹只要顺序/大小写/空白不同，必须是同一个 key；
// 否则"同一种技术栈"会被拆成多张卡，永远命中不了。
func TestPlaybookFingerprintKeyNormalizes(t *testing.T) {
	a := PlaybookFingerprint{Technologies: []string{"WordPress", " php ", "wordpress"}, FaviconMMH3: " -123 ", ServiceName: "HTTP", Port: 443, PageTitle: " Example "}
	b := PlaybookFingerprint{Technologies: []string{"php", "wordpress"}, FaviconMMH3: "-123", ServiceName: "http", Port: 443, PageTitle: "Example"}
	if a.Key() != b.Key() {
		t.Fatalf("归一化后 key 不一致：%s != %s", a.Key(), b.Key())
	}
	c := b
	c.FaviconMMH3 = "-999"
	if a.Key() == c.Key() {
		t.Fatal("favicon 不同必须得到不同 key（它是最强匹配信号）")
	}
}

// TestUpsertPlaybookDedup：同 (指纹, 手法) 更新同一行并 revision+1，统计不丢；
// 同指纹不同手法是两张卡（同一套应用可以有多条打法）。
func TestUpsertPlaybookDedup(t *testing.T) {
	_, as, _ := playbookFixture(t)
	fp := PlaybookFingerprint{Technologies: []string{"wordpress", "php"}, FaviconMMH3: "-123", ServiceName: "http", Port: 443}
	first, inserted, err := as.UpsertPlaybook(Playbook{
		Fingerprint: fp, TechniqueKey: "wp-file-manager-upload",
		Title: "test-playbook-WP 文件管理器上传", Summary: "插件默认口令 + 上传", Steps: "1. 访问 /wp-content/plugins/wp-file-manager/",
		Evidence: "curl ...", Confidence: ConfidenceHigh, SourceTaskID: "1",
	})
	if err != nil || !inserted {
		t.Fatalf("首次写入：%v inserted=%v", err, inserted)
	}
	if _, err := as.RecordPlaybookFeedback(first.ID, "worked", "复现成功"); err != nil {
		t.Fatal(err)
	}
	second, inserted, err := as.UpsertPlaybook(Playbook{
		Fingerprint: fp, TechniqueKey: "wp-file-manager-upload",
		Title: "test-playbook-WP 文件管理器上传", Summary: "更新后的摘要", Steps: "1. 新版步骤",
		Confidence: ConfidenceHigh,
	})
	if err != nil || inserted {
		t.Fatalf("重复写入应更新同一行：%v inserted=%v", err, inserted)
	}
	if second.ID != first.ID || second.Revision != 2 {
		t.Fatalf("去重失败：id %d→%d revision %d", first.ID, second.ID, second.Revision)
	}
	if second.Successes != 1 || second.Summary != "更新后的摘要" {
		t.Fatalf("更新时统计丢了或内容没更新：%+v", second)
	}
	if second.Evidence == "" {
		t.Fatal("更新时空 evidence 不应清掉旧证据")
	}
	other, inserted, err := as.UpsertPlaybook(Playbook{
		Fingerprint: fp, TechniqueKey: "wp-admin-bruteforce",
		Title: "test-playbook-WP 后台弱口令", Steps: "hydra ...", Confidence: ConfidenceHigh,
	})
	if err != nil || !inserted || other.ID == first.ID {
		t.Fatalf("不同手法应新建卡片：%v inserted=%v", err, inserted)
	}
}

// TestMatchPlaybooksScoring：favicon 是最强信号；一两个非通用技术可以命中；
// 只有 nginx/php 这类通用标签不能单独命中（否则每条 nginx+php 都会误配）。
func TestMatchPlaybooksScoring(t *testing.T) {
	_, as, _ := playbookFixture(t)
	mk := func(title, tech, technique, favicon string) *Playbook {
		p, _, err := as.UpsertPlaybook(Playbook{
			Fingerprint:  PlaybookFingerprint{Technologies: strings.Split(tech, ","), FaviconMMH3: favicon, ServiceName: "http", Port: 443},
			TechniqueKey: technique, Title: "test-playbook-" + title, Steps: "steps", Confidence: ConfidenceHigh,
		})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	byFavicon := mk("favicon", "wordpress,php", "t-favicon", "-123")
	byTech := mk("tech", "wordpress,php", "t-tech", "")
	generic := mk("generic", "nginx,php", "t-generic", "")
	unrelated := mk("unrelated", "shiro,spring", "t-unrelated", "")

	matches, err := as.MatchPlaybooks(PlaybookFingerprint{Technologies: []string{"wordpress", "php"}, FaviconMMH3: "-123", ServiceName: "http", Port: 443}, 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]int{}
	for _, m := range matches {
		got[m.ID] = m.Score
	}
	if got[byFavicon.ID] < 100 || got[byTech.ID] < 40 {
		t.Fatalf("应命中 favicon 与 wordpress/php：%v", got)
	}
	if _, ok := got[generic.ID]; ok {
		t.Fatalf("只有通用技术不应命中：score=%d", got[generic.ID])
	}
	if _, ok := got[unrelated.ID]; ok {
		t.Fatal("无关技术栈不应命中")
	}
	if matches[0].ID != byFavicon.ID {
		t.Fatalf("排序应把 favicon 强信号放前面：%d", matches[0].ID)
	}

	// 只有通用标签的查询：不应把 wordpress 卡片硬套上来。
	matches, err = as.MatchPlaybooks(PlaybookFingerprint{Technologies: []string{"nginx", "php"}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("通用指纹查询不该命中特定打法：%+v", matches)
	}
}

// TestTaskFingerprintAndMatch：task 级聚合能看见范围内 service 资产的技术栈与 favicon，
// 并据此命中打法卡；范围外的资产不参与。
func TestTaskFingerprintAndMatch(t *testing.T) {
	_, as, taskID := playbookFixture(t)
	addPlaybookService(t, as, taskID, "https://www.matrix-test.example/", []string{"WordPress", "PHP"}, "-321", "Example")
	addPlaybookService(t, as, taskID, "https://api.matrix-test.example/", []string{"nginx"}, "", "")
	p, _, err := as.UpsertPlaybook(Playbook{
		Fingerprint:  PlaybookFingerprint{Technologies: []string{"wordpress", "php"}, FaviconMMH3: "-321"},
		TechniqueKey: "task-match", Title: "test-playbook-task", Steps: "steps", Confidence: ConfidenceHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	tf := as.TaskFingerprint(taskID)
	joined := strings.Join(tf.Technologies, ",")
	if !strings.Contains(joined, "wordpress") || !strings.Contains(joined, "php") {
		t.Fatalf("task 指纹没聚合出技术栈：%+v", tf)
	}
	if len(tf.Favicons) == 0 || tf.Favicons[0] != "-321" {
		t.Fatalf("task 指纹没聚合出 favicon：%+v", tf)
	}
	matches, err := as.MatchPlaybooksForTask(taskID, 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range matches {
		if m.ID == p.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("task 指纹没命中卡片：%+v", matches)
	}
}

// TestPlaybookFeedbackAndStatus：反馈计数、非法 outcome 拒收、废弃需要理由且不再参与匹配。
func TestPlaybookFeedbackAndStatus(t *testing.T) {
	_, as, _ := playbookFixture(t)
	p, _, err := as.UpsertPlaybook(Playbook{
		Fingerprint:  PlaybookFingerprint{Technologies: []string{"wordpress", "php"}, FaviconMMH3: "-777"},
		TechniqueKey: "feedback", Title: "test-playbook-feedback", Steps: "steps", Confidence: ConfidenceHigh,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := as.RecordPlaybookFeedback(p.ID, "maybe", ""); err == nil {
		t.Fatal("非法 outcome 应被拒收")
	}
	if _, err := as.RecordPlaybookFeedback(p.ID, "worked", "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := as.RecordPlaybookFeedback(p.ID, "failed", "WAF 拦了"); err != nil {
		t.Fatal(err)
	}
	got, _ := as.GetPlaybook(p.ID)
	if got.Successes != 1 || got.Failures != 1 || got.LastOutcome != "failed" || got.LastVerifiedAt == nil {
		t.Fatalf("反馈计数不对：%+v", got)
	}
	if _, err := as.SetPlaybookStatus(p.ID, PlaybookStatusDeprecated, ""); err == nil {
		t.Fatal("废弃不给理由应被拒收")
	}
	if n, err := as.SetPlaybookStatus(p.ID, PlaybookStatusDeprecated, "打法已过期"); err != nil || n != 1 {
		t.Fatalf("废弃失败：%v n=%d", err, n)
	}
	matches, err := as.MatchPlaybooks(PlaybookFingerprint{FaviconMMH3: "-777"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if m.ID == p.ID {
			t.Fatal("已废弃的卡片不该再被检索到")
		}
	}
	list, total, err := as.ListPlaybooks(PlaybookFilter{Q: "test-playbook-feedback", Status: PlaybookStatusDeprecated})
	if err != nil || total != 1 || len(list) != 1 || list[0].DeprecatedReason != "打法已过期" {
		t.Fatalf("列表/状态过滤不对：%v total=%d %+v", err, total, list)
	}
}
