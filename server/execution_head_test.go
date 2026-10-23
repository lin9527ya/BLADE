package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadOutputHead 钉住两点：读得到头部、读不到（路径空/不存在）时返回空串而不是 panic。
func TestReadOutputHead(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.log")
	body := strings.Repeat("HIT 200 /admin\n", 1000) // 远大于 1KB
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got := readOutputHead(p, 1024)
	if len(got) != 1024 {
		t.Fatalf("应读到 1024 字节，实际 %d", len(got))
	}
	if !strings.HasPrefix(got, "HIT 200 /admin") {
		t.Fatalf("头部内容不对: %q", got[:40])
	}

	if got := readOutputHead("", 1024); got != "" {
		t.Fatalf("空路径应返回空串，实际 %q", got)
	}
	if got := readOutputHead(filepath.Join(dir, "nope.log"), 1024); got != "" {
		t.Fatalf("文件不存在应返回空串，实际 %q", got)
	}
}
