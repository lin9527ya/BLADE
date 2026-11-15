package intercept

// alwaysAllowDenied 是 code-owned denylist：这些程序的前缀 allow 规则等价于
// “允许任意代码/任意第三方代码/不可逆破坏”。只按规范化后的 basename 比较。
var alwaysAllowDenied = func() map[string]bool {
	names := []string{
		// interpreters / shells
		"sh", "bash", "zsh", "dash", "ksh", "fish", "csh", "tcsh", "ash", "mksh", "rbash", "pwsh", "powershell",
		"python", "pypy", "ipython", "perl", "ruby", "irb", "node", "deno", "bun", "php", "lua", "luajit",
		"rscript", "tclsh", "wish", "expect", "awk", "gawk", "mawk", "nawk", "sed", "jshell", "osascript",
		// launchers / run-argument or exec-via-flag programs
		"env", "command", "builtin", "exec", "eval", "source", ".", "nohup", "timeout", "nice", "ionice",
		"stdbuf", "setsid", "chroot", "unshare", "nsenter", "systemd-run", "su", "sudo", "doas", "pkexec",
		"runuser", "setpriv", "sg", "watch", "busybox", "toybox", "xargs", "parallel", "make", "gmake",
		"mapfile", "trap", "script", "find", "ssh", "scp", "sftp", "rsync", "socat", "ncat", "nc", "netcat",
		"flock", "strace", "ltrace", "gdb", "taskset", "chrt", "unbuffer", "rlwrap", "firejail", "bwrap",
		"proot", "proxychains", "torsocks", "tar", "zip", "vim", "vi", "nvim", "emacs", "less", "more", "man",
		// package managers / build tools (run package scripts, hooks, arbitrary code)
		"npm", "npx", "pnpm", "yarn", "bunx", "corepack", "pip", "pipx", "uv", "uvx", "poetry", "conda",
		"composer", "gem", "bundle", "bundler", "rake", "cargo", "go", "mvn", "gradle", "dotnet", "apt",
		"apt-get", "dpkg", "dnf", "yum", "rpm", "pacman", "apk", "brew", "snap", "flatpak",
		// orchestration
		"docker", "docker-compose", "podman", "nerdctl", "kubectl", "helm", "ansible", "ansible-playbook",
		"terraform", "pulumi", "vagrant",
		// destructive / host control
		"rm", "dd", "mkfs", "mkswap", "wipefs", "fdisk", "sfdisk", "parted", "shred", "truncate", "chmod",
		"chown", "kill", "pkill", "killall", "systemctl", "service", "iptables", "ip6tables", "nft", "ufw",
		"mount", "umount", "swapoff", "shutdown", "reboot", "poweroff", "halt", "init", "telinit", "crontab",
		"insmod", "rmmod", "modprobe",
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}()
