package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/selfupdate"
)

// releaseCache 是保护 GitHub 配额的那一层：未认证的 API 只有 60 次/小时/IP，
// 而顶栏的"有新版本"提示每次整页加载都会查一次。缓存一旦失效，用户多开几个
// 标签页就会把配额耗光，之后真想更新反而查不动。

func newTestCache(fetch func(context.Context, *http.Client) (*selfupdate.Release, error)) *releaseCache {
	return &releaseCache{fetch: fetch}
}

func TestReleaseCacheServesFromCache(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	for range 5 {
		rel, err := c.get(t.Context(), nil, false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rel.TagName != "v0.3.8" {
			t.Fatalf("TagName = %q", rel.TagName)
		}
	}
	if calls != 1 {
		t.Errorf("5 次查询只应回源 1 次，实际 %d 次", calls)
	}
}

func TestReleaseCacheForceBypasses(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// 用户点「检查更新」必须拿到实时结果，否则刚发布的版本要等缓存过期才看得见。
	if _, err := c.get(t.Context(), nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("force 应绕过缓存，期望回源 2 次，实际 %d 次", calls)
	}
}

func TestReleaseCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// 把落库时间往前拨到刚过期，模拟 TTL 到点。
	c.at = time.Now().Add(-releaseTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("TTL 过期后应重新回源，期望 2 次，实际 %d 次", calls)
	}
}

func TestReleaseCacheUsesShorterTTLForErrors(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return nil, errors.New("github 不可达")
	})

	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("期望返回错误")
	}
	// 失败结果也要缓存一会儿，否则 GitHub 不可达时每次页面加载都白等一次超时。
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("期望返回错误")
	}
	if calls != 1 {
		t.Errorf("错误应短时缓存，期望回源 1 次，实际 %d 次", calls)
	}

	// 但错误的 TTL 必须明显短于成功的，网络恢复后要能很快自愈。
	if releaseErrTTL >= releaseTTL {
		t.Fatalf("错误 TTL(%v) 必须短于成功 TTL(%v)", releaseErrTTL, releaseTTL)
	}
	c.at = time.Now().Add(-releaseErrTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("期望返回错误")
	}
	if calls != 2 {
		t.Errorf("错误 TTL 过期后应重试，期望 2 次，实际 %d 次", calls)
	}
}

func TestReleaseCacheDoesNotPoisonOnCallerCancel(t *testing.T) {
	good := &selfupdate.Release{TagName: "v0.3.8"}
	c := newTestCache(func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return good, nil
	})
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}

	// 访客关掉标签页会取消请求。那不代表 GitHub 有问题，绝不能把"已取消"
	// 写进缓存——否则接下来 30 分钟内每个访客都会收到一条莫名其妙的错误。
	c.fetch = func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return nil, ctx.Err()
	}
	c.at = time.Now().Add(-releaseTTL - time.Second) // 让缓存过期，逼它回源

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, nil, false); err == nil {
		t.Fatal("调用方已取消时应把错误透传给它")
	}

	// 关键不变量：被取消的那一次不留下任何痕迹——缓存里既没有"已取消"这个错误，
	// 也还保着上一次的好结果。
	if c.err != nil {
		t.Fatalf("取消错误不应写进缓存，得到 %v", c.err)
	}
	if c.rel == nil || c.rel.TagName != "v0.3.8" {
		t.Fatalf("缓存应保留上一次的好结果，得到 %+v", c.rel)
	}

	// 那次取消没换来任何新数据，所以下一个访客理应重新回源——而且能正常拿到结果，
	// 不会被上一次的取消连累。
	c.fetch = func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return good, nil
	}
	rel, err := c.get(t.Context(), nil, false)
	if err != nil {
		t.Fatalf("取消之后的正常请求不应报错: %v", err)
	}
	if rel == nil || rel.TagName != "v0.3.8" {
		t.Fatalf("应拿到正常结果，得到 %+v", rel)
	}
}

// 发布给他人使用时，更新接口不能把发布源地址带出去：错误里常常包着
// *url.Error 的完整请求地址，仅隐藏按钮不够。
func TestRedactUpdateSource(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"访问 GitHub 失败: Get \"https://api.github.com/repos/lin9527ya/blade/releases/latest\": EOF",
			"访问 GitHub 失败: Get \"https://api.github.com/repos/发布源/releases/latest\": EOF"},
		{"仓库 lin9527ya/blade 尚未发布任何正式版本", "仓库 发布源 尚未发布任何正式版本"},
		{"下载失败: https://github.com/lin9527ya/blade/releases/download/v1/x.zip", "下载失败: https://github.com/发布源/releases/download/v1/x.zip"},
	} {
		if got := redactUpdateSource(tc.in); got != tc.want {
			t.Errorf("redactUpdateSource(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if exposeUpdateSource {
		t.Fatal("exposeUpdateSource 默认必须关闭，否则更新日志/发布源会重新暴露")
	}
}

// 交付给他人时，/api/update/check 不能把发布源地址（repo / release 链接 / notes）
// 带出去；前端隐藏按钮只是第一层，接口本身也要干净。
func TestUpdateCheckHidesReleaseSource(t *testing.T) {
	_, _, request := trafficEvidenceServer(t)
	prev := relCache
	relCache = &releaseCache{fetch: func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return &selfupdate.Release{
			TagName: "v9.9.9",
			HTMLURL: "https://github.com/lin9527ya/blade/releases/tag/v9.9.9",
			Body:    "release notes for lin9527ya/blade",
		}, nil
	}}
	t.Cleanup(func() { relCache = prev })

	w := request("GET", "/api/update/check?force=1", "")
	if w.Code != 200 {
		t.Fatalf("update check: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, forbidden := range []string{"lin9527ya", "html_url", "notes", `"repo"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("update check leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, `"latest":"v9.9.9"`) {
		t.Fatalf("update check missing latest: %s", body)
	}
}
