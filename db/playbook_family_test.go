package db

import (
	"fmt"
	"testing"
	"time"
)

// 族内统计按 technique_key 聚合：单卡的成败可能只是运气，族内多张卡的成败才是
// "这道打法真能用"的证据。
func TestPlaybookFamilyStatsAggregatesByTechnique(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	as := d.Assets()
	tech := fmt.Sprintf("family-test-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM attack_playbooks WHERE technique_key=$1`, tech) })

	mk := func(fp PlaybookFingerprint, title string) *Playbook {
		p, _, err := as.UpsertPlaybook(Playbook{
			Fingerprint: fp, TechniqueKey: tech, Title: title, Summary: "x",
			Steps: "## 步骤", Confidence: ConfidenceHigh,
		})
		if err != nil {
			t.Fatalf("upsert %s: %v", title, err)
		}
		return p
	}
	a := mk(PlaybookFingerprint{Technologies: []string{"family-a"}}, "族卡片 A")
	b := mk(PlaybookFingerprint{Technologies: []string{"family-b"}}, "族卡片 B")

	if _, err := as.RecordPlaybookFeedback(a.ID, "worked", "A 成功"); err != nil {
		t.Fatal(err)
	}
	if _, err := as.RecordPlaybookFeedback(b.ID, "failed", "B 被拦"); err != nil {
		t.Fatal(err)
	}

	cards, wins, losses, err := as.PlaybookFamilyStats(tech)
	if err != nil {
		t.Fatal(err)
	}
	if cards != 2 || wins != 1 || losses != 1 {
		t.Fatalf("族统计 = cards %d / %d 成 / %d 败，want 2/1/1", cards, wins, losses)
	}
	// 空 key 不查库：返回零值而不是把整库聚合进来。
	if cards, wins, losses, err = as.PlaybookFamilyStats("  "); err != nil || cards+wins+losses != 0 {
		t.Fatalf("空 technique_key 应返回零值：%d/%d/%d err=%v", cards, wins, losses, err)
	}
}
