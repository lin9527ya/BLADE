package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lin9527ya/artex/db"
)

// AD 攻击路径导入（P32）：把 BloodHound / SharpHound 的采集 JSON 落进任务的探索图。
//
// 为什么不另建一套 AD 模型：平台的攻击路径视图读的就是 exploration_edges（见
// server/attack_chains.go 的「不引入新模型」）。域内路径要能被同一套视图、同一份报告读出来，
// 数据就得在同一张图里。所以 AD 对象是**一种新的节点 kind（ad_object）+ 四种新边**，
// 不是第二个图、也不是另一套 API。
//
// 契约照 P18/P26 的「先预览再应用」：解析与映射只有一份（buildADPlan），预览只读、应用才写，
// 「预览说会导入什么」与「应用真的导入了什么」因此出自同一段代码。
//
// 幂等：节点身份是 SID（payload.sid）——同 sid 复用节点并刷新属性（改名、启用/禁用都会变，
// SID 不会）；边走 Link / LinkAttrs 的 ON CONFLICT。同一份采集导两次，节点数与边数都不变。
//
// 采集结果是**分文件的**（users/groups/computers/domains/ous/gpos），用户常常只导其中几个。
// 因此判类按对象自身的特征、不靠文件名；指向未采集对象的边**留边不丢**——用 DN 或知名 RID
// 造明确标记的 stub 节点（payload.stub=true），而不是静默丢掉一条本该存在的路。
const (
	maxADImportBodyBytes = 32 << 20
	maxADImportObjects   = 200000
	adSampleLimit        = 20
)

// ---------- 计划模型 ----------

// adNode 是一个待落库的 AD 对象。stub=true 表示它不是采集来的，而是由一条边反推出来的
// （DN 指向未采集的组、SID 是知名 RID、域信任指向未采集的域）——照实标记，读的人要知道
// 「这个节点的属性是空的，因为没人采过它」。
type adNode struct {
	SID           string
	Name          string
	Category      string // user/group/computer/domain/ou/gpo
	Domain        string
	SAM           string
	DN            string
	OS            string
	Desc          string
	LastLogon     string
	Enabled       *bool // 来源没这个字段时留 nil——不要替它编一个
	HighValue     bool
	AdminCount    bool
	HasSPN        bool
	ASREP         bool
	Unconstrained bool
	Stub          bool
	StubReason    string
	Tags          []string
}

func (n *adNode) payload() map[string]any {
	m := map[string]any{
		"category": n.Category,
		"name":     n.Name,
	}
	if n.SID != "" {
		m["sid"] = n.SID
	}
	for k, v := range map[string]string{
		"domain": n.Domain, "sam": n.SAM, "dn": n.DN, "os": n.OS,
		"description": n.Desc, "lastlogon": n.LastLogon,
	} {
		if v != "" {
			m[k] = v
		}
	}
	if n.Enabled != nil {
		m["enabled"] = *n.Enabled
	}
	for k, v := range map[string]bool{
		"highvalue": n.HighValue, "admincount": n.AdminCount, "hasspn": n.HasSPN,
		"asrep": n.ASREP, "unconstrained": n.Unconstrained,
	} {
		if v {
			m[k] = true
		}
	}
	if len(n.Tags) > 0 {
		m["tags"] = n.Tags
	}
	if n.Stub {
		m["stub"] = true
		if n.StubReason != "" {
			m["stub_reason"] = n.StubReason
		}
	}
	return m
}

// adEdge 是一条待落库的边。两端都是 SID（stub 用 dn:/rid: 前缀的合成键，见 plan 里的 Nodes）。
type adEdge struct {
	From   string
	Rel    string
	To     string
	Rights []string // 仅 can 边
}

// adPlan 是一次导入的完整计划——预览与应用共用。
type adPlan struct {
	Nodes map[string]*adNode // 键：SID（stub 为合成键）
	Edges map[string]*adEdge // 键：from|rel|to（can 边的 rights 在此并集）
	// 指向的对象没采到、连 SID/DN 都没有（既不是知名 RID 也认不出名字）时，边只能丢——
	// 如实计数，不静默。
	UnresolvedEdges int
	UnknownObjects  int
	// 未收录的权限名 → 条数。权限名是开放集合，白名单外的（ReadProperty 之类噪声）不建边，
	// 但要让人看得见"跳过了什么"，而不是以为导全了。
	SkippedRights map[string]int
	// GPO 对象只登记节点、不建模继承链（GPO --链接--> OU --包含--> 机器），本轮不做，
	// 有 GPO 时在预览里说清楚。
	GPONodes int
	// domainSID 是本域 SID 前缀（S-1-5-21-a-b-c），从采到的任一对象推出来。它让「DN 指向的组」
	// 能还原成 SID 键：CN=Domain Admins 的 DN 与 ACE 里的 S-1-5-21-…-512 是**同一个组**，
	// 两个键会让图上出现两个 Domain Admins。
	domainSID string
	Warnings  []string
}

func newADPlan() *adPlan {
	return &adPlan{
		Nodes:         map[string]*adNode{},
		Edges:         map[string]*adEdge{},
		SkippedRights: map[string]int{},
	}
}

func (p *adPlan) warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

// node 取（或建）一个节点。同名 SID 复用——后采到的属性覆盖先前的，但 stub 不会覆盖真节点。
func (p *adPlan) node(n *adNode) *adNode {
	if n.SID == "" {
		p.UnknownObjects++
		return nil
	}
	if old, ok := p.Nodes[n.SID]; ok {
		if !n.Stub {
			p.Nodes[n.SID] = n
			return n
		}
		return old
	}
	p.Nodes[n.SID] = n
	return n
}

