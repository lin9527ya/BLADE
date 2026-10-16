package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/mcphttp"
)

// P24 把设置里的 MCP 守卫参数接到 mcphttp 的全局解析器上（每调用现读：改设置立即生效，
// 不需要重连 MCP）。
func (s *Server) installMCPGuard() {
	mcphttp.SetGuardConfigResolver(func(server string) mcphttp.GuardConfig {
		return mcphttp.GuardConfig{
			MaxConcurrent:    s.mcpGuardSetting(settingMCPMaxConcurrent, defaultMCPMaxConcurrent),
			BreakerThreshold: s.mcpGuardSetting(settingMCPBreakerFails, defaultMCPBreakerFails),
			BreakerCooldown:  time.Duration(s.mcpGuardSetting(settingMCPBreakerCooldown, defaultMCPBreakerCooldown)) * time.Second,
		}
	})
}

// mcpGuardSetting 读一个非负整数设置（缺失/坏值 → 默认）。
func (s *Server) mcpGuardSetting(key string, def int) int {
	v, ok, _ := s.m.pg.GetSetting(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return def
	}
	return n
}
