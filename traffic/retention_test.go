package traffic

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDeleteOlderThanKeepsHostsThatStillHaveRecentTraffic pins the one thing a
// time-based delete can easily get wrong: a host's on-disk directory is shared by
// all of that host's exchanges, so it may only be removed once the host has no rows
// left. Dropping the whole directory of a host that still has recent traffic would
// take live recordings with it.
func TestDeleteOlderThanKeepsHostsThatStillHaveRecentTraffic(t *testing.T) {
	tr, dir := openTraffic(t)

	old := time.Now().AddDate(0, 0, -100).Unix()
	recent := time.Now().AddDate(0, 0, -1).Unix()
	insert := func(id, host string, ts int64) {
		t.Helper()
		if _, err := tr.DB().Exec(
			`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
			 VALUES(?,?,?,?,?,?,?,?,?,?,'')`,
			id, ts, host, "GET", "/x", "http://"+host+"/x", 200, "text/plain", 1, 1); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	insert("e-old-only", "old-only.example.com", old)
	insert("e-mixed-old", "mixed.example.com", old)
	insert("e-mixed-new", "mixed.example.com", recent)

	// 两个 host 都有旧版本写下的遗留文件树。
	for _, h := range []string{"old-only.example.com", "mixed.example.com"} {
		p := filepath.Join(dir, sanitize(h))
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "body.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	n, err := tr.DeleteOlderThan(time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if n != 2 {
		t.Fatalf("删了 %d 条，期望 2 条（两条过期的）", n)
	}

	var left []string
	rows, err := tr.DB().Query(`SELECT id FROM exchanges ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if len(left) != 1 || left[0] != "e-mixed-new" {
		t.Fatalf("剩下的应只有 e-mixed-new，实际 %v", left)
	}

	if _, err := os.Stat(filepath.Join(dir, sanitize("old-only.example.com"))); !os.IsNotExist(err) {
		t.Fatalf("行已清空的 host 目录应被删除（stat err=%v）", err)
	}
	if _, err := os.Stat(filepath.Join(dir, sanitize("mixed.example.com"), "body.txt")); err != nil {
		t.Fatalf("仍有新流量的 host 目录必须留着，否则等于删掉了在用数据：%v", err)
	}
}

// TestDeleteOlderThanWithNothingExpiredIsNoop covers the default posture: when the
// cutoff is in the past nothing matches, nothing is staged, and the call is quiet.
func TestDeleteOlderThanWithNothingExpiredIsNoop(t *testing.T) {
	tr, _ := openTraffic(t)
	if _, err := tr.DB().Exec(
		`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
		 VALUES('e-recent',?,?,'GET','/x','http://keep.example.com/x',200,'text/plain',1,1,'')`,
		time.Now().Unix(), "keep.example.com"); err != nil {
		t.Fatal(err)
	}

	n, err := tr.DeleteOlderThan(time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatalf("DeleteOlderThan: %v", err)
	}
	if n != 0 {
		t.Fatalf("没有过期行时应删 0 条，实际 %d", n)
	}
	var left int
	if err := tr.DB().QueryRow(`SELECT count(*) FROM exchanges`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("未过期的行不该被删，实际剩 %d 条", left)
	}
}
