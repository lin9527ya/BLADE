package db

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// 通路台账（Chain Ledger）：把「手上这条据点/凭据推进到哪一步」摊成状态。
//
// 与报告侧的 attack_chains 不同：报告只保留"推进过"的路径（没推进的死路会被丢掉，
// server/attack_chains.go），台账要的恰恰是没推进的那部分——它回答的是"该不该继续打"。
// 两边共用同一条判定原语 LootConsumingIntentState，避免口径漂移。
//
// 注意 uses 边是**规划者声明的**（add_intent.uses_loot_ids），worker 执行类工具并不写它。
// 所以字段叫 DeclaredUses，不宣称"观测到的使用"。

const (
	ChainStatusUnused    = "unused"    // 从没被任何意图动用
	ChainStatusOpen      = "open"      // 动过，但没产出，且最后一次没善终
	ChainStatusAttempted = "attempted" // 动过、没产出，但最后一次不是失败（正常中间态）
	ChainStatusAdvanced  = "advanced"  // 动过且产出了下游（事实/漏洞/新战果）
)

// ChainRow 是一条通路的状态。
type ChainRow struct {
	LootID      int64   `json:"loot_id"`
	Kind        string  `json:"kind"` // foothold | credential
	Label       string  `json:"label"`
	Stage       string  `json:"stage"` // access | privilege | lateral
	Status      string  `json:"status"`
	DeclaredUse []int64 `json:"declared_uses,omitempty"`
	Uses        int     `json:"uses"`
	LastOutcome string  `json:"last_outcome,omitempty"`
	Downstream  []int64 `json:"downstream,omitempty"` // 这些意图产出的节点 id
}

// ChainReport 是全部活跃战果的通路状态。
type ChainReport struct {
	Rows   []ChainRow     `json:"rows"`
	Counts map[string]int `json:"counts"`
}

// ActiveLootIDs 判断一个节点 id 是不是活跃战果（报告/审计共用）。
func ActiveLootIDs(ns []*Node) map[int64]bool {
	out := map[int64]bool{}
	for _, n := range ns {
		if n != nil && (n.Kind == KindFoothold || n.Kind == KindCredential) && n.State == StateLootActive {
			out[n.ID] = true
		}
	}
	return out
}

// ChainLedger 构造当前探索的通路台账（只读，单次定点查询 + 两次批量读节点）。
func (s *ExplorationStore) ChainLedger() (*ChainReport, error) {
	if !s.InReadSnapshot() {
		var out *ChainReport
		err := s.ReadSnapshot(context.Background(), func(_ *DB, view *ExplorationStore) error { var err error; out, err = view.ChainLedger(); return err })
		return out, err
	}
	loot := make([]*Node, 0, 64)
	for _, kind := range []string{KindFoothold, KindCredential} {
		ns, err := s.AllByKindState(kind, StateLootActive)
		if err != nil {
			return nil, err
		}
		loot = append(loot, ns...)
	}
	rep := &ChainReport{Rows: []ChainRow{}, Counts: map[string]int{}}
	if len(loot) == 0 {
		return rep, nil
	}

	// uses: intent -> loot；yields: intent -> 下游节点。只取这两种边，按 exploration 定点。
	rows, err := s.db.Query(`SELECT src_id, rel, dst_id FROM exploration_edges
WHERE exploration_id=$1 AND rel IN ('uses','yields')`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usedBy := map[int64][]int64{}   // loot -> intents
	produces := map[int64][]int64{} // intent -> downstream nodes
	intentSet := map[int64]bool{}
	for rows.Next() {
		var src, dst int64
		var rel string
		if err := rows.Scan(&src, &rel, &dst); err != nil {
			return nil, err
		}
		switch rel {
		case RelUses:
			usedBy[dst] = append(usedBy[dst], src)
			intentSet[src] = true
		case RelYields:
			produces[src] = append(produces[src], dst)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 批量取意图状态 + 下游节点类型。
	ids := make([]int64, 0, len(intentSet))
	for id := range intentSet {
		ids = append(ids, id)
	}
	all := make([]int64, 0, len(ids))
	all = append(all, ids...)
	for _, ds := range produces {
		all = append(all, ds...)
	}
	nodes := map[int64]*Node{}
	if len(all) > 0 {
		ns, err := s.NodesByIDs(all)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			nodes[n.ID] = n
		}
	}

	for _, n := range loot {
		row := ChainRow{LootID: n.ID, Kind: n.Kind, Label: nodeSummary(n), Stage: "access"}
		users := append([]int64(nil), usedBy[n.ID]...)
		sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
		row.DeclaredUse = users
		row.Uses = len(users)
		var downstream []int64
		for _, intent := range users {
			downstream = append(downstream, produces[intent]...)
		}
		// 最后一次声明使用这条战果的意图的终态（按 id 倒序取第一个能查到的）。
		for i := len(users) - 1; i >= 0; i-- {
			if in := nodes[users[i]]; in != nil {
				row.LastOutcome = in.State
				break
			}
		}
		sort.Slice(downstream, func(i, j int) bool { return downstream[i] < downstream[j] })
		row.Downstream = downstream

		switch {
		case len(users) == 0:
			row.Status = ChainStatusUnused
		case len(downstream) > 0:
			row.Status = ChainStatusAdvanced
		case IntentStateDead(row.LastOutcome):
			row.Status = ChainStatusOpen
		default:
			// 动过、没产出、但最后一次不是失败（例如 done 只写了资产）：不判 open，
			// 避免把正常中间态误报成缺口。
			row.Status = ChainStatusAttempted
		}
		row.Stage = chainStage(n, downstream, nodes)
		rep.Rows = append(rep.Rows, row)
		rep.Counts[row.Status]++
	}
	sort.Slice(rep.Rows, func(i, j int) bool {
		ri, rj := chainRank(rep.Rows[i].Status), chainRank(rep.Rows[j].Status)
		if ri != rj {
			return ri < rj
		}
		return rep.Rows[i].LootID < rep.Rows[j].LootID
	})
	return rep, nil
}

func chainRank(status string) int {
	switch status {
	case ChainStatusOpen:
		return 0
	case ChainStatusUnused:
		return 1
	default:
		return 2
	}
}

// chainStage 推断阶段：有下游战果/AD/云对象 = 已横向；高权限据点 = 已提权；否则只有访问。
func chainStage(loot *Node, downstream []int64, nodes map[int64]*Node) string {
	lateral := false
	for _, id := range downstream {
		switch nodes[id].Kind {
		case KindFoothold, KindCredential, KindADObject, KindCloudObject:
			lateral = true
		}
	}
	if lateral {
		return "lateral"
	}
	if loot.Kind == KindFoothold {
		switch strings.ToLower(strings.TrimSpace(ParseFoothold(loot).Privilege)) {
		case "root", "system", "admin", "administrator", "da":
			return "privilege"
		}
	}
	if loot.Kind == KindCredential {
		for _, target := range ParseCredential(loot).Targets {
			if target.Verified {
				return "lateral"
			}
		}
	}
	return "access"
}

// nodeSummary 取节点一行式摘要（战果的 payload.summary）。
func nodeSummary(n *Node) string {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	if s, ok := p["summary"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return n.Kind + "#" + strconv.FormatInt(n.ID, 10)
}
