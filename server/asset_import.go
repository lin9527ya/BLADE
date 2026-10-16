package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/lin9527ya/blade/db"
)

// 资产 CSV 导入：把一份目标清单批量登记成资产。
//
// 契约照 P18 的「先预览再应用」：解析器只有一份，两条路径共用——预览只读、应用才写。
// 预览在这里不是礼节：无表头的文件按**单元格形状**自动判类，认错了必须在落库前被看见，
// 所以预览把每一条的判定结果逐条列出来，供人核对之后才应用。
//
// 幂等：四种目标（根域 / 子域 / IP / HTTP 服务）都走既有的 Upsert*，全是
// ON CONFLICT DO UPDATE 的合并写——同一份清单导入两次不会产生重复行。`host:port`
// 刻意不在其中：UpsertOtherService 要求 service_name，而 CSV 里无从得知（拿占位符顶替
// 会污染它的 (domain,ip,port,service_name) 去重键），所以只按主机导入并如实注明端口未登记。

const (
	maxAssetImportBodyBytes = 4 << 20
	maxAssetImportTargets   = 20000
	assetImportSampleLimit  = 20
)

// utf8BOM 是 Excel 导出 CSV 时会放在文件头的字节序标记，解析前先剥掉。
const utf8BOM = "\ufeff"

// 表头别名。命中其一才认定首行是表头，且**只取命中的列**——有表头就意味着人已经说了
// 哪列是什么，此时再按形状猜其余列，只会把 type/port/category 这类列也当目标吞进来。
var (
	csvHostHeader = map[string]bool{
		"domain": true, "host": true, "hostname": true, "fqdn": true, "target": true,
		"域名": true, "主机": true, "主机名": true, "目标": true,
	}
	csvIPHeader = map[string]bool{
		"ip": true, "ipv4": true, "ipv6": true, "地址": true, "ip地址": true,
	}
	csvURLHeader = map[string]bool{
		"url": true, "uri": true, "link": true, "网址": true, "链接": true,
	}
)

// assetTypeWords 是 assets.type 的全部取值。无表头时这些词只可能来自某一列的类型标注
// （type 列），不该当成目标——否则一行 `example.com,subdomain` 会把 "subdomain"
// 也登记成一条资产。
var assetTypeWords = map[string]bool{
	"root_domain": true, "subdomain": true, "ip": true,
	"app": true, "service": true, "endpoint": true,
}

// reHostname 域名字符集（放行下划线：SRV/TXT 记录里常见）。用 Unicode 类别而不是
// [A-Za-z0-9]，免得中文域名被当成非法——本仓的 DomainKey 不转 punycode，存进去的就是原样。
// 纯数字串由 hasHostLetter 判掉。
var reHostname = regexp.MustCompile(`^[\p{L}\p{N}_]([\p{L}\p{N}_-]*[\p{L}\p{N}_])?(\.[\p{L}\p{N}_]([\p{L}\p{N}_-]*[\p{L}\p{N}_])?)*$`)

// assetImportItem 是一条已分类、已归一化、已定位到源文件行的候选值。
type assetImportItem struct {
	Line  int    `json:"line"`
	Value string `json:"value"`
	Type  string `json:"type"`
	// key 是存在性检查用的自然键，write 是真正落库的值。都不对外——对外只需要
	// 行号 / 原文 / 判定类型。
	key   string
	write string
}

