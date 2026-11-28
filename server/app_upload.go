package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/mobile"
)

// maxAppUpload caps mobile package streaming uploads (.apk/.aab/.xapk/.ipa) at 1 GiB.
const maxAppUpload = 1024 << 20 // 1024 MiB

type appUploadResult struct {
	SHA256       string                  `json:"sha256,omitempty"`
	Upload       string                  `json:"upload"`
	Inspection   string                  `json:"inspection"`
	Registration string                  `json:"registration"`
	Message      string                  `json:"message,omitempty"`
	AppID        string                  `json:"appid,omitempty"`
	SamplePath   string                  `json:"sample_path"`
	Name         string                  `json:"name"`
	SizeBytes    int64                   `json:"size_bytes"`
	Manifest     *mobile.PackageMetadata `json:"manifest,omitempty"`
}

// appUpload streams a multipart uploaded mobile sample directly to disk (up to 1024 MiB)
// and immediately performs static package inspection without buffering in memory.
//
// POST /api/apps/upload?scope=staging|task&id=<id>
func (s *Server) appUpload(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(20 * time.Minute))

	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = "staging"
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if !safeChatID.MatchString(id) {
		writeErr(w, 400, "非法 id")
		return
	}

	var sub string
	var taskID int64
	switch scope {
	case "staging":
		sub = "drafts"
	case "task":
		sub = "tasks"
		if _, exists := s.m.Task(id); !exists {
			writeErr(w, 404, "task not found")
			return
		}
		if !s.engine.beginTaskOperation(id) {
			writeErr(w, http.StatusConflict, "任务正在删除，无法上传安装包")
			return
		}
		defer s.engine.decInflight(id)
		taskID, _ = strconv.ParseInt(id, 10, 64)
	default:
		writeErr(w, 400, "scope 必须是 staging 或 task")
		return
	}

	dir, err := ensureMobileDir(s.m.dir, sub, id, "samples")
	if err != nil {
		writeErr(w, 500, "建目录失败: "+err.Error())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAppUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "需要 multipart/form-data 上传: "+err.Error())
		return
	}

	hintAppID := strings.TrimSpace(r.URL.Query().Get("appid"))
	var uploaded []appUploadResult
	var staged []string
	keep := false
	defer func() {
		if !keep {
			for _, path := range staged {
				_ = os.Remove(path)
				_ = os.Remove(mobileReceiptFile(path))
			}
		}
	}()
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, "读取上传流失败（可能超过 1024 MiB 限制）: "+err.Error())
			return
		}
		if part.FileName() == "" {
			if strings.EqualFold(part.FormName(), "appid") && hintAppID == "" {
				b, _ := io.ReadAll(io.LimitReader(part, 64))
				hintAppID = strings.TrimSpace(string(b))
			}
			_ = part.Close()
			continue
		}
		rawName := part.FileName()
		name := filepath.Base(rawName)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			_ = part.Close()
			writeErr(w, 400, fmt.Sprintf("非法文件名 %q", rawName))
			return
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".apk", ".aab", ".xapk", ".apkm", ".apks", ".ipa", ".wxapkg", ".zip":
		default:
			_ = part.Close()
			writeErr(w, 400, fmt.Sprintf("不支持的应用包格式 %q（支持 .apk / .aab / .xapk / .apkm / .apks / .ipa / .wxapkg / .zip）", ext))
			return
		}

		dst, f, err := reserveMobileSample(dir, name)
		if err != nil {
			_ = part.Close()
			writeErr(w, 500, "落盘失败: "+err.Error())
			return
		}

		staged = append(staged, dst)
		digest := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(f, digest), part)
		closeErr := f.Close()
		_ = part.Close()
		if copyErr != nil {
			_ = os.Remove(dst)
			writeErr(w, 400, "写入安装包失败: "+copyErr.Error())
			return
		}
		if closeErr != nil {
			_ = os.Remove(dst)
			writeErr(w, 500, "保存安装包失败: "+closeErr.Error())
			return
		}

		// Verify ZIP or wxapkg magic bytes cleanly before parsing.
		switch ext {
		case ".wxapkg":
			if _, ok := mobile.HasWxapkgMagic(dst); !ok {
				_ = os.Remove(dst)
				writeErr(w, 400, fmt.Sprintf("文件 %s 不是有效的微信小程序 .wxapkg 包", name))
				return
			}
		case ".zip":
			if kind, ok := mobile.HasWxapkgMagic(dst); !ok || kind != "zip_bundle" {
				_ = os.Remove(dst)
				writeErr(w, 400, fmt.Sprintf("ZIP 压缩包 %s 内未检测到 .wxapkg 小程序包", name))
				return
			}
		default:
			if !hasZIPMagic(dst) {
				_ = os.Remove(dst)
				writeErr(w, 400, fmt.Sprintf("文件 %s 不是有效的 APK/IPA 压缩归档", name))
				return
			}
		}

		rel, _ := filepath.Rel(s.m.dir, dst)
		uploaded = append(uploaded, appUploadResult{SamplePath: filepath.ToSlash(rel), SizeBytes: written, SHA256: fmt.Sprintf("%x", digest.Sum(nil))})
	}

	if len(uploaded) == 0 {
		writeErr(w, 400, "缺少安装包文件")
		return
	}

	for i, item := range uploaded {
		uploaded[i] = inspectUploadedSample(staged[i], item.SamplePath, hintAppID, item.SizeBytes)
		if item.SHA256 != "" {
			uploaded[i].SHA256 = item.SHA256
		}
		if err = saveMobileReceipt(staged[i], uploaded[i]); err != nil {
			writeErr(w, 500, "保存上传回执失败，请重新上传")
			return
		}
	}
	keep = true
	status := 200
	if taskID > 0 {
		if err := s.adoptUploadedToTask(taskID, uploaded); err != nil {
			status = http.StatusMultiStatus
			for i := range uploaded {
				uploaded[i].Registration = "failed"
				_ = saveMobileReceipt(staged[i], uploaded[i])
			}
		} else {
			for i := range uploaded {
				uploaded[i].Registration = "registered"
			}
		}
	}
	writeJSON(w, status, map[string]any{
		"samples": uploaded,
		"primary": uploaded[0],
	})
}

