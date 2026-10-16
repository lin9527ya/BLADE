package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// P23 公司级知识层（agent 侧）：把"这家公司已知什么"作为**只读背景知识**注入
// 态势（graph_overview）与常驻索引；沉淀走显式工具 promote_knowledge。
//
// 三条纪律：
//   - **只读**：注入面绝不参与 planner 的"本任务已做过"去重（那是本任务图的事），
//     只作背景——"别的任务测过 X"不等于"本任务测过 X"。
//   - **带来源**：每行都标来自哪个任务，人可回溯；没有来源的结论不进知识层。
//   - **注入面收紧**：最多 20 行、一行式截断（与常驻索引同一条纪律：它是"别忘了
//     这家公司已知什么"的保险，不是全量知识库）。

// knowledgeKindLabels 是给模型看的中文标签（与 db 侧词表一一对应）。
var knowledgeKindLabels = map[string]string{
	db.KnowledgeKindFact:         "事实",
	db.KnowledgeKindConclusion:   "结论",
	db.KnowledgeKindAssetHint:    "资产线索",
	db.KnowledgeKindPlaybookHint: "打法提示",
}

// taskCompanyIDs 解析本任务关联的公司 id（task_scope 里的 company 行）。
func (t *ToolSet) taskCompanyIDs() []int64 {
	if t.as == nil || t.taskID <= 0 {
		return nil
	}
	rows, err := t.as.ListTaskScope(t.taskID)
	if err != nil {
		return nil
	}
	seen := map[int64]bool{}
	var out []int64
	for _, r := range rows {
		if r.Kind != "company" || r.CompanyID == nil || *r.CompanyID <= 0 {
			continue
		}
		id := *r.CompanyID
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// companyKnowledgeRows 取本任务可读的公司知识（生效中；最多 20 条）。
func (t *ToolSet) companyKnowledgeRows() []db.CompanyKnowledge {
	ids := t.taskCompanyIDs()
	if len(ids) == 0 {
		return nil
	}
	rows, err := t.as.ListCompanyKnowledge(ids, 20)
	if err != nil {
		return nil
	}
	return rows
}

// renderKnowledgeLine 渲染一行式（态势与索引共用，措辞一致）。
func renderKnowledgeLine(k db.CompanyKnowledge) string {
	label := knowledgeKindLabels[k.Kind]
	if label == "" {
		label = k.Kind
	}
	line := fmt.Sprintf("[%s] %s", label, clipKnowledge(k.Summary, 160))
	if k.Confidence != "" {
		line += "（置信度 " + k.Confidence + "）"
	}
	if k.SourceTaskID != "" {
		line += "（来自任务 #" + k.SourceTaskID + "）"
	}
	return line
}

func clipKnowledge(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// promoteKnowledge 是沉淀工具（planner/mainagent 可用，worker 不绑）：把本任务图里
// 一条已确认的 fact/finding 的结论沉淀到公司知识。
func (t *ToolSet) promoteKnowledge() actool.CoreTool {
	return t.writeExpTool("promote_knowledge",
		"把本任务已确认的一条结论**沉淀为公司级知识**，供同公司的其它任务当只读背景读到。\n"+
			"用在哪：某个结论对「这家公司」长期成立、值得后来人先知道（如「WAF 是 X 会拦 SQLi 关键字」"+
			"「登录口在 /admin 有图形验证码」「上次的越权读取已修复」）。\n"+
			"⚠️ 只沉淀**已确认**的结论（有证据的 fact/finding）；猜测、未验证的方向不要沉淀——"+
			"沉淀得越随意，后来人越不敢信。\n"+
			"⚠️ 只读背景：其它任务看得到，但改不了（废弃/恢复由人在页面上做）。\n"+
			"公司必须是本任务关联的企业之一（本工具不接受任意 company_id）。",
		obj(map[string]any{
			"company_id": idp("目标企业 id（必须是本任务关联的企业；列表见 list_companies）"),
			"node_id":    idp("来源节点 id：本任务图里已确认的 fact/finding（结论来源，可回溯）"),
			"summary":    str("一行式结论（后来人先看这句）。留空则取来源节点的摘要"),
			"detail":     str("可选：展开说明（为什么成立、怎么验证、适用边界）"),
			"kind":       str("条目类型：fact 事实 | conclusion 结论 | asset_hint 资产线索 | playbook_hint 打法提示（默认 fact）"),
			"confidence": str("可选置信度：high | medium | low（没把握就留空，平台不猜）"),
		}, "company_id", "node_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				CompanyID  int64  `json:"company_id"`
				NodeID     int64  `json:"node_id"`
				Summary    string `json:"summary"`
				Detail     string `json:"detail"`
				Kind       string `json:"kind"`
				Confidence string `json:"confidence"`
			}
			_ = json.Unmarshal(in, &a)
			if t.as == nil {
				return actool.Errorf("资产/企业库未初始化，无法沉淀公司知识"), nil
			}
			if a.CompanyID <= 0 || a.NodeID <= 0 {
				return actool.Errorf("company_id 与 node_id 都必填"), nil
			}
			// 公司必须是本任务关联的企业之一：知识写错公司比不写更糟（后来人会当真的用）。
			allowed := t.taskCompanyIDs()
			ok := false
			for _, id := range allowed {
				if id == a.CompanyID {
					ok = true
					break
				}
			}
			if !ok {
				if len(allowed) == 0 {
					return actool.Errorf("本任务没有关联任何企业，无法沉淀公司知识；请先在任务里关联企业（company_ids）"), nil
				}
				return actool.Errorf(fmt.Sprintf("company_id %d 不是本任务关联的企业（可用：%v）——知识写错公司会误导后来人", a.CompanyID, allowed)), nil
			}
			// 来源节点必须在本任务可见（本任务或直接上游），且是已确认的 fact/finding。
			node, err := t.ts.GetNodeWithSources(a.NodeID)
			if err != nil || node == nil {
				return actool.Errorf(fmt.Sprintf("来源节点 %d 在本任务（含直接关联任务）里不存在", a.NodeID)), nil
			}
			if node.Kind != db.KindFact && node.Kind != db.KindFinding {
				return actool.Errorf(fmt.Sprintf("来源节点 %d 是 %q 节点：只有已确认的 fact/finding 能作为沉淀来源", a.NodeID, node.Kind)), nil
			}
			if node.State != "confirmed" {
				return actool.Errorf(fmt.Sprintf("来源节点 %d 当前状态是 %q：只沉淀已确认（confirmed）的结论", a.NodeID, node.State)), nil
			}
			// 图节点状态不反映 triage 状态：撤回/误报只翻 findings 行——必须另查一次，
			// 否则"被复测推翻的结论"照样能沉淀出去（验收抓出来的）。
			if node.Kind == db.KindFinding {
				if st, err := t.ts.FindingStatusByNodeID(a.NodeID); err == nil {
					if err := db.FindingPromotableForKnowledge(st); err != nil {
						return actool.Errorf(err.Error()), nil
					}
				}
			}
			if k := db.NormalizeKnowledgeKind(a.Kind); a.Kind != "" && k == "" {
				return actool.Errorf(fmt.Sprintf("未知的 kind %q（可用：%s）", a.Kind, strings.Join(db.KnowledgeKinds, " | "))), nil
			} else if k != "" {
				a.Kind = k
			}
			summary := strings.TrimSpace(a.Summary)
			if summary == "" {
				summary = knowledgeNodeSummary(node)
			}
			if summary == "" {
				return actool.Errorf("摘要不能为空（来源节点里也没有可用的摘要）"), nil
			}
			// 来源任务：节点属于哪个任务就记哪个（上游节点的沉淀也应指向真正的来源）。
			sourceTaskID := ""
			if t.taskID > 0 {
				sourceTaskID = fmt.Sprintf("%d", t.taskID)
			}
			if st, ok := nodeSourceTask(node); ok {
				sourceTaskID = st
			}
			saved, err := t.as.CreateCompanyKnowledge(db.CompanyKnowledge{
				CompanyID: a.CompanyID, Kind: a.Kind, Summary: summary, Detail: strings.TrimSpace(a.Detail),
				SourceTaskID: sourceTaskID, SourceNodeID: a.NodeID, Confidence: a.Confidence,
			})
			if err != nil {
				return actool.Errorf("沉淀失败：" + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"id": saved.ID, "company_id": saved.CompanyID, "kind": saved.Kind,
				"summary": saved.Summary, "source_task_id": saved.SourceTaskID,
				"note": "已沉淀为公司级只读背景：同公司的其它任务会在态势里看到它；要撤下请让操作者在页面上废弃。",
			})
		})
}

// knowledgeNodeSummary 从节点 payload 里取一行式摘要（fact/finding 的字段名不同，都试）。
func knowledgeNodeSummary(n *db.Node) string {
	var p map[string]any
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return ""
	}
	for _, key := range []string{"summary", "text", "description"} {
		if s, ok := p[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// nodeSourceTask 读节点自带的来源任务标记（跨任务读取的行会带 source_task_id）。
func nodeSourceTask(n *db.Node) (string, bool) {
	var p map[string]any
	if err := json.Unmarshal(n.Payload, &p); err != nil {
		return "", false
	}
	if s, ok := p["source_task_id"].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s), true
	}
	return "", false
}

// companyKnowledgeLines 给常驻索引用的一行式（≤5 行；空 = 不注入）。
func (t *ToolSet) companyKnowledgeLines() []string {
	rows := t.companyKnowledgeRows()
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > 5 {
		rows = rows[:5]
	}
	out := make([]string, 0, len(rows))
	for _, k := range rows {
		out = append(out, renderKnowledgeLine(k))
	}
	return out
}
