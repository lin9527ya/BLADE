package server

// 测试默认走增强模式；经典模式用例显式用 noConstraints。
func allConstraints(string) bool { return true }
func noConstraints(string) bool  { return false }
