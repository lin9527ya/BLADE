package server

import (
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
	SamplePath string                  `json:"sample_path"`
	Name       string                  `json:"name"`
	SizeBytes  int64                   `json:"size_bytes"`
	Manifest   *mobile.PackageMetadata `json:"manifest,omitempty"`
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
		if s.m.ResolveTask(id) == nil {
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

	dir := filepath.Join(s.m.dir, sub, id, "samples")
	if err := os.MkdirAll(dir, 0o775); err != nil {
		writeErr(w, 500, "建目录失败: "+err.Error())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAppUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "需要 multipart/form-data 上传: "+err.Error())
		return
	}

	var uploaded []appUploadResult
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, 400, "读取上传流失败（可能超过 1024 MiB 限制）: "+err.Error())
			return
		}
		rawName := part.FileName()
		if rawName == "" {
			_ = part.Close()
			continue
		}
		name := filepath.Base(rawName)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			_ = part.Close()
			writeErr(w, 400, fmt.Sprintf("非法文件名 %q", rawName))
			return
		}
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".apk", ".aab", ".xapk", ".apkm", ".apks", ".ipa":
		default:
			_ = part.Close()
			writeErr(w, 400, fmt.Sprintf("不支持的移动应用格式 %q（支持 .apk / .aab / .xapk / .ipa）", ext))
			return
		}

		dst := uniqueUploadPath(dir, name)
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			_ = part.Close()
			writeErr(w, 500, "落盘失败: "+err.Error())
			return
		}

		written, copyErr := io.Copy(f, part)
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

		// Verify ZIP magic bytes ("PK") right away before unpacking.
		if !hasZIPMagic(dst) {
			_ = os.Remove(dst)
			writeErr(w, 400, fmt.Sprintf("文件 %s 不是有效的 APK/IPA 压缩归档", name))
			return
		}

		meta, iErr := mobile.InspectPackage(dst)
		if iErr != nil {
			log.Printf("[app-upload] inspect warning for %s: %v", name, iErr)
		}
		rel, _ := filepath.Rel(s.m.dir, dst)
		rel = filepath.ToSlash(rel)
		uploaded = append(uploaded, appUploadResult{
			SamplePath: rel,
			Name:       name,
			SizeBytes:  written,
			Manifest:   meta,
		})
	}

	if len(uploaded) == 0 {
		writeErr(w, 400, "缺少安装包文件")
		return
	}

	// If uploaded directly to a live task, append sample paths and refresh task metadata.
	if taskID > 0 {
		s.adoptUploadedToTask(taskID, uploaded)
	}

	writeJSON(w, 200, map[string]any{
		"samples": uploaded,
		"primary": uploaded[0],
	})
}

func (s *Server) adoptUploadedToTask(taskID int64, items []appUploadResult) {
	cfg, _ := s.m.pg.TaskMobileInfo(taskID)
	paths := append([]string(nil), cfg.AppSamplePaths...)
	var primaryMeta *mobile.PackageMetadata
	if len(cfg.AppManifestJSON) > 2 {
		var m mobile.PackageMetadata
		if json.Unmarshal(cfg.AppManifestJSON, &m) == nil && m.PackageName != "" {
			primaryMeta = &m
		}
	}
	for _, it := range items {
		paths = appendUniqueStr(paths, it.SamplePath)
		if primaryMeta == nil && it.Manifest != nil {
			primaryMeta = it.Manifest
		}
	}
	var rawMan json.RawMessage
	platform, pkg := cfg.AppPlatform, cfg.AppPackageName
	if primaryMeta != nil {
		rawMan, _ = json.Marshal(primaryMeta)
		if platform == "" {
			platform = string(primaryMeta.Platform)
		}
		if pkg == "" {
			pkg = primaryMeta.PackageName
		}
	}
	if err := s.m.pg.UpdateTaskAppMetadata(taskID, paths, rawMan, platform, pkg); err == nil {
		if t, ok := s.m.Task(strconv.FormatInt(taskID, 10)); ok && t != nil {
			t.updateLifecycle(func(st *taskLifecycleState) {
				st.AppMode = "mobile"
				st.AppPlatform = platform
				st.AppPackageName = pkg
				st.AppSamplePaths = paths
				st.AppManifestJSON = rawMan
			})
		}
	}
}

// prepareTaskMobileSamples adopts staged draft sample paths into tasks/<taskID>/samples/,
// inspects the primary package, and seeds assets + approved domains + origin facts.
func (s *Server) prepareTaskMobileSamples(t *Task, draftPaths, extraHosts []string) {
	if t == nil {
		return
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	if taskID <= 0 {
		return
	}
	taskSamplesDir := filepath.Join(s.m.dir, "tasks", t.ID, "samples")
	var adopted []string
	var primaryMeta *mobile.PackageMetadata

	baseAbs, _ := filepath.Abs(s.m.dir)
	for _, rel := range draftPaths {
		rel = strings.TrimSpace(filepath.ToSlash(rel))
		if rel == "" {
			continue
		}
		srcAbs := rel
		if !filepath.IsAbs(srcAbs) {
			srcAbs = filepath.Join(s.m.dir, filepath.FromSlash(rel))
		}
		srcAbs, err := filepath.Abs(srcAbs)
		if err != nil || !strings.HasPrefix(srcAbs, baseAbs+string(filepath.Separator)) {
			continue
		}
		// Restrict adoption to drafts/<id>/samples/ or this task's own samples/.
		if !strings.Contains(filepath.ToSlash(srcAbs), "/samples/") {
			continue
		}
		if _, err := os.Stat(srcAbs); err != nil {
			continue
		}
		_ = os.MkdirAll(taskSamplesDir, 0o775)
		dstAbs := uniqueUploadPath(taskSamplesDir, filepath.Base(srcAbs))
		if err := moveOrCopyFile(srcAbs, dstAbs); err != nil {
			log.Printf("[mobile] adopt sample %s failed: %v", srcAbs, err)
			continue
		}
		dstRel, _ := filepath.Rel(s.m.dir, dstAbs)
		dstRel = filepath.ToSlash(dstRel)
		adopted = appendUniqueStr(adopted, dstRel)
		if primaryMeta == nil {
			if m, err := mobile.InspectPackage(dstAbs); err == nil {
				m.SamplePath = dstRel
				primaryMeta = m
			}
		}
	}

	var manRaw json.RawMessage
	platform, pkg := "", ""
	if primaryMeta != nil {
		manRaw, _ = json.Marshal(primaryMeta)
		platform = string(primaryMeta.Platform)
		pkg = primaryMeta.PackageName
	}
	if len(adopted) > 0 || len(manRaw) > 0 {
		if err := s.m.pg.UpdateTaskAppMetadata(taskID, adopted, manRaw, platform, pkg); err == nil {
			t.updateLifecycle(func(st *taskLifecycleState) {
				st.AppMode = "mobile"
				st.AppPlatform = platform
				st.AppPackageName = pkg
				st.AppSamplePaths = adopted
				st.AppManifestJSON = manRaw
			})
		}
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

func moveOrCopyFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	_ = os.Remove(src)
	return nil
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
