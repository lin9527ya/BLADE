package db

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// AD 攻击路径重建（P32）：在导入进来的域内图上，回答「从手头的账号出发，怎么走到高价值目标」。
//
// 放在 db 而不是 server：**同一条路径，任务页、报告、agent 三处都要读**。写三份必然漂移成
// 三个答案（本仓在 TechniqueTally 上已经吃过一次这个亏），所以实现只有这一份。
//
// 为什么不复用 buildAttackChains（server/attack_chains.go）：那一条走的是 uses/yields 两种边、
// 节点只取战果（据点/凭据），讲的是「这次作业里哪一步动用了哪次访问」——是**作业叙述**。
// AD 路径讲的是「域里本来存在哪条路」——是**拓扑**。两者的边集、节点集、语义都不同，
// 硬合成一条只会两边都读不清。
//
// 遍历方向的取舍（这是本文件唯一需要动脑的地方）：
//   - member_of（子 → 父）、admin_to（主体 → 机器）、can（主体 → 资源）：**顺势走**。
//     进了父组就继承它的权限，进了机器就拿到它的本地管理员位。
//   - session_on（user → computer，即 BloodHound 的 HasSession）：**反着走**。存的方向是
//     「谁在这台机器上有会话」，而利用方向是「我控住了这台机器 → 这台机器上有谁的会话 →
//     取他的凭据」。这是横向里最经典的一跳，方向搞反了它就从图里消失。
const (
	// ADPathMaxSteps 以上的长链不再展开：AD 图里的长链多半是噪声，十几跳的路也没人照着做。
	ADPathMaxSteps = 12
	ADPathMaxPaths = 50
	adStartListCap = 500
)

// adRelPriority 决定同一层里先展开哪条边（数越小越先）。会话最省事（机器已在手，
// 取凭据是本地动作），权限最费事（往往还得再找工具、还要看版本）。
var adRelPriority = map[string]int{
	RelSessionOn: 0, RelAdminTo: 1, RelMemberOf: 2, RelCan: 3,
}

// ADPath 是一条域内攻击路径：从手头的账号出发，沿成员/本机管理员/会话/权限四类边
// 走到一个高价值目标（DA/EA/DC/高价值组）。
//
// 与 report.AttackChain 分开：那一条讲「这次作业里哪一步动用了哪次访问」（作业叙述），
// 这一条讲「域里本来存在哪条路」（拓扑）。两者的边与节点都不同，合成一条只会两边都读不清。
type ADPath struct {
	Start      string   `json:"start"`
	Target     string   `json:"target"`
	TargetTags []string `json:"target_tags,omitempty"` // 目标为什么值钱（域控/可烤/无约束委派…）
	Steps      []ADHop  `json:"steps"`
}

// ADHop 是域内路径上的一跳。
type ADHop struct {
	From   string   `json:"from"`
	Rel    string   `json:"rel"`
	To     string   `json:"to"`
	Rights []string `json:"rights,omitempty"` // can 边的具体权限名
	Note   string   `json:"note,omitempty"`   // 一句人话：这一跳为什么能走
}

// ADStart 是一个可选的起点账号（页面上的下拉、agent 的提示都用它）。
type ADStart struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Owned bool   `json:"owned,omitempty"`
}

// ADPathResult 是一次域内路径读的全部产出：路径 + 统计 + 起点候选。
// 三样一起给，读的人（页面/报告/agent）不必各自再数一遍图。
type ADPathResult struct {
	Paths     []ADPath
	Starts    []ADStart
	Objects   map[string]int // 对象分类计数（含 stub）
	Edges     map[string]int // 四种关系的边数
	Nodes     int
	HighValue int
}

// ADRelLabel 把边类型翻成中文。报告、页面、agent 提示共用同一套词——
// 同一件事在三处叫三个名字，读的人会以为是三件事。
func ADRelLabel(rel string) string {
	switch rel {
	case RelMemberOf:
		return "成员"
	case RelAdminTo:
		return "本机管理员"
	case RelSessionOn:
		return "有会话"
	case RelCan:
		return "权限"
	}
	return rel
}

