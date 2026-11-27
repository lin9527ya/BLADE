package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lin9527ya/blade/traffic"
)

const (
	settingSearxngManagedEnabled      = "searxng_managed_enabled"
	settingSearxngManagedPort         = "searxng_managed_port"
	settingSearxngManagedProxyMode    = "searxng_managed_proxy_mode"
	settingSearxngManagedCustomProxy  = "searxng_managed_custom_proxy"
	settingSearxngManagedPreset       = "searxng_managed_engines_preset"
	settingSearxngManagedRepoURL      = "searxng_managed_repo_url"
	settingSearxngManagedPyPIIndex    = "searxng_managed_pypi_index"
	settingSearxngManagedInstallProxy = "searxng_managed_install_proxy"
	settingSearxngManagedAutoSwitch   = "searxng_managed_auto_switch"

	defaultSearxngPort       = 8888
	defaultSearxngRepoURL    = "https://github.com/searxng/searxng.git"
	defaultSearxngProxyMode  = "inherit"
	defaultSearxngPreset     = "security"
	searxngInstallTimeout    = 15 * time.Minute
	searxngStartWaitTimeout  = 25 * time.Second
	searxngLogMaxTailBytes   = 12 * 1024
	searxngRotateLogMaxBytes = 5 * 1024 * 1024
)

// SearxngManagedConfig holds persisted configuration for BLADE's bundled SearXNG service.
type SearxngManagedConfig struct {
	Enabled       bool   `json:"enabled"`
	Port          int    `json:"port"`
	ProxyMode     string `json:"proxy_mode"` // "inherit" | "custom" | "direct"
	CustomProxy   string `json:"custom_proxy"`
	EnginesPreset string `json:"engines_preset"` // "security" | "default" | "cn_friendly"
	RepoURL       string `json:"repo_url"`
	PyPIIndex     string `json:"pypi_index"`
	InstallProxy  string `json:"install_proxy"`
	AutoSwitch    bool   `json:"auto_switch"`
}

// SearxngStatus represents the runtime, install, and configuration state of the bundled SearXNG sidecar.
type SearxngStatus struct {
	Mode           string               `json:"mode"`  // "host_venv" | "compose"
	State          string               `json:"state"` // "not_installed" | "installing" | "starting" | "running" | "stopped" | "error"
	Installed      bool                 `json:"installed"`
	Running        bool                 `json:"running"`
	Healthy        bool                 `json:"healthy"`
	PID            int                  `json:"pid,omitempty"`
	URL            string               `json:"url"`
	PythonBin      string               `json:"python_bin,omitempty"`
	PythonVersion  string               `json:"python_version,omitempty"`
	EffectiveProxy string               `json:"effective_proxy,omitempty"`
	InstallStep    string               `json:"install_step,omitempty"`
	LastError      string               `json:"last_error,omitempty"`
	InstallLogTail string               `json:"install_log_tail,omitempty"`
	LogTail        string               `json:"log_tail,omitempty"`
	DataDir        string               `json:"data_dir,omitempty"`
	ActiveInSearch bool                 `json:"active_in_search"`
	Config         SearxngManagedConfig `json:"config"`
}

// searxngService manages the host Python venv sidecar (or Compose companion config) for SearXNG.
type searxngService struct {
	m *Manager

	mu          sync.Mutex
	busy        bool
	opState     string // "", "installing", "starting", "error"
	installStep string
	lastError   string
	opCancel    context.CancelFunc
	memCfg      *SearxngManagedConfig // 仅无 PG 的单测环境兜底内存态
}

func (m *Manager) searxng() *searxngService {
	if m == nil {
		return nil
	}
	m.searxngOnce.Do(func() {
		m.searxngSvc = &searxngService{m: m}
	})
	return m.searxngSvc
}

func (sx *searxngService) rootDir() string {
	if sx == nil || sx.m == nil || strings.TrimSpace(sx.m.dir) == "" {
		return ""
	}
	return filepath.Join(sx.m.dir, "searxng")
}

func (sx *searxngService) srcDir() string         { return filepath.Join(sx.rootDir(), "src") }
func (sx *searxngService) venvDir() string        { return filepath.Join(sx.rootDir(), "venv") }
func (sx *searxngService) settingsPath() string   { return filepath.Join(sx.rootDir(), "settings.yml") }
func (sx *searxngService) secretKeyPath() string  { return filepath.Join(sx.rootDir(), "secret.key") }
func (sx *searxngService) pidPath() string        { return filepath.Join(sx.rootDir(), "searxng.pid") }
func (sx *searxngService) runtimeLogPath() string { return filepath.Join(sx.rootDir(), "searxng.log") }
func (sx *searxngService) installLogPath() string { return filepath.Join(sx.rootDir(), "install.log") }
func (sx *searxngService) venvPythonPath() string {
	return filepath.Join(sx.venvDir(), "bin", "python")
}
func (sx *searxngService) venvPipPath() string { return filepath.Join(sx.venvDir(), "bin", "pip") }
func (sx *searxngService) venvGranianPath() string {
	return filepath.Join(sx.venvDir(), "bin", "granian")
}

func isSearxngComposeMode() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("BLADE_SEARXNG_MANAGED_MODE")), "compose") {
		return true
	}
	return strings.TrimSpace(os.Getenv("BLADE_SEARXNG_COMPANION_URL")) != ""
}

