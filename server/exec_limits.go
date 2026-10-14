//go:build !windows

package server

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/Autumn-27/artex/db"
)

// P24 资源限额：长任务执行（run_execution）此前只有硬超时，没有 CPU/内存/进程数上限——
// 一条 `nmap -T5` 或 `ffuf -w big.txt` 就能把平台自己这台机器打满（还不算目标侧）。
//
// 实现走 `/bin/sh -c` 的 `ulimit` 前导：零依赖、跨发行版、与执行用的同一个 shell
// （所以限制对整棵子进程树生效，不是只限制 shell 本身）。**能力探测一次**：
// 本机 sh 不支持某项时如实写进台账快照（applied=false），不静默当成"已限住"——
// "以为设了其实没设"比不设更危险。
//
// cgroup v2 作为更硬的隔离是可选升级路径（需要可写的 /sys/fs/cgroup + 权限），
// 当前不启用；需要时按同一份 ExecLimits 快照并行加一层即可。

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