func (s *Server) adoptUploadedToTask(taskID int64, items []appUploadResult) error {
	if len(items) == 0 {
		return nil
	}
	s.mobileSampleMu.Lock()
	defer s.mobileSampleMu.Unlock()
	paths := make([]string, 0, len(items))
	for _, it := range items {
		paths = append(paths, it.SamplePath)
	}
	cfg, err := s.m.pg.AppendTaskMobileSamples(taskID, s.m.dir, paths, items[0].Manifest)
	if err != nil {
		return err
	}
	if t, ok := s.m.Task(strconv.FormatInt(taskID, 10)); ok {
		t.updateLifecycle(func(st *taskLifecycleState) {
			st.AppMode = "mobile"
			st.AppSamplePaths = cfg.AppSamplePaths
			st.AppPlatform = cfg.AppPlatform
			st.AppPackageName = cfg.AppPackageName
			st.AppManifestJSON = cfg.AppManifestJSON
		})
	}
	for _, item := range items {
		item.Registration = "registered"
		if err := saveMobileReceipt(filepath.Join(s.m.dir, filepath.FromSlash(item.SamplePath)), item); err != nil {
			// The committed DB entry is authoritative when serving the receipt.
			log.Printf("[mobile] 任务 %d 已登记，回执状态同步失败: %v", taskID, err)
		}
	}
	return nil
}