func searxngCompanionURL() string {
	if u := strings.TrimSpace(os.Getenv("BLADE_SEARXNG_COMPANION_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "http://searxng:8080"
}

func defaultSearxngManagedConfig() SearxngManagedConfig {
	return SearxngManagedConfig{
		Enabled:       false,
		Port:          defaultSearxngPort,
		ProxyMode:     defaultSearxngProxyMode,
		EnginesPreset: defaultSearxngPreset,
		RepoURL:       defaultSearxngRepoURL,
		AutoSwitch:    true,
	}
}

func normalizeSearxngManagedConfig(in SearxngManagedConfig) (SearxngManagedConfig, error) {
	cfg := in
	if cfg.Port == 0 {
		cfg.Port = defaultSearxngPort
	}
	if cfg.Port < 1024 || cfg.Port > 65535 {
		return cfg, fmt.Errorf("SearXNG 监听端口需在 1024–65535 之间（当前 %d）", cfg.Port)
	}
	cfg.ProxyMode = strings.ToLower(strings.TrimSpace(cfg.ProxyMode))
	if cfg.ProxyMode == "" {
		cfg.ProxyMode = defaultSearxngProxyMode
	}
	switch cfg.ProxyMode {
	case "inherit", "custom", "direct":
	default:
		return cfg, fmt.Errorf("SearXNG 代理模式必须是 inherit / custom / direct")
	}
	cfg.CustomProxy = strings.TrimSpace(cfg.CustomProxy)
	if cfg.CustomProxy != "" {
		if err := validateSearxngProxyURL(cfg.CustomProxy, "自定义代理"); err != nil {
			return cfg, err
		}
	}
	if cfg.ProxyMode == "custom" && cfg.CustomProxy == "" {
		return cfg, fmt.Errorf("选择自定义代理模式时请填写 SearXNG 出海代理地址")
	}
	cfg.EnginesPreset = strings.ToLower(strings.TrimSpace(cfg.EnginesPreset))
	if cfg.EnginesPreset == "" {
		cfg.EnginesPreset = defaultSearxngPreset
	}
	switch cfg.EnginesPreset {
	case "security", "default", "cn_friendly":
	default:
		return cfg, fmt.Errorf("引擎预设必须是 security / default / cn_friendly")
	}
	cfg.RepoURL = strings.TrimSpace(cfg.RepoURL)
	if cfg.RepoURL == "" {
		cfg.RepoURL = defaultSearxngRepoURL
	}
	if err := validateHTTPMirrorURL(cfg.RepoURL, "Git 仓库地址"); err != nil {
		return cfg, err
	}
	cfg.PyPIIndex = strings.TrimSpace(cfg.PyPIIndex)
	if cfg.PyPIIndex != "" {
		if err := validateHTTPMirrorURL(cfg.PyPIIndex, "PyPI 镜像源"); err != nil {
			return cfg, err
		}
	}
	cfg.InstallProxy = strings.TrimSpace(cfg.InstallProxy)
	if cfg.InstallProxy != "" {
		if err := validateSearxngProxyURL(cfg.InstallProxy, "安装代理"); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func validateSearxngProxyURL(raw, label string) error {
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("%s地址不能以 '-' 开头", label)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s地址格式无效：%q", label, raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks4", "socks5", "socks5h":
		return nil
	default:
		return fmt.Errorf("%s仅支持 http / https / socks4 / socks5 / socks5h 协议", label)
	}
}

func validateHTTPMirrorURL(raw, label string) error {
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("%s不能以 '-' 开头", label)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s格式无效：%q", label, raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("%s仅支持 http:// 或 https:// 地址", label)
	}
}

func (sx *searxngService) loadConfig() SearxngManagedConfig {
	cfg := defaultSearxngManagedConfig()
	if sx == nil || sx.m == nil {
		return cfg
	}
	if sx.m.pg == nil {
		sx.mu.Lock()
		defer sx.mu.Unlock()
		if sx.memCfg != nil {
			return *sx.memCfg
		}
		return cfg
	}
	pg := sx.m.pg
	cfg.Enabled = pg.GetBool(settingSearxngManagedEnabled, false)
	if v, ok, _ := pg.GetSetting(settingSearxngManagedPort); ok && strings.TrimSpace(v) != "" {
		if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && p >= 1024 && p <= 65535 {
			cfg.Port = p
		}
	}
	if v, ok, _ := pg.GetSetting(settingSearxngManagedProxyMode); ok && strings.TrimSpace(v) != "" {
		cfg.ProxyMode = strings.TrimSpace(v)
	}
	if v, ok, _ := pg.GetSetting(settingSearxngManagedCustomProxy); ok {
		cfg.CustomProxy = strings.TrimSpace(v)
	}
	if v, ok, _ := pg.GetSetting(settingSearxngManagedPreset); ok && strings.TrimSpace(v) != "" {
		cfg.EnginesPreset = strings.TrimSpace(v)
	}
	if v, ok, _ := pg.GetSetting(settingSearxngManagedRepoURL); ok && strings.TrimSpace(v) != "" {
		cfg.RepoURL = strings.TrimSpace(v)
	}
	if v, ok, _ := pg.GetSetting(settingSearxngManagedPyPIIndex); ok {
		cfg.PyPIIndex = strings.TrimSpace(v)
	}
	if v, ok, _ := pg.GetSetting(settingSearxngManagedInstallProxy); ok {
		cfg.InstallProxy = strings.TrimSpace(v)
	}
	cfg.AutoSwitch = pg.GetBool(settingSearxngManagedAutoSwitch, true)
	norm, err := normalizeSearxngManagedConfig(cfg)
	if err != nil {
		return defaultSearxngManagedConfig()
	}
	return norm
}

func (sx *searxngService) saveConfig(cfg SearxngManagedConfig) error {
	norm, err := normalizeSearxngManagedConfig(cfg)
	if err != nil {
		return err
	}
	if sx == nil || sx.m == nil {
		return nil
	}
	sx.mu.Lock()
	cp := norm
	sx.memCfg = &cp
	sx.mu.Unlock()
	if sx.m.pg == nil {
		return nil
	}
	return sx.m.pg.SetSettings(map[string]string{
		settingSearxngManagedEnabled:      strconv.FormatBool(norm.Enabled),
		settingSearxngManagedPort:         strconv.Itoa(norm.Port),
		settingSearxngManagedProxyMode:    norm.ProxyMode,
		settingSearxngManagedCustomProxy:  norm.CustomProxy,
		settingSearxngManagedPreset:       norm.EnginesPreset,
		settingSearxngManagedRepoURL:      norm.RepoURL,
		settingSearxngManagedPyPIIndex:    norm.PyPIIndex,
		settingSearxngManagedInstallProxy: norm.InstallProxy,
		settingSearxngManagedAutoSwitch:   strconv.FormatBool(norm.AutoSwitch),
	})
}

func (sx *searxngService) resolveEffectiveProxy(cfg SearxngManagedConfig) string {
	switch cfg.ProxyMode {
	case "direct":
		return ""
	case "custom":
		return strings.TrimSpace(cfg.CustomProxy)
	default: // "inherit"
		if sx == nil || sx.m == nil {
			return ""
		}
		_, _, _, _, wsProxy := sx.m.WebSearch()
		if p := strings.TrimSpace(wsProxy); p != "" {
			return p
		}
		egress := strings.TrimSpace(sx.m.usableEgressProxy())
		if egress == "" || egress == blockedProxyURL {
			return ""
		}
		return egress
	}
}

func (sx *searxngService) resolveInstallProxy(cfg SearxngManagedConfig) string {
	if p := strings.TrimSpace(cfg.InstallProxy); p != "" {
		return p
	}
	if cfg.ProxyMode == "direct" {
		return ""
	}
	return sx.resolveEffectiveProxy(cfg)
}

func (sx *searxngService) isInstalled() bool {
	if sx == nil || sx.rootDir() == "" {
		return false
	}
	for _, p := range []string{
		sx.venvPythonPath(),
		sx.venvGranianPath(),
		filepath.Join(sx.srcDir(), "searx", "webapp.py"),
	} {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			return false
		}
	}
	return true
}

func yamlQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func searxngEnginesForPreset(preset string) []string {
	switch preset {
	case "default":
		return []string{"duckduckgo", "bing", "brave", "google"}
	case "cn_friendly":
		return []string{"bing", "baidu", "duckduckgo", "github", "wikipedia"}
	default: // "security"
		return []string{"duckduckgo", "bing", "brave", "google", "startpage", "qwant", "wikipedia", "github", "stackoverflow"}
	}
}

func renderSearxngSettingsYAML(secretKey, bindAddr string, port int, effectiveProxy, preset string) string {
	var b strings.Builder
	b.WriteString("# managed-by: blade\n")
	b.WriteString("use_default_settings: true\n\n")
	b.WriteString("general:\n")
	b.WriteString("  debug: false\n")
	b.WriteString("  instance_name: \"BLADE SearXNG\"\n\n")
	b.WriteString("server:\n")
	fmt.Fprintf(&b, "  secret_key: %s\n", yamlQuote(secretKey))
	fmt.Fprintf(&b, "  bind_address: %s\n", yamlQuote(bindAddr))
	fmt.Fprintf(&b, "  port: %d\n", port)
	b.WriteString("  limiter: false\n")
	b.WriteString("  public_instance: false\n")
	b.WriteString("  image_proxy: false\n")
	b.WriteString("  method: \"GET\"\n\n")
	b.WriteString("search:\n")
	b.WriteString("  safe_search: 0\n")
	b.WriteString("  formats:\n")
	b.WriteString("    - html\n")
	b.WriteString("    - json\n\n")
	b.WriteString("outgoing:\n")
	b.WriteString("  request_timeout: 6.0\n")
	b.WriteString("  max_request_timeout: 15.0\n")
	b.WriteString("  pool_connections: 100\n")
	b.WriteString("  enable_http2: true\n")
	if p := strings.TrimSpace(effectiveProxy); p != "" {
		b.WriteString("  proxies:\n")
		b.WriteString("    all://:\n")
		fmt.Fprintf(&b, "      - %s\n", yamlQuote(p))
	}
	b.WriteString("\nengines:\n")
	for _, name := range searxngEnginesForPreset(preset) {
		fmt.Fprintf(&b, "  - name: %s\n", name)
		b.WriteString("    disabled: false\n")
	}
	return b.String()
}

func (sx *searxngService) ensureSecretKey() (string, error) {
	if err := os.MkdirAll(sx.rootDir(), 0o755); err != nil {
		return "", err
	}
	keyPath := sx.secretKeyPath()
	if raw, err := os.ReadFile(keyPath); err == nil {
		k := strings.TrimSpace(string(raw))
		if len(k) >= 32 {
			return k, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	key := hex.EncodeToString(buf)
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return "", err
	}
	return key, nil
}

func (sx *searxngService) writeManagedSettingsFile(cfg SearxngManagedConfig, compose bool) (string, error) {
	if sx == nil || sx.rootDir() == "" {
		return "", fmt.Errorf("未配置 data 目录，无法写入 SearXNG 配置")
	}
	secretKey, err := sx.ensureSecretKey()
	if err != nil {
		return "", fmt.Errorf("生成 SearXNG secret_key 失败：%w", err)
	}
	bindAddr := "127.0.0.1"
	port := cfg.Port
	perm := os.FileMode(0o600)
	if compose {
		// Compose 容器内 SearXNG 监听容器网卡 8080，宿主由 docker-compose 仅发布到 127.0.0.1；
		// 容器通常降权到 UID 977 读取挂载配置，因此文件使用 0644 保证可读。
		bindAddr = "0.0.0.0"
		port = 8080
		perm = 0o644
	}
	proxy := sx.resolveEffectiveProxy(cfg)
	content := renderSearxngSettingsYAML(secretKey, bindAddr, port, proxy, cfg.EnginesPreset)
	settingsPath := sx.settingsPath()
	tmpPath := settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), perm); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, settingsPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return settingsPath, nil
}

