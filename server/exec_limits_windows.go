//go:build windows

package server

import (
	"os/exec"
	"strings"
)

// Windows 侧的两处桩。为什么需要它们：
//
// P24 的资源限额实现走 `/bin/sh` 的 `ulimit` 前导，那是 Unix 的东西；`signaledBy` 也依赖
// syscall.WaitStatus。此前这些与类型定义放在同一个文件里、整份带 `//go:build !windows`，
// 而 execution.go / server.go 是**无条件**引用它们的——于是 Windows 交叉编译直接编译不过。
//
// 这个洞一直没暴露，因为：CI 的测试与构建都在 Linux 上跑，**只有 release 工作流跨平台构建**，
// 而它只在打 tag 时运行。2026-10-14 的 v0.4.1 发布就是这么翻车的——五个平台里 Windows 编译
// 失败、其余四个被 matrix 的 fail-fast 连坐取消、release job 因 needs: binaries 被跳过，
// 于是 tag 有了、GitHub Release 却没有。
//
// 这里不做"假装限住"：按 P24 既有的纪律（探测不到就如实写 applied=false），Windows 上快照的
// applied_* 全为 false，并把原因写进 Note，让台账与回执如实说明——"以为设了其实没设"比不设更危险。

// applyExecLimits 在 Windows 上原样返回命令，并把"未限住"如实写进快照。
func applyExecLimits(command string, l ExecLimits) (string, ExecLimitsSnapshot) {
	snap := ExecLimitsSnapshot{CPUSeconds: l.CPUSeconds, MemoryMB: l.MemoryMB, MaxProcs: l.MaxProcs}
	if l.IsZero() || strings.TrimSpace(command) == "" {
		return command, snap
	}
	snap.Note = "本平台不支持 ulimit 限额（仅 Unix）：本次未限住（台账如实记录）"
	return command, snap
}

// signaledBy 在 Windows 上没有 Unix 信号语义，恒报告"未被信号终止"。
func signaledBy(*exec.Cmd) (int, bool) {
	return 0, false
}
