package db

import (
	"fmt"
	"testing"
	"time"
)

// 新策略的核心语义：target=目标所属全资产（同根域/同公司跨根域/本任务发现），
// exact=只认明确登记的主机；切回 exact 只移除策略自动写入的行。
func TestAssetScopePolicyTargetVisibilityAndSwitch(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	tag := fmt.Sprintf("policy-%d", time.Now().UnixNano())
	as := d.Assets()
	comp, _, err := d.Companies().UpsertCompany("policyco-"+tag, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Companies().DeleteCompany(comp) })

	task, err := d.CreateTaskWithOptions("policy target", "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })

	root := tag + ".example"
	// 目标精确子域（seed 等价物）+ 手工范围。
	if _, err := as.AddAgentScope(task.ID, "subdomain", "www."+root, "任务目标", "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err := as.AddAgentScope(task.ID, "subdomain", "manual."+root, "手工", "manual"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"www." + root, "manual." + root} {
		if _, err := d.Exec(`INSERT INTO assets(type,domain,root_domain) VALUES('subdomain',$1,$2)`, host, root); err != nil {
			t.Fatal(err)
		}
	}
	// 目标同公司、不同根域（company 归因）。
	if _, err := d.Exec(`INSERT INTO assets(type,domain,root_domain,company_id) VALUES('subdomain',$1,$2,$3)`,
		"app."+tag+".cross.example", tag+".cross.example", comp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE $1`, tag+"%") })
	// 通过目标发现、已关联本任务但尚未登记 scope 的第三方根域资产。
	discovered := "cloud." + tag + ".disc.example"
	if _, err := d.Exec(`INSERT INTO assets(type,domain,root_domain,task_ids) VALUES('subdomain',$1,$2,ARRAY[$3]::bigint[])`,
		discovered, tag+".disc.example", task.ID); err != nil {
		t.Fatal(err)
	}
	unrelated := "other." + tag + ".other.example"
	if _, err := d.Exec(`INSERT INTO assets(type,domain,root_domain) VALUES('subdomain',$1,$2)`,
		unrelated, tag+".other.example"); err != nil {
		t.Fatal(err)
	}

	if _, err := as.SetTaskAssetScopePolicy(task.ID, AssetScopePolicyTarget, []string{"www." + root}, []int64{comp}); err != nil {
		t.Fatal(err)
	}
	if got := as.TaskAssetScopePolicy(task.ID); got != AssetScopePolicyTarget {
		t.Fatalf("policy=%s", got)
	}
	visible, err := as.QueryDSLInScope("", "", task.ID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range visible {
		seen[a.Domain] = true
	}
	for _, want := range []string{"www." + root, "manual." + root, "app." + tag + ".cross.example", discovered} {
		if !seen[want] {
			t.Fatalf("target 策略应可见 %s，实际 %v", want, seen)
		}
	}
	if seen[unrelated] {
		t.Fatalf("未归因的第三方根域不应可见: %v", seen)
	}
	if ok, err := d.HostInDeclaredScope(task.ID, discovered); err != nil || !ok {
		t.Fatalf("发现资产应在 RoE 范围内 ok=%v err=%v", ok, err)
	}
	if ok, err := d.HostInDeclaredScope(task.ID, unrelated); err != nil || ok {
		t.Fatalf("未归因资产不应在 RoE 范围内 ok=%v err=%v", ok, err)
	}

	// 切回 exact：策略行删除，手工行保留；发现资产不再由策略可见。
	if _, err := as.SetTaskAssetScopePolicy(task.ID, AssetScopePolicyExact, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := as.TaskAssetScopePolicy(task.ID); got != AssetScopePolicyExact {
		t.Fatalf("policy=%s", got)
	}
	scopeRows, err := as.ListTaskScope(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasManual := false
	for _, r := range scopeRows {
		if r.Source == "policy" {
			t.Fatalf("exact 后不应残留策略行: %+v", r)
		}
		if r.Kind == "subdomain" && r.Domain == "manual."+root {
			hasManual = true
		}
	}
	if !hasManual {
		t.Fatalf("手工范围行被误删: %+v", scopeRows)
	}
	visible, err = as.QueryDSLInScope("", "", task.ID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range visible {
		if a.Domain == discovered {
			t.Fatalf("exact 策略下发现资产不应仅凭 task_ids 可见")
		}
	}
}

// 公司范围应能授权“尚未登记到资产图”的同公司主机：否则 target 任务在 worker
// insert_assets 之前访问 example.net 的新子域会被 RoE 误判越界。
func TestCompanyScopeAuthorizesUnregisteredHost(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	tag := fmt.Sprintf("companyscope-%d", time.Now().UnixNano())
	domain := tag + ".example"
	netRange := "203.0.113.0/24"
	comp, _, err := d.Companies().UpsertCompany("companyscope-"+tag, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Companies().DeleteCompany(comp) })
	if _, _, _, errs := d.Companies().AddScope(comp, []string{domain, netRange}, "test company scope"); len(errs) > 0 {
		t.Fatalf("add company scope: %v", errs)
	}

	task, err := d.CreateTaskWithOptions("company scope", "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	as := d.Assets()
	if _, err := as.AddAgentScope(task.ID, "company", fmt.Sprintf("%d", comp), "test", "manual"); err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"new." + domain, "deep.new." + domain, "203.0.113.55"} {
		if ok, err := d.HostInDeclaredScope(task.ID, host); err != nil || !ok {
			t.Fatalf("%s 应被公司范围授权 ok=%v err=%v", host, ok, err)
		}
	}
	for _, host := range []string{"unrelated.example", "203.0.114.55"} {
		if ok, err := d.HostInDeclaredScope(task.ID, host); err != nil || ok {
			t.Fatalf("%s 不应被公司范围授权 ok=%v err=%v", host, ok, err)
		}
	}
}

// target 策略的可见范围应包含直接关联任务发现/锚定的资产，否则 list_assets 与
// add_intent 看不到来源任务已经交给本任务继承的线索。
func TestTargetScopeIncludesDirectSourceTaskAssets(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	tag := fmt.Sprintf("sourcectx-%d", time.Now().UnixNano())
	mainTask, err := d.CreateTaskWithOptions("target main", "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyTarget})
	if err != nil {
		t.Fatal(err)
	}
	sourceTask, err := d.CreateTaskWithOptions("source", "goal", TaskCreateOptions{AssetScopePolicy: AssetScopePolicyExact})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(mainTask.ID); _ = d.DeleteTask(sourceTask.ID) })
	if _, err := d.Exec(`INSERT INTO task_relations(task_id,source_task_id) VALUES($1,$2)`, mainTask.ID, sourceTask.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE $1`, tag+"%")
	})

	linkedHost := "linked." + tag + ".example"
	anchoredHost := "anchored." + tag + ".example"
	var linkedID, anchoredID int64
	if err := d.QueryRow(`INSERT INTO assets(type,domain,root_domain,task_ids) VALUES('subdomain',$1,$2,ARRAY[$3]::bigint[]) RETURNING id`,
		linkedHost, tag+".example", sourceTask.ID).Scan(&linkedID); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`INSERT INTO assets(type,domain,root_domain) VALUES('subdomain',$1,$2) RETURNING id`,
		anchoredHost, tag+".example").Scan(&anchoredID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(sourceTask.ExplorationID).AddNode(KindFact, map[string]any{"summary": "source discovered"}, 0, "confirmed", "worker", []int64{anchoredID}); err != nil {
		t.Fatal(err)
	}

	visible, err := d.Assets().QueryDSLInScope("", "", mainTask.ID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, a := range visible {
		seen[a.Domain] = true
	}
	if !seen[linkedHost] || !seen[anchoredHost] {
		t.Fatalf("target 主任务应继承直接来源任务的 task_ids/anchors：%v", seen)
	}
	for _, host := range []string{linkedHost, anchoredHost} {
		if ok, err := d.HostInDeclaredScope(mainTask.ID, host); err != nil || !ok {
			t.Fatalf("%s 应进入 RoE 范围 ok=%v err=%v", host, ok, err)
		}
	}
}
