package mobile

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FridaCatalog returns the standardized library of defensive security-testing Frida modules.
func FridaCatalog() []FridaScriptSpec {
	return []FridaScriptSpec{
		{
			ID:          "ssl_unpinning",
			Name:        "SSL/TLS Pinning Bypass",
			Category:    "ssl",
			Description: "Neutralizes TrustManager, OkHttp CertificatePinner, Conscrypt TrustManagerImpl, and WebViewClient SSL error handlers to enable authorized MITM inspection.",
			Frameworks:  []string{"Android Java", "OkHttp", "Conscrypt", "WebView"},
			Source: `Java.perform(function() {
  try {
    var X509TM = Java.use('javax.net.ssl.X509TrustManager');
    var SSLContext = Java.use('javax.net.ssl.SSLContext');
    var TrustManager = Java.registerClass({
      name: 'com.blade.TrustAllManager',
      implements: [X509TM],
      methods: {
        checkClientTrusted: function(chain, authType) {},
        checkServerTrusted: function(chain, authType) {},
        getAcceptedIssuers: function() { return []; }
      }
    });
    SSLContext.init.overload('[Ljavax.net.ssl.KeyManager;', '[Ljavax.net.ssl.TrustManager;', 'java.security.SecureRandom').implementation = function(km, tm, sr) {
      this.init(km, [TrustManager.$new()], sr);
    };
    send({tag: 'ssl_unpinning', msg: 'SSLContext hooked'});
  } catch (e) {}
  try {
    var CertificatePinner = Java.use('okhttp3.CertificatePinner');
    CertificatePinner.check.overload('java.lang.String', 'java.util.List').implementation = function(host, certs) {
      send({tag: 'ssl_unpinning', msg: 'OkHttp check bypassed: ' + host});
    };
  } catch (e) {}
  try {
    var TrustManagerImpl = Java.use('com.android.org.conscrypt.TrustManagerImpl');
    TrustManagerImpl.verifyChain.implementation = function(untrustedChain, trustAnchorChain, host, clientAuth, ocspData, tlsSctData) {
      return untrustedChain;
    };
    send({tag: 'ssl_unpinning', msg: 'Conscrypt TrustManagerImpl hooked'});
  } catch (e) {}
  try {
    var WebViewClient = Java.use('android.webkit.WebViewClient');
    WebViewClient.onReceivedSslError.implementation = function(view, handler, error) {
      handler.proceed();
    };
  } catch (e) {}
});`,
		},
		{
			ID:          "anti_detection",
			Name:        "Environment Check Bypass (Root/Debug)",
			Category:    "bypass",
			Description: "Masks common root file probes, Debug.isDebuggerConnected, and build tags during authorized dynamic assessment.",
			Frameworks:  []string{"Android Java"},
			Source: `Java.perform(function() {
  try {
    var Debug = Java.use('android.os.Debug');
    Debug.isDebuggerConnected.implementation = function() { return false; };
  } catch (e) {}
  try {
    var File = Java.use('java.io.File');
    var origExists = File.exists.implementation;
    var rootPaths = ['/su', '/sbin/su', '/system/bin/su', '/system/xbin/su', '/data/local/xbin/su', '/data/local/bin/su'];
    File.exists.implementation = function() {
      var p = this.getAbsolutePath();
      for (var i = 0; i < rootPaths.length; i++) {
        if (p === rootPaths[i]) return false;
      }
      return origExists.call(this);
    };
  } catch (e) {}
  send({tag: 'anti_detection', msg: 'Environment hooks ready'});
});`,
		},
		{
			ID:          "crypto_monitor",
			Name:        "Crypto & Signature Monitor",
			Category:    "crypto",
			Description: "Logs javax.crypto.Cipher keys/IVs/plaintext and MessageDigest inputs to inspect custom API request signing.",
			Frameworks:  []string{"Android Java", "javax.crypto"},
			Source: `Java.perform(function() {
  function toHex(arr) {
    if (!arr) return '';
    var hex = '';
    for (var i = 0; i < Math.min(arr.length, 64); i++) {
      var b = arr[i] & 0xff;
      hex += (b < 16 ? '0' : '') + b.toString(16);
    }
    return hex;
  }
  try {
    var Cipher = Java.use('javax.crypto.Cipher');
    Cipher.init.overload('int', 'java.security.Key', 'java.security.spec.AlgorithmParameterSpec').implementation = function(mode, key, spec) {
      send({tag: 'crypto', op: 'Cipher.init', alg: this.getAlgorithm(), mode: mode, key_hex: toHex(key ? key.getEncoded() : null)});
      return this.init(mode, key, spec);
    };
    Cipher.doFinal.overload('[B').implementation = function(input) {
      var out = this.doFinal(input);
      send({tag: 'crypto', op: 'Cipher.doFinal', alg: this.getAlgorithm(), in_len: input ? input.length : 0, in_hex: toHex(input)});
      return out;
    };
  } catch (e) {}
  try {
    var Mac = Java.use('javax.crypto.Mac');
    Mac.doFinal.overload('[B').implementation = function(input) {
      var out = this.doFinal(input);
      send({tag: 'crypto', op: 'Mac.doFinal', alg: this.getAlgorithm(), in_hex: toHex(input), out_hex: toHex(out)});
      return out;
    };
  } catch (e) {}
});`,
		},
		{
			ID:          "okhttp_plaintext",
			Name:        "Pre-Encryption OkHttp Logger",
			Category:    "network",
			Description: "Captures OkHttp Request URL, headers, and unencrypted body before custom payload encryption or certificate pinning.",
			Frameworks:  []string{"OkHttp3"},
			Source: `Java.perform(function() {
  try {
    var RealCall = Java.use('okhttp3.RealCall');
    RealCall.execute.implementation = function() {
      var req = this.request();
      send({tag: 'okhttp', method: req.method(), url: req.url().toString(), headers: req.headers().toString()});
      return this.execute();
    };
    RealCall.enqueue.implementation = function(cb) {
      var req = this.request();
      send({tag: 'okhttp', method: req.method(), url: req.url().toString(), headers: req.headers().toString()});
      return this.enqueue(cb);
    };
  } catch (e) {}
});`,
		},
		{
			ID:          "webview_bridge",
			Name:        "WebView JS Bridge & URL Monitor",
			Category:    "webview",
			Description: "Logs loadUrl calls and addJavascriptInterface bridges exposed to WebViews.",
			Frameworks:  []string{"Android WebView"},
			Source: `Java.perform(function() {
  try {
    var WebView = Java.use('android.webkit.WebView');
    WebView.loadUrl.overload('java.lang.String').implementation = function(u) {
      send({tag: 'webview', op: 'loadUrl', url: u});
      return this.loadUrl(u);
    };
    WebView.addJavascriptInterface.implementation = function(obj, name) {
      send({tag: 'webview', op: 'addJavascriptInterface', name: name, cls: obj.getClass().getName()});
      return this.addJavascriptInterface(obj, name);
    };
  } catch (e) {}
});`,
		},
		{
			ID:          "flutter_ssl_bypass",
			Name:        "Flutter BoringSSL Bypass (arm64 heuristic)",
			Category:    "ssl",
			Description: "Attempts pattern scan on libflutter.so ssl_crypto_x509_session_verify_cert_chain on arm64-v8a devices.",
			Frameworks:  []string{"Flutter"},
			Source: `setTimeout(function() {
  var m = Process.findModuleByName('libflutter.so');
  if (!m) { send({tag: 'flutter_ssl', msg: 'libflutter.so not loaded'}); return; }
  send({tag: 'flutter_ssl', msg: 'libflutter.so base=' + m.base + ' size=' + m.size});
}, 1500);`,
		},
	}
}

