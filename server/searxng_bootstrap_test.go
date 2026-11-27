package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func overridePipBootstrapTestVars(t *testing.T, localGlobs []string, simpleBase, zipappURL string) {
	t.Helper()
	origGlobs := localPipWheelGlobs
	origSimple := pypiSimpleBaseURL
	origZip := defaultPipZipappURL
	localPipWheelGlobs = localGlobs
	pypiSimpleBaseURL = simpleBase
	defaultPipZipappURL = zipappURL
	t.Cleanup(func() {
		localPipWheelGlobs = origGlobs
		pypiSimpleBaseURL = origSimple
		defaultPipZipappURL = origZip
	})
}

func TestPythonVersionSatisfies(t *testing.T) {
	cases := []struct {
		pyVer string
		req   string
		want  bool
	}{
		{"3.12.3", "", true},
		{"3.12.3", ">=3.9", true},
		{"3.12.3", ">=3.13", false},
		{"3.10.12", ">=3.9,<3.14", true},
		{"3.10.12", ">3.10.12", false},
		{"3.12.3", "!=3.12.3", false},
		{"3.12.3", ">=3.8,!=3.11.*", true},
	}
	for _, tc := range cases {
		if got := pythonVersionSatisfies(tc.pyVer, tc.req); got != tc.want {
			t.Errorf("pythonVersionSatisfies(%q, %q) = %v, want %v", tc.pyVer, tc.req, got, tc.want)
		}
	}
}

func TestPickPipWheelFromSimpleHTML(t *testing.T) {
	htmlBody := `<!DOCTYPE html><html><body>
		<a href="../../packages/pip-9.0.3-py2.py3-none-any.whl">pip-9.0.3-py2.py3-none-any.whl</a>
		<a href="../../packages/pip-9.0.3-py3-none-any.whl#sha256=1111111111111111111111111111111111111111111111111111111111111111">pip-9.0.3-py3-none-any.whl</a>
		<a href="../../packages/pip-26.1-py3-none-any.whl#sha256=2222222222222222222222222222222222222222222222222222222222222222">pip-26.1-py3-none-any.whl</a>
		<a href="../../packages/pip-26.1.1-py3-none-any.whl#sha256=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" data-requires-python="&gt;=3.9">pip-26.1.1-py3-none-any.whl</a>
		<a href="../../packages/pip-27.0b1-py3-none-any.whl">pip-27.0b1-py3-none-any.whl</a>
		<a href="../../packages/pip-28.0-py3-none-any.whl" data-requires-python="&gt;=3.13">pip-28.0-py3-none-any.whl</a>
		<a href="../../packages/pip-99.0.0-py3-none-any.whl" data-yanked="broken">pip-99.0.0-py3-none-any.whl</a>
	</body></html>`

	cand, err := pickPipWheelFromSimpleHTML("https://mirror.example.test/simple/pip/", htmlBody, "3.12.3")
	if err != nil {
		t.Fatalf("pickPipWheelFromSimpleHTML error = %v", err)
	}
	if cand.Filename != "pip-26.1.1-py3-none-any.whl" {
		t.Fatalf("filename = %q, want pip-26.1.1-py3-none-any.whl", cand.Filename)
	}
	wantURL := "https://mirror.example.test/packages/pip-26.1.1-py3-none-any.whl"
	if cand.URL != wantURL {
		t.Fatalf("URL = %q, want %q", cand.URL, wantURL)
	}
	if len(cand.SHA256) != 64 || !strings.EqualFold(cand.SHA256, strings.Repeat("A", 64)) {
		t.Fatalf("SHA256 = %q, want 64 As", cand.SHA256)
	}
}

func writeMockEnsurepipFailurePython(t *testing.T, dir string) string {
	t.Helper()
	pyPath := filepath.Join(dir, "mock-python3")
	script := `#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-m" && "${2:-}" == "venv" ]]; then
  shift 2
  without_pip=0
  if [[ "${1:-}" == "--without-pip" ]]; then
    without_pip=1
    shift
  fi
  venv_dir="${1:?missing venv_dir}"
  mkdir -p "${venv_dir}/bin"
  cat > "${venv_dir}/bin/python" <<'INNER'
#!/usr/bin/env bash
set -euo pipefail
venv_bin_dir="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" == "-m" && "${2:-}" == "pip" && "${3:-}" == "install" ]]; then
  whl="${!#}"
  if [[ "${PYTHONPATH:-}" != "${whl}" ]]; then
    echo "expected PYTHONPATH=${whl}, got ${PYTHONPATH:-}" >&2
    exit 3
  fi
  if [[ ! -f "${whl}" ]]; then
    echo "missing wheel file ${whl}" >&2
    exit 4
  fi
  cat > "${venv_bin_dir}/pip" <<'PIPBIN'
#!/usr/bin/env bash
echo "pip from ${PYTHONPATH:-venv} $@"
PIPBIN
  chmod +x "${venv_bin_dir}/pip"
  exit 0
fi
if [[ "${1:-}" == *.pyz && "${2:-}" == "install" ]]; then
  cat > "${venv_bin_dir}/pip" <<PIPBIN
#!/usr/bin/env bash
echo "pip-via-pyz args: $*"
PIPBIN
  chmod +x "${venv_bin_dir}/pip"
  echo "bootstrapped via pyz: $*"
  exit 0
fi
echo "unexpected venv python call: $*" >&2
exit 5
INNER
  chmod +x "${venv_dir}/bin/python"
  if [[ "${without_pip}" -eq 0 ]]; then
    echo "The virtual environment was not created successfully because ensurepip is not" >&2
    echo "available. On Debian/Ubuntu systems, you need to install the python3.12-venv package." >&2
    exit 1
  fi
  exit 0
fi
echo "unexpected python call: $*" >&2
exit 2
`
	if err := os.WriteFile(pyPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock python: %v", err)
	}
	return pyPath
}

