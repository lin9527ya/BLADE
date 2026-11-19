package db

import (
	"fmt"
	"testing"
	"time"
)

func TestCampaignContextExposesGoalAndSiblings(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	tag := fmt.Sprintf("ctx-%d", time.Now().UnixNano())
	c, err := d.CreateCampaign("campaign-"+tag, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteCampaign(c.ID) })
	t1, err := d.CreateTaskWithOptions("a-"+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := d.CreateTaskWithOptions("b-"+tag, "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(t1.ID); _ = d.DeleteTask(t2.ID) })
	if err := d.SetTaskCampaign(t1.ID, &c.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.SetTaskCampaign(t2.ID, &c.ID); err != nil {
		t.Fatal(err)
	}
	ctx, err := d.Exploration(t1.ExplorationID).CampaignContext()
	if err != nil || ctx == nil || ctx.CampaignID != c.ID || ctx.TaskCount != 2 || len(ctx.Tasks) != 2 {
		t.Fatalf("campaign context incomplete: %+v err=%v", ctx, err)
	}
}
