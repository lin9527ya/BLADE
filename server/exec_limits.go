package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// P24 资源限额：长任务执行（run_execution）此前只有硬超时，没有 CPU/内存/进程数上限——
// 一条 `nmap -T5` 或 `ffuf -w big.txt` 就能把平台自己这台机器打满（还不算目标侧）。
//
// 实现走 `/bin/sh` 的 `ulimit` 前导：零依赖、跨发行版、与执行用的同一个 shell（所以限制对
// 整棵子进程树生效，不是只限制 shell 本身）。**能力探测一次**：本机 sh 不支持某项时如实写进
// 台账快照（applied=false），不静默当成"已限住"——"以为设了其实没设"比不设更危险。
//
// cgroup v2 作为更硬的隔离是可选升级路径（需要可写的 /sys/fs/cgroup + 权限），当前不启用；
// 需要时按同一份 ExecLimits 快照并行加一层即可。
//
// **平台切分**：本文件只放平台无关的部分（类型、编码、读设置）。真正施加限额的
// applyExecLimits 与取信号的 signaledBy 分别在 exec_limits_unix.go 与 exec_limits_windows.go
// 里——`ulimit` 是 Unix 的东西，Windows 上如实标注"未限住"而不是假装限住。
// （此前整份文件带 `//go:build !windows`，而 execution.go/server.go 无条件引用这里的符号，
// Windows 交叉编译因此编译不过；见 exec_limits_windows.go 的说明。）

// ExecLimits 是一次执行的资源限额（0 = 该轴不限）。
type ExecLimits struct {
	CPUSeconds int `json:"cpu_seconds,omitempty"`
	MemoryMB   int `json:"memory_mb,omitempty"`
	MaxProcs   int `json:"max_procs,omitempty"`
}

// IsZero 报告没有任何限额（此时不加前导，行为与从前完全一致）。
func (l ExecLimits) IsZero() bool {
	return l.CPUSeconds <= 0 && l.MemoryMB <= 0 && l.MaxProcs <= 0
}

// Describe 渲染成人话（台账/回执/日志共用）。
func (l ExecLimits) Describe() string {
	if l.IsZero() {
		return "无"
	}
	var parts []string
	if l.CPUSeconds > 0 {
		parts = append(parts, fmt.Sprintf("CPU %ds", l.CPUSeconds))
	}
	if l.MemoryMB > 0 {
		parts = append(parts, fmt.Sprintf("内存 %dMB", l.MemoryMB))
	}
	if l.MaxProcs > 0 {
		parts = append(parts, fmt.Sprintf("进程数 %d", l.MaxProcs))
	}
	return strings.Join(parts, " / ")
}

// ExecLimitsSnapshot 是落进台账的限额快照：请求了什么 + 哪些真的生效。
type ExecLimitsSnapshot struct {
	CPUSeconds int  `json:"cpu_seconds,omitempty"`
	MemoryMB   int  `json:"memory_mb,omitempty"`
	MaxProcs   int  `json:"max_procs,omitempty"`
	AppliedCPU bool `json:"applied_cpu,omitempty"`
	AppliedMem bool `json:"applied_mem,omitempty"`
	AppliedPrc bool `json:"applied_procs,omitempty"`
	// Note 是人话补充（哪些轴没限住、为什么）。
	Note string `json:"note,omitempty"`
}

// marshalExecLimits 把快照编码进台账（编码失败返回 nil：限额记账不该挡住执行）。
func marshalExecLimits(snap ExecLimitsSnapshot) json.RawMessage {
	b, err := json.Marshal(snap)
	if err != nil {
		return nil
	}
	return b
}

// execLimitsFromSettings 从设置现读限额（每调用读一次：改设置立即对之后提交的执行生效）。
// 0 = 该轴不限；三轴全 0 = 不限额（行为与从前一致）。
func execLimitsFromSettings(pg *db.DB) ExecLimits {
	if pg == nil {
		return ExecLimits{}
	}
	getInt := func(key string, def int) int {
		v, ok, _ := pg.GetSetting(key)
		if !ok || strings.TrimSpace(v) == "" {
			return def
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			return def
		}
		return n
	}
	return ExecLimits{
		CPUSeconds: getInt(settingExecCPUSeconds, defaultExecCPUSeconds),
		MemoryMB:   getInt(settingExecMemoryMB, defaultExecMemoryMB),
		MaxProcs:   getInt(settingExecMaxProcs, defaultExecMaxProcs),
	}
}
