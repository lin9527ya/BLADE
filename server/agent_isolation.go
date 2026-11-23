package server

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
)

// agentIso 是 agent 降权的进程级配置（一次安装、全局生效）。未启用时一切保持原行为：
// agent 与服务端同权——提示注入即控制台全权（见 deploy/README.md「信任模型」）。
type agentIsoState struct {
	enabled bool
	user    string
	uid     uint32
	gid     uint32
	home    string
	roots   []string // 文件工具允许访问的真实根（已 EvalSymlinks）
}

var agentIso agentIsoState

// fileGuard holds the file-tool allow roots. It is installed regardless of
// BLADE_AGENT_USER: dropping privileges is optional, but a prompt-injected
// Read/Write must not reach ~/.ssh or the JWT key on a default deployment.
type fileGuardState struct {
	installed bool
	roots     []string // real paths (EvalSymlinks)
	denied    []string // real paths inside dataDir forbidden even when dataDir is an allow root
}

var fileGuard fileGuardState

var privilegedDataSubdirs = []string{
	filepath.Join("traffic", "_ca"),
	filepath.Join("traffic", "_index"),
	filepath.Join("traffic", "_blobs"),
	"archives",
	"transcripts",
}

func agentDeniedSubpaths(realDataDir string) []string {
	if realDataDir == "" {
		return nil
	}
	out := make([]string, 0, len(privilegedDataSubdirs)+1)
	for _, rel := range privilegedDataSubdirs {
		out = append(out, filepath.Join(realDataDir, rel))
	}
	out = append(out, filepath.Join(realDataDir, jwtKeyFilename))
	return out
}

func isUnderDeniedPath(abs string, denied []string) bool {
	sep := string(os.PathSeparator)
	for _, d := range denied {
		if abs == d || strings.HasPrefix(abs, d+sep) {
			return true
		}
	}
	return false
}

const settingFileRootsOpenEnv = "BLADE_AGENT_FILE_ROOTS_OPEN"

// agentEnvDenyPrefixes / agentEnvDenyFragments 是子进程环境的"疑似凭据"判定。
//
// 刻意用**黑名单**而不是白名单：白名单会悄悄弄坏 agent 依赖的一堆工具变量
// （JAVA_HOME / NODE_OPTIONS / PLAYWRIGHT_BROWSERS_PATH…），而它们不是秘密。代价是
// "没被模式覆盖的新秘密"会漏——新增带凭据的环境变量时，记得把前缀补到这里。
var agentEnvDenyPrefixes = []string{
	"ANTHROPIC_", "OPENAI_", "CLAUDE_", "BLADE_",
	"AWS_", "GH_", "GITHUB_", "AZURE_", "GOOGLE_", "GCP_", "HF_", "HUGGINGFACE_",
}

var agentEnvDenyFragments = []string{
	"API_KEY", "APIKEY", "ACCESS_KEY", "SECRET", "TOKEN", "PASSWORD", "PASSWD",
	"CREDENTIAL", "PRIVATE_KEY",
}

// agentEnvOverrideKeys 不继承，而是由 BLADE 重写（家目录/身份/条目指向服务端用户的都换掉）。
var agentEnvOverrideKeys = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
	"SSH_AUTH_SOCK": true, "SSH_AGENT_PID": true, "GPG_AGENT_INFO": true, "KRB5CCNAME": true,
}

