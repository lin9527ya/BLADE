package db

import (
	"strings"
	"testing"
)

// TestTerminalStatusSQLCoversEveryStatus 钉住 SQL 字面量与 Go 侧真源一致。
//
// 为什么值得测：终态集合在 SQL 里被重抄过多次，抄错的方式是"只写三个、漏掉
// policy_blocked"——不报错，只是把策略封死的任务当成未终态（completed_at 被清空、
// 统计口径与任务列表不一致）。现在 SQL 由 TerminalStatusSQL 生成，这条用例防止
// 有人以后又手写字面量。
func TestTerminalStatusSQLCoversEveryStatus(t *testing.T) {
	got := TerminalStatusSQL()
	for _, s := range TerminalStatuses {
		if !strings.Contains(got, "'"+s+"'") {
			t.Fatalf("TerminalStatusSQL() 漏了 %q：%s", s, got)
		}
	}
	if !strings.Contains(got, "'policy_blocked'") {
		t.Fatalf("policy_blocked 必须在终态集合里（P20 策略封死是终态）：%s", got)
	}
	if err := checkTerminalStatuses(); err != nil {
		t.Fatalf("终态集合里有非法值：%v", err)
	}
}

// TestNoHandWrittenTerminalLiterals 是防漂移断言：源码里不该再出现手写的终态字面量。
//
// 用 go/parser 太重，这里退一步：读本包与 server 包的相关文件做文本检查。它只拦
// 最常见的那种写法（IN ('done','failed','timeout')），正好是此前漏 policy_blocked 的形态。
func TestNoHandWrittenTerminalLiterals(t *testing.T) {
	for _, file := range []string{"findings.go", "tasks.go"} {
		body := readSourceForTest(t, file)
		for _, bad := range []string{"IN ('done','failed','timeout')", "NOT IN ('done','failed','timeout')"} {
			if strings.Contains(body, bad) {
				t.Fatalf("%s 里仍有手写的三元素终态字面量 %q——用 TerminalStatusSQL()", file, bad)
			}
		}
	}
}
