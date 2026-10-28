package server

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/intercept"
)

// validateSettingsRequest 是 putSettings 的**纯校验阶段**：在任何写库与运行态动作
// 之前跑完。
//
// 此前校验分散在各写库分支里：后面的字段非法时，前面的设置已经落库、任务并发/代理/
// 搜索等运行态已经被触发——用户看到 400，实际却改了一半。这个函数负责「先全部验完」，
// 各分支里原有的校验保留为第二道保险（防漏字段）；新增字段时两处都要加。
func (s *Server) validateSettingsRequest(req settingsRequest) error {
	if req.ScopeMode != nil {
		switch strings.TrimSpace(*req.ScopeMode) {
		case "off", "ask", "deny":
		default:
			return fmt.Errorf("授权范围模式必须是 off / ask / deny")
		}
	}
	if req.ScopeHours != nil {
		if err := intercept.ValidateScopeHours(strings.TrimSpace(*req.ScopeHours)); err != nil {
			return err
		}
	}
	if req.ArtifactLanguage != nil {
		v := strings.TrimSpace(*req.ArtifactLanguage)
		if v == "" {
			v = agent.ArtifactLangZh
		}
		if v != agent.ArtifactLangZh && v != agent.ArtifactLangEn {
			return fmt.Errorf("产物语言必须是 zh / en（zh = 不约束，en = 面向目标的产物一律英文）")
		}
	}
	if req.CredentialBudget != nil {
		if v := *req.CredentialBudget; v < 0 || v > 1000 {
			return fmt.Errorf("凭据尝试预算必须是 0–1000（0 = 不限）")
		}
	}
	if req.CredentialBudgetAction != nil {
		switch strings.TrimSpace(*req.CredentialBudgetAction) {
		case "allow", "ask", "deny":
		default:
			return fmt.Errorf("超预算处置必须是 allow / ask / deny")
		}
	}
	if req.CORSAllowedOrigins != nil {
		if utf8.RuneCountInString(strings.TrimSpace(*req.CORSAllowedOrigins)) > 500 {
			return fmt.Errorf("CORS 来源列表过长（最多 500 字）")
		}
	}
	if req.SiteName != nil {
		if utf8.RuneCountInString(strings.TrimSpace(*req.SiteName)) > 60 {
			return fmt.Errorf("显示名过长（最多 60 字）")
		}
	}
	if req.NotifyBaseURL != nil {
		base := trimTrailingSlash(strings.TrimSpace(*req.NotifyBaseURL))
		if base != "" && !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
			return fmt.Errorf("回链地址需以 http:// 或 https:// 开头")
		}
	}
	if req.NotifyDigestMins != nil {
		if v := *req.NotifyDigestMins; v < 1 || v > 24*60 {
			return fmt.Errorf("汇总周期需在 1 到 1440 分钟之间")
		}
	}
	if req.IntelDailyQuota != nil {
		if v := *req.IntelDailyQuota; v < 0 || v > 100000 {
			return fmt.Errorf("情报每日上限需在 0（不限）到 100000 之间")
		}
	}
	// 资源限额与存储保留：与 putSettings 里的写库表保持同一份上限。
	for _, f := range []struct {
		val  *int
		max  int
		name string
	}{
		{req.ExecCPUSeconds, 86400, "CPU 限额（秒）"},
		{req.ExecMemoryMB, 1048576, "内存限额（MB）"},
		{req.ExecMaxProcs, 65536, "进程数限额"},
		{req.ExecMaxActive, 4096, "同时执行数上限"},
		{req.ExecMaxAge, 525600, "单条执行最大存活（分钟）"},
		{req.MCPMaxConcurrent, 128, "MCP 并发上限"},
		{req.MCPBreakerFails, 1000, "MCP 熔断阈值"},
		{req.MCPBreakerCooldown, 86400, "MCP 熔断冷却（秒）"},
		{req.StorageRetentionTasks, 3650, "任务产物保留天数"},
		{req.StorageRetentionSessions, 3650, "会话目录保留天数"},
		{req.StorageRetentionDrafts, 3650, "草稿保留天数"},
		{req.StorageRetentionTranscript, 3650, "转写保留天数"},
		{req.StorageRetentionBinary, 3650, "更新残留保留天数"},
		{req.RetentionLLMUsage, 3650, "LLM 用量台账保留天数"},
		{req.RetentionActivity, 3650, "执行活动流保留天数"},
		{req.RetentionServerLogs, 3650, "后端日志保留天数"},
		{req.RetentionLLMRecordBodys, 3650, "LLM 录制载荷保留天数"},
		{req.RetentionLLMRecords, 3650, "LLM 录制整行保留天数"},
		{req.RetentionTraffic, 3650, "流量归档保留天数"},
	} {
		if f.val == nil {
			continue
		}
		if *f.val < 0 || *f.val > f.max {
			return fmt.Errorf("%s 需在 0（不限）到 %d 之间", f.name, f.max)
		}
	}
	if req.Workers != nil && *req.Workers <= 0 {
		return fmt.Errorf("workers 必须 >0")
	}
	if req.OpsecNoise != nil {
		switch strings.ToLower(strings.TrimSpace(*req.OpsecNoise)) {
		case "normal", "quiet":
		default:
			return fmt.Errorf("opsec_noise_level 只能是 normal 或 quiet")
		}
	}
	if req.ProxyEgressIPURL != nil {
		if _, err := normalizeEgressIPURL(*req.ProxyEgressIPURL); err != nil {
			return err
		}
	}
	if req.GlobalProxy != nil || req.GlobalProxyPool != nil || req.ProxyHealthURL != nil ||
		req.ProxyHealthTimeout != nil || req.ProxyHealthInterval != nil ||
		req.ProxyFailThreshold != nil || req.ProxySwitchCooldown != nil {
		// 与写库分支同样的「未给字段用当前值兜底」构造，只做校验、不落库。
		cfg := s.m.ProxyPoolConfig()
		switch {
		case req.GlobalProxyPool != nil:
			cfg.Candidates = splitProxyCandidates(*req.GlobalProxyPool)
		case req.GlobalProxy != nil:
			cfg.Candidates = splitProxyCandidates(*req.GlobalProxy)
		}
		if req.ProxyHealthURL != nil {
			cfg.HealthURL = strings.TrimSpace(*req.ProxyHealthURL)
		}
		if req.ProxyHealthTimeout != nil {
			cfg.Timeout = time.Duration(*req.ProxyHealthTimeout) * time.Second
		}
		if req.ProxyHealthInterval != nil {
			cfg.Interval = time.Duration(*req.ProxyHealthInterval) * time.Second
		}
		if req.ProxyFailThreshold != nil {
			cfg.FailThreshold = *req.ProxyFailThreshold
		}
		if req.ProxySwitchCooldown != nil {
			cfg.Cooldown = time.Duration(*req.ProxySwitchCooldown) * time.Second
		}
		if _, err := NormalizeProxyPoolConfig(cfg); err != nil {
			return err
		}
	}
	if req.WebSearchBackend != nil {
		// 未知后端此前会被原样存下、运行期再静默失效；保存时当场拒掉。
		switch strings.TrimSpace(*req.WebSearchBackend) {
		case "", defaultWebSearchBackend, "brave-free", "tavily",
			deepSeekWebSearchBackend, opencodeWebSearchBackend, commandCodeWebSearchBackend:
		default:
			return fmt.Errorf("未知的搜索后端 %q", strings.TrimSpace(*req.WebSearchBackend))
		}
	}
	return nil
}
