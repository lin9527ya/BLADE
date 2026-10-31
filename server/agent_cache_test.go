package server

import (
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

// TestAgentRowCacheHonoursTTLAndInvalidation 钉住缓存的三个性质：
// 命中返回副本、过期回源、失效立即生效。
//
// 为什么缓存值得单测：它挡的是 worker 循环里每 0.8s 一次的 agent 行查询；
// 但它一旦不失效，用户改了 agent 配置却「半天不生效」——比慢更难查。
func TestAgentRowCacheHonoursTTLAndInvalidation(t *testing.T) {
	c := newAgentRowCache()
	row := &db.Agent{Key: "worker", MaxTurns: 7}
	c.put("worker", row)

	got := c.get("worker")
	if got == nil || got.MaxTurns != 7 {
		t.Fatalf("缓存未命中：%+v", got)
	}
	// 返回副本：调用方改它不该污染缓存。
	got.MaxTurns = 99
	if again := c.get("worker"); again.MaxTurns != 7 {
		t.Fatal("缓存返回了共享指针，调用方的改动串进了缓存")
	}

	// 失效后立即回源。
	c.invalidate()
	if c.get("worker") != nil {
		t.Fatal("invalidate 之后仍然命中")
	}

	// 过期后回源（这里直接改过期时间，避免测试里 sleep 5 秒）。
	c.put("worker", row)
	c.mu.Lock()
	e := c.entries["worker"]
	e.expires = time.Now().Add(-time.Second)
	c.entries["worker"] = e
	c.mu.Unlock()
	if c.get("worker") != nil {
		t.Fatal("过期条目仍然命中")
	}
}
