package db

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestMiniAppPlaybookAndSkillVisibilitySeed(t *testing.T) {
	vis := builtinSkillVisibility["mini-program"]
	if !slices.Contains(vis, "worker") || !slices.Contains(vis, "pentest") {
		t.Fatalf("expected mini-program bound to worker and pentest, got %v", vis)
	}

	dsn := testDSN(t)
	d, err := Open(dsn)
	if err != nil {
		if os.Getenv("BLADE_REQUIRE_TESTS") == "1" {
			t.Fatalf("postgres unavailable (%v)", err)
		}
		t.Skipf("postgres unavailable (%v)", err)
	}
	defer d.Close()

	// Simulate an upgraded DB where prior mobile playbook flag was already done
	if err := d.SetSetting("task_playbooks_mobile_v1", "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`DELETE FROM settings WHERE key='task_playbooks_miniapp_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := d.seedMiniAppTaskPlaybooks(); err != nil {
		t.Fatalf("seedMiniAppTaskPlaybooks failed: %v", err)
	}
	// Second invocation must stay idempotent without duplicating templates
	if err := d.seedMiniAppTaskPlaybooks(); err != nil {
		t.Fatalf("second seedMiniAppTaskPlaybooks failed: %v", err)
	}
	tpls, err := d.ListTaskTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var count int
	for _, tpl := range tpls {
		if strings.Contains(tpl.Name, "微信小程序") {
			count++
		}
	}
	if count != len(builtinMiniAppPlaybooks) {
		t.Fatalf("expected %d miniapp playbook(s), got %d", len(builtinMiniAppPlaybooks), count)
	}
}