func readSearxngPIDFile(pidPath string) int {
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return 0
	}
	return pid
}

func probeSearxngEndpoint(ctx context.Context, baseURL string) (reachable, healthy bool, detail string) {
	probeURL := strings.TrimRight(baseURL, "/") + "/search?q=blade_probe&format=json"
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return false, false, err.Error()
	}
	req.Header.Set("Accept", "application/json")
	cli := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := cli.Do(req)
	if err != nil {
		return false, false, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if resp.StatusCode != http.StatusOK {
		return true, false, fmt.Sprintf("HTTP %d（请确认 settings.yml 的 search.formats 包含 json）", resp.StatusCode)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return true, false, fmt.Sprintf("响应非合法 JSON：%v", err)
	}
	if _, ok := doc["results"]; !ok {
		return true, false, "JSON 响应缺少 results 字段"
	}
	return true, true, ""
}

func tailTextFile(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	size := fi.Size()
	if size == 0 {
		return ""
	}
	offset := int64(0)
	if size > maxBytes {
		offset = size - maxBytes
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(raw))
	if offset > 0 {
		if nl := strings.IndexByte(text, '\n'); nl >= 0 && nl+1 < len(text) {
			text = text[nl+1:]
		}
	}
	return text
}

func rotateLogIfLarge(path string, maxBytes int64) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < maxBytes {
		return
	}
	_ = os.Rename(path, path+".old")
}

func resolveHostPython(configured string) (binPath, version string, err error) {
	cands := make([]string, 0, 3)
	if strings.TrimSpace(configured) != "" {
		cands = append(cands, strings.TrimSpace(configured))
	}
	for _, name := range []string{"python3", "python"} {
		if p, lookErr := exec.LookPath(name); lookErr == nil && p != "" {
			cands = append(cands, p)
		}
	}
	seen := map[string]bool{}
	var lastErr string
	for _, cand := range cands {
		if seen[cand] {
			continue
		}
		seen[cand] = true
		p := cand
		if !filepath.IsAbs(p) {
			resolved, lookErr := exec.LookPath(p)
			if lookErr != nil {
				lastErr = fmt.Sprintf("未找到 %q：%v", p, lookErr)
				continue
			}
			p = resolved
		}
		out, runErr := exec.Command(p, "-c", "import sys; print(f'{sys.version_info[0]}.{sys.version_info[1]}.{sys.version_info[2]}'); sys.exit(0 if sys.version_info >= (3,10) else 2)").CombinedOutput()
		ver := strings.TrimSpace(string(out))
		if runErr != nil {
			lastErr = fmt.Sprintf("%s 版本检查失败（需要 Python >= 3.10）：%s (%v)", p, ver, runErr)
			continue
		}
		if venvOut, venvErr := exec.Command(p, "-m", "venv", "--help").CombinedOutput(); venvErr != nil {
			if _, virtErr := exec.Command(p, "-m", "virtualenv", "--version").CombinedOutput(); virtErr != nil {
				lastErr = fmt.Sprintf("%s 缺少 venv/virtualenv 模块（Debian/Ubuntu/Kali 可安装 python3-venv 或 virtualenv）：%s (%v)", p, strings.TrimSpace(string(venvOut)), venvErr)
				continue
			}
		}
		return p, ver, nil
	}
	if lastErr == "" {
		lastErr = "宿主未检测到可用的 Python 3.10+ 解释器，请在 PATH 中安装 python3 与 python3-venv，或在设置页填写「Python 解释器路径」"
	}
	return "", "", errors.New(lastErr)
}

var (
	pypiSimpleBaseURL   = "https://pypi.org/simple/"
	defaultPipZipappURL = "https://bootstrap.pypa.io/pip/pip.pyz"
	localPipWheelGlobs  = []string{
		"/usr/share/python-wheels/pip-*.whl",
		"/usr/lib/python*/ensurepip/_bundled/pip-*.whl",
		"/usr/local/lib/python*/ensurepip/_bundled/pip-*.whl",
	}

	pipSimpleAnchorRe = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	pipHrefAttrRe     = regexp.MustCompile(`(?is)(?:^|\s)href\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	pipReqPyAttrRe    = regexp.MustCompile(`(?is)(?:^|\s)data-requires-python\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	pipYankedAttrRe   = regexp.MustCompile(`(?is)(?:^|\s)data-yanked(?:\s*=|\s|$)`)
	pipWheelFileRe    = regexp.MustCompile(`^pip-(\d+(?:\.\d+)*)-py3-none-any\.whl$`)
)

type pipWheelCandidate struct {
	URL      string
	Filename string
	SHA256   string
	Version  []int
}

func logSearxngNote(lf io.Writer, format string, args ...any) {
	if lf == nil {
		return
	}
	_, _ = fmt.Fprintf(lf, "\n[blade-searxng] "+format+"\n", args...)
}

func withEnvOverrides(env []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return env
	}
	out := append([]string(nil), env...)
	for k, v := range extra {
		prefix := k + "="
		replaced := false
		for i, entry := range out {
			if strings.HasPrefix(entry, prefix) {
				out[i] = prefix + v
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, prefix+v)
		}
	}
	return out
}

