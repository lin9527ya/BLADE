package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// assetInterceptCandidates 提取一条待插入资产输入项的 域名/IP/URL 候选串，用于资产拦截匹配。
// URL 的 host 会拆出归类，使「只带 URL」的服务/端点资产也能被 域名/IP 规则命中。
func assetInterceptCandidates(item assetInputItem) (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, item.Domain)
	for _, d := range item.BoundDomains {
		add(&domains, d)
	}
	add(&ips, item.IP)
	add(&ips, item.ServiceIP)
	add(&urls, item.URL)
	if item.URL != "" {
		if u, err := url.Parse(item.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// assetInputLabel 返回一条待插入资产的简短标识，用于拦截说明消息。
func assetInputLabel(item assetInputItem) string {
	typ := strings.TrimSpace(item.Type)
	var target string
	switch {
	case strings.TrimSpace(item.Domain) != "":
		target = strings.TrimSpace(item.Domain)
	case strings.TrimSpace(item.URL) != "":
		target = strings.TrimSpace(item.URL)
	case strings.TrimSpace(item.IP) != "":
		target = strings.TrimSpace(item.IP)
	case strings.TrimSpace(item.ServiceIP) != "":
		target = strings.TrimSpace(item.ServiceIP)
	default:
		target = "(未知)"
	}
	if typ != "" {
		return fmt.Sprintf("[%s] %s", typ, target)
	}
	return target
}

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	CompanyID   *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceName   string           `json:"service_name"`
	ServiceIP     string           `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port  int    `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"批量登记新发现的资产，一次可混合多种类型（type 见枚举）。\n"+
			"各类型必填字段：root_domain→domain；ip→ip（须为 IPv4/IPv6，非主机名）；subdomain→domain；app→app_name；service(HTTP)→url；service(非HTTP)→service_name+port（ip/domain 至少填一个）；endpoint→url+method。其余字段含义见各自说明。\n"+
			"auth/technologies/params 为追加合并(append)，不覆盖原值。\n"+
			"返回：{results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			// task_id 不暴露给模型：worker 归属哪个 task 由程序经 SetTaskID 权威赋值(见 handler)。
			"assets": map[string]any{
				"type":        "array",
				"description": "资产数组，每个元素对应一条资产记录",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "资产类型",
					},
					// root_domain / subdomain
					"domain":      str("根域名或子域名（root_domain/subdomain 必填）"),
					"icp":         str("ICP 备案号（可选）"),
					"record_type": str("DNS 解析类型：A/AAAA/CNAME/MX 等（subdomain 可选）"),
					"record_value": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "DNS 解析值列表（subdomain 可选，如 [\"1.2.3.4\",\"2.3.4.5\"]）",
					},
					// ip
					"ip": str("IP 地址，必须是 IPv4/IPv6 地址，不能填主机名（主机名请用 type=subdomain 的 domain 字段）；ip 类型必填；service/endpoint 类型可填，用于关联 IP"),
					"bound_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "该 IP 绑定的域名列表（ip 类型可选）",
					},
					"open_ports": map[string]any{
						"type":        "array",
						"description": "开放端口列表（ip 类型可选）",
						"items": obj(map[string]any{
							"port":    intp("端口号"),
							"service": str("服务名称，如 http/ssh/mysql 等（可选）"),
						}, "port"),
					},
					// app
					"app_name":    str("应用名称（app 类型必填）"),
					"bundle_id":   str("Bundle ID（app 类型可选）"),
					"category":    str("应用分类（可选）"),
					"description": str("应用描述（可选）"),
					"app_icp":     str("应用 ICP 备案（可选）"),
					"company_id":  intp("归属企业 id（app 类型可选；app 无法靠 scope 自动归因，需显式指定。id 由 add_company_scope 返回）"),
					// service (http)
					"url":         str("完整 URL，含协议和端口（HTTP 服务必填；service_type 自动设为 http）"),
					"status_code": intp("HTTP 响应状态码，如 200/301/403/404（可选）"),
					"content_length": map[string]any{
						"type":        "integer",
						"description": "HTTP 响应体字节数（可选）",
					},
					"page_title":   str("页面 <title> 内容（可选）"),
					"favicon_mmh3": str("favicon MMH3 哈希（可选）"),
					"technologies": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "指纹/技术栈列表，如 [\"Nginx\",\"Vue\",\"Bootstrap\"]（可选）",
					},
					"auth": map[string]any{
						"type":        "array",
						"description": "发现的认证信息列表，每条含 type/username/password 等字段（可选，追加不覆盖）",
						"items":       map[string]any{"type": "object"},
					},
					// service (other，非 HTTP)
					"service_name": str("服务名称，如 ssh/mysql/redis（service 非 HTTP 时必填）"),
					"port":         intp("端口号（service 非 HTTP 时必填）"),
					// endpoint
					"method": str("HTTP 方法：GET/POST/PUT/PATCH/DELETE 等（endpoint 必填）"),
					"params": map[string]any{
						"type":        "array",
						"description": "请求参数列表，每条含 location(query/body/header/path)/name/value/type（可选，追加不覆盖）",
						"items":       map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("insert_assets 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				Assets []assetInputItem `json:"assets"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			// task_id 由程序权威赋值(worker: SetTaskID)，不接受模型传入——避免模型漏传/错传
			// 导致资产未归任务或归错任务。无任务上下文的调用方(auto/pentest/chat)其 t.taskID=0。
			taskID := t.taskID

			type result struct {
				Index int    `json:"index"`
				ID    int64  `json:"id"`
				Type  string `json:"type"`
			}
			type errEntry struct {
				Index int    `json:"index"`
				Error string `json:"error"`
			}

			var results []result
			var errs []errEntry

			// 资产闸门规则一次性载入；读取失败则跳过判定（不阻断插入）。
			// 拦截规则 = 全局 ∪ 任务级 block；允许规则 = 任务级 allow。
			blockRules, _ := t.as.ListAssetInterceptRules()
			var allowRules []db.AssetInterceptRule
			if t.taskID > 0 {
				if tb, ta, err := t.as.TaskInterceptRulesSplit(t.taskID); err == nil {
					blockRules = append(blockRules, tb...)
					allowRules = ta
				}
			}

			for i, item := range a.Assets {
				// 资产闸门：先拦截后允许，被拒的资产禁止插入（跳过 Upsert 及后续副作用）。
				domains, ips, urls := assetInterceptCandidates(item)
				if d := db.EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
					errs = append(errs, errEntry{
						Index: i,
						Error: fmt.Sprintf("资产 %s %s，已禁止插入", assetInputLabel(item), d.Reason),
					})
					continue
				}

				typ := strings.TrimSpace(item.Type)
				var id int64
				var err error

				switch typ {
				case "root_domain":
					id, err = t.as.UpsertRootDomain(db.UpsertRootDomainReq{
						Domain: item.Domain,
						ICP:    item.ICP,
						TaskID: taskID,
					})

				case "ip":
					id, err = t.as.UpsertIP(db.UpsertIPReq{
						IP:           item.IP,
						BoundDomains: item.BoundDomains,
						OpenPorts:    item.OpenPorts,
						TaskID:       taskID,
					})

				case "subdomain":
					id, err = t.as.UpsertSubdomain(db.UpsertSubdomainReq{
						Domain:      item.Domain,
						RecordType:  item.RecordType,
						RecordValue: item.RecordValue,
						ICP:         item.ICP,
						TaskID:      taskID,
					})

				case "app":
					id, err = t.as.UpsertApp(db.UpsertAppReq{
						Name:        item.AppName,
						BundleID:    item.BundleID,
						Category:    item.Category,
						Description: item.Description,
						ICP:         item.AppICP,
						CompanyID:   item.CompanyID,
						TaskID:      taskID,
					})

				case "service":
					// distinguish HTTP vs other by presence of url
					if item.URL != "" {
						// agent may send "ip" or "service_ip" for the enrichment IP; accept both
						svcIP := item.ServiceIP
						if svcIP == "" {
							svcIP = item.IP
						}
						id, err = t.as.UpsertHTTPService(db.UpsertHTTPServiceReq{
							URL:           item.URL,
							Technologies:  item.Technologies,
							StatusCode:    item.StatusCode,
							ContentLength: item.ContentLength,
							PageTitle:     item.PageTitle,
							FaviconMMH3:   item.FaviconMMH3,
							Auth:          item.Auth,
							IP:            svcIP,
							TaskID:        taskID,
						})
					} else {
						id, err = t.as.UpsertOtherService(db.UpsertOtherServiceReq{
							Domain:      item.Domain,
							IP:          item.IP,
							Port:        item.Port,
							ServiceName: item.ServiceName,
							Auth:        item.Auth,
							TaskID:      taskID,
						})
					}

				case "endpoint":
					id, err = t.as.UpsertEndpoint(db.UpsertEndpointReq{
						URL:    item.URL,
						Method: item.Method,
						Params: item.Params,
						IP:     item.ServiceIP,
						TaskID: taskID,
					})

				default:
					errs = append(errs, errEntry{Index: i, Error: "unknown type: " + typ})
					continue
				}

				if err != nil {
					errs = append(errs, errEntry{Index: i, Error: err.Error()})
					continue
				}
				results = append(results, result{Index: i, ID: id, Type: typ})
				t.writes.Assets++
				t.anchorOwner(id)
				if taskID > 0 {
					var sourceNodeID *int64
					if t.ownerNode > 0 {
						nodeID := t.ownerNode
						sourceNodeID = &nodeID
					}
					summary := "Agent 通过 insert_assets 登记"
					if t.ownerNode > 0 {
						summary = fmt.Sprintf("Worker 意图 #%d 通过 insert_assets 登记", t.ownerNode)
					}
					if err := t.as.SetTaskAssetSource(taskID, id, "agent", summary, sourceNodeID); err != nil {
						log.Printf("[insert_assets] 标注来源失败 task=%d asset=%d: %v（资产已建立，只是来源缺失）", taskID, id, err)
					}
				}
				// 自动入测试范围(source='auto')：只对 worker 顶层显式插入的这一项，按其
				// 类型加保守范围；side-effect 派生的资产不经此处，故范围不盲目扩大。taskID=0 时无操作。
				// 与覆盖度开关无关：task_scope 是任务的范围边界(list/查询的过滤基准)，
				// 覆盖度开关只决定要不要把它当分母去算指标，不决定要不要累积范围本身。
				{
					svcIP := item.ServiceIP
					if svcIP == "" {
						svcIP = item.IP
					}
					if err := t.as.AddAutoScope(taskID, typ, item.Domain, item.URL, svcIP); err != nil {
						log.Printf("[insert_assets] 自动入范围失败 task=%d type=%s: %v（范围边界可能少一条）", taskID, typ, err)
					}
				}
			}

			return jsonResult(map[string]any{
				"results": results,
				"errors":  errs,
			})
		},
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"把域名/IP/CIDR/ICP备案/企业关键词加入某公司的【资产范围】——域名、网络和ICP会自动认领命中的资产，关键词只提供给Agent作为范围提示。\n"+
			"公司名唯一：company 不存在则新建，已存在则复用(只把范围并进去)。\n"+
			"scope 一行一条，系统自动识别：根域名 / URL / 单个 IP / CIDR 网段 / ICP备案 / 企业关键词。\n"+
			"务必给 reason 说明归属依据(whois/证书/ASN 等)。\n"+
			"护栏：拒绝裸 TLD 与过宽网段(IPv4前缀需为/16-/32、IPv6前缀需为/32-/128)，非法行会被跳过并在 errors 返回。",
		obj(map[string]any{
			"company": str("公司名(不存在则新建、存在则复用；名称唯一)"),
			"scope":   str("资产范围，一行一条：域名 / URL / IP / CIDR / ICP备案 / 企业关键词"),
			"reason":  str("归属依据(证据/来源)，务必填写"),
			"logo":    str("公司图标 URL(可选；仅新建公司时生效)"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope 未启用: CompanyStore 未初始化"), nil
			}
			var a struct {
				Company string `json:"company"`
				Scope   string `json:"scope"`
				Reason  string `json:"reason"`
				Logo    string `json:"logo"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Company) == "" {
				return actool.Errorf("company 不能为空"), nil
			}
			companyID, _, err := t.cs.UpsertCompany(a.Company, a.Logo)
			if err != nil {
				return actool.Errorf("创建/获取公司失败: " + err.Error()), nil
			}
			lines := splitLines(a.Scope)
			added, skipped, invalid, errMsgs := t.cs.AddScope(companyID, lines, a.Reason)
			out := map[string]any{
				"company_id": companyID,
				"added":      added,
				"skipped":    skipped,
				"invalid":    invalid,
			}
			if len(errMsgs) > 0 {
				out["errors"] = errMsgs
			}
			return jsonResult(out)
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"把测试范围加入【本任务】——这是本任务的授权边界，也是资产测试覆盖度的分母。\n"+
			"kind 支持：company(整个公司名下资产) / root_domain(整个根域，含所有子域) / subdomain(单个精确子域) / ip / cidr / icp / keyword。\n"+
			"说明：worker 逐个碰到的主机会被系统【自动】加进范围(精确子域)；本工具用于【主动扩大】——把整个根域/整个公司纳入，或补充指定某子域/IP。\n"+
			"value：company 传公司名或 id(公司须已存在)；root_domain/subdomain 传域名；ip/cidr 传 IP 或网段；icp/keyword 传备案号或企业关键词。\n"+
			"务必给 reason 说明依据(可审计)。多条用 entries 数组。",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "批量：[{kind, value}]。kind∈company/root_domain/subdomain/ip/cidr/icp/keyword。", "items": map[string]any{"type": "object"}},
			"kind":    str("[单条] company / root_domain / subdomain / ip / cidr / icp / keyword"),
			"value":   str("[单条] 公司名或id / 域名 / IP / CIDR / ICP / 关键词"),
			"reason":  str("加入依据(用于审计)，务必填写"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope 未启用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope 需要任务上下文(当前无 task)"), nil
			}
			type scopeEntry struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries    []scopeEntry `json:"entries"`
				scopeEntry              // 单条模式
				Reason     string       `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items := a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs := map[string]string{}
			for i, e := range items {
				ts, err := t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason, "agent")
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "value": ts.Value, "company_id": ts.CompanyID})
			}
			out := map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// coverageMatrix 让规划者按需拉「范围内资产 × 风险类」覆盖矩阵：每个资产的已测/
