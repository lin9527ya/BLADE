package db

import (
	"encoding/json"
	"strings"
	"testing"
)

// AD 路径重建单测：方向（会话反跳）、最短路、环路、起点限制、note。
// 这几条错了，页面上会显示一条"看着很像"但走不通的路——比不显示更糟。

func adPathTestNode(t *testing.T, id int64, name, cat string, highvalue, owned bool) *Node {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"name": name, "category": cat, "highvalue": highvalue, "owned": owned,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Node{ID: id, Kind: KindADObject, Payload: raw, State: StateADActive}
}

func TestADPathReverseSessionHop(t *testing.T) {
	// 经典横向：我们有一个普通账号 → 是 IT 组成员 → IT 组是 WS01 的本机管理员 →
	// WS01 上有 DA 的会话（反跳）→ DA 是 Domain Admins 成员（高价值目标）。
	nodes := []*Node{
		adPathTestNode(t, 1, "jdoe", "user", false, false),
		adPathTestNode(t, 2, "IT Support", "group", false, false),
		adPathTestNode(t, 3, "WS01", "computer", false, false),
		adPathTestNode(t, 4, "da", "user", false, false),
		adPathTestNode(t, 5, "Domain Admins", "group", true, false),
	}
	edges := []Edge{
		{From: 1, Rel: RelMemberOf, To: 2},
		{From: 2, Rel: RelAdminTo, To: 3},
		{From: 4, Rel: RelSessionOn, To: 3}, // 存储方向：da 在 WS01 上有会话
		{From: 4, Rel: RelMemberOf, To: 5},
	}
	res := ADPaths(nodes, edges, 0)
	paths, starts := res.Paths, res.Starts
	// 默认起点只收非特权账号：da 属于 Domain Admins，不算"手头的账号"——
	// 否则「da 一跳走到 Domain Admins」会把真正那条 4 跳的路挤掉。
	if len(starts) != 1 || starts[0].ID != 1 {
		t.Fatalf("默认起点该只剩 jdoe，得到 %v", starts)
	}
	if len(paths) != 1 {
		t.Fatalf("该有且只有一条到 Domain Admins 的路，得到 %d 条：%+v", len(paths), paths)
	}
	p := paths[0]
	if p.Target != "Domain Admins" || p.Start != "jdoe" {
		t.Fatalf("路径两端不对：%+v", p)
	}
	var rels []string
	for _, s := range p.Steps {
		rels = append(rels, s.Rel)
	}
	got := strings.Join(rels, ">")
	if got != "member_of>admin_to>session_on>member_of" {
		t.Fatalf("跳序不对：%s（会话那一跳必须反着走才走得通）", got)
	}
	// 反跳要带上人话，否则读的人以为"WS01 属于 da"
	if !strings.Contains(p.Steps[2].Note, "会话") {
		t.Errorf("会话反跳该有 note：%+v", p.Steps[2])
	}
	if p.Steps[2].From != "WS01" || p.Steps[2].To != "da" {
		t.Errorf("反跳方向该是 机器 → 账号：%+v", p.Steps[2])
	}
}

func TestADPathPrefersOwnedAndShorter(t *testing.T) {
	// 有 owned 账号时只从它出发（用户已经说了"这个账号在我手上"）；
	// 两条路到同一个目标时取更短的那条。
	nodes := []*Node{
		adPathTestNode(t, 1, "owned-acct", "user", false, true),
		adPathTestNode(t, 2, "other", "user", false, false),
		adPathTestNode(t, 3, "DA", "group", true, false),
		adPathTestNode(t, 4, "EA", "group", true, false),
		adPathTestNode(t, 5, "mid", "group", false, false),
	}
	edges := []Edge{
		{From: 1, Rel: RelCan, To: 3, Attrs: json.RawMessage(`{"rights":["GenericAll"]}`)},
		{From: 2, Rel: RelMemberOf, To: 5},
		{From: 5, Rel: RelMemberOf, To: 3},
		{From: 1, Rel: RelMemberOf, To: 4},
	}
	res := ADPaths(nodes, edges, 0)
	paths, starts := res.Paths, res.Starts
	if len(starts) != 1 || starts[0].ID != 1 || !starts[0].Owned {
		t.Fatalf("有 owned 时起点只该是它：%v", starts)
	}
	if len(paths) != 2 {
		t.Fatalf("该报两条（DA/EA 各一条）：%+v", paths)
	}
	if paths[0].Target != "DA" || len(paths[0].Steps) != 1 {
		t.Errorf("最短的那条该排前面：%+v", paths[0])
	}
	if paths[0].Steps[0].Rights[0] != "GenericAll" {
		t.Errorf("can 边该带出权限名：%+v", paths[0].Steps[0])
	}
	if !strings.Contains(paths[0].Steps[0].Note, "写入该组") {
		t.Errorf("对组有 GenericAll 该给一句人话：%+v", paths[0].Steps[0])
	}
}

