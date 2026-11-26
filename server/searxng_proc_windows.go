//go:build windows

package server

import (
	"os"
	"syscall"
)

const windowsStillActiveExitCode = 259 // STILL_ACTIVE

func isSearxngProcessAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	const processQueryLimitedInformation = 0x1000
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == windowsStillActiveExitCode
}

func signalSearxngProcess(pid int, _ bool) {
	if pid <= 1 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil && p != nil {
		_ = p.Kill()
	}
}
