package mobile

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	reTokenLeak   = regexp.MustCompile(`(?i)(?:authorization:\s*(?:bearer|basic)\s+\S+|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{8,}|(?:access_token|secret|password|passwd|app_secret|api_key)\s*[=:]\s*["']?[^\s"'&<>]{6,})`)
	reLogSecret   = regexp.MustCompile(`(?i)(?:authorization|bearer\s+\S+|eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|password\s*[=:]|secret\s*[=:]|token\s*[=:])`)
	dangerousText = []string{
		"注销", "退出", "退出登录", "删除", "清空", "销毁", "支付", "立即付款", "确认支付", "转账", "卸载",
		"logout", "sign out", "delete account", "remove", "pay now", "confirm pay", "transfer", "uninstall",
	}
)

// FuzzIPC automatically probes unprotected exported Android components (activities, services,
// receivers, providers) and declared DeepLink URI schemes from PackageMetadata.
func (m *Manager) FuzzIPC(ctx context.Context, serial, pkg string, meta *PackageMetadata, maxProbes int) (*IPCFuzzReport, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, fmt.Errorf("package metadata required for automated IPC fuzzing")
	}
	if pkg == "" {
		pkg = meta.PackageName
	}
	if err := ValidatePackageName(pkg); err != nil {
		return nil, err
	}
	if maxProbes <= 0 || maxProbes > 60 {
		maxProbes = 25
	}

	report := &IPCFuzzReport{Serial: serial, Package: pkg}
	addProbe := func(target, severity string, req IPCProbeRequest) {
		if report.ProbedCount >= maxProbes {
			return
		}
		res, err := m.ProbeIPC(ctx, serial, req)
		report.ProbedCount++
		if err != nil {
			report.Results = append(report.Results, IPCFuzzResult{
				Kind: req.Kind, Target: target, Summary: err.Error(), Severity: "info",
			})
			return
		}
		outLower := strings.ToLower(res.Output)
		denied := strings.Contains(outLower, "securityexception") ||
			strings.Contains(outLower, "permission denial") ||
			strings.Contains(outLower, "requires permission")
		accessible := res.Success && !denied
		if accessible {
			report.AccessibleCount++
		} else {
			report.DeniedCount++
		}
		sev := "info"
		if accessible {
			sev = severity
		}
		summary := res.Output
		if len(summary) > 260 {
			summary = summary[:260] + "..."
		}
		report.Results = append(report.Results, IPCFuzzResult{
			Kind:       req.Kind,
			Target:     target,
			Command:    res.Command,
			Accessible: accessible,
			Denied:     denied,
			ExitCode:   res.ExitCode,
			Summary:    strings.TrimSpace(summary),
			Severity:   sev,
		})
	}

	for _, c := range meta.ExportedComponents {
		if !c.Exported {
			continue
		}
		sev := "medium"
		if c.Permission == "" && c.ReadPermission == "" && c.WritePermission == "" && (c.Type == "provider" || c.Type == "receiver" || c.Type == "service") {
			sev = "high"
		}
		switch c.Type {
		case "activity":
			addProbe(c.Name, sev, IPCProbeRequest{
				Kind:      "activity",
				Package:   pkg,
				Component: c.Name,
				Extras:    map[string]string{"blade_ipc_probe": "1", "url": "https://example.invalid/blade-canary"},
			})
		case "service":
			addProbe(c.Name, sev, IPCProbeRequest{
				Kind:      "service",
				Package:   pkg,
				Component: c.Name,
				Extras:    map[string]string{"blade_ipc_probe": "1"},
			})
		case "receiver":
			act := ""
			if len(c.Filters) > 0 && len(c.Filters[0].Actions) > 0 {
				act = c.Filters[0].Actions[0]
			}
			addProbe(c.Name, sev, IPCProbeRequest{
				Kind:      "broadcast",
				Package:   pkg,
				Component: c.Name,
				Action:    act,
				Extras:    map[string]string{"blade_ipc_probe": "1"},
			})
		case "provider":
			if c.Authorities == "" {
				continue
			}
			for _, auth := range strings.Split(c.Authorities, ";") {
				auth = strings.TrimSpace(auth)
				if auth == "" {
					continue
				}
				// Read-only boundary query against authority root
				addProbe("content://"+auth+"/", "high", IPCProbeRequest{
					Kind:    "provider",
					Package: pkg,
					URI:     "content://" + auth + "/",
				})
				// Benign file provider path segment check
				if c.GrantURIPermissions {
					addProbe("content://"+auth+"/../", "high", IPCProbeRequest{
						Kind:    "provider",
						Package: pkg,
						URI:     "content://" + auth + "/../",
					})
				}
			}
		}
	}

	for _, scheme := range meta.URLSchemes {
		if scheme == "" || scheme == "http" || scheme == "https" {
			continue
		}
		uri := scheme + "://blade-probe.invalid/open?url=https://blade-canary.invalid/"
		addProbe(uri, "medium", IPCProbeRequest{
			Kind:    "deeplink",
			Package: pkg,
			URI:     uri,
		})
	}
	return report, nil
}

