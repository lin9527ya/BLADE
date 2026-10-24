package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lin9527ya/blade/db"
)

// 提示词与 node_detail 的报错一直在教模型调 asset_neighbors，但这个工具此前根本不存在。
// 这条用例同时钉住"工具在工具面上"和"返回关系而不只是资产列表"。
func TestAssetNeighborsTool(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("asset-neighbors", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := d.Exploration(task.ExplorationID)
	tools := NewToolSet(ts, "planner")
	tools.SetTaskID(task.ID)
	tools.SetAssetStore(d.Assets(), nil)

	const domain = "nb-tool.example"
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM assets WHERE domain LIKE $1 OR root_domain LIKE $1`, "%"+domain+"%")
	})
	// 邻域与 list_assets 同一条纪律：只露出本任务范围内的资产——先把域加进授权范围。
	if _, err := d.Assets().AddAgentScope(task.ID, "root_domain", domain, "用例范围", "agent"); err != nil {
		t.Fatal(err)
	}
	root, err := d.Assets().UpsertRootDomain(db.UpsertRootDomainReq{Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := d.Assets().UpsertSubdomain(db.UpsertSubdomainReq{Domain: "api." + domain})
	if err != nil {
		t.Fatal(err)
	}

	out := callTool(t, tools.PlannerTools(), "asset_neighbors", fmt.Sprintf(`{"asset_id":%d}`, sub))
	if !strings.Contains(out, `"relation":"parent"`) || !strings.Contains(out, fmt.Sprintf(`"id":%d`, root)) {
		t.Fatalf("应返回父域邻居：%s", out)
	}
	if !strings.Contains(out, `"label"`) {
		t.Fatalf("邻居应带可读标签：%s", out)
	}

	// 没有资产库时给出可读拒绝，而不是 panic（域工具会被绑到非任务 agent）。
	empty := NewToolSet(nil, "")
	if got := callTool(t, empty.PlannerTools(), "asset_neighbors", `{"asset_id":1}`); !strings.Contains(got, "未启用") {
		t.Fatalf("无 AssetStore 时应明确拒绝：%s", got)
	}
	// 缺参数时拒收。
	if got := callTool(t, tools.PlannerTools(), "asset_neighbors", `{}`); !strings.Contains(got, "必填") {
		t.Fatalf("缺 asset_id 应被拒：%s", got)
	}
}
