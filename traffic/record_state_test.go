package traffic

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewExchangeIDUniquePerCapture(t *testing.T) {
	now := time.Unix(1700000000, 123456789)
	seen := map[string]bool{}
	for i := int64(1); i <= 1000; i++ {
		id := newExchangeID(now, i)
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	// Same second but a later nanosecond must not collide with an earlier capture.
	if newExchangeID(now, 1) == newExchangeID(now.Add(time.Nanosecond), 1) {
		t.Fatal("nanosecond timestamp must distinguish captures")
	}
}

func TestSpillFailureMarksPartial(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	// Force MkdirAll under _blobs/sha256 to fail by making it a regular file.
	if err := os.WriteFile(filepath.Join(dir, "_blobs", "sha256"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("A"), maxInlineBody+1)
	sb := tr.spill(body, "text/plain")
	if sb.state != "partial" || sb.storedLen != blobPreview {
		t.Fatalf("spill = %+v, want partial with preview length", sb)
	}
	if len(sb.inline) == 0 {
		t.Fatal("partial spill should keep a preview")
	}
}

func TestReadEvidenceRejectsPartialBody(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,req_state,resp_state,path)
VALUES('partial-1',1,'p.example','GET','/','http://p.example/',200,'text/plain',100,0,'partial','complete','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DB().Exec(`INSERT INTO exchange_bodies(id,req_head,req_body,resp_head,resp_body) VALUES('partial-1','GET / HTTP/1.1','preview','HTTP/1.1 200 OK','')`); err != nil {
		t.Fatal(err)
	}
	err = tr.ReadEvidence(context.Background(), []string{"partial-1"}, func(EvidenceExchange) error { return nil })
	if err == nil {
		t.Fatal("partial body must not be bindable as complete evidence")
	}
}