// assetImportNote 是一条进不去、或进去了但有话要说的行。如实报出（含行号与原因），不静默丢。
type assetImportNote struct {
	Line   int    `json:"line"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// assetImportParse 是一次解析的全部产出。
type assetImportParse struct {
	Items    []assetImportItem
	Skipped  []assetImportNote // 没能进候选的（含原因）
	Warnings []assetImportNote // 进了候选但打了折扣的（目前只有 host:port 的端口未登记）
	Dupes    int               // 表内重复、被折掉的条数
	ParseErr string            // 读不下去时的错误（此时后面的行没解析）
}

// ---------- POST /api/assets/import/preview ----------
// ---------- POST /api/assets/import ----------

// assetImportPlan 是一次导入的完整计划。两条路径共用同一份构造，保证「预览里说会怎样」
// 与「应用时真的怎样」出自同一段代码。
type assetImportPlan struct {
	Items    []assetImportItem
	Skipped  []assetImportNote
	Warnings []assetImportNote
	Dupes    int
	ParseErr string
	Presence db.AssetKeyPresence
	TaskID   int64
}

// existing 报告一条候选是否已在库。
//
// 查的是 **it.write 而不是 it.Value**：write 才是真正落库的那个值（摘过端口、归过一），
// 而存在性查询只能拿它去比。拿原文比会在 host:port 这类输入上永远查不到——库里存的是
// 摘掉端口的主机名，原文 `www.host:8443` 怎么归一化都变不出它，于是每次导入都误报「新增」。
func (p *assetImportPlan) existing(it assetImportItem) bool {
	switch it.Type {
	case "ip":
		return p.Presence.IPs[it.write]
	case "service":
		return p.Presence.URLs[it.write]
	default:
		return p.Presence.Domains[it.write]
	}
}

func (p *assetImportPlan) byType() map[string]int {
	out := map[string]int{}
	for _, it := range p.Items {
		out[it.Type]++
	}
	return out
}

// buildAssetImportPlan 读请求、解析、分类、查库存在性。失败时已写好响应，返回 ok=false。
func (s *Server) buildAssetImportPlan(w http.ResponseWriter, r *http.Request) (*db.AssetStore, *assetImportPlan, bool) {
	as := s.assetStore()
	if as == nil {
		writeErr(w, 503, "database unavailable")
		return nil, nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAssetImportBodyBytes)
	var req struct {
		CSV    string `json:"csv"`
		TaskID int64  `json:"task_id"`
	}
	if err := decode(r, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, 400, "invalid JSON: "+err.Error())
		}
		return nil, nil, false
	}
	if strings.TrimSpace(req.CSV) == "" {
		writeErr(w, 400, "csv 不能为空")
		return nil, nil, false
	}
	// 任务先确认存在：给错 task_id 时，Upsert 会照样把不存在的 id 写进 task_ids 数组，
	// 而 task_asset_links 的 JOIN 只是静默不建行——「导入说成功、任务里什么都没有」比报错难查得多。
	if req.TaskID > 0 {
		task, err := s.m.pg.GetTask(req.TaskID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return nil, nil, false
		}
		if task == nil {
			writeErr(w, 404, "任务不存在")
			return nil, nil, false
		}
	}

	parsed := parseAssetCSV(req.CSV)
	if len(parsed.Items) > maxAssetImportTargets {
		parsed.Skipped = append(parsed.Skipped, assetImportNote{
			Line:   parsed.Items[maxAssetImportTargets].Line,
			Value:  "",
			Reason: fmt.Sprintf("单次最多导入 %d 条，其余未解析（请分批）", maxAssetImportTargets),
		})
		parsed.Items = parsed.Items[:maxAssetImportTargets]
	}

	// 存在性查询按类型分组，只查真有的那几类。传的是 it.write（落库值）而不是原文——
	// 键必须与库里的那一列对得上，理由见 assetImportPlan.existing。
	var domains, ips, urls []string
	for _, it := range parsed.Items {
		switch it.Type {
		case "ip":
			ips = append(ips, it.write)
		case "service":
			urls = append(urls, it.write)
		default:
			domains = append(domains, it.write)
		}
	}
	presence, err := as.ExistingAssetKeys(domains, ips, urls)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, nil, false
	}

	return as, &assetImportPlan{
		Items:    parsed.Items,
		Skipped:  parsed.Skipped,
		Warnings: parsed.Warnings,
		Dupes:    parsed.Dupes,
		ParseErr: parsed.ParseErr,
		Presence: presence,
		TaskID:   req.TaskID,
	}, true
}

// assetImportPreview 只读：把「会导入什么」说清楚，一个字节都不写。
func (s *Server) assetImportPreview(w http.ResponseWriter, r *http.Request) {
	_, plan, ok := s.buildAssetImportPlan(w, r)
	if !ok {
		return
	}
	existing := 0
	for _, it := range plan.Items {
		if plan.existing(it) {
			existing++
		}
	}
	samples := plan.Items
	if len(samples) > assetImportSampleLimit {
		samples = samples[:assetImportSampleLimit]
	}
	type sample struct {
		Line     int    `json:"line"`
		Value    string `json:"value"`
		Type     string `json:"type"`
		Existing bool   `json:"existing"`
	}
	out := make([]sample, 0, len(samples))
	for _, it := range samples {
		out = append(out, sample{Line: it.Line, Value: it.Value, Type: it.Type, Existing: plan.existing(it)})
	}
	resp := map[string]any{
		"total":        len(plan.Items),
		"by_type":      plan.byType(),
		"new":          len(plan.Items) - existing,
		"existing":     existing,
		"duplicates":   plan.Dupes,
		"unrecognized": plan.Skipped,
		"warnings":     plan.Warnings,
		"samples":      out,
		"task_id":      plan.TaskID,
	}
	if plan.ParseErr != "" {
		resp["parse_error"] = plan.ParseErr
	}
	if len(plan.Items) > len(out) {
		resp["samples_truncated"] = len(plan.Items) - len(out)
	}
	writeJSON(w, 200, resp)
}

// assetImportApply 落库。逐条 Upsert，单条失败不连坐——与 POST /api/assets 同一口径。
//
// **刻意不包事务**：本仓此前量过资产批量入库，包一个大事务只快 1.7×（1000 条省 0.8 秒），
// 却要为此引入 SAVEPOINT（否则一条坏数据回滚整批），并恶化跨任务的 company scope 锁争用。
// 导入是一次性动作、不在热路径上，不值得把这个复杂度请回来。
func (s *Server) assetImportApply(w http.ResponseWriter, r *http.Request) {
	as, plan, ok := s.buildAssetImportPlan(w, r)
	if !ok {
		return
	}
	created, merged := 0, 0
	type errEntry struct {
		Line  int    `json:"line"`
		Value string `json:"value"`
		Error string `json:"error"`
	}
	var errs []errEntry

	for _, it := range plan.Items {
		var id int64
		var err error
		switch it.Type {
		case "root_domain":
			id, err = as.UpsertRootDomain(db.UpsertRootDomainReq{Domain: it.write, TaskID: plan.TaskID})
		case "subdomain":
			id, err = as.UpsertSubdomain(db.UpsertSubdomainReq{Domain: it.write, TaskID: plan.TaskID})
		case "ip":
			id, err = as.UpsertIP(db.UpsertIPReq{IP: it.write, TaskID: plan.TaskID})
		case "service":
			id, err = as.UpsertHTTPService(db.UpsertHTTPServiceReq{URL: it.write, TaskID: plan.TaskID})
		default:
			err = fmt.Errorf("未知类型 %s", it.Type)
		}
		if err != nil {
			errs = append(errs, errEntry{Line: it.Line, Value: it.Value, Error: err.Error()})
			continue
		}
		if plan.TaskID > 0 {
			// 来源如实标注：任务页的「测试资产」按来源分组，混进 "api" 会让「这批是怎么来的」查不清。
			_ = as.SetTaskAssetSource(plan.TaskID, id, "csv", "通过 CSV 导入登记", nil)
		}
		if plan.existing(it) {
			merged++
		} else {
			created++
		}
	}

	resp := map[string]any{
		"created":      created,
		"merged":       merged,
		"by_type":      plan.byType(),
		"unrecognized": plan.Skipped,
		"warnings":     plan.Warnings,
		"task_id":      plan.TaskID,
	}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	if plan.ParseErr != "" {
		resp["parse_error"] = plan.ParseErr
	}
	writeJSON(w, 200, resp)
}

// ---------- 解析与分类 ----------

// parseAssetCSV 把一份 CSV 目标清单解析成候选值。
//
// 契约（刻意保守，因为导入会写库）：
//   - 首行命中表头别名 → 认定是表头，此后**只取命中列**，并按列的类型强制判类；
//   - 没有表头 → 首行也是数据，逐单元格按形状判类；
//   - 空单元格正常（跳过、不报）；纯数字、类型词、认不出的形状都进 Skipped 报出来。
func parseAssetCSV(raw string) assetImportParse {
	var out assetImportParse
	text := strings.TrimPrefix(raw, utf8BOM) // Excel 导出的 UTF-8 BOM
	rd := csv.NewReader(strings.NewReader(text))
	rd.FieldsPerRecord = -1 // 参差的行照样收：按列取需要的值，不因一列缺失整行作废
	rd.TrimLeadingSpace = true
	rd.LazyQuotes = true
	rd.Comment = '#' // 允许以 # 开头的注释行

	seen := map[string]bool{}
	process := func(line int, cell, hint string) {
		value := strings.TrimSpace(cell)
		out2 := classifyTarget(value, hint)
		if !out2.ok {
			if out2.reason != "" { // 空单元格没有 reason：正常情况，不报
				out.Skipped = append(out.Skipped, assetImportNote{Line: line, Value: value, Reason: out2.reason})
			}
			return
		}
		// 存在性检查的键是**原文**，所以去重也按原文+类型——同一目标写两遍（大小写不同）
		// 会落成两行候选，但指示到同一条资产，Upsert 后仍是同一行；这里按自然键折掉。
		key := out2.typ + "\x00" + strings.ToLower(out2.key)
		if seen[key] {
			out.Dupes++
			return
		}
		seen[key] = true
		out.Items = append(out.Items, assetImportItem{
			Line: line, Value: value, Type: out2.typ, key: out2.key, write: out2.write,
		})
		if out2.note != "" {
			out.Warnings = append(out.Warnings, assetImportNote{Line: line, Value: value, Reason: out2.note})
		}
	}

	first, err := rd.Read()
	if err == io.EOF {
		return out // 空文件
	}
	if err != nil {
		out.ParseErr = err.Error()
		return out
	}
	var hints []string
	if face := headerHints(first); face != nil {
		hints = face
	} else {
		// 无表头：首行也是数据。
		for i, cell := range first {
			process(rowLine(rd, i), cell, "")
		}
	}

	for {
		rec, rerr := rd.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// 如实报出「从哪一行起没解析」，而不是静默截断。
			out.ParseErr = rerr.Error()
			break
		}
		for i, cell := range rec {
			hint := ""
			if hints != nil {
				if i >= len(hints) || hints[i] == "" {
					continue // 有表头：没被点名的列不碰
				}
				hint = hints[i]
			}
			process(rowLine(rd, i), cell, hint)
		}
	}
	return out
}

// rowLine 取当前记录里第 i 个字段所在的源文件行号（列从 1 数）。FieldPos 越界会 panic，
// 所以先挡住；挡不住时退回 0（只影响提示里的行号，不影响导入本身）。
func rowLine(rd *csv.Reader, i int) int {
	if i < 0 {
		return 0
	}
	line, _ := rd.FieldPos(i)
	return line
}

// headerHints 判定首行是不是表头。是则返回「每列是什么」（没命中的列给 ""），不是则返回 nil。
//
// 认定条件是**任一**单元格命中别名：要求全部命中会漏掉 `domain,port,备注` 这类混列；
// 而只要有别名命中就几乎不可能是数据行——真实的域名/IP/URL 不会长成 "domain" 这个词。
func headerHints(first []string) []string {
	hints := make([]string, len(first))
	found := false
	for i, cell := range first {
		c := strings.ToLower(strings.TrimSpace(cell))
		switch {
		case csvHostHeader[c]:
			hints[i], found = "host", true
		case csvIPHeader[c]:
			hints[i], found = "ip", true
		case csvURLHeader[c]:
			hints[i], found = "url", true
		}
	}
	if !found {
		return nil
	}
	return hints
}

// classifyOutcome 是一次分类的结果。ok=false 时 reason 说明为什么进不去（空串=不必报）；
// ok=true 时 note 非空表示「能进，但有话要说」。
type classifyOutcome struct {
	typ    string
	key    string
	write  string
	ok     bool
	reason string
	note   string
}

// classifyTarget 判定一个单元格是什么。hint 来自表头（"host"/"ip"/"url"/""），非空时按 hint
// 强制类型：有表头就是人已经说了这列是什么，此时不该再猜。
//
// 判定顺序即语义：URL → host:port → IP → 主机名。**IP 必须排在主机名之前**——db.RootDomain
// 对 IP 字面量返回 isApex=true（它的 best-effort 边界处理：分类不了的当自己的根），顺序错了
// 10.0.0.1 会被登记成根域。
func classifyTarget(cell, hint string) classifyOutcome {
	v := strings.TrimSpace(cell)
	if v == "" {
		return classifyOutcome{}
	}
	switch hint {
	case "host":
		return classifyHostOrPort(v)
	case "ip":
		if net.ParseIP(v) == nil {
			return classifyOutcome{reason: "ip 列的值不是 IPv4/IPv6 地址"}
		}
		return classifyOutcome{typ: "ip", key: db.IPKey(v), write: v, ok: true}
	case "url":
		if _, ok := parseImportURL(v); !ok {
			return classifyOutcome{reason: "url 列的值不是带主机的 http/https 地址"}
		}
		return classifyOutcome{typ: "service", key: v, write: v, ok: true}
	}

	if _, ok := parseImportURL(v); ok {
		return classifyOutcome{typ: "service", key: v, write: v, ok: true}
	}
	return classifyHostOrPort(v)
}

// classifyHostOrPort 摘掉 host:port 的端口再判主机，并把「端口没登记」如实说出来。
// IPv6 字面量的冒号不止一个，不会误进这一支。
//
// 表头点名 host 列时也走这里：`example.com:8443` 出现在 host 列是常事，不摘的话端口会被
// 并进域名存进去（DomainKey 不管端口），那条资产从此谁也匹配不上。
func classifyHostOrPort(v string) classifyOutcome {
	if strings.Count(v, ":") == 1 {
		if h, p, found := strings.Cut(v, ":"); found {
			if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
				out := classifyHost(h)
				if out.ok {
					out.note = fmt.Sprintf("按主机导入，端口 %d 未登记（要登记端口请写完整 URL）", n)
				}
				return out
			}
		}
	}
	return classifyHost(v)
}

// classifyHost 判定一个主机：顶级域 → root_domain，其余 → subdomain。
//
// 三项检查都照做，不因为「表头点名了 host 列」就免检——它们判的是「这能不能是一条目标」，
// 不是「它像不像目标」：字符集、纯数字（端口/计数）、类型词（type 列的值漏进来的）。
// 免检只会把 `subdomain` 这种标注当成域名存下来。
func classifyHost(v string) classifyOutcome {
	h := db.DomainKey(v) // lower + trim + 去末尾点
	if h == "" {
		return classifyOutcome{}
	}
	if ip := net.ParseIP(h); ip != nil {
		return classifyOutcome{typ: "ip", key: db.IPKey(h), write: h, ok: true}
	}
	switch {
	case !reHostname.MatchString(h):
		return classifyOutcome{reason: "不是域名 / IP / URL"}
	case assetTypeWords[h]:
		return classifyOutcome{reason: "是资产类型标注（type 列的取值），不是目标"}
	case !hasHostLetter(h):
		return classifyOutcome{reason: "是纯数字（端口或计数），不当作目标"}
	}
	root, isApex := db.RootDomain(h)
	if root == "" {
		return classifyOutcome{reason: "域名无法解析"}
	}
	// 子域的 value 写完整主机名：UpsertSubdomain 自己会推出根域并确保根域资产存在。
	if isApex {
		return classifyOutcome{typ: "root_domain", key: h, write: h, ok: true}
	}
	return classifyOutcome{typ: "subdomain", key: h, write: h, ok: true}
}

// parseImportURL 只接受 http/https 且必须带主机。其它 scheme（ftp://、file://）
// 落进 assets.url 没有意义，登记成 HTTP 服务更是错的。
func parseImportURL(v string) (string, bool) {
	if !strings.Contains(v, "://") {
		return "", false
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", false
	}
	if u.Hostname() == "" {
		return "", false
	}
	return v, true
}

// hasHostLetter 判「含字母」。纯数字串（端口、计数）不是主机名；按 Unicode 判，
// 中文域名同样是字母。
func hasHostLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
