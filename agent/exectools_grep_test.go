package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestGrepFile 钉住 grep_execution 的核心语义：行号对得上、命中上限生效、无命中返回空。
func TestGrepFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ffuf.out")
	body := strings.Join([]string{
		"index [Status: 200, Size: 1234]",
		"admin [Status: 403, Size: 0]",
		"login [Status: 200, Size: 900]",
		"static [Status: 301, Size: 0]",
	}, "\n") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	hits, scanned, err := grepFile(p, regexp.MustCompile(`Status: 200`), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 4 {
		t.Fatalf("应扫描 4 行，实际 %d", scanned)
	}
	if len(hits) != 2 || hits[0].line != 1 || hits[1].line != 3 {
		t.Fatalf("命中/行号不对: %+v", hits)
	}
	if !strings.Contains(hits[0].text, "index") {
		t.Fatalf("命中行内容不对: %q", hits[0].text)
	}

	// 上限生效。
	hits, _, _ = grepFile(p, regexp.MustCompile(`Status:`), 1, 0)
	if len(hits) != 1 {
		t.Fatalf("上限应为 1，实际 %d", len(hits))
	}

	// 无命中也算正常。
	hits, _, _ = grepFile(p, regexp.MustCompile(`NOPE`), 10, 0)
	if len(hits) != 0 {
		t.Fatalf("不该有命中，实际 %+v", hits)
	}

	// 文件不存在要报错（调用方会转成可读的工具错误）。
	if _, _, err := grepFile(filepath.Join(dir, "missing"), regexp.MustCompile(`x`), 1, 0); err == nil {
		t.Fatal("文件不存在应返回错误")
	}
}
