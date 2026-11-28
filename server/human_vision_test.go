package server

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestManualChatImageOwnershipAndFormat(t *testing.T) {
	data := t.TempDir()
	base := filepath.Join(data, "sessions", "conv-1")
	dir := filepath.Join(base, "uploads")
	other := filepath.Join(data, "sessions", "conv-2", "uploads")
	for _, d := range []string{dir, other} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, other} {
		if err := os.WriteFile(filepath.Join(d, "shot"), imageData.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	a := chatAttachment{Name: "shot", Path: "uploads/shot", ImageMIME: "image/png"}
	if images, err := readChatImages(base, []chatAttachment{a}); err != nil || len(images) != 1 {
		t.Fatal(err)
	}
	bad := a
	bad.ImageMIME = "image/jpeg"
	if _, err := readChatImages(base, []chatAttachment{bad}); err == nil {
		t.Fatal("wrong MIME accepted")
	}
	bad = a
	bad.Path = "../conv-2/uploads/shot"
	if _, err := readChatImages(base, []chatAttachment{bad}); err == nil {
		t.Fatal("cross-session path accepted")
	}
	if err := os.Symlink(filepath.Join(other, "shot"), filepath.Join(dir, "link.png")); err == nil {
		if _, err := readChatImages(base, []chatAttachment{{Name: "link.png", Path: "uploads/link.png"}}); err == nil {
			t.Fatal("cross-session symlink accepted")
		}
	}
}