// ---------- 读模型 ----------

// ADPathNode 是路径重建要用的节点最小集（只取 payload 里那几个字段）。
type ADPathNode struct {
	ID        int64
	Name      string
	Category  string
	HighValue bool
	Stub      bool
	Owned     bool
	Tags      []string
}

// ADPathEdge 是展开后的邻接边。Reverse=true 表示这是会话边的反跳
// （存储方向是 user → computer，遍历方向是 computer → user）。
type ADPathEdge struct {
	To      int64
	Rel     string
	Rights  []string
	Reverse bool
}

// ADPathGraph 是路径重建的邻接表。
type ADPathGraph struct {
	Nodes map[int64]*ADPathNode
	Adj   map[int64][]ADPathEdge
}

// adPathPayload 是 AD 对象 payload 里路径重建要用的那几个字段。
type adPathPayload struct {
	Name      string   `json:"name"`
	Category  string   `json:"category"`
	HighValue bool     `json:"highvalue"`
	Stub      bool     `json:"stub"`
	Owned     bool     `json:"owned"`
	Tags      []string `json:"tags"`
}

// BuildADPathGraph 把图读成邻接表。只认 AD 的四条边——其余边（spawns/yields/…）不属于域内拓扑。
func BuildADPathGraph(nodes []*Node, edges []Edge) *ADPathGraph {
	g := &ADPathGraph{Nodes: map[int64]*ADPathNode{}, Adj: map[int64][]ADPathEdge{}}
	for _, n := range nodes {
		var p adPathPayload
		_ = json.Unmarshal(n.Payload, &p)
		name := p.Name
		if name == "" {
			name = "ad-" + strconv.FormatInt(n.ID, 10)
		}
		g.Nodes[n.ID] = &ADPathNode{
			ID: n.ID, Name: name, Category: p.Category,
			HighValue: p.HighValue, Stub: p.Stub, Owned: p.Owned, Tags: p.Tags,
		}
	}
	for _, e := range edges {
		if _, ok := g.Nodes[e.From]; !ok {
			continue
		}
		if _, ok := g.Nodes[e.To]; !ok {
			continue
		}
		switch e.Rel {
		case RelMemberOf, RelAdminTo, RelCan:
			g.Adj[e.From] = append(g.Adj[e.From], ADPathEdge{To: e.To, Rel: e.Rel, Rights: EdgeRights(e)})
		case RelSessionOn:
			// 反跳：从机器回到在它上面有会话的账号。
			g.Adj[e.To] = append(g.Adj[e.To], ADPathEdge{To: e.From, Rel: e.Rel, Reverse: true})
		}
	}
	for id := range g.Adj {
		list := g.Adj[id]
		sort.Slice(list, func(i, j int) bool {
			pi, pj := adRelPriority[list[i].Rel], adRelPriority[list[j].Rel]
			if pi != pj {
				return pi < pj
			}
			return list[i].To < list[j].To
		})
		g.Adj[id] = list
	}
	return g
}

// ADPaths 是给调用方的主入口：读一批节点与边，算出到高价值目标的最短路 + 统计 + 起点候选。
//
// 页面、报告、agent 工具都走它——三处同源，因此不会出现「页面说走得通、报告里没有」这种事。
func ADPaths(nodes []*Node, edges []Edge, startID int64) ADPathResult {
	g := BuildADPathGraph(nodes, edges)
	paths, starts := g.shortestPaths(startID)

	res := ADPathResult{
		Paths: paths, Starts: starts,
		Objects: map[string]int{}, Edges: map[string]int{},
		Nodes: len(g.Nodes),
	}
	for _, n := range g.Nodes {
		if n.Stub {
			res.Objects["stub"]++
		} else {
			res.Objects[n.Category]++
		}
		if n.HighValue {
			res.HighValue++
		}
	}
	for _, list := range g.Adj {
		for _, e := range list {
			if e.Reverse {
				continue // 反跳边是展开出来的，别把它算成两条
			}
			res.Edges[e.Rel]++
		}
	}
	if res.Paths == nil {
		res.Paths = []ADPath{}
	}
	if res.Starts == nil {
		res.Starts = []ADStart{}
	}
	return res
}

