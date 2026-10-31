package server

import (
	"sync"
	"time"

	"github.com/lin9527ya/blade/db"
)

// agentCacheTTL 是 agent 行缓存的兜底有效期。
//
// 为什么需要缓存：worker 循环每 200ms–1.5s 就要问一次「这个角色的 LLM 还可用吗」
// （engine.snapshotFor → taskRuntimeAvailable → agentBindingProvider →
// effectiveProfileForAgent → GetAgentByKey）。3 个 worker × N 个任务 × 每秒约 1.2 次
// 就是每秒几十条 SQL，全部打在 32 条连接的池子上——而这些行几乎从不变化。
//
// 为什么不只靠失效：写入路径有好几处（设置页、agent 编辑、编排工具），漏掉一处的
// 后果是「改了配置但半天不生效」。TTL 让漏失效只退化成最多 5 秒的延迟，而不是永久
// 陈旧。写入路径仍然显式失效，TTL 只是安全网。
const agentCacheTTL = 5 * time.Second

// agentRowCache 是 agent 表按 key 的进程内缓存（每个 Server 一份）。
//
// 存的是**副本**：调用方会往返回的结构体上挂东西，共享指针会把状态串到别的调用里。
type agentRowCache struct {
	mu      sync.RWMutex
	gen     uint64 // 每次失效自增；读到的旧代结果不回填
	entries map[string]agentRowEntry
}

type agentRowEntry struct {
	row     *db.Agent
	expires time.Time
	gen     uint64
}

func newAgentRowCache() *agentRowCache {
	return &agentRowCache{entries: map[string]agentRowEntry{}}
}

// get 返回缓存行；miss/过期/已被失效时返回 nil。
func (c *agentRowCache) get(key string) *db.Agent {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expires) {
		return nil
	}
	return copyAgent(e.row)
}

// put 写入一行；调用方持有该行的最新副本。
func (c *agentRowCache) put(key string, row *db.Agent) {
	if c == nil || row == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = agentRowEntry{row: copyAgent(row), expires: time.Now().Add(agentCacheTTL), gen: c.gen}
}

// invalidate 清空缓存（任何 agent 行写入之后调用）。
func (c *agentRowCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.entries = map[string]agentRowEntry{}
}

// copyAgent 浅拷贝一行 agent（字段都是值类型或不可变切片，浅拷贝足够）。
func copyAgent(a *db.Agent) *db.Agent {
	if a == nil {
		return nil
	}
	cp := *a
	if a.LLMProfileID != nil {
		v := *a.LLMProfileID
		cp.LLMProfileID = &v
	}
	return &cp
}

// agentRow 读取一行 agent，优先走进程内缓存。
//
// 所有**热路径**都应改用它；写入路径与需要强一致的读取（例如设置页保存后回读）
// 继续直接用 pg.GetAgentByKey。
func (s *Server) agentRow(key string) (*db.Agent, error) {
	if s == nil || s.m == nil || s.m.pg == nil {
		return nil, nil
	}
	if row := s.agentCache.get(key); row != nil {
		return row, nil
	}
	row, err := s.m.pg.GetAgentByKey(key)
	if err != nil {
		return nil, err
	}
	s.agentCache.put(key, row)
	return row, nil
}

// invalidateAgentRows 让下一次读取回源。任何写 agent 行的路径都应调用它。
func (s *Server) invalidateAgentRows() {
	if s == nil {
		return
	}
	s.agentCache.invalidate()
}
