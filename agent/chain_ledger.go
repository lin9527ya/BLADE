package agent

import (
	"context"
	"encoding/json"
	"strconv"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// 通路台账（agent 侧）：每轮只算一次，供态势与 chain_detail 工具共用。

func (t *ToolSet) chainReport() *db.ChainReport {
	if t.ts == nil || t.taskID <= 0 {
		return nil
	}
	if t.chainsLoaded {
		return t.chainsCached
	}
	t.chainsLoaded = true
	if rep, err := t.ts.ChainLedger(); err == nil {
		t.chainsCached = rep
	}
	return t.chainsCached
}

// chainDetail 读一条通路（或全部）并给出**基于现有工具**的下一步建议。
// 建议是文本，不替模型做决定：它指向 asset_neighbors / ad_paths / verify_credential 等已有能力。
func (t *ToolSet) chainDetail() actool.CoreTool {
	return t.readExpTool("chain_detail",
		"查询本任务「通路台账」：每个据点/凭据被哪些意图声明使用过（uses_loot_ids）、产出了什么、"+
			"推进到哪个阶段（access→privilege→lateral），以及下一步建议。\n"+
			"关键区分：unused 从没动过；open 动过但没产出且最后一次没善终；advanced 已产出下游。"+
			"\"用过一次\"不等于推进——看 stage 与 downstream。",
		obj(map[string]any{
			"loot_id": idp("只看某条战果（foothold/credential 节点 id；省略=全部）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil || t.taskID <= 0 {
				return actool.Errorf("chain_detail 需要任务上下文"), nil
			}
			var a struct {
				LootID json.RawMessage `json:"loot_id"`
			}
			_ = json.Unmarshal(in, &a)
			want := pid(a.LootID)
			rep := t.chainReport()
			if rep == nil || len(rep.Rows) == 0 {
				return jsonResult(map[string]any{"chains": []any{}, "note": "手上还没有据点或凭据"})
			}
			out := make([]map[string]any, 0, len(rep.Rows))
			for _, r := range rep.Rows {
				if want > 0 && r.LootID != want {
					continue
				}
				item := map[string]any{
					"loot_id": r.LootID, "kind": r.Kind, "label": r.Label,
					"stage": r.Stage, "status": r.Status, "uses": r.Uses,
					"last_outcome": r.LastOutcome, "downstream": r.Downstream,
				}
				item["next_steps"] = t.chainNextSteps(r)
				out = append(out, item)
			}
			if want > 0 && len(out) == 0 {
				return actool.Errorf("这是一条不存在或已失效的战果（只看 active 的据点/凭据）"), nil
			}
			return jsonResult(map[string]any{"counts": rep.Counts, "chains": out})
		})
}

// chainNextSteps 把"下一步该做什么"投影成建议，全部指向已有工具/信号。
func (t *ToolSet) chainNextSteps(r db.ChainRow) []string {
	steps := []string{}
	switch r.Status {
	case db.ChainStatusUnused:
		steps = append(steps, "还没被任何意图动用：派一条意图并带 uses_loot_ids=["+strconv.FormatInt(r.LootID, 10)+"]，写清要用它做什么")
	case db.ChainStatusOpen:
		steps = append(steps, "上一次没有产出就收场了：换角度重派（别原样重试），或写明为什么放弃")
	}
	if r.Kind == db.KindCredential && t.ts != nil {
		if n, err := t.ts.GetNode(r.LootID); err == nil && n != nil {
			pending := 0
			for _, tgt := range db.ParseCredential(n).Targets {
				if !tgt.Verified {
					pending++
				}
			}
			if pending > 0 {
				steps = append(steps, "有 "+strconv.Itoa(pending)+" 条未验证的目标：先 verify_credential 确认能不能登，再谈横向")
			}
		}
	}
	if r.Stage == "access" {
		steps = append(steps, "阶段还在 access：查 asset_neighbors(asset_id)/ad_paths 找提权或横向入口")
	}
	if r.Status == db.ChainStatusAdvanced {
		steps = append(steps, "已产出下游：用 node_detail 看下游事实/战果，决定继续深入还是收束")
	}
	return steps
}