// 未测成/进行中/范围外计数、事实证据数与意图点名；未出现的风险类即未触及。
func (t *ToolSet) coverageMatrix() actool.CoreTool {
	return readTool(
		"coverage_matrix",
		"查询本任务「覆盖矩阵」：范围内资产(task_scope) × 风险类(intent 的 risk_area) → "+
			"已测(done/negative)/未测成(blocked/exhausted)/进行中/范围外 计数 + 该资产锚定的事实证据数。"+
			"未出现的风险类即【未触及】（对照 risk_areas 全集）。规划补测方向前先调它。仅任务上下文可用。",
		obj(map[string]any{}),
		func(_ context.Context, _ json.RawMessage) (actool.Result, error) {
			if t.ts == nil || t.taskID <= 0 {
				return actool.Errorf("coverage_matrix 需要任务上下文"), nil
			}
			assets, err := t.ts.CoverageMatrix(t.taskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"risk_areas": db.RiskAreas, "assets": assets})
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"查询【本任务及直接关联任务】范围内、还没被事实锚点覆盖的资产（关联范围只读，供你自己判断要不要补测，不代替你决策）。\n"+
			"可选按资产类型过滤：root_domain/subdomain/service/app/endpoint/ip。\n"+
			"分页：page 从 1 起、page_size 默认 10。返回 {assets:[{id,type,label}], total, page, page_size}。仅任务上下文可用。",
		obj(map[string]any{
			"type":      str("资产类型过滤（可选）：root_domain/subdomain/service/app/endpoint/ip"),
			"page":      intp("页码，从 1 起（默认 1）"),
			"page_size": intp("每页数量（默认 10）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets 未启用: AssetStore 未初始化"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets 需要任务上下文"), nil
			}
			var a struct {
				Type     string `json:"type"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Page <= 0 {
				a.Page = 1
			}
			if a.PageSize <= 0 {
				a.PageSize = 10
			}
			offset := (a.Page - 1) * a.PageSize
			assets, total, err := t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"查询资产库：DSL 表达式搜索，或按 id/ids 直取；支持分页。只返回【本任务及直接关联任务】测试范围内的资产。\n"+
			"DSL：field=value 模糊(ILIKE) | field==value 精确 | field!=value 排除 | 数字字段支持 > >= < <= | 裸词=全文模糊；AND/OR 组合(AND 优先级高)，可用括号分组。资产类型用独立 type 参数，不写进 DSL。\n"+
			"未传 id/ids 时 dsl 必须非空（不允许无条件全量查询）。\n"+
			"可用字段：domain(根/子/服务域名)、root_domain、ip、url、page_title、icp、service_name、app_name、method(如 GET/POST)、service_type(http|other)、record_type(如 A/CNAME)、technology(数组，=模糊 ==精确)、port/status_code/company_id(整数)。\n"+
			"示例：status_code>=400 AND technology=shiro ；(port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl":    str(`DSL 查询表达式（语法/字段见工具描述）。未传 id/ids 时必须非空。`),
			"type":   str("资产类型过滤：root_domain|ip|subdomain|app|service|endpoint（独立字段，可与 dsl 叠加；单独 type 不足以查询，仍需 dsl）"),
			"id":     intp("直接按单个资产 id 取（可选，与 dsl/type 互斥）"),
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "直接按多个资产 id 取（可选，与 dsl/type 互斥）"},
			"limit":  intp("返回上限，默认 10（可选）"),
			"offset": intp("分页偏移，默认 0（可选）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				DSL    string  `json:"dsl"`
				Type   string  `json:"type"`
				ID     int64   `json:"id"`
				IDs    []int64 `json:"ids"`
				Limit  int     `json:"limit"`
				Offset int     `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}

			var assets []*db.Asset
			var err error
			switch {
			case a.ID > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, []int64{a.ID})
			case len(a.IDs) > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, a.IDs)
			case a.DSL != "":
				assets, err = t.as.QueryDSLInScope(a.DSL, a.Type, t.taskID, a.Limit, a.Offset)
			default:
				return actool.Errorf("未传 id/ids 时 dsl 不能为空：不允许无条件查询全部资产，请提供查询条件"), nil
			}
			if err != nil {
				return actool.Errorf("DSL 错误: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"count":  len(assets),
				"assets": assets,
			})
		},
	)
}

