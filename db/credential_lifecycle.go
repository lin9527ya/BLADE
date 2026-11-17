package db

import (
	"strings"
	"time"
)

// 凭据生命周期：页面与筛选共用的纯函数分类。它不是新的权威状态——
// 权威仍是节点 state + credential_checks 流水；这里只把两者收敛成一个可筛选的词。
const (
	CredLifecycleLost       = "lost"       // 节点已标记失效
	CredLifecycleLocked     = "locked"     // 最近一次复核：账号锁定
	CredLifecycleInvalid    = "invalid"    // 最近一次复核：口令/密钥已失效
	CredLifecycleExpired    = "expired"    // verified 目标已过 valid_until
	CredLifecycleExpiring   = "expiring"   // 7 天内到期
	CredLifecycleStale      = "stale"      // verified 但超过 30 天未复核
	CredLifecycleVerified   = "verified"   // 至少一条目标实测成功，且未过期
	CredLifecycleUnverified = "unverified" // 有目标，但没有实测成功
	CredLifecycleUnknown    = "unknown"    // 没有登记目标
)

// CredentialLifecycleInput 是分类所需的输入。
type CredentialLifecycleInput struct {
	State       string
	Targets     []CredentialTarget
	LastResult  string
	LastCheckAt *time.Time
	Now         time.Time
}

// ComputeCredentialLifecycle 按优先级分类：lost > locked > invalid > expired >
// expiring > stale > verified > unverified > unknown。
func ComputeCredentialLifecycle(in CredentialLifecycleInput) string {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	if strings.EqualFold(strings.TrimSpace(in.State), StateLootLost) {
		return CredLifecycleLost
	}
	switch strings.ToLower(strings.TrimSpace(in.LastResult)) {
	case "locked":
		return CredLifecycleLocked
	case "failed":
		return CredLifecycleInvalid
	}
	verified := false
	earliestExpiry := time.Time{}
	for _, t := range in.Targets {
		if !t.Verified {
			continue
		}
		verified = true
		if exp := ParseLooseTime(t.ValidUntil); exp != nil {
			if exp.Before(now) {
				return CredLifecycleExpired
			}
			if earliestExpiry.IsZero() || exp.Before(earliestExpiry) {
				earliestExpiry = *exp
			}
		}
	}
	if verified && !earliestExpiry.IsZero() && earliestExpiry.Sub(now) <= 7*24*time.Hour {
		return CredLifecycleExpiring
	}
	if verified {
		last := in.LastCheckAt
		if last == nil {
			for _, t := range in.Targets {
				if t.Verified {
					if ts := ParseLooseTime(t.ObservedAt); ts != nil && (last == nil || ts.After(*last)) {
						last = ts
					}
				}
			}
		}
		if last != nil && now.Sub(*last) > 30*24*time.Hour {
			return CredLifecycleStale
		}
		return CredLifecycleVerified
	}
	if len(in.Targets) > 0 {
		return CredLifecycleUnverified
	}
	return CredLifecycleUnknown
}

// ParseLooseTime 解析 RFC3339 或常见日期格式；空/非法返回 nil。
func ParseLooseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}
