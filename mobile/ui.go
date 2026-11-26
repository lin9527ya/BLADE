package mobile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var reBounds = regexp.MustCompile(`^\[(-?\d+),(-?\d+)\]\[(-?\d+),(-?\d+)\]$`)

// ParseUIHierarchyXML parses Android `uiautomator dump` XML into compact indexed elements.
func ParseUIHierarchyXML(raw []byte, maxElements int) ([]UIElement, bool) {
	if maxElements <= 0 {
		maxElements = 120
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var out []UIElement
	truncated := false

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "node" {
			continue
		}
		attr := make(map[string]string, len(se.Attr))
		for _, a := range se.Attr {
			attr[a.Name.Local] = a.Value
		}
		text := strings.TrimSpace(attr["text"])
		desc := strings.TrimSpace(attr["content-desc"])
		resID := strings.TrimSpace(attr["resource-id"])
		clickable := attr["clickable"] == "true"
		editable := strings.Contains(strings.ToLower(attr["class"]), "edittext")
		password := attr["password"] == "true"
		enabled := attr["enabled"] != "false"
		bounds := attr["bounds"]

		// Drop invisible/unlabelled container nodes unless clickable or editable.
		if !clickable && !editable && text == "" && desc == "" && resID == "" {
			continue
		}
		cx, cy := parseBoundsCenter(bounds)
		if len(out) >= maxElements {
			truncated = true
			break
		}
		out = append(out, UIElement{
			Index:       len(out),
			Class:       shortClass(attr["class"]),
			Text:        clipUIString(text, 120),
			ContentDesc: clipUIString(desc, 120),
			ResourceID:  resID,
			Clickable:   clickable,
			Enabled:     enabled,
			Editable:    editable,
			Password:    password,
			Bounds:      bounds,
			CenterX:     cx,
			CenterY:     cy,
		})
	}
	return out, truncated
}

func parseBoundsCenter(bounds string) (int, int) {
	m := reBounds.FindStringSubmatch(strings.TrimSpace(bounds))
	if len(m) != 5 {
		return 0, 0
	}
	x1, _ := strconv.Atoi(m[1])
	y1, _ := strconv.Atoi(m[2])
	x2, _ := strconv.Atoi(m[3])
	y2, _ := strconv.Atoi(m[4])
	return (x1 + x2) / 2, (y1 + y2) / 2
}

func shortClass(cls string) string {
	if i := strings.LastIndex(cls, "."); i >= 0 && i+1 < len(cls) {
		return cls[i+1:]
	}
	return cls
}

