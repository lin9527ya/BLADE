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
func (t *ToolSet) markADOwned(host string) int {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "$"))
	if host == "" {
		return 0
	}
	short := firstLabel(host)
	nodes, err := t.ts.ListByKind(db.KindADObject, 1000)
	if err != nil {
		return 0
	}
	marked := 0
	for _, n := range nodes {
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(payloadString(p, "category"))) != "computer" {
			continue
		}
		if owned, _ := p["owned"].(bool); owned {
			continue // 已经是 owned，不用重写
		}
		name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(payloadString(p, "name")), "$"))
		if name == "" {
			continue
		}
		if name != host && firstLabel(name) != short {
			continue
		}
		p["owned"] = true
		if err := t.ts.SetNodePayload(n.ID, db.KindADObject, p); err != nil {
			continue
		}
		marked++
	}
	return marked
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