// Starts 给默认起点集：用户明确标记过的（owned）优先；一个都没有时，用全部**非特权**账号——
// 「还没拿到任何账号时，谁能走到 DA」正是红队第一个要问的问题。指定 only 时只从它出发。
func (g *ADPathGraph) Starts(only int64) []int64 {
	if only > 0 {
		if _, ok := g.Nodes[only]; ok {
			return []int64{only}
		}
		return nil
	}
	var owned, users []int64
	for id, n := range g.Nodes {
		if n.Owned {
			owned = append(owned, id)
			continue
		}
		// 起点 = 能**被握在手里**的身份：用户与 ServiceAccount（云上泄漏的 SA token 与口令一样
		// 常见，而且往往就是那条路的开头）。
		//
		// **不含角色**：角色是"代入的目标"（信任边正指向它），把它当起点会让"某角色挂着 admin
		// 策略"这种一跳结论把真正那条路挤掉——与"DA 一跳走到 DA"是同一个错误。
		if (n.Category == "user" || n.Category == "service_account") && !g.privileged(id) {
			users = append(users, id)
		}
	}
	pick := owned
	if len(pick) == 0 {
		pick = users
	}
	sort.Slice(pick, func(i, j int) bool { return pick[i] < pick[j] })
	return pick
}

// privileged 判一个账号是不是已经站在高价值一侧：自己就是高价值，或属于某个高价值组。
//
// 它们**不进默认起点集**——多源 BFS 报的是全局最短路，「用 DA 账号走到 DA」这种一跳
// 会把真正有价值的那条（普通账号 → 组 → 机器 → 会话 → DA）挤掉，等于这个视图什么都没说。
// 用户显式指定起点、或把它标成 owned 时不受此限：那时"从它出发"是用户的明确意思。
func (g *ADPathGraph) privileged(id int64) bool {
	if n := g.Nodes[id]; n != nil && n.HighValue {
		return true
	}
	for _, e := range g.Adj[id] {
		if e.Rel != RelMemberOf {
			continue
		}
		if t := g.Nodes[e.To]; t != nil && t.HighValue {
			return true
		}
	}
	return false
}

// ADStep 是回溯用的一跳（prev 表里存的是「从谁、走哪条边到的这里」）。
type ADStep struct {
	Edge ADPathEdge
	From int64
}

