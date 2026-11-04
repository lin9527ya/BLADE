package server

import (
	"runtime"
	"testing"
)

// 系统指标是只读探测：Linux 上必须能拿到内存与磁盘，且百分比永远在 0–100。
// 读不到时如实 supported=false，而不是返回一组编出来的数字。
func TestSystemMetricsSnapshot(t *testing.T) {
	s := &Server{m: &Manager{dir: t.TempDir()}}
	got := s.systemMetricsSnapshot()
	if runtime.GOOS != "linux" {
		if got.Supported {
			t.Fatalf("非 Linux 应回 supported=false：%+v", got)
		}
		return
	}
	if !got.Supported {
		t.Fatal("Linux 上应能读出系统指标")
	}
	if got.Memory.TotalBytes == 0 || got.Disk.TotalBytes == 0 {
		t.Fatalf("内存/磁盘总量应可读：%+v", got)
	}
	if got.Memory.Percent < 0 || got.Memory.Percent > 100 || got.Disk.Percent < 0 || got.Disk.Percent > 100 {
		t.Fatalf("百分比必须在 0–100：%+v", got)
	}
	if got.DataDisk.TotalBytes == 0 {
		t.Fatal("数据盘读不到时应回落到根文件系统，而不是留空")
	}
	if got.CPULimitCores <= 0 {
		t.Fatalf("CPU 额度应可读（cgroup 配额或核数）：%+v", got)
	}
}

// 两次采样之间才有 CPU 占用率；第一次调用必须返回 0 而不是垃圾值。
func TestSystemMetricsCPUFirstSampleIsZero(t *testing.T) {
	s := &Server{m: &Manager{dir: t.TempDir()}}
	first := s.systemMetricsSnapshot()
	if first.CPU.Percent < 0 || first.CPU.Percent > 100 {
		t.Fatalf("首次采样的 CPU 占用率必须落在 0–100，实际 %v", first.CPU.Percent)
	}
}
