//go:build !windows

package server

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// 本文件是 P24 资源限额的 Unix 侧实现：把 ExecLimits 编译成 `/bin/sh` 的 `ulimit` 前导。
// 类型与读设置的部分在 exec_limits.go（平台无关），Windows 侧的桩在 exec_limits_windows.go。
//
// 为什么单独一个文件：`ulimit` 与 syscall.WaitStatus 都是 Unix 概念。整份塞进一个文件时，
// 一旦给那个文件加 `//go:build !windows`，Windows 交叉编译就会因符号缺失而失败——而这正是
// 2026-10-14 的 v0.4.1 发布翻车的原因（见 exec_limits_windows.go 的说明）。

// ulimitCapability 是"这台机器的 /bin/sh 支持哪些 ulimit 轴"的一次性探测结果。
var ulimitCapability struct {
	once  sync.Once
	cpu   bool
	mem   bool
	procs bool
}

func probeUlimitCapability() (cpu, mem, procs bool) {
	ulimitCapability.once.Do(func() {
		ulimitCapability.cpu = ulimitWorks("-t")
		ulimitCapability.mem = ulimitWorks("-v")
		ulimitCapability.procs = ulimitWorks("-u")
	})
	return ulimitCapability.cpu, ulimitCapability.mem, ulimitCapability.procs
}

// ulimitWorks 用一次真实调用探测某个 ulimit 轴（能设值 = 支持）。
func ulimitWorks(flag string) bool {
	if _, err := exec.LookPath("/bin/sh"); err != nil {
		return false
	}
	// 设一个无关紧要的小值然后退出；选项不支持时 sh 会以非零退出。
	return exec.Command("/bin/sh", "-c", "ulimit "+flag+" 64 2>/dev/null").Run() == nil
}

// applyExecLimits 生成实际执行的命令（前导 ulimit）与台账快照。
// command 为空/限额为空 → 原样返回（行为与从前一致）。
func applyExecLimits(command string, l ExecLimits) (string, ExecLimitsSnapshot) {
	snap := ExecLimitsSnapshot{CPUSeconds: l.CPUSeconds, MemoryMB: l.MemoryMB, MaxProcs: l.MaxProcs}
	if l.IsZero() || strings.TrimSpace(command) == "" {
		return command, snap
	}
	cpuOK, memOK, procsOK := probeUlimitCapability()
	var prelude []string
	var unsupported []string
	if l.CPUSeconds > 0 {
		if cpuOK {
			prelude = append(prelude, fmt.Sprintf("ulimit -t %d", l.CPUSeconds))
			snap.AppliedCPU = true
		} else {
			unsupported = append(unsupported, "CPU")
		}
	}
	if l.MemoryMB > 0 {
		if memOK {
			// ulimit -v 的单位是 KB。
			prelude = append(prelude, fmt.Sprintf("ulimit -v %d", l.MemoryMB*1024))
			snap.AppliedMem = true
		} else {
			unsupported = append(unsupported, "内存")
		}
	}
	if l.MaxProcs > 0 {
		if procsOK {
			prelude = append(prelude, fmt.Sprintf("ulimit -u %d", l.MaxProcs))
			snap.AppliedPrc = true
		} else {
			unsupported = append(unsupported, "进程数")
		}
	}
	if len(prelude) == 0 {
		snap.Note = "本机 /bin/sh 不支持 " + strings.Join(unsupported, "/") + " 限额：本次未限住（台账如实记录）"
		return command, snap
	}
	if len(unsupported) > 0 {
		snap.Note = "本机 /bin/sh 不支持 " + strings.Join(unsupported, "/") + " 限额：这些轴未限住"
	}
	// ulimit 与命令在同一个 shell 里（限制跟着子进程树走）；ulimit 失败不会中断命令
	// （2>/dev/null 吞掉告警），由快照如实记录谁生效了。
	return strings.Join(prelude, "; ") + "; " + command, snap
}

// signaledBy 取进程被哪个信号终止（未被信号终止 → ok=false）。
func signaledBy(cmd *exec.Cmd) (int, bool) {
	if cmd == nil || cmd.ProcessState == nil {
		return 0, false
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return int(ws.Signal()), true
	}
	return 0, false
}