func TestSearxngEnsureVenvAndPip_WithoutPipPyPIWheelFallback(t *testing.T) {
	wheelPayload := []byte("mock-wheel-zip-payload-pip-26.1.1")
	sum := sha256.Sum256(wheelPayload)
	wantHex := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/simple/pip/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<a href="/packages/pip-26.1.1-py3-none-any.whl#sha256=` + wantHex + `">pip-26.1.1-py3-none-any.whl</a>`))
		case "/packages/pip-26.1.1-py3-none-any.whl":
			_, _ = w.Write(wheelPayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	overridePipBootstrapTestVars(t, nil, "", "")

	tmp := t.TempDir()
	mockPy := writeMockEnsurepipFailurePython(t, tmp)
	sx := (&Manager{dir: filepath.Join(tmp, "data")}).searxng()
	_ = os.MkdirAll(sx.rootDir(), 0o755)
	// 预先放一个坏掉的残缺 venv 目录（无 pip），模拟上次 ensurepip 失败后重试。
	_ = os.MkdirAll(filepath.Join(sx.venvDir(), "bin"), 0o755)
	_ = os.WriteFile(sx.venvPythonPath(), []byte("stale"), 0o755)

	cfg := SearxngManagedConfig{PyPIIndex: srv.URL + "/simple"}
	pipArgs := func(base ...string) []string { return append(append([]string(nil), base...), "-i", cfg.PyPIIndex) }
	var logBuf bytes.Buffer

	err := sx.ensureVenvAndPip(context.Background(), &logBuf, cfg, mockPy, "3.12.3", "", nil, false, pipArgs)
	if err != nil {
		t.Fatalf("ensureVenvAndPip failed: %v\nlog:\n%s", err, logBuf.String())
	}
	if !fileExists(sx.venvPipPath()) {
		t.Fatalf("expected %s to exist after fallback bootstrap", sx.venvPipPath())
	}
	if fileExists(filepath.Join(sx.rootDir(), ".bootstrap")) {
		t.Fatalf("expected temporary .bootstrap dir to be cleaned up")
	}
	if !strings.Contains(logBuf.String(), "--without-pip") {
		t.Fatalf("expected log to record --without-pip fallback, got:\n%s", logBuf.String())
	}

	// 验证 SHA-256 不匹配会拒绝写入。
	client, _ := newSearxngInstallHTTPClient("", 5*time.Second)
	defer client.CloseIdleConnections()
	badDest := filepath.Join(tmp, "bad.whl")
	if dlErr := downloadVerifiedFile(context.Background(), client, srv.URL+"/packages/pip-26.1.1-py3-none-any.whl", badDest, strings.Repeat("0", 64), 1<<20); dlErr == nil || !strings.Contains(dlErr.Error(), "sha256") {
		t.Fatalf("expected sha256 mismatch error, got: %v", dlErr)
	}
	if fileExists(badDest) {
		t.Fatalf("bad downloaded file should not persist at %s", badDest)
	}
}

func TestSearxngEnsureVenvAndPip_PipPyzFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bootstrap/pip.pyz":
			_, _ = w.Write([]byte("mock-zipapp"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	overridePipBootstrapTestVars(t, nil, srv.URL+"/simple/", srv.URL+"/bootstrap/pip.pyz")

	tmp := t.TempDir()
	mockPy := writeMockEnsurepipFailurePython(t, tmp)
	sx := (&Manager{dir: filepath.Join(tmp, "data")}).searxng()
	_ = os.MkdirAll(sx.rootDir(), 0o755)

	cfg := SearxngManagedConfig{PyPIIndex: srv.URL + "/simple"}
	pipArgs := func(base ...string) []string { return append(append([]string(nil), base...), "-i", cfg.PyPIIndex) }
	var logBuf bytes.Buffer

	if err := sx.ensureVenvAndPip(context.Background(), &logBuf, cfg, mockPy, "3.12.3", "", nil, true, pipArgs); err != nil {
		t.Fatalf("ensureVenvAndPip pip.pyz fallback failed: %v\nlog:\n%s", err, logBuf.String())
	}
	if !fileExists(sx.venvPipPath()) {
		t.Fatalf("expected %s to exist after pip.pyz fallback", sx.venvPipPath())
	}
	if !strings.Contains(logBuf.String(), "pip.pyz") {
		t.Fatalf("expected pip.pyz note in log, got:\n%s", logBuf.String())
	}
}

func TestSearxngEnsureVenvAndPip_LocalWheelOfflineFallback(t *testing.T) {
	tmp := t.TempDir()
	wheelDir := filepath.Join(tmp, "wheels")
	_ = os.MkdirAll(wheelDir, 0o755)
	_ = os.WriteFile(filepath.Join(wheelDir, "pip-24.0-py3-none-any.whl"), []byte("old"), 0o644)
	_ = os.WriteFile(filepath.Join(wheelDir, "pip-26.1.1-py3-none-any.whl"), []byte("new"), 0o644)

	overridePipBootstrapTestVars(t, []string{filepath.Join(wheelDir, "pip-*.whl")}, "http://127.0.0.1:1/simple/", "")

	mockPy := writeMockEnsurepipFailurePython(t, tmp)
	sx := (&Manager{dir: filepath.Join(tmp, "data")}).searxng()
	_ = os.MkdirAll(sx.rootDir(), 0o755)

	cfg := SearxngManagedConfig{PyPIIndex: "http://127.0.0.1:1/simple"}
	pipArgs := func(base ...string) []string { return base }
	var logBuf bytes.Buffer

	if err := sx.ensureVenvAndPip(context.Background(), &logBuf, cfg, mockPy, "3.12.3", "", nil, false, pipArgs); err != nil {
		t.Fatalf("ensureVenvAndPip local wheel failed: %v\nlog:\n%s", err, logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "pip-26.1.1-py3-none-any.whl") {
		t.Fatalf("expected newest local wheel pip-26.1.1 to be used, log:\n%s", logBuf.String())
	}
}

func TestSearxngRealHostVenvWithoutPipBootstrap(t *testing.T) {
	pyBin, pyVer, err := resolveHostPython("")
	if err != nil {
		t.Skipf("host python >= 3.10 unavailable: %v", err)
	}
	wheels := findLocalPipWheels()
	if len(wheels) == 0 {
		t.Skip("no local /usr/share/python-wheels/pip-*.whl present for offline real bootstrap test")
	}

	tmp := t.TempDir()
	sx := (&Manager{dir: filepath.Join(tmp, "data")}).searxng()
	_ = os.MkdirAll(sx.rootDir(), 0o755)

	cacheRoot := filepath.Join(sx.rootDir(), ".cache")
	pipCacheDir := filepath.Join(cacheRoot, "pip")
	_ = os.MkdirAll(pipCacheDir, 0o755)
	cmdEnv := buildSearxngChildEnv(nil, map[string]string{
		"HOME":                          sx.rootDir(),
		"XDG_CACHE_HOME":                cacheRoot,
		"PIP_CACHE_DIR":                 pipCacheDir,
		"PIP_PREFER_BINARY":             "1",
		"PIP_DISABLE_PIP_VERSION_CHECK": "1",
		"PIP_NO_INPUT":                  "1",
	}, "")

	var logBuf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 直接使用 --without-pip 创建真实宿主 venv，并走无特权 pip 引导路径验证。
	if err := sx.runLoggedCmd(ctx, &logBuf, sx.rootDir(), cmdEnv, pyBin, "-m", "venv", "--without-pip", sx.venvDir()); err != nil {
		t.Fatalf("real python -m venv --without-pip failed: %v\nlog:\n%s", err, logBuf.String())
	}
	if fileExists(sx.venvPipPath()) {
		t.Fatalf("expected --without-pip venv to not have %s initially", sx.venvPipPath())
	}
	if err := sx.ensureVenvPip(ctx, &logBuf, SearxngManagedConfig{}, pyVer, "", cmdEnv, func(a ...string) []string { return a }); err != nil {
		t.Fatalf("real ensureVenvPip failed: %v\nlog:\n%s", err, logBuf.String())
	}
	out, runErr := exec.CommandContext(ctx, sx.venvPipPath(), "--version").CombinedOutput()
	if runErr != nil {
		t.Fatalf("venv pip --version failed: %v, out=%s", runErr, string(out))
	}
	if !strings.Contains(string(out), sx.venvDir()) {
		t.Fatalf("expected venv pip --version to reference %s, got %q", sx.venvDir(), string(out))
	}
}