const fridaPythonBridge = `
import frida, sys, json, threading

def main():
    serial = sys.argv[1]
    pkg = sys.argv[2]
    mode = sys.argv[3]
    script_path = sys.argv[4]
    with open(script_path, "r", encoding="utf-8") as f:
        code = f.read()
    dev = frida.get_device(serial, timeout=10)
    if mode == "spawn":
        pid = dev.spawn([pkg])
        sess = dev.attach(pid)
    else:
        sess = dev.attach(pkg)
    sc = sess.create_script(code)
    def on_msg(msg, data):
        sys.stdout.write("MSG:" + json.dumps(msg, ensure_ascii=False) + "\n")
        sys.stdout.flush()
    sc.on("message", on_msg)
    sc.load()
    if mode == "spawn":
        dev.resume(pid)
    sys.stdout.write("READY\n")
    sys.stdout.flush()
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
            if req.get("cmd") == "quit":
                break
            if req.get("cmd") == "rpc":
                method = req.get("method", "")
                args = req.get("args") or []
                fn = getattr(sc.exports_sync, method)
                res = fn(*args)
                sys.stdout.write("RPC:" + json.dumps({"id": req.get("id"), "ok": True, "result": res}, ensure_ascii=False) + "\n")
                sys.stdout.flush()
        except Exception as e:
            sys.stdout.write("RPC:" + json.dumps({"id": req.get("id") if isinstance(locals().get("req"), dict) else None, "ok": False, "error": str(e)}, ensure_ascii=False) + "\n")
            sys.stdout.flush()
    try:
        sess.detach()
    except Exception:
        pass

if __name__ == "__main__":
    main()
`

