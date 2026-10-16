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

	"github.com/lin9527ya/blade/db"
)

// 云上枚举结果导入（P39，红队路线图的 G 项）。
//
// 与 AD 导入（P32）同一条纪律，也与它同一个现实前提：**平台不持有云凭据，也不该持有**。
// 枚举在凭据所在的地方做（aws-cli / kubectl / ScoutSuite 之类），平台只把**离线结果**读成图。
// 这既是部署现实（云凭据交给一个渗透平台保管，本身就是个新风险面），也是本项目唯一能端到端
// 验证的形态——本机没有云 CLI、没有云凭据（同 A 项的 Sliver：没有可验证的东西就不做）。
//
// 吃两种格式，**按内容判、不按文件名**：
//   ① AWS IAM：`aws iam get-account-authorization-details` 的输出
//      （UserDetailList / GroupDetailList / RoleDetailList / Policies）
//   ② K8s RBAC：`kubectl get clusterroles,clusterrolebindings,roles,rolebindings,serviceaccounts -A -o json`
//      的输出（{kind:List, items:[…]} 或裸数组）
//
// 与 AD 共用的东西：同一套边（member_of / can）、同一条路径算法（db/ad_paths.go 顺势走）、
// 同一份"预览只读、应用才写"的契约。所以云对象进的是**同一张图**，攻击路径视图零改动就能读它们。

// ---------- 计划 ----------

type cloudNode struct {
	Key        string // 身份键：ARN / k8s 的 kind:ns:name
	Name       string
	Category   string // account/user/group/role/policy/service_account/cluster/namespace
	Provider   string // aws | k8s
	Detail     string
	HighValue  bool
	Tags       []string
	Stub       bool
	StubReason string
}

