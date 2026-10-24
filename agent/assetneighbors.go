package agent

import (
	"context"
	"encoding/json"
	"fmt"

	actool "github.com/lin9527ya/Norma/tool"
)

// assetNeighbors 查一个资产的直接邻域。
//
// 提示词与 node_detail 的报错一直在教模型调它（"资产请用 list_assets/asset_neighbors"），
// 但这个工具此前**根本不存在**——模型每调一次就浪费一轮，还会开始怀疑提示词里的其它指引。
// 它补上的是关系层：list_assets 只能按字段筛，「这台机器还连着谁」得按域/IP/C 段聚。
func (t *ToolSet) assetNeighbors() actool.CoreTool {
	return readTool("asset_neighbors",
		"查一个资产的**直接邻域**：父域、子资产（子域/服务/端点）、同根兄弟子域、同 IP 的 vhost、同 C 段的其它 IP。\n"+
			"横向/扩面时用它回答「这台机器还连着谁」——list_assets 只能按字段筛，邻域是关系。\n"+
			"★ asset_id 是 list_assets 返回的**资产 id**（不是探索节点 id）；资产全局共享。\n"+
			"★ 每类默认最多 8 条，返回里的 relation 标明关系（parent/child/sibling/same_ip/same_csegment）。",
		obj(map[string]any{
			"asset_id": idp("资产 id（必填）"),
			"limit":    intp("每类最多返回几条（默认 8，上限 50）"),
		}, "asset_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("asset_neighbors 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				AssetID json.RawMessage `json:"asset_id"`
				Limit   int             `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.AssetID)
			if id <= 0 {
				return actool.Errorf("asset_neighbors: asset_id 必填（list_assets 返回的资产 id）"), nil
			}
			nbs, err := t.as.AssetNeighbors(t.taskID, id, a.Limit)
			if err != nil {
				return actool.Errorf("asset_neighbors: " + err.Error()), nil
			}
			if len(nbs) == 0 {
				return actool.Text(fmt.Sprintf("资产 #%d 没有已登记的邻居（父域/子资产/同 IP/同 C 段都为空）："+
					"关系还没进资产库——先用 insert_assets 补，或直接对目标做一轮发现。", id)), nil
			}
			return jsonResult(map[string]any{"asset_id": id, "neighbors": nbs})
		})
}