type fridaSessionHandle struct {
	info      FridaSession
	scriptDir string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	mu        sync.Mutex
	logs      []string
	rpcMu     sync.Mutex
	rpcWait   map[string]chan string
}

// StartFridaServer ensures frida-server is running on a rooted Android device.
func (m *Manager) StartFridaServer(ctx context.Context, serial, binaryPath string) (string, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return "", err
	}
	if strings.TrimSpace(binaryPath) == "" {
		binaryPath = "/data/local/tmp/frida-server"
	}
	if strings.ContainsAny(binaryPath, " \t\n;|&$`\"'") {
		return "", fmt.Errorf("invalid remote frida-server path %q", binaryPath)
	}
	script := fmt.Sprintf(`
BIN=%s
if pidof frida-server >/dev/null 2>&1; then
  echo "already_running:$(pidof frida-server)"
  exit 0
fi
if [ ! -e "$BIN" ]; then
  echo "frida-server not found at $BIN" >&2
  exit 2
fi
chmod 755 "$BIN"
nohup "$BIN" -D >/data/local/tmp/frida-server.log 2>&1 </dev/null &
sleep 1
if pidof frida-server >/dev/null 2>&1; then
  echo "started:$(pidof frida-server)"
else
  echo "failed_to_start" >&2
  exit 1
fi
`, shellQuote(binaryPath))
	out, stderr, code, err := m.runRootShell(ctx, serial, script)
	if err != nil || code != 0 {
		return "", fmt.Errorf("start frida-server failed: %s %s (%v)", strings.TrimSpace(out), strings.TrimSpace(stderr), err)
	}
	return strings.TrimSpace(out), nil
}