func (p *adPlan) edge(from, rel, to string, rights ...string) {
	if from == "" || to == "" || from == to {
		return
	}
	key := from + "|" + rel + "|" + to
	if e, ok := p.Edges[key]; ok {
		e.Rights = mergeRights(e.Rights, rights)
		return
	}
	p.Edges[key] = &adEdge{From: from, Rel: rel, To: to, Rights: mergeRights(nil, rights)}
}

func mergeRights(dst []string, add []string) []string {
	seen := map[string]bool{}
	for _, r := range dst {
		seen[r] = true
	}
	for _, r := range add {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		dst = append(dst, r)
	}
	sort.Strings(dst)
	return dst
}

// ---------- 权限白名单 ----------

// adRights 是**建边**的权限白名单。它只收「拿了这个能往下走」的权限；ReadProperty /
// WriteProperty 这类读属性、写普通字段的 ACE 在本仓不建边——图上会瞬间被噪声淹没，
// 而路径要回答的是"怎么到他"，不是"他和谁有过一条 ACE"。跳过的名字进 SkippedRights 如实报出。
var adRights = map[string]bool{
	"GenericAll": true, "GenericWrite": true, "WriteDacl": true, "WriteOwner": true,
	// Owner / Owns 是同一语义的两个拼法（BloodHound 各版本都用过），两个都收。
	"Owner": true, "Owns": true,
	"ForceChangePassword": true, "AddMember": true, "AddSelf": true,
	"AllowedToDelegate": true, "AllowedToAct": true,
	"DCSync": true, "GetChanges": true, "GetChangesAll": true, "GetChangesInFilteredSet": true,
	"AllExtendedRights": true,
	"ReadLAPSPassword":  true, "ReadGMSAPassword": true, "SyncLAPSPassword": true,
	"AddKeyCredentialLink": true, "WriteAccountRestrictions": true,
	// AddSIDHistory：往自己的令牌里塞别人的 SID 历史——域内最彻底的一类接管，必须收。
	"AddSIDHistory": true, "WriteSPN": true,
	"CanRDP": true, "CanPSRemote": true, "ExecuteDCOM": true, "AllowedToAuthenticate": true,
}

// wellKnownRID 是域 SID 尾号 → 内置对象名。ACE 指向的 principal 常常没被采集（尤其是内置组，
// 采集器默认不展开），没有它，一条 "S-1-5-21-…-512 --admin_to--> DC" 就只能丢掉——而那恰恰是
// 最容易读懂的提权边。只收与路径相关的那些，不求全。
var wellKnownRID = map[string]string{
	"500": "Administrator", "502": "krbtgt",
	"512": "Domain Admins", "513": "Domain Users", "514": "Domain Guests",
	"515": "Domain Computers", "516": "Domain Controllers", "517": "Cert Publishers",
	"518": "Schema Admins", "519": "Enterprise Admins", "520": "Group Policy Creator Owners",
	"525": "Protected Users", "526": "Key Admins", "527": "Enterprise Key Admins",
	"571": "Allowed RODC Password Replication Group",
	"572": "Denied RODC Password Replication Group",
}

// wellKnownSID 是完整的知名 SID（本地/内置域），键为整串。
var wellKnownSID = map[string]string{
	"S-1-5-32-544": "BUILTIN\\Administrators",
	"S-1-5-32-548": "BUILTIN\\Account Operators",
	"S-1-5-32-549": "BUILTIN\\Server Operators",
	"S-1-5-32-550": "BUILTIN\\Print Operators",
	"S-1-5-32-551": "BUILTIN\\Backup Operators",
	"S-1-5-32-552": "BUILTIN\\Replicators",
	"S-1-5-32-555": "BUILTIN\\Remote Desktop Users",
	"S-1-5-32-562": "BUILTIN\\Distributed COM Users",
	"S-1-5-32-569": "BUILTIN\\Cryptographic Operators",
}

// ridByName 是 wellKnownRID 的反查（组名 → 尾号），用于把 DN 还原成 SID 键。
var ridByName = func() map[string]string {
	m := make(map[string]string, len(wellKnownRID))
	for rid, name := range wellKnownRID {
		m[strings.ToLower(name)] = rid
	}
	return m
}()

// adHighValueGroups 是「成员即高价值」的内置组名（小写）。判定用的是名字而不是 RID：
// 采集结果里组名可靠，而 RID 只在内置域里才有意义。
var adHighValueGroups = map[string]bool{
	"domain admins": true, "enterprise admins": true, "schema admins": true,
	"administrators": true, "domain controllers": true, "enterprise key admins": true,
	"key admins": true, "account operators": true, "backup operators": true,
	"print operators": true, "server operators": true, "cert publishers": true,
	"group policy creator owners": true,
}

// ---------- 解析 ----------

// normalizeSHResp 吃下三种形状：BloodHound 4.x 的裸数组、5.x/CE 的 {meta,data,license} 包装、
// 以及单对象文件。都不像就**报错**——绝不静默返回空（同 intel/fofa.go 的教训：
// 「解析失败」被读成「没有数据」，是最难查的一类假阴性）。
func normalizeSHResp(raw []byte) ([]map[string]any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("文件是空的")
	}
	var top any
	if err := json.Unmarshal(trimmed, &top); err != nil {
		return nil, fmt.Errorf("JSON 解析失败：%w", err)
	}
	switch v := top.(type) {
	case []any:
		return shObjects(v)
	case map[string]any:
		if data, ok := v["data"].([]any); ok {
			return shObjects(data)
		}
		if _, ok := v["Properties"]; ok {
			return []map[string]any{v}, nil
		}
		if _, ok := v["SID"]; ok {
			return []map[string]any{v}, nil
		}
		return nil, errors.New("不是 SharpHound 采集结果：顶层对象既没有 data 数组，也没有 SID/Properties")
	}
	return nil, fmt.Errorf("不是 SharpHound 采集结果：顶层是 %T", top)
}

