package server

import "testing"

func TestRedactProxyURL(t *testing.T) {
	cases := map[string]string{
		"http://user:pass@127.0.0.1:7890": "http://127.0.0.1:7890",
		"socks5://u:p@proxy.example:1080": "socks5://proxy.example:1080",
		"http://127.0.0.1:17890":          "http://127.0.0.1:17890",
		"":                                "",
	}
	for in, want := range cases {
		if got := redactProxyURL(in); got != want {
			t.Errorf("redactProxyURL(%q) = %q, want %q", in, got, want)
		}
	}
}
