package server

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
)

// ingest_scan_output：把已经落盘的扫描器结果确定性入库，避免模型把几万行原始
// 输出读进上下文。它复用 insert_assets / record_fact，所以资产去重、scope 归因、
// 事实节点与图谱写入口径与模型手工写回完全一致。
//
// 约束（与 BaaRai/ARTEX 的同类工具一致）：
//   - worker-only，由 agent 主动调用；Bash 输出绝不自动解析；
//   - path 必须落在本 run 的 WorkingDir 内（绝对路径/.. /软链越界一律拒绝）；
//   - 文件上限 256 MiB；JSONL 单行上限 64 KiB；单次最多 50k 资产+事实；
//   - 扫描器命中只记 fact（明确标为“未验证”），不自动 report_finding；
//   - 原始 banner/标题不进入 summary/notable，只作为 Go quote 后的 detail。

const (
	scanMaxFileBytes = 256 << 20
	scanMaxLineBytes = 64 << 10
	scanMaxItems     = 50000
	scanNotableMax   = 20
)

type scanIngestInput struct {
	Format string `json:"format"`
	Path   string `json:"path"`
}

type scanIngestResult struct {
	Format        string
	File          string
	Assets        []map[string]any
	Facts         []map[string]any
	Hosts         []string
	URLs          []string
	Ports         []string
	Skipped       int
	Oversized     int
	OverCap       int
	Truncated     bool
	AssetsWritten int
	FactsWritten  int
	Errors        []string
}

// summary is the only shape returned to the model: counters and a small notable
// sample, never the raw assets/facts arrays (which can hold 50k entries).
func (r *scanIngestResult) summary() map[string]any {
	return map[string]any{
		"format": r.Format, "file": r.File,
		"assets": r.AssetsWritten, "facts": r.FactsWritten,
		"skipped": r.Skipped, "oversized_lines": r.Oversized, "over_cap": r.OverCap,
		"truncated": r.Truncated, "errors": r.Errors,
		"notable": map[string]any{"hosts": r.Hosts, "urls": r.URLs, "ports": r.Ports},
	}
}

func (r *scanIngestResult) full() bool { return len(r.Assets)+len(r.Facts) >= scanMaxItems }

func (r *scanIngestResult) addAsset(a map[string]any) bool {
	if r.full() {
		r.OverCap++
		return false
	}
	r.Assets = append(r.Assets, a)
	return true
}

func (r *scanIngestResult) addFact(summary, detail, evidence string) bool {
	if r.full() {
		r.OverCap++
		return false
	}
	f := map[string]any{
		"summary":    summary,
		"detail":     detail,
		"evidence":   evidence,
		"confidence": "observed",
	}
	r.Facts = append(r.Facts, f)
	return true
}

func (r *scanIngestResult) noteHost(h string) {
	h = strings.TrimSpace(h)
	if h == "" || len(r.Hosts) >= scanNotableMax {
		return
	}
	for _, x := range r.Hosts {
		if x == h {
			return
		}
	}
	r.Hosts = append(r.Hosts, h)
}

func (r *scanIngestResult) noteURL(u string) {
	u = strings.TrimSpace(u)
	if u == "" || len(r.URLs) >= scanNotableMax {
		return
	}
	for _, x := range r.URLs {
		if x == u {
			return
		}
	}
	r.URLs = append(r.URLs, u)
}

func (r *scanIngestResult) notePort(p string) {
	p = strings.TrimSpace(p)
	if p == "" || len(r.Ports) >= scanNotableMax {
		return
	}
	for _, x := range r.Ports {
		if x == p {
			return
		}
	}
	r.Ports = append(r.Ports, p)
}

func (s *Server) scanIngestTools() []actool.CoreTool {
	return []actool.CoreTool{s.toolIngestScanOutput()}
}

