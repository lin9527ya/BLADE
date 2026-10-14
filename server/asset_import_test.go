package server

import (
	"strings"
	"testing"
)

// 这些用例全部落在纯解析层（不连库）：导入会写库，判定错了代价是脏资产，
// 所以形状判定这一层要能被单独钉住。

func itemTypes(items []assetImportItem) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		out[it.Value] = it.Type
	}
	return out
}

func TestParseAssetCSVClassifiesByShape(t *testing.T) {
	in := "example.com\napi.example.com\n10.0.0.1\nhttps://app.example.com:8443/login\n"
	got := parseAssetCSV(in)
	want := map[string]string{
		"example.com":                        "root_domain",
		"api.example.com":                    "subdomain",
		"10.0.0.1":                           "ip",
		"https://app.example.com:8443/login": "service",
	}
	if diff := itemTypes(got.Items); len(diff) != len(want) {
		t.Fatalf("条数不对：got %v want %v", diff, want)
	}
	for k, v := range want {
		if itemTypes(got.Items)[k] != v {
			t.Errorf("%s 判成了 %q，期望 %q", k, itemTypes(got.Items)[k], v)
		}
	}
	if len(got.Skipped) != 0 || len(got.Warnings) != 0 {
		t.Errorf("不该有跳过/警告：skipped=%v warnings=%v", got.Skipped, got.Warnings)
	}
}

// 有表头时只取被点名的列——port 这类列不该被当成目标吞进来。
func TestParseAssetCSVHeaderSelectsColumnsOnly(t *testing.T) {
	in := "domain,ip,port\napi.example.com,10.0.0.1,443\n"
	got := parseAssetCSV(in)
	types := itemTypes(got.Items)
	if len(types) != 2 {
		t.Fatalf("只该取 domain 与 ip 两列，得到 %v", types)
	}
	if types["api.example.com"] != "subdomain" || types["10.0.0.1"] != "ip" {
		t.Fatalf("判定不对：%v", types)
	}
	if _, taken := types["443"]; taken {
		t.Error("port 列的 443 被当成目标了")
	}
}

// 表头点名了列就按列的类型判，不再猜形状。
func TestParseAssetCSVHeaderColumnForcesType(t *testing.T) {
	got := parseAssetCSV("ip,域名\n10.0.0.1,例子.中国\n")
	if len(got.Items) != 2 {
		t.Fatalf("两条都该进：%v skipped=%v", got.Items, got.Skipped)
	}
	for _, it := range got.Items {
		if it.Value == "10.0.0.1" && it.Type != "ip" {
			t.Errorf("ip 列判成了 %q", it.Type)
		}
	}
}

// 无表头时，纯数字（端口/计数）不该变成资产；类型词（type 列的取值）同理。
func TestParseAssetCSVRejectsNumbersAndTypeWords(t *testing.T) {
	got := parseAssetCSV("example.com,subdomain\n1234\n")
	if len(got.Items) != 1 || got.Items[0].Value != "example.com" {
		t.Fatalf("只该留下 example.com：%v", got.Items)
	}
	reasons := map[string]string{}
	for _, n := range got.Skipped {
		reasons[n.Value] = n.Reason
	}
	if !strings.Contains(reasons["subdomain"], "类型标注") {
		t.Errorf("subdomain 的理由不对：%q", reasons["subdomain"])
	}
	if !strings.Contains(reasons["1234"], "纯数字") {
		t.Errorf("1234 的理由不对：%q", reasons["1234"])
	}
}

// host:port 按主机导入，并把「端口没登记」如实说出来——不静默丢端口。
func TestParseAssetCSVHostPortKeepsHostAndWarns(t *testing.T) {
	got := parseAssetCSV("www.example.com:8443\n")
	if len(got.Items) != 1 || got.Items[0].Type != "subdomain" {
		t.Fatalf("该按子域导入：%v", got.Items)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0].Reason, "8443") {
		t.Fatalf("该有一条端口未登记的警告：%v", got.Warnings)
	}
}

