package server

import (
	"sync"
	"time"
)

// 系统资源指标：CPU / 内存 / 磁盘。并入 /api/metrics 的 "system" 段，不另开路由。
//
// 存在的理由很具体：任务卡住时，操作者要先排除「机器本身跑满了」再去看模型和代理。
// 只读 cgroup 与 /proc，无依赖、无副作用；非 Linux 或读不到时返回 supported=false，
// 绝不让整个 /api/metrics 因此失败。
//
// 与 ARTEX 二开版的差异：它的 CPU 百分比用「两次采样差」算，采样状态挂在 Server 上；
// 这里沿用同一思路，但把状态收进 systemSampler，避免 Server 结构体再挂零散字段。

type metricValue struct {
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"percent"`
}

// SystemMetrics 是 /api/metrics 的 system 段。
type SystemMetrics struct {
	Scope     string      `json:"scope"`     // container | host
	Supported bool        `json:"supported"` // 读不到（非 Linux / 无 cgroup）时为 false
	Timestamp time.Time   `json:"timestamp"`
	CPU       metricValue `json:"cpu"` // CPU 只有 Percent 有意义（used/total 为 0）
	Memory    metricValue `json:"memory"`
	Disk      metricValue `json:"disk"`      // 根文件系统
	DataDisk  metricValue `json:"data_disk"` // 数据目录所在文件系统
	Container bool        `json:"container_metrics"`
	// CPULimitCores 是当前可见的 CPU 额度（cgroup 配额或核数），便于解释百分比。
	CPULimitCores float64 `json:"cpu_limit_cores,omitempty"`
}

// systemSamplerState 保存上一次 CPU 采样，供两次调用之间算占用率。
type systemSamplerState struct {
	mu      sync.Mutex
	lastAt  time.Time
	lastUse uint64
	lastLim float64
	primed  bool
}

func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return clampPercent(float64(used) / float64(total) * 100)
}

func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
