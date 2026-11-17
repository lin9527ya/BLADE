package db

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestCampaignMetaAndPhases(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	name := fmt.Sprintf("phase-test-%d", time.Now().UnixNano())
	c, err := d.CreateCampaign(name, "campaign phase test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM campaigns WHERE id=$1`, c.ID) //nolint:errcheck

	starts := time.Now().UTC().Truncate(time.Second)
	ends := starts.Add(24 * time.Hour)
	meta, err := d.UpdateCampaignMeta(c.ID, "达成外网打点", json.RawMessage(`{"note":"display only"}`), "recon", "active", &starts, &ends)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.Goal != "达成外网打点" || meta.Phase != "recon" || meta.Status != "active" {
		t.Fatalf("meta = %+v", meta)
	}
	if meta.StartsAt == nil || !meta.StartsAt.Equal(starts) {
		t.Fatalf("starts_at = %v, want %v", meta.StartsAt, starts)
	}

	p1, err := d.CreateCampaignPhase(c.ID, "侦察", 0, "done", "只读", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateCampaignPhase(c.ID, "打点", 1, "active", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListCampaignPhases(c.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("phases = %v err=%v", list, err)
	}
	if _, err := d.UpdateCampaignPhase(c.ID, p1.ID, "侦察与枚举", 0, "done", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if n, err := d.DeleteCampaignPhase(c.ID, p1.ID); err != nil || n != 1 {
		t.Fatalf("delete phase = %d,%v", n, err)
	}
}

func TestSetTaskCampaignPhaseRequiresMembership(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()
	name := fmt.Sprintf("phase-task-%d", time.Now().UnixNano())
	c, err := d.CreateCampaign(name, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM campaigns WHERE id=$1`, c.ID) //nolint:errcheck
	p, err := d.CreateCampaignPhase(c.ID, "阶段", 0, "planned", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 不存在的任务 → 0 行（路由回 404）。
	if n, err := d.SetTaskCampaignPhase(c.ID, 999999999, p.ID); err != nil || n != 0 {
		t.Fatalf("missing task = %d,%v want 0,nil", n, err)
	}
}