// listCompanies lets an agent enumerate companies (企业) with their scope + asset count.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"列出资产库中的【企业/公司】及其资产范围(scope)与已归属资产数。用于查看有哪些公司、"+
			"拿到 company_id（insert_assets 关联 app、list_assets 按 company_id 过滤时用）。"+
			"可选 search 按公司名模糊过滤(不区分大小写)，留空返回全部。",
		obj(map[string]any{
			"search": str("按公司名模糊过滤(可选，不区分大小写)；留空返回全部"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies 未启用: CompanyStore 未初始化"), nil
			}
			var a struct {
				Search string `json:"search"`
			}
			_ = json.Unmarshal(in, &a)
			cos, err := t.cs.ListCompanies()
			if err != nil {
				return actool.Errorf("查询公司失败: " + err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Search))
			type companyOut struct {
				ID         int64    `json:"id"`
				Name       string   `json:"name"`
				AssetCount int      `json:"asset_count"`
				Scope      []string `json:"scope"`
			}
			out := make([]companyOut, 0, len(cos))
			for _, c := range cos {
				if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
					continue
				}
				scope := make([]string, 0, len(c.Scope))
				for _, r := range c.Scope {
					scope = append(scope, r.Raw)
				}
				out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
			}
			return jsonResult(map[string]any{"count": len(out), "companies": out})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		// list_findings 保留：报漏洞前先查本任务已确认漏洞，避免重复上报同一漏洞。
		t.listFindings(),
		t.addFinding(), t.closeCandidate(), t.recordFact(),
		// 战果：拿到据点/凭据是「往深打穿」的产物，必须由产出它的 worker 当场记下来，
		// 否则规划层看不见这条通路，下一轮从外面重打一遍。
		t.reportFoothold(), t.reportCredential(), t.reportDetection(),
		// 遗留物（P43）：我们留在目标上的东西。它跟其它战果是反的——那些记"拿到了什么"，
		// 这个记"给客户留下了什么"。没清掉的 webshell 是留在客户系统上的后门，
		// 而交付时也要能说清我们动过什么。
		t.reportArtifact(), t.markArtifactCleared(),
		// verify_credential（P36）：拿凭据真的登进去了，就把「这条目标上验证过」补上——
		// 重复 report 会多出一条凭据节点，图上就出现两条一样的凭据。
		t.verifyCredential(),
		// asset management (handlers guard nil store internally)。
		// add_company_scope 不给 worker：定义企业资产范围属规划/主控/Auto 的职责，worker 只执行探索。
		t.insertAssets(), t.listAssets(), t.assetNeighbors(),
		// 指纹打法库：动手前先查同类指纹的历史打法（lookup 会累加使用次数），
		// 用后回报 worked/failed，卡片靠它越用越准。沉淀本身归 playbook agent。
		t.lookupPlaybooks(), t.recordPlaybookFeedback(),
		// conclude_intent：worker 对领到的意图声明语义终态（negative / out-of-scope）。
		// 没测成（blocked/exhausted）由引擎判定，不归本工具。
		t.concludeIntent(),
		// 跨 work 回看：worker 也可复用其他 work 的观察，避免重复劳动。
		// search_all_worker_traces：不必先知道 intent_id，按关键字全局捞命中步骤；
		// get_worker_trace：锁定某条 work 后列步骤/就地搜/取完整内容。
		t.searchAllWorkerTraces(), t.getWorkerTrace(),
		// node_detail：worker 拿到 intent_id/节点 id 后可查该节点完整详情（配合上面的回看）。
		t.nodeDetail(),
		// ad_paths（P32）：手上已经有域内凭据/会话时，读「从这到 DA/域控怎么走」——
		// 域内横向的下一步该由正在那台机器上的 worker 决定，而不是等规划层猜。
		t.adPaths(),
		// opsec_notes（P38）：动手前查这一手**预计**会留下什么（事前；实测结论走 report_detection_check）。
		t.opsecNotes(),
		// P13 长任务执行：进程活过本次 run、输出落盘、状态进台账。未接线（nil 服务）
		// 时工具返回可读拒绝，绑定可见性与目录 seed 不受影响。
		t.runExecution(), t.waitExecution(), t.getExecution(), t.grepExecution(), t.lootExec(), t.lootUpload(), t.lootDownload(), t.footholdExec(), t.tunnelOpen(), t.cancelExecution(),
		// OAST（带外回调）：盲打类漏洞的唯一确认手段；未配置时由 server 从工具面移除。
		t.oastMint(), t.oastPoll(),
		// 以下工具仍【不给】worker，只留给 planner/main（读上下文、跨 work 复盘是规划职责，
		// worker 只做单条意图的执行与写回）：list_facts / list_companies / list_worker_traces。
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.expandDigest(), // cold-digest §6.1
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// steer_work：人可对某条正在运行的意图(work)实时注入纠偏指令（不打断、不丢进展）。
		t.steerWorkTool(),
		// set_goals：人可在运行时给本任务补一个新的最终目标（规划者据此重判是否达成）。
		t.setGoals(),
		// set_constraints：人可在运行时给本任务补/改操作约束（allow/deny），约束 planner/worker 的探索边界。
		t.setConstraints(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(), t.assetNeighbors(),
		t.addFinding(), t.closeCandidate(), t.recordFact(),
		// 战果：人机对话里也能补记——人在跑任务时拿到的据点/凭据同样要进图，才算数。
		// 遗留物同理：人在现场手动落的东西，也该有人能替它销账。
		t.reportFoothold(), t.reportCredential(), t.reportDetection(),
		t.reportArtifact(), t.markArtifactCleared(),
		// verify_credential（P36）：拿凭据真的登进去了，就把「这条目标上验证过」补上——
		// 重复 report 会多出一条凭据节点，图上就出现两条一样的凭据。
		t.verifyCredential(),
		// promote_knowledge（P23）：人机对话里也可把结论沉淀给公司（只读背景，供别的任务用）。
		t.promoteKnowledge(),
		// lookup_playbooks：人机对话里按资产/指纹查历史打法（只读）。
		t.lookupPlaybooks(),
		t.addTaskScope(),
		// list_untested_assets：按需查本任务范围内未测资产(类型+分页)，自行决定补测。
		t.listUntestedAssets(),
		// coverage_matrix：按需拉「范围内资产 × 风险类」覆盖矩阵（已测/未测成/进行中/
		// 范围外 + 事实证据数），规划补测方向前先看它。
		t.coverageMatrix(),
		// ad_paths（P32）：手上已有域内凭据/会话时，读「从这到 DA/域控怎么走」（来自导入的采集结果）。
		t.adPaths(),
		// opsec_notes（P38）：人机对话里问"这一手会留下什么"是常见需求（事前；实测走 report_detection_check）。
		t.opsecNotes(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen := map[string]bool{}
	var out []actool.CoreTool
	all := append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool := range all {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}