// prepareTaskMobileSamples adopts staged draft sample paths into tasks/<taskID>/samples/,
// inspects the primary package, and seeds assets + approved domains + origin facts.
func (s *Server) prepareTaskMobileSamples(t *Task, draftPaths, extraHosts []string) []appUploadResult {
	if t == nil {
		return nil
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	if taskID <= 0 {
		return nil
	}
	base, _ := filepath.Abs(s.m.dir)
	dstDir := filepath.Join(base, "tasks", t.ID, "samples")
	var results, valid []appUploadResult
	var primaryMeta *mobile.PackageMetadata
	var removeAfterCommit []string
	for _, path := range draftPaths {
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(base, filepath.FromSlash(path))
		}
		rel, e := filepath.Rel(base, abs)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		fail := appUploadResult{Name: filepath.Base(path), Upload: "unknown", Inspection: "unknown", Registration: "failed", Message: "草稿样本不可用，请重新上传"}
		if e != nil || len(parts) != 4 || parts[2] != "samples" || !(parts[0] == "drafts" && safeChatID.MatchString(parts[1]) || parts[0] == "tasks" && parts[1] == t.ID) {
			results = append(results, fail)
			continue
		}
		real, e := s.realMobileWorkspaceRel(abs)
		fi, statErr := os.Stat(abs)
		if e != nil || real != filepath.ToSlash(rel) || statErr != nil || !fi.Mode().IsRegular() {
			results = append(results, fail)
			continue
		}
		prev, e := readMobileReceipt(abs)
		if e != nil && !os.IsNotExist(e) {
			results = append(results, fail)
			continue
		}
		if _, e = ensureMobileDir(base, "tasks", t.ID, "samples"); e != nil {
			results = append(results, fail)
			continue
		}
		dst := abs
		if parts[0] == "drafts" {
			dst, e = copyMobileSample(abs, dstDir)
			if e != nil {
				results = append(results, fail)
				continue
			}
		}
		destRel, _ := filepath.Rel(base, dst)
		item := inspectUploadedSample(dst, destRel, prev.AppID, fi.Size())
		if prev.SHA256 != "" && prev.SHA256 != item.SHA256 {
			if dst != abs {
				_ = os.Remove(dst)
			}
			fail.Message = "草稿样本与上传时的摘要不匹配，请重新上传"
			results = append(results, fail)
			continue
		}
		if e = saveMobileReceipt(dst, item); e != nil {
			if dst != abs {
				_ = os.Remove(dst)
			}
			results = append(results, fail)
			continue
		}
		if dst != abs {
			removeAfterCommit = append(removeAfterCommit, abs)
		}
		item.Registration = "registered"
		results = append(results, item)
		valid = append(valid, item)
		if len(valid) == 1 {
			primaryMeta = item.Manifest
		}
	}
	if err := s.adoptUploadedToTask(taskID, valid); err != nil {
		for i := range results {
			results[i].Registration = "failed"
			if results[i].Upload == "stored" {
				_ = saveMobileReceipt(filepath.Join(base, results[i].SamplePath), results[i])
			}
		}
		return results
	}
	for _, src := range removeAfterCommit {
		_ = os.Remove(src)
		_ = os.Remove(mobileReceiptFile(src))
	}
	// Register APP asset + explicit target hosts into asset graph and task_scope.
	as := s.m.Assets()
	beginFact, _ := t.Store.OriginFactID()
	if as != nil && primaryMeta != nil && (primaryMeta.PackageName != "" || primaryMeta.AppName != "") {
		appName := primaryMeta.AppName
		if appName == "" {
			appName = primaryMeta.PackageName
		}
		desc := fmt.Sprintf("%s v%s (%s)", primaryMeta.PackageName, primaryMeta.VersionName, primaryMeta.Platform)
		if aid, err := as.UpsertApp(db.UpsertAppReq{
			BundleID:    primaryMeta.PackageName,
			Name:        appName,
			Category:    string(primaryMeta.Platform),
			Description: desc,
			TaskID:      taskID,
		}); err == nil && aid > 0 {
			_ = as.SetTaskAssetSource(taskID, aid, "task", "由上传的移动应用安装包解析生成", nil)
			if beginFact > 0 {
				_ = t.Store.Anchor(beginFact, aid)
			}
		}
	}

	// Add operator-approved extra hosts (API domains / gateways) to asset graph & task_scope.
	if as != nil {
		for _, h := range extraHosts {
			h = strings.ToLower(strings.TrimSpace(h))
			if h == "" {
				continue
			}
			var aid int64
			if net.ParseIP(h) != nil {
				aid, _ = as.UpsertIP(db.UpsertIPReq{IP: h, TaskID: taskID})
				_, _ = as.AddAgentScope(taskID, "ip", h, "移动 APP 关联后端域名/IP", "manual")
			} else if strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
				aid, _ = as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: h, TaskID: taskID})
				if uHost := strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://"); uHost != "" {
					if i := strings.IndexAny(uHost, "/:"); i > 0 {
						uHost = uHost[:i]
					}
					_, _ = as.AddAgentScope(taskID, "subdomain", uHost, "移动 APP 关联后端 API", "manual")
				}
			} else {
				aid, _ = as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: h, TaskID: taskID})
				_, _ = as.AddAgentScope(taskID, "subdomain", h, "移动 APP 关联后端域名", "manual")
			}
			if aid > 0 {
				_ = as.SetTaskAssetSource(taskID, aid, "task", "移动任务创建时指定的后端域", nil)
				if beginFact > 0 {
					_ = t.Store.Anchor(beginFact, aid)
				}
			}
		}
	}

	// Emit a structured mobile static summary fact right under origin so Planner sees it cleanly.
	if primaryMeta != nil && t.Store != nil {
		manRaw, _ := json.Marshal(primaryMeta)
		highCount, medCount := 0, 0
		var topTitles []string
		for _, sf := range primaryMeta.StaticFindings {
			switch sf.Severity {
			case "high":
				highCount++
				if len(topTitles) < 5 {
					topTitles = append(topTitles, "[HIGH] "+sf.Title)
				}
			case "medium":
				medCount++
				if len(topTitles) < 5 {
					topTitles = append(topTitles, "[MED] "+sf.Title)
				}
			}
		}
		summary := fmt.Sprintf("移动样本预解析完成：%s (%s v%s)，签名=%v，原生库=%d，SAST 发现高=%d/中=%d，框架=%v，壳=%v，候选域名=%v，DeepLink=%v",
			primaryMeta.PackageName, primaryMeta.Platform, primaryMeta.VersionName,
			primaryMeta.SignatureSchemes, len(primaryMeta.NativeBinaries), highCount, medCount,
			primaryMeta.Frameworks, primaryMeta.Packers, primaryMeta.CandidateDomains, primaryMeta.URLSchemes)
		_, _ = t.Store.AddLinkedNode(db.KindFact, map[string]any{
			"summary":  summary,
			"detail":   string(manRaw),
			"category": "mobile_manifest",
			"sample":   primaryMeta.SamplePath,
		}, 0, "confirmed", "system", nil, beginFact, db.RelYields)
		if len(topTitles) > 0 {
			_, _ = t.Store.AddLinkedNode(db.KindFact, map[string]any{
				"summary":  fmt.Sprintf("移动端静态 SAST 预检命中高危/中危风险项（共 高%d / 中%d 条）：%s", highCount, medCount, strings.Join(topTitles, "；")),
				"category": "mobile_sast",
				"sample":   primaryMeta.SamplePath,
			}, 0, "confirmed", "system", nil, beginFact, db.RelYields)
		}
	}
	return results
}

func hasZIPMagic(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var buf [4]byte
	n, err := io.ReadFull(f, buf[:])
	return err == nil && n >= 2 && buf[0] == 'P' && buf[1] == 'K'
}

func reserveMobileSample(dir, name string) (string, *os.File, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 0; ; i++ {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		path := filepath.Join(dir, candidate)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if !os.IsExist(err) {
			return path, f, err
		}
	}
}
func copyMobileSample(src, dir string) (string, error) {
	in, e := os.Open(src)
	if e != nil {
		return "", e
	}
	defer in.Close()
	path, out, e := reserveMobileSample(dir, filepath.Base(src))
	if e != nil {
		return "", e
	}
	_, e = io.Copy(out, in)
	ce := out.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		_ = os.Remove(path)
		return "", e
	}
	return path, nil
}
func appendUniqueStr(ss []string, v string) []string {
	if v == "" {
		return ss
	}
	for _, s := range ss {
		if s == v {
			return ss
		}
	}
	return append(ss, v)
}
