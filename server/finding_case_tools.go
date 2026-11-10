package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 统一报告（Reporter）：同一缺陷的重复上报先归并成案例，再由报告 Agent 读取全部
// 成员材料撰写一份统一报告。原始 finding 报告、评级和证据始终保留；工具只用案例
// 版本做 CAS，材料变化后旧版本写入会被拒绝并要求重新读取。

func (s *Server) findingCaseRunTask(ctx context.Context) (int64, error) {
	ri := agent.RunInfoFrom(ctx)
	if ri.TaskID <= 0 {
		return 0, errors.New("统一报告工具需要任务上下文")
	}
	return ri.TaskID, nil
}

func (s *Server) findingCaseAccess(ctx context.Context, caseID int64) (*db.FindingCase, error) {
	if caseID <= 0 {
		return nil, errors.New("case_id 必填")
	}
	taskID, err := s.findingCaseRunTask(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.m.pg.GetFindingCase(caseID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("漏洞案例不存在")
	}
	if c.TaskID == nil || *c.TaskID != taskID {
		return nil, errors.New("只能读取当前任务的漏洞案例")
	}
	return c, nil
}

func (s *Server) findingRecordAccess(ctx context.Context, findingID int64) (*db.DBFinding, error) {
	if findingID <= 0 {
		return nil, errors.New("finding_id 必填（独立漏洞记录 ID，不是探索节点 ID）")
	}
	taskID, err := s.findingCaseRunTask(ctx)
	if err != nil {
		return nil, err
	}
	f, err := s.m.pg.GetFinding(findingID)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fmt.Errorf("漏洞 %d 不存在", findingID)
	}
	if f.TaskID == nil || *f.TaskID != taskID {
		return nil, errors.New("只能处理当前任务的漏洞记录")
	}
	return f, nil
}