func clipUIString(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// UIDump captures and parses the current Android UI accessibility hierarchy.
func (m *Manager) UIDump(ctx context.Context, serial string) (*UIDumpResult, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	remoteXML := "/data/local/tmp/blade_ui.xml"
	_, _, _, _ = m.runADBDevice(ctx, serial, "shell", "uiautomator", "dump", remoteXML)
	rawOut, stderr, code, err := m.runADBDevice(ctx, serial, "shell", "cat", remoteXML)
	if err != nil || code != 0 || !strings.Contains(rawOut, "<hierarchy") {
		return nil, fmt.Errorf("uiautomator dump failed: %s %s (%v)", strings.TrimSpace(rawOut), strings.TrimSpace(stderr), err)
	}
	elems, trunc := ParseUIHierarchyXML([]byte(rawOut), 120)
	topOut, _, _, _ := m.runADBDevice(ctx, serial, "shell", `dumpsys activity activities 2>/dev/null | grep -m1 -E 'mResumedActivity|topResumedActivity'`)
	return &UIDumpResult{
		Serial:        serial,
		ElementCount:  len(elems),
		Elements:      elems,
		RawTruncated:  trunc,
		ForegroundPkg: parseForegroundPkg(topOut),
	}, nil
}

func parseForegroundPkg(raw string) string {
	for _, field := range strings.Fields(raw) {
		if strings.Contains(field, "/") && !strings.HasPrefix(field, "/") {
			parts := strings.SplitN(field, "/", 2)
			pkg := strings.TrimPrefix(parts[0], "u0")
			if rePkgName.MatchString(pkg) {
				return pkg
			}
		}
	}
	return ""
}

// Screenshot captures a PNG screen snapshot from the connected Android device.
func (m *Manager) Screenshot(ctx context.Context, serial string) ([]byte, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	remotePNG := "/data/local/tmp/blade_screen.png"
	if out, stderr, code, err := m.runADBDevice(ctx, serial, "shell", "screencap", "-p", remotePNG); err != nil || code != 0 {
		return nil, fmt.Errorf("screencap failed: %s %s (%v)", out, stderr, err)
	}
	tmp, err := os.CreateTemp("", "blade-screen-*.png")
	if err != nil {
		return nil, err
	}
	localPath := tmp.Name()
	tmp.Close()
	defer os.Remove(localPath)

	if out, stderr, code, err := m.runADBDevice(ctx, serial, "pull", remotePNG, localPath); err != nil || code != 0 {
		return nil, fmt.Errorf("adb pull screenshot failed: %s %s (%v)", out, stderr, err)
	}
	return os.ReadFile(localPath)
}

// PerformUIAction runs tap/input/swipe/keyevent/launch/deeplink/force_stop on Android.
func (m *Manager) PerformUIAction(ctx context.Context, serial string, req UIActionRequest) (string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", err
	}
	var args []string
	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "tap":
		if req.X < 0 || req.Y < 0 {
			return "", fmt.Errorf("invalid tap coordinates (%d,%d)", req.X, req.Y)
		}
		args = []string{"shell", "input", "tap", strconv.Itoa(req.X), strconv.Itoa(req.Y)}
	case "input", "text":
		if req.Text == "" {
			return "", fmt.Errorf("text required for input action")
		}
		args = []string{"shell", "input", "text", escapeADBInputText(req.Text)}
	case "swipe":
		args = []string{"shell", "input", "swipe", strconv.Itoa(req.X), strconv.Itoa(req.Y), strconv.Itoa(req.X2), strconv.Itoa(req.Y2), "300"}
	case "back":
		args = []string{"shell", "input", "keyevent", "4"}
	case "home":
		args = []string{"shell", "input", "keyevent", "3"}
	case "keyevent":
		args = []string{"shell", "input", "keyevent", strconv.Itoa(req.KeyCode)}
	case "launch":
		if err := ValidatePackageName(req.Package); err != nil {
			return "", err
		}
		if strings.TrimSpace(req.Activity) != "" {
			comp := req.Package + "/" + req.Activity
			if strings.Contains(req.Activity, "/") {
				comp = req.Activity
			}
			if strings.ContainsAny(comp, " \t\n;|&$`\"'") {
				return "", fmt.Errorf("invalid activity component %q", req.Activity)
			}
			args = []string{"shell", "am", "start", "-n", comp}
		} else {
			args = []string{"shell", "monkey", "-p", req.Package, "-c", "android.intent.category.LAUNCHER", "1"}
		}
	case "deeplink":
		uri := strings.TrimSpace(req.URI)
		if uri == "" || strings.ContainsAny(uri, " \t\r\n;|&$`\"'") {
			return "", fmt.Errorf("invalid deeplink URI %q", req.URI)
		}
		args = []string{"shell", "am", "start", "-a", "android.intent.action.VIEW", "-d", uri}
		if strings.TrimSpace(req.Package) != "" {
			if err := ValidatePackageName(req.Package); err != nil {
				return "", err
			}
			args = append(args, req.Package)
		}
	case "force_stop":
		if err := ValidatePackageName(req.Package); err != nil {
			return "", err
		}
		args = []string{"shell", "am", "force-stop", req.Package}
	default:
		return "", fmt.Errorf("unsupported UI action %q", req.Action)
	}
	out, stderr, code, err := m.runADBDevice(ctx, serial, args...)
	msg := strings.TrimSpace(out + " " + stderr)
	if err != nil || code != 0 {
		return msg, fmt.Errorf("adb ui action %s failed: %s (%v)", req.Action, msg, err)
	}
	return msg, nil
}