// IPv6 字面量的冒号不止一个，不能被 host:port 那条支路切坏。
func TestParseAssetCSVDoesNotSplitIPv6(t *testing.T) {
	got := parseAssetCSV("2001:db8::1\n::1\n")
	if len(got.Items) != 2 {
		t.Fatalf("两个 IPv6 都该进：%v skipped=%v", got.Items, got.Skipped)
	}
	for _, it := range got.Items {
		if it.Type != "ip" {
			t.Errorf("%s 判成了 %q，期望 ip", it.Value, it.Type)
		}
	}
}

// Excel 导出的 BOM 与 # 注释行都要能正确处理。
func TestParseAssetCSVStripsBOMAndSkipsComments(t *testing.T) {
	got := parseAssetCSV(utf8BOM + "# 这是注释\nexample.com\n")
	if len(got.Items) != 1 || got.Items[0].Value != "example.com" {
		t.Fatalf("BOM 或注释没处理对：%v skipped=%v", got.Items, got.Skipped)
	}
}

// 表内重复按自然键折掉（大小写不同也算同一条），并如实计数。
func TestParseAssetCSVFoldsDuplicates(t *testing.T) {
	got := parseAssetCSV("example.com\nEXAMPLE.COM\n")
	if len(got.Items) != 1 {
		t.Fatalf("重复该被折掉：%v", got.Items)
	}
	if got.Dupes != 1 {
		t.Errorf("重复计数应为 1，得到 %d", got.Dupes)
	}
}

// 非 http/https 的 scheme 不是 HTTP 服务，不登记。
func TestParseAssetCSVRejectsNonHTTPScheme(t *testing.T) {
	got := parseAssetCSV("ftp://example.com\n")
	if len(got.Items) != 0 {
		t.Fatalf("ftp 不该进候选：%v", got.Items)
	}
	if len(got.Skipped) != 1 {
		t.Fatalf("该如实报出这一行：%v", got.Skipped)
	}
}

func TestClassifyTargetHeaderHints(t *testing.T) {
	cases := []struct {
		cell, hint, wantType string
		wantOK               bool
	}{
		{"example.com", "host", "root_domain", true},
		{"api.example.com", "host", "subdomain", true},
		{"10.0.0.1", "ip", "ip", true},
		{"not-an-ip", "ip", "", false},
		{"https://a.example.com", "url", "service", true},
		{"example.com", "url", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		got := classifyTarget(c.cell, c.hint)
		if got.ok != c.wantOK {
			t.Errorf("classifyTarget(%q,%q).ok = %v，期望 %v（reason=%q）", c.cell, c.hint, got.ok, c.wantOK, got.reason)
			continue
		}
		if got.ok && got.typ != c.wantType {
			t.Errorf("classifyTarget(%q,%q).typ = %q，期望 %q", c.cell, c.hint, got.typ, c.wantType)
		}
	}
}

// IP 必须排在主机名之前判：db.RootDomain 对 IP 字面量返回 isApex=true，
// 顺序反了 10.0.0.1 会被登记成根域。
func TestClassifyTargetOrdersIPBeforeHost(t *testing.T) {
	if got := classifyTarget("10.0.0.1", ""); got.typ != "ip" {
		t.Fatalf("IP 字面量被当成了 %q", got.typ)
	}
}

// host 列里出现 host:port 也要摘端口：DomainKey 不管端口，不摘的话端口会被并进域名
// 存下来，那条资产从此谁都匹配不上。
func TestParseAssetCSVHostColumnStripsPort(t *testing.T) {
	got := parseAssetCSV("domain\nwww.example.com:8443\n")
	if len(got.Items) != 1 {
		t.Fatalf("该留下一条：%v skipped=%v", got.Items, got.Skipped)
	}
	if got.Items[0].write != "www.example.com" {
		t.Fatalf("端口没被摘掉，落库值 = %q", got.Items[0].write)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("该有一条端口未登记的警告：%v", got.Warnings)
	}
}

// 表头点名了列也不免检：三项检查判的是「这能不能是一条目标」，不是「它像不像目标」，
// 免检只会把 `subdomain` 这种标注当成域名存下来。
func TestParseAssetCSVHostColumnStillRejectsTypeWords(t *testing.T) {
	got := parseAssetCSV("domain\nsubdomain\n")
	if len(got.Items) != 0 || len(got.Skipped) != 1 {
		t.Fatalf("表头列不该免检：items=%v skipped=%v", got.Items, got.Skipped)
	}
}