// shortestPaths 多源 BFS：每个高价值目标只报**最短**的那条路（同长度时按 adRelPriority 取更省事的）。
func (g *ADPathGraph) shortestPaths(startID int64) ([]ADPath, []ADStart) {
	starts := g.Starts(startID)
	if len(starts) == 0 {
		return nil, nil
	}
	isStart := map[int64]bool{}
	prev := map[int64]ADStep{}
	visited := map[int64]bool{}
	depth := map[int64]int{}
	queue := make([]int64, 0, len(starts))
	for _, s := range starts {
		if visited[s] {
			continue
		}
		visited[s] = true
		isStart[s] = true
		queue = append(queue, s)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if depth[cur] >= ADPathMaxSteps {
			continue
		}
		for _, e := range g.Adj[cur] {
			if visited[e.To] {
				continue
			}
			visited[e.To] = true
			depth[e.To] = depth[cur] + 1
			prev[e.To] = ADStep{Edge: e, From: cur}
			queue = append(queue, e.To)
		}
	}

	targets := make([]int64, 0, 32)
	for id, n := range g.Nodes {
		if !n.HighValue || isStart[id] || !visited[id] {
			continue
		}
		targets = append(targets, id)
	}
	sort.Slice(targets, func(i, j int) bool {
		if depth[targets[i]] != depth[targets[j]] {
			return depth[targets[i]] < depth[targets[j]]
		}
		return targets[i] < targets[j]
	})
	if len(targets) > ADPathMaxPaths {
		targets = targets[:ADPathMaxPaths]
	}

	out := make([]ADPath, 0, len(targets))
	for _, t := range targets {
		steps := []ADStep{}
		for cur := t; ; {
			s, ok := prev[cur]
			if !ok {
				break
			}
			steps = append(steps, s)
			cur = s.From
			if isStart[cur] {
				break
			}
		}
		// 反着走回来（prev 是从目标回溯到起点）。
		for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
			steps[i], steps[j] = steps[j], steps[i]
		}
		if len(steps) == 0 {
			continue
		}
		head := steps[0].From
		p := ADPath{Start: g.name(head), Target: g.name(t), TargetTags: g.Nodes[t].Tags}
		for _, s := range steps {
			p.Steps = append(p.Steps, ADHop{
				From:   g.name(s.From),
				Rel:    s.Edge.Rel,
				To:     g.name(s.Edge.To),
				Rights: s.Edge.Rights,
				Note:   g.hopNote(s),
			})
		}
		out = append(out, p)
	}

	startList := make([]ADStart, 0, len(starts))
	for _, id := range starts {
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		startList = append(startList, ADStart{ID: id, Name: n.Name, Owned: n.Owned})
	}
	if startID <= 0 && len(startList) > adStartListCap {
		startList = startList[:adStartListCap]
	}
	return out, startList
}

func (g *ADPathGraph) name(id int64) string {
	if n, ok := g.Nodes[id]; ok {
		return n.Name
	}
	return "ad-" + strconv.FormatInt(id, 10)
}

// hopNote 给一跳写一句人话——路径读起来是「怎么走」，不是边类型表。
func (g *ADPathGraph) hopNote(s ADStep) string {
	target := g.Nodes[s.Edge.To]
	if s.Edge.Reverse {
		return "该机器上有此账号的会话：控住机器即可取其凭据（横向最经典的一跳）"
	}
	if s.Edge.Rel != RelCan {
		return ""
	}
	rights := s.Edge.Rights
	has := func(want ...string) bool {
		for _, r := range rights {
			for _, w := range want {
				if r == w {
					return true
				}
			}
		}
		return false
	}
	switch {
	// 云上（P39）：权利名是 attached/inline/assume_role/roleRef 那几种，与域内的 ACE 名不重叠，
	// 所以两类分支可以并列判、不会互相误伤。
	case target != nil && target.Category == "policy" && has("attached", "inline"):
		return "该主体被授予此策略：它允许的动作与资源就此到手"
	case target != nil && target.Category == "role" && has("assume_role"):
		return "可代入该角色（承担它的权限）"
	case target != nil && target.Category == "role" && len(rights) > 0 &&
		(strings.HasPrefix(rights[0], "clusterrole:") || strings.HasPrefix(rights[0], "role:")):
		return "该绑定把此角色授予它，随后受角色规则约束"
	case target != nil && target.Category == "group" && has("GenericAll", "WriteDacl", "WriteOwner", "Owner", "AddMember", "GenericWrite"):
		return "可写入该组（加成员或改 ACL），随后继承它下面的一切权限"
	case target != nil && target.Category == "user" && has("ForceChangePassword", "GenericAll", "WriteDacl", "WriteOwner", "Owner", "AddKeyCredentialLink"):
		return "可重置口令或改 ACL 接管该账号，随后以其身份行动"
	case target != nil && target.Category == "computer" && has("CanRDP", "CanPSRemote", "ExecuteDCOM", "AllowedToAuthenticate"):
		return "可远程登录该机器"
	case has("DCSync", "GetChanges", "GetChangesAll"):
		return "可同步目录数据库（DCSync）：直接取域内任意账号的哈希"
	case has("AllowedToDelegate"):
		return "可委派身份（冒充任意用户访问它）"
	default:
		return ""
	}
}
