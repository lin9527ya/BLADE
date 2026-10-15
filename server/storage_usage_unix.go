//go:build !windows

package server

import "syscall"

// diskUsage 返回数据目录所在文件系统的总容量与可用量（读不到就返回 0，页面上按"未知"显示）。
//
// 按平台拆开的原因与 P24 的资源限额同一条纪律：跨平台构建是 release 与 CI 的门禁，
// 而 syscall.Statfs 是 unix 专有——写在一个文件里会让 windows 目标编译不过
// （P24 就这样藏了一整个功能周期，见 CHANGELOG 0.4.2「构建」）。
func diskUsage(path string) (total, free int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize)
}