func TestADPathStartRestrictionAndNoTarget(t *testing.T) {
	nodes := []*Node{
		adPathTestNode(t, 1, "a", "user", false, false),
		adPathTestNode(t, 2, "b", "user", false, false),
		adPathTestNode(t, 3, "DA", "group", true, false),
	}
	edges := []Edge{{From: 1, Rel: RelMemberOf, To: 3}}
	// 指定起点：只有 a 能到
	if paths := ADPaths(nodes, edges, 1).Paths; len(paths) != 1 {
		t.Errorf("指定起点 a 该有一条：%+v", paths)
	}
	// 指定起点 b：到不了 → 空，且**不是错误**
	if paths := ADPaths(nodes, edges, 2).Paths; len(paths) != 0 {
		t.Errorf("b 到不了该返回空：%+v", paths)
	}
	// 高价值目标本身就是起点时不报"自己到自己"
	owned := []*Node{adPathTestNode(t, 9, "DA-acct", "user", true, true)}
	if paths := ADPaths(owned, nil, 0).Paths; len(paths) != 0 {
		t.Errorf("起点即目标不该成路：%+v", paths)
	}
}

func TestADPathCycleTerminates(t *testing.T) {
	// 组成员关系是可能成环的（采集异常/嵌套组），BFS 必须收敛。
	nodes := []*Node{
		adPathTestNode(t, 1, "a", "user", false, false),
		adPathTestNode(t, 2, "g1", "group", false, false),
		adPathTestNode(t, 3, "g2", "group", true, false),
	}
	edges := []Edge{
		{From: 1, Rel: RelMemberOf, To: 2},
		{From: 2, Rel: RelMemberOf, To: 3},
		{From: 3, Rel: RelMemberOf, To: 2}, // 环
	}
	paths := ADPaths(nodes, edges, 0).Paths
	if len(paths) != 1 || len(paths[0].Steps) != 2 {
		t.Fatalf("环里该收敛到最短路：%+v", paths)
	}
}

func TestADPathIgnoresNonADEges(t *testing.T) {
	// 图里混着作业边（spawns/yields/uses）：域内拓扑不该把它们读进来。
	nodes := []*Node{
		adPathTestNode(t, 1, "a", "user", false, false),
		adPathTestNode(t, 2, "DA", "group", true, false),
	}
	edges := []Edge{{From: 1, Rel: RelUses, To: 2}, {From: 1, Rel: RelYields, To: 2}}
	if paths := ADPaths(nodes, edges, 0).Paths; len(paths) != 0 {
		t.Errorf("uses/yields 不该被当成域内路径：%+v", paths)
	}
}

func TestADPathStepsCapped(t *testing.T) {
	// 长链（超过 ADPathMaxSteps）不再往下展开——AD 图里的长链多半是噪声，
	// 而且真要走十几跳，报出来也没人照着做。
	var nodes []*Node
	var edges []Edge
	for i := int64(1); i <= int64(ADPathMaxSteps+3); i++ {
		nodes = append(nodes, adPathTestNode(t, i, "g"+string(rune('a'+i%26)), "group", i == int64(ADPathMaxSteps+3), false))
		edges = append(edges, Edge{From: i, Rel: RelMemberOf, To: i + 1})
	}
	nodes = append(nodes, adPathTestNode(t, 99, "start", "user", false, false))
	edges = append(edges, Edge{From: 99, Rel: RelMemberOf, To: 1})
	paths := ADPaths(nodes, edges, 0).Paths
	if len(paths) != 0 {
		t.Errorf("超过 %d 跳的目标不该报出来：%+v", ADPathMaxSteps, paths)
	}
}
