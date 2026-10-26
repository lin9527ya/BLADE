package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// CVE 库（agent 侧）：按资产/指纹查 CVE、按库内脚本执行利用。
//
// 两条硬纪律：
//   - 匹配只是线索，不是漏洞证据；不能据此报 finding/foothold；
//   - cve_exploit 只执行并返回输出，不自动产生 any 节点；真实回显由 worker 核验后
//     用现有的 report_finding/report_foothold 登记。

const cveLookupNote = "CVE/版本/指纹匹配只是线索，不是漏洞证据；先按 probe_guide/描述实际验证。" +
	"cve_exploit 只执行库内脚本并返回输出/execution_id，不会自动产生 finding 或据点；" +
	"真实可复现的输出才能用 report_finding/report_foothold 登记。"

// cveToolBlock 是代码持有的 CVE 纪律，追加在 worker/planner 可编辑提示词之后。
// 与 hintLifecycleBlock 同一模式：旧库里的自定义提示词也能收到这条纪律。
const cveToolBlock = `

**CVE 库（lookup_cves / cve_exploit）**：
- 识别到产品/版本或指纹时，可 lookup_cves(asset_id) 查库；它返回详情、探测指南与"是否有脚本"，不返回脚本正文。
- 版本匹配且条目 active 时，可用 cve_exploit(cve_id, asset_id, target, args) 按库内脚本实际触发；平台仍会走授权范围/拦截规则/作业时间窗/人工审批门。
- cve_exploit 只执行并返回输出/execution_id，**不会自动产生 finding 或据点**；输出必须自己核实，真实可复现才用 report_finding/report_foothold 登记。
- **CVE/版本/指纹匹配本身不是证据**；匹配只是线索，不能据此报漏洞或据此判定目标达成。`

const cveLookupBlock = `

**CVE 库（lookup_cves）**：识别到产品/版本或指纹时，可 lookup_cves(asset_id) 查 CVE 详情、探测指南与是否有脚本；**匹配本身不是证据**。脚本执行由任务内的 worker/planner 用 cve_exploit 负责（主 Agent 无执行服务）。`

func cveAgentView(c db.CVE) map[string]any {
	return map[string]any{
		"cve_id":             c.CVEID,
		"title":              c.Title,
		"severity":           c.Severity,
		"cvss_score":         c.CVSSScore,
		"vendor":             c.Vendor,
		"description":        clipKnowledge(c.Description, 6000),
		"affected_products":  c.AffectedProducts,
		"version_constraint": c.VersionConstraint,
		"fingerprint":        c.Fingerprint,
		"probe_guide":        clipKnowledge(c.ProbeGuide, 6000),
		"refs":               c.Refs,
		"has_exploit":        c.HasExploit,
		"exploit_language":   c.ExploitLanguage,
		"exploit_filename":   c.ExploitFilename,
		"exploit_usage":      c.ExploitUsage,
		"exploit_sha256":     c.ExploitSHA256,
		"status":             c.Status,
		"revision":           c.Revision,
		"uses":               c.Uses,
		"executions":         c.Executions,
	}
}

func cveMatchView(m db.CVEMatch) map[string]any {
	out := cveAgentView(m.CVE)
	out["score"] = m.Score
	out["match_reasons"] = m.MatchReasons
	out["version_match"] = m.VersionMatch
	out["asset_version"] = m.AssetVersion
	out["matched_product"] = m.MatchedProduct
	out["asset_id"] = m.AssetID
	out["asset_label"] = m.AssetLabel
	return out
}

