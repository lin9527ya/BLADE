package mobile

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ReportSummary struct {
	RunID         string `json:"run_id"`
	SamplePath    string `json:"sample_path"`
	SampleSHA256  string `json:"sample_sha256"`
	QualityStatus string `json:"quality_status"`
	GeneratedAt   string `json:"generated_at"`
	path          string
}

func reportPathKey(sample string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.ToSlash(sample))))
}

func readReportJSON(root *os.Root, name string, out any) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	const maxSize = 32 << 20
	if !info.Mode().IsRegular() || info.Size() > maxSize {
		return errors.New("报告文件类型或大小无效")
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, maxSize+1))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("报告末尾有无效数据")
	}
	return nil
}

// ReadReportHistory resolves only published directories beneath this task.
// The legacy single report is attributed only to its recorded sample.
func ReadReportHistory(taskRoot, sample, runID string) ([]ReportSummary, *DecompileAuditReport, error) {
	history := []ReportSummary{}
	root, err := os.OpenRoot(taskRoot)
	if os.IsNotExist(err) {
		if runID != "" {
			return nil, nil, os.ErrNotExist
		}
		return history, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	runs := filepath.Join("mobile_audit", "runs", reportPathKey(sample))
	list := func(dir string) ([]os.DirEntry, error) {
		f, e := root.Open(dir)
		if e != nil {
			return nil, e
		}
		defer f.Close()
		return f.ReadDir(-1)
	}
	dirs, err := list(runs)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	for _, d := range dirs {
		if !d.IsDir() || len(d.Name()) != 64 {
			continue
		}
		entries, e := list(filepath.Join(runs, d.Name()))
		if e != nil {
			return nil, nil, e
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			dir := filepath.Join(runs, d.Name(), entry.Name())
			var item ReportSummary
			if e := readReportJSON(root, filepath.Join(dir, "manifest.json"), &item); e != nil {
				return nil, nil, fmt.Errorf("读取运行摘要失败: %w", e)
			}
			if item.RunID != entry.Name() || item.SampleSHA256 != d.Name() {
				return nil, nil, errors.New("报告摘要标识不一致")
			}
			if item.SamplePath == sample {
				item.path = dir
				history = append(history, item)
			}
		}
	}
	old, legacyErr := protectedLegacyReport(taskRoot)
	if legacyErr != nil && (runID == "legacy" || len(history) == 0) {
		return nil, nil, legacyErr
	}
	if old != nil && old.SamplePath == sample {
		history = append(history, ReportSummary{RunID: "legacy", SamplePath: old.SamplePath, SampleSHA256: old.SampleSHA256, QualityStatus: old.QualityStatus, GeneratedAt: old.GeneratedAt, path: "mobile_audit"})
	}
	sort.Slice(history, func(i, j int) bool {
		if history[j].RunID == "legacy" {
			return history[i].RunID != "legacy"
		}
		if history[i].RunID == "legacy" {
			return false
		}
		left, _ := time.Parse(time.RFC3339Nano, history[i].GeneratedAt)
		right, _ := time.Parse(time.RFC3339Nano, history[j].GeneratedAt)
		if !left.Equal(right) {
			return left.After(right)
		}
		return history[i].RunID > history[j].RunID
	})
	for _, item := range history {
		if runID != "" && item.RunID != runID {
			continue
		}
		var rep DecompileAuditReport
		if item.RunID != "legacy" {
			if err := readReportJSON(root, filepath.Join(item.path, "report.json"), &rep); err != nil {
				return nil, nil, err
			}
			if rep.RunID != item.RunID || rep.SampleSHA256 != item.SampleSHA256 || rep.SamplePath != sample || rep.AuditDir != filepath.ToSlash(item.path) || rep.DecompiledDir != filepath.ToSlash(filepath.Join(item.path, "sources")) {
				return nil, nil, errors.New("报告与样本不匹配")
			}
		} else {
			rep = *old
			rep.RunID = "legacy"
		}
		rep.ProtectSharedReport()
		return history, &rep, nil
	}
	if runID != "" {
		return nil, nil, os.ErrNotExist
	}
	return history, nil, nil
}

// ProtectSharedReport keeps file/line provenance without copying original
// source into the shared report. Also applied when reading earlier formats.
func (r *DecompileAuditReport) ProtectSharedReport() {
	for i, warn := range r.Warnings {
		if strings.HasPrefix(warn, "jadx exited") || strings.HasPrefix(warn, "apktool exited") || strings.HasPrefix(warn, "hbc-decompiler exited") {
			r.Warnings[i], _, _ = strings.Cut(warn, " (")
		}
	}
	for i := range r.Secrets {
		r.Secrets[i].Snippet = ""
	}
	for i := range r.Risks {
		r.Risks[i].Snippet = ""
	}
	for i := range r.SignCandidates {
		r.SignCandidates[i].Snippet = ""
	}
	for i := range r.Endpoints {
		r.Endpoints[i].URL = reportURL(r.Endpoints[i].URL)
	}
	if r.ManifestMeta != nil {
		r.ManifestMeta.ProtectReportMetadata()
	}
}
func reportURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[地址格式无效]"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return u.String()
}

// ProtectLegacyReport upgrades the shared copies in place, without touching
// source files. Publish the report last, so interruption can be retried.
func ProtectLegacyReport(taskRoot string) error {
	_, err := protectedLegacyReport(taskRoot)
	return err
}

func protectedLegacyReport(taskRoot string) (*DecompileAuditReport, error) {
	dir := filepath.Join(taskRoot, "mobile_audit")
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("历史报告目录无效")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	fi, err = root.Lstat("report.json")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("历史报告不是普通文件")
	}
	var r DecompileAuditReport
	if err = readReportJSON(root, "report.json", &r); err != nil {
		return nil, err
	}
	before, err := json.Marshal(&r)
	if err != nil {
		return nil, err
	}
	r.ProtectSharedReport()
	after, err := json.Marshal(&r)
	if err != nil {
		return nil, err
	}
	if string(before) == string(after) {
		return &r, nil
	}
	tmp, err := os.MkdirTemp(dir, ".upgrade-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err = writeDecompileAuditArtifacts(tmp, &r); err != nil {
		return nil, err
	}
	for _, name := range []string{"manifest.json", "endpoints.txt", "secrets.txt", "report.json"} {
		if err = os.Rename(filepath.Join(tmp, name), filepath.Join(dir, name)); err != nil {
			return nil, err
		}
	}
	return &r, nil
}
