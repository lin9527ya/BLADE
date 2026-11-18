package agent

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 真实回连冒烟：只在本机显式开启时运行（会向 oast.fun 注册并发出一次回调）。
// 用法：BLADE_OAST_LIVE=1 HTTP_PROXY=http://127.0.0.1:17890 HTTPS_PROXY=... go test ./agent -run TestLiveInteractshCallback -v
func TestLiveInteractshCallback(t *testing.T) {
	if os.Getenv("BLADE_OAST_LIVE") != "1" {
		t.Skip("未设置 BLADE_OAST_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	server := strings.TrimSpace(os.Getenv("BLADE_OAST_SERVER"))
	if server == "" {
		server = "oast.fun"
	}
	session, err := newInteractshSession(ctx, server)
	if err != nil {
		t.Fatalf("注册 %s 失败：%v", server, err)
	}
	marker := "cblive" + randomToken(6)
	callback := "http://" + session.URL() + "/" + marker
	t.Logf("callback=%s", callback)
	client := &http.Client{Timeout: 10 * time.Second}
	if resp, err := client.Get(callback); err != nil {
		t.Logf("回调请求返回错误（不一定影响记录）：%v", err)
	} else {
		_ = resp.Body.Close()
		t.Logf("回调 HTTP 状态：%d", resp.StatusCode)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		items, err := session.Poll(ctx)
		if err != nil {
			t.Logf("poll 失败：%v", err)
		}
		for _, it := range items {
			t.Logf("interaction: type=%s id=%s path=%s url=%s", it.Type, it.ID, it.Path, it.URL)
			hay := strings.ToLower(it.Path + " " + it.URL + " " + it.Domain)
			if strings.Contains(hay, marker) {
				t.Logf("真实回连成功：%s", marker)
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("60 秒内没有收到含 %s 的回调", marker)
}