func (n *cloudNode) payload() map[string]any {
	m := map[string]any{"name": n.Name, "category": n.Category, "provider": n.Provider}
	if n.Detail != "" {
		m["detail"] = n.Detail
	}
	if n.HighValue {
		m["highvalue"] = true
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

type cloudPlan struct {
	Nodes map[string]*cloudNode
	Edges map[string]*cloudEdge
	// 未收录/未建模的东西如实计数（同 AD 那份纪律：宁可少收，不可静默丢）。
	Warnings []string
	Skipped  map[string]int
}

type cloudEdge struct {
	From   string
	Rel    string
	To     string
	Rights []string
}

func newCloudPlan() *cloudPlan {
	return &cloudPlan{Nodes: map[string]*cloudNode{}, Edges: map[string]*cloudEdge{}, Skipped: map[string]int{}}
}

func (p *cloudPlan) warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

func (p *cloudPlan) skip(reason string) { p.Skipped[reason]++ }

// node 登记一个节点。**真节点优先于 stub**：先建出来的 stub 会被后来的真节点顶掉
// （枚举文件是分着导的，顺序不该决定图长什么样）。
func (p *cloudPlan) node(n *cloudNode) *cloudNode {
	if n.Key == "" {
		p.skip("没有身份键的对象")
		return nil
	}
	if old, ok := p.Nodes[n.Key]; ok {
		if n.Stub || !old.Stub {
			if !n.Stub {
				p.Nodes[n.Key] = n
				return n
			}
			return old
		}
		p.Nodes[n.Key] = n
		return n
	}
	p.Nodes[n.Key] = n
	return n
}

func (p *cloudPlan) stub(key, name, category, provider, why string) *cloudNode {
	return p.node(&cloudNode{Key: key, Name: name, Category: category, Provider: provider, Stub: true, StubReason: why})
}

func (p *cloudPlan) edge(from, rel, to string, rights ...string) {
	if from == "" || to == "" || from == to {
		return
	}
	key := from + "|" + rel + "|" + to
	if e, ok := p.Edges[key]; ok {
		e.Rights = mergeRights(e.Rights, rights)
		return
	}
	p.Edges[key] = &cloudEdge{From: from, Rel: rel, To: to, Rights: mergeRights(nil, rights)}
}

func (p *cloudPlan) objectCounts() map[string]int {
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

func (p *cloudPlan) edgeCounts() map[string]int {
	out := map[string]int{}
	for _, e := range p.Edges {
		out[e.Rel]++
	}
	return out
}

func (p *cloudPlan) highValue(limit int) []map[string]any {
	var list []*cloudNode
	for _, n := range p.Nodes {
		if n.HighValue {
			list = append(list, n)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	if len(list) > limit {
		list = list[:limit]
	}
	out := make([]map[string]any, 0, len(list))
	for _, n := range list {
		item := map[string]any{"name": n.Name, "category": n.Category, "provider": n.Provider}
		if len(n.Tags) > 0 {
			item["tags"] = n.Tags
		}
		out = append(out, item)
	}
	return out
}

// ---------- 判类 ----------

// cloudDoc 是一次解析出来的文档（AWS 的对象 / K8s 的对象数组）。
type cloudDoc struct {
	provider string // aws | k8s
	aws      map[string]any
	k8s      []map[string]any
}

// parseCloudDoc 按**内容**判这是哪一种枚举结果。都不像就报错——绝不静默返回空
// （"解析失败"被读成"云上什么都没有"，是最难查的一类假阴性，同 FOFA 那次的教训）。
func parseCloudDoc(raw []byte) (cloudDoc, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return cloudDoc{}, errors.New("文件是空的")
	}
	var top any
	if err := json.Unmarshal(trimmed, &top); err != nil {
		return cloudDoc{}, fmt.Errorf("JSON 解析失败：%w", err)
	}
	switch v := top.(type) {
	case map[string]any:
		// ① AWS IAM 授权明细
		if _, ok := v["UserDetailList"]; ok {
			return cloudDoc{provider: "aws", aws: v}, nil
		}
		if _, ok := v["RoleDetailList"]; ok {
			return cloudDoc{provider: "aws", aws: v}, nil
		}
		if _, ok := v["Policies"]; ok {
			return cloudDoc{provider: "aws", aws: v}, nil
		}
		// ② K8s List
		if items, ok := v["items"].([]any); ok {
			objs, err := cloudObjects(items)
			return cloudDoc{provider: "k8s", k8s: objs}, err
		}
		return cloudDoc{}, errors.New("不是云上枚举结果：既没有 AWS 授权明细的字段（UserDetailList/RoleDetailList/Policies），也不是 K8s 的 {items:[…]}")
	case []any:
		// 裸数组：K8s 多类型混导常见这种形状。
		objs, err := cloudObjects(v)
		if err != nil {
			return cloudDoc{}, err
		}
		return cloudDoc{provider: "k8s", k8s: objs}, nil
	}
	return cloudDoc{}, fmt.Errorf("不是云上枚举结果：顶层是 %T", top)
}

func cloudObjects(in []any) ([]map[string]any, error) {
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

// ---------- 取值助手 ----------

func cloudStr(o map[string]any, key string) string {
	if v, ok := o[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func cloudList(o map[string]any, key string) []map[string]any {
	arr, ok := o[key].([]any)
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

// cloudStrs 读一个"字符串或字符串数组"的字段（AWS 策略文档里两种写法都常见）。
func cloudStrs(v any) []string {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

func cloudStmtList(doc map[string]any) []map[string]any {
	if doc == nil {
		return nil
	}
	switch st := doc["Statement"].(type) {
	case []any:
		var out []map[string]any
		for _, e := range st {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{st}
	}
	return nil
}

// isAdminPolicyDoc 判一份策略文档是不是**管理员等价**：Allow + Action 含 "*" + Resource 含 "*"。
//
// 只认这一种写法，不做"看起来很大"的推断：`iam:*` 之类是强权限但不等价于完全控制，
// 把它们也算成 admin 会让"高价值目标"这张表失去分辨力（而路径视图要答的正是"能不能到管理员"）。
func isAdminPolicyDoc(doc map[string]any) bool {
	for _, st := range cloudStmtList(doc) {
		if !strings.EqualFold(cloudStr(st, "Effect"), "Allow") {
			continue
		}
		acts := cloudStrs(st["Action"])
		res := cloudStrs(st["Resource"])
		if listHas(acts, "*") && listHas(res, "*") {
			return true
		}
	}
	return false
}

// k8sRulesAreAdmin 判一组 K8s 规则是不是管理员等价（verbs/resources 都是 *）。
func k8sRulesAreAdmin(rules []map[string]any) bool {
	for _, r := range rules {
		verbs := cloudStrs(r["verbs"])
		res := cloudStrs(r["resources"])
		if listHas(verbs, "*") && listHas(res, "*") {
			return true
		}
	}
	return false
}

// listHas 判一个字符串列表里有没有某一项（策略动作/资源里的 "*" 判定）。
func listHas(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// strListOf 把 []string 里的空串去掉（拼 detail 用）。
func strListOf(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------- 建计划 ----------

func buildCloudPlan(files []graphImportFile, plan *cloudPlan) {
	var awsDocs []map[string]any
	var k8sObjs []map[string]any
	for _, f := range files {
		doc, err := parseCloudDoc([]byte(f.JSON))
		if err != nil {
			plan.warn("%s：%v（该文件整个跳过）", f.displayName(), err)
			continue
		}
		switch doc.provider {
		case "aws":
			awsDocs = append(awsDocs, doc.aws)
		case "k8s":
			k8sObjs = append(k8sObjs, doc.k8s...)
		}
	}
	if len(awsDocs) == 0 && len(k8sObjs) == 0 {
		plan.warn("没有解析到任何云上枚举结果")
		return
	}
	for _, doc := range awsDocs {
		buildAWSPlan(doc, plan)
	}
	if len(k8sObjs) > 0 {
		buildK8sPlan(k8sObjs, plan)
	}

	if len(plan.Skipped) > 0 {
		reasons := make([]string, 0, len(plan.Skipped))
		for r, c := range plan.Skipped {
			reasons = append(reasons, fmt.Sprintf("%s×%d", r, c))
		}
		sort.Strings(reasons)
		plan.warn("以下内容未建模：%s", strings.Join(reasons, "、"))
	}
}

// buildAWSPlan 吃 aws iam get-account-authorization-details 的一份输出。
func buildAWSPlan(doc map[string]any, plan *cloudPlan) {
	groups := cloudList(doc, "GroupDetailList")
	groupARN := map[string]string{}
	for _, g := range groups {
		name := cloudStr(g, "GroupName")
		if arn := cloudStr(g, "Arn"); arn != "" && name != "" {
			groupARN[name] = arn
		}
	}
	accountOf := func(arn string) string {
		// arn:aws:iam::123456789012:user/x → 123456789012
		parts := strings.Split(arn, ":")
		if len(parts) >= 5 {
			return parts[4]
		}
		return ""
	}
	accountKey := func(arn string) string {
		if acct := accountOf(arn); acct != "" {
			return "aws:account:" + acct
		}
		return ""
	}
	ensureAccount := func(arn string) string {
		key := accountKey(arn)
		if key == "" {
			return ""
		}
		if _, ok := plan.Nodes[key]; !ok {
			plan.node(&cloudNode{Key: key, Name: "AWS 账号 " + accountOf(arn), Category: "account", Provider: "aws"})
		}
		return key
	}

	// 策略：托管策略（Policies）先登记，管理员等价的标高价值。
	policyDoc := func(p map[string]any) map[string]any {
		best := map[string]any(nil)
		for _, v := range cloudList(p, "PolicyVersionList") {
			isDefault, _ := v["IsDefaultVersion"].(bool)
			doc, _ := v["Document"].(map[string]any)
			if doc == nil {
				continue
			}
			if isDefault || best == nil {
				best = doc
			}
		}
		return best
	}
	for _, p := range cloudList(doc, "Policies") {
		arn := cloudStr(p, "Arn")
		name := cloudStr(p, "PolicyName")
		if arn == "" {
			plan.skip("没有 ARN 的策略")
			continue
		}
		adminDoc := policyDoc(p)
		n := &cloudNode{Key: arn, Name: name, Category: "policy", Provider: "aws"}
		if isAdminPolicyDoc(adminDoc) {
			n.HighValue = true
			n.Tags = []string{"admin"}
			n.Detail = "允许 * / *（管理员等价）"
		} else if len(adminDoc) > 0 {
			n.Detail = summarizePolicyDoc(adminDoc)
		}
		plan.node(n)
		ensureAccount(arn)
	}
	attachPolicy := func(ownerKey, policyArn, policyName, kind string) {
		if policyArn == "" {
			return
		}
		if _, ok := plan.Nodes[policyArn]; !ok {
			plan.stub(policyArn, nzStr(policyName, policyArn), "policy", "aws", "策略未在本份枚举里（只导了主体）")
		}
		plan.edge(ownerKey, db.RelCan, policyArn, kind)
	}
	attachInline := func(ownerKey string, list []map[string]any) {
		for _, ip := range list {
			name := cloudStr(ip, "PolicyName")
			if name == "" {
				plan.skip("没有名字的内联策略")
				continue
			}
			key := "inline:" + ownerKey + ":" + name
			docu, _ := ip["PolicyDocument"].(map[string]any)
			n := &cloudNode{Key: key, Name: name + "（内联）", Category: "policy", Provider: "aws"}
			if isAdminPolicyDoc(docu) {
				n.HighValue = true
				n.Tags = []string{"admin"}
				n.Detail = "允许 * / *（管理员等价）"
			} else if len(docu) > 0 {
				n.Detail = summarizePolicyDoc(docu)
			}
			plan.node(n)
			plan.edge(ownerKey, db.RelCan, key, "inline")
		}
	}

	for _, u := range cloudList(doc, "UserDetailList") {
		arn := cloudStr(u, "Arn")
		name := cloudStr(u, "UserName")
		if arn == "" {
			arn = "aws:user:" + name
		}
		plan.node(&cloudNode{Key: arn, Name: name, Category: "user", Provider: "aws"})
		ensureAccount(arn)
		for _, g := range cloudStrs(u["GroupList"]) {
			key := groupARN[g]
			if key == "" {
				key = "aws:group:" + g
				plan.stub(key, g, "group", "aws", "组不在本份枚举里（只导了用户）")
			}
			plan.edge(arn, db.RelMemberOf, key)
		}
		for _, ap := range cloudList(u, "AttachedManagedPolicies") {
			attachPolicy(arn, cloudStr(ap, "PolicyArn"), cloudStr(ap, "PolicyName"), "attached")
		}
		attachInline(arn, cloudList(u, "UserPolicyList"))
	}

	for _, g := range groups {
		arn := cloudStr(g, "Arn")
		name := cloudStr(g, "GroupName")
		if arn == "" {
			arn = "aws:group:" + name
		}
		plan.node(&cloudNode{Key: arn, Name: name, Category: "group", Provider: "aws"})
		ensureAccount(arn)
		for _, ap := range cloudList(g, "AttachedManagedPolicies") {
			attachPolicy(arn, cloudStr(ap, "PolicyArn"), cloudStr(ap, "PolicyName"), "attached")
		}
		attachInline(arn, cloudList(g, "GroupPolicyList"))
	}

	for _, r := range cloudList(doc, "RoleDetailList") {
		arn := cloudStr(r, "Arn")
		name := cloudStr(r, "RoleName")
		if arn == "" {
			arn = "aws:role:" + name
		}
		plan.node(&cloudNode{Key: arn, Name: name, Category: "role", Provider: "aws"})
		ensureAccount(arn)
		for _, ap := range cloudList(r, "AttachedManagedPolicies") {
			attachPolicy(arn, cloudStr(ap, "PolicyArn"), cloudStr(ap, "PolicyName"), "attached")
		}
		attachInline(arn, cloudList(r, "RolePolicyList"))

		// 信任策略：谁可以代入这个角色。服务主体（ec2.amazonaws.com 这类）不建模——
		// 它们不是图上的身份；如实计数，别让它静默消失（"谁能 assume"少了一条是最容易被忽略的缺口）。
		trust, _ := r["AssumeRolePolicyDocument"].(map[string]any)
		for _, st := range cloudStmtList(trust) {
			if !strings.EqualFold(cloudStr(st, "Effect"), "Allow") {
				continue
			}
			acts := cloudStrs(st["Action"])
			if !listHas(acts, "sts:AssumeRole") && !listHas(acts, "*") && len(acts) > 0 {
				continue
			}
			princ, _ := st["Principal"].(map[string]any)
			if princ == nil {
				continue
			}
			handled := false
			for _, key := range []string{"AWS"} {
				for _, p := range cloudStrs(princ[key]) {
					handled = true
					if p == "*" {
						plan.warn("角色 %s 的信任策略允许**任意主体**（Principal: \"*\"）——本平台不为此造节点，但这条要人自己看一眼", name)
						continue
					}
					if _, ok := plan.Nodes[p]; !ok {
						plan.stub(p, p, principalCategoryOf(p), "aws", "信任策略里的主体不在本份枚举里")
					}
					plan.edge(p, db.RelCan, arn, "assume_role")
				}
			}
			for _, key := range []string{"Service", "Federated", "CanonicalUser"} {
				if len(cloudStrs(princ[key])) > 0 {
					handled = true
					plan.skip(key + " 主体（" + key + "，不是图上的身份）")
				}
			}
			if !handled {
				plan.skip("空信任主体")
			}
		}
	}
}

// principalCategoryOf 从 ARN 猜类别（信任策略里可能出现没枚举到的主体）。
func principalCategoryOf(arn string) string {
	switch {
	case strings.Contains(arn, ":role/"):
		return "role"
	case strings.Contains(arn, ":group/"):
		return "group"
	case strings.Contains(arn, ":user/"):
		return "user"
	case strings.HasPrefix(arn, "arn:aws:iam::"):
		return "account"
	}
	return "user"
}

// summarizePolicyDoc 把一份策略文档压成一句话（进 detail，不进 prompt 的是原文）。
func summarizePolicyDoc(doc map[string]any) string {
	var parts []string
	for i, st := range cloudStmtList(doc) {
		if i >= 3 {
			parts = append(parts, "…")
			break
		}
		eff := cloudStr(st, "Effect")
		acts := strListOf(cloudStrs(st["Action"]))
		if len(acts) == 0 {
			acts = strListOf(cloudStrs(st["NotAction"]))
			if len(acts) > 0 {
				acts = []string{"非 " + strings.Join(acts, ",")}
			}
		}
		res := strListOf(cloudStrs(st["Resource"]))
		parts = append(parts, fmt.Sprintf("%s %s on %s", eff, orJoin(acts, "—"), orJoin(res, "—")))
	}
	return strings.Join(parts, "；")
}

func orJoin(list []string, fallback string) string {
	if len(list) == 0 {
		return fallback
	}
	return strings.Join(list, ",")
}

// buildK8sPlan 吃 K8s 的 RBAC 对象（ClusterRole/Role/ClusterRoleBinding/RoleBinding/ServiceAccount）。
func buildK8sPlan(objs []map[string]any, plan *cloudPlan) {
	roleKey := func(kind, ns, name string) string {
		if strings.EqualFold(kind, "ClusterRole") {
			return "k8s:clusterrole:" + name
		}
		return "k8s:role:" + ns + "/" + name
	}
	subjectNode := func(kind, name, ns string) string {
		switch strings.ToLower(kind) {
		case "serviceaccount":
			// 绑定里写的是 system:serviceaccount:<ns>:<name>，对象本身是 <ns>/<name>——两种都归一。
			if strings.HasPrefix(name, "system:serviceaccount:") {
				parts := strings.Split(name, ":")
				if len(parts) == 4 {
					ns, name = parts[2], parts[3]
				}
			}
			key := "k8s:sa:" + ns + "/" + name
			if _, ok := plan.Nodes[key]; !ok {
				plan.stub(key, name, "service_account", "k8s", "绑定里出现、但没导这个 ServiceAccount")
			}
			return key
		case "group":
			key := "k8s:group:" + name
			_ = plan.stub(key, name, "group", "k8s", "")
			n := plan.Nodes[key]
			if n != nil && (name == "system:authenticated" || name == "system:unauthenticated") {
				n.Tags = append(n.Tags, "broad")
			}
			return key
		default: // User
			key := "k8s:user:" + name
			plan.stub(key, name, "user", "k8s", "")
			return key
		}
	}

	for _, o := range objs {
		kind := cloudStr(o, "kind")
		meta, _ := o["metadata"].(map[string]any)
		if meta == nil {
			plan.skip("没有 metadata 的对象")
			continue
		}
		name := cloudStr(meta, "name")
		ns := cloudStr(meta, "namespace")
		switch kind {
		case "ClusterRole", "Role":
			rules := cloudList(o, "rules")
			n := &cloudNode{Key: roleKey(kind, ns, name), Name: name, Category: "role", Provider: "k8s"}
			if kind == "Role" && ns != "" {
				n.Name = ns + "/" + name
				n.Detail = "命名空间 " + ns + " 内的角色"
			}
			if k8sRulesAreAdmin(rules) {
				n.HighValue = true
				n.Tags = []string{"cluster-admin-equivalent"}
				if kind == "Role" {
					n.Tags = []string{"namespace-admin-equivalent"}
				}
				n.Detail = strings.TrimSpace(n.Detail + " 规则含 verbs:* / resources:*（管理员等价）")
			} else if len(rules) > 0 {
				n.Detail = summarizeK8sRules(rules)
			}
			plan.node(n)
		case "ClusterRoleBinding", "RoleBinding":
			ref, _ := o["roleRef"].(map[string]any)
			if ref == nil {
				plan.skip("没有 roleRef 的绑定")
				continue
			}
			refKind := cloudStr(ref, "kind")
			refName := cloudStr(ref, "name")
			// roleRef 指向的是角色**对象**；命名空间绑定的 Role 就在绑定的命名空间里。
			target := roleKey(refKind, ns, refName)
			if _, ok := plan.Nodes[target]; !ok {
				plan.stub(target, refName, "role", "k8s", "绑定指向、但没导这个角色")
			}
			right := strings.ToLower(refKind) + ":" + refName
			subjects := cloudList(o, "subjects")
			if len(subjects) == 0 {
				plan.skip("没有 subjects 的绑定")
			}
			for _, s := range subjects {
				sk := strings.ToLower(cloudStr(s, "kind"))
				sname := cloudStr(s, "name")
				sns := cloudStr(s, "namespace")
				if sname == "" {
					plan.skip("没有名字的绑定主体")
					continue
				}
				plan.edge(subjectNode(sk, sname, sns), db.RelCan, target, right)
			}
		case "ServiceAccount":
			key := "k8s:sa:" + ns + "/" + name
			plan.node(&cloudNode{Key: key, Name: name, Category: "service_account", Provider: "k8s",
				Detail: "命名空间 " + ns})
		case "Namespace":
			// 命名空间不是身份也不是授权，只登记节点会让图上多出一堆没有边的点；
			// 如实计数，不建。
			plan.skip("Namespace 对象（不是身份/授权，未建模）")
		default:
			plan.skip("未建模的 K8s 对象：" + nzStr(kind, "（无 kind）"))
		}
	}
}

// summarizeK8sRules 把 K8s 规则压成一句话。
func summarizeK8sRules(rules []map[string]any) string {
	var parts []string
	for i, r := range rules {
		if i >= 3 {
			parts = append(parts, "…")
			break
		}
		verbs := strListOf(cloudStrs(r["verbs"]))
		res := strListOf(cloudStrs(r["resources"]))
		parts = append(parts, fmt.Sprintf("%s on %s", orJoin(verbs, "—"), orJoin(res, "—")))
	}
	return strings.Join(parts, "；")
}

// ---------- HTTP ----------

type cloudImportReq struct {
	TaskID int64             `json:"task_id"`
	Files  []graphImportFile `json:"files"`
}

type cloudImportPlan struct {
	TaskID        int64
	Store         *db.ExplorationStore
	Plan          *cloudPlan
	ExistingNodes map[string]bool
	ExistingEdges map[string][]string
}

func (s *Server) buildCloudImportPlan(w http.ResponseWriter, r *http.Request) (*cloudImportPlan, bool) {
	if s.m == nil || s.m.pg == nil {
		writeErr(w, 503, "database unavailable")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxADImportBodyBytes)
	var req cloudImportReq
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
		writeErr(w, 400, "task_id 必填：云上枚举结果也挂在任务的探索图上，与域内数据同图")
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
	plan, err := planCloudImport(store, req.Files)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, false
	}
	plan.TaskID = req.TaskID
	return plan, true
}

// planCloudImport 建计划并读回既有节点/边。**页面导入与 agent 工具共用**——agent 在目标里
// 拿着偷到的 key 跑出授权明细时，走的是与页面导入完全相同的那条落库路径。
func planCloudImport(store *db.ExplorationStore, files []graphImportFile) (*cloudImportPlan, error) {
	plan := newCloudPlan()
	buildCloudPlan(files, plan)
	nodes, err := store.ListByKind(db.KindCloudObject, 20000)
	if err != nil {
		return nil, err
	}
	existingNodes := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if key := cloudNodeKeyOf(n); key != "" {
			existingNodes[key] = true
		}
	}
	edges, err := store.Edges(20000)
	if err != nil {
		return nil, err
	}
	existingEdges := make(map[string][]string, len(edges))
	for _, e := range edges {
		key := strconv.FormatInt(e.From, 10) + "|" + e.Rel + "|" + strconv.FormatInt(e.To, 10)
		existingEdges[key] = db.EdgeRights(e)
	}
	return &cloudImportPlan{Store: store, Plan: plan, ExistingNodes: existingNodes, ExistingEdges: existingEdges}, nil
}

// cloudApplyOutcome 是一次落库的结果（页面导入与 agent 工具共用同一份统计口径）。
type cloudApplyOutcome struct {
	Created  int
	Updated  int
	Linked   int
	Relinked int
	Missing  int
	Failed   int
	Errs     []string
}

// cloudNodeKeyOf 读回一个云节点的身份键。键存在 payload.key 里——
// 与 AD 用 payload.sid 同一个道理：**身份键要能查回来**，否则重导就变成重复建点。
func cloudNodeKeyOf(n *db.Node) string {
	if n == nil || len(n.Payload) == 0 {
		return ""
	}
	var p struct {
		Key string `json:"key"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return p.Key
}

func (s *Server) cloudImportPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.buildCloudImportPlan(w, r)
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
			stubs = append(stubs, map[string]any{"name": n.Name, "category": n.Category, "why": n.StubReason})
		}
	}
	newEdges, existingEdges := 0, 0
	for _, e := range p.Plan.Edges {
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
	if len(p.Plan.Skipped) > 0 {
		resp["skipped"] = p.Plan.Skipped
	}
	writeJSON(w, 200, resp)
}

func (s *Server) cloudImportApply(w http.ResponseWriter, r *http.Request) {
	p, ok := s.buildCloudImportPlan(w, r)
	if !ok {
		return
	}
	out := s.applyCloudImport(p)
	resp := map[string]any{
		"task_id":       p.TaskID,
		"nodes_created": out.Created,
		"nodes_updated": out.Updated,
		"edges_created": out.Linked,
		"edges_merged":  out.Relinked,
		"objects":       p.Plan.objectCounts(),
		"edges":         p.Plan.edgeCounts(),
		"warnings":      p.Plan.Warnings,
	}
	if out.Missing > 0 {
		resp["edges_unresolved"] = out.Missing
	}
	if len(p.Plan.Skipped) > 0 {
		resp["skipped"] = p.Plan.Skipped
	}
	if out.Failed > 0 {
		resp["failures"] = out.Failed
		resp["errors"] = out.Errs
	}
	writeJSON(w, 200, resp)
}

// applyCloudImport 落库。页面导入与 agent 工具走的是**这一份实现**。
func (s *Server) applyCloudImport(p *cloudImportPlan) cloudApplyOutcome {
	keys := make([]string, 0, len(p.Plan.Nodes))
	for k := range p.Plan.Nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	nodeID := map[string]int64{}
	created, updated, failed := 0, 0, 0
	var errs []string
	for _, k := range keys {
		n := p.Plan.Nodes[k]
		payload := n.payload()
		payload["key"] = k
		id, err := findByPayloadKey(p.Store, db.KindCloudObject, k)
		if err != nil {
			failed++
			if len(errs) < adSampleLimit {
				errs = append(errs, fmt.Sprintf("%s：查既有节点失败：%v", n.Name, err))
			}
			continue
		}
		if id > 0 {
			if err := p.Store.SetNodePayload(id, db.KindCloudObject, payload); err != nil {
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
		id, err = p.Store.AddNode(db.KindCloudObject, payload, 0, db.StateCloudActive, "cloud_import", nil)
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

	return cloudApplyOutcome{
		Created: created, Updated: updated, Linked: linked, Relinked: relinked,
		Missing: missing, Failed: failed, Errs: errs,
	}
}

// findByPayloadKey 按 payload.key 找一条云节点（找不到返回 0，不是错误）。
func findByPayloadKey(store *db.ExplorationStore, kind, key string) (int64, error) {
	if key == "" {
		return 0, nil
	}
	return store.FindNodeByPayload(kind, "key", key)
}