func shObjects(in []any) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(in))
	for _, v := range in {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("文件里没有对象")
	}
	return out, nil
}

// ---------- 取值助手（4.x 的字段在顶层，5.x 的压在 Properties 里，两边都要吃）----------

func shProps(o map[string]any) map[string]any {
	if p, ok := o["Properties"].(map[string]any); ok {
		return p
	}
	return o
}

func shRaw(o map[string]any, key string) (any, bool) {
	if v, ok := o[key]; ok && v != nil {
		return v, true
	}
	if v, ok := shProps(o)[key]; ok && v != nil {
		return v, true
	}
	return nil, false
}

func shStr(o map[string]any, key string) string {
	if v, ok := shRaw(o, key); ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func shBool(o map[string]any, key string) (bool, bool) {
	v, ok := shRaw(o, key)
	if !ok {
		return false, false
	}
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		if t == "true" || t == "True" {
			return true, true
		}
		if t == "false" || t == "False" {
			return false, true
		}
	}
	return false, false
}

func shStrList(o map[string]any, key string) []string {
	v, ok := shRaw(o, key)
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// shPrincipals 读「一串 principal」的字段。SharpHound 里这类字段有两种形状：
// LocalAdmins.Results 那样带 {ObjectIdentifier,ObjectType} 的对象数组，和 AllowedToDelegate
// 那样的裸 SID 数组，两种都收。
func shPrincipals(o map[string]any, key string) []string {
	v, ok := shRaw(o, key)
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			switch e := e.(type) {
			case string:
				if s := strings.TrimSpace(e); s != "" {
					out = append(out, s)
				}
			case map[string]any:
				if s := shStr(e, "ObjectIdentifier"); s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case map[string]any:
		// LocalAdmins: {Collected:true,Results:[…]}
		return shPrincipals(t, "Results")
	}
	return nil
}

// shSessions 读 Sessions/PrivilegedSessions/RegistrySessions/HasSessions。两种形状：
//
//	{SID: ["COMPUTER"]}                 （4.x/5.x 的计算机对象：谁在这台机器上有会话）
//	[{ComputerSID:…,ComputerName:…}]    （部分导出的用户对象：这个人在哪些机器上有会话）
//
// 返回 SID → 机器名列表。
func shSessions(o map[string]any, key string) map[string][]string {
	v, ok := shRaw(o, key)
	if !ok {
		return nil
	}
	out := map[string][]string{}
	switch t := v.(type) {
	case map[string]any:
		for sid, names := range t {
			out[sid] = append(out[sid], shNameList(names)...)
		}
	case []any:
		for _, e := range t {
			switch e := e.(type) {
			case string:
				out[strings.TrimSpace(e)] = nil
			case map[string]any:
				sid := shStr(e, "ComputerSID")
				if sid == "" {
					sid = shStr(e, "ObjectIdentifier")
				}
				if sid != "" {
					out[sid] = append(out[sid], shStr(e, "ComputerName"))
				}
			}
		}
	}
	return out
}

func shNameList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if s, ok := v.(string); ok && s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------- 判类与映射 ----------

// shCategory 按对象自身的特征判类。**不靠文件名**：用户会把 users.json 改名、也会把几个
// 文件合成一个（比如只挑高价值对象导出），名字在这里不算证据。
func shCategory(o map[string]any) string {
	if _, ok := shRaw(o, "Trusts"); ok {
		return "domain"
	}
	if _, ok := shRaw(o, "GPOChanges"); ok {
		return "gpo"
	}
	if shStr(o, "gplink") != "" || shStr(o, "gpcfilesyspath") != "" {
		return "gpo"
	}
	if _, ok := shRaw(o, "BlocksInheritance"); ok {
		return "ou"
	}
	if _, ok := shRaw(o, "Sessions"); ok {
		return "computer"
	}
	if _, ok := shRaw(o, "LocalAdmins"); ok {
		return "computer"
	}
	if _, ok := shRaw(o, "IsDC"); ok {
		return "computer"
	}
	if shStr(o, "operatingsystem") != "" {
		return "computer"
	}
	if _, ok := shRaw(o, "Members"); ok {
		return "group"
	}
	// 用户独有的字段。组的 Properties 里只有 name/samaccountname/description/admincount/
	// whencreated 这几样，用户字段它一个都没有（enabled 尤其：组没有启用/禁用一说）。
	//
	// 顺序要紧：admincount **不是**用户独有的（受保护的用户也有），所以它只能当最后兜底，
	// 不能放在这里——否则一个只带 admincount 的账号会被判成组，路径上就会读出
	// 「可写入该组」这种指向个人的错话。
	for _, k := range []string{"hasspn", "pwdlastset", "dontreqpreauth", "serviceprincipalnames",
		"passwordnotrequired", "lastlogontimestamp", "pwdneverexpires", "sensitive",
		"enabled", "displayname", "lastlogon", "logoncount", "sidhistory"} {
		if _, ok := shRaw(o, k); ok {
			return "user"
		}
	}
	if _, ok := shRaw(o, "PrimaryGroupSID"); ok {
		return "user"
	}
	if sam := shStr(o, "samaccountname"); strings.HasSuffix(sam, "$") {
		return "computer"
	}
	if _, ok := shRaw(o, "admincount"); ok {
		// 到这一步只剩「只有 admincount」的对象（没展开成员的组，或属性被裁剪过的导出）。
		// 判成组：这个字段在真实采集里绝大多数出现在受保护组上。
		return "group"
	}
	return ""
}

func adSID(o map[string]any) string {
	if sid := shStr(o, "ObjectIdentifier"); sid != "" {
		return sid
	}
	return shStr(o, "SID")
}

// adKey 是节点的身份键：SID 优先；没有 SID 时退回 DN（组对象在少数导出里只有 DN）。
// 两遍扫描共用它——第一遍建节点与索引、第二遍建边，两边口径必须一致，
// 否则"第一遍存进去了、第二遍找不到"。
func adKey(o map[string]any) string {
	if sid := adSID(o); sid != "" {
		return sid
	}
	if dn := shStr(o, "distinguishedname"); dn != "" {
		return "dn:" + strings.ToLower(dn)
	}
	return ""
}

// objectName 取可读名。优先 Properties.name（JDOE@CORP.LOCAL 这种带域的完整写法），
// 其次是 samaccountname、DN。
func objectName(o map[string]any) string {
	for _, k := range []string{"name", "samaccountname"} {
		if s := shStr(o, k); s != "" {
			return s
		}
	}
	return shStr(o, "distinguishedname")
}

func objectDomain(o map[string]any) string {
	if d := shStr(o, "domain"); d != "" {
		return d
	}
	return domainFromDN(shStr(o, "distinguishedname"))
}

// domainFromDN 从 DN 的 DC= 段拼出域名（CN=a,DC=corp,DC=local → corp.local）。
func domainFromDN(dn string) string {
	var parts []string
	for _, seg := range strings.Split(dn, ",") {
		seg = strings.TrimSpace(seg)
		if len(seg) > 3 && strings.EqualFold(seg[:3], "DC=") {
			parts = append(parts, seg[3:])
		}
	}
	return strings.Join(parts, ".")
}

// dnToName 从 DN 取最左的 CN/RDN，用作 stub 组/OU 的可读名。
func dnToName(dn string) string {
	for _, seg := range strings.Split(dn, ",") {
		seg = strings.TrimSpace(seg)
		if i := strings.Index(seg, "="); i > 0 {
			key := strings.ToUpper(seg[:i])
			if key == "CN" || key == "OU" {
				return seg[i+1:]
			}
		}
	}
	return dn
}

// adTags 是给路径视图与报告用的标签（可烤/免认证/委派这类"这台机器/这个账号特殊在哪"）。
func adTags(o map[string]any, cat string) []string {
	var tags []string
	if b, _ := shBool(o, "hasspn"); b {
		tags = append(tags, "kerberoastable")
	}
	if b, _ := shBool(o, "dontreqpreauth"); b {
		tags = append(tags, "asreproastable")
	}
	if b, _ := shBool(o, "unconstraineddelegation"); b {
		tags = append(tags, "unconstrained-delegation")
	}
	if b, _ := shBool(o, "trustedtoauth"); b {
		tags = append(tags, "constrained-delegation")
	}
	if b, _ := shBool(o, "enabled"); !b && cat == "computer" {
		tags = append(tags, "disabled")
	}
	if b, _ := shBool(o, "IsDC"); b {
		tags = append(tags, "dc")
	}
	return tags
}

// isDC 判一台机器是不是域控。
func isDC(o map[string]any) bool {
	if b, ok := shBool(o, "IsDC"); ok && b {
		return true
	}
	// 5.x 把域控标在 Properties 里：isdc / 名字以 DC 开头都不算证据，只有显式字段算。
	if b, ok := shBool(o, "isdc"); ok && b {
		return true
	}
	return false
}

// highValueGroupName 判一个组名是不是内置高价值组（去掉域前缀与空格差异）。
func highValueGroupName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(n, "@"); i > 0 {
		n = n[:i]
	}
	return adHighValueGroups[n]
}

