//go:build !linux

package server

// 非 Linux 平台不读 cgroup/proc：如实回 supported=false，而不是编一组数字。
func (s *Server) systemMetricsSnapshot() SystemMetrics {
	return SystemMetrics{Scope: "host", Supported: false}
}
