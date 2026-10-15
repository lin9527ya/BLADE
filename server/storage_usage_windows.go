//go:build windows

package server

// diskUsage 在本平台**未实现**：返回 (0, 0)，页面据此显示"磁盘用量未知"。
//
// 如实留空而不是编一个数字：清理判定用的是单元自身的写入时间与任务状态，
// 与磁盘余量无关——磁盘用量只是页面上的参考信息，缺它不影响清理正确性。
func diskUsage(string) (total, free int64) { return 0, 0 }
