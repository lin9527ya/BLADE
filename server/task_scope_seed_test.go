package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// IP 目标在 target 策略下也要按公司归因扩面：只写 ip 范围会把任务永远锁在单 IP。
func TestApplySeedScopeIPTargetAddsCompanyScope(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	domain := "seed-ip-" + tag + ".example"
	octet := (time.Now().UnixNano() / 250) % 200
	ip := fmt.Sprintf("203.0.%d.%d", octet+1, time.Now().UnixNano()%200+1)
	cidr := fmt.Sprintf("203.0.%d.0/24", octet+1)
	comp, _, err := d.Companies().UpsertCompany("seed-ip-co-"+tag, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Companies().DeleteCompany(comp) })
	if _, _, _, errs := d.Companies().AddScope(comp, []string{domain, cidr}, "seed ip test"); len(errs) > 0 {
		t.Fatalf("add company scope: %v", errs)
	}

	task, err := d.CreateTaskWithOptions("ip target", "goal", db.TaskCreateOptions{AssetScopePolicy: db.AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })

	applySeedScope(d.Assets(), task.ID, ip, db.AssetScopePolicyTarget, nil)

	rows, err := d.Assets().ListTaskScope(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasIP, hasCompany := false, false
	for _, r := range rows {
		if r.Kind == "ip" {
			hasIP = true
		}
		if r.Kind == "company" && r.CompanyID != nil && *r.CompanyID == comp {
			hasCompany = true
		}
	}
	if !hasIP || !hasCompany {
		t.Fatalf("IP target 应写入 ip + 公司范围：%+v", rows)
	}
	if ok, err := d.HostInDeclaredScope(task.ID, "new."+domain); err != nil || !ok {
		t.Fatalf("IP 目标应通过公司范围授权同公司主机 ok=%v err=%v", ok, err)
	}
}