func findHostExecutable(name string) string {
	if p, err := exec.LookPath(name); err == nil && p != "" {
		return p
	}
	cands := []string{
		filepath.Join("/usr/local/bin", name),
		filepath.Join("/usr/bin", name),
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		cands = append(cands,
			filepath.Join(home, ".local", "bin", name),
			filepath.Join(home, ".cargo", "bin", name),
		)
	}
	for _, p := range cands {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func parseNumericVersion(raw string) ([]int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	parts := strings.Split(raw, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		nums = append(nums, n)
	}
	return nums, true
}

func compareIntSlices(a, b []int) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func pythonVersionSatisfies(pyVer, reqExpr string) bool {
	reqExpr = strings.TrimSpace(reqExpr)
	if reqExpr == "" {
		return true
	}
	pyNums, ok := parseNumericVersion(pyVer)
	if !ok {
		return true
	}
	for _, clause := range strings.Split(reqExpr, ",") {
		c := strings.TrimSpace(clause)
		if c == "" {
			continue
		}
		var op, rhs string
		for _, candOp := range []string{">=", "<=", "==", "!=", ">", "<"} {
			if strings.HasPrefix(c, candOp) {
				op = candOp
				rhs = strings.TrimSpace(strings.TrimPrefix(c, candOp))
				break
			}
		}
		if op == "" || strings.Contains(rhs, "*") {
			continue
		}
		reqNums, ok := parseNumericVersion(rhs)
		if !ok {
			continue
		}
		cmp := compareIntSlices(pyNums, reqNums)
		switch op {
		case ">=":
			if cmp < 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		case "==":
			if cmp != 0 {
				return false
			}
		case "!=":
			if cmp == 0 {
				return false
			}
		}
	}
	return true
}

func extractHTMLAttr(attrs string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(attrs)
	if len(m) < 4 {
		return ""
	}
	for i := 1; i <= 3; i++ {
		if m[i] != "" {
			return strings.TrimSpace(html.UnescapeString(m[i]))
		}
	}
	return ""
}

func pypiSimplePackageURL(indexURL, pkg string) string {
	base := strings.TrimRight(strings.TrimSpace(indexURL), "/")
	if base == "" {
		base = strings.TrimRight(pypiSimpleBaseURL, "/")
	}
	return base + "/" + strings.Trim(pkg, "/") + "/"
}

func newSearxngInstallHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	// 显式置 nil Proxy，避免未配置安装代理时隐式读取进程环境变量出网。
	tr := &http.Transport{
		Proxy:               nil,
		TLSHandshakeTimeout: 20 * time.Second,
	}
	if raw := strings.TrimSpace(proxyURL); raw != "" {
		u, err := traffic.TransportProxyURL(raw)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

func pickPipWheelFromSimpleHTML(baseURL, htmlBody, pyVer string) (*pipWheelCandidate, error) {
	baseParsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("无效索引地址 %q: %w", baseURL, err)
	}
	var best *pipWheelCandidate
	for _, match := range pipSimpleAnchorRe.FindAllStringSubmatch(htmlBody, -1) {
		attrs, text := match[1], strings.TrimSpace(html.UnescapeString(match[2]))
		if pipYankedAttrRe.MatchString(attrs) {
			continue
		}
		rawHref := extractHTMLAttr(attrs, pipHrefAttrRe)
		if rawHref == "" {
			continue
		}
		reqPy := extractHTMLAttr(attrs, pipReqPyAttrRe)
		if !pythonVersionSatisfies(pyVer, reqPy) {
			continue
		}
		refURL, parseErr := url.Parse(rawHref)
		if parseErr != nil {
			continue
		}
		resolved := baseParsed.ResolveReference(refURL)
		filename := filepath.Base(resolved.Path)
		if filename == "" || filename == "." || filename == "/" {
			filename = text
		}
		verMatch := pipWheelFileRe.FindStringSubmatch(filename)
		if len(verMatch) != 2 {
			continue
		}
		verNums, ok := parseNumericVersion(verMatch[1])
		if !ok {
			continue
		}
		var sha string
		if frag := resolved.Fragment; strings.HasPrefix(strings.ToLower(frag), "sha256=") {
			candSHA := strings.TrimSpace(frag[len("sha256="):])
			if len(candSHA) == 64 {
				sha = candSHA
			}
		}
		resolved.Fragment = ""
		cand := &pipWheelCandidate{
			URL:      resolved.String(),
			Filename: filename,
			SHA256:   sha,
			Version:  verNums,
		}
		if best == nil || compareIntSlices(cand.Version, best.Version) > 0 {
			best = cand
		}
	}
	if best == nil {
		return nil, fmt.Errorf("索引页 %s 未找到适用于 Python %s 的 pip-*.whl", baseURL, pyVer)
	}
	return best, nil
}

func downloadVerifiedFile(ctx context.Context, client *http.Client, rawURL, destPath, expectedSHA256 string, maxBytes int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, rawURL)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".dl-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	limited := io.LimitReader(resp.Body, maxBytes+1)
	n, err := io.Copy(io.MultiWriter(tmp, hasher), limited)
	if err != nil {
		return err
	}
	if n > maxBytes {
		return fmt.Errorf("下载文件 %s 超过大小上限 (%d bytes)", rawURL, maxBytes)
	}
	if expectedSHA256 != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, expectedSHA256) {
			return fmt.Errorf("下载校验失败 (%s): sha256 got %s want %s", filepath.Base(destPath), got, expectedSHA256)
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func findLocalPipWheels() []string {
	type item struct {
		path string
		ver  []int
	}
	seen := map[string]bool{}
	var found []item
	for _, pat := range localPipWheelGlobs {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			if seen[m] || !fileExists(m) {
				continue
			}
			seen[m] = true
			var ver []int
			if vm := pipWheelFileRe.FindStringSubmatch(filepath.Base(m)); len(vm) == 2 {
				ver, _ = parseNumericVersion(vm[1])
			}
			found = append(found, item{path: m, ver: ver})
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if c := compareIntSlices(found[i].ver, found[j].ver); c != 0 {
			return c > 0
		}
		return found[i].path > found[j].path
	})
	out := make([]string, len(found))
	for i, it := range found {
		out[i] = it.path
	}
	return out
}

func (sx *searxngService) fetchPipBootstrapWheel(ctx context.Context, lf io.Writer, cfg SearxngManagedConfig, pyVer, bootstrapDir, proxyURL string) (string, error) {
	client, err := newSearxngInstallHTTPClient(proxyURL, 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("构建安装 HTTP 客户端失败：%s", traffic.SanitizeProxyError(proxyURL, err))
	}
	defer client.CloseIdleConnections()

	indexURL := pypiSimplePackageURL(cfg.PyPIIndex, "pip")
	logSearxngNote(lf, "从 PyPI 索引获取 pip 引导包元信息：%s", indexURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New(traffic.SanitizeProxyError(proxyURL, err))
	}
	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PyPI 索引 %s 返回 HTTP %d", indexURL, resp.StatusCode)
	}
	if readErr != nil {
		return "", readErr
	}

	resolvedBase := indexURL
	if resp.Request != nil && resp.Request.URL != nil {
		resolvedBase = resp.Request.URL.String()
	}
	cand, err := pickPipWheelFromSimpleHTML(resolvedBase, string(bodyBytes), pyVer)
	if err != nil {
		return "", err
	}
	destPath := filepath.Join(bootstrapDir, cand.Filename)
	logSearxngNote(lf, "下载 pip 引导 wheel：%s", cand.URL)
	if err := downloadVerifiedFile(ctx, client, cand.URL, destPath, cand.SHA256, 64<<20); err != nil {
		return "", errors.New(traffic.SanitizeProxyError(proxyURL, err))
	}
	return destPath, nil
}