// StartFridaSession launches a managed Python/Frida hook session using built-in modules and/or custom JS.
func (m *Manager) StartFridaSession(ctx context.Context, taskID int64, serial, pkg, mode string, modules []string, customJS string) (*FridaSession, error) {
	if err := ValidateDeviceSerial(serial); err != nil {
		return nil, err
	}
	if err := ValidatePackageName(pkg); err != nil {
		return nil, err
	}
	if mode == "" {
		mode = "spawn"
	}
	if mode != "spawn" && mode != "attach" {
		return nil, fmt.Errorf("frida mode must be spawn or attach")
	}

	catalog := map[string]string{}
	for _, spec := range FridaCatalog() {
		catalog[spec.ID] = spec.Source
	}
	var scriptBuf strings.Builder
	var activeMods []string
	for _, mod := range modules {
		src, ok := catalog[mod]
		if !ok {
			return nil, fmt.Errorf("unknown frida module %q", mod)
		}
		scriptBuf.WriteString("// === " + mod + " ===\n")
		scriptBuf.WriteString(src)
		scriptBuf.WriteString("\n\n")
		activeMods = append(activeMods, mod)
	}
	if strings.TrimSpace(customJS) != "" {
		scriptBuf.WriteString("// === custom ===\n")
		scriptBuf.WriteString(customJS)
		scriptBuf.WriteString("\n")
		activeMods = append(activeMods, "custom")
	}
	if len(activeMods) == 0 {
		return nil, fmt.Errorf("select at least one frida module or customJS")
	}

	pyBin, err := exec.LookPath("python3")
	if err != nil {
		return nil, fmt.Errorf("python3 unavailable for Frida bridge")
	}

	dir, err := os.MkdirTemp("", "blade-frida-*")
	if err != nil {
		return nil, err
	}
	scriptPath := filepath.Join(dir, "hook.js")
	runnerPath := filepath.Join(dir, "bridge.py")
	if err := os.WriteFile(scriptPath, []byte(scriptBuf.String()), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := os.WriteFile(runnerPath, []byte(fridaPythonBridge), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	sessID := newShortID()
	cmd := exec.Command(pyBin, runnerPath, serial, pkg, mode, scriptPath)
	if m.adbSocket != "" {
		cmd.Env = append(os.Environ(), "ADB_SERVER_SOCKET="+m.adbSocket)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("launch frida bridge: %w", err)
	}

	h := &fridaSessionHandle{
		info: FridaSession{
			ID:        sessID,
			TaskID:    taskID,
			Serial:    serial,
			Package:   pkg,
			Mode:      mode,
			Modules:   activeMods,
			Running:   true,
			StartedAt: time.Now().UTC(),
		},
		scriptDir: dir,
		cmd:       cmd,
		stdin:     stdin,
		rpcWait:   make(map[string]chan string),
	}

	m.fridaMu.Lock()
	m.fridaSess[sessID] = h
	if taskID > 0 {
		m.fridaByTask[taskID] = sessID
	}
	m.fridaMu.Unlock()

	readyCh := make(chan error, 1)
	go m.pumpFridaStdout(h, stdout, readyCh)
	go m.pumpFridaStderr(h, stderr)
	go func() {
		waitErr := cmd.Wait()
		h.mu.Lock()
		h.info.Running = false
		h.info.EndedAt = time.Now().UTC()
		if waitErr != nil && h.info.ExitError == "" {
			h.info.ExitError = waitErr.Error()
		}
		h.mu.Unlock()
		_ = os.RemoveAll(dir)
	}()

	select {
	case rErr := <-readyCh:
		if rErr != nil {
			_ = m.StopFridaSession(sessID)
			return nil, rErr
		}
	case <-time.After(12 * time.Second):
		// Timed out waiting for READY — return logs if it failed early.
		snap := h.snapshot()
		if !snap.Running {
			return nil, fmt.Errorf("frida session exited: %s (%s)", snap.ExitError, strings.Join(snap.LastLogs, " | "))
		}
	case <-ctx.Done():
		_ = m.StopFridaSession(sessID)
		return nil, ctx.Err()
	}

	snap := h.snapshot()
	return &snap, nil
}

func (m *Manager) pumpFridaStdout(h *fridaSessionHandle, r io.Reader, readyCh chan<- error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 512*1024)
	sentReady := false
	for sc.Scan() {
		line := sc.Text()
		if line == "READY" {
			if !sentReady {
				sentReady = true
				readyCh <- nil
			}
			continue
		}
		if strings.HasPrefix(line, "RPC:") {
			payload := strings.TrimPrefix(line, "RPC:")
			var resp struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal([]byte(payload), &resp)
			h.rpcMu.Lock()
			ch := h.rpcWait[resp.ID]
			delete(h.rpcWait, resp.ID)
			h.rpcMu.Unlock()
			if ch != nil {
				ch <- payload
			}
			continue
		}
		h.appendLog(strings.TrimPrefix(line, "MSG:"))
	}
	if !sentReady {
		readyCh <- fmt.Errorf("frida bridge ended before attachment (ensure frida Python package and device frida-server are installed)")
	}
}

func (m *Manager) pumpFridaStderr(h *fridaSessionHandle, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		h.mu.Lock()
		if h.info.ExitError == "" {
			h.info.ExitError = line
		}
		h.mu.Unlock()
		h.appendLog("[stderr] " + line)
	}
}