// lookupCVEs 查 CVE 库：按编号取详情，或按资产/指纹找候选。
func (t *ToolSet) lookupCVEs() actool.CoreTool {
	return readTool("lookup_cves",
		"查【CVE 库】：按 cve_id 取详情（描述/探测指引/是否有脚本），或按 asset_id/指纹/产品+版本找匹配的 CVE。\n"+
			"★ CVE 命中只是线索，不是漏洞证据；先按 probe_guide 实际验证。\n"+
			"★ 返回 exploit 元数据（语言/用法/SHA），**不返回脚本正文**；要执行用 cve_exploit。\n"+
			"★ 具体到某台资产时优先传 asset_id，平台会按任务范围与真实指纹匹配。",
		obj(map[string]any{
			"cve_id":       str("CVE 编号，如 CVE-2021-44228（优先精确取详情）"),
			"asset_id":     idp("资产 id（优先；平台按任务范围与真实指纹匹配）"),
			"technologies": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "技术栈（可选，如 [\"log4j 2.14.1\"]）"},
			"favicon_mmh3": str("favicon MMH3（可选，强信号）"),
			"service_name": str("服务名（可选，如 http）"),
			"port":         intp("端口（可选）"),
			"page_title":   str("页面标题（可选，弱信号）"),
			"product":      str("产品标识（可选；只给产品/版本时用）"),
			"version":      str("产品版本（可选，如 2.14.1）"),
			"limit":        intp("返回上限，默认 5，最大 10"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("lookup_cves 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				CVEID        string          `json:"cve_id"`
				AssetID      json.RawMessage `json:"asset_id"`
				Technologies []string        `json:"technologies"`
				FaviconMMH3  string          `json:"favicon_mmh3"`
				ServiceName  string          `json:"service_name"`
				Port         int             `json:"port"`
				PageTitle    string          `json:"page_title"`
				Product      string          `json:"product"`
				Version      string          `json:"version"`
				Limit        int             `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 5
			}
			if limit > 10 {
				limit = 10
			}
			cveID := db.NormalizeCVEID(a.CVEID)
			if cveID != "" {
				c, err := t.as.GetCVEByCVEID(cveID)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if c == nil {
					return actool.Errorf(fmt.Sprintf("CVE 不存在: %s", cveID)), nil
				}
				t.as.TouchCVEUses([]int64{c.ID})
				out := map[string]any{"cve": cveAgentView(*c), "note": cveLookupNote}
				// 同时给了资产/指纹时，附带该目标上的匹配结论。
				if assets := t.lookupCVEAsset(a.AssetID); len(assets) > 0 {
					if m, err := t.as.MatchCVEForAsset(t.taskID, c.CVEID, assets[0].ID); err == nil && m != nil {
						out["match"] = cveMatchView(*m)
					}
				}
				return jsonResult(out)
			}

			var sig db.CVEAssetSignals
			var assetView map[string]any
			assetID := pid(a.AssetID)
			if assetID > 0 {
				assets := t.lookupCVEAsset(a.AssetID)
				if len(assets) == 0 {
					return actool.Errorf(fmt.Sprintf("asset_id=%d 不在本任务范围内或不存在", assetID)), nil
				}
				sig = db.SignalsFromAsset(assets[0])
				assetView = map[string]any{
					"id": assets[0].ID, "type": assets[0].Type, "url": assets[0].URL,
					"domain": assets[0].Domain, "ip": assets[0].IP,
					"technologies": assets[0].Technologies, "favicon_mmh3": assets[0].FaviconMMH3,
					"service_name": assets[0].ServiceName, "port": assets[0].Port,
				}
			} else {
				fp := db.PlaybookFingerprint{
					Technologies: a.Technologies, FaviconMMH3: a.FaviconMMH3,
					ServiceName: a.ServiceName, Port: a.Port, PageTitle: a.PageTitle,
				}
				sig = db.SignalsFromFingerprint(fp, a.Product, a.Version)
				if len(sig.Products) == 0 && sig.FaviconMMH3 == "" && sig.ServiceName == "" {
					return actool.Errorf("没有可用输入：传 cve_id、asset_id，或至少给 technologies/product/favicon_mmh3/service_name 之一"), nil
				}
			}
			matches, err := t.as.MatchCVEs(sig, limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			ids := make([]int64, 0, len(matches))
			views := make([]map[string]any, 0, len(matches))
			for _, m := range matches {
				ids = append(ids, m.ID)
				views = append(views, cveMatchView(m))
			}
			t.as.TouchCVEUses(ids)
			out := map[string]any{
				"matches": views, "count": len(views),
				"fingerprint": map[string]any{
					"products": sig.Products, "technologies": sig.Technologies,
					"favicon_mmh3": sig.FaviconMMH3, "service_name": sig.ServiceName,
					"port": sig.Port, "page_title": sig.PageTitle,
				},
				"note": cveLookupNote,
			}
			if assetView != nil {
				out["asset"] = assetView
			}
			return jsonResult(out)
		})
}

// lookupCVEAsset resolves an asset in task scope when task context exists; otherwise
// falls back to the global asset store. Used by lookup_cves.
func (t *ToolSet) lookupCVEAsset(raw json.RawMessage) []*db.Asset {
	id := pid(raw)
	if id <= 0 {
		return nil
	}
	var assets []*db.Asset
	var err error
	if t.taskID > 0 {
		assets, err = t.as.GetByIDsInScope(t.taskID, []int64{id})
	} else {
		assets, err = t.as.GetByIDs([]int64{id})
	}
	if err != nil {
		return nil
	}
	return assets
}

// cveLines 是常驻上下文索引里的一行式 CVE 线索（最多 2 行）。
func (t *ToolSet) cveLines() []string {
	if t.as == nil || t.taskID <= 0 {
		return nil
	}
	matches, err := t.as.MatchCVEsForTask(t.taskID, 2)
	if err != nil || len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, renderCVEMatchLine(m))
	}
	return out
}

func renderCVEMatchLine(m db.CVEMatch) string {
	version := map[string]string{
		"compatible": "版本匹配", "version_unknown": "版本未知",
		"unconstrained": "无版本约束", "product_unknown": "产品未确认",
	}[m.VersionMatch]
	if version == "" {
		version = m.VersionMatch
	}
	script := "无脚本"
	if m.HasExploit {
		script = "有脚本"
	}
	line := fmt.Sprintf("[%s] %s（%s · %s · %s）", m.CVEID, clipKnowledge(m.Title, 80), m.Severity, version, script)
	if s := strings.TrimSpace(m.Description); s != "" {
		line += " — " + clipKnowledge(s, 120)
	}
	if m.AssetLabel != "" {
		line += " · 资产 " + m.AssetLabel
	}
	return line
}

// cveExploit 按 CVE 库里的脚本实际触发。真正的安全门在 guard（工具调用前）：
// 这里再做资产范围/target/版本/匹配校验，然后交给 execution service。
func (t *ToolSet) cveExploit() actool.CoreTool {
	return writeTool("cve_exploit",
		"用 CVE 库里保存的脚本实际触发一次利用。\n"+
			"★ 平台会在执行前走现有授权范围、禁止规则、作业时间窗与拦截审批；不会绕过。\n"+
			"★ 只返回输出/execution_id，**不会自动产生 finding 或据点**；真实可复现的输出由你自己用 report_finding/report_foothold 登记。\n"+
			"★ CVE/版本/指纹匹配不是证据；先 lookup_cves 确认，再在这里显式指定 cve_id/asset_id/target。\n"+
			"★ 脚本从 BLADE_TARGET 环境变量读取目标；额外参数按 argv 追加。",
		obj(map[string]any{
			"cve_id":          str("CVE 编号（必填，优先 lookup_cves 确认）"),
			"asset_id":        idp("要攻击的资产 id（必填，必须在本任务范围内）"),
			"target":          str("目标 URL/host:port（必填；用于 RoE 提取，必须匹配资产）"),
			"args":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "可选：按顺序追加给脚本的 argv 参数（最多 16 个）"},
			"timeout_ms":      intp("同步等待毫秒数，默认 120000，上限 600000；超时返回 execution_id 供继续查询"),
			"hard_timeout_ms": intp("可选硬超时毫秒（到点杀进程组）；省略=平台默认最大存活时长"),
			"description":     str("可选：这次执行的一句话说明（进台账）"),
		}, "cve_id", "asset_id", "target"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("cve_exploit 未启用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("cve_exploit 需要任务上下文：没有任务范围就无法做授权校验"), nil
			}
			if t.execService == nil {
				return actool.Errorf("cve_exploit: 执行服务未接线，当前环境不能执行脚本"), nil
			}
			svc, ok := t.execService.(CVEScriptService)
			if !ok {
				return actool.Errorf("cve_exploit: 当前执行服务不支持脚本托管执行"), nil
			}
			if runtime.GOOS == "windows" {
				return actool.Errorf("cve_exploit 暂不支持 Windows（脚本命令使用 POSIX shell 引号）"), nil
			}
			var a struct {
				CVEID         string          `json:"cve_id"`
				AssetID       json.RawMessage `json:"asset_id"`
				Target        string          `json:"target"`
				Args          []string        `json:"args"`
				TimeoutMs     int             `json:"timeout_ms"`
				HardTimeoutMs int             `json:"hard_timeout_ms"`
				Description   string          `json:"description"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			cveID := db.NormalizeCVEID(a.CVEID)
			if cveID == "" {
				return actool.Errorf("cve_exploit: cve_id 必填"), nil
			}
			assetID := pid(a.AssetID)
			if assetID <= 0 {
				return actool.Errorf("cve_exploit: asset_id 必填"), nil
			}
			target := strings.TrimSpace(a.Target)
			if target == "" {
				return actool.Errorf("cve_exploit: target 必填（用于授权范围校验）"), nil
			}
			if err := validateCVETarget(target); err != nil {
				return actool.Errorf("cve_exploit: " + err.Error()), nil
			}
			c, err := t.as.GetCVEByCVEID(cveID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if c == nil {
				return actool.Errorf(fmt.Sprintf("cve_exploit: CVE 不存在: %s", cveID)), nil
			}
			if c.Status != db.CVEStatusActive {
				return cveCannotExploit(c, nil, "该 CVE 条目已废弃，不能执行脚本")
			}
			if !c.HasExploit || strings.TrimSpace(c.ExploitScript) == "" {
				return cveCannotExploit(c, nil, "该 CVE 没有保存利用脚本；请按 probe_guide 手工验证")
			}
			if db.NormalizeExploitLanguage(c.ExploitLanguage) == "" {
				return cveCannotExploit(c, nil, "脚本语言不受支持")
			}
			assets := t.lookupCVEAsset(a.AssetID)
			if len(assets) == 0 {
				return actool.Errorf(fmt.Sprintf("cve_exploit: 资产 %d 不在本任务范围内或不存在", assetID)), nil
			}
			if !targetMatchesAsset(assets[0], target) {
				return cveCannotExploit(c, nil, fmt.Sprintf("target %q 不属于资产 %s，拒绝执行", target, assetLabel(assets[0])))
			}
			m, err := t.as.MatchCVEForAsset(t.taskID, c.CVEID, assetID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if m == nil || m.Score <= 0 {
				return cveCannotExploit(c, m, "该 CVE 与资产指纹/产品不匹配；先 lookup_cves 确认，不要拿错 CVE 的脚本硬打")
			}
			if m.VersionMatch == "mismatch" {
				return cveCannotExploit(c, m, "目标版本不在受影响范围内；不要用版本不匹配的脚本，先按 probe_guide 手工验证")
			}
			if err := validateCVEArgs(a.Args); err != nil {
				return actool.Errorf("cve_exploit: " + err.Error()), nil
			}
			scriptName := "cve-" + strings.ToLower(c.CVEID) + "-" + shortSHA(c.ExploitSHA256) + db.ExploitScriptExt(c.ExploitLanguage)
			path, err := svc.StageScript(ctx, t.taskIDStr(), t.ownerNode, scriptName, []byte(c.ExploitScript))
			if err != nil {
				return actool.Errorf("cve_exploit: 落盘脚本失败: " + err.Error()), nil
			}
			command, err := buildCVECommand(c.CVEID, c.ExploitLanguage, path, target, a.Args)
			if err != nil {
				return actool.Errorf("cve_exploit: " + err.Error()), nil
			}
			waitTimeout := time.Duration(a.TimeoutMs) * time.Millisecond
			if waitTimeout <= 0 {
				waitTimeout = 120 * time.Second
			}
			if waitTimeout > 10*time.Minute {
				waitTimeout = 10 * time.Minute
			}
			hardTimeout := time.Duration(a.HardTimeoutMs) * time.Millisecond
			desc := strings.TrimSpace(a.Description)
			if desc == "" {
				desc = "CVE 利用: " + c.CVEID + " → " + target
			}
			st, err := svc.SubmitTool(ctx, t.taskIDStr(), t.ownerNode, t.worker, "cve_exploit", command, desc, hardTimeout)
			if err != nil {
				return actool.Errorf("cve_exploit: " + err.Error()), nil
			}
			t.as.RecordCVEExecution(c.ID)
			final := st
			var waitErr string
			if got, werr := t.execService.Wait(ctx, st.ID, waitTimeout); werr != nil {
				waitErr = werr.Error()
			} else {
				final = got
			}
			timedOut := ExecActive(final.Status)
			out := map[string]any{
				"cve_id": c.CVEID, "asset_id": assetID, "target": target,
				"execution_id": st.ID, "status": final.Status, "exit_code": final.ExitCode,
				"timed_out": timedOut, "output_head": final.Head, "partial_tail": final.Tail,
				"output_path": final.OutputPath, "output_bytes": final.OutputBytes,
				"script_sha256": c.ExploitSHA256, "version_match": m.VersionMatch,
				"match_reasons": m.MatchReasons,
				"note":          "执行只返回输出/execution_id，不自动产生 finding 或据点；真实可复现输出才用 report_finding/report_foothold 登记。",
			}
			if waitErr != "" {
				out["wait_error"] = waitErr
			}
			if m.VersionMatch == "version_unknown" {
				out["warning"] = "资产版本未知，执行前请确认版本确实在受影响范围；匹配本身不是证据。"
			}
			if timedOut {
				out["next"] = fmt.Sprintf("仍在运行：用 wait_execution(%d, timeout_ms) 或 get_execution(%d) 继续查看", st.ID, st.ID)
			}
			return jsonResult(out)
		})
}

func cveCannotExploit(c *db.CVE, m *db.CVEMatch, reason string) (actool.Result, error) {
	out := map[string]any{
		"available": false, "cve_id": c.CVEID, "reason": reason,
		"probe_guide": clipKnowledge(c.ProbeGuide, 6000),
		"has_exploit": c.HasExploit, "note": cveLookupNote,
	}
	if m != nil {
		out["score"] = m.Score
		out["version_match"] = m.VersionMatch
		out["match_reasons"] = m.MatchReasons
		out["asset_version"] = m.AssetVersion
		out["matched_product"] = m.MatchedProduct
	}
	return jsonResult(out)
}

func shortSHA(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "nosha"
	}
	return s
}

func validateCVETarget(target string) error {
	if len([]rune(target)) > 2048 {
		return fmt.Errorf("target 过长")
	}
	for _, r := range target {
		if r == 0 || r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return fmt.Errorf("target 含控制字符")
		}
	}
	return nil
}

func validateCVEArgs(args []string) error {
	if len(args) > 16 {
		return fmt.Errorf("args 最多 16 个")
	}
	total := 0
	for _, a := range args {
		if len([]rune(a)) > 512 {
			return fmt.Errorf("单个 arg 不能超过 512 字符")
		}
		for _, r := range a {
			if r == 0 || r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
				return fmt.Errorf("arg 含控制字符")
			}
		}
		total += len([]rune(a))
	}
	if total > 4096 {
		return fmt.Errorf("args 总长度不能超过 4096 字符")
	}
	return nil
}

func assetLabel(a *db.Asset) string {
	switch {
	case a.URL != "":
		return a.URL
	case a.Domain != "":
		return a.Domain
	case a.IP != "":
		return a.IP
	default:
		return fmt.Sprintf("asset#%d", a.ID)
	}
}

func targetMatchesAsset(a *db.Asset, target string) bool {
	host, port, ok := targetHostPort(target)
	if !ok {
		return false
	}
	allowed := map[string]bool{}
	addHost := func(raw string) {
		if raw == "" {
			return
		}
		h, _, ok := targetHostPort(raw)
		if !ok {
			h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		}
		if h != "" {
			allowed[h] = true
		}
	}
	addHost(a.URL)
	addHost(a.Domain)
	addHost(a.IP)
	for _, d := range a.BoundDomains {
		addHost(d)
	}
	if !allowed[host] {
		return false
	}
	if port > 0 && a.Port != nil && *a.Port > 0 && port != *a.Port {
		return false
	}
	return true
}

func targetHostPort(raw string) (host string, port int, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		// 裸 host:port
		u, err = url.Parse("http://" + raw)
	}
	if err != nil || u.Hostname() == "" {
		return "", 0, false
	}
	host = strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	if host == "" {
		return "", 0, false
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return host, port, true
}

func buildCVECommand(cveID, language, scriptPath, target string, args []string) (string, error) {
	lang := db.NormalizeExploitLanguage(language)
	interpreter := ""
	switch lang {
	case "python3":
		interpreter = "python3 -u"
	case "bash":
		interpreter = "bash"
	case "sh":
		interpreter = "sh"
	case "ruby":
		interpreter = "ruby"
	case "perl":
		interpreter = "perl"
	case "php":
		interpreter = "php"
	case "node":
		interpreter = "node"
	case "pwsh":
		interpreter = "pwsh -NoProfile -File"
	default:
		return "", fmt.Errorf("不支持的脚本语言: %s", language)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "BLADE_CVE_ID=%s BLADE_TARGET=%s %s %s",
		shellSingleQuote(cveID), shellSingleQuote(target), interpreter, shellSingleQuote(scriptPath))
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(shellSingleQuote(a))
	}
	command := b.String()
	if len([]rune(command)) > 8000 {
		return "", fmt.Errorf("生成的命令过长（>8000 字符）：减少 args")
	}
	return command, nil
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