func (sx *searxngService) ensureVenvDir(ctx context.Context, lf io.Writer, pyBin, venvDir string, cmdEnv []string) error {
	_ = os.RemoveAll(venvDir)
	firstErr := sx.runLoggedCmd(ctx, lf, sx.rootDir(), cmdEnv, pyBin, "-m", "venv", venvDir)
	if firstErr == nil && fileExists(sx.venvPythonPath()) {
		return nil
	}

	// Debian/Ubuntu 等系统拆分了 python3.X-venv (ensurepip)，无 root 时改用 --without-pip。
	logSearxngNote(lf, "标准 venv 初始化失败（常见于受限环境缺少 ensurepip / python3-venv），改用 --without-pip 创建并无特权引导 pip")
	_ = os.RemoveAll(venvDir)
	noPipErr := sx.runLoggedCmd(ctx, lf, sx.rootDir(), cmdEnv, pyBin, "-m", "venv", "--without-pip", venvDir)
	if noPipErr == nil && fileExists(sx.venvPythonPath()) {
		return nil
	}

	_ = os.RemoveAll(venvDir)
	if err := sx.runLoggedCmd(ctx, lf, sx.rootDir(), cmdEnv, pyBin, "-m", "virtualenv", venvDir); err == nil && fileExists(sx.venvPythonPath()) {
		return nil
	}
	if virtBin := findHostExecutable("virtualenv"); virtBin != "" {
		_ = os.RemoveAll(venvDir)
		if err := sx.runLoggedCmd(ctx, lf, sx.rootDir(), cmdEnv, virtBin, "-p", pyBin, venvDir); err == nil && fileExists(sx.venvPythonPath()) {
			return nil
		}
	}
	if uvBin := findHostExecutable("uv"); uvBin != "" {
		_ = os.RemoveAll(venvDir)
		if err := sx.runLoggedCmd(ctx, lf, sx.rootDir(), cmdEnv, uvBin, "venv", "--seed", "--python", pyBin, venvDir); err == nil && fileExists(sx.venvPythonPath()) {
			return nil
		}
	}
	return fmt.Errorf("创建 Python venv 失败：%w（--without-pip 回退亦失败：%v）", firstErr, noPipErr)
}

func (sx *searxngService) ensureVenvPip(ctx context.Context, lf io.Writer, cfg SearxngManagedConfig, pyVer, proxyURL string, cmdEnv []string, pipArgs func(...string) []string) error {
	if fileExists(sx.venvPipPath()) {
		return nil
	}
	sx.setProgress("installing", "无特权引导 venv 内 pip（跳过系统 ensurepip）", "")
	venvPy := sx.venvPythonPath()
	bootstrapDir := filepath.Join(sx.rootDir(), ".bootstrap")
	_ = os.MkdirAll(bootstrapDir, 0o755)
	defer func() { _ = os.RemoveAll(bootstrapDir) }()

	installFromWheel := func(whlPath, label string) bool {
		logSearxngNote(lf, "使用%s引导安装 venv pip：%s", label, filepath.Base(whlPath))
		whlEnv := withEnvOverrides(cmdEnv, map[string]string{"PYTHONPATH": whlPath})
		if err := sx.runLoggedCmd(ctx, lf, sx.rootDir(), whlEnv, venvPy, "-m", "pip", "install", "--no-index", "--no-deps", "--ignore-installed", whlPath); err == nil && fileExists(sx.venvPipPath()) {
			return true
		}
		return false
	}

	for _, localWhl := range findLocalPipWheels() {
		if installFromWheel(localWhl, "本地 wheel ") {
			return nil
		}
	}

	var bootstrapErr error
	if whlPath, err := sx.fetchPipBootstrapWheel(ctx, lf, cfg, pyVer, bootstrapDir, proxyURL); err == nil {
		if installFromWheel(whlPath, "PyPI 镜像 wheel ") {
			return nil
		}
		bootstrapErr = errors.New("通过下载的 pip wheel 写入 venv/bin/pip 失败")
	} else {
		bootstrapErr = err
		logSearxngNote(lf, "PyPI wheel 引导失败（%v），尝试 pip.pyz 回退", err)
	}

	if zipURL := strings.TrimSpace(defaultPipZipappURL); zipURL != "" {
		if client, err := newSearxngInstallHTTPClient(proxyURL, 2*time.Minute); err == nil {
			pyzPath := filepath.Join(bootstrapDir, "pip.pyz")
			logSearxngNote(lf, "下载 pip.pyz 引导包：%s", zipURL)
			dlErr := downloadVerifiedFile(ctx, client, zipURL, pyzPath, "", 64<<20)
			client.CloseIdleConnections()
			if dlErr == nil {
				pyzArgs := append([]string{pyzPath}, pipArgs("install", "--no-deps", "--ignore-installed", "pip")...)
				if err := sx.runLoggedCmd(ctx, lf, sx.rootDir(), cmdEnv, venvPy, pyzArgs...); err == nil && fileExists(sx.venvPipPath()) {
					return nil
				} else {
					bootstrapErr = err
				}
			} else {
				logSearxngNote(lf, "pip.pyz 下载失败：%s", traffic.SanitizeProxyError(proxyURL, dlErr))
			}
		}
	}

	if uvBin := findHostExecutable("uv"); uvBin != "" {
		logSearxngNote(lf, "尝试使用 %s 安装 venv pip", uvBin)
		uvArgs := []string{"pip", "install", "--python", venvPy}
		if idx := strings.TrimSpace(cfg.PyPIIndex); idx != "" {
			uvArgs = append(uvArgs, "--index-url", idx)
		}
		uvArgs = append(uvArgs, "pip")
		if err := sx.runLoggedCmd(ctx, lf, sx.rootDir(), cmdEnv, uvBin, uvArgs...); err == nil && fileExists(sx.venvPipPath()) {
			return nil
		}
	}

	return fmt.Errorf("venv 已使用 --without-pip 创建，但无特权引导 pip 失败（请检查出网/安装代理/PyPI 镜像，或在宿主机安装 python3-venv）：%w", bootstrapErr)
}

func (sx *searxngService) ensureVenvAndPip(ctx context.Context, lf io.Writer, cfg SearxngManagedConfig, pyBin, pyVer, proxyURL string, cmdEnv []string, forceReinstall bool, pipArgs func(...string) []string) error {
	venvDir := sx.venvDir()
	if forceReinstall || !fileExists(sx.venvPipPath()) || !fileExists(sx.venvPythonPath()) {
		if err := sx.ensureVenvDir(ctx, lf, pyBin, venvDir, cmdEnv); err != nil {
			return err
		}
	}
	return sx.ensureVenvPip(ctx, lf, cfg, pyVer, proxyURL, cmdEnv, pipArgs)
}

func buildSearxngChildEnv(baseEnv []string, extra map[string]string, proxyURL string) []string {
	if baseEnv == nil {
		baseEnv = os.Environ()
	}
	vals := make(map[string]string, len(baseEnv)+len(extra)+8)
	order := make([]string, 0, len(baseEnv)+len(extra)+8)
	setKey := func(k, v string) {
		if _, ok := vals[k]; !ok {
			order = append(order, k)
		}
		vals[k] = k + "=" + v
	}
	for _, kv := range baseEnv {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		// 剥离宿主高危凭证与旧代理变量，避免向 SearXNG 子进程泄漏 BLADE_PG_DSN / LLM Key 或静默继承未知环境代理。
		if isReservedHostEnvKey(k) {
			continue
		}
		switch strings.ToUpper(k) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
			continue
		}
		setKey(k, kv[i+1:])
	}
	for k, v := range extra {
		setKey(k, v)
	}
	if p := strings.TrimSpace(proxyURL); p != "" {
		for _, pk := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			setKey(pk, p)
		}
		setKey("NO_PROXY", "127.0.0.1,localhost")
		setKey("no_proxy", "127.0.0.1,localhost")
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, vals[k])
	}
	return out
}

func (sx *searxngService) configuredPython() string {
	if sx != nil && sx.m != nil && sx.m.pg != nil {
		if v, ok, _ := sx.m.pg.GetSetting(settingPythonInterp); ok {
			v = strings.TrimSpace(v)
			if v != "" && validatePythonInterp(v) == nil {
				return v
			}
		}
	}
	return detectPython()
}

