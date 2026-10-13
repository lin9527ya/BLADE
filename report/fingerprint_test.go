package report

import "testing"

// 归一化：方法大写、host 小写、默认端口去掉、尾斜杠去掉、查询只留键名（排序）。
func TestNormalizeEndpoint(t *testing.T) {
	cases := []struct{ method, url, want string }{
		{"get", "https://App.Example.com/api/User?id=1", "GET app.example.com/api/User?id"},
		{"GET", "https://app.example.com/api/user/?id=2&name=x", "GET app.example.com/api/user?id,name"},
		{"POST", "http://app.example.com:8080/x", "POST app.example.com:8080/x"},
		{"GET", "https://app.example.com:443/x", "GET app.example.com/x"},
		{"GET", "http://app.example.com:80/", "GET app.example.com/"},
		{"GET", "not a url", "GET not a url"},
	}
	for _, c := range cases {
		if got := NormalizeEndpoint(c.method, c.url); got != c.want {
			t.Fatalf("NormalizeEndpoint(%q,%q)=%q want %q", c.method, c.url, got, c.want)
		}
	}
}

// 同一漏洞换个人重报（不同 payload、不同措辞、端点顺序不同）→ 同一指纹；
// 换成别的参数名 / 别的路径 / 别的漏洞类型 → 不同指纹。
func TestFingerprintGroupsSameBug(t *testing.T) {
	a := Fingerprint("sqli", []EndpointRef{{"GET", "https://a.example.com/api/user?id=1"}})
	b := Fingerprint("sqli", []EndpointRef{{"GET", "https://A.example.com/api/user/?id=999"}})
	if a == "" || a != b {
		t.Fatalf("同一注入点应同指纹: %q vs %q", a, b)
	}
	different := []string{
		Fingerprint("sqli", []EndpointRef{{"GET", "https://a.example.com/api/user?q=1"}}),   // 换参数名
		Fingerprint("sqli", []EndpointRef{{"GET", "https://a.example.com/api/order?id=1"}}), // 换路径
		Fingerprint("xss", []EndpointRef{{"GET", "https://a.example.com/api/user?id=1"}}),   // 换类型
		Fingerprint("sqli", []EndpointRef{{"POST", "https://a.example.com/api/user"}}),      // 换方法
	}
	for _, d := range different {
		if d == a {
			t.Fatalf("不同的洞不该同指纹（%q）", d)
		}
	}
	// 多端点：集合相同（顺序不同）→ 同指纹。
	m1 := Fingerprint("sqli", []EndpointRef{{"GET", "https://a/x?id=1"}, {"GET", "https://a/y"}})
	m2 := Fingerprint("sqli", []EndpointRef{{"GET", "https://a/y"}, {"GET", "https://a/x?id=2"}})
	if m1 != m2 {
		t.Fatalf("端点集合相同应同指纹: %q vs %q", m1, m2)
	}
	// 没有端点 → 不算指纹（不参与归并）。
	if got := Fingerprint("sqli", nil); got != "" {
		t.Fatalf("没有绑定流量不该给指纹: %q", got)
	}
}