// InstallAgentIsolation 按 BLADE_AGENT_USER 给 agent 的**执行面**上锁：
//
//   - 命令（Bash / 后台任务 / 交互式 shell / run_execution / 自定义工具）以该用户身份运行；
//   - 子进程环境按"疑似凭据"模式清洗——LLM key、BLADE_* 等不再被继承；
//   - Norma 的文件工具（Read/Write/Edit/MultiEdit/LS/Glob/Grep）被限制在
//     dataDir + skillDir + BLADE_AGENT_READ_ROOTS 之内（逗号或冒号分隔）。
//
// 未设置 BLADE_AGENT_USER 时什么都不做，只留一行信任模型提示——行为与之前完全一致。
// 设置后要求进程是 root（降权需要），否则**报错而不是静默**：配了却没生效比不配更危险。
func InstallAgentIsolation(dataDir, keyDir, skillDir string) error {
	spec := strings.TrimSpace(os.Getenv("BLADE_AGENT_USER"))
	if spec == "" {
		log.Printf("[isolation] 未启用 agent 降权（BLADE_AGENT_USER 未设置）：agent 与服务端同权，" +
			"提示注入即控制台全权。隔离做法见 deploy/README.md「信任模型」")
		return nil
	}
	// keyDir 放 jwt.key 镜像、config.json 也在它旁边。它若落在 dataDir 里面，降权后
	// agent 依然读得到——那就不用降权了。宁可拒绝启动，也不给一个"看着隔离、实则没隔"的状态。
	if d, k := filepath.Clean(dataDir), filepath.Clean(keyDir); k == d || strings.HasPrefix(k, d+string(os.PathSeparator)) {
		return fmt.Errorf("data 目录 %s 包含了密钥/配置目录 %s（jwt.key、config.json）："+
			"隔离模式要求 -data 与可执行文件目录分开（默认 data/ 子目录即可）", d, k)
	}
	uid, gid, home, err := resolveAgentAccount(spec)
	if err != nil {
		return fmt.Errorf("BLADE_AGENT_USER=%q: %w", spec, err)
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("BLADE_AGENT_USER=%q 需要以 root 启动才能降权（当前 euid=%d）", spec, os.Geteuid())
	}
	roots, err := agentAllowedRoots(dataDir, skillDir, os.Getenv("BLADE_AGENT_READ_ROOTS"))
	if err != nil {
		return err
	}
	var denied []string
	if len(roots) > 0 && strings.TrimSpace(dataDir) != "" {
		denied = agentDeniedSubpaths(roots[0])
	}
	agentIso = agentIsoState{enabled: true, user: spec, uid: uid, gid: gid, home: home, roots: roots}
	// Keep the file guard's roots identical to the isolation roots; the guard is
	// installed by InstallFileRootGuard either before or after this call.
	if fileGuard.installed {
		fileGuard.roots = roots
		fileGuard.denied = denied
	}

	if err := prepareAgentStateDirs(dataDir); err != nil {
		return fmt.Errorf("把 data 目录交给 agent 用户：%w", err)
	}
	actool.SetExecHook(hardenAgentCmd)
	actool.SetPathGuard(agentPathGuard)
	log.Printf("[isolation] agent 以 %s (uid=%d gid=%d, home=%s) 执行；文件工具允许访问：%s；子进程环境已按凭据模式清洗",
		spec, uid, gid, home, strings.Join(roots, "、"))
	return nil
}

// prepareAgentStateDirs 把 dataDir 下工作目录交给 agent 用户（如 tasks），但显式排除
// 服务端特权目录（traffic/_ca 私钥、traffic/_index、traffic/_blobs、archives、transcripts
// 以及可能遗留的 jwt.key），并将它们收紧到服务端进程用户与 0700/0600 权限。
func prepareAgentStateDirs(dataDir string) error {
	uid, gid := agentIso.uid, agentIso.gid
	srvUID, srvGID := os.Geteuid(), os.Getegid()
	denied := map[string]bool{}
	for _, rel := range privilegedDataSubdirs {
		p := filepath.Join(dataDir, rel)
		denied[p] = true
		if _, err := os.Lstat(p); err == nil {
			_ = hardenServerOnlyTree(p, srvUID, srvGID)
		}
	}
	trafficRoot := filepath.Join(dataDir, "traffic")
	publicCADir := filepath.Join(trafficRoot, "_public_ca")
	jwtLegacy := filepath.Join(dataDir, jwtKeyFilename)

	return filepath.WalkDir(dataDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if denied[p] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// 保持 data/traffic 与只读公钥证书目录归服务端所有（仅授予遍历/只读权限）。
		if p == trafficRoot || p == publicCADir {
			_ = os.Lchown(p, srvUID, srvGID)
			if d.IsDir() {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		}
		if strings.HasPrefix(p, publicCADir+string(os.PathSeparator)) {
			_ = os.Lchown(p, srvUID, srvGID)
			if d.IsDir() {
				_ = os.Chmod(p, 0o755)
			} else {
				_ = os.Chmod(p, 0o644)
			}
			return nil
		}
		if p == jwtLegacy {
			_ = os.Lchown(p, srvUID, srvGID)
			if !d.IsDir() {
				_ = os.Chmod(p, 0o600)
			}
			return nil
		}
		if err := os.Lchown(p, int(uid), int(gid)); err != nil {
			return err
		}
		if d.IsDir() {
			// setgid + 组可写：之后服务端在这个目录下新建的子目录继承 worker 组，
			// 配合 0775 的创建模式，降权后的 agent 才写得进去。
			if err := os.Chmod(p, 0o775|os.ModeSetgid); err != nil {
				return err
			}
		}
		return nil
	})
}

func hardenServerOnlyTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		_ = os.Lchown(p, uid, gid)
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			_ = os.Chmod(p, 0o700)
		} else {
			_ = os.Chmod(p, 0o600)
		}
		return nil
	})
}

// hardenAgentCmd 把一条即将执行的命令收敛到 agent 身份：降权 + 环境清洗。
// Norma 的 ExecHook 与 BLADE 自己的两个 spawn 点（run_execution、自定义工具）共用它；
// 隔离未启用时是 no-op。
func hardenAgentCmd(cmd *exec.Cmd) {
	if !agentIso.enabled {
		return
	}
	applyAgentCredential(cmd)
	cmd.Env = scrubAgentEnv(cmd.Env)
}

