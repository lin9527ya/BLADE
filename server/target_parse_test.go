package server

import "testing"

func TestParseTargetStripsPastedInstructions(t *testing.T) {
	cases := []struct {
		text   string
		scheme string
		host   string
		port   int
	}{
		{
			"https://www.esm-computer.de，本地用了全局代理，你要学会通过doh等方式获取目标的真实ip，严禁使用直连的方式暴露本机真实ip，禁止测试xss。",
			"https", "www.esm-computer.de", 443,
		},
		{
			"www.donic.com，优先探测目标指纹，使用2026年cve进行利用",
			"https", "www.donic.com", 443,
		},
		{
			"https://www.mykastar.com，对该网站进行渗透测试，获取shell，可以参考cve-2026-75650，",
			"https", "www.mykastar.com", 443,
		},
	}
	for _, c := range cases {
		scheme, host, port, ok := parseTarget(c.text)
		if !ok || scheme != c.scheme || host != c.host || port != c.port {
			t.Errorf("parseTarget(%q) = (%q,%q,%d,%v), want (%q,%q,%d,true)",
				c.text, scheme, host, port, ok, c.scheme, c.host, c.port)
		}
	}
}
