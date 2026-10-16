package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lin9527ya/artex/db"
)

// AD 导入的映射单测：吃两种 SharpHound 形状、成员方向、ACL 白名单、DN/知名 RID 的 stub、
// 权限并集。这些是路径重建的地基——错了路径就是错的，而且错得看不出来。

const (
	accUserSID = "S-1-5-21-100-200-300-1105"
	accDASID   = "S-1-5-21-100-200-300-512"
	accDCSID   = "S-1-5-21-100-200-300-1001"
	accAdmSID  = "S-1-5-21-100-200-300-500"
)

// shUser45 是 BloodHound 4.x/5.x 的用户对象（Properties 里是属性，顶层是关系）。
func shUser() string {
	return `[{
	  "SID": "` + accUserSID + `",
	  "ObjectIdentifier": "` + accUserSID + `",
	  "PrimaryGroupSID": "S-1-5-21-100-200-300-513",
	  "AllowedToDelegate": ["` + accDCSID + `"],
	  "Aces": [
	    {"PrincipalSID": "` + accDASID + `", "PrincipalType": "Group", "RightName": "GenericAll", "IsInherited": false},
	    {"PrincipalSID": "` + accDASID + `", "PrincipalType": "Group", "RightName": "ReadProperty", "IsInherited": true}
	  ],
	  "MemberOf": ["CN=Domain Admins,CN=Users,DC=corp,DC=local"],
	  "Properties": {
	    "name": "JDOE@CORP.LOCAL", "samaccountname": "jdoe",
	    "distinguishedname": "CN=John Doe,CN=Users,DC=corp,DC=local",
	    "domain": "CORP.LOCAL", "highvalue": false, "admincount": true,
	    "enabled": true, "hasspn": true, "dontreqpreauth": false,
	    "unconstraineddelegation": false, "description": "helpdesk"
	  }
	}]`
}

func shComputer() string {
	return `[{
	  "SID": "` + accDCSID + `",
	  "ObjectIdentifier": "` + accDCSID + `",
	  "IsDC": true,
	  "Sessions": {"` + accUserSID + `": ["DC"]},
	  "LocalAdmins": {"Collected": true, "Results": [{"ObjectIdentifier": "` + accDASID + `", "ObjectType": "Group"}]},
	  "RemoteDesktopUsers": {"Collected": true, "Results": [{"ObjectIdentifier": "` + accAdmSID + `", "ObjectType": "User"}]},
	  "Aces": [{"PrincipalSID": "` + accUserSID + `", "PrincipalType": "User", "RightName": "DCSync"}],
	  "Properties": {
	    "name": "DC.CORP.LOCAL", "samaccountname": "DC$", "operatingsystem": "Windows Server 2019",
	    "distinguishedname": "CN=DC,OU=Domain Controllers,DC=corp,DC=local", "domain": "CORP.LOCAL", "highvalue": true
	  }
	}]`
}

// shDomain5x 用 5.x/CE 的 {meta,data} 包装，顺带覆盖信任边。
func shDomain5x() string {
	return `{"meta": {"count": 1, "type": "domains"}, "data": [{
	  "SID": "S-1-5-21-100-200-300",
	  "ObjectIdentifier": "S-1-5-21-100-200-300",
	  "Trusts": [{"TargetDomainSid": "S-1-5-21-900-900-900", "TargetDomainName": "OTHER.LOCAL",
	              "TrustDirection": "Bidirectional", "TrustType": "External", "IsTransitive": true}],
	  "Properties": {"name": "CORP.LOCAL", "domain": "CORP.LOCAL",
	                 "distinguishedname": "DC=corp,DC=local", "highvalue": true}
	}], "license": "MIT"}`
}

func shGroup(name, sid string) string {
	return `{
	  "SID": "` + sid + `", "ObjectIdentifier": "` + sid + `",
	  "Members": [{"ObjectIdentifier": "` + accUserSID + `", "ObjectType": "User"}],
	  "Properties": {"name": "` + name + `", "samaccountname": "domain admins",
	                 "distinguishedname": "CN=Domain Admins,CN=Users,DC=corp,DC=local",
	                 "domain": "CORP.LOCAL", "admincount": true, "description": "Designated administrators"}
	}`
}

func filesFor(json string) adImportFile { return adImportFile{Name: "x.json", JSON: json} }

func buildPlanFor(t *testing.T, files ...adImportFile) *adPlan {
	t.Helper()
	p := newADPlan()
	buildADPlan(files, p)
	return p
}

func edgeOf(p *adPlan, from, rel, to string) *adEdge {
	return p.Edges[from+"|"+rel+"|"+to]
}

