package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
)

// miniappAudit inspects a unpacked/decrypted WeChat Mini Program source tree inside
// the task workspace using the shared Python scanner (blade-jsaudit.py --dir), leaving
// js_audit's DB schema untouched.
func (t *ToolSet) miniappAudit() actool.CoreTool {
	return readTool(
		"miniapp_audit",
		"微信小程序解包产物静态审计（纯本地离线扫描解包目录）："+
			"提取页面路由（app.json/app-config.json）、接口与域名（wx.request/uploadFile/downloadFile/connectSocket/web-view）、"+
			"硬编码密钥（AppSecret/AK/SK/JWT/地图 key）、CloudBase 云开发（env/云函数/数据库集合）及签名算法候选位置。"+
			"输入解包目录（如 ../miniapp/<appid>/source 或 ../samples/decompiled/raw），返回可执行命令并在 runDir/miniapp-audit 输出 report.json/endpoints.txt/secrets.txt（密钥已脱敏）。",
		obj(map[string]any{
			"dir":   str("解包源码目录：相对当前 run 目录，或任务目录内的路径（例如 ../miniapp/<appid>/source 或 ../samples/decompiled/raw）"),
			"appid": str("可选：小程序 AppID（wx+16位hex，仅用于报告标注；请勿填写个人微信号 wxid）"),
			"osv":   boolField("是否包含依赖 OSV 校验参数传递（默认 false，纯本地静态扫描）"),
		}, "dir"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Dir   string `json:"dir"`
				AppID string `json:"appid"`
				OSV   *bool  `json:"osv"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("参数解析失败: " + err.Error()), nil
			}
			dir := strings.TrimSpace(a.Dir)
			if dir == "" {
				return actool.Errorf("dir 不能为空"), nil
			}
			if strings.TrimSpace(t.runDir) == "" {
				return actool.Errorf("miniapp_audit 未启用: 本次 run 没有可用的工作目录"), nil
			}
			runDir := t.runDir
			taskRoot := strings.TrimSpace(t.taskRoot)
			if taskRoot == "" {
				taskRoot = runDir
				if ok, _ := regexp.MatchString(`^i[0-9]+$`, filepath.Base(runDir)); ok {
					taskRoot = filepath.Dir(runDir)
				}
			}
			abs := dir
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(runDir, abs)
			}
			abs = filepath.Clean(abs)
			if resolved, err := filepath.EvalSymlinks(abs); err == nil {
				abs = resolved
			}
			if resolved, err := filepath.EvalSymlinks(taskRoot); err == nil {
				taskRoot = resolved
			}
			rel, err := filepath.Rel(taskRoot, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return actool.Errorf(fmt.Sprintf("dir 超出任务目录范围: %s", a.Dir)), nil
			}
			info, err := os.Stat(abs)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("目录不存在或不可读: %v", err)), nil
			}
			if !info.IsDir() {
				return actool.Errorf(fmt.Sprintf("dir 不是目录: %s", a.Dir)), nil
			}
			script := t.ensureJsAuditScript()
			if script == "" {
				return actool.Errorf("miniapp_audit 未启用: 脚本落盘失败"), nil
			}
			out := filepath.Join(runDir, "miniapp-audit")
			cmd := fmt.Sprintf("python3 %s --dir %s --out %s", quoteShell(script), quoteShell(abs), quoteShell(out))
			if appid := strings.TrimSpace(a.AppID); appid != "" {
				cmd += " --appid " + quoteShell(appid)
			}
			if a.OSV == nil || !*a.OSV {
				cmd += " --no-osv"
			}
			return jsonResult(map[string]any{
				"run_with_bash": cmd,
				"dir":           abs,
				"out":           out,
				"note": "纯本地静态扫描；report.json 含 routes/endpoints/secrets/cloud/sign_candidates。" +
					"发现的 AppID 与接口批量入 insert_assets，密钥与云环境先 record_fact 再动态验证，提交发现前请过 mobile_evidence_check。",
			})
		})
}
