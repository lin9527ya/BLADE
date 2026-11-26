package mobile

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	reDeviceSerial = regexp.MustCompile(`^[a-zA-Z0-9._:-]{2,128}$`)
	rePkgName      = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z0-9_]+)+$`)
	reHostPort     = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)
)

// ADBRunner abstracts external command invocation so unit tests can mock adb/frida calls.
type ADBRunner func(ctx context.Context, env []string, name string, args ...string) (stdout, stderr string, exitCode int, err error)

// Manager manages attached Android/iOS mobile test devices, exclusive task bindings,
// ADB proxy/CA provisioning, rooted inspection, UI automation, and Frida hook sessions.
type Manager struct {
	mu           sync.Mutex
	adbBin       string
	adbSocket    string
	runner       ADBRunner
	deviceLocks  map[string]int64  // serial -> taskID (exclusive reservation)
	origProxy    map[string]string // serial -> previous global http_proxy
	activeProxy  map[string]string // serial -> mode (usb_reverse | lan)
	reversePorts map[string]int    // serial -> remote tcp port reversed via adb reverse
	fridaMu      sync.Mutex
	fridaSess    map[string]*fridaSessionHandle
	fridaByTask  map[int64]string // taskID -> latest active session ID
}

// NewManager creates a mobile device manager. Empty adbSocket inherits ADB_SERVER_SOCKET.
func NewManager(adbSocket string) *Manager {
	if strings.TrimSpace(adbSocket) == "" {
		adbSocket = strings.TrimSpace(os.Getenv("ADB_SERVER_SOCKET"))
	}
	return &Manager{
		adbBin:       "adb",
		adbSocket:    adbSocket,
		runner:       defaultCommandRunner,
		deviceLocks:  make(map[string]int64),
		origProxy:    make(map[string]string),
		activeProxy:  make(map[string]string),
		reversePorts: make(map[string]int),
		fridaSess:    make(map[string]*fridaSessionHandle),
		fridaByTask:  make(map[int64]string),
	}
}

func defaultCommandRunner(ctx context.Context, env []string, name string, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return outBuf.String(), errBuf.String(), exitCode, err
}

func (m *Manager) adbEnv() []string {
	if m.adbSocket == "" {
		return nil
	}
	return []string{"ADB_SERVER_SOCKET=" + m.adbSocket}
}

func (m *Manager) runADB(ctx context.Context, args ...string) (string, string, int, error) {
	if m == nil {
		return "", "", -1, fmt.Errorf("mobile manager unavailable")
	}
	bin := m.adbBin
	if path, err := exec.LookPath(bin); err == nil {
		bin = path
	}
	return m.runner(ctx, m.adbEnv(), bin, args...)
}

func (m *Manager) runADBDevice(ctx context.Context, serial string, args ...string) (string, string, int, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", "", -1, err
	}
	full := append([]string{"-s", serial}, args...)
	return m.runADB(ctx, full...)
}

// ValidateDeviceSerial rejects malformed or shell-unsafe ADB serial identifiers.
func ValidateDeviceSerial(serial string) error {
	s := strings.TrimSpace(serial)
	if !reDeviceSerial.MatchString(s) {
		return fmt.Errorf("invalid device serial %q", serial)
	}
	return nil
}

// ValidatePackageName validates an Android/iOS bundle identifier.
func ValidatePackageName(pkg string) error {
	p := strings.TrimSpace(pkg)
	if !rePkgName.MatchString(p) || len(p) > 255 {
		return fmt.Errorf("invalid package name %q", pkg)
	}
	return nil
}

// shellQuote single-quotes an argument safely for POSIX sh on Android.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// AndroidCertHash computes the legacy OpenSSL X509_NAME_hash (<hex>.0) used by Android's
// /system/etc/security/cacerts and /apex/com.android.conscrypt/cacerts stores.
func AndroidCertHash(pemBytes []byte) (string, *x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", nil, fmt.Errorf("failed to decode PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", nil, fmt.Errorf("parse X509 certificate: %w", err)
	}
	sum := md5.Sum(cert.RawSubject)
	hash := binary.LittleEndian.Uint32(sum[:4])
	return fmt.Sprintf("%08x", hash), cert, nil
}

// ListDevices discovers currently connected ADB devices and enriches online devices with
// device properties, root capability, system CA install state, Frida status, and task lock.
func (m *Manager) ListDevices(ctx context.Context, pemCACert []byte) ([]DeviceInfo, error) {
	out, stderr, code, err := m.runADB(ctx, "devices", "-l")
	if err != nil && code == -1 {
		return nil, fmt.Errorf("adb unavailable (%v): %s", err, strings.TrimSpace(stderr))
	}
	lines := strings.Split(out, "\n")
	var devices []DeviceInfo
	var certHash string
	if len(pemCACert) > 0 {
		certHash, _, _ = AndroidCertHash(pemCACert)
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		serial := fields[0]
		rawState := fields[1]
		conn := ConnUSB
		endpoint := ""
		if strings.Contains(serial, ":") && netHostPortLooksLikeIP(serial) {
			conn = ConnWiFi
			endpoint = serial
		}
		state := mapADBState(rawState)
		dev := DeviceInfo{
			ID:             "adb:" + serial,
			Serial:         serial,
			Platform:       PlatformAndroid,
			ConnectionType: conn,
			Endpoint:       endpoint,
			State:          state,
			LastSeenAt:     time.Now().UTC(),
		}
		for _, tok := range fields[2:] {
			if strings.HasPrefix(tok, "model:") {
				dev.Model = strings.TrimPrefix(tok, "model:")
			}
		}
		m.mu.Lock()
		dev.LockedByTaskID = m.deviceLocks[serial]
		dev.ProxyMode = m.activeProxy[serial]
		m.mu.Unlock()
		if dev.ProxyMode == "" {
			dev.ProxyMode = "none"
		}
		if state == "online" {
			m.enrichOnlineDevice(ctx, &dev, certHash)
		}
		devices = append(devices, dev)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Serial < devices[j].Serial })
	return devices, nil
}

func netHostPortLooksLikeIP(s string) bool {
	return strings.Count(s, ":") == 1 && !strings.HasPrefix(s, "emulator-")
}

func mapADBState(raw string) string {
	switch raw {
	case "device":
		return "online"
	case "unauthorized":
		return "unauthorized"
	case "offline":
		return "offline"
	default:
		return raw
	}
}

func (m *Manager) enrichOnlineDevice(ctx context.Context, dev *DeviceInfo, certHash string) {
	subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Single compound shell command reduces ADB roundtrips significantly.
	caCheck := "echo CA=none"
	if certHash != "" {
		caCheck = fmt.Sprintf(
			`if [ -f /apex/com.android.conscrypt/cacerts/%s.0 ]; then echo CA=system_apex; `+
				`elif [ -f /system/etc/security/cacerts/%s.0 ]; then echo CA=system_legacy; `+
				`elif ls /data/misc/user/0/cacerts-added/%s.0 >/dev/null 2>&1; then echo CA=user; `+
				`else echo CA=none; fi`,
			certHash, certHash, certHash,
		)
	}
	script := `echo MODEL=$(getprop ro.product.model);` +
		`echo MANUF=$(getprop ro.product.manufacturer);` +
		`echo REL=$(getprop ro.build.version.release);` +
		`echo SDK=$(getprop ro.build.version.sdk);` +
		`echo ABI=$(getprop ro.product.cpu.abi);` +
		`echo PROXY=$(settings get global http_proxy 2>/dev/null);` +
		`if [ "$(id -u)" = "0" ]; then echo ROOT=adbd;` +
		`elif su -c id 2>/dev/null | grep -q "uid=0"; then echo ROOT=su;` +
		`elif su 0 id 2>/dev/null | grep -q "uid=0"; then echo ROOT=su0;` +
		`else echo ROOT=none; fi;` +
		caCheck + ";" +
		`FPID=$(pidof frida-server 2>/dev/null | awk '{print $1}');` +
		`if [ -n "$FPID" ]; then echo FRIDA=running:$FPID;` +
		`elif [ -x /data/local/tmp/frida-server ] || command -v frida-server >/dev/null 2>&1; then echo FRIDA=stopped;` +
		`else echo FRIDA=missing; fi;` +
		`TOP=$(dumpsys activity activities 2>/dev/null | grep -m1 -E 'mResumedActivity|topResumedActivity' | sed -n 's/.* \([a-zA-Z0-9._]*\)\/.*/\1/p');` +
		`echo TOP=$TOP`

	out, _, _, err := m.runADBDevice(subCtx, dev.Serial, "shell", script)
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "MODEL="):
			if v := strings.TrimPrefix(line, "MODEL="); v != "" {
				dev.Model = v
			}
		case strings.HasPrefix(line, "MANUF="):
			dev.Manufacturer = strings.TrimPrefix(line, "MANUF=")
		case strings.HasPrefix(line, "REL="):
			dev.OSVersion = strings.TrimPrefix(line, "REL=")
		case strings.HasPrefix(line, "SDK="):
			dev.APILevel, _ = strconv.Atoi(strings.TrimPrefix(line, "SDK="))
		case strings.HasPrefix(line, "ABI="):
			dev.ABI = strings.TrimPrefix(line, "ABI=")
		case strings.HasPrefix(line, "PROXY="):
			v := strings.TrimPrefix(line, "PROXY=")
			if v != "" && v != "null" && v != ":0" {
				dev.ProxyAddr = v
			}
		case strings.HasPrefix(line, "ROOT="):
			v := strings.TrimPrefix(line, "ROOT=")
			dev.Rooted = (v != "" && v != "none")
			dev.RootMethod = v
		case strings.HasPrefix(line, "CA="):
			dev.SystemCAStatus = strings.TrimPrefix(line, "CA=")
		case strings.HasPrefix(line, "FRIDA="):
			v := strings.TrimPrefix(line, "FRIDA=")
			if strings.HasPrefix(v, "running:") {
				dev.FridaServer = "running"
				dev.FridaPID, _ = strconv.Atoi(strings.TrimPrefix(v, "running:"))
			} else {
				dev.FridaServer = v
			}
		case strings.HasPrefix(line, "TOP="):
			dev.ForegroundPkg = strings.TrimPrefix(line, "TOP=")
		}
	}
}

// ConnectWiFi connects to an Android device over wireless ADB (ip:port).
func (m *Manager) ConnectWiFi(ctx context.Context, endpoint string) (string, error) {
	ep := strings.TrimSpace(endpoint)
	if !reHostPort.MatchString(ep) || !strings.Contains(ep, ":") {
		return "", fmt.Errorf("invalid wireless ADB endpoint %q (expected host:port)", endpoint)
	}
	out, stderr, code, err := m.runADB(ctx, "connect", ep)
	msg := strings.TrimSpace(out + " " + stderr)
	if err != nil || code != 0 || strings.Contains(strings.ToLower(msg), "failed") || strings.Contains(strings.ToLower(msg), "unable") {
		return msg, fmt.Errorf("adb connect %s failed: %s", ep, msg)
	}
	return msg, nil
}

// PairWiFi pairs an Android 11+ device over wireless debugging using a 6-digit pairing code.
func (m *Manager) PairWiFi(ctx context.Context, endpoint, codeStr string) (string, error) {
	ep := strings.TrimSpace(endpoint)
	codeStr = strings.TrimSpace(codeStr)
	if !reHostPort.MatchString(ep) || !strings.Contains(ep, ":") {
		return "", fmt.Errorf("invalid pairing endpoint %q (expected host:port)", endpoint)
	}
	if codeStr == "" || len(codeStr) > 16 {
		return "", fmt.Errorf("invalid pairing code")
	}
	out, stderr, exitCode, err := m.runADB(ctx, "pair", ep, codeStr)
	msg := strings.TrimSpace(out + " " + stderr)
	if err != nil || exitCode != 0 || strings.Contains(strings.ToLower(msg), "failed") {
		return msg, fmt.Errorf("adb pair %s failed: %s", ep, msg)
	}
	return msg, nil
}

// BindTask claims exclusive use of serial for taskID.
func (m *Manager) BindTask(taskID int64, serial string) error {
	if err := ValidateDeviceSerial(serial); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, busy := m.deviceLocks[serial]; busy && cur != 0 && cur != taskID {
		return fmt.Errorf("device %s is currently locked by task #%d", serial, cur)
	}
	// Release any previous lock held by the same task.
	for s, tid := range m.deviceLocks {
		if tid == taskID && s != serial {
			delete(m.deviceLocks, s)
		}
	}
	m.deviceLocks[serial] = taskID
	return nil
}

// UnbindTask releases any device lock owned by taskID.
func (m *Manager) UnbindTask(taskID int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s, tid := range m.deviceLocks {
		if tid == taskID {
			delete(m.deviceLocks, s)
			return s
		}
	}
	return ""
}

// LockedTask returns the taskID holding serial (0 if free).
func (m *Manager) LockedTask(serial string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deviceLocks[serial]
}

// SetupProxy configures ADB reverse (for USB) or LAN proxy on Android and snapshots the prior proxy.
func (m *Manager) SetupProxy(ctx context.Context, serial, mode string, localProxyPort int, lanProxyAddr string) (string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", err
	}
	// Snapshot previous setting once so teardown restores accurately.
	curOut, _, _, _ := m.runADBDevice(ctx, serial, "shell", "settings get global http_proxy")
	cur := strings.TrimSpace(curOut)
	m.mu.Lock()
	if _, exists := m.origProxy[serial]; !exists {
		m.origProxy[serial] = cur
	}
	m.mu.Unlock()

	var targetProxy string
	switch mode {
	case "", "usb_reverse":
		mode = "usb_reverse"
		if localProxyPort <= 0 || localProxyPort > 65535 {
			return "", fmt.Errorf("invalid local proxy port %d", localProxyPort)
		}
		devicePort := localProxyPort
		revSpec := fmt.Sprintf("tcp:%d", devicePort)
		hostSpec := fmt.Sprintf("tcp:%d", localProxyPort)
		if out, stderr, code, err := m.runADBDevice(ctx, serial, "reverse", revSpec, hostSpec); err != nil || code != 0 {
			return "", fmt.Errorf("adb reverse failed: %s %s (%v)", out, stderr, err)
		}
		targetProxy = fmt.Sprintf("127.0.0.1:%d", devicePort)
		m.mu.Lock()
		m.reversePorts[serial] = devicePort
		m.mu.Unlock()
	case "lan":
		if strings.TrimSpace(lanProxyAddr) == "" {
			return "", fmt.Errorf("lan proxy address required for lan mode")
		}
		targetProxy = strings.TrimSpace(lanProxyAddr)
	default:
		return "", fmt.Errorf("unsupported proxy mode %q (use usb_reverse or lan)", mode)
	}

	if out, stderr, code, err := m.runADBDevice(ctx, serial, "shell", "settings", "put", "global", "http_proxy", targetProxy); err != nil || code != 0 {
		return "", fmt.Errorf("failed to set http_proxy=%s: %s %s", targetProxy, out, stderr)
	}
	m.mu.Lock()
	m.activeProxy[serial] = mode
	m.mu.Unlock()
	return targetProxy, nil
}

// ClearProxy removes adb reverse mappings and restores the prior global http_proxy value.
func (m *Manager) ClearProxy(ctx context.Context, serial string) error {
	if err := ValidateDeviceSerial(serial); err != nil {
		return err
	}
	m.mu.Lock()
	revPort := m.reversePorts[serial]
	prev, hadPrev := m.origProxy[serial]
	delete(m.reversePorts, serial)
	delete(m.origProxy, serial)
	delete(m.activeProxy, serial)
	m.mu.Unlock()

	if revPort > 0 {
		_, _, _, _ = m.runADBDevice(ctx, serial, "reverse", "--remove", fmt.Sprintf("tcp:%d", revPort))
	}
	if hadPrev && prev != "" && prev != "null" && prev != ":0" {
		_, _, _, err := m.runADBDevice(ctx, serial, "shell", "settings", "put", "global", "http_proxy", prev)
		return err
	}
	_, _, _, _ = m.runADBDevice(ctx, serial, "shell", "settings", "put", "global", "http_proxy", ":0")
	_, _, _, err := m.runADBDevice(ctx, serial, "shell", "settings", "delete", "global", "http_proxy")
	return err
}

// InstallSystemCA deploys the MITM CA certificate into a rooted Android device's system store.
// Supports both legacy /system/etc/security/cacerts and Android 14+ APEX Conscrypt cacerts via tmpfs overlay.
func (m *Manager) InstallSystemCA(ctx context.Context, serial, pemPath string) (string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(pemPath)
	if err != nil {
		return "", fmt.Errorf("read CA cert %s: %w", pemPath, err)
	}
	certHash, _, err := AndroidCertHash(raw)
	if err != nil {
		return "", err
	}

	tmpCertFile, err := os.CreateTemp("", "blade-ca-*.0")
	if err != nil {
		return "", err
	}
	tmpCertPath := tmpCertFile.Name()
	defer os.Remove(tmpCertPath)
	if _, err := tmpCertFile.Write(raw); err != nil {
		tmpCertFile.Close()
		return "", err
	}
	tmpCertFile.Close()

	remoteCert := fmt.Sprintf("/data/local/tmp/%s.0", certHash)
	if out, stderr, code, err := m.runADBDevice(ctx, serial, "push", tmpCertPath, remoteCert); err != nil || code != 0 {
		return "", fmt.Errorf("adb push CA failed: %s %s (%v)", out, stderr, err)
	}

	// Build Android CA install script handling Android <=13 and Android 14+ Conscrypt APEX.
	script := fmt.Sprintf(`