func TestADNormalizeAcceptsBothSHShapes(t *testing.T) {
	// 4.x 裸数组
	objs, err := normalizeSHResp([]byte(shUser()))
	if err != nil || len(objs) != 1 {
		t.Fatalf("4.x 裸数组该被吃下：err=%v n=%d", err, len(objs))
	}
	// 5.x {meta,data}
	objs, err = normalizeSHResp([]byte(shDomain5x()))
	if err != nil || len(objs) != 1 {
		t.Fatalf("5.x 包装该被吃下：err=%v n=%d", err, len(objs))
	}
	// 单对象
	if objs, err = normalizeSHResp([]byte(shGroup("DOMAIN ADMINS@CORP.LOCAL", accDASID))); err != nil || len(objs) != 1 {
		t.Fatalf("单对象该被吃下：err=%v n=%d", err, len(objs))
	}
	// 不是采集结果 → 必须报错（绝不静默返回空：那会把"文件错了"读成"域里什么都没有"）
	for _, bad := range []string{``, `   `, `not json`, `{"hello":1}`, `{"data": {}}`, `[]`} {
		if _, err := normalizeSHResp([]byte(bad)); err == nil {
			t.Errorf("输入 %q 该报错，却通过了", bad)
		}
	}
}

func TestADMapUserRelations(t *testing.T) {
	p := buildPlanFor(t, filesFor(shUser()))

	n := p.Nodes[accUserSID]
	if n == nil {
		t.Fatalf("用户没被登记：%v", p.Warnings)
	}
	if n.Category != "user" || n.SAM != "jdoe" || !n.AdminCount || !n.HasSPN {
		t.Errorf("用户属性不对：%+v", n)
	}
	if !containsStr(n.Tags, "kerberoastable") {
		t.Errorf("hasspn 该标成可烤：%v", n.Tags)
	}

	// MemberOf 是 DN，采集里没有这个组 → 造 stub，而不是把这条边丢掉。
	da := p.Nodes[accDASID]
	if da == nil || !da.Stub || da.Category != "group" {
		t.Fatalf("未采集的 Domain Admins 该有 stub：%+v", da)
	}
	if !da.HighValue {
		t.Errorf("Domain Admins 该是高价值：%+v", da)
	}
	// 方向：子 --member_of--> 父
	if e := edgeOf(p, accUserSID, db.RelMemberOf, accDASID); e == nil {
		t.Errorf("缺 member_of 边：%v", p.Edges)
	}
	// 只导了 users.json 时：MemberOf 的 DN 该按内置组名还原成 -512 键，
	// 与 ACE 里的 S-1-5-21-…-512 合成同一个节点（两个键 = 图上两个 Domain Admins）。
	if p.Nodes["dn:cn=domain admins,cn=users,dc=corp,dc=local"] != nil {
		t.Errorf("内置组名该还原成 SID 键，而不是另造 dn: stub")
	}
	if e := edgeOf(p, accDASID, db.RelMemberOf, accUserSID); e != nil {
		t.Errorf("member_of 方向反了")
	}
	// 主组也是成员关系（Domain Users -513）
	if e := edgeOf(p, accUserSID, db.RelMemberOf, "S-1-5-21-100-200-300-513"); e == nil {
		t.Errorf("PrimaryGroupSID 该落一条 member_of")
	}

	// ① ACE：白名单内建边，白名单外只计数
	if e := edgeOf(p, accDASID, db.RelCan, accUserSID); e == nil || !containsStr(e.Rights, "GenericAll") {
		t.Errorf("GenericAll 该建一条 can 边：%+v", e)
	}
	if p.SkippedRights["ReadProperty"] != 1 {
		t.Errorf("ReadProperty 该被跳过并计数：%v", p.SkippedRights)
	}
	// ② 委派
	if e := edgeOf(p, accUserSID, db.RelCan, accDCSID); e == nil || !containsStr(e.Rights, "AllowedToDelegate") {
		t.Errorf("AllowedToDelegate 该建边：%+v", e)
	}
}

func TestADMapComputerRelations(t *testing.T) {
	p := buildPlanFor(t, filesFor(shComputer()))

	dc := p.Nodes[accDCSID]
	if dc == nil || dc.Category != "computer" || !dc.HighValue || dc.OS == "" {
		t.Fatalf("机器对象不对：%+v", dc)
	}
	if !containsStr(dc.Tags, "dc") {
		t.Errorf("域控该带 dc 标签：%v", dc.Tags)
	}
	// 会话方向：user --session_on--> computer（利用时反着走）
	if e := edgeOf(p, accUserSID, db.RelSessionOn, accDCSID); e == nil {
		t.Errorf("会话该落 user --session_on--> computer：%v", p.Edges)
	}
	// 本机管理员 → admin_to；远程用户 → can（权限名保留，读得出是 RDP 还是别的）
	if e := edgeOf(p, accDASID, db.RelAdminTo, accDCSID); e == nil {
		t.Errorf("LocalAdmins 该落 admin_to")
	}
	if e := edgeOf(p, accAdmSID, db.RelCan, accDCSID); e == nil || !containsStr(e.Rights, "CanRDP") {
		t.Errorf("RemoteDesktopUsers 该落 can(CanRDP)：%+v", e)
	}
	// 知名 RID 兜底：Administrator(-500) 没被采集，也该有个像样的名字。
	if n := p.Nodes[accAdmSID]; n == nil || n.Name != "Administrator" || !n.Stub {
		t.Errorf("-500 该由知名 RID 认出来：%+v", n)
	}
	// 机器的 DCSync ACE 是别人对它？不——是它对别的主体。这里断言的是 user 对 DC 的 DCSync。
	if e := edgeOf(p, accUserSID, db.RelCan, accDCSID); e == nil || !containsStr(e.Rights, "DCSync") {
		t.Errorf("DCSync 该建 can 边：%+v", e)
	}
}