func (s *Server) toolIngestScanOutput() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "ingest_scan_output",
		Description: "把**已经落盘的扫描器结果**确定性写入任务图，模型不需要读原始输出。\n" +
			"支持 format：nmap-xml、nuclei-jsonl、httpx-jsonl、ffuf-json。\n" +
			"先用 Bash 把扫描器输出写到文件（nmap -oX；nuclei -jsonl -o；httpx -json -o；ffuf -of json -o），" +
			"再调用本工具并传 path。工具会解析文件、用 insert_assets/record_fact 落库，只返回紧凑计数与少量 notable 项。\n" +
			"约束：path 必须在本次 run 的工作目录内；不上传/不解析任意远端内容；扫描器命中只记 fact（未验证），不自动生成 finding。",
		Schema: objSchema(map[string]any{
			"format": map[string]any{
				"type": "string",
				"enum": []string{"nmap-xml", "nuclei-jsonl", "httpx-jsonl", "ffuf-json"},
			},
			"path": strParam("扫描器输出文件路径；必须位于本次 run 的工作目录内"),
		}, "format", "path"),
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a scanIngestInput
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			res, err := s.ingestScanOutput(ctx, a, tc)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out, _ := json.Marshal(res.summary())
			return actool.Text(string(out)), nil
		},
	})
}