func (sx *searxngService) Status(ctx context.Context) SearxngStatus {
	cfg := sx.loadConfig()
	_, wsBackend, _, _, _ := sx.m.WebSearch()
	wsURL := sx.m.SearxngURL()
	effProxy := sx.resolveEffectiveProxy(cfg)

	if isSearxngComposeMode() {
		compURL := searxngCompanionURL()
		_, err := sx.writeManagedSettingsFile(cfg, true)
		reachable, healthy, healthErr := probeSearxngEndpoint(ctx, compURL)
		state := "stopped"
		if healthy {
			state = "running"
		} else if reachable {
			state = "error"
		}
		lastErr := healthErr
		if err != nil && lastErr == "" {
			lastErr = err.Error()
		}
		return SearxngStatus{
			Mode:           "compose",
			State:          state,
			Installed:      true,
			Running:        reachable,
			Healthy:        healthy,
			URL:            compURL,
			EffectiveProxy: effProxy,
			LastError:      lastErr,
			DataDir:        sx.rootDir(),
			ActiveInSearch: wsBackend == searxngWebSearchBackend && strings.EqualFold(wsURL, compURL),
			Config:         cfg,
		}
	}

	installed := sx.isInstalled()
	localURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	pid := readSearxngPIDFile(sx.pidPath())
	alive := isSearxngProcessAlive(pid)
	if !alive {
		pid = 0
	}

	sx.mu.Lock()
	opState := sx.opState
	step := sx.installStep
	lastErr := sx.lastError
	sx.mu.Unlock()

	var reachable, healthy bool
	var probeErr string
	if alive {
		reachable, healthy, probeErr = probeSearxngEndpoint(ctx, localURL)
	}

	state := "stopped"
	switch {
	case opState == "installing":
		state = "installing"
	case opState == "starting":
		if alive && healthy {
			state = "running"
		} else {
			state = "starting"
		}
	case alive && healthy:
		state = "running"
	case alive && reachable:
		state = "error"
		if lastErr == "" {
			lastErr = probeErr
		}
	case alive:
		state = "starting"
	case !installed:
		if opState == "error" && lastErr != "" {
			state = "error"
		} else {
			state = "not_installed"
		}
	case opState == "error" && lastErr != "":
		state = "error"
	}

	pyBin, pyVer, _ := resolveHostPython(sx.configuredPython())
	return SearxngStatus{
		Mode:           "host_venv",
		State:          state,
		Installed:      installed,
		Running:        alive,
		Healthy:        alive && healthy,
		PID:            pid,
		URL:            localURL,
		PythonBin:      pyBin,
		PythonVersion:  pyVer,
		EffectiveProxy: effProxy,
		InstallStep:    step,
		LastError:      lastErr,
		InstallLogTail: tailTextFile(sx.installLogPath(), searxngLogMaxTailBytes),
		LogTail:        tailTextFile(sx.runtimeLogPath(), searxngLogMaxTailBytes),
		DataDir:        sx.rootDir(),
		ActiveInSearch: wsBackend == searxngWebSearchBackend && strings.EqualFold(wsURL, localURL),
		Config:         cfg,
	}
}

func (sx *searxngService) setProgress(state, step, errMsg string) {
	sx.mu.Lock()
	sx.opState = state
	sx.installStep = step
	sx.lastError = errMsg
	sx.mu.Unlock()
}

