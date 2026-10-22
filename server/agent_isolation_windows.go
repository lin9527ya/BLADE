//go:build windows

package server

import (
	"fmt"
	"os/exec"
)

func resolveAgentAccount(spec string) (uid, gid uint32, home string, err error) {
	return 0, 0, "", fmt.Errorf("Windows 上不支持 agent 降权（%q），请去掉 BLADE_AGENT_USER", spec)
}

func applyAgentCredential(*exec.Cmd) {}