// scrubAgentEnv 丢掉疑似凭据的环境变量，并把 HOME/USER 指向 worker。
// 同名键取**最后一次出现**（append 覆盖语义；libc getenv 取第一个，不去重就会拿到旧值）。
func scrubAgentEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	val := make(map[string]string, len(env)+3)
	order := make([]string, 0, len(env)+3)
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		if agentEnvDenied(k) {
			continue
		}
		if _, ok := val[k]; !ok {
			order = append(order, k)
		}
		val[k] = kv
	}
	out := make([]string, 0, len(order)+3)
	for _, k := range order {
		out = append(out, val[k])
	}
	out = append(out, "HOME="+agentIso.home, "USER="+agentIso.user, "LOGNAME="+agentIso.user)
	return out
}

func agentEnvDenied(key string) bool {
	up := strings.ToUpper(key)
	if agentEnvOverrideKeys[up] {
		return true
	}
	for _, p := range agentEnvDenyPrefixes {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	for _, f := range agentEnvDenyFragments {
		if strings.Contains(up, f) {
			return true
		}
	}
	return false
}

// agentPathGuard 是隔离模式下文件工具的允许面：只放行配置的根（含其子路径）。
// 它收到的是 Norma 解析过软链的真实路径，所以软链绕不过去。
func agentPathGuard(abs string) error {
	if !fileGuard.installed {
		return nil
	}
	if isUnderDeniedPath(abs, fileGuard.denied) {
		return fmt.Errorf("拒绝访问受限系统目录 %s（私钥/流量索引/归档/转写受保护）", abs)
	}
	for _, root := range fileGuard.roots {
		if abs == root || strings.HasPrefix(abs, root+string(os.PathSeparator)) {
			return nil
		}
	}
	return fmt.Errorf("拒绝访问 %s：隔离模式下文件工具只允许访问 %s；其它路径请用 Bash 在任务目录内处理",
		abs, strings.Join(fileGuard.roots, "、"))
}

// InstallFileRootGuard confines the agent's file tools to dataDir, skillDir and
// BLADE_AGENT_READ_ROOTS. It is independent of BLADE_AGENT_USER: without the
// guard, a prompt-injected Read/Write can reach ~/.ssh/authorized_keys or the
// JWT key even when the process runs unprivileged.
//
// BLADE_AGENT_FILE_ROOTS_OPEN=1 disables the guard for troubleshooting only; the
// startup log says so explicitly.
func InstallFileRootGuard(dataDir, skillDir string) error {
	fileGuard = fileGuardState{}
	if v := strings.ToLower(strings.TrimSpace(os.Getenv(settingFileRootsOpenEnv))); v == "1" || v == "true" || v == "yes" {
		actool.SetPathGuard(nil)
		log.Printf("[file-guard] 已按 %s 关闭文件工具边界：agent 可读写任意路径（仅用于排障）", settingFileRootsOpenEnv)
		return nil
	}
	roots, err := agentAllowedRoots(dataDir, skillDir, os.Getenv("BLADE_AGENT_READ_ROOTS"))
	if err != nil {
		return err
	}
	var denied []string
	if len(roots) > 0 && strings.TrimSpace(dataDir) != "" {
		denied = agentDeniedSubpaths(roots[0])
	}
	fileGuard = fileGuardState{installed: true, roots: roots, denied: denied}
	actool.SetPathGuard(agentPathGuard)
	log.Printf("[file-guard] 文件工具限制在：%s（已排除 CA 私钥/索引/归档/转写；这是工具层边界，不是沙箱）",
		strings.Join(roots, "、"))
	return nil
}

// agentAllowedRoots 解析允许根为真实路径。dataDir / skillDir 必须存在；额外根来自
// BLADE_AGENT_READ_ROOTS（PathListSeparator 或逗号分隔），写错就报错（启动期暴露）。
func agentAllowedRoots(dataDir, skillDir, extra string) ([]string, error) {
	seen := map[string]bool{}
	roots := make([]string, 0, 2)
	add := func(p string) error {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return fmt.Errorf("允许根 %s 不存在或无法解析：%w", p, err)
		}
		if seen[real] {
			return nil
		}
		seen[real] = true
		roots = append(roots, real)
		return nil
	}
	if err := add(dataDir); err != nil {
		return nil, err
	}
	if err := add(skillDir); err != nil {
		return nil, err
	}
	for _, p := range strings.FieldsFunc(extra, func(r rune) bool {
		return r == ',' || r == os.PathListSeparator || r == ';'
	}) {
		if err := add(p); err != nil {
			return nil, err
		}
	}
	return roots, nil
}