func (sx *searxngService) runLoggedCmd(ctx context.Context, logFile io.Writer, dir string, env []string, bin string, args ...string) error {
	fmt.Fprintf(logFile, "\n$ %s %s\n", bin, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return cmd.Run()
}

func (sx *searxngService) installSync(ctx context.Context, cfg SearxngManagedConfig, forceReinstall bool) error {
	if err := os.MkdirAll(sx.rootDir(), 0o755); err != nil {
		return err
	}
	rotateLogIfLarge(sx.installLogPath(), searxngRotateLogMaxBytes)
	lf, err := os.OpenFile(sx.installLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer lf.Close()

	sx.setProgress("installing", "检查 Python 与 Git 运行环境", "")
	pyBin, pyVer, err := resolveHostPython(sx.configuredPython())
	if err != nil {
		return err
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("未检测到 git，请先安装 git（例如 sudo apt-get install -y git）")
	}
	fmt.Fprintf(lf, "[blade-searxng] python=%s (%s) git=%s\n", pyBin, pyVer, gitBin)

	proxy := sx.resolveInstallProxy(cfg)
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
		"GIT_TERMINAL_PROMPT":           "0",
	}, proxy)

	srcDir := sx.srcDir()
	sx.setProgress("installing", "拉取 SearXNG 源码仓库", "")
	webappFile := filepath.Join(srcDir, "searx", "webapp.py")
	if forceReinstall || !fileExists(webappFile) {
		tmpSrc := srcDir + ".tmp"
		_ = os.RemoveAll(tmpSrc)
		if err := sx.runLoggedCmd(ctx, lf, sx.rootDir(), cmdEnv, gitBin, "clone", "--depth", "1", "--", cfg.RepoURL, tmpSrc); err != nil {
			_ = os.RemoveAll(tmpSrc)
			return fmt.Errorf("拉取 SearXNG 源码失败（可尝试配置 Git 镜像或安装代理）：%w", err)
		}
		_ = os.RemoveAll(srcDir)
		if err := os.Rename(tmpSrc, srcDir); err != nil {
			_ = os.RemoveAll(tmpSrc)
			return err
		}
	} else {
		if err := sx.runLoggedCmd(ctx, lf, srcDir, cmdEnv, gitBin, "pull", "--ff-only"); err != nil {
			fmt.Fprintf(lf, "[warn] git pull skipped (%v), continuing with existing checkout\n", err)
		}
	}

	pipArgs := func(base ...string) []string {
		out := append([]string(nil), base...)
		if idx := strings.TrimSpace(cfg.PyPIIndex); idx != "" {
			out = append(out, "-i", idx)
			if u, parseErr := url.Parse(idx); parseErr == nil && strings.EqualFold(u.Scheme, "http") && u.Hostname() != "" {
				out = append(out, "--trusted-host", u.Hostname())
			}
		}
		return out
	}

	sx.setProgress("installing", "初始化 Python 虚拟环境 (venv)", "")
	if err := sx.ensureVenvAndPip(ctx, lf, cfg, pyBin, pyVer, proxy, cmdEnv, forceReinstall, pipArgs); err != nil {
		return err
	}

	pipBin := sx.venvPipPath()
	sx.setProgress("installing", "安装基础打包工具 (pip / setuptools / wheel)", "")
	if err := sx.runLoggedCmd(ctx, lf, srcDir, cmdEnv, pipBin, pipArgs("install", "--upgrade", "pip", "setuptools", "wheel")...); err != nil {
		return fmt.Errorf("升级 pip 工具链失败：%w", err)
	}

	sx.setProgress("installing", "安装 SearXNG 构建期依赖 (msgspec / pyyaml / pybind11)", "")
	if err := sx.runLoggedCmd(ctx, lf, srcDir, cmdEnv, pipBin, pipArgs("install", "pyyaml", "msgspec", "typing-extensions", "pybind11")...); err != nil {
		return fmt.Errorf("安装 SearXNG 构建依赖失败：%w", err)
	}

	if fileExists(filepath.Join(srcDir, "requirements.txt")) {
		sx.setProgress("installing", "安装 SearXNG 运行依赖 (requirements.txt)", "")
		if err := sx.runLoggedCmd(ctx, lf, srcDir, cmdEnv, pipBin, pipArgs("install", "-r", "requirements.txt")...); err != nil {
			return fmt.Errorf("安装 requirements.txt 失败（可在高级设置切换 PyPI 国内镜像或安装代理）：%w", err)
		}
	}

	sx.setProgress("installing", "安装 Granian WSGI 服务组件", "")
	if fileExists(filepath.Join(srcDir, "requirements-server.txt")) {
		if err := sx.runLoggedCmd(ctx, lf, srcDir, cmdEnv, pipBin, pipArgs("install", "-r", "requirements-server.txt")...); err != nil {
			return fmt.Errorf("安装 requirements-server.txt 失败：%w", err)
		}
	}
	if !fileExists(sx.venvGranianPath()) {
		if err := sx.runLoggedCmd(ctx, lf, srcDir, cmdEnv, pipBin, pipArgs("install", "granian")...); err != nil {
			return fmt.Errorf("安装 granian 失败：%w", err)
		}
	}

	sx.setProgress("installing", "注册 SearXNG 包与校验导入", "")
	if err := sx.runLoggedCmd(ctx, lf, srcDir, cmdEnv, pipBin, pipArgs("install", "--use-pep517", "--no-build-isolation", "--no-deps", "-e", ".")...); err != nil {
		return fmt.Errorf("注册 SearXNG 包失败：%w", err)
	}
	if _, err := sx.writeManagedSettingsFile(cfg, false); err != nil {
		return err
	}
	verifyEnv := buildSearxngChildEnv(nil, map[string]string{
		"PYTHONPATH":            srcDir,
		"SEARXNG_SETTINGS_PATH": sx.settingsPath(),
	}, "")
	if err := sx.runLoggedCmd(ctx, lf, srcDir, verifyEnv, sx.venvPythonPath(), "-c", "import searx.webapp, granian; print('SEARXNG_IMPORT_OK')"); err != nil {
		return fmt.Errorf("SearXNG 模块导入校验失败：%w", err)
	}
	return nil
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func isLoopbackPortOccupied(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (sx *searxngService) stopProcessOnly() error {
	pid := readSearxngPIDFile(sx.pidPath())
	if pid <= 1 || !isSearxngProcessAlive(pid) {
		_ = os.Remove(sx.pidPath())
		return nil
	}
	signalSearxngProcess(pid, false)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !isSearxngProcessAlive(pid) {
			_ = os.Remove(sx.pidPath())
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	signalSearxngProcess(pid, true)
	time.Sleep(150 * time.Millisecond)
	_ = os.Remove(sx.pidPath())
	return nil
}

func (sx *searxngService) startSidecarSync(ctx context.Context, cfg SearxngManagedConfig) error {
	if !sx.isInstalled() {
		return fmt.Errorf("SearXNG 尚未安装，请先安装或点击「一键部署并启动」")
	}
	// 先停掉旧 sidecar，保证端口变更或新 settings.yml / 代理配置干净重加载。
	_ = sx.stopProcessOnly()
	if isLoopbackPortOccupied(cfg.Port) {
		return fmt.Errorf("本机端口 127.0.0.1:%d 已被其它进程占用，请在 SearXNG 配置中更换端口或先释放占用进程", cfg.Port)
	}
	settingsPath, err := sx.writeManagedSettingsFile(cfg, false)
	if err != nil {
		return fmt.Errorf("生成 settings.yml 失败：%w", err)
	}
	rotateLogIfLarge(sx.runtimeLogPath(), searxngRotateLogMaxBytes)
	lf, err := os.OpenFile(sx.runtimeLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开 SearXNG 日志失败：%w", err)
	}

	proxy := sx.resolveEffectiveProxy(cfg)
	sx.setProgress("starting", fmt.Sprintf("启动本地 SearXNG（127.0.0.1:%d）", cfg.Port), "")
	fmt.Fprintf(lf, "\n=== [blade-searxng] starting on 127.0.0.1:%d (proxy=%q, preset=%s) ===\n", cfg.Port, proxy, cfg.EnginesPreset)

	cmd := exec.Command(
		sx.venvGranianPath(),
		"--interface", "wsgi",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(cfg.Port),
		"--workers", "2",
		"searx.webapp:app",
	)
	cmd.Dir = sx.srcDir()
	cmd.Env = buildSearxngChildEnv(nil, map[string]string{
		"PYTHONPATH":            sx.srcDir(),
		"SEARXNG_SETTINGS_PATH": settingsPath,
		"PYTHONUNBUFFERED":      "1",
	}, proxy)
	cmd.Stdout = lf
	cmd.Stderr = lf
	// Setsid / 新进程组让宿主 SearXNG 作为常驻 sidecar 独立于 BLADE 进程组生命周期，
	// ./blade 重启或一键更新换装时不断开服务。
	startDetached(cmd)
	if err := cmd.Start(); err != nil {
		_ = lf.Close()
		return fmt.Errorf("启动 granian 失败：%w", err)
	}
	pid := cmd.Process.Pid
	_ = os.WriteFile(sx.pidPath(), []byte(strconv.Itoa(pid)+"\n"), 0o600)

	waitDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = lf.Close()
		waitDone <- err
	}()

	localURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	deadline := time.Now().Add(searxngStartWaitTimeout)
	var lastProbeErr string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case werr := <-waitDone:
			_ = os.Remove(sx.pidPath())
			tail := tailTextFile(sx.runtimeLogPath(), 2048)
			if tail != "" {
				return fmt.Errorf("SearXNG 启动后提前退出（%v）：%s", werr, tail)
			}
			return fmt.Errorf("SearXNG 启动后提前退出：%v", werr)
		default:
		}
		_, healthy, detail := probeSearxngEndpoint(ctx, localURL)
		if healthy {
			if err := sx.applyURLToManager(localURL, cfg.AutoSwitch); err != nil {
				log.Printf("[searxng] 自动写入搜索配置失败：%v", err)
			}
			log.Printf("[searxng] 本地 sidecar 已就绪：%s (pid=%d)", localURL, pid)
			return nil
		}
		lastProbeErr = detail
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("等待 SearXNG 健康检查超时（%s）：%s", localURL, lastProbeErr)
}

func (sx *searxngService) applyURLToManager(targetURL string, switchBackend bool) error {
	if sx == nil || sx.m == nil {
		return nil
	}
	if err := sx.m.SetSearxngURL(targetURL); err != nil {
		return err
	}
	if !switchBackend {
		return nil
	}
	_, _, _, _, proxy := sx.m.WebSearch()
	if sx.m.pg != nil {
		if err := sx.m.pg.SetSettings(map[string]string{
			settingWebSearchOn:      "true",
			settingWebSearchBackend: searxngWebSearchBackend,
			settingSearxngURL:       targetURL,
		}); err != nil {
			return err
		}
	}
	return sx.m.SetWebSearch(true, searxngWebSearchBackend, nil, nil, &proxy)
}

// StartAsync launches install (if needed) + sidecar start in background, returning the updated status immediately.
func (sx *searxngService) StartAsync(autoInstall bool, forceReinstall bool) (SearxngStatus, error) {
	if sx == nil || sx.m == nil {
		return SearxngStatus{}, fmt.Errorf("manager 未初始化")
	}
	if isSearxngComposeMode() {
		return sx.Status(context.Background()), fmt.Errorf("当前运行在 Docker Compose 伴生模式，SearXNG 由 docker compose 统一启停；改完预设/代理配置后执行 `docker compose restart searxng` 即可生效")
	}
	cfg := sx.loadConfig()
	if !autoInstall && !sx.isInstalled() {
		return sx.Status(context.Background()), fmt.Errorf("SearXNG 尚未安装，请先点「一键部署并启动」")
	}

	sx.mu.Lock()
	if sx.busy {
		sx.mu.Unlock()
		return sx.Status(context.Background()), nil
	}
	opCtx, cancel := context.WithTimeout(context.Background(), searxngInstallTimeout)
	sx.busy = true
	sx.opCancel = cancel
	if forceReinstall || !sx.isInstalled() {
		sx.opState = "installing"
		sx.installStep = "准备安装 SearXNG（Python venv）"
	} else {
		sx.opState = "starting"
		sx.installStep = "准备启动 SearXNG"
	}
	sx.lastError = ""
	sx.mu.Unlock()

	cfg.Enabled = true
	_ = sx.saveConfig(cfg)

	goSafe("searxng/start-async", nil, func() {
		defer func() {
			cancel()
			sx.mu.Lock()
			sx.busy = false
			sx.opCancel = nil
			sx.mu.Unlock()
		}()
		needInstall := forceReinstall || !sx.isInstalled()
		if needInstall {
			if err := sx.installSync(opCtx, cfg, forceReinstall); err != nil {
				sx.setProgress("error", "", err.Error())
				return
			}
		}
		if !autoInstall && forceReinstall {
			// 仅重装时不强制重启？用户点「更新/重装」时通常也希望直接可用，只有显式 InstallOnly 时停在就绪态。
			sx.setProgress("", "", "")
			return
		}
		if err := sx.startSidecarSync(opCtx, cfg); err != nil {
			sx.setProgress("error", "", err.Error())
			return
		}
		sx.setProgress("", "", "")
	})

	// 若已安装且仅启动，稍等最多 2 秒看是否已速启成功，减少一次不必要轮询。
	if !forceReinstall && sx.isInstalled() {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			sx.mu.Lock()
			busy := sx.busy
			sx.mu.Unlock()
			if !busy {
				break
			}
		}
	}
	return sx.Status(context.Background()), nil
}