// ridName 从域 SID 的尾号认内置对象名（S-1-5-21-a-b-c-RID）。
func ridName(sid string) (string, bool) {
	parts := strings.Split(sid, "-")
	if len(parts) < 5 || !strings.HasPrefix(sid, "S-1-5-21-") {
		return "", false
	}
	name, ok := wellKnownRID[parts[len(parts)-1]]
	return name, ok
}

// sidName 认知名 SID（含域 SID 尾号规则）。返回值还带一个「是否高价值」的判断——
// Administrators / Domain Admins 这些既是名字也是高价值标记，分两处写迟早对不上。
func sidName(sid string) (string, bool, bool) {
	if name, ok := wellKnownSID[sid]; ok {
		return name, true, true
	}
	if name, ok := ridName(sid); ok {
		hv := name == "Administrator" || name == "krbtgt" ||
			highValueGroupName(name) || name == "Domain Controllers"
		return name, true, hv
	}
	return "", false, false
}

// ---------- 建计划 ----------

// buildADPlan 把若干采集文件映射成节点与边。纯函数——预览与应用共用同一份结果，
// 单测也直接打它，不必起库。
func buildADPlan(files []graphImportFile, plan *adPlan) {
	type rawObj struct {
		file string
		obj  map[string]any
	}
	var objs []rawObj
	unknownFiles := 0

	for _, f := range files {
		list, err := normalizeSHResp([]byte(f.JSON))
		if err != nil {
			plan.warn("%s：%v（该文件整个跳过）", f.displayName(), err)
			unknownFiles++
			continue
		}
		for _, o := range list {
			objs = append(objs, rawObj{f.displayName(), o})
		}
	}
	if len(objs) > maxADImportObjects {
		plan.warn("单次最多导入 %d 个对象，其余未解析（请分批）", maxADImportObjects)
		objs = objs[:maxADImportObjects]
	}
	if len(objs) == 0 && unknownFiles == 0 {
		plan.warn("没有解析到任何对象")
		return
	}

	// 第一遍：对象 → 节点，并建 DN → SID 索引（后面解析 MemberOf 全靠它）。
	dnIndex := map[string]string{}
	names := map[string]string{} // 机器名（小写）→ SID，解析会话/本地管理员里那些没采到的机器
	for _, ro := range objs {
		cat := shCategory(ro.obj)
		if cat == "" {
			plan.UnknownObjects++
			continue
		}
		sid := adKey(ro.obj)
		if sid == "" {
			plan.UnknownObjects++
			continue
		}
		n := &adNode{
			SID:       sid,
			Name:      objectName(ro.obj),
			Category:  cat,
			Domain:    objectDomain(ro.obj),
			SAM:       shStr(ro.obj, "samaccountname"),
			DN:        shStr(ro.obj, "distinguishedname"),
			OS:        shStr(ro.obj, "operatingsystem"),
			Desc:      shStr(ro.obj, "description"),
			LastLogon: shStr(ro.obj, "lastlogon"),
			Tags:      adTags(ro.obj, cat),
		}
		if v, ok := shBool(ro.obj, "enabled"); ok {
			n.Enabled = &v
		}
		if b, _ := shBool(ro.obj, "highvalue"); b {
			n.HighValue = true
		}
		if !n.HighValue {
			if b, _ := shBool(ro.obj, "IsHighValue"); b {
				n.HighValue = true
			}
		}
		n.AdminCount, _ = shBool(ro.obj, "admincount")
		n.HasSPN, _ = shBool(ro.obj, "hasspn")
		n.ASREP, _ = shBool(ro.obj, "dontreqpreauth")
		n.Unconstrained, _ = shBool(ro.obj, "unconstraineddelegation")
		if cat == "group" && highValueGroupName(n.Name) {
			n.HighValue = true
		}
		if isDC(ro.obj) {
			n.HighValue = true
		}
		if cat == "gpo" {
			plan.GPONodes++
		}
		plan.node(n)
		if plan.domainSID == "" {
			if ds := shStr(ro.obj, "domainsid"); ds != "" {
				plan.domainSID = ds
			} else if strings.HasPrefix(sid, "S-1-5-21-") {
				if i := strings.LastIndex(sid, "-"); i > 0 {
					plan.domainSID = sid[:i]
				}
			}
		}
		if n.DN != "" {
			dnIndex[strings.ToLower(n.DN)] = n.SID
		}
		if cat == "computer" && n.Name != "" {
			names[strings.ToLower(n.Name)] = n.SID
		}
	}
	if plan.GPONodes > 0 {
		plan.warn("已登记 %d 个 GPO 对象，但 GPO 的继承链（GPO→OU→机器）本轮未建模，攻击路径不经过 GPO", plan.GPONodes)
	}

	// sidOr 把一个 principal 解析成节点键：已知 SID → 直接用；已知 DN → 用 DN 查到 SID；
	// 知名 RID → 造 stub；都没有 → 返回空（调用方记 UnresolvedEdges）。
	sidOr := func(ref, why string) string {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return ""
		}
		if _, ok := plan.Nodes[ref]; ok {
			return ref
		}
		if sid, ok := dnIndex[strings.ToLower(ref)]; ok {
			return sid
		}
		if strings.Contains(ref, "=") { // DN
			name := dnToName(ref)
			key := "dn:" + strings.ToLower(ref)
			// CN 是内置组名、又知道本域 SID 时，还原成 SID 键——这样它与 ACE 里的
			// S-1-5-21-…-512 合成同一个节点，图上不会出现两个 Domain Admins。
			if rid, ok := ridByName[strings.ToLower(name)]; ok && plan.domainSID != "" {
				key = plan.domainSID + "-" + rid
				if _, exists := plan.Nodes[key]; exists {
					return key
				}
			}
			plan.node(&adNode{
				SID: key, Name: name, Category: "group",
				Domain: domainFromDN(ref), DN: ref, Stub: true, StubReason: why,
				HighValue: highValueGroupName(name),
			})
			return key
		}
		if name, known, hv := sidName(ref); known {
			plan.node(&adNode{
				SID: ref, Name: name, Category: "group", Stub: true, StubReason: why,
				HighValue: hv,
			})
			return ref
		}
		if sid, ok := names[strings.ToLower(ref)]; ok { // 裸机器名
			return sid
		}
		// SID 已知、名字不知道：留边，节点名暂用 SID 本身。丢掉这条边等于把「X 能委派给 Y」
		// 这条情报抹掉，而 Y 下次被采到时这个 stub 会被真节点顶掉（身份是 SID，名字只是属性）。
		if strings.HasPrefix(ref, "S-1-") {
			plan.node(&adNode{
				SID: ref, Name: ref, Category: "unknown", Stub: true, StubReason: why,
			})
			return ref
		}
		return ""
	}

	// 第二遍：边。
	for _, ro := range objs {
		sid := adKey(ro.obj)
		if sid == "" {
			continue
		}
		cat := shCategory(ro.obj)
		if _, ok := plan.Nodes[sid]; !ok {
			continue
		}

		// ① 成员关系。MemberOf 写在子对象上（DN 列表），Members 写在组上（SID 列表），
		// 两个方向都收——只导了 users.json 或只导了 groups.json 时都能建出这条边。
		for _, dn := range shStrList(ro.obj, "MemberOf") {
			if p := sidOr(dn, "MemberOf 指向未采集的组"); p != "" {
				plan.edge(sid, db.RelMemberOf, p)
			} else {
				plan.UnresolvedEdges++
			}
		}
		if pg := shStr(ro.obj, "PrimaryGroupSID"); pg != "" {
			if p := sidOr(pg, "主组未采集"); p != "" {
				plan.edge(sid, db.RelMemberOf, p)
			}
		}
		if cat == "group" {
			for _, m := range shPrincipals(ro.obj, "Members") {
				if p := sidOr(m, "组成员未采集"); p != "" {
					plan.edge(p, db.RelMemberOf, sid)
				} else {
					plan.UnresolvedEdges++
				}
			}
		}

		// ② 会话。Sessions 写在机器上（用户 SID → 机器名），HasSessions 写在用户上
		// （机器 SID → 用户）；两种都映射成 user --session_on--> computer。
		if cat == "computer" {
			// 这三个字段只写在机器对象上：用户 SID → 机器名列表，即「谁在这台机器上有会话」。
			for _, key := range []string{"Sessions", "PrivilegedSessions", "RegistrySessions"} {
				for ref := range shSessions(ro.obj, key) {
					if u := sidOr(ref, "会话里的账号未采集"); u != "" {
						plan.edge(u, db.RelSessionOn, sid)
					} else {
						plan.UnresolvedEdges++
					}
				}
			}
		}
		if cat == "user" {
			for ref := range shSessions(ro.obj, "HasSessions") {
				if c := sidOr(ref, "会话所在的机器未采集"); c != "" {
					plan.edge(sid, db.RelSessionOn, c)
				}
			}
		}

		// ③ 本机管理员与远程权限。四类分开落：
		//    LocalAdmins → admin_to（真的本机管理员）
		//    RemoteDesktopUsers / PSRemoteUsers / DcomUsers → can（权限名分别标出，
		//    路径视图里读得出"是靠 RDP 还是靠 DCOM"，合并成 admin_to 就把这个信息烧掉了）
		if cat == "computer" {
			for _, ref := range shPrincipals(ro.obj, "LocalAdmins") {
				if p := sidOr(ref, "本机管理员未采集"); p != "" {
					plan.edge(p, db.RelAdminTo, sid)
				} else {
					plan.UnresolvedEdges++
				}
			}
			for key, right := range map[string]string{
				"RemoteDesktopUsers": "CanRDP", "PSRemoteUsers": "CanPSRemote", "DcomUsers": "ExecuteDCOM",
			} {
				for _, ref := range shPrincipals(ro.obj, key) {
					if p := sidOr(ref, key+" 里的主体未采集"); p != "" {
						plan.edge(p, db.RelCan, sid, right)
					} else {
						plan.UnresolvedEdges++
					}
				}
			}
		}

		// ④ ACE。只收白名单里的权限；白名单外的按名字计数如实报出。
		for _, ace := range shObjList(ro.obj, "Aces") {
			right := shStr(ace, "RightName")
			principal := shStr(ace, "PrincipalSID")
			if right == "" || principal == "" {
				continue
			}
			if !adRights[right] {
				plan.SkippedRights[right]++
				continue
			}
			if p := sidOr(principal, "ACE 主体未采集"); p != "" {
				plan.edge(p, db.RelCan, sid, right)
			} else {
				plan.UnresolvedEdges++
			}
		}

		// ⑤ 委派。AllowedToDelegate 是裸 SID 数组，AllowedToAct 是对象数组，都是
		// 「这个主体能冒充谁」——单独一条 right，因为它是横向里最值钱的一类。
		for _, key := range []string{"AllowedToDelegate", "AllowedToAct"} {
			for _, ref := range shPrincipals(ro.obj, key) {
				if p := sidOr(ref, "委派目标未采集"); p != "" {
					plan.edge(sid, db.RelCan, p, "AllowedToDelegate")
				} else {
					plan.UnresolvedEdges++
				}
			}
		}

		// ⑥ 域信任。目标是另一个域，通常不在本次采集里——造域 stub：信任记录本身
		// 就说明了那个域存在，这是跨域路径的落脚点，丢掉它等于把跨域这条路抹掉。
		for _, t := range shObjList(ro.obj, "Trusts") {
			tsid := shStr(t, "TargetDomainSid")
			tname := shStr(t, "TargetDomainName")
			if tsid == "" && tname == "" {
				continue
			}
			if tsid == "" {
				tsid = "dn:" + strings.ToLower(tname)
			}
			if _, ok := plan.Nodes[tsid]; !ok {
				plan.node(&adNode{
					SID: tsid, Name: tname, Category: "domain", Domain: tname,
					Stub: true, StubReason: "域信任的目标域（不在本次采集中）",
					HighValue: false,
				})
			}
			right := "trusted_by"
			if dir := shStr(t, "TrustDirection"); dir != "" {
				right = "trusted_by:" + strings.ToLower(dir)
			}
			plan.edge(sid, db.RelCan, tsid, right)
		}
	}

	// 收尾如实报账：认不出的对象、连身份都拿不到的边、跳过的权限——都要在预览里看得见。
	if plan.UnknownObjects > 0 {
		plan.warn("有 %d 个对象认不出类型（既不是用户/组/机器/域/OU/GPO），已跳过", plan.UnknownObjects)
	}
	if plan.UnresolvedEdges > 0 {
		plan.warn("有 %d 条边的主体既不是已知对象、也不是 SID/DN，已跳过", plan.UnresolvedEdges)
	}
	if len(plan.SkippedRights) > 0 {
		names := make([]string, 0, len(plan.SkippedRights))
		for r, c := range plan.SkippedRights {
			names = append(names, fmt.Sprintf("%s×%d", r, c))
		}
		sort.Strings(names)
		plan.warn("权限白名单外、未建边的 ACE：%s", strings.Join(names, "、"))
	}
}