func (s *Server) ingestScanOutput(ctx context.Context, in scanIngestInput, tc *actool.ToolContext) (*scanIngestResult, error) {
	format := strings.ToLower(strings.TrimSpace(in.Format))
	switch format {
	case "nmap-xml", "nuclei-jsonl", "httpx-jsonl", "ffuf-json":
	default:
		return nil, fmt.Errorf("format 必须是 nmap-xml / nuclei-jsonl / httpx-jsonl / ffuf-json")
	}
	workDir := ""
	if tc != nil {
		workDir = tc.WorkingDir
	}
	path, err := resolveScanPath(workDir, in.Path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("读取扫描结果失败：%w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("path 不是普通文件")
	}
	if st.Size() > scanMaxFileBytes {
		return nil, fmt.Errorf("文件过大（%d 字节，上限 %d）：请分批处理", st.Size(), scanMaxFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	res := &scanIngestResult{Format: format, File: filepath.Base(path)}
	switch format {
	case "nmap-xml":
		err = parseNmapXML(io.LimitReader(f, scanMaxFileBytes), res)
	case "nuclei-jsonl":
		err = parseNucleiJSONL(f, res)
	case "httpx-jsonl":
		err = parseHTTPXJSONL(f, res)
	case "ffuf-json":
		err = parseFFUFJSON(f, res)
	}
	if err != nil {
		return nil, err
	}
	ts, err := s.scanIngestToolSet(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeScanAssets(ctx, ts, res); err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	if err := writeScanFacts(ctx, ts, res); err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	return res, nil
}

// resolveScanPath confines a scanner output path to the run's working directory.
// It rejects absolute/relative escapes and symlinks that resolve outside it.
func resolveScanPath(workDir, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("path 不能为空")
	}
	if strings.TrimSpace(workDir) == "" {
		return "", errors.New("当前 run 没有工作目录，无法安全读取扫描结果文件")
	}
	base, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	p := raw
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	inside := func(candidate string) bool {
		rel, err := filepath.Rel(base, candidate)
		if err != nil || filepath.IsAbs(rel) {
			return false
		}
		return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if !inside(p) {
		return "", fmt.Errorf("path 必须位于本次 run 的工作目录内：%s", workDir)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("解析路径失败：%w", err)
	}
	if !inside(resolved) {
		return "", fmt.Errorf("path 的软链目标越出了工作目录：%s", raw)
	}
	return resolved, nil
}

func (s *Server) scanIngestToolSet(ctx context.Context) (*agent.ToolSet, error) {
	ri := agent.RunInfoFrom(ctx)
	if ri.TaskID <= 0 || ri.ExplorationID <= 0 {
		return nil, fmt.Errorf("ingest_scan_output 要在任务 worker 上下文里使用（需要 task/exploration id）")
	}
	ts := agent.NewToolSet(s.m.pg.Exploration(ri.ExplorationID), "scan-ingest")
	ts.SetTaskID(ri.TaskID)
	ts.SetAssetStore(s.assetStore(), s.companyStore())
	return ts, nil
}

func findCoreTool(tools []actool.CoreTool, name string) actool.CoreTool {
	for _, t := range tools {
		if t.Name() == name {
			return t
		}
	}
	return nil
}

func writeScanAssets(ctx context.Context, ts *agent.ToolSet, res *scanIngestResult) error {
	if len(res.Assets) == 0 {
		return nil
	}
	tool := findCoreTool(ts.WorkerTools(), "insert_assets")
	if tool == nil {
		return errors.New("平台没有提供 insert_assets，无法写入扫描资产")
	}
	for start := 0; start < len(res.Assets); start += 500 {
		end := start + 500
		if end > len(res.Assets) {
			end = len(res.Assets)
		}
		payload, _ := json.Marshal(map[string]any{"assets": res.Assets[start:end]})
		out, err := tool.Call(ctx, payload, nil)
		if err != nil {
			return fmt.Errorf("insert_assets: %w", err)
		}
		if out.IsError {
			return fmt.Errorf("insert_assets: %s", out.Flatten())
		}
		var parsed struct {
			Results []any `json:"results"`
			Errors  []any `json:"errors"`
		}
		_ = json.Unmarshal([]byte(out.Flatten()), &parsed)
		res.AssetsWritten += len(parsed.Results)
		for _, e := range parsed.Errors {
			res.Errors = append(res.Errors, fmt.Sprint(e))
		}
	}
	return nil
}

func writeScanFacts(ctx context.Context, ts *agent.ToolSet, res *scanIngestResult) error {
	if len(res.Facts) == 0 {
		return nil
	}
	tool := findCoreTool(ts.WorkerTools(), "record_fact")
	if tool == nil {
		return errors.New("平台没有提供 record_fact，无法写入扫描事实")
	}
	for start := 0; start < len(res.Facts); start += 200 {
		end := start + 200
		if end > len(res.Facts) {
			end = len(res.Facts)
		}
		payload, _ := json.Marshal(map[string]any{"facts": res.Facts[start:end]})
		out, err := tool.Call(ctx, payload, nil)
		if err != nil {
			return fmt.Errorf("record_fact: %w", err)
		}
		if out.IsError {
			return fmt.Errorf("record_fact: %s", out.Flatten())
		}
		var parsed struct {
			IDs    []int64           `json:"ids"`
			Errors map[string]string `json:"errors"`
		}
		_ = json.Unmarshal([]byte(out.Flatten()), &parsed)
		res.FactsWritten += len(parsed.IDs)
		for id, e := range parsed.Errors {
			res.Errors = append(res.Errors, "fact["+id+"]: "+e)
		}
	}
	return nil
}

// --- nmap XML ---

type nmapHost struct {
	Status struct {
		State string `xml:"state,attr"`
	} `xml:"status"`
	Addresses []struct {
		Addr string `xml:"addr,attr"`
		Type string `xml:"addrtype,attr"`
	} `xml:"address"`
	Hostnames []struct {
		Name string `xml:"name,attr"`
		Type string `xml:"type,attr"`
	} `xml:"hostnames>hostname"`
	Ports []struct {
		Protocol string `xml:"protocol,attr"`
		PortID   int    `xml:"portid,attr"`
		State    struct {
			State string `xml:"state,attr"`
		} `xml:"state"`
		Service struct {
			Name    string `xml:"name,attr"`
			Product string `xml:"product,attr"`
			Version string `xml:"version,attr"`
		} `xml:"service"`
	} `xml:"ports>port"`
}

func parseNmapXML(r io.Reader, res *scanIngestResult) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("nmap XML 解析失败：%w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "host" {
			continue
		}
		var h nmapHost
		if err := dec.DecodeElement(&h, &se); err != nil {
			res.Skipped++
			continue
		}
		addNmapHost(res, h)
		if res.full() {
			res.Truncated = true
			return nil
		}
	}
}

func addNmapHost(res *scanIngestResult, h nmapHost) {
	if strings.ToLower(strings.TrimSpace(h.Status.State)) != "up" {
		return
	}
	var ip string
	var names []string
	for _, a := range h.Addresses {
		switch strings.ToLower(a.Type) {
		case "ipv4", "ipv6":
			if ip == "" {
				ip = a.Addr
			}
		}
	}
	for _, hn := range h.Hostnames {
		if strings.EqualFold(hn.Type, "user") && strings.TrimSpace(hn.Name) != "" {
			names = append(names, strings.TrimSpace(hn.Name))
		}
	}
	if ip == "" && len(names) > 0 {
		ip = names[0]
	}
	if ip == "" {
		res.Skipped++
		return
	}
	res.noteHost(ip)
	ports := make([]map[string]any, 0, len(h.Ports))
	for _, p := range h.Ports {
		if !strings.EqualFold(strings.TrimSpace(p.State.State), "open") {
			continue
		}
		svc := strings.TrimSpace(p.Service.Name)
		portLabel := fmt.Sprintf("%d/%s", p.PortID, svc)
		res.notePort(portLabel)
		entry := map[string]any{"port": p.PortID}
		if svc != "" {
			entry["service"] = svc
		}
		ports = append(ports, entry)
		if isHTTPPort(p.PortID, svc) {
			scheme := "http"
			if p.PortID == 443 || p.PortID == 8443 || strings.Contains(strings.ToLower(svc), "https") || strings.Contains(strings.ToLower(svc), "ssl") {
				scheme = "https"
			}
			u := scheme + "://" + net.JoinHostPort(ip, strconv.Itoa(p.PortID))
			res.noteURL(u)
			res.addAsset(map[string]any{
				"type": "service", "url": u, "service_name": "http",
				"port": p.PortID, "service_ip": ip,
			})
		} else if svc != "" {
			res.addAsset(map[string]any{
				"type": "service", "service_name": svc, "port": p.PortID,
				"ip": ip,
			})
		}
	}
	if len(ports) > 0 {
		res.addAsset(map[string]any{"type": "ip", "ip": ip, "open_ports": ports, "bound_domains": names})
	}
	detail := scanDetail(map[string]any{"host": ip, "hostnames": names, "ports": ports})
	res.addFact(fmt.Sprintf("nmap：%s 开放 %d 个端口（扫描器结果，未验证）", ip, len(ports)), detail, "nmap -oX")
}

func isHTTPPort(port int, svc string) bool {
	if strings.Contains(strings.ToLower(svc), "http") {
		return true
	}
	switch port {
	case 80, 81, 88, 443, 444, 8000, 8008, 8080, 8081, 8443, 8888, 9000:
		return true
	}
	return false
}

// --- JSONL scanners ---

type nucleiLine struct {
	TemplateID string `json:"template-id"`
	Info       struct {
		Name     string `json:"name"`
		Severity string `json:"severity"`
	} `json:"info"`
	Host             string   `json:"host"`
	MatchedAt        string   `json:"matched-at"`
	MatcherName      string   `json:"matcher-name"`
	ExtractedResults []string `json:"extracted-results"`
}

func parseNucleiJSONL(r io.Reader, res *scanIngestResult) error {
	return readJSONLines(r, res, func(line []byte) {
		var n nucleiLine
		if json.Unmarshal(line, &n) != nil {
			res.Skipped++
			return
		}
		target := strings.TrimSpace(n.Host)
		if target == "" {
			target = strings.TrimSpace(n.MatchedAt)
		}
		if target == "" {
			res.Skipped++
			return
		}
		if u, err := url.Parse(target); err == nil && u.Host != "" && u.Scheme != "" {
			res.noteURL(u.String())
			res.addAsset(scanHTTPAsset(u, nil, "", nil, ""))
		} else if net.ParseIP(target) != nil {
			res.noteHost(target)
			res.addAsset(map[string]any{"type": "ip", "ip": target})
		} else {
			res.noteHost(target)
			res.addAsset(map[string]any{"type": "subdomain", "domain": target})
		}
		detail := scanDetail(map[string]any{
			"template": n.TemplateID, "name": n.Info.Name, "severity": n.Info.Severity,
			"host": n.Host, "matched_at": n.MatchedAt, "matcher": n.MatcherName,
			"extracted": n.ExtractedResults,
		})
		res.addFact(fmt.Sprintf("nuclei 命中（扫描器结果，未验证）：%s [%s] %s @ %s",
			n.TemplateID, n.Info.Severity, n.Info.Name, target), detail, "nuclei -jsonl")
	})
}

type httpxLine struct {
	URL           string          `json:"url"`
	StatusCode    int             `json:"status_code"`
	Title         string          `json:"title"`
	Tech          json.RawMessage `json:"tech"`
	WebServer     string          `json:"webserver"`
	ContentLength int64           `json:"content_length"`
	Host          string          `json:"host"`
	IP            string          `json:"ip"`
	TLS           json.RawMessage `json:"tls"`
}

func parseHTTPXJSONL(r io.Reader, res *scanIngestResult) error {
	return readJSONLines(r, res, func(line []byte) {
		var h httpxLine
		if json.Unmarshal(line, &h) != nil || strings.TrimSpace(h.URL) == "" {
			res.Skipped++
			return
		}
		u, err := url.Parse(h.URL)
		if err != nil || u.Host == "" {
			res.Skipped++
			return
		}
		res.noteURL(u.String())
		res.noteHost(u.Hostname())
		status := h.StatusCode
		length := h.ContentLength
		tech := jsonStringList(h.Tech)
		res.addAsset(scanHTTPAsset(u, &status, h.Title, &length, h.IP, tech...))
		res.addFact(fmt.Sprintf("httpx：%s 返回 %d（扫描器结果，未验证）", u.String(), status),
			scanDetail(map[string]any{
				"url": h.URL, "status_code": h.StatusCode, "title": h.Title, "tech": tech,
				"webserver": h.WebServer, "content_length": h.ContentLength, "host": h.Host, "ip": h.IP,
				"tls": string(h.TLS),
			}), "httpx -json")
	})
}

type ffufResult struct {
	Input  string `json:"input"`
	Status int    `json:"status"`
	Length int64  `json:"length"`
	Words  int    `json:"words"`
	Lines  int    `json:"lines"`
	URL    string `json:"url"`
}

func parseFFUFJSON(r io.Reader, res *scanIngestResult) error {
	var doc struct {
		Results []ffufResult `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(r, scanMaxFileBytes)).Decode(&doc); err != nil {
		return fmt.Errorf("ffuf JSON 解析失败：%w", err)
	}
	for _, x := range doc.Results {
		if res.full() {
			res.Truncated = true
			return nil
		}
		target := strings.TrimSpace(x.URL)
		if target == "" {
			target = strings.TrimSpace(x.Input)
		}
		if target == "" {
			res.Skipped++
			continue
		}
		if u, err := url.Parse(target); err == nil && u.Host != "" && u.Scheme != "" {
			res.noteURL(u.String())
			status := x.Status
			length := x.Length
			res.addAsset(scanHTTPAsset(u, &status, "", &length, ""))
		}
		res.addFact(fmt.Sprintf("ffuf：%s 返回 %d（扫描器结果，未验证）", target, x.Status),
			scanDetail(map[string]any{"url": x.URL, "input": x.Input, "status": x.Status,
				"length": x.Length, "words": x.Words, "lines": x.Lines}), "ffuf -of json")
	}
	return nil
}

func readJSONLines(r io.Reader, res *scanIngestResult, fn func([]byte)) error {
	br := bufio.NewReaderSize(r, scanMaxLineBytes)
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// Discard the rest of this oversized line without allocating it whole.
			res.Oversized++
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = br.ReadSlice('\n')
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}
		if len(line) > 0 {
			fn(line)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func scanHTTPAsset(u *url.URL, status *int, title string, length *int64, ip string, tech ...string) map[string]any {
	a := map[string]any{"type": "service", "url": u.String(), "service_name": "http"}
	if u.Port() != "" {
		if p, err := strconv.Atoi(u.Port()); err == nil {
			a["port"] = p
		}
	}
	if ip != "" {
		a["service_ip"] = ip
	}
	if status != nil {
		a["status_code"] = *status
	}
	if length != nil && *length > 0 {
		a["content_length"] = *length
	}
	if strings.TrimSpace(title) != "" {
		a["page_title"] = title
	}
	if len(tech) > 0 {
		a["technologies"] = tech
	}
	return a
}

func jsonStringList(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []string{s}
	}
	return nil
}

func scanDetail(v map[string]any) string {
	b, _ := json.Marshal(v)
	return strconv.Quote(string(b))
}
