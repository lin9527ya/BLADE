package db

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

// TestHostsByTaskWithSourcesScopeKinds 钉住「按范围取主机」的语义：六种 scope 行
// （company / root_domain / subdomain / ip / cidr / icp）各自把哪些资产算进本任务，
// 以及每个匹配资产贡献哪些主机串（domain、ip、url 的 hostname 各算一个）。
//
// 它是 D7 那次改写（5 路 OR 连接 → 按 kind 的 UNION，好让每支各走索引）的**等价性测试**：
// 改写前后必须返回完全相同的集合——否则"优化"改掉的就是范围判定本身。
func TestHostsByTaskWithSourcesScopeKinds(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })

	tag := fmt.Sprintf("d7-%d", time.Now().UnixNano())
	rd := tag + "-rd.example.com"
	sub := tag + "-sub.example.com"
	ipExact := "10.77.1.1"
	cidrIP := "10.78.0.9"
	cidrNet := "10.78.0.0/24"
	icp := "京icp" + tag

	// ip 资产用的是合法固定 IP，不带 tag——必须按值精确清，否则上一轮的残留会撞
	// uq_av2_ip 唯一索引（本用例第一次写就是这么红的）。
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE $1 OR root_domain LIKE $1`, tag+"%")
		_, _ = d.Exec(`DELETE FROM assets WHERE ip IN ($1,$2)`, ipExact, cidrIP)
		_, _ = d.Exec(`DELETE FROM task_scope WHERE reason=$1`, tag)
	})

	comp, _, err := d.Companies().UpsertCompany("d7co-"+tag, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Companies().DeleteCompany(comp) })

	task, err := d.CreateTaskWithOptions("d7 scope kinds", "d7", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })

	insertScope := func(kind string, companyID *int64, domain, net, value string) {
		t.Helper()
		if _, err := d.Exec(`INSERT INTO task_scope(task_id,kind,company_id,domain,net,value,source,reason)
			VALUES($1,$2,$3,NULLIF($4,'')::text,NULLIF($5,'')::cidr,NULLIF($6,'')::text,'manual',$7)`,
			task.ID, kind, companyID, domain, net, value, tag); err != nil {
			t.Fatalf("insert scope %s: %v", kind, err)
		}
	}
	insertScope("company", &comp, "", "", "")
	insertScope("root_domain", nil, rd, "", "")
	insertScope("subdomain", nil, sub, "", "")
	insertScope("ip", nil, "", ipExact+"/32", "")
	insertScope("cidr", nil, "", cidrNet, "")
	insertScope("icp", nil, "", "", icp)

	insertAsset := func(typ, domain, rootDomain, ip, url, icpVal string, companyID *int64) {
		t.Helper()
		if _, err := d.Exec(`INSERT INTO assets(type,domain,root_domain,ip,url,icp,company_id)
			VALUES($1,NULLIF($2,'')::text,NULLIF($3,'')::text,NULLIF($4,'')::text,NULLIF($5,'')::text,NULLIF($6,'')::text,$7)`,
			typ, domain, rootDomain, ip, url, icpVal, companyID); err != nil {
			t.Fatalf("insert asset %s: %v", domain, err)
		}
	}
	// 每个 scope 各命中一个资产；最后一个谁都不命中。
	insertAsset("subdomain", "a."+rd, rd, "", "", "", nil)                             // ← root_domain 范围
	insertAsset("subdomain", sub, tag+"-other.com", "", "", "", nil)                   // ← subdomain 范围
	insertAsset("ip", "", "", ipExact, "", "", nil)                                    // ← ip 范围
	insertAsset("ip", "", "", cidrIP, "", "", nil)                                     // ← cidr 范围
	insertAsset("subdomain", tag+"-icp.example.com", "", "", "", "京 ICP "+tag, nil)    // ← icp 范围（含空格，测归一化）
	insertAsset("app", "", "", "", "https://app."+tag+".example.com/login", "", &comp) // ← company 范围（url 取 hostname）
	insertAsset("subdomain", tag+"-nomatch.example.com", "nowhere.example.com", "", "", "", nil)

	got, err := d.Assets().HostsByTaskWithSources(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"10.77.1.1",
		"10.78.0.9",
		"a." + rd,
		"app." + tag + ".example.com",
		sub,
		tag + "-icp.example.com",
	}
	// 排序无关：函数已排序，这里也排一遍再比——免得将来排序实现变化被误判成语义变化。
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("按范围取到的主机集不符\n got=%v\nwant=%v\n（六种 scope 行必须各自命中其资产；改写实现不得改变这个集合）", got, want)
	}
}
