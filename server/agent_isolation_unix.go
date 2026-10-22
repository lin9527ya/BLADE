//go:build !windows

package server

import (
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// resolveAgentAccount 解析 BLADE_AGENT_USER：用户名，或 "uid:gid"（数字，容器里常用）。
func resolveAgentAccount(spec string) (uid, gid uint32, home string, err error) {
	if u, g, ok, perr := parseUIDGID(spec); ok {
		if perr != nil {
			return 0, 0, "", perr
		}
		home = "/tmp"
		if acct, e := user.LookupId(strconv.FormatUint(uint64(u), 10)); e == nil && acct.HomeDir != "" {
			home = acct.HomeDir
		}
		return u, g, home, nil
	}
	acct, e := user.Lookup(spec)
	if e != nil {
		return 0, 0, "", fmt.Errorf("找不到用户：%w", e)
	}
	u64, e1 := strconv.ParseUint(acct.Uid, 10, 32)
	g64, e2 := strconv.ParseUint(acct.Gid, 10, 32)
	if e1 != nil || e2 != nil {
		return 0, 0, "", fmt.Errorf("用户 %s 的 uid/gid 不是数字", spec)
	}
	if acct.HomeDir == "" {
		home = "/tmp"
	} else {
		home = acct.HomeDir
	}
	return uint32(u64), uint32(g64), home, nil
}

// parseUIDGID：只有形如 "1000:1000" 且两段都是数字时才算数字形式（ok=true）。
func parseUIDGID(spec string) (uint32, uint32, bool, error) {
	i := -1
	for idx := 0; idx < len(spec); idx++ {
		if spec[idx] == ':' {
			i = idx
			break
		}
	}
	if i < 0 {
		return 0, 0, false, nil
	}
	u, e1 := strconv.ParseUint(spec[:i], 10, 32)
	g, e2 := strconv.ParseUint(spec[i+1:], 10, 32)
	if e1 != nil || e2 != nil {
		return 0, 0, false, nil // 带冒号但不是数字：交给用户名查找去报错
	}
	return uint32(u), uint32(g), true, nil
}

// applyAgentCredential 让子进程以 --agent-user 的身份执行。
// Groups 只保留主组：worker 不该带着服务端用户的附加组跑。
func applyAgentCredential(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    agentIso.uid,
		Gid:    agentIso.gid,
		Groups: []uint32{agentIso.gid},
	}
}