// AuditSandboxStorage inspects rooted /data/data/<pkg>/ and /sdcard/Android/data/<pkg>/
// for unencrypted SQLite DBs, plaintext tokens/secrets in SharedPreferences or WebView storage,
// and over-permissive file permissions.
func (m *Manager) AuditSandboxStorage(ctx context.Context, serial, pkg string) (*StorageAuditReport, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	if err := ValidatePackageName(pkg); err != nil {
		return nil, err
	}

	script := fmt.Sprintf(`
PKG=%s
BASE="/data/data/$PKG"
if [ ! -d "$BASE" ]; then echo "ERR:sandbox_missing"; exit 2; fi
echo "===PERMS==="
ls -l "$BASE" "$BASE/shared_prefs" "$BASE/databases" 2>/dev/null | head -n 80 || true
echo "===SQLITE==="
for f in "$BASE/databases/"*; do
  [ -f "$f" ] || continue
  HDR=$(head -c 15 "$f" 2>/dev/null | tr -cd '[:print:]')
  echo "DB:$f|$HDR"
done
echo "===PREFS==="
for f in "$BASE/shared_prefs/"*.xml "$BASE/app_webview/"* "$BASE/cache/"*; do
  [ -f "$f" ] || continue
  SIZE=$(wc -c < "$f" 2>/dev/null || echo 0)
  if [ "$SIZE" -le 65536 ]; then
    echo "FILE_START:$f"
    cat "$f" 2>/dev/null || true
    echo ""
    echo "FILE_END:$f"
  fi
done
echo "===EXT==="
ls -la "/sdcard/Android/data/$PKG" "/sdcard/Android/data/$PKG/files" 2>/dev/null | head -n 40 || true
`, shellQuote(pkg))

	out, stderr, code, err := m.runRootShell(ctx, serial, script)
	if err != nil || code != 0 {
		return nil, fmt.Errorf("sandbox storage audit failed (root required): %s %s (%v)", strings.TrimSpace(out), strings.TrimSpace(stderr), err)
	}

	report := &StorageAuditReport{Serial: serial, Package: pkg}
	section := ""
	curFile := ""
	var curBuf strings.Builder

	flushFile := func() {
		if curFile == "" {
			return
		}
		txt := curBuf.String()
		if m := reTokenLeak.FindString(txt); m != "" {
			preview := m
			if len(preview) > 72 {
				preview = preview[:72] + "..."
			}
			kind := "shared_prefs"
			if strings.Contains(curFile, "app_webview") {
				kind = "webview"
			} else if strings.Contains(curFile, "/cache/") {
				kind = "cache"
			}
			report.Findings = append(report.Findings, StorageFinding{
				Location: curFile,
				Kind:     kind,
				Severity: "high",
				Summary:  fmt.Sprintf("在应用私有文件 %s 中发现疑似明文凭据/Token 持久化", curFile),
				Evidence: preview,
			})
		}
		curFile = ""
		curBuf.Reset()
	}

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch line {
		case "===PERMS===", "===SQLITE===", "===PREFS===", "===EXT===":
			flushFile()
			section = line
			continue
		}
		switch section {
		case "===PERMS===":
			// Check world-readable or world-writable permissions (------r-- / -------w-)
			fields := strings.Fields(line)
			if len(fields) >= 8 && len(fields[0]) >= 10 {
				perm := fields[0]
				if (perm[7] == 'r' || perm[8] == 'w') && !strings.HasPrefix(perm, "l") {
					fname := fields[len(fields)-1]
					report.Findings = append(report.Findings, StorageFinding{
						Location: fname,
						Kind:     "permission",
						Severity: "medium",
						Summary:  fmt.Sprintf("文件/目录存在全局可读或可写权限 (%s: %s)", perm, fname),
						Evidence: line,
					})
				}
			}
		case "===SQLITE===":
			if strings.HasPrefix(line, "DB:") {
				rest := strings.TrimPrefix(line, "DB:")
				dbPath, hdr, _ := strings.Cut(rest, "|")
				if strings.HasSuffix(dbPath, "-wal") || strings.HasSuffix(dbPath, "-shm") || strings.HasSuffix(dbPath, "-journal") {
					continue
				}
				if strings.HasPrefix(hdr, "SQLite format 3") {
					report.SQLiteFiles = append(report.SQLiteFiles, dbPath)
					report.Findings = append(report.Findings, StorageFinding{
						Location: dbPath,
						Kind:     "sqlite",
						Severity: "medium",
						Summary:  "检测到未加密明文 SQLite 数据库 (" + dbPath + ")",
						Evidence: "magic=SQLite format 3",
					})
				} else {
					report.CipherDBFiles = append(report.CipherDBFiles, dbPath)
				}
			}
		case "===PREFS===":
			if strings.HasPrefix(line, "FILE_START:") {
				flushFile()
				curFile = strings.TrimPrefix(line, "FILE_START:")
				if strings.HasSuffix(curFile, ".xml") && strings.Contains(curFile, "shared_prefs") {
					report.PrefFiles = append(report.PrefFiles, curFile)
				}
				continue
			}
			if strings.HasPrefix(line, "FILE_END:") {
				flushFile()
				continue
			}
			if curFile != "" && curBuf.Len() < 65536 {
				curBuf.WriteString(line)
				curBuf.WriteByte('\n')
			}
		case "===EXT===":
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "total") && !strings.HasSuffix(trimmed, " .") && !strings.HasSuffix(trimmed, " ..") && strings.Contains(trimmed, " ") {
				fields := strings.Fields(trimmed)
				name := fields[len(fields)-1]
				if name != "." && name != ".." && name != "files" {
					report.Findings = append(report.Findings, StorageFinding{
						Location: "/sdcard/Android/data/" + pkg,
						Kind:     "external_storage",
						Severity: "low",
						Summary:  "外部公共存储目录存在应用落盘文件：" + name,
						Evidence: trimmed,
					})
					section = "" // report summary once
				}
			}
		}
	}
	flushFile()
	return report, nil
}