func escapeADBInputText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ':
			b.WriteString("%s")
		case '&', '|', ';', '<', '>', '(', ')', '$', '`', '\\', '"', '\'':
			b.WriteRune('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ProbeIPC safely probes exported Android components or deep links on the bound device.
func (m *Manager) ProbeIPC(ctx context.Context, serial string, req IPCProbeRequest) (*IPCProbeResult, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	args, desc, err := BuildIPCProbeArgs(req)
	if err != nil {
		return nil, err
	}
	// Clear logcat buffer right before probe? No, just capture latest timestamped crash slice lightly if failed.
	out, stderr, code, runErr := m.runADBDevice(ctx, serial, args...)
	combined := strings.TrimSpace(out + "\n" + stderr)
	res := &IPCProbeResult{
		Kind:     req.Kind,
		Command:  "adb -s " + serial + " " + desc,
		Success:  runErr == nil && code == 0 && !strings.Contains(combined, "SecurityException") && !strings.Contains(combined, "Error:"),
		ExitCode: code,
		Output:   combined,
	}
	if !res.Success || strings.Contains(combined, "Exception") {
		crash, _, _, _ := m.runADBDevice(ctx, serial, "logcat", "-d", "-t", "30", "*:E")
		res.CrashLog = clipUIString(strings.TrimSpace(crash), 1500)
	}
	return res, nil
}

// BuildIPCProbeArgs builds validated adb shell arguments for IPC testing without shell injection.
func BuildIPCProbeArgs(req IPCProbeRequest) ([]string, string, error) {
	if strings.TrimSpace(req.Package) != "" {
		if err := ValidatePackageName(req.Package); err != nil {
			return nil, "", err
		}
	}
	safeToken := func(v string) bool {
		return v != "" && !strings.ContainsAny(v, " \t\r\n;|&$`\"'")
	}
	args := []string{"shell"}
	switch strings.ToLower(strings.TrimSpace(req.Kind)) {
	case "activity":
		if !safeToken(req.Component) {
			return nil, "", fmt.Errorf("valid component required for activity probe")
		}
		comp := req.Component
		if !strings.Contains(comp, "/") && req.Package != "" {
			comp = req.Package + "/" + comp
		}
		args = append(args, "am", "start", "-W", "-n", comp)
	case "service":
		if !safeToken(req.Component) {
			return nil, "", fmt.Errorf("valid component required for service probe")
		}
		comp := req.Component
		if !strings.Contains(comp, "/") && req.Package != "" {
			comp = req.Package + "/" + comp
		}
		args = append(args, "am", "startservice", "-n", comp)
	case "deeplink":
		if !safeToken(req.URI) {
			return nil, "", fmt.Errorf("valid URI required for deeplink probe")
		}
		args = append(args, "am", "start", "-W", "-a", "android.intent.action.VIEW", "-d", req.URI)
		if req.Package != "" {
			args = append(args, req.Package)
		}
	case "broadcast":
		args = append(args, "am", "broadcast")
		if safeToken(req.Action) {
			args = append(args, "-a", req.Action)
		}
		if safeToken(req.Component) {
			comp := req.Component
			if !strings.Contains(comp, "/") && req.Package != "" {
				comp = req.Package + "/" + comp
			}
			args = append(args, "-n", comp)
		}
		if req.Action == "" && req.Component == "" {
			return nil, "", fmt.Errorf("action or component required for broadcast probe")
		}
	case "provider":
		if !safeToken(req.URI) || !strings.HasPrefix(req.URI, "content://") {
			return nil, "", fmt.Errorf("content:// URI required for provider query")
		}
		args = append(args, "content", "query", "--uri", req.URI)
	default:
		return nil, "", fmt.Errorf("unsupported IPC probe kind %q", req.Kind)
	}
	for k, v := range req.Extras {
		if !safeToken(k) || !safeToken(v) {
			return nil, "", fmt.Errorf("unsafe extra parameter %q", k)
		}
		args = append(args, "--es", k, v)
	}
	return args, strings.Join(args, " "), nil
}