// shObjList 读一个对象数组字段（如 Aces）。元素不是对象的条目跳过。
func shObjList(o map[string]any, key string) []map[string]any {
	v, ok := shRaw(o, key)
	if !ok {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// ---------- HTTP ----------

// graphImportFile 是一个上传的采集文件。名字只用于报错时说清是哪个文件——判类不靠它。
// graphImportFile 是一个上传的枚举结果文件（AD 的 SharpHound 与云上的 aws-cli/kubectl 输出共用）。
type graphImportFile struct {
	Name string `json:"name"`
	JSON string `json:"json"`
}

func (f graphImportFile) displayName() string {
	if strings.TrimSpace(f.Name) == "" {
		return "（未命名文件）"
	}
	return f.Name
}

type adImportReq struct {
	TaskID int64             `json:"task_id"`
	Files  []graphImportFile `json:"files"`
}

// adImportPlan 是「计划 + 落点」：两条路径共用，保证预览与应用同源。
type adImportPlan struct {
	TaskID int64
	Store  *db.ExplorationStore
	Plan   *adPlan
	// existingNodes 是本探索里已存在的 AD 对象 SID 集合（预览据此报"新增/已有"）。
	ExistingNodes map[string]bool
	// existingEdges 是已存在的 from|rel|to 集合，以及已存在的 can 边 rights（并集用）。
	ExistingEdges map[string][]string
}

func (s *Server) buildADImportPlan(w http.ResponseWriter, r *http.Request) (*adImportPlan, bool) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxADImportBodyBytes)
	var req adImportReq
	if err := decode(r, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, 400, "invalid JSON: "+err.Error())
		}
		return nil, false
	}
	if len(req.Files) == 0 {
		writeErr(w, 400, "files 不能为空")
		return nil, false
	}
	if req.TaskID <= 0 {
		writeErr(w, 400, "task_id 必填：AD 图挂在某个任务的探索图上（导入前先选任务）")
		return nil, false
	}
	task, err := s.m.pg.GetTask(req.TaskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, false
	}
	if task == nil {
		writeErr(w, 404, "任务不存在")
		return nil, false
	}
	store := s.m.pg.Exploration(task.ExplorationID)

	plan := newADPlan()
	buildADPlan(req.Files, plan)

	// 已存在的节点/边：预览要能说清「这次会新增多少、合并多少」。
	nodes, err := store.ListByKind(db.KindADObject, 20000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, false
	}
	existingNodes := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		var p struct {
			SID string `json:"sid"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if p.SID != "" {
			existingNodes[p.SID] = true
		}
	}
	edges, err := store.Edges(20000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, false
	}
	existingEdges := make(map[string][]string, len(edges))
	for _, e := range edges {
		key := strconv.FormatInt(e.From, 10) + "|" + e.Rel + "|" + strconv.FormatInt(e.To, 10)
		existingEdges[key] = db.EdgeRights(e)
	}
	return &adImportPlan{TaskID: req.TaskID, Store: store, Plan: plan, ExistingNodes: existingNodes, ExistingEdges: existingEdges}, true
}

// adObjectCounts 按类统计节点数（含 stub，单列出来）。
func (p *adPlan) objectCounts() map[string]int {
	out := map[string]int{}
	for _, n := range p.Nodes {
		if n.Stub {
			out["stub"]++
			continue
		}
		out[n.Category]++
	}
	return out
}

func (p *adPlan) edgeCounts() map[string]int {
	out := map[string]int{}
	for _, e := range p.Edges {
		out[e.Rel]++
	}
	return out
}

// adHighValue 列高价值对象，供预览里"这次导进来的重点是什么"一眼看见。
func (p *adPlan) highValue(limit int) []map[string]any {
	var list []*adNode
	for _, n := range p.Nodes {
		if n.HighValue {
			list = append(list, n)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Category != list[j].Category {
			return list[i].Category < list[j].Category
		}
		return list[i].Name < list[j].Name
	})
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]map[string]any, 0, len(list))
	for _, n := range list {
		item := map[string]any{"name": n.Name, "category": n.Category}
		if n.Stub {
			item["stub"] = true
		}
		if len(n.Tags) > 0 {
			item["tags"] = n.Tags
		}
		out = append(out, item)
	}
	return out
}

// adImportPreview 只读：说清「会导入什么」，一个字节都不写。
func (s *Server) adImportPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.buildADImportPlan(w, r)
	if !ok {
		return
	}
	newNodes, existingNodes := 0, 0
	stubs := []map[string]any{}
	keys := make([]string, 0, len(p.Plan.Nodes))
	for k := range p.Plan.Nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		n := p.Plan.Nodes[k]
		if p.ExistingNodes[k] {
			existingNodes++
		} else {
			newNodes++
		}
		if n.Stub && len(stubs) < adSampleLimit {
			stubs = append(stubs, map[string]any{
				"name": n.Name, "category": n.Category, "why": n.StubReason,
			})
		}
	}
	newEdges, existingEdges := 0, 0
	for _, e := range p.Plan.Edges {
		// 边的两端此时还是 SID，落库后才成 id——预览里"已存在"看的是两端**都已在库里**
		// 的那一类，够用来回答"这次是不是重导同一份"。
		if p.ExistingNodes[e.From] && p.ExistingNodes[e.To] {
			existingEdges++
		} else {
			newEdges++
		}
	}
	resp := map[string]any{
		"task_id":        p.TaskID,
		"objects":        p.Plan.objectCounts(),
		"edges":          p.Plan.edgeCounts(),
		"nodes_new":      newNodes,
		"nodes_existing": existingNodes,
		"edges_new":      newEdges,
		"edges_existing": existingEdges,
		"highvalue":      p.Plan.highValue(adSampleLimit),
		"warnings":       p.Plan.Warnings,
		"stubs":          stubs,
	}
	if len(p.Plan.SkippedRights) > 0 {
		resp["skipped_rights"] = p.Plan.SkippedRights
	}
	if p.Plan.UnresolvedEdges > 0 {
		resp["unresolved_edges"] = p.Plan.UnresolvedEdges
	}
	if p.Plan.UnknownObjects > 0 {
		resp["unknown_objects"] = p.Plan.UnknownObjects
	}
	writeJSON(w, 200, resp)
}

// adImportApply 落库。节点按 SID 复用（已存在则刷新属性），边按 (from,rel,to) 幂等；
// can 边的 rights 在这里做并集——新采到的权限不覆盖旧的，而是加上去。
//
// 与 P26 的资产导入一致：**不包大事务**。导入是一次性动作、不在热路径上，
// 单条失败不连坐比"全有全无"更符合这里的用途（一批几千个对象里坏一个不该整批白导）。
func (s *Server) adImportApply(w http.ResponseWriter, r *http.Request) {
	p, ok := s.buildADImportPlan(w, r)
	if !ok {
		return
	}

	// 锚定用：机器名 → 资产 id。一条 SQL 查完，不按节点逐个查。
	var hosts []string
	for _, n := range p.Plan.Nodes {
		if n.Category != "computer" || n.Name == "" {
			continue
		}
		hosts = append(hosts, strings.TrimSuffix(n.Name, "$"))
		if n.SAM != "" {
			hosts = append(hosts, strings.TrimSuffix(n.SAM, "$"))
		}
	}
	assetIDs := map[string]int64{}
	if len(hosts) > 0 {
		if as := s.assetStore(); as != nil {
			if m, err := as.IDsByHosts(hosts); err == nil {
				assetIDs = m
			}
			// 查不到就锚不上——这不是错误：AD 图与资产图本来就允许各有一半。
		}
	}

	keys := make([]string, 0, len(p.Plan.Nodes))
	for k := range p.Plan.Nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	nodeID := map[string]int64{}
	created, updated := 0, 0
	failed := 0
	var errs []string
	for _, k := range keys {
		n := p.Plan.Nodes[k]
		id, err := p.Store.FindADNodeBySID(k)
		if err != nil {
			failed++
			if len(errs) < adSampleLimit {
				errs = append(errs, fmt.Sprintf("%s：查既有节点失败：%v", n.Name, err))
			}
			continue
		}
		if id > 0 {
			if err := p.Store.SetNodePayload(id, db.KindADObject, n.payload()); err != nil {
				failed++
				if len(errs) < adSampleLimit {
					errs = append(errs, fmt.Sprintf("%s：更新属性失败：%v", n.Name, err))
				}
				continue
			}
			nodeID[k] = id
			updated++
			continue
		}
		var anchors []int64
		if n.Category == "computer" {
			for _, h := range []string{strings.TrimSuffix(n.Name, "$"), strings.TrimSuffix(n.SAM, "$")} {
				if h == "" {
					continue
				}
				if aid, ok := assetIDs[strings.ToLower(h)]; ok {
					anchors = append(anchors, aid)
					break
				}
			}
		}
		id, err = p.Store.AddNode(db.KindADObject, n.payload(), 0, db.StateADActive, "ad_import", anchors)
		if err != nil {
			failed++
			if len(errs) < adSampleLimit {
				errs = append(errs, fmt.Sprintf("%s：建节点失败：%v", n.Name, err))
			}
			continue
		}
		nodeID[k] = id
		created++
	}

	edgeKeys := make([]string, 0, len(p.Plan.Edges))
	for k := range p.Plan.Edges {
		edgeKeys = append(edgeKeys, k)
	}
	sort.Strings(edgeKeys)

	linked, relinked, missing := 0, 0, 0
	for _, k := range edgeKeys {
		e := p.Plan.Edges[k]
		from, okFrom := nodeID[e.From]
		to, okTo := nodeID[e.To]
		if !okFrom || !okTo {
			missing++
			continue
		}
		exKey := strconv.FormatInt(from, 10) + "|" + e.Rel + "|" + strconv.FormatInt(to, 10)
		old, seen := p.ExistingEdges[exKey]
		var err error
		if e.Rel == db.RelCan {
			err = p.Store.LinkAttrs(from, e.Rel, to, map[string]any{"rights": mergeRights(old, e.Rights)})
		} else {
			err = p.Store.Link(from, e.Rel, to)
		}
		if err != nil {
			failed++
			if len(errs) < adSampleLimit {
				errs = append(errs, fmt.Sprintf("边 %s → %s 失败：%v", e.From, e.To, err))
			}
			continue
		}
		if seen {
			relinked++
		} else {
			linked++
		}
	}

	resp := map[string]any{
		"task_id":       p.TaskID,
		"nodes_created": created,
		"nodes_updated": updated,
		"edges_created": linked,
		"edges_merged":  relinked,
		"objects":       p.Plan.objectCounts(),
		"edges":         p.Plan.edgeCounts(),
		"warnings":      p.Plan.Warnings,
	}
	if missing > 0 {
		resp["edges_unresolved"] = missing
	}
	if len(p.Plan.SkippedRights) > 0 {
		resp["skipped_rights"] = p.Plan.SkippedRights
	}
	if failed > 0 {
		resp["failures"] = failed
		resp["errors"] = errs
	}
	writeJSON(w, 200, resp)
}
