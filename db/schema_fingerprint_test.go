package db

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

// TestSchemaFingerprintRecorded 钉住 schema 指纹：Open 之后库里记着**当前二进制嵌入的
// schema.sql** 的 sha256。
//
// 为什么需要它：「重启即迁移」意味着库的结构由**最近一次启动的二进制**决定，事后想回答
// 「这个库是哪一版结构」时没有别的依据（schema 里只有并发迁移用的 advisory lock，无版本号）。
func TestSchemaFingerprintRecorded(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	sum := sha256.Sum256([]byte(schemaSQL))
	want := hex.EncodeToString(sum[:])

	got, ok, err := d.GetSetting(settingSchemaFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Open 之后应当记下 schema 指纹——重启即迁移下，这是回答「库是哪一版结构」的唯一依据")
	}
	if got != want {
		t.Fatalf("指纹 = %q，want %q", got, want)
	}

	// 正常重启（指纹未变）**不该产生写**：updated_at 必须原封不动。
	var before time.Time
	if err := d.QueryRow(`SELECT updated_at FROM settings WHERE key=$1`, settingSchemaFingerprint).Scan(&before); err != nil {
		t.Fatal(err)
	}
	d2, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	d2.Close()
	var after time.Time
	if err := d.QueryRow(`SELECT updated_at FROM settings WHERE key=$1`, settingSchemaFingerprint).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("指纹没变时不该改写（updated_at 被动了：%v → %v）", before, after)
	}

	// 库里是别的结构留下的指纹时，本次启动要把它纠正过来。
	if err := d.SetSetting(settingSchemaFingerprint, "stale-from-another-build"); err != nil {
		t.Fatal(err)
	}
	d3, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d3.Close()
	if got, _, _ := d3.GetSetting(settingSchemaFingerprint); got != want {
		t.Fatalf("陈旧指纹未被纠正：%q，want %q", got, want)
	}
}
