package db

import (
	"fmt"
	"sort"
	"strings"
)

// TerminalStatusSQL 返回终态集合的 SQL 字面量，例如 `('done','failed','timeout','policy_blocked')`。
//
// 存在的理由：TerminalStatuses 是 Go 侧的单一真源，但它在 SQL 里被重抄了多次，
// 而且抄错的方式很隐蔽——有的地方只写了三个（漏 policy_blocked）。漏掉的后果不是
// 报错，而是"policy_blocked 的任务被当成未终态"：completed_at 被清空、first_run_at
// 被重置、统计口径与任务列表不一致。
//
// 所有需要把终态集合写进 SQL 的地方都应该用这个函数拼（配合 placeholders/参数化
// 无法做到时的常量拼接），不要再手写字面量。
func TerminalStatusSQL() string {
	quoted := make([]string, 0, len(TerminalStatuses))
	for _, s := range TerminalStatuses {
		quoted = append(quoted, "'"+strings.ReplaceAll(s, "'", "''")+"'")
	}
	return "(" + strings.Join(quoted, ",") + ")"
}

// TerminalStatusesOrdered 返回按字母序排列的终态（供测试与文档展示；判定一律用 IsTerminal）。
func TerminalStatusesOrdered() []string {
	out := append([]string(nil), TerminalStatuses...)
	sort.Strings(out)
	return out
}

// ErrUnknownTerminal 是给调用方拼 SQL 时的防御：终态集合里出现了非标识符字符
// （引号注入/拼错）时直接报错，而不是把一段可疑的 SQL 拼进去。
func checkTerminalStatuses() error {
	for _, s := range TerminalStatuses {
		if s == "" || strings.ContainsAny(s, "\"'\\;() ") {
			return fmt.Errorf("终态集合里有非法值 %q", s)
		}
	}
	return nil
}