func TestADMapGroupMembersResolveDN(t *testing.T) {
	// 组与用户一起导：DN 能解析到真 SID，不该再有 stub。
	p := buildPlanFor(t, filesFor(shUser()), filesFor(shGroup("DOMAIN ADMINS@CORP.LOCAL", accDASID)))
	da := p.Nodes[accDASID]
	if da == nil || da.Stub {
		t.Fatalf("组被采集了，就不该是 stub：%+v", da)
	}
	if !da.HighValue {
		t.Errorf("内置高价值组名该被判成高价值：%+v", da)
	}
	if e := edgeOf(p, accUserSID, db.RelMemberOf, accDASID); e == nil {
		t.Errorf("Members 反向也该落同一条边（两个方向都收）")
	}
	if p.Nodes["dn:cn=domain admins,cn=users,dc=corp,dc=local"] != nil {
		t.Errorf("DN 能解析到 SID 时不该再造 dn: stub")
	}
	if p.UnknownObjects != 0 {
		t.Errorf("不该有认不出的对象：%d", p.UnknownObjects)
	}
}

func TestADMapDomainTrust(t *testing.T) {
	p := buildPlanFor(t, filesFor(shDomain5x()))
	dom := p.Nodes["S-1-5-21-100-200-300"]
	if dom == nil || dom.Category != "domain" {
		t.Fatalf("域对象没登记：%+v", dom)
	}
	other := p.Nodes["S-1-5-21-900-900-900"]
	if other == nil || !other.Stub || other.Name != "OTHER.LOCAL" {
		t.Fatalf("信任目标该造域 stub：%+v", other)
	}
	e := edgeOf(p, "S-1-5-21-100-200-300", db.RelCan, "S-1-5-21-900-900-900")
	if e == nil || !containsStr(e.Rights, "trusted_by:bidirectional") {
		t.Errorf("信任该落 can(trusted_by)：%+v", e)
	}
}

func TestADPlanIsDeduplicated(t *testing.T) {
	// 同一份采集导两次（同一批文件喂两遍），计划里的节点与边数量不变。
	once := buildPlanFor(t, filesFor(shUser()), filesFor(shComputer()), filesFor(shDomain5x()))
	twice := buildPlanFor(t, filesFor(shUser()), filesFor(shComputer()), filesFor(shDomain5x()),
		filesFor(shUser()), filesFor(shComputer()), filesFor(shDomain5x()))
	if len(once.Nodes) != len(twice.Nodes) {
		t.Errorf("节点数该幂等：%d vs %d", len(once.Nodes), len(twice.Nodes))
	}
	if len(once.Edges) != len(twice.Edges) {
		t.Errorf("边数该幂等：%d vs %d", len(once.Edges), len(twice.Edges))
	}
}

func TestADRightsUnionOnSameEdge(t *testing.T) {
	// 同一个主体对同一目标的两条 ACE（如 GenericAll + WriteDacl）该并成一条边的 rights，
	// 而不是互相覆盖——否则"能做的"会随文件顺序变。
	u := strings.Replace(shUser(), `"RightName": "ReadProperty"`, `"RightName": "WriteDacl"`, 1)
	p := buildPlanFor(t, filesFor(u))
	e := edgeOf(p, accDASID, db.RelCan, accUserSID)
	if e == nil {
		t.Fatalf("缺 can 边")
	}
	if !containsStr(e.Rights, "GenericAll") || !containsStr(e.Rights, "WriteDacl") {
		t.Errorf("两条权限该并集：%v", e.Rights)
	}
}

func TestADPlanReportsWhatItCouldNotDo(t *testing.T) {
	// 认不出的对象、跳过的权限、没采到的主体：都要如实计数，不静默。
	weird := `[{"SID": "S-1-5-21-1-1-1-1", "Properties": {"name": "MYSTERY"}},
	           {"SID": "", "Properties": {"name": "NOSID"}}]`
	p := buildPlanFor(t, filesFor(weird))
	if p.UnknownObjects != 2 {
		t.Errorf("认不出的对象该计数：%d", p.UnknownObjects)
	}
	if len(p.Warnings) == 0 {
		t.Errorf("有对象认不出时该有 warning")
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// guard：payload 里 stub 必须显式落库，读的人才知道"这个节点没有属性是因为没人采过它"。
func TestADStubPayloadMarked(t *testing.T) {
	p := buildPlanFor(t, filesFor(shUser()))
	raw, _ := json.Marshal(p.Nodes[accDASID].payload())
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["stub"] != true {
		t.Errorf("stub 该显式标记：%s", raw)
	}
	if m["name"] != "Domain Admins" {
		t.Errorf("stub 名该取 DN 的 CN：%s", raw)
	}
}