// CaptureLogcatAudit reads recent logcat lines related to pkg and flags sensitive tokens,
// secrets, URLs, or fatal unhandled exceptions.
func (m *Manager) CaptureLogcatAudit(ctx context.Context, serial, pkg string, lines int) (*LogcatAuditReport, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	if err := ValidatePackageName(pkg); err != nil {
		return nil, err
	}
	if lines <= 0 || lines > 2000 {
		lines = 500
	}
	uidOut, _, _, _ := m.runADBDevice(ctx, serial, "shell", "dumpsys", "package", pkg)
	uid := ""
	for _, ln := range strings.Split(uidOut, "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "userId=") {
			uid = strings.Fields(strings.TrimPrefix(ln, "userId="))[0]
			break
		}
	}

	out, stderr, code, err := m.runADBDevice(ctx, serial, "logcat", "-d", "-t", fmt.Sprintf("%d", lines), "-v", "brief")
	if err != nil && code != 0 && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("adb logcat failed: %s %s (%v)", out, stderr, err)
	}

	report := &LogcatAuditReport{Serial: serial, Package: pkg}
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if uid != "" && !strings.Contains(line, pkg) && !strings.Contains(line, uid) {
			// Still keep global explicit package references or clear crashes matching package
			continue
		}
		report.ScannedLines++
		switch {
		case reLogSecret.MatchString(line):
			report.Findings = append(report.Findings, LogcatFinding{
				Kind: "token", Severity: "high", Line: clipUIString(line, 260),
			})
		case strings.Contains(line, "https://") || strings.Contains(line, "http://"):
			report.Findings = append(report.Findings, LogcatFinding{
				Kind: "url", Severity: "info", Line: clipUIString(line, 260),
			})
		case strings.Contains(line, "Exception") || strings.Contains(line, "FATAL"):
			report.Findings = append(report.Findings, LogcatFinding{
				Kind: "stacktrace", Severity: "low", Line: clipUIString(line, 260),
			})
		}
		if len(report.Findings) >= 50 {
			break
		}
	}
	return report, nil
}