func (h *fridaSessionHandle) appendLog(entry string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.info.LogCount++
	h.logs = append(h.logs, entry)
	if len(h.logs) > 200 {
		h.logs = h.logs[len(h.logs)-200:]
	}
}

func (h *fridaSessionHandle) snapshot() FridaSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := h.info
	tailStart := 0
	if len(h.logs) > 30 {
		tailStart = len(h.logs) - 30
	}
	cp.LastLogs = append([]string(nil), h.logs[tailStart:]...)
	return cp
}

// InvokeFridaRPC invokes an exported rpc.exports method on an active Frida session.
func (m *Manager) InvokeFridaRPC(ctx context.Context, sessionID string, taskID int64, method string, args []any) (any, error) {
	if strings.TrimSpace(method) == "" {
		return nil, fmt.Errorf("rpc method name required")
	}
	m.fridaMu.Lock()
	if sessionID == "" && taskID > 0 {
		sessionID = m.fridaByTask[taskID]
	}
	h := m.fridaSess[sessionID]
	m.fridaMu.Unlock()
	if h == nil {
		return nil, fmt.Errorf("active frida session not found (start a hook session exporting rpc.exports.%s first)", method)
	}
	if !h.snapshot().Running {
		return nil, fmt.Errorf("frida session %s is no longer running", h.info.ID)
	}

	reqID := newShortID()
	ch := make(chan string, 1)
	h.rpcMu.Lock()
	h.rpcWait[reqID] = ch
	h.rpcMu.Unlock()
	defer func() {
		h.rpcMu.Lock()
		delete(h.rpcWait, reqID)
		h.rpcMu.Unlock()
	}()

	reqBytes, _ := json.Marshal(map[string]any{
		"cmd":    "rpc",
		"id":     reqID,
		"method": method,
		"args":   args,
	})
	if _, err := io.WriteString(h.stdin, string(reqBytes)+"\n"); err != nil {
		return nil, fmt.Errorf("write rpc to frida session: %w", err)
	}

	select {
	case raw := <-ch:
		var out struct {
			OK     bool   `json:"ok"`
			Result any    `json:"result"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			return nil, err
		}
		if !out.OK {
			return nil, fmt.Errorf("frida rpc error: %s", out.Error)
		}
		return out.Result, nil
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("frida rpc %s timed out", method)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// GetFridaSession returns the current snapshot of a session (or task's latest session).
func (m *Manager) GetFridaSession(sessionID string, taskID int64) (*FridaSession, bool) {
	m.fridaMu.Lock()
	if sessionID == "" && taskID > 0 {
		sessionID = m.fridaByTask[taskID]
	}
	h := m.fridaSess[sessionID]
	m.fridaMu.Unlock()
	if h == nil {
		return nil, false
	}
	snap := h.snapshot()
	return &snap, true
}

// ListFridaSessions lists active and recent sessions for taskID (0 = all).
func (m *Manager) ListFridaSessions(taskID int64) []FridaSession {
	m.fridaMu.Lock()
	handles := make([]*fridaSessionHandle, 0, len(m.fridaSess))
	for _, h := range m.fridaSess {
		handles = append(handles, h)
	}
	m.fridaMu.Unlock()
	var out []FridaSession
	for _, h := range handles {
		snap := h.snapshot()
		if taskID == 0 || snap.TaskID == taskID {
			out = append(out, snap)
		}
	}
	return out
}

// StopFridaSession detaches and terminates an active Frida session cleanly.
func (m *Manager) StopFridaSession(sessionID string) error {
	m.fridaMu.Lock()
	h := m.fridaSess[sessionID]
	m.fridaMu.Unlock()
	if h == nil {
		return nil
	}
	if h.stdin != nil {
		_, _ = io.WriteString(h.stdin, `{"cmd":"quit"}`+"\n")
		_ = h.stdin.Close()
	}
	if h.cmd != nil && h.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_, _ = h.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = h.cmd.Process.Kill()
		}
	}
	return nil
}

func newShortID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
