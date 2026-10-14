//go:build windows

package server

import "encoding/json"

// Windows：没有 /bin/sh 的 ulimit 语义，限额一律不生效——如实记录（不静默当成已限住）。
// 本产品主场景是 Linux 单机部署；Windows 上长任务仍受硬超时约束。

type ExecLimits struct {
	CPUSeconds int `json:"cpu_seconds,omitempty"`
	MemoryMB   int `json:"memory_mb,omitempty"`
	MaxProcs   int `json:"max_procs,omitempty"`
}

func (l ExecLimits) IsZero() bool {
	return l.CPUSeconds <= 0 && l.MemoryMB <= 0 && l.MaxProcs <= 0
}

func (l ExecLimits) Describe() string {
	if l.IsZero() {
		return "无"
	}
	return "（Windows 不支持资源限额）"
}

type ExecLimitsSnapshot struct {
	CPUSeconds int    `json:"cpu_seconds,omitempty"`
	MemoryMB   int    `json:"memory_mb,omitempty"`
	MaxProcs   int    `json:"max_procs,omitempty"`
	Note       string `json:"note,omitempty"`
}

func applyExecLimits(command string, l ExecLimits) (string, ExecLimitsSnapshot) {
	snap := ExecLimitsSnapshot{CPUSeconds: l.CPUSeconds, MemoryMB: l.MemoryMB, MaxProcs: l.MaxProcs}
	if !l.IsZero() {
		snap.Note = "Windows 不支持 ulimit 资源限额：本次未限住（台账如实记录）"
	}
	return command, snap
}

func marshalExecLimits(snap ExecLimitsSnapshot) json.RawMessage {
	b, err := json.Marshal(snap)
	if err != nil {
		return nil
	}
	return b
}
