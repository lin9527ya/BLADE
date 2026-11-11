package server

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"testing"
)

func srcBatchFixture(t *testing.T) (*Server, *Task) {
	t.Helper()
	s, f, _ := trafficEvidenceServer(t)
	full, err := s.m.pg.GetFinding(f.FindingID)
	if err != nil || full.TaskID == nil {
		t.Fatalf("fixture finding: %+v %v", full, err)
	}
	taskID := *full.TaskID
	t.Cleanup(func() {
		_, _ = s.m.pg.Exec(`DELETE FROM findings WHERE task_id=$1 AND name LIKE 'bulk-%'`, taskID)
		_, _ = s.m.pg.Exec(`DELETE FROM findings WHERE task_id=$1 AND name='retracted-bulk'`, taskID)
	})
	if _, err := s.m.pg.Exec(`
INSERT INTO findings(task_id,vulnclass,name,severity,summary,evidence,worker,status,confidence)
SELECT $1,'TEST','bulk-'||g,'high','bulk finding','evidence','test','confirmed','high'
FROM generate_series(1,501) g`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.m.pg.Exec(`
INSERT INTO findings(task_id,vulnclass,name,severity,summary,evidence,worker,status,confidence,retract_reason)
VALUES($1,'TEST','retracted-bulk','low','retracted','evidence','test','retracted','high','复测推翻')`, taskID); err != nil {
		t.Fatal(err)
	}
	task, ok := s.m.Task(strconv.FormatInt(taskID, 10))
	if !ok || task == nil {
		t.Fatal("task not found")
	}
	return s, task
}

// 交付计划不得在 500 条处静默截断；ZIP 必须含 manifest.json 与 SHA256SUMS，
// 且每个文件都能按清单校验。
func TestSrcBatchFullAndManifest(t *testing.T) {
	s, task := srcBatchFixture(t)
	plan, err := s.buildSrcBatch(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	var total int
	if err := s.m.pg.QueryRow(`SELECT COUNT(*) FROM findings WHERE task_id=$1`, mustI64(t, task.ID)).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if plan.Total != total || len(plan.Entries) != total {
		t.Fatalf("plan total=%d entries=%d db=%d", plan.Total, len(plan.Entries), total)
	}
	if plan.Total <= 500 {
		t.Fatalf("fixture did not exceed 500 findings: %d", plan.Total)
	}
	skipped := 0
	for _, e := range plan.Entries {
		if !e.Included {
			skipped++
			if e.SkipReason == "" {
				t.Fatalf("skipped entry #%d has no reason", e.ID)
			}
		}
	}
	if skipped == 0 {
		t.Fatal("retracted finding should be skipped")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "交付包.zip")
	manifest, err := s.buildSrcZipToFile(context.Background(), task, plan, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) == 0 {
		t.Fatal("manifest has no files")
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	for _, name := range []string{"提交说明.md", "manifest.json", "SHA256SUMS"} {
		if files[name] == nil {
			t.Fatalf("zip missing %s", name)
		}
	}
	raw, err := readZipFile(files["manifest.json"])
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Meta  map[string]any `json:"meta"`
		Files []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != len(manifest.Files) {
		t.Fatalf("manifest files=%d, want %d", len(got.Files), len(manifest.Files))
	}
	for _, f := range got.Files {
		zf := files[f.Path]
		if zf == nil {
			t.Fatalf("manifest references missing file %s", f.Path)
		}
		body, err := readZipFile(zf)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("sha256 mismatch for %s", f.Path)
		}
	}
	if skippedMeta, ok := got.Meta["skipped_entries"].([]any); !ok || len(skippedMeta) == 0 {
		t.Fatalf("manifest missing skipped_entries: %+v", got.Meta["skipped_entries"])
	}
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func mustI64(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
