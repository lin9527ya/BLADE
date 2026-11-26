package server

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// 宿主工具 inventory：只读检查关键二进制是否存在、版本是什么。
//
// 不做安装、不调 root、不改变环境。镜像/主机缺少工具时，agent 的 loot_exec/扫描类
// 工具会“看起来调了但没效果”；把这件事显式列出来，排查时先看这里。

type hostToolProbe struct {
	Name     string
	Args     []string
	Required bool
	Purpose  string
}

var hostToolCatalog = []hostToolProbe{
	{Name: "ssh", Args: []string{"-V"}, Required: true, Purpose: "tunnel_open / loot_exec / foothold_exec"},
	{Name: "sshpass", Args: []string{"-V"}, Required: true, Purpose: "密码型 SSH/FTP 登录"},
	{Name: "scp", Args: []string{"-V"}, Required: true, Purpose: "loot_upload / loot_download（SSH）"},
	{Name: "smbclient", Args: []string{"--version"}, Purpose: "loot_upload / loot_download（SMB）"},
	{Name: "nxc", Args: []string{"--version"}, Purpose: "loot_exec / enum_privilege（Windows/AD）"},
	{Name: "netexec", Args: []string{"--version"}, Purpose: "nxc 的别名"},
	{Name: "nmap", Args: []string{"--version"}, Purpose: "端口/服务扫描"},
	{Name: "nuclei", Args: []string{"-version"}, Purpose: "模板验证与 OAST"},
	{Name: "ffuf", Args: []string{"-V"}, Purpose: "目录/参数枚举"},
	{Name: "sqlmap", Args: []string{"--version"}, Purpose: "SQL 注入验证"},
	{Name: "hydra", Args: []string{"-h"}, Purpose: "口令尝试（预算门覆盖）"},
	{Name: "proxychains4", Args: []string{"--version"}, Purpose: "经隧道访问内网"},
	{Name: "chisel", Args: []string{"--version"}, Purpose: "可选多协议隧道"},
	{Name: "java", Args: []string{"-version"}, Purpose: "jadx / apktool / apksigner"},
	{Name: "jadx", Args: []string{"--version"}, Purpose: "APK 反编译"},
	{Name: "apktool", Args: []string{"--version"}, Purpose: "APK 解包/重打包"},
	{Name: "aapt2", Args: []string{"version"}, Purpose: "Android 资源/Manifest 解析"},
	{Name: "apksigner", Args: []string{"version"}, Purpose: "APK 签名验证与重签名"},
	{Name: "zipalign", Args: []string{"-h"}, Purpose: "APK 安装包字节对齐"},
	{Name: "adb", Args: []string{"version"}, Purpose: "Android 真机/模拟器"},
	{Name: "frida", Args: []string{"--version"}, Purpose: "移动端动态 Hook / SSL 绕过 / RPC 签名"},
	{Name: "objection", Args: []string{"version"}, Purpose: "基于 Frida 的自动化运行时探索"},
	{Name: "radare2", Args: []string{"-v"}, Purpose: "SO / Mach-O 原生库逆向分析"},
	{Name: "ideviceinfo", Args: []string{"--version"}, Purpose: "iOS USB 真机状态查询"},
	{Name: "iproxy", Args: []string{"--version"}, Purpose: "iOS usbmuxd 端口映射"},
	{Name: "curl", Args: []string{"--version"}, Purpose: "HTTP 探测/回连验证"},
	{Name: "python3", Args: []string{"--version"}, Purpose: "脚本与工具链"},
}

type hostToolStatus struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Available bool   `json:"available"`
	Required  bool   `json:"required"`
	Purpose   string `json:"purpose"`
	Error     string `json:"error,omitempty"`
}

func probeHostTool(ctx context.Context, t hostToolProbe) hostToolStatus {
	out := hostToolStatus{Name: t.Name, Required: t.Required, Purpose: t.Purpose}
	path, err := exec.LookPath(t.Name)
	if err != nil {
		out.Error = "未安装"
		return out
	}
	out.Path = path
	out.Available = true
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, path, t.Args...)
	raw, err := cmd.CombinedOutput()
	line := strings.TrimSpace(string(raw))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	out.Version = strings.TrimSpace(line)
	if err != nil && out.Version == "" {
		out.Version = "已安装（版本命令返回非零）"
	}
	return out
}

func (s *Server) hostToolsInventory(w http.ResponseWriter, r *http.Request) {
	out := make([]hostToolStatus, 0, len(hostToolCatalog))
	for _, t := range hostToolCatalog {
		out = append(out, probeHostTool(r.Context(), t))
	}
	writeJSON(w, 200, map[string]any{
		"tools": out,
		"note":  "只读检查：不安装、不提权、不修改环境；版本命令失败只表示无法确认版本。",
	})
}
