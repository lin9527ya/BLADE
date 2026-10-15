package server

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/lin9527ya/artex/db"
	"github.com/lin9527ya/artex/report"
)

// 攻击路径重建：把图里已经记着的两种边接起来读——
//
//	intent --uses-->   战果（这一步动用了哪次已得的访问）
//	intent --yields--> 战果（这一步产出了什么）
//
// **不引入任何新模型**：两种边都已存在（uses 由 add_intent 的 uses_loot_ids 落，yields 由
// report_foothold/report_credential 落），这里只是把它们串成一条读得出来的路。
//
// 节点只取战果（据点/凭据）：它们是「访问」的具象，是路径上的落脚点；findings/facts 是结论，
// 不是通往下一步的台阶，混进来只会让路径变成流水账。
func buildAttackChains(edges []db.Edge, lootLabel map[int64]string, intentSummary, intentState map[int64]string, intentTech map[int64][]string) []report.AttackChain {
	usedBy := map[int64][]int64{}   // 战果 → 动用过它的意图（遍历方向）
	producedBy := map[int64]int64{} // 战果 → 产出它的意图（用来找「起点」）
	produces := map[int64][]int64{} // 意图 → 它产出了哪些战果
	for _, e := range edges {
		switch e.Rel {
		case db.RelUses:
			usedBy[e.To] = append(usedBy[e.To], e.From)
		case db.RelYields:
			// yields 也连 fact/finding——只有战果算路径上的节点，其余跳过。
			if _, isLoot := lootLabel[e.To]; !isLoot {
				continue
			}
			producedBy[e.To] = e.From
			produces[e.From] = append(produces[e.From], e.To)
		}
	}

	// 起点 = 没有被任何一步产出的战果：自己打下来的第一次落脚。
	roots := make([]int64, 0, len(lootLabel))
	for id := range lootLabel {
		if _, produced := producedBy[id]; !produced {
			roots = append(roots, id)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })

	var chains []report.AttackChain
	for _, root := range roots {
		chain := report.AttackChain{Head: lootLabel[root]}
		seen := map[int64]bool{} // 逐条路径去重（同一条路径上不重复走同一个意图；带环也不会死循环）
		queue := []int64{root}
		for len(queue) > 0 {
			loot := queue[0]
			queue = queue[1:]
			for _, intent := range usedBy[loot] {
				if seen[intent] {
					continue
				}
				seen[intent] = true
				kids := produces[intent]
				// 中途没善终、**又什么都没产出**的意图不算路径上的一跳：它没走过去。
				// 但只要它产出了东西（哪怕中途被预算掐断），那产出就是真的，这一跳要留——
				// 与 LootConsumingIntentState 是同一条精神：如实，而不是图省事。
				if len(kids) == 0 && !db.LootConsumingIntentState(intentState[intent]) {
					continue
				}
				step := report.ChainStep{IntentID: intent, Summary: intentSummary[intent], Techniques: intentTech[intent]}
				for _, kid := range produces[intent] {
					step.Produced = append(step.Produced, lootLabel[kid])
					queue = append(queue, kid)
				}
				chain.Steps = append(chain.Steps, step)
			}
		}
		// 只把「真的走出去了」的算路径：起点被用了一下但什么也没产出，那不叫路径，
		// 那只是战果段里已经写过的那一行「动用于」。
		advanced := false
		for _, s := range chain.Steps {
			if len(s.Produced) > 0 {
				advanced = true
				break
			}
		}
		if advanced {
			chains = append(chains, chain)
		}
	}
	return chains
}

// lootLabels 把战果节点压成 id → 可读标签。**带类型前缀**（据点「…」/ 凭据「…」）：路径上
// 每一跳拿到的是据点还是凭据，读的人必须一眼看得出，否则「用 X 拿到 Y」会失去意义。
func lootLabels(groups ...[]*db.Node) map[int64]string {
	kindLabel := map[string]string{db.KindFoothold: "据点", db.KindCredential: "凭据"}
	out := map[int64]string{}
	for _, group := range groups {
		for _, n := range group {
			var p struct {
				Summary string `json:"summary"`
			}
			_ = json.Unmarshal(n.Payload, &p)
			label := strings.TrimSpace(p.Summary)
			if label == "" {
				label = "节点 " + strconv.FormatInt(n.ID, 10) // 没填摘要就退回节点号，不编造
			}
			out[n.ID] = kindLabel[n.Kind] + "「" + label + "」"
		}
	}
	return out
}