func (s *Server) findingCaseTools() []actool.CoreTool {
	taskParam := map[string]any{"type": "integer", "description": "同一来源任务 ID"}
	idsParam := map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "独立漏洞记录 ID 列表"}
	pageParam := map[string]any{"type": "integer", "description": "分页，从 1 开始，每页 20 条"}
	return []actool.CoreTool{
		roTool("search_finding_duplicates",
			"只读检索同任务候选摘要，按评分排序并返回 score/reasons：同漏洞类型且有资产重叠的候选优先核对，等级不同也可能重复。支持分页，详情按需用 get_finding_record 读取。",
			objSchema(map[string]any{"finding_id": map[string]any{"type": "integer", "description": "独立 findings 记录 ID，不是探索节点 ID"}, "page": pageParam}, "finding_id"),
			func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
				var a struct {
					FindingID json.RawMessage `json:"finding_id"`
					Page      int             `json:"page"`
				}
				if err := json.Unmarshal(in, &a); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				f, err := s.findingRecordAccess(ctx, parseProfileID(a.FindingID))
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				taskID, _ := s.findingCaseRunTask(ctx)
				items, err := s.m.pg.FindingCaseCandidates(taskID, f.ID, 200)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				page := a.Page
				if page < 1 {
					page = 1
				}
				const size = 20
				start := (page - 1) * size
				if start > len(items) {
					start = len(items)
				}
				end := start + size
				if end > len(items) {
					end = len(items)
				}
				out := make([]map[string]any, 0, end-start)
				strong := 0
				for _, c := range items {
					if c.Score >= 4 {
						strong++
					}
				}
				for _, c := range items[start:end] {
					out = append(out, map[string]any{"finding_id": c.Finding.ID, "name": c.Finding.Name, "severity": c.Finding.Severity, "summary": c.Finding.Summary, "score": c.Score, "reasons": c.Reasons})
				}
				return jsonResult(map[string]any{"items": out, "total": len(items), "strong_total": strong, "page": page, "page_size": size,
					"note": "评分只是线索：必须核对目标、端点、参数、权限边界、根因与实际验证证据后才能归并"})
			}),
		roTool("get_finding_record",
			"读取一条独立漏洞的完整原始证据与报告，返回探索节点编号以便追溯执行记录。",
			objSchema(map[string]any{"finding_id": map[string]any{"type": "integer", "description": "独立 findings 记录 ID"}}, "finding_id"),
			func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
				var a struct {
					FindingID json.RawMessage `json:"finding_id"`
				}
				_ = json.Unmarshal(in, &a)
				f, err := s.findingRecordAccess(ctx, parseProfileID(a.FindingID))
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				dto := findingFromDB(f, s.resolveFindingAssets([]*db.DBFinding{f}))
				result := map[string]any{"finding": dto}
				if f.NodeID != nil {
					result["finding_node_id"] = *f.NodeID
				}
				return jsonResult(result)
			}),
		wrTool("merge_finding_records",
			"证据明确证明是同一实际缺陷时归并成漏洞案例；保留所有原始记录、报告与证据，不跨任务、不改原评级、不可绕过人工否决。已有案例会被合并到同一个文件夹。归并后应读取案例并尽力更新统一报告。",
			objSchema(map[string]any{"task_id": taskParam, "finding_ids": idsParam, "title": strParam("同一实际缺陷的名称"), "reason": strParam("具体证据与归并理由，不能仅类型相同")}, "task_id", "finding_ids", "title", "reason"),
			func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
				var a struct {
					TaskID     json.RawMessage   `json:"task_id"`
					FindingIDs []json.RawMessage `json:"finding_ids"`
					Title      string            `json:"title"`
					Reason     string            `json:"reason"`
				}
				if err := json.Unmarshal(in, &a); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				taskID, err := s.findingCaseRunTask(ctx)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if parseProfileID(a.TaskID) != taskID {
					return actool.Errorf("只能归并当前任务的漏洞"), nil
				}
				ids := make([]int64, 0, len(a.FindingIDs))
				for _, raw := range a.FindingIDs {
					if id := parseProfileID(raw); id > 0 {
						ids = append(ids, id)
					}
				}
				c, err := s.m.pg.MergeFindingCase(ctx, taskID, ids, a.Title, a.Reason, "reporter")
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return jsonResult(map[string]any{"case_id": fmt.Sprint(c.ID), "version": c.Version})
			}),
		wrTool("suggest_finding_merge",
			"证据有具体共同点但缺少确认同一根因的关键证据时，提交一个 2..20 条的疑似重复分组等待人工确认；不是自动合并。仅类型/资产相同不得提交。",
			objSchema(map[string]any{"task_id": taskParam, "finding_ids": idsParam, "title": strParam("疑似同一缺陷的名称"), "reason": strParam("具体共同点与仍缺的证据")}, "task_id", "finding_ids", "title", "reason"),
			func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
				var a struct {
					TaskID     json.RawMessage   `json:"task_id"`
					FindingIDs []json.RawMessage `json:"finding_ids"`
					Title      string            `json:"title"`
					Reason     string            `json:"reason"`
				}
				if err := json.Unmarshal(in, &a); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				taskID, err := s.findingCaseRunTask(ctx)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if parseProfileID(a.TaskID) != taskID {
					return actool.Errorf("只能处理当前任务的漏洞"), nil
				}
				ids := make([]int64, 0, len(a.FindingIDs))
				for _, raw := range a.FindingIDs {
					if id := parseProfileID(raw); id > 0 {
						ids = append(ids, id)
					}
				}
				id, err := s.m.pg.SuggestFindingCase(ctx, taskID, ids, a.Title, a.Reason)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return jsonResult(map[string]any{"suggestion_id": fmt.Sprint(id), "finding_ids": ids})
			}),
		roTool("get_finding_case",
			"读取漏洞案例、全部成员的原报告/证据及当前 version。先记录 version 再撰写；材料变化后版本会前进，必须重新读取，不能用旧版本保存。",
			objSchema(map[string]any{"case_id": map[string]any{"type": "integer", "description": "归并案例 ID"}, "page": pageParam}, "case_id"),
			func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
				var a struct {
					CaseID json.RawMessage `json:"case_id"`
					Page   int             `json:"page"`
				}
				_ = json.Unmarshal(in, &a)
				c, err := s.findingCaseAccess(ctx, parseProfileID(a.CaseID))
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				page := a.Page
				if page < 1 {
					page = 1
				}
				const size = 20
				members := make([]FindingDTO, 0, size)
				start := (page - 1) * size
				for i, m := range c.Members {
					if i < start || i >= start+size {
						continue
					}
					members = append(members, findingFromDB(m, s.resolveFindingAssets([]*db.DBFinding{m})))
				}
				c.Members = nil
				return jsonResult(map[string]any{"case": c, "members": members, "total": c.MemberCount, "page": page, "page_size": size})
			}),
		wrTool("update_finding_case_report",
			"保存案例的统一报告、统一评级与评级依据。version 必须是 get_finding_case 实际读取的值；必须包含有内容的概述、影响、前置条件、复现步骤、证据、PoC、根因分析、修复建议，不得半篇截断。不覆盖任何原始报告。",
			objSchema(map[string]any{"case_id": map[string]any{"type": "integer", "description": "归并案例 ID"}, "version": map[string]any{"type": "integer", "description": "get_finding_case 实际读取的 version"}, "title": strParam("统一报告标题"), "report": strParam("完整统一 Markdown 报告"), "severity": strParam("critical|high|medium|low"), "severity_reason": strParam("基于已验证影响的评级依据，不直接取成员最高等级")},
				"case_id", "version", "title", "report", "severity", "severity_reason"),
			func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
				var a struct {
					CaseID         json.RawMessage `json:"case_id"`
					Version        int64           `json:"version"`
					Title          string          `json:"title"`
					Report         string          `json:"report"`
					Severity       string          `json:"severity"`
					SeverityReason string          `json:"severity_reason"`
				}
				if err := json.Unmarshal(in, &a); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				c, err := s.findingCaseAccess(ctx, parseProfileID(a.CaseID))
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if a.Version <= 0 {
					return actool.Errorf("version 必须来自 get_finding_case 的实际读取，不能省略"), nil
				}
				if err := validateUnifiedFindingReport(a.Report); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if strings.TrimSpace(a.SeverityReason) == "" {
					return actool.Errorf("severity_reason 必填：统一评级必须说明基于哪条已验证影响"), nil
				}
				if _, err := s.m.pg.UpdateFindingCaseReport(ctx, c.ID, a.Version, a.Title, a.Report, a.Severity, a.SeverityReason); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return jsonResult(map[string]any{"ok": true})
			}),
	}
}

func (s *Server) seedFindingCaseTools() {
	const flag = "finding_case_tools_v1"
	if value, _, _ := s.m.pg.GetSetting(flag); value == "true" {
		return
	}
	names := []string{}
	for _, t := range s.findingCaseTools() {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.SeedTool(t.Name(), t.Description(), schema, json.RawMessage(`["reporter"]`)); err != nil {
			log.Printf("[finding-cases] seed %s failed: %v", t.Name(), err)
			return
		}
		if err := s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
			log.Printf("[finding-cases] refresh %s failed: %v", t.Name(), err)
			return
		}
		names = append(names, t.Name())
	}
	if err := s.m.pg.AddAgentToToolBinding("reporter", names); err != nil {
		log.Printf("[finding-cases] bind reporter failed: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[finding-cases] record flag failed: %v", err)
	}
}