// CrawlUI performs a bounded state-machine traversal across Android screens using UIDump + safe tap/back.
func (m *Manager) CrawlUI(ctx context.Context, serial string, req UICrawlRequest, trafficCountFn func() int64) (*UICrawlResult, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	if req.Package != "" {
		if err := ValidatePackageName(req.Package); err != nil {
			return nil, err
		}
	}
	maxSteps := req.MaxSteps
	if maxSteps <= 0 || maxSteps > 40 {
		maxSteps = 12
	}
	delay := time.Duration(req.StepDelayMs) * time.Millisecond
	if delay <= 0 || delay > 5*time.Second {
		delay = 900 * time.Millisecond
	}

	var startCount int64
	if trafficCountFn != nil {
		startCount = trafficCountFn()
	}

	// Ensure target app is brought to foreground first if specified.
	if req.Package != "" {
		_, _ = m.PerformUIAction(ctx, serial, UIActionRequest{Action: "launch", Package: req.Package})
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	result := &UICrawlResult{Serial: serial, Package: req.Package}
	seenScreens := map[string]bool{}
	clickedKeys := map[string]bool{}

	for step := 0; step < maxSteps; step++ {
		if ctx.Err() != nil {
			break
		}
		dump, err := m.UIDump(ctx, serial)
		if err != nil {
			break
		}
		if req.Package != "" && dump.ForegroundPkg != "" && dump.ForegroundPkg != req.Package {
			// Steered outside target app (e.g. launcher or external browser) -> press Back once.
			_, _ = m.PerformUIAction(ctx, serial, UIActionRequest{Action: "back"})
			result.StepsExecuted++
			select {
			case <-time.After(delay):
			case <-ctx.Done():
			}
			continue
		}

		screenHash := hashUIScreen(dump)
		if !seenScreens[screenHash] {
			seenScreens[screenHash] = true
			sampleText := ""
			for _, el := range dump.Elements {
				if el.Text != "" {
					sampleText = el.Text
					break
				}
			}
			result.Screens = append(result.Screens, CrawledScreen{
				ScreenHash:    screenHash,
				ForegroundPkg: dump.ForegroundPkg,
				ElementCount:  dump.ElementCount,
				SampleText:    clipUIString(sampleText, 64),
			})
		}

		// Pick first unvisited safe clickable widget on current screen.
		var picked *UIElement
		var pickKey string
		for i := range dump.Elements {
			el := &dump.Elements[i]
			if !el.Clickable || !el.Enabled || (el.CenterX <= 0 && el.CenterY <= 0) {
				continue
			}
			if isUnsafeUIElement(el) {
				continue
			}
			k := fmt.Sprintf("%s|%s|%d,%d", screenHash, el.ResourceID, el.CenterX, el.CenterY)
			if clickedKeys[k] {
				continue
			}
			picked = el
			pickKey = k
			break
		}

		if picked == nil {
			_, _ = m.PerformUIAction(ctx, serial, UIActionRequest{Action: "back"})
			result.StepsExecuted++
		} else {
			clickedKeys[pickKey] = true
			lbl := picked.Text
			if lbl == "" {
				lbl = picked.ContentDesc
			}
			if lbl == "" {
				lbl = picked.ResourceID
			}
			if len(result.Screens) > 0 {
				last := &result.Screens[len(result.Screens)-1]
				if last.ClickedLabel == "" {
					last.ClickedLabel = clipUIString(lbl, 48)
				}
			}
			_, _ = m.PerformUIAction(ctx, serial, UIActionRequest{Action: "tap", X: picked.CenterX, Y: picked.CenterY})
			result.StepsExecuted++
		}

		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}

	result.UniqueScreensVisited = len(seenScreens)
	if trafficCountFn != nil {
		delta := trafficCountFn() - startCount
		if delta > 0 {
			result.TrafficDelta = delta
		}
	}
	return result, nil
}

func hashUIScreen(d *UIDumpResult) string {
	h := sha1.New()
	_, _ = h.Write([]byte(d.ForegroundPkg))
	limit := len(d.Elements)
	if limit > 30 {
		limit = 30
	}
	for i := 0; i < limit; i++ {
		el := d.Elements[i]
		_, _ = fmt.Fprintf(h, "|%s:%s:%s:%d,%d", el.Class, el.ResourceID, el.Text, el.CenterX, el.CenterY)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func isUnsafeUIElement(el *UIElement) bool {
	joined := strings.ToLower(el.Text + " " + el.ContentDesc + " " + el.ResourceID)
	for _, bad := range dangerousText {
		if strings.Contains(joined, bad) {
			return true
		}
	}
	return false
}