set -e
CERT=%s
SRC=%s
install_dir() {
  TARGET="$1"
  STAGE="/data/local/tmp/blade_ca_stage"
  rm -rf "$STAGE"
  mkdir -p "$STAGE"
  if mount | grep -q " $TARGET "; then
    cp -a "$TARGET/"* "$STAGE/" 2>/dev/null || true
    umount "$TARGET" 2>/dev/null || true
  fi
  mount -t tmpfs tmpfs "$TARGET"
  if [ -d "$STAGE" ] && [ "$(ls -A "$STAGE" 2>/dev/null)" != "" ]; then
    cp -a "$STAGE/"* "$TARGET/" 2>/dev/null || true
  fi
  rm -rf "$STAGE"
}
if [ -d /apex/com.android.conscrypt/cacerts ]; then
  mkdir -p /data/local/tmp/blade_apex_backup
  cp -a /apex/com.android.conscrypt/cacerts/* /data/local/tmp/blade_apex_backup/ 2>/dev/null || true
  mount -t tmpfs tmpfs /apex/com.android.conscrypt/cacerts
  cp -a /data/local/tmp/blade_apex_backup/* /apex/com.android.conscrypt/cacerts/ 2>/dev/null || true
  cp "$SRC" "/apex/com.android.conscrypt/cacerts/$CERT"
  chown root:root "/apex/com.android.conscrypt/cacerts/$CERT" || chown 0:0 "/apex/com.android.conscrypt/cacerts/$CERT"
  chmod 644 /apex/com.android.conscrypt/cacerts/*
  chcon u:object_r:system_security_cacerts_file:s0 /apex/com.android.conscrypt/cacerts/* 2>/dev/null || true
  for pid in $(pidof zygote zygote64 2>/dev/null); do
    nsenter --mount=/proc/$pid/ns/mnt -- /bin/sh -c "mount --bind /apex/com.android.conscrypt/cacerts /apex/com.android.conscrypt/cacerts" 2>/dev/null || true
  done
  rm -rf /data/local/tmp/blade_apex_backup
  echo "installed:system_apex:$CERT"
elif [ -d /system/etc/security/cacerts ]; then
  mkdir -p /data/local/tmp/blade_sys_backup
  cp -a /system/etc/security/cacerts/* /data/local/tmp/blade_sys_backup/ 2>/dev/null || true
  mount -t tmpfs tmpfs /system/etc/security/cacerts
  cp -a /data/local/tmp/blade_sys_backup/* /system/etc/security/cacerts/ 2>/dev/null || true
  cp "$SRC" "/system/etc/security/cacerts/$CERT"
  chown 0:0 "/system/etc/security/cacerts/$CERT" 2>/dev/null || true
  chmod 644 /system/etc/security/cacerts/*
  chcon u:object_r:system_file:s0 /system/etc/security/cacerts/* 2>/dev/null || true
  rm -rf /data/local/tmp/blade_sys_backup
  echo "installed:system_legacy:$CERT"
else
  echo "error:no_cacerts_dir" >&2
  exit 1
fi
`, shellQuote(certHash+".0"), shellQuote(remoteCert))

	out, stderr, code, err := m.runRootShell(ctx, serial, script)
	if err != nil || code != 0 {
		return "", fmt.Errorf("system CA install failed (root required): %s %s (%v)", strings.TrimSpace(out), strings.TrimSpace(stderr), err)
	}
	return strings.TrimSpace(out), nil
}

func (m *Manager) runRootShell(ctx context.Context, serial, script string) (string, string, int, error) {
	// Write temporary script to device and execute via root (adbd uid=0, su -c, or su 0).
	tmpScript, err := os.CreateTemp("", "blade-root-*.sh")
	if err != nil {
		return "", "", -1, err
	}
	localPath := tmpScript.Name()
	defer os.Remove(localPath)
	if _, err := tmpScript.WriteString(script); err != nil {
		tmpScript.Close()
		return "", "", -1, err
	}
	tmpScript.Close()

	remotePath := fmt.Sprintf("/data/local/tmp/blade_run_%d.sh", time.Now().UnixNano())
	if out, stderr, code, err := m.runADBDevice(ctx, serial, "push", localPath, remotePath); err != nil || code != 0 {
		return out, stderr, code, err
	}
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _, _, _ = m.runADBDevice(cleanCtx, serial, "shell", "rm", "-f", remotePath)
	}()

	cmd := fmt.Sprintf(
		`chmod 700 %s; `+
			`if [ "$(id -u)" = "0" ]; then sh %s; `+
			`elif su -c id 2>/dev/null | grep -q "uid=0"; then su -c "sh %s"; `+
			`elif su 0 id 2>/dev/null | grep -q "uid=0"; then su 0 sh %s; `+
			`else echo "ROOT_UNAVAILABLE" >&2; exit 126; fi`,
		remotePath, remotePath, remotePath, remotePath,
	)
	return m.runADBDevice(ctx, serial, "shell", cmd)
}

// InstallAPK installs an APK/APKS/XAPK sample onto a connected Android device.
func (m *Manager) InstallAPK(ctx context.Context, serial, apkPath string) (string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", err
	}
	if _, err := os.Stat(apkPath); err != nil {
		return "", err
	}
	out, stderr, code, err := m.runADBDevice(ctx, serial, "install", "-r", "-d", apkPath)
	msg := strings.TrimSpace(out + " " + stderr)
	if err != nil || code != 0 || strings.Contains(msg, "Failure") {
		return msg, fmt.Errorf("adb install failed: %s", msg)
	}
	return msg, nil
}

// UninstallApp removes package from the device.
func (m *Manager) UninstallApp(ctx context.Context, serial, pkg string) (string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", err
	}
	if err := ValidatePackageName(pkg); err != nil {
		return "", err
	}
	out, stderr, code, err := m.runADBDevice(ctx, serial, "uninstall", pkg)
	msg := strings.TrimSpace(out + " " + stderr)
	if err != nil || code != 0 {
		return msg, fmt.Errorf("adb uninstall failed: %s", msg)
	}
	return msg, nil
}

// ClearAppData wipes app data via pm clear.
func (m *Manager) ClearAppData(ctx context.Context, serial, pkg string) (string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", err
	}
	if err := ValidatePackageName(pkg); err != nil {
		return "", err
	}
	out, stderr, code, err := m.runADBDevice(ctx, serial, "shell", "pm", "clear", pkg)
	msg := strings.TrimSpace(out + " " + stderr)
	if err != nil || code != 0 {
		return msg, fmt.Errorf("pm clear failed: %s", msg)
	}
	return msg, nil
}

// ReadSandboxFiles lists files or previews a small file inside /data/data/<pkg>/<subpath> on a rooted device.
func (m *Manager) ReadSandboxFiles(ctx context.Context, serial, pkg, subpath string) ([]SandboxFileEntry, string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, "", err
	}
	if err := ValidatePackageName(pkg); err != nil {
		return nil, "", err
	}
	cleanSub := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+subpath)), "/")
	if strings.Contains(cleanSub, "..") {
		return nil, "", fmt.Errorf("invalid sandbox subpath %q", subpath)
	}
	target := "/data/data/" + pkg
	if cleanSub != "" && cleanSub != "." {
		target += "/" + cleanSub
	}
	script := fmt.Sprintf(`
P=%s
if [ ! -e "$P" ]; then echo "ERR:not_found"; exit 2; fi
if [ -d "$P" ]; then
  echo "MODE:DIR"
  ls -la "$P" | head -n 100
else
  echo "MODE:FILE"
  head -c 32768 "$P" | base64 2>/dev/null || cat "$P" | head -c 32768
fi
`, shellQuote(target))
	out, stderr, code, err := m.runRootShell(ctx, serial, script)
	if err != nil || code != 0 {
		return nil, "", fmt.Errorf("read sandbox %s failed: %s %s", target, out, stderr)
	}
	if strings.HasPrefix(out, "MODE:FILE\n") {
		return nil, strings.TrimPrefix(out, "MODE:FILE\n"), nil
	}
	var entries []SandboxFileEntry
	for _, line := range strings.Split(strings.TrimPrefix(out, "MODE:DIR\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || strings.HasPrefix(line, "total") {
			continue
		}
		name := fields[len(fields)-1]
		if name == "." || name == ".." {
			continue
		}
		isDir := strings.HasPrefix(fields[0], "d")
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		rel := name
		if cleanSub != "" && cleanSub != "." {
			rel = cleanSub + "/" + name
		}
		entries = append(entries, SandboxFileEntry{
			Path:  rel,
			Name:  name,
			IsDir: isDir,
			Size:  size,
		})
	}
	return entries, "", nil
}
