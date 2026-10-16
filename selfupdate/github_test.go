package selfupdate

import (
	"net/http"
	"testing"
)

// TestAuthTransportAddsTokenOnlyForGitHubHosts 钉住 token 的转发边界：
// 私有仓库的 release 接口需要它，但下载会 302 到对象存储——**把 token 转发给那些主机
// 等于把它交给第三方**，所以只在 GitHub 自己的主机上加。
func TestAuthTransportAddsTokenOnlyForGitHubHosts(t *testing.T) {
	var gotAuth, gotHost string
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotAuth, gotHost = req.Header.Get("Authorization"), req.URL.Host
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	c := &http.Client{Transport: authTransport{base: base, token: "ghp_secret"}}
	send := func(raw string) {
		gotAuth, gotHost = "", ""
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if _, err := c.Do(req); err != nil {
			t.Fatal(err)
		}
	}
	send("https://api.github.com/repos/lin9527ya/blade/releases/latest")
	if gotAuth != "Bearer ghp_secret" {
		t.Fatalf("API 主机必须带 token，实际 %q", gotAuth)
	}
	send("https://github.com/lin9527ya/blade/releases/download/v1/x.tar.gz")
	if gotAuth != "Bearer ghp_secret" {
		t.Fatalf("github.com 下载入口必须带 token，实际 %q", gotAuth)
	}
	send("https://objects.githubusercontent.com/signed/asset")
	if gotAuth != "" {
		t.Fatalf("对象存储（重定向目标）绝不能收到 token，实际 %q", gotAuth)
	}
	// 空 token = 完全不加头（公开源的老行为不变）
	c2 := &http.Client{Transport: authTransport{base: base}}
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/x", nil)
	_, _ = c2.Do(req)
	if gotAuth != "" {
		t.Fatalf("未配 token 时不该加头，实际 %q", gotAuth)
	}
	_ = gotHost
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
