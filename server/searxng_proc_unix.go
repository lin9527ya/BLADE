//go:build !windows

package server

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func isSearxngProcessAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	// Linux 下校对 /proc/<pid>/cmdline，避免 PID 复用到其它进程后误杀或误判运行中。
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err == nil {
		flat := strings.ToLower(string(bytes.ReplaceAll(cmdline, []byte{0}, []byte{' '})))
		return strings.Contains(flat, "searx") || strings.Contains(flat, "granian")
	}
	return true
}

func signalSearxngProcess(pid int, force bool) {
	if pid <= 1 {
		return
	}
	sig := syscall.SIGTERM
	if force {
		sig = syscall.SIGKILL
	}
	// Setsid 启动时 pid == pgid，先发信号给进程组以停掉 granian worker。
	_ = syscall.Kill(-pid, sig)
	_ = syscall.Kill(pid, sig)
}
