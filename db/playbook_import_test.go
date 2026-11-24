package db

import (
	"fmt"
	"testing"
	"time"
)

func TestCreateImportedPlaybookInsertsLowConfidenceAndSkipsDuplicates(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	as := d.Assets()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tech := "md-import-" + suffix
	p := Playbook{
		Title: "Markdown 导入卡片 " + suffix, Summary: "外部文档草案",
		TechniqueKey: tech, Steps: "## 步骤\n1. 验证", Evidence: "GET / HTTP/1.1",
		Confidence: "high", // import path must downgrade this
	}
	created, inserted, err := as.CreateImportedPlaybook(p)
	if err != nil || !inserted || created == nil {
		t.Fatalf("insert: %+v inserted=%v err=%v", created, inserted, err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM attack_playbooks WHERE id=$1`, created.ID) })
	if created.Confidence != "low" {
		t.Fatalf("imported confidence=%q want low", created.Confidence)
	}
	if created.LastVerifiedAt != nil {
		t.Fatalf("imported card must not be marked verified: %v", created.LastVerifiedAt)
	}
	if created.Status != PlaybookStatusActive {
		t.Fatalf("status=%q", created.Status)
	}
	if created.FingerprintKey == "" {
		t.Fatal("empty fingerprint should still have a stable key")
	}
	// Same fingerprint (empty) + technique key must not overwrite.
	dup := p
	dup.Title = "另一份文档的同手法卡片"
	dup.Steps = "不同步骤"
	again, inserted, err := as.CreateImportedPlaybook(dup)
	if err != nil || inserted || again != nil {
		t.Fatalf("duplicate should be skipped: %+v inserted=%v err=%v", again, inserted, err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM attack_playbooks WHERE technique_key=$1`, tech).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows=%d want 1", count)
	}
	// The stored card remains the original, not the duplicate.
	got, err := as.GetPlaybook(created.ID)
	if err != nil || got == nil || got.Title != p.Title {
		t.Fatalf("duplicate overwrote card: %+v %v", got, err)
	}
}
