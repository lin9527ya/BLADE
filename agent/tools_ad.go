package agent

import (
	"context"
	"encoding/json"

	actool "github.com/lin9527ya/Norma/tool"

	"github.com/lin9527ya/artex/db"
)

// ad_paths（P32）：读域内攻击路径。
//
// 数据来自导入的 BloodHound/SharpHound 采集结果（server 侧的 AD 导入），路径重建在
// db/ad_paths.go——与任务页、交付报告读的是**同一份实现**。给 agent 一个读口是必要的：
// 采集结果导进来若只有人能看，规划层就还是"每轮从外面重新打一遍"，而这恰恰是战果层
// （P27）当初要解决的问题。
//
// 只读：这条路表示**存在**这条利用链，要不要走、怎么走仍走既有的意图与命令门。
func (t *ToolSet) adPaths() actool.CoreTool {
	return readTool(
		"ad_paths",
		"读攻击路径：**域内与云上一起读**（域内对象来自导入的 BloodHound/SharpHound 采集结果，"+
			"云上对象来自导入的 AWS 授权明细 / K8s RBAC 枚举结果；两者共用同一套边与同一条路径算法）。"+
			"返回从**非特权身份**（域内账号、云上用户与 ServiceAccount）出发、到高价值目标的"+
			"最短利用链，每跳带关系类型（member_of 成员 / admin_to 本机管理员 / session_on 有会话 / "+
			"can 权限——云上的 attached / inline / assume_role / clusterrole:xxx 都落在 can 上）与一句说明。\n"+
			"注意两点：①「有会话」那一跳是反着走的（你在机器上已有权限 → 机器上有此账号的会话 → 可取它的凭据）；"+
			"②这里只说明路存在，**不代表已实施**——真去打仍要按常规走意图与命令门。\n"+
			"域内数据是空的（没导过采集结果）时返回空列表。可用 start 指定起点节点 id。",
		obj(map[string]any{
			"start": intp("起点节点 id（可选；不传=全部非特权账号）"),
			"limit": intp("最多返回几条路径（默认 10）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("ad_paths 需要任务上下文"), nil
			}
			var a struct {
				Start int64 `json:"start"`
				Limit int   `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}
			nodes, err := t.ts.ListByKind(db.KindADObject, 20000)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(nodes) == 0 {
				return jsonResult(map[string]any{
					"paths": []any{}, "objects": map[string]int{},
					"note": "本任务图上还没有域内数据：需要先导入 BloodHound/SharpHound 的采集结果（任务页「AD 攻击路径」标签）。",
				})
			}
			edges, err := t.ts.Edges(20000)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			res := db.ADPaths(nodes, edges, a.Start)
			paths := res.Paths
			truncated := 0
			if len(paths) > a.Limit {
				truncated = len(paths) - a.Limit
				paths = paths[:a.Limit]
			}
			out := map[string]any{
				"paths": paths, "objects": res.Objects, "edges": res.Edges,
				"highvalue_targets": res.HighValue, "start_candidates": len(res.Starts),
			}
			if truncated > 0 {
				out["truncated"] = truncated
			}
			return jsonResult(out)
		},
	)
}