// InstallOnlyAsync clones/installs/upgrades SearXNG venv in background without forcing backend switch.
func (sx *searxngService) InstallOnlyAsync(forceReinstall bool) (SearxngStatus, error) {
	if sx == nil || sx.m == nil {
		return SearxngStatus{}, fmt.Errorf("manager 未初始化")
	}
	if isSearxngComposeMode() {
		return sx.Status(context.Background()), fmt.Errorf("Docker Compose 伴生模式无需宿主 Python 安装")
	}
	cfg := sx.loadConfig()
	sx.mu.Lock()
	if sx.busy {
		sx.mu.Unlock()
		return sx.Status(context.Background()), nil
	}
	opCtx, cancel := context.WithTimeout(context.Background(), searxngInstallTimeout)
	sx.busy = true
	sx.opCancel = cancel
	sx.opState = "installing"
	sx.installStep = "准备更新 / 安装 SearXNG"
	sx.lastError = ""
	sx.mu.Unlock()

	goSafe("searxng/install-async", nil, func() {
		defer func() {
			cancel()
			sx.mu.Lock()
			sx.busy = false
			sx.opCancel = nil
			sx.mu.Unlock()
		}()
		wasRunning := isSearxngProcessAlive(readSearxngPIDFile(sx.pidPath()))
		if wasRunning {
			_ = sx.stopProcessOnly()
		}
		if err := sx.installSync(opCtx, cfg, forceReinstall); err != nil {
			sx.setProgress("error", "", err.Error())
			return
		}
		if wasRunning || cfg.Enabled {
			if err := sx.startSidecarSync(opCtx, cfg); err != nil {
				sx.setProgress("error", "", err.Error())
				return
			}
		}
		sx.setProgress("", "", "")
	})
	return sx.Status(context.Background()), nil
}

// Stop halts the host SearXNG sidecar and cancels any in-flight install/start operation.
func (sx *searxngService) Stop() (SearxngStatus, error) {
	if sx == nil || sx.m == nil {
		return SearxngStatus{}, fmt.Errorf("manager 未初始化")
	}
	if isSearxngComposeMode() {
		return sx.Status(context.Background()), fmt.Errorf("当前运行在 Docker Compose 伴生模式，请使用 `docker compose stop searxng`")
	}
	sx.mu.Lock()
	cancel := sx.opCancel
	sx.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	cfg := sx.loadConfig()
	cfg.Enabled = false
	if err := sx.saveConfig(cfg); err != nil {
		return sx.Status(context.Background()), err
	}
	err := sx.stopProcessOnly()
	sx.setProgress("", "", "")
	return sx.Status(context.Background()), err
}

// SaveConfig validates, persists, and regenerates SearXNG's settings.yml.
func (sx *searxngService) SaveConfig(in SearxngManagedConfig) (SearxngStatus, error) {
	if sx == nil || sx.m == nil {
		return SearxngStatus{}, fmt.Errorf("manager 未初始化")
	}
	prev := sx.loadConfig()
	in.Enabled = prev.Enabled // 启停态只由 start/stop 显式控制
	norm, err := normalizeSearxngManagedConfig(in)
	if err != nil {
		return SearxngStatus{}, err
	}
	if err := sx.saveConfig(norm); err != nil {
		return SearxngStatus{}, err
	}
	if isSearxngComposeMode() {
		if _, err := sx.writeManagedSettingsFile(norm, true); err != nil {
			return sx.Status(context.Background()), err
		}
		return sx.Status(context.Background()), nil
	}
	if sx.isInstalled() {
		if _, err := sx.writeManagedSettingsFile(norm, false); err != nil {
			return sx.Status(context.Background()), err
		}
		// 若正在运行，同步平滑重启让新端口/代理/引擎预设立刻生效。
		if isSearxngProcessAlive(readSearxngPIDFile(sx.pidPath())) {
			return sx.StartAsync(false, false)
		}
	}
	return sx.Status(context.Background()), nil
}

// EnsureComposeBootConfig synchronously ensures ./data/searxng/settings.yml exists in compose
// mode before /api/health turns ready (preserving manually customized files without the managed header).
func (sx *searxngService) EnsureComposeBootConfig() {
	if sx == nil || sx.m == nil || !isSearxngComposeMode() {
		return
	}
	cfg := sx.loadConfig()
	path := sx.settingsPath()
	if raw, err := os.ReadFile(path); err == nil {
		if !strings.HasPrefix(string(raw), "# managed-by: blade") {
			return
		}
	}
	if _, err := sx.writeManagedSettingsFile(cfg, true); err != nil {
		log.Printf("[searxng] 初始化 Compose 配置失败：%v", err)
	}
	compURL := searxngCompanionURL()
	if sx.m.SearxngURL() == "" {
		if err := sx.m.SetSearxngURL(compURL); err != nil {
			log.Printf("[searxng] 自动设置伴生 URL 失败：%v", err)
		}
	}
}

// CancelPendingOps cancels any in-flight install/start goroutine without stopping the detached sidecar.
func (sx *searxngService) CancelPendingOps() {
	if sx == nil {
		return
	}
	sx.mu.Lock()
	cancel := sx.opCancel
	sx.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// AutoResume re-attaches or restarts a previously enabled SearXNG sidecar on BLADE startup.
func (sx *searxngService) AutoResume(ctx context.Context) {
	if sx == nil || sx.m == nil {
		return
	}
	if isSearxngComposeMode() {
		sx.EnsureComposeBootConfig()
		return
	}
	cfg := sx.loadConfig()
	if !cfg.Enabled || !sx.isInstalled() {
		return
	}
	pid := readSearxngPIDFile(sx.pidPath())
	localURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Port)
	if isSearxngProcessAlive(pid) {
		if _, healthy, _ := probeSearxngEndpoint(ctx, localURL); healthy {
			log.Printf("[searxng] 已挂载存活宿主 sidecar：%s (pid=%d)", localURL, pid)
			return
		}
	}
	log.Printf("[searxng] 正在恢复已启用的宿主 SearXNG sidecar (127.0.0.1:%d)…", cfg.Port)
	if err := sx.startSidecarSync(ctx, cfg); err != nil {
		log.Printf("[searxng] 启动恢复失败：%v", err)
		sx.setProgress("error", "", err.Error())
	}
}
