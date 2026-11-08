package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWorkspaceVersionedWrites(t *testing.T) {
	root := t.TempDir()
	s := &Server{m: &Manager{dir: root}}
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	version, err := workspaceVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	write := func(expected *string, content string) int {
		raw, _ := json.Marshal(map[string]any{"path": "a.txt", "content": content, "version": expected})
		w := httptest.NewRecorder()
		s.wsWrite(w, httptest.NewRequest("POST", "/api/workspace/write", bytes.NewReader(raw)))
		return w.Code
	}
	if code := write(nil, "overwrite"); code != 409 {
		t.Fatalf("unconfirmed overwrite=%d", code)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := write(&version, "stale"); code != 409 {
		t.Fatalf("stale write=%d", code)
	}
	version, _ = workspaceVersion(path)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, content := range []string{"first", "second"} {
		wg.Go(func() { codes <- write(&version, content) })
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for code := range codes {
		got[code]++
	}
	if got[200] != 1 || got[409] != 1 {
		t.Fatalf("concurrent CAS=%v", got)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatal("write changed permissions")
	}
}

func TestWorkspaceUploadConfirmationIsBatchPreflight(t *testing.T) {
	root := t.TempDir()
	s := &Server{m: &Manager{dir: root}}
	path := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	version, _ := workspaceVersion(path)
	upload := func(expected map[string]string) int {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		for _, name := range []string{"new.txt", "existing.txt"} {
			f, _ := mw.CreateFormFile("file", name)
			io.WriteString(f, "replacement")
		}
		if expected != nil {
			raw, _ := json.Marshal(expected)
			mw.WriteField("expected_versions", string(raw))
		}
		mw.Close()
		r := httptest.NewRequest("POST", "/api/workspace/upload", &body)
		r.Header.Set("Content-Type", mw.FormDataContentType())
		w := httptest.NewRecorder()
		s.wsUpload(w, r)
		return w.Code
	}
	if code := upload(nil); code != 409 {
		t.Fatalf("unconfirmed upload=%d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("preflight conflict partially uploaded batch")
	}
	if code := upload(map[string]string{"existing.txt": version}); code != http.StatusOK {
		t.Fatalf("confirmed upload=%d", code)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "replacement" {
		t.Fatal("confirmed file not replaced")
	}
	if code := upload(map[string]string{"existing.txt": version}); code != 409 {
		t.Fatalf("stale upload=%d", code)
	}
}

func TestWorkspaceInternalSymlinkWritesTarget(t *testing.T) {
	root := t.TempDir()
	s := &Server{m: &Manager{dir: root}}
	real := filepath.Join(root, "real.txt")
	os.WriteFile(real, []byte("old"), 0o600)
	if err := os.Symlink(real, filepath.Join(root, "alias.txt")); err != nil {
		t.Skip(err)
	}
	version, _ := workspaceVersion(real)
	raw, _ := json.Marshal(map[string]string{"path": "alias.txt", "content": "new", "version": version})
	w := httptest.NewRecorder()
	s.wsWrite(w, httptest.NewRequest("POST", "/api/workspace/write", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if _, err := os.Readlink(filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal("internal symlink was replaced")
	}
	data, _ := os.ReadFile(real)
	if string(data) != "new" {
		t.Fatal("target not updated")
	}
}
