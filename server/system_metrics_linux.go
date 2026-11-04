//go:build linux

package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func (s *Server) systemMetricsSnapshot() SystemMetrics {
	use, limit, cpuOK := cgroupCPU()
	cpuPct := 0.0
	if cpuOK {
		now := time.Now()
		s.sysMetrics.mu.Lock()
		if s.sysMetrics.primed && use >= s.sysMetrics.lastUse && limit > 0 {
			if elapsed := now.Sub(s.sysMetrics.lastAt).Seconds(); elapsed > 0 {
				cpuPct = float64(use-s.sysMetrics.lastUse) / 1e6 / elapsed / limit * 100
			}
		}
		s.sysMetrics.lastUse, s.sysMetrics.lastAt, s.sysMetrics.lastLim = use, now, limit
		s.sysMetrics.primed = true
		s.sysMetrics.mu.Unlock()
	}
	memUsed, memTotal, memOK := cgroupMemory()
	if !memOK {
		// 没有 cgroup 内存账本（裸机、cgroup v1 未挂 memory 控制器）：回落到
		// /proc/meminfo。这时读的是整机内存，指标依然有意义——只是不是"本进程组"。
		memUsed, memTotal, memOK = hostMemory()
	}
	root := diskMetric("/")
	data := diskMetric(s.m.dir)
	if data.TotalBytes == 0 {
		data = root
	}
	return SystemMetrics{
		Scope: "container", Supported: true, Timestamp: time.Now().UTC(),
		Container: cpuOK || memOK,
		CPU:       metricValue{Percent: clampPercent(cpuPct)},
		Memory:    metricValue{UsedBytes: memUsed, TotalBytes: memTotal, Percent: percent(memUsed, memTotal)},
		Disk:      root, DataDisk: data,
		CPULimitCores: limit,
	}
}

// cgroupCPU 读 cgroup v2（cpu.stat usage_usec + cpu.max）或 v1（cpuacct）。
// 读不到 usage_usec 时返回 ok=false——宁可显示"不支持"，也不要编一个百分比。
func cgroupCPU() (uint64, float64, bool) {
	for _, dir := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/cpu"} {
		b, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
		if err != nil {
			continue
		}
		var use uint64
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == "usage_usec" {
				use, _ = strconv.ParseUint(f[1], 10, 64)
			}
		}
		limit := float64(runtime.NumCPU())
		if q, err := os.ReadFile(filepath.Join(dir, "cpu.max")); err == nil {
			f := strings.Fields(string(q))
			if len(f) == 2 && f[0] != "max" {
				quota, _ := strconv.ParseFloat(f[0], 64)
				period, _ := strconv.ParseFloat(f[1], 64)
				if period > 0 && quota > 0 {
					limit = quota / period
				}
			}
		}
		if use > 0 {
			return use, limit, true
		}
	}
	return 0, 0, false
}

func cgroupMemory() (uint64, uint64, bool) {
	for _, dir := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/memory"} {
		cur, ce := readUint(filepath.Join(dir, "memory.current"))
		max, me := readUint(filepath.Join(dir, "memory.max"))
		if ce == nil && me == nil {
			return cur, max, max > 0
		}
		if ce == nil {
			if b, err := os.ReadFile(filepath.Join(dir, "memory.max")); err == nil && strings.TrimSpace(string(b)) == "max" {
				return cur, hostMemoryTotal(), true
			}
		}
		cur, ce = readUint(filepath.Join(dir, "memory.usage_in_bytes"))
		max, me = readUint(filepath.Join(dir, "memory.limit_in_bytes"))
		if ce == nil && me == nil && max < ^uint64(0)/2 {
			return cur, max, true
		}
	}
	return 0, 0, false
}

// hostMemory 从 /proc/meminfo 读整机内存（used = total - available）。
func hostMemory() (uint64, uint64, bool) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	var total, avail uint64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	if total == 0 {
		return 0, 0, false
	}
	if avail > total {
		avail = total
	}
	return total - avail, total, true
}

func hostMemoryTotal() uint64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "MemTotal:" {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			return n * 1024
		}
	}
	return 0
}

func readUint(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

func diskMetric(path string) metricValue {
	var st syscall.Statfs_t
	if path == "" || syscall.Statfs(path, &st) != nil {
		return metricValue{}
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	used := total - free
	return metricValue{UsedBytes: used, TotalBytes: total, Percent: percent(used, total)}
}
