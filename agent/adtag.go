package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// markADOwned 把与 host 对应的 AD 计算机对象标成 owned——「这台机器已经在手」。
//
// 为什么要在据点落库时做：`owned` 是 AD 路径重建的**起点选择**依据
// （db/ad_paths.go 的 Starts 优先从 owned 出发）。此前它只能由导入文件带进来，
// 平台上真的拿下了一台机器，域内路径却不知道——报告里的攻击路径起点永远是
// "用户标的"，而不是"我们打下来的"。
//
// 匹配口径：类别必须是 computer；host 等于对象名，或等于对象名的主机名段
// （web01 ↔ WEB01.CORP.LOCAL）；大小写不敏感，末尾 '$' 去掉。**宁可不匹配，也不要
// 张冠李戴**——错标 owned 会让攻击路径从一个其实没拿下的节点出发。
// markADOwned 标 owned 并返回**匹配到的全部**对象 id（含本来就已经 owned 的）——
// 调用方还要用这些 id 建 reaches 边：谁通到谁，和"在不在手"是两件事。
func (t *ToolSet) markADOwned(host string) []int64 {
	if t.ts == nil || strings.TrimSpace(host) == "" {
		return nil
	}
	ids := t.adObjectsForHost(host)
	for _, id := range ids {
		n, err := t.ts.GetNode(id)
		if err != nil || n == nil {
			continue
		}
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		if owned, _ := p["owned"].(bool); owned {
			continue // 已经是 owned，不用重写
		}
		p["owned"] = true
		_ = t.ts.SetNodePayload(id, db.KindADObject, p)
	}
	return ids
}

// adObjectsForHost 找出 host 对应的 AD 计算机对象（只读，不做任何写入）。
//
// 匹配口径：类别必须是 computer；host 等于对象名，或等于对象名的主机名段
// （web01 ↔ WEB01.CORP.LOCAL）；大小写不敏感，末尾 '$' 去掉。**宁可不匹配，也不要
// 张冠李戴**——错配会让攻击路径从一台其实没拿下的机器出发。
func (t *ToolSet) adObjectsForHost(host string) []int64 {
	if t.ts == nil {
		return nil
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "$"))
	if host == "" {
		return nil
	}
	short := firstLabel(host)
	nodes, err := t.ts.ListByKind(db.KindADObject, 1000)
	if err != nil {
		return nil
	}
	var ids []int64
	for _, n := range nodes {
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(payloadString(p, "category"))) != "computer" {
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(payloadString(p, "name")), "$"))
		if name == "" {
			continue
		}
		if name == host || firstLabel(name) == short {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

// linkReaches 建「这条战果能通到这些 AD 机器」的边。与 owned 分开：一条凭据能登某台机器
// 不等于那台机器已经在手（可能只是能认证），但"通到哪"对规划同样关键。
func (t *ToolSet) linkReaches(lootID int64, hosts ...string) int {
	if t.ts == nil || lootID <= 0 {
		return 0
	}
	linked := 0
	seen := map[int64]bool{}
	for _, host := range hosts {
		for _, aid := range t.adObjectsForHost(host) {
			if seen[aid] {
				continue
			}
			seen[aid] = true
			if err := t.ts.Link(lootID, db.RelReaches, aid); err == nil {
				linked++
			}
		}
	}
	return linked
}

// firstLabel 取 FQDN 的主机名段（web01.corp.local → web01）。
func firstLabel(v string) string {
	if i := strings.Index(v, "."); i > 0 {
		return v[:i]
	}
	return v
}

func payloadString(p map[string]any, key string) string {
	s, _ := p[key].(string)
	if s == "" {
		if v, ok := p[key]; ok && v != nil {
			return fmt.Sprint(v)
		}
	}
	return s
}
