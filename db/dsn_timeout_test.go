package db

import (
	"net/url"
	"strings"
	"testing"
)

// TestWithStatementTimeout 钉住连接池的语句级超时兜底怎么落到 DSN 上。
//
// 为什么必须在 DSN 层：启动时 SET 一次只作用于当时那条连接，池里后来新建的连接
// 不受影响——那种"设了等于没设"的实现比没有更危险。
func TestWithStatementTimeout(t *testing.T) {
	base := "postgres://u:p@h:5432/db?sslmode=disable"

	got := withStatementTimeout(base, "60s")
	if !strings.Contains(got, "options=") {
		t.Fatalf("没有注入 options：%s", got)
	}
	q, err := url.Parse(got)
	if err != nil {
		t.Fatalf("拼出来的 DSN 不可解析：%v（%s）", err, got)
	}
	if opts := q.Query().Get("options"); !strings.Contains(opts, "statement_timeout=60s") {
		t.Fatalf("options=%q 里没有 statement_timeout", opts)
	}
	if q.Query().Get("sslmode") != "disable" {
		t.Fatalf("原有参数被破坏：%s", got)
	}

	// 已有 options：并进去而不是覆盖。
	merged := withStatementTimeout("postgres://u:p@h/db?options=-c%20search_path%3Dpublic", "60s")
	opts, _ := url.Parse(merged)
	if v := opts.Query().Get("options"); !strings.Contains(v, "search_path=public") || !strings.Contains(v, "statement_timeout=60s") {
		t.Fatalf("合并后的 options=%q 丢了原有项或新项", v)
	}

	// DSN 自己已经指定：尊重调用方。
	explicit := "postgres://u:p@h/db?options=-c%20statement_timeout%3D5s"
	if got := withStatementTimeout(explicit, "60s"); got != explicit {
		t.Fatalf("DSN 显式指定的超时被覆盖：%s", got)
	}
}
