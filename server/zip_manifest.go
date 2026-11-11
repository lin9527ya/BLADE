package server

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// zipManifestFile 是一条进包文件的校验记录。SHA256SUMS 与它同源，避免两份清单漂移。
type zipManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// zipManifest 是交付 ZIP 的整包清单（manifest.json）。Meta 放任务/格式/统计等元信息，
// Files 只列真正写入包内的文件；被跳过的发现放在 Meta["skipped_entries"]。
type zipManifest struct {
	Meta  map[string]any    `json:"meta"`
	Files []zipManifestFile `json:"files"`
}

func newZipManifest(meta map[string]any) *zipManifest {
	if meta == nil {
		meta = map[string]any{}
	}
	return &zipManifest{Meta: meta, Files: []zipManifestFile{}}
}

// add 写一个文件并记录 sha256/size。调用方负责保证 path 唯一。
func (m *zipManifest) add(zw *zip.Writer, path string, data []byte) error {
	w, err := zw.Create(path)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	m.Files = append(m.Files, zipManifestFile{
		Path: path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)),
	})
	return nil
}

// writeTo 追加 manifest.json 与 SHA256SUMS。SHA256SUMS 采用 `<hex>  <path>` 格式，
// 解压目录里可直接 `sha256sum -c SHA256SUMS` 校验。
func (m *zipManifest) writeTo(zw *zip.Writer) error {
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	w, err := zw.Create("manifest.json")
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	w, err = zw.Create("SHA256SUMS")
	if err != nil {
		return err
	}
	for _, f := range m.Files {
		if _, err := fmt.Fprintf(w, "%s  %s\n", f.SHA256, f.Path); err != nil {
			return err
		}
	}
	return nil
}
