package server

import "testing"

// 出口失败识别：WAF 指纹与"连接层没打通"都算；端口关闭（connection refused）不算——
// 它说明出口 IP 已经抵达目标，换出口毫无意义，端口扫描里到处都是这种输出。
func TestDetectEgressFailure(t *testing.T) {
	cases := []struct {
		text string
		sig  egressSignal
		hit  bool
	}{
		{"HTTP 403 from https://t.example/ Just a moment... cloudflare", egressSignalWAF, true},
		{"curl: (28) Failed to connect to https://t.example/ port 443 after 21000 ms: Connection timed out", egressSignalUnreachable, true},
		{"curl: (7) Failed to connect to t.example port 80: Connection refused", 0, false},
		{"code=000 sin=0 time=10.0", egressSignalUnreachable, true},
		{"502 Bad Gateway", egressSignalUnreachable, true},
		{"HTTP/1.1 200 OK\n<h1>welcome</h1>", 0, false},
		{"nmap: host seems down", 0, false},
	}
	for _, c := range cases {
		_, sig, hit := detectEgressFailure(c.text)
		if hit != c.hit || (hit && sig != c.sig) {
			t.Fatalf("detectEgressFailure(%q) = hit=%v sig=%v, want hit=%v sig=%v", c.text, hit, sig, c.hit, c.sig)
		}
	}
}
