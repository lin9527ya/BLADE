package main

import (
	"context"
	"testing"

	"github.com/lin9527ya/artex/agent"
)

func TestShutdownContextPreservesNamedCause(t *testing.T) {
	signalCtx, signalCancel := context.WithCancel(context.Background())
	ctx, shutdown := shutdownContext(signalCtx)
	defer shutdown(nil)
	signalCancel()
	<-ctx.Done()
	if code, _, _, ok := agent.AbortReason(ctx); !ok || code != "shutdown" {
		t.Fatalf("code=%q ok=%v, want shutdown", code, ok)
	}
}

// 暴露面护栏的判定（P42）：这个函数判错的两种后果都很糟——判宽了会放过开放代理，
// 判严了会让正常部署起不来。所以把两边都钉住。
func TestListenIsLoopback(t *testing.T) {
	loopback := []string{
		"127.0.0.1:8787", "127.0.0.5:1", "localhost:8788", "[::1]:8787", "::1",
	}
	for _, addr := range loopback {
		if !listenIsLoopback(addr) {
			t.Errorf("%q 该判为只绑本机", addr)
		}
	}
	exposed := []string{
		"", ":8787", "0.0.0.0:8787", "[::]:8787", "::", "192.168.1.10:8787",
		"10.0.0.1:8788", "example.com:8787", "8787", // 裸端口交给 net.Listen 判，这里保守当暴露
	}
	for _, addr := range exposed {
		if listenIsLoopback(addr) {
			t.Errorf("%q 不该判为只绑本机（判宽了会放过开放代理）", addr)
		}
	}
}
