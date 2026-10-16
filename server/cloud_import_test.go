package server

import (
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 云上枚举结果导入（P39）的单测：吃 AWS 授权明细与 K8s RBAC 两种形状、
// 认得出管理员等价、把"谁可以代入谁/谁被绑定了什么角色"落成边，未建模的东西如实计数。
//
// 这些是路径的地基——地基错了，页面会显示一条"看着像"但走不通的云上提权链。

const awsFixture = `{
  "UserDetailList": [
    {"Path":"/","UserName":"alice","UserId":"AIDA1","Arn":"arn:aws:iam::123456789012:user/alice",
     "GroupList":["admins"],
     "AttachedManagedPolicies":[{"PolicyName":"AdministratorAccess","PolicyArn":"arn:aws:iam::aws:policy/AdministratorAccess"}]},
    {"Path":"/","UserName":"bob","UserId":"AIDA2","Arn":"arn:aws:iam::123456789012:user/bob",
     "GroupList":[],
     "UserPolicyList":[{"PolicyName":"bucket-read","PolicyDocument":{"Version":"2012-10-17",
       "Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":"arn:aws:s3:::b/*"}]}}]}
  ],
  "GroupDetailList": [
    {"Path":"/","GroupName":"admins","GroupId":"AGPA1","Arn":"arn:aws:iam::123456789012:group/admins",
     "AttachedManagedPolicies":[{"PolicyName":"AdministratorAccess","PolicyArn":"arn:aws:iam::aws:policy/AdministratorAccess"}]}
  ],
  "RoleDetailList": [
    {"Path":"/","RoleName":"app-role","RoleId":"AROA1","Arn":"arn:aws:iam::123456789012:role/app-role",
     "AssumeRolePolicyDocument":{"Version":"2012-10-17","Statement":[
       {"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:user/alice"},"Action":"sts:AssumeRole"},
       {"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]},
     "AttachedManagedPolicies":[{"PolicyName":"s3-read","PolicyArn":"arn:aws:iam::123456789012:policy/s3-read"}]}
  ],
  "Policies": [
    {"PolicyName":"AdministratorAccess","Arn":"arn:aws:iam::aws:policy/AdministratorAccess",
     "PolicyVersionList":[{"IsDefaultVersion":true,"Document":{"Version":"2012-10-17",
       "Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}}]},
    {"PolicyName":"s3-read","Arn":"arn:aws:iam::123456789012:policy/s3-read",
     "PolicyVersionList":[{"IsDefaultVersion":true,"Document":{"Version":"2012-10-17",
       "Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:ListBucket"],"Resource":"*"}]}}]}
  ]
}`

const k8sFixture = `{"kind":"List","items":[
  {"kind":"ClusterRole","metadata":{"name":"cluster-admin"},
   "rules":[{"apiGroups":["*"],"resources":["*"],"verbs":["*"]}]},
  {"kind":"ClusterRoleBinding","metadata":{"name":"alice-admin"},
   "roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"cluster-admin"},
   "subjects":[{"kind":"User","name":"alice"}]},
  {"kind":"ClusterRole","metadata":{"name":"view"},
   "rules":[{"apiGroups":[""],"resources":["pods"],"verbs":["get","list"]}]},
  {"kind":"Role","metadata":{"name":"pod-reader","namespace":"ns1"},
   "rules":[{"apiGroups":[""],"resources":["pods","secrets"],"verbs":["get","list"]}]},
  {"kind":"RoleBinding","metadata":{"name":"reader-binding","namespace":"ns1"},
   "roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"pod-reader"},
   "subjects":[{"kind":"ServiceAccount","name":"system:serviceaccount:ns1:reader","namespace":"ns1"},
               {"kind":"Group","name":"system:authenticated"}]},
  {"kind":"ServiceAccount","metadata":{"name":"reader","namespace":"ns1"}},
  {"kind":"Namespace","metadata":{"name":"ns1"}}
]}`

func cloudPlanFor(t *testing.T, files ...graphImportFile) *cloudPlan {
	t.Helper()
	p := newCloudPlan()
	buildCloudPlan(files, p)
	return p
}

func cloudEdgeOf(p *cloudPlan, from, rel, to string) *cloudEdge {
	return p.Edges[from+"|"+rel+"|"+to]
}

func TestCloudParseRejectsJunk(t *testing.T) {
	for _, bad := range []string{``, `  `, `not json`, `{"hello":1}`, `[]`, `{"items":[]}`} {
		if _, err := parseCloudDoc([]byte(bad)); err == nil {
			t.Errorf("输入 %q 该报错（绝不能静默返回空：那会把「文件错了」读成「云上什么都没有」）", bad)
		}
	}
	for name, raw := range map[string]string{"aws": awsFixture, "k8s": k8sFixture} {
		if _, err := parseCloudDoc([]byte(raw)); err != nil {
			t.Errorf("%s 该被吃下：%v", name, err)
		}
	}
}

func TestCloudAWSPlan(t *testing.T) {
	p := cloudPlanFor(t, graphImportFile{Name: "authz.json", JSON: awsFixture})

	alice := p.Nodes["arn:aws:iam::123456789012:user/alice"]
	admins := p.Nodes["arn:aws:iam::123456789012:group/admins"]
	adminPol := p.Nodes["arn:aws:iam::aws:policy/AdministratorAccess"]
	if alice == nil || alice.Category != "user" {
		t.Fatalf("用户没登记：%+v", alice)
	}
	if admins == nil || admins.Category != "group" || admins.Stub {
		t.Fatalf("组没登记：%+v", admins)
	}
	// 管理员等价的托管策略：Action * + Resource * → 高价值 + admin 标签。
	if adminPol == nil || !adminPol.HighValue || !listHas(adminPol.Tags, "admin") {
		t.Fatalf("AdministratorAccess 该是高价值：%+v", adminPol)
	}
	// 方向：用户 --member_of--> 组（与 AD 侧同一套边，路径算法因此能一起走）。
	if e := cloudEdgeOf(p, "arn:aws:iam::123456789012:user/alice", db.RelMemberOf, "arn:aws:iam::123456789012:group/admins"); e == nil {
		t.Errorf("缺 member_of 边：%v", p.Edges)
	}
	if e := cloudEdgeOf(p, "arn:aws:iam::123456789012:group/admins", db.RelCan, "arn:aws:iam::aws:policy/AdministratorAccess"); e == nil || !listHas(e.Rights, "attached") {
		t.Errorf("组 → 托管策略该是一条 can(attached) 边：%+v", e)
	}
	// 信任策略：alice 可以代入 app-role。
	if e := cloudEdgeOf(p, "arn:aws:iam::123456789012:user/alice", db.RelCan, "arn:aws:iam::123456789012:role/app-role"); e == nil || !listHas(e.Rights, "assume_role") {
		t.Errorf("信任策略该落一条 can(assume_role)：%+v", e)
	}
	// 内联策略：建节点 + can(inline) 边。
	var inlineKey string
	for k := range p.Nodes {
		if strings.HasPrefix(k, "inline:") {
			inlineKey = k
		}
	}
	if inlineKey == "" {
		t.Fatalf("内联策略该建节点：%v", p.Nodes)
	}
	if e := cloudEdgeOf(p, "arn:aws:iam::123456789012:user/bob", db.RelCan, inlineKey); e == nil || !listHas(e.Rights, "inline") {
		t.Errorf("内联策略该落 can(inline)：%+v", e)
	}
	if p.Nodes[inlineKey].HighValue {
		t.Error("s3:GetObject 不该被判成管理员等价——判宽了会让「高价值目标」这张表失去分辨力")
	}
	// 服务主体不建模，但**如实计数**（「谁能 assume」少一条是最容易被忽略的缺口）。
	if p.Skipped["Service 主体（Service 主体，不是图上的身份）"] == 0 &&
		len(p.Skipped) == 0 {
		t.Errorf("服务主体该被如实计数：%+v", p.Skipped)
	}
	// 账号节点。
	if p.Nodes["aws:account:123456789012"] == nil {
		t.Errorf("该建出账号节点（归属可读）：%v", p.Nodes)
	}
}

func TestCloudK8sPlan(t *testing.T) {
	p := cloudPlanFor(t, graphImportFile{Name: "rbac.json", JSON: k8sFixture})

	ca := p.Nodes["k8s:clusterrole:cluster-admin"]
	if ca == nil || !ca.HighValue {
		t.Fatalf("cluster-admin 该是高价值：%+v", ca)
	}
	if e := cloudEdgeOf(p, "k8s:user:alice", db.RelCan, "k8s:clusterrole:cluster-admin"); e == nil {
		t.Errorf("绑定该落成 主体 → 角色 的 can 边：%v", p.Edges)
	} else if !listHas(e.Rights, "clusterrole:cluster-admin") {
		t.Errorf("权利名该写清是哪一个角色：%+v", e.Rights)
	}
	// 服务账号归一：绑定里写 system:serviceaccount:ns1:reader，对象是 ns1/reader —— 同一个键。
	if p.Nodes["k8s:sa:ns1/reader"] == nil {
		t.Errorf("ServiceAccount 该按 ns/name 归一：%v", p.Nodes)
	}
	if e := cloudEdgeOf(p, "k8s:sa:ns1/reader", db.RelCan, "k8s:role:ns1/pod-reader"); e == nil {
		t.Errorf("SA → 命名空间角色该有边：%v", p.Edges)
	}
	// 宽主体：system:authenticated 该被标出来（它意味着「任何已认证用户」）。
	g := p.Nodes["k8s:group:system:authenticated"]
	if g == nil || !listHas(g.Tags, "broad") {
		t.Errorf("system:authenticated 该带 broad 标签：%+v", g)
	}
	// 命名空间不是身份也不是授权：如实计数，不建点。
	if p.Nodes["k8s:namespace:ns1"] != nil {
		t.Error("Namespace 不该建节点")
	}
	if len(p.Skipped) == 0 {
		t.Errorf("未建模的东西该如实计数：%+v", p.Skipped)
	}
	// 普通角色（只有 get/list pods）不是高价值。
	if view := p.Nodes["k8s:clusterrole:view"]; view == nil || view.HighValue {
		t.Errorf("view 角色不该是高价值：%+v", view)
	}
}

func TestCloudPlanIsIdempotent(t *testing.T) {
	once := cloudPlanFor(t, graphImportFile{Name: "a.json", JSON: awsFixture}, graphImportFile{Name: "k.json", JSON: k8sFixture})
	twice := cloudPlanFor(t,
		graphImportFile{Name: "a.json", JSON: awsFixture}, graphImportFile{Name: "k.json", JSON: k8sFixture},
		graphImportFile{Name: "a2.json", JSON: awsFixture}, graphImportFile{Name: "k2.json", JSON: k8sFixture})
	if len(once.Nodes) != len(twice.Nodes) || len(once.Edges) != len(twice.Edges) {
		t.Errorf("同一份枚举导两次该幂等：nodes %d/%d edges %d/%d",
			len(once.Nodes), len(twice.Nodes), len(once.Edges), len(twice.Edges))
	}
}

func TestCloudPlanBadFileIsReported(t *testing.T) {
	p := cloudPlanFor(t, graphImportFile{Name: "notes.txt", JSON: "这不是 JSON"})
	if len(p.Warnings) == 0 {
		t.Error("解析不了的文件要如实报出来，不能静默跳过")
	}
	if len(p.Nodes) != 0 {
		t.Errorf("坏文件不该产生节点：%v", p.Nodes)
	}
}
