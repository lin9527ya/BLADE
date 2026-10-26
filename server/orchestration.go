package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/Norma/permission"
	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// jsonResult marshals v to a JSON tool result.
func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// 本文件实现 P2「跨任务编排工具集」(docs/跑分编排 §2 P2)。这些是 host 工具——需要
// 访问 Manager(任意任务的 Store)、Engine(暂停)、以及建任务流程,所以住在 server 层。
// 读类工具把「现有 per-task 工具」重定向到目标任务的 store 上跑(建一个临时 ToolSet
// 并 Call 其对应工具),从而复用完全相同的逻辑;控制类(spawn/pause)直接调 Manager/Engine。
// 它们像流量工具一样 seed 进 tools 表、按 agent 绑定(只绑给编排 agent 才可见)。

// hostTools is the runtime host-tool provider fed to ToolAugment: traffic tools
// (gated by capture) + cross-task orchestration tools + user-defined custom tools.
// The second return is the names of custom tools flagged `deferred` (schema
// withheld, routed via SearchExtraTools/ExecuteExtraTool). Per-agent binding still
// decides who actually sees any of them.
//
//nolint:unused // used as the hostTools provider in wireAgentAugment
func (s *Server) hostTools() ([]actool.CoreTool, map[string][]string) {
	tools := append(s.m.HostTools(), s.orchestrationTools()...)
	tools = append(tools, s.findingRetestTools()...)
	tools = append(tools, s.platformTools()...)    // 平台操作工具(建改 skill/工具/MCP，给 Auto 用)
	tools = append(tools, s.intelTools()...)       // P21 情报/测绘工具(默认绑 planner/worker/mainagent)
	tools = append(tools, s.graphIngestTools()...) // P40 域内/云上枚举结果的摄入(同绑三面)
	custom, err := s.customTools()
	if err != nil {
		log.Printf("[custom-tool] 加载失败: %v", err)
		return tools, nil
	}
	tools = append(tools, custom...)
	// deferred custom tools → name -> its bound agent keys. ToolAugment turns a
	// name into a deferred entry only for agents it's actually bound to (so we don't
	// advertise a tool the per-agent binding will drop from the callable set).
	deferred := map[string][]string{}
	rows, _ := s.m.pg.ListCustomTools()
	for _, t := range rows {
		if t.Deferred && t.Enabled {
			deferred[t.Key] = t.Agents
		}
	}
	return tools, deferred
}

// orchestrationTools returns the cross-task tool set. Bound per-agent via the
// tools table (default: no binding — opt-in for orchestration agents).
func (s *Server) orchestrationTools() []actool.CoreTool {
	return []actool.CoreTool{
		s.toolListTasks(),
		s.toolListLLMProfiles(),
		s.toolSpawnTask(),
		s.toolPauseTask(),
		s.toolGetTaskGraph(),
		s.toolListTaskFindings(),
		s.toolAddHint(),
		s.toolGetWorkerTrace(),
		s.toolListWorkerTraces(),
		s.toolSearchWorkerTraces(),
		s.toolGetTaskNodeDetail(),
		s.toolUpdateFindingReport(),
		s.toolGetFindingTraffic(),
		s.toolBindFindingTraffic(),
	}
}

// --- schema helpers ---

func strParam(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// parseProfileID reads an LLM profile id from a tool arg that may arrive as a JSON
// number (5) or a numeric string ("5"); returns 0 when absent/unparseable.
func parseProfileID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(str), 10, 64)
		return v
	}
	return 0
}

func objSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		m["required"] = req
	}
	return m
}

func roTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:   func(json.RawMessage) bool { return true },
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func wrTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

// delegateToTask resolves the `task_id` in the input, builds a ToolSet bound to
// that task's store, strips task_id, and calls the chosen per-task tool — so the
// cross-task read reuses the exact in-task logic against another task.
func (s *Server) delegateToTask(ctx context.Context, in json.RawMessage, pick func(*agent.ToolSet) actool.CoreTool) (actool.Result, error) {
	var head struct {
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(in, &head)
	if strings.TrimSpace(head.TaskID) == "" {
		return actool.Errorf("task_id 为必填"), nil
	}
	t, ok := s.m.Task(head.TaskID)
	if !ok {
		return actool.Errorf("task 不存在: " + head.TaskID), nil
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(in, &m)
	delete(m, "task_id")
	inner, _ := json.Marshal(m)
	tsx := agent.NewToolSet(t.Store, "orchestrator")
	if s.m.Assets() != nil {
		tsx.SetAssetStore(s.m.Assets(), s.m.Assets().Companies())
	}
	tsx.SetNotify(t.Notify)         // 通用唤醒（无专用回调的写操作走它；读工具为 no-op）
	tsx.SetNotifyHint(t.NotifyHint) // add_hint → 记一条「人新增了 N 条战略提示：…」触发并唤醒 planner
	return pick(tsx).Call(ctx, inner, nil)
}

// --- tools ---

func (s *Server) toolListTasks() actool.CoreTool {
	return roTool("list_tasks",
		"列出所有任务(id/描述/目标/状态/运行时长/父任务/LLM 配置)，编排 agent 用它掌握全局、看哪些任务卡太久、各自用哪个 LLM。运行时长：运行中=创建→现在，终态=创建→最后活动(秒)。llm_profile：任务 planner/worker 用的配置名，(激活配置)=跟随全局激活。",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _ := s.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profName := map[int64]string{}
			if profs, err := s.m.pg.ListProfiles(); err == nil {
				for _, p := range profs {
					profName[p.ID] = p.Name
				}
			}
			out := make([]map[string]any, 0)
			for _, t := range s.m.List() {
				status := s.deriveTaskStatus(t)
				end := lastAct[t.ExpID]
				if live := s.engine.LastActivity(t.ID); live > end {
					end = live
				}
				dur := int64(0)
				if status == "running" {
					dur = time.Now().Unix() - t.CreatedAt
				} else if end > t.CreatedAt {
					dur = end - t.CreatedAt
				}
				row := map[string]any{"id": t.ID, "description": t.Description, "goal": t.Goal, "status": status, "run_seconds": dur}
				if t.ParentRef != "" {
					row["parent_ref"] = t.ParentRef
				}
				llmState := t.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(激活配置)"
				} else if n, ok := profName[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d(已删除)", *llmState.ProfileID)
				}
				out = append(out, row)
			}
			return jsonResult(out)
		})
}

// toolListLLMProfiles lists the available LLM profiles (name/model/active) so an
// orchestration agent can pick one for spawn_task's llm_profile. Never leaks keys.
func (s *Server) toolListLLMProfiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"列出可用的 LLM 配置(profile)：id、名称、模型、格式、是否为当前激活配置。用 id 给 spawn_task 的 llm_profile_id 参数指定子任务专属 LLM（如侦察用便宜模型、利用用强模型）。不含 API Key。",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err := s.m.pg.ListProfiles()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(profs))
			for _, p := range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

func (s *Server) toolSpawnTask() actool.CoreTool {
	return wrTool("spawn_task",
		"新建一个子任务并启动探索引擎，返回 task_id。用于把一件事(如一道题/一个目标)派成独立任务。parent_ref 可选：填当前编排关联的父任务 id 做父子关联。",
		objSchema(map[string]any{
			"description":            strParam("任务描述(简短标题)"),
			"goal":                   strParam("任务目标(要达成什么)"),
			"parent_ref":             strParam("可选：父任务 id(做父子关联)"),
			"source_task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("可选：只读继承的来源任务 id 列表(最多 %d 个)。子任务可只读引用这些任务已探明的资产/结论作为起点；与 parent_ref 的纯父子指针不同，这是内容继承。", db.MaxTaskSourceCount)},
			"llm_profile_id":         map[string]any{"type": "integer", "description": "可选：指定本子任务 planner/worker 用的 LLM 配置 id(见 list_llm_profiles)；留空则继承父任务、再回退全局激活配置"},
			"timeout_seconds":        map[string]any{"type": "integer", "description": "可选：任务级超时(秒)。到点后触发优雅收尾并进入 timeout 终态；留空或 0 = 不限时"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "可选：planner 心跳触发间隔(秒)。距上轮规划结束/任务开始满该值且期间无触发 → 触发一轮规划(兜底死锁 + 唤醒去监督飞行中的 worker)。留空或 0 = 默认 600(10min)；"},
			"seed_first_intent":      map[string]any{"type": "boolean", "description": "可选：对于简单任务可开启，创建时直接下发一条种子意图(内容=描述+目标)让 worker 免等首轮 planner 直接开跑测试；默认 false(走标准先规划再执行)。"},
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Description          string          `json:"description"`
				Goal                 string          `json:"goal"`
				ParentRef            string          `json:"parent_ref"`
				SourceTaskIDs        []string        `json:"source_task_ids"`
				LLMProfileID         json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds       int             `json:"timeout_seconds"`
				PlanHeartbeatSeconds int             `json:"plan_heartbeat_seconds"`
				SeedFirstIntent      bool            `json:"seed_first_intent"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Description) == "" {
				a.Description = "未命名任务"
			}
			if strings.TrimSpace(a.Goal) == "" {
				return actool.Errorf("goal 为必填"), nil
			}
			if a.TimeoutSeconds < 0 {
				a.TimeoutSeconds = 0
			}
			// 只读继承来源任务：数量上限 + 每个 id 有效/去重/存在，校验规则与 HTTP 建任务一致。
			if len(a.SourceTaskIDs) > db.MaxTaskSourceCount {
				return actool.Errorf(fmt.Sprintf("关联任务最多选择 %d 个", db.MaxTaskSourceCount)), nil
			}
			sourceIDs := make([]int64, 0, len(a.SourceTaskIDs))
			seenSources := map[int64]bool{}
			for _, raw := range a.SourceTaskIDs {
				id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
				if err != nil || id <= 0 || seenSources[id] {
					return actool.Errorf("关联任务 id 无效或重复"), nil
				}
				if _, ok := s.m.Task(strconv.FormatInt(id, 10)); !ok {
					return actool.Errorf(fmt.Sprintf("关联任务 #%d 不存在", id)), nil
				}
				seenSources[id] = true
				sourceIDs = append(sourceIDs, id)
			}
			// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
			var pin *int64
			if id := parseProfileID(a.LLMProfileID); id > 0 {
				if _, ok := s.loadProfileConfig(id); !ok {
					return actool.Errorf(fmt.Sprintf("LLM 配置 #%d 不存在或未设置 API Key", id)), nil
				}
				pin = &id
			} else if a.ParentRef != "" {
				if pt, ok := s.m.Task(a.ParentRef); ok {
					pin = pt.LLMProfileID
				}
			}
			var llmIDs []int64
			if pin != nil {
				llmIDs = []int64{*pin}
			}
			t, err := s.m.CreateTaskWithOptions(a.Description, a.Goal, db.TaskCreateOptions{
				SourceTaskIDs:        sourceIDs,
				LLMProfileIDs:        llmIDs,
				TimeoutSeconds:       a.TimeoutSeconds,
				PlanHeartbeatSeconds: a.PlanHeartbeatSeconds,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if a.ParentRef != "" {
				t.ParentRef = a.ParentRef
				if id, e := strconv.ParseInt(t.ID, 10, 64); e == nil {
					if perr := s.m.PG().SetParentRef(id, a.ParentRef); perr != nil {
						log.Printf("[orchestration] 记录父子任务关联失败 task=%d parent=%s: %v", id, a.ParentRef, perr)
					}
				}
			}
			// 共享的建后流程,与 HTTP 建任务(server.go createTask)复用同一段 launchTask:
			// seed + 后台可见地做目标分解(第0轮/LLM步骤/逐条goal) + engine.Run。
			// seed_first_intent 默认 false(标准先规划再执行);简单任务可开启直接下发一 work 测试。
			s.launchTask(t, a.Description+" "+a.Goal, a.SeedFirstIntent)
			return actool.Text(fmt.Sprintf("task created: %s", t.ID)), nil
		})
}

func (s *Server) toolPauseTask() actool.CoreTool {
	return wrTool("pause_task", "暂停指定任务(停止其 planner/worker 循环)。",
		objSchema(map[string]any{"task_id": strParam("要暂停的任务 id")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(in, &a)
			t, ok := s.m.Task(a.TaskID)
			if !ok {
				return actool.Errorf("task 不存在: " + a.TaskID), nil
			}
			if _, err := s.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("task paused: " + a.TaskID), nil
		})
}

func (s *Server) toolGetTaskGraph() actool.CoreTool {
	return roTool("get_task_graph", "读指定任务的探索图总览(同 graph_overview：资产计数/frontier/发现/覆盖等)，用 task_id 指定任务。",
		objSchema(map[string]any{"task_id": strParam("任务 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
		})
}

func (s *Server) toolListTaskFindings() actool.CoreTool {
	return roTool("list_task_findings", "读指定任务的确认漏洞(含 flag/PoC；每条带 id/task_id/intent_id/vulnclass/severity/摘要/状态)，用 task_id 指定任务。",
		objSchema(map[string]any{"task_id": strParam("任务 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (s *Server) toolAddHint() actool.CoreTool {
	return wrTool("add_task_hint", "给指定任务注入战略提示(该任务的 planner 下轮生成意图时会读到)。\n"+
		"★优先批量：多条提示放进 hints 数组一次提交（返回 ids 数组，与 hints 等长同序，失败项 id=0）；单条则省略 hints 直接给顶层 text。",
		objSchema(map[string]any{
			"task_id":      strParam("任务 id"),
			"hints":        map[string]any{"type": "array", "description": "【优先用这个】提示数组，每个元素字段同顶层（text/asset_ids/traffic_refs）。", "items": objSchema(map[string]any{"text": strParam("提示内容"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text":         strParam("[单条] 提示内容"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "锚定的资产 id（可选，0/1/多个；该任务内的资产 id）"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (s *Server) toolGetWorkerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"看指定任务里某个 work(意图)的执行过程：get_task_worker_trace(task_id, intent_id) 看步骤摘要；再带 step_ids=[...] 取那几步完整内容(一次最多 5 个,多传只返回前 5 个)。",
		objSchema(map[string]any{
			"task_id":   strParam("任务 id"),
			"intent_id": map[string]any{"type": "integer", "description": "意图 id(该任务里的 work)"},
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选：要取完整内容的步骤 id(一次最多 5 个,多传只返回前 5 个,其余在 omitted_step_ids 里列出)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (s *Server) toolListWorkerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "列出指定任务里跑过哪些 work(意图) + 各自步数，用于发现哪些 work 值得翻看(再用 get_task_worker_trace)。",
		objSchema(map[string]any{"task_id": strParam("任务 id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (s *Server) toolSearchWorkerTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "在指定任务里按关键字搜索所有 work 的执行过程(返回命中步骤摘要 + intent_id)。",
		objSchema(map[string]any{"task_id": strParam("任务 id"), "q": strParam("搜索关键字")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (s *Server) toolGetTaskNodeDetail() actool.CoreTool {
	return roTool("get_task_node_detail",
		"读指定任务里某个探索图节点的完整内容(发现/事实/意图/目标：摘要 + 详情/证据/PoC)。id 为探索节点 id(如 report_finding 返回、或 list_task_findings 里的 id)。写漏洞报告前用它取该漏洞的完整证据。",
		objSchema(map[string]any{
			"task_id": strParam("任务 id"),
			"id":      map[string]any{"type": "integer", "description": "探索图节点 id(非资产 id)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return s.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

// toolUpdateFindingReport writes/overwrites a finding's detailed Markdown report.
// finding_id is the id report_finding returned ("finding recorded: <id>", the
// finding node id). The write (SetFindingReportByNodeID) is keyed by node_id and
// task-agnostic, so this host tool needs no task_id / exploration store.
func (s *Server) toolUpdateFindingReport() actool.CoreTool {
	return wrTool("update_finding_report",
		"为【已确认确实存在】的漏洞写入/更新【详细报告】(Markdown 全文,整段覆盖旧内容)。先确认漏洞真实存在——confidence=high、处置状态为「已确认」、或复测结论为 reproduced/fixed，三者满足其一才允许保存；未确认的候选会被拒收（请用 close_candidate 记录缺口，不要写报告）。finding_id 传 report_finding 返回的那个 id(\"finding recorded: <id>\" 里的数字)。报告必须包含:漏洞概述、影响与危害、**详细利用过程(按时间顺序,每步 动作→请求/命令→回显→结论)**、**关键位置与产物表(主机/绝对路径/访问入口/写入方式/权限/状态/清理方式)**、执行过的命令与回显、复现步骤、证据/PoC、根因、修复与清理建议。",
		objSchema(map[string]any{
			"finding_id":       map[string]any{"type": "integer", "description": "目标漏洞 id(report_finding 返回的 id)"},
			"report":           strParam("详细报告全文,Markdown 格式"),
			"evidence_version": map[string]any{"type": "integer", "description": "get_finding_traffic 返回的证据 version；用于防止报告覆盖新的证据变更"},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				EvidenceVersion *int64          `json:"evidence_version"`
				FindingID       json.RawMessage `json:"finding_id"`
				Report          string          `json:"report"`
			}
			_ = json.Unmarshal(in, &a)
			nodeID := parseProfileID(a.FindingID) // 复用「数字或数字字符串」解析
			if nodeID <= 0 {
				return actool.Errorf("finding_id 无效"), nil
			}
			// 报告只写给【确认确实存在】的漏洞。没有这道门，reporter 会给"据版本推断"
			// 的候选也写一份像模像样的报告，交付物里就混进了未验证的结论——那比不写更糟。
			confidence, status, verdict, found, err := s.m.pg.FindingConfirmationByNodeID(nodeID)
			if err != nil {
				return actool.Errorf("读取漏洞确认状态失败：" + err.Error()), nil
			}
			if found && !findingConfirmed(confidence, status, verdict) {
				return actool.Errorf(fmt.Sprintf("拒绝写入报告：该漏洞尚未确认确实存在（confidence=%q，处置状态=%q，复测结论=%q）。"+
					"报告只写给真实复现/确认过的漏洞：补可复现证据并以 confidence=high 重新上报，或把处置状态标为「已确认」，"+
					"或完成复测得到 reproduced/fixed 结论；未确认的候选请用 close_candidate(outcome=open_proof_gap) 记录缺口，不要写成报告。",
					confidence, status, verdict)), nil
			}
			n, err := s.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, a.Report, a.EvidenceVersion)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == 0 {
				return actool.Errorf(fmt.Sprintf("未找到 finding_id=%d 对应的漏洞记录(先用 report_finding 登记)", nodeID)), nil
			}
			return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(a.Report))), nil
		})
}

// findingConfirmed 决定"能不能给它写报告"。只认三类硬证据：
//   - confidence=high：上报者本次真实触发、拿到可复现的触发链；
//   - 处置状态=已确认：人工/流程确认真实漏洞；
//   - 最近一次复测=reproduced 或 fixed：独立复测确认它存在（fixed 的前提是确认过原触发条件）。
//
// 已撤回/误报/已排除/重复**一律不写**，哪怕置信度标了 high——处置结论优先于自评。
func findingConfirmed(confidence, status, retestVerdict string) bool {
	switch status {
	case db.FindingRetracted, db.FindingFalsePositive, db.FindingRuledOut, db.FindingDuplicate:
		return false
	case db.FindingConfirmed:
		return true
	}
	switch retestVerdict {
	case "reproduced", "fixed":
		return true
	}
	return confidence == db.ConfidenceHigh
}

// deriveTaskStatus mirrors listTasks' status derivation for the list_tasks tool.
func (s *Server) deriveTaskStatus(t *Task) string {
	lifecycle := t.lifecycleSnapshot()
	switch {
	case isTerminalStatus(lifecycle.Status):
		return lifecycle.Status
	case lifecycle.Paused || s.engine.IsPaused(t.ID):
		return "paused"
	case s.engine.ReadyFor(t) && s.engine.Started(t.ID):
		return "running"
	}
	return "created"
}

// orchestrationToolSeeds seeds the cross-task tools into the tools table so they
// are bindable per-agent (default: bound to nobody — opt-in for orchestration
// agents). First-insert only, like the traffic seeds.
func (s *Server) seedOrchestrationTools() {
	// task-op + platform tools default-bind to the built-in Auto agent (它天生用来
	// 操作平台)。SeedTool 首插入生效;老库已 seed 的行由 seedAutoDefaultBindings 补绑。
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range s.orchestrationTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings := autoAgents
		if t.Name() == "bind_finding_traffic" {
			bindings = json.RawMessage(`["reporter"]`)
		}
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t := range s.platformTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	// P21 情报工具：默认绑 planner/worker/mainagent（决策方向、执行侦察、人工直查）。
	intelAgents, _ := json.Marshal([]string{"planner", "worker", "mainagent"})
	for _, t := range s.intelTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, intelAgents)
	}
	// P40 图数据摄入：同样绑三面——**worker 是产出方**（在机器上跑出枚举结果的人当场记，
	// 否则图还是空的），planner 据此规划方向，人在对话里也能补录。
	for _, t := range s.graphIngestTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = s.m.PG().SeedTool(t.Name(), t.Description(), schema, intelAgents)
	}
	s.refreshBuiltinToolSchemas()
	s.seedAutoDefaultBindings()
	s.seedBuiltinBindingBackfill()
	s.seedPlannerDefaultBindings()
	s.seedPlannerListAssetsBinding()
	s.seedGraphToolBindings()
	s.seedCompanyScopeRebind()
	s.seedRedTeamPolicyDefault()  // 旧全局策略若仍是"只读验证"默认，迁到红队默认(一次性)
	s.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces 从 worker 默认解绑(一次性)
	s.seedWorkerReadbackRebind()  // 修复旧迁移误删：把 search_all_worker_traces/get_worker_trace/node_detail 补绑回 worker(一次性)
	s.seedAutoReportFindingBinding()
	s.unbindGoalMetDefault()
	s.reseedGoalsPrompt()             // goals 提示词加入「抽操作约束」步 → 旧库追加一版新默认(一次性)
	s.reseedMainAgentPrompt()         // mainagent 提示词加入「目标达成后 add_intent 反问是否建目标」(一次性)
	s.reseedPlannerPrompt()           // planner 提示词:重写「0 意图」正当理由 + 加量化验收核对(一次性)
	s.reseedWorkerPrompt()            // worker 提示词:加否定结论证据门槛(一次性)
	s.seedReporterAgent()             // 预置「报告撰写」agent + 工具绑定 + finding 触发器(一次性)
	s.reseedReporterPrompt()          // 报告模板:补详细利用过程/绝对路径/命令回显(一次性)
	s.seedPlaybookAgent()             // 预置「打法沉淀」agent + report_foothold 触发器(一次性)
	s.seedPlaybookGoalTrigger()       // 存量库补「目标达成」触发器(一次性)
	s.reseedPlaybookPrompt()          // 打法沉淀提示词兼容目标达成触发(一次性)
	s.reseedPentestPrompt()           // pentest 提示词:加 lookup_playbooks 打法复用(一次性)
	s.upgradeReporterTriggerMessage() // 老库补迁移:让 reporter 回传 evidence_version(一次性)
	s.seedFindingTrafficTools()       // 增加可选证据参数及只读证据工具，保留用户配置
	s.seedFindingWorkflowTools()
	s.seedCVEInterceptTool() // 老库已保存 intercept_enabled_tools 时补 cve_exploit(一次性)
	// 注：pentest 的默认工具绑定无需迁移——BuiltinToolSeeds 在全新初始化时就把
	// list_assets/insert_assets/report_finding/list_findings/list_companies 连同
	// pentest 一起 seed 好了（项目尚无旧库，不做迁移）。
}

// refreshBuiltinToolSchemas propagates code schema/description changes on the
// orchestration + platform tools into already-seeded rows ONCE per version flag —
// SeedTool is first-insert-only, so a new param (e.g. spawn_task 的 llm_profile) never
// reaches an old DB otherwise. Preserves each tool's agent binding + enabled flag.
// Bump the flag whenever these tools' schemas/descriptions change in code.
func (s *Server) refreshBuiltinToolSchemas() {
	const flag = "tool_schema_refresh_v10_report_artifact_url"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	tools := append(s.orchestrationTools(), s.platformTools()...)
	tools = append(tools, s.intelTools()...)
	tools = append(tools, s.graphIngestTools()...)
	for _, t := range tools {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
			log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
		}
	}
	// 同时把部分内置 agent 工具刷成代码默认：
	//   - goal_met：旧库 seed 的描述带“结束本轮规划”的误导，会让 planner 把它当成
	//     “结束空轮”的手段、刚开跑就误判整个任务完成。
	//   - insert_assets：新增 related 入参(标记资产是否与当前任务相关、决定是否入覆盖度)，
	//     SeedTool 首插入only，旧库已 seed 的 schema 否则收不到这个新参数。
	//   - list_facts：改为分页，新增 limit/before/q 入参；旧库已 seed 的空 schema 否则
	//     在工具管理页显示「无参数」，模型也拿不到这几个参数说明。
	refreshBuiltin := map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true,
		// report_artifact 新增 url（访问入口）：旧库的 schema 里没有这个参数，模型也就不会填；
		// 报告因此只有绝对路径、没有访问 URL。只刷新 description/schema，绑定与开关保持不变。
		"report_artifact": true}
	for _, sd := range agent.BuiltinToolSeeds() {
		if !refreshBuiltin[sd.Key] {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		if err := s.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err != nil {
			log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
		}
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
	log.Printf("[tools] 已刷新 orchestration/platform 工具 schema 到代码默认(一次性)")
}

// unbindGoalMetDefault removes goal_met's default "planner" binding ONCE (guarded by
// a settings flag), so existing DBs match the new default of NO agent. goal_met bypasses
// per-goal prove_goal to declare the whole task done — powerful/risky and redundant with
// the prove_goal→auto-complete path — so it ships unbound; users can re-bind it per agent
// in the UI. A user's own binding to another agent is untouched (we only strip planner).
func (s *Server) unbindGoalMetDefault() {
	const flag = "goal_met_unbind_default_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("planner", "goal_met"); err != nil {
		log.Printf("[tools] goal_met 解绑 planner 失败: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}

// reseedGoalsPrompt 把 goals 目标拆解器的提示词刷成【当前代码默认】——因为默认正文新增了
// 「先抽操作约束(set_constraints)再拆目标」这一步,而 SeedPromptIfEmpty 首插入only,旧库
// 已有的 version 1 收不到这步。这里用版本管理【追加一个新版本】并切过去(ResetPromptToDefault),
// 旧的版本仍保留在历史里,用户若自定义过可从版本记录找回。settings flag 守卫 → 只做一次;
// 以后默认再变就 bump 这个 flag。全新库无需处理(SeedPromptIfEmpty 已 seed 最新默认)。
func (s *Server) reseedGoalsPrompt() {
	const flag = "goals_prompt_constraint_step_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("goals")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时,seedPrompts 会直接 seed 最新默认,无需此迁移
	}
	tmpl := agent.BuiltinPromptSeeds()["goals"]
	if tmpl == "" {
		return
	}
	// 全新库 seedPrompts 已 seed 最新默认 → 当前版本已等于代码默认,不必再追加重复版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] goals 提示词重刷为新默认失败: %v", err)
		return
	}
	log.Printf("[prompts] goals 提示词已追加新默认版本(加入抽操作约束步,一次性)")
}

// reseedMainAgentPrompt 把 mainagent 提示词刷成【当前代码默认】——默认正文新增了「目标全部
// 达成后 add_intent 直投意图时,反问人是否登记为正式目标」这段引导,而 SeedPromptIfEmpty 首插入
// only,旧库已有版本收不到。用版本管理【追加一个新版本】并切过去(ResetPromptToDefault),旧版本仍
// 保留在历史里,用户若自定义过可从版本记录找回。settings flag 守卫 → 只做一次。全新库无需处理
// (SeedPromptIfEmpty 已 seed 最新默认)。与 reseedGoalsPrompt 完全同构。
func (s *Server) reseedMainAgentPrompt() {
	const flag = "mainagent_prompt_goalless_intent_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("mainagent")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时,seedPrompts 会直接 seed 最新默认,无需此迁移
	}
	tmpl := agent.BuiltinPromptSeeds()["mainagent"]
	if tmpl == "" {
		return
	}
	// 全新库 seedPrompts 已 seed 最新默认 → 当前版本已等于代码默认,不必再追加重复版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] mainagent 提示词重刷为新默认失败: %v", err)
		return
	}
	log.Printf("[prompts] mainagent 提示词已追加新默认版本(加入目标达成后反问建目标,一次性)")
}

// reseedPlannerPrompt 把 planner 提示词刷成【当前代码默认】——默认正文做了精简重构,并把「克制」降级为
// 仅去重、新增「深度优先于覆盖度」「硬底线:目标未达成且无在跑意图必须产出」、给否定结论复核加上界。
// 每次默认有实质变更就 bump 下面的 flag(当前 v3)让存量旧库再刷一次。SeedPromptIfEmpty 首插入only,旧库已有版本收不到,故用版本管理
// 【追加一个新版本】并切过去(ResetPromptToDefault),旧版本仍保留在历史里,用户若自定义过可从版本记录
// 找回。settings flag 守卫 → 只做一次。全新库无需处理(SeedPromptIfEmpty 已 seed 最新默认)。与
// reseedGoalsPrompt 完全同构。
func (s *Server) reseedPlannerPrompt() {
	const flag = "planner_prompt_playbooks_v3"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("planner")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时,seedPrompts 会直接 seed 最新默认,无需此迁移
	}
	tmpl := agent.BuiltinPromptSeeds()["planner"]
	if tmpl == "" {
		return
	}
	// 全新库 seedPrompts 已 seed 最新默认 → 当前版本已等于代码默认,不必再追加重复版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] planner 提示词重刷为新默认失败: %v", err)
		return
	}
	log.Printf("[prompts] planner 提示词已追加新默认版本(加入 matching_playbooks 历史打法引导,一次性)")
}

// reseedWorkerPrompt 把 worker 提示词刷成【当前代码默认】——默认正文 record_fact 段删掉了「否定类结论
// 写观察+试探性读法」整句、并把 confidence(observed/inferred)与「是否穷尽本意图手段」解耦(这些易误导规划者),
// 同时把 facts 数组分条收紧为「彼此完全独立、无法归并」的极少数例外。bump flag 至 v3 让存量旧库再刷一次。
// SeedPromptIfEmpty 首插入only,旧库已有版本收不到,故用版本管理【追加一个新版本】并切过去,旧版本仍保留在历史里可找回。
// settings flag 守卫 → 只做一次。全新库无需处理。与 reseedGoalsPrompt 完全同构。
func (s *Server) reseedWorkerPrompt() {
	const flag = "worker_prompt_playbooks_v5"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("worker")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时,seedPrompts 会直接 seed 最新默认,无需此迁移
	}
	tmpl := agent.BuiltinPromptSeeds()["worker"]
	if tmpl == "" {
		return
	}
	// 全新库 seedPrompts 已 seed 最新默认 → 当前版本已等于代码默认,不必再追加重复版本。
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] worker 提示词重刷为新默认失败: %v", err)
		return
	}
	log.Printf("[prompts] worker 提示词已追加新默认版本(加入 lookup_playbooks 打法复用,一次性)")
}

// reporterToolCallMessage 必须无条件要求先读一次 get_finding_traffic 再写报告。
// 该工具是只读的、「不依赖捕获开关」,自动绑定关不关都能读到人工绑定的证据。若这里
// 写成「启用自动绑定才读」,默认关闭配置下 reporter 就不会传 evidence_version,
// SetFindingReportVersionByNodeID 便按 legacy 语义写 -1,漏洞详情与 Markdown 导出
// 从此常驻「证据已变更，报告待更新」,而 UI 上没有任何入口能把它清掉。
const reporterToolCallMessage = "上面刚有一个漏洞被 report_finding 登记。请读取返回 JSON 的 finding_id（独立漏洞记录 ID）与 finding_node_id（探索节点 ID），" +
	"先用 get_finding_traffic(finding_id) 读取当前证据清单及其 version（空清单是正常情况，照常写报告）；" +
	"若运行指引启用自动绑定，在读取前先核实并关联本次漏洞的流量。节点详情使用 finding_node_id。" +
	"最后调用 update_finding_report(finding_id=finding_node_id, report, evidence_version=实际读取版本) 保存，" +
	"evidence_version 必须传，否则报告会被永久标记为待更新。不要混用两种编号。"

// 旧版触发消息(0.3.8 及更早)。只有仍与它逐字相同的记录才会被迁移覆盖，用户改过的保持原样。
const reporterToolCallMessageV1 = "上面刚有一个漏洞被 report_finding 登记。请从触发上下文里取出 finding_id" +
	"（工具返回 \"finding recorded: <id>\" 里的数字）与任务 id，按你的职责撰写该漏洞的详细报告，" +
	"最后调用 update_finding_report(finding_id, report) 保存。"

// reseedReporterPrompt 把 reporter 的提示词刷成【当前代码默认】——默认正文新增了
// 「详细利用过程」「关键位置与产物（绝对路径 + 访问入口）」「执行过的命令与回显」
// 等报告要求，而 SeedPromptIfEmpty 首插入 only，旧库已有版本收不到。用版本管理
// 【追加一个新版本】并切过去（ResetPromptToDefault），旧版本仍保留在历史里，用户若
// 自定义过可从版本记录找回。settings flag 守卫 → 只做一次。全新库无需处理
// （SeedPromptIfEmpty 已 seed 最新默认）。与 reseedWorkerPrompt 完全同构。
func (s *Server) reseedReporterPrompt() {
	const flag = "reporter_prompt_detailed_exploitation_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 无论成功与否只尝试一次
	a, err := s.m.pg.GetAgentByKey("reporter")
	if err != nil || a == nil {
		return // 全新库尚未建 agent 行时，seedReporterAgent 会直接 seed 最新默认
	}
	tmpl := agent.ReporterDefaultPrompt
	if tmpl == "" {
		return
	}
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] reporter 提示词重刷为新默认失败: %v", err)
		return
	}
	log.Printf("[prompts] reporter 提示词已追加新默认版本(详细利用过程/绝对路径/命令回显,一次性)")
}

// upgradeReporterTriggerMessage 把老库里仍是默认文案的 reporter 触发消息刷成新版本。
// seedReporterAgent 受 reporter_agent_seed_v1 守卫且只在新建 agent 时写触发器，所以
// 升级上来的库拿不到新文案 —— 工具 schema 由 seedFindingTrafficTools 补齐了
// evidence_version，但没有任何东西告诉 reporter 去用它。一次性，且只覆盖未被改动的文案。
func (s *Server) upgradeReporterTriggerMessage() {
	const flag = "reporter_trigger_evidence_version_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}() // 只尝试一次
	triggers, err := s.m.pg.ListTriggersFor("reporter")
	if err != nil {
		log.Printf("[reporter] 读取触发器失败: %v", err)
		return
	}
	for _, t := range triggers {
		if !t.OnToolCall || t.ToolCallMessage != reporterToolCallMessageV1 {
			continue // 用户改过或不是 finding 触发器，不动。
		}
		t.ToolCallMessage = reporterToolCallMessage
		if err := s.m.pg.UpdateTrigger(t); err != nil {
			log.Printf("[reporter] 升级触发消息失败: %v", err)
			return
		}
		log.Printf("[reporter] 触发消息已升级为读取并回传 evidence_version")
	}
}

// seedReporterAgent 预置一个「报告撰写」自定义 agent(builtin=false，可在 UI 编辑/删除)：
// 绑定 update_finding_report + 任务查询工具，并挂一个「report_finding 被调用即触发」的
// 触发器 —— 每登记一个漏洞就唤起它写详细报告。一次性(settings flag 守卫)：用户删掉后不再重建。
// 依赖：orchestration 工具已在本函数上方 SeedTool 入库，故绑定得上。
func (s *Server) seedReporterAgent() {
	const flag = "reporter_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}() // 无论成功与否只尝试一次

	if exist, _ := s.m.pg.GetAgentByKey("reporter"); exist != nil {
		return // key 已被占用(用户手建过)——不覆盖
	}
	a, err := s.m.pg.CreateAgent("reporter", "报告撰写",
		"漏洞详细报告撰写：发现漏洞时自动触发，查取证据与执行过程后写 Markdown 报告并回写。")
	if err != nil {
		log.Printf("[reporter] 创建 agent 失败: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err != nil {
		log.Printf("[reporter] seed prompt 失败: %v", err)
	}
	// 触发运行策略：parallel + none —— 一漏洞一报告、多个 finding 并发各写各的。
	// merge 必须为 none：否则(默认 all)一波 finding 会被合并成一次运行，并行就没意义。
	// maxParallel=5：同时最多 5 个报告会话，避免瞬时太多 LLM 调用。
	if err := s.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err != nil {
		log.Printf("[reporter] 设置触发运行策略失败: %v", err)
	}
	// 绑定它需要的工具：写报告 + 读证据/执行过程/态势。
	if err := s.m.pg.AddAgentToToolBinding("reporter", []string{
		"update_finding_report", "get_task_node_detail", "list_task_findings",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"get_task_graph",
	}); err != nil {
		log.Printf("[reporter] 绑定工具失败: %v", err)
	}
	// 触发器：report_finding 被调用即触发（工具返回 "finding recorded: <id>" 带上 finding_id，
	// 任务 id 也在触发消息里）。
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:        "reporter",
		Enabled:         true,
		OnToolCall:      true,
		ToolNames:       []string{"report_finding"},
		ToolCallMessage: reporterToolCallMessage,
	}); err != nil {
		log.Printf("[reporter] 创建触发器失败: %v", err)
	}
	log.Printf("[reporter] 已预置「报告撰写」agent + finding 触发器")
}

// playbookToolCallMessage 是「打法沉淀」agent 的触发文案：report_foothold 一响就唤起。
const playbookToolCallMessage = "上面刚有一个据点在 report_foothold 里登记（= 打穿信号）。请沉淀打法：" +
	"先用 get_task_node_detail 读据点节点、用 get_task_worker_trace 读该意图的完整执行过程，" +
	"确认 confidence=high 且证据里确有可复现通路；从 list_assets 取目标真实指纹；" +
	"先 lookup_playbooks 查同指纹是否已有卡片；最后用 record_playbook 写入或更新一张可跨目标复用的打法卡。" +
	"未确认（medium/low）不要沉淀，说明原因即可；卡片必须脱敏（不写凭据/客户域名/IP）。"

// playbookDistillBody 是「沉淀一次」的共用指令尾部：目标达成与手动复盘都按它判断。
const playbookDistillBody = "请判断这次是否留下一条可跨目标复用的方法：读任务图 / 已确认发现 / 意图与 worker trace，还原真正走通的链路（入口→验证→利用→结果/权限），只写有证据的部分。" +
	"只要方法可复现（high 置信度、有请求/命令/回显；不要求拿到 shell），就用 record_playbook 沉淀；覆盖率/侦察类目标没有完整方法时不要硬写，直接说明“无新打法”并结束。" +
	"先 lookup_playbooks 查同指纹/同手法是否已有卡片：有则用相同 technique_key 更新，不要重复建卡；指纹优先用 asset_ids 从资产库取；" +
	"source_node_id 传对应的据点/发现节点 id（没有可省略）；证据必须脱敏（不写凭据/客户域名/IP）。"

// playbookGoalMessage 是「目标达成」触发：即使没有 report_foothold（例如靠一个高置信度
// 漏洞证明目标），也要把真正走通的方法沉淀下来；覆盖率/侦察类目标允许明确说“无新打法”。
const playbookGoalMessage = "本任务刚达成一个目标（触发上下文里有目标文本）。这是「目标达成」触发，不是 report_foothold；没有据点也可以沉淀。" + playbookDistillBody

// playbookManualMessage 是操作者在任务详情手动点「沉淀本任务打法」时的触发文案。
const playbookManualMessage = "操作者手动要求复盘本任务并沉淀打法（不要求本任务刚达成目标）。" + playbookDistillBody

// seedPlaybookAgent 预置一个「打法沉淀」自定义 agent（builtin=false，可在 UI 编辑/删除）：
// 挂 report_foothold 的 on_tool_call 触发器，以及目标达成的 on_goal_met 触发器——
// 前者覆盖“拿到据点”，后者覆盖“靠确认漏洞/完整链路完成目标但没有据点”的情况。
// 一次性(settings flag 守卫)：用户删掉后不再重建。
func (s *Server) seedPlaybookAgent() {
	const flag = "playbook_agent_seed_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }() // 无论成功与否只尝试一次

	if exist, _ := s.m.pg.GetAgentByKey("playbook"); exist != nil {
		return // key 已被占用(用户手建过)——不覆盖
	}
	a, err := s.m.pg.CreateAgent("playbook", "打法沉淀",
		"打穿目标（report_foothold）或达成目标时自动触发：还原执行过程与目标指纹，沉淀成可跨目标复用的打法卡片。")
	if err != nil {
		log.Printf("[playbook] 创建 agent 失败: %v", err)
		return
	}
	if err := s.m.pg.SeedPromptIfEmpty(a.ID, agent.PlaybookDefaultPrompt); err != nil {
		log.Printf("[playbook] seed prompt 失败: %v", err)
	}
	// 一据点一卡片、并发各写各的；merge 必须 none，否则一波据点会被合并成一次运行。
	if err := s.m.pg.SetAgentTriggerBehavior("playbook", "parallel", "none", 3); err != nil {
		log.Printf("[playbook] 设置触发运行策略失败: %v", err)
	}
	if err := s.m.pg.AddAgentToToolBinding("playbook", []string{
		"record_playbook", "lookup_playbooks", "get_task_node_detail",
		"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
		"list_assets", "list_task_findings", "get_task_graph",
	}); err != nil {
		log.Printf("[playbook] 绑定工具失败: %v", err)
	}
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:        "playbook",
		Enabled:         true,
		OnToolCall:      true,
		ToolNames:       []string{"report_foothold"},
		ToolCallMessage: playbookToolCallMessage,
	}); err != nil {
		log.Printf("[playbook] 创建触发器失败: %v", err)
		return
	}
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:    "playbook",
		Enabled:     true,
		OnGoalMet:   true,
		GoalMessage: playbookGoalMessage,
	}); err != nil {
		log.Printf("[playbook] 创建目标达成触发器失败: %v", err)
		return
	}
	log.Printf("[playbook] 已预置「打法沉淀」agent + 据点/目标达成触发器")
}

// seedPlaybookGoalTrigger 给存量库补上「目标达成」触发器（一次性）。
// 老库的 playbook agent 只有 report_foothold 触发——靠确认漏洞完成目标的任务因此
// 从不沉淀。保留用户已有的任何 on_goal_met 配置，只在缺失时补一条。
func (s *Server) seedPlaybookGoalTrigger() {
	const flag = "playbook_goal_trigger_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}()
	a, err := s.m.pg.GetAgentByKey("playbook")
	if err != nil || a == nil {
		return // 用户删掉了 playbook agent：尊重删除，不重建
	}
	trs, err := s.m.pg.ListTriggersFor("playbook")
	if err != nil {
		return
	}
	for _, tr := range trs {
		if tr.OnGoalMet {
			return // 用户已自行配置过目标达成触发
		}
	}
	if _, err := s.m.pg.CreateTrigger(&db.AgentTrigger{
		AgentKey:    "playbook",
		Enabled:     true,
		OnGoalMet:   true,
		GoalMessage: playbookGoalMessage,
	}); err != nil {
		log.Printf("[playbook] 补目标达成触发器失败: %v", err)
		return
	}
	log.Printf("[playbook] 已为存量库补上目标达成触发器")
}

// reseedPlaybookPrompt 只替换**仍带旧据点触发开头**的打法沉淀提示词；用户改掉该
// 开头的保留原样（尊重自定义）。旧版本留在版本历史里可回滚。
func (s *Server) reseedPlaybookPrompt() {
	const flag = "playbook_prompt_goal_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}()
	a, err := s.m.pg.GetAgentByKey("playbook")
	if err != nil || a == nil {
		return
	}
	cur, err := s.m.pg.CurrentPrompt(a.ID)
	if err != nil || !strings.Contains(cur, "刚刚有 worker 拿到了据点（report_foothold）") {
		return // 用户改过开头：不覆盖
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, agent.PlaybookDefaultPrompt); err != nil {
		log.Printf("[playbook] 刷新提示词失败: %v", err)
		return
	}
	log.Printf("[playbook] 提示词已支持目标达成触发（一次性）")
}

// seedCVEInterceptTool 给存量库补上 cve_exploit 的规则面覆盖。
//
// defaultEnabledTools 只在 intercept_enabled_tools 从未保存时生效；老库保存过这个
// 设置时，新工具不会自动进入规则/裁判面。保留用户已有选择，只追加 cve_exploit。
// 用户把所有工具都关掉（空列表）时不擅自改回，记录日志并跳过。
func (s *Server) seedCVEInterceptTool() {
	const flag = "intercept_cve_tool_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() {
		if err := s.m.pg.SetSetting(flag, "true"); err != nil {
			log.Printf("[intercept] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
		}
	}()
	if _, ok, _ := s.m.pg.GetSetting("intercept_enabled_tools"); !ok {
		return // 从未保存：defaultEnabledTools 已包含 cve_exploit
	}
	names, err := s.m.interceptor.GetEnabledTools()
	if err != nil {
		log.Printf("[intercept] 读取已启用工具失败，暂不追加 cve_exploit: %v", err)
		return
	}
	if len(names) == 0 {
		log.Printf("[intercept] intercept_enabled_tools 为空（用户关闭了全部工具），不擅自追加 cve_exploit")
		return
	}
	for _, n := range names {
		if n == "cve_exploit" {
			return
		}
	}
	if err := s.m.interceptor.SetEnabledTools(append(names, "cve_exploit")); err != nil {
		log.Printf("[intercept] 追加 cve_exploit 失败: %v", err)
		return
	}
	log.Printf("[intercept] 已把 cve_exploit 追加到已启用工具列表")
}

// reseedPentestPrompt 把 pentest（独立渗透 agent）的提示词刷成当前代码默认——
// 默认正文新增了 lookup_playbooks 打法复用引导，SeedPromptIfEmpty 首插入 only，
// 旧库已有版本收不到。与 reseedWorkerPrompt 同构。
func (s *Server) reseedPentestPrompt() {
	const flag = "pentest_prompt_playbooks_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	defer func() { _ = s.m.pg.SetSetting(flag, "true") }()
	a, err := s.m.pg.GetAgentByKey("pentest")
	if err != nil || a == nil {
		return
	}
	tmpl := agent.BuiltinPromptSeeds()["pentest"]
	if tmpl == "" {
		return
	}
	if cur, err := s.m.pg.CurrentPrompt(a.ID); err == nil && cur == tmpl {
		return
	}
	if _, err := s.m.pg.ResetPromptToDefault(a.ID, tmpl); err != nil {
		log.Printf("[prompts] pentest 提示词重刷为新默认失败: %v", err)
		return
	}
	log.Printf("[prompts] pentest 提示词已追加新默认版本(加入 lookup_playbooks 打法复用,一次性)")
}

// seedAutoReportFindingBinding adds "auto" to report_finding's binding ONCE so
// conversation-context agents can call it without requiring an intent_id.
func (s *Server) seedAutoReportFindingBinding() {
	const flag = "auto_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err != nil {
		log.Printf("[auto] report_finding 默认绑定失败: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}

// seedPlannerDefaultBindings adds "planner" to report_finding's binding ONCE
// (guarded by a settings flag), so existing DBs — whose report_finding row was
// seeded as worker-only — also let the planner record findings. Fresh DBs already
// get it via PlannerTools(); this only backfills without overriding a user unbind.
func (s *Server) seedPlannerDefaultBindings() {
	const flag = "planner_report_finding_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err != nil {
		log.Printf("[planner] report_finding 默认绑定失败: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}

// seedPlannerListAssetsBinding adds "planner" to list_assets's binding ONCE
// (guarded by a settings flag), so existing DBs — whose list_assets row was seeded
// as auto/pentest-only — also let the planner query the asset store by DSL. Fresh
// DBs already get it via PlannerTools(); this only backfills without overriding a
// user unbind.
func (s *Server) seedPlannerListAssetsBinding() {
	const flag = "planner_list_assets_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err != nil {
		log.Printf("[planner] list_assets 默认绑定失败: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}

// seedCompanyScopeRebind changes add_company_scope's default binding ONCE on
// existing DBs (guarded by a settings flag): the tool moves off worker and onto
// planner — defining a company's asset scope is a planning/main/auto concern, not
// something a worker does mid-exploration. Fresh DBs already get planner via
// PlannerTools() and lack worker via WorkerTools(); this only backfills old rows.
// One-shot + flag-guarded so a user who later re-binds worker isn't overridden.
// seedGraphToolBindings 把「图相关工具」补绑到平台级 agent（auto / pentest）——一次性、只加不减。
//
// 为什么：这几个工具（域内/云上枚举的摄入、攻击路径、事前可检出性）最初只绑了
// planner/worker/mainagent，而**写图的 insert_assets / list_assets 是绑 auto + pentest 的**。
// 于是从对话页（Auto / 渗透测试）里"把这份 SharpHound 结果导进任务图"这件事根本调不到——
// 而人恰恰最可能从对话里做这件事。同一族工具、两套绑定，是个说不通的差异。
//
// 只加不减：用户自己解绑过的不动（与 company_scope_rebind 同一条纪律）。
func (s *Server) seedGraphToolBindings() {
	const flag = "graph_tools_bind_auto_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys := []string{"ingest_ad_data", "ingest_cloud_data", "ad_paths", "opsec_notes"}
	for _, agentKey := range []string{"auto", "pentest"} {
		if err := s.m.pg.AddAgentToToolBinding(agentKey, keys); err != nil {
			log.Printf("[tools] 图相关工具绑定到 %s 失败: %v", agentKey, err)
			return
		}
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
	log.Printf("[tools] 图相关工具（摄入/路径/可检出性）已补绑 auto/pentest（一次性）")
}

func (s *Server) seedCompanyScopeRebind() {
	const flag = "company_scope_rebind_v1" // worker→planner 默认绑定切换
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err != nil {
		log.Printf("[planner] add_company_scope 默认绑定失败: %v", err)
		return
	}
	if err := s.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err != nil {
		log.Printf("[worker] add_company_scope 解绑失败: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}

// seedWorkerReadToolsUnbind strips the read-context tools off worker's default
// binding ONCE on existing DBs (guarded by a settings flag): a worker executes one
// intent and writes back — reading facts/companies and listing all workers' traces is
// a planning/main concern, not the executor's. Fresh DBs already lack these via
// WorkerTools(); this only backfills old rows without overriding a user who
// deliberately re-binds worker. Each RemoveAgentFromTool is per-tool +
// membership-guarded, so planner/mainagent bindings of the same tool are untouched.
//
// NOTE: search_all_worker_traces / get_worker_trace / node_detail are intentionally NOT
// unbound — worker owns them for cross-work look-back + node drill-down (see WorkerTools).
// They used to be in this list back when worker lacked them; seedWorkerReadbackRebind
// repairs DBs whose old run stripped them.
func (s *Server) seedWorkerReadToolsUnbind() {
	const flag = "worker_readtools_unbind_v1"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	for _, k := range []string{
		"list_facts", "list_companies", "list_worker_traces",
	} {
		if err := s.m.pg.RemoveAgentFromTool("worker", k); err != nil {
			log.Printf("[worker] %s 从 worker 解绑失败: %v", k, err)
			return // 出错则不落 flag，下次启动重试
		}
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}

// seedWorkerReadbackRebind re-binds the cross-work look-back / drill-down tools onto
// worker ONCE (guarded by a settings flag): an earlier seedWorkerReadToolsUnbind wrongly
// stripped search_all_worker_traces / get_worker_trace / node_detail from worker after
// they had been added to WorkerTools(), so any DB that ran that migration lost them.
// Fresh DBs already have them via WorkerTools() and this is a harmless no-op there.
// One-shot + flag-guarded so a user who later deliberately unbinds them isn't overridden.
func (s *Server) seedWorkerReadbackRebind() {
	const flag = "worker_readback_rebind_v2" // v2: 追加 node_detail
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("worker", []string{
		"search_all_worker_traces", "get_worker_trace", "node_detail",
	}); err != nil {
		log.Printf("[worker] 回看/详情工具补绑失败: %v", err)
		return // 出错则不落 flag，下次启动重试
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}

// seedBuiltinBindingBackfill 补齐存量库漏绑的工具。
//
// tools.agents 是**首次 seed 时**写入的；之后代码把某个工具加进 agent 的默认工具集，
// 不会自动改已有行。而运行期 ToolResolve 会按 DB 行过滤——于是出现「代码里有、提示词
// 也让人调、运行期却被丢掉」的漂移。这里只补代码明确要求的两个：
//   - planner: coverage_matrix（规划补测前要看资产×风险类矩阵）
//   - mainagent: list_companies（promote_knowledge 的提示要它拿 company_id）
//
// 新库由 BuiltinToolSeeds 直接 seed 正确；本迁移只对存量库生效，一次性。
func (s *Server) seedBuiltinBindingBackfill() {
	const flag = "builtin_binding_backfill_20261025"
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("planner", []string{"coverage_matrix"}); err != nil {
		log.Printf("[migrate] planner 补绑 coverage_matrix 失败: %v", err)
		return
	}
	if err := s.m.pg.AddAgentToToolBinding("mainagent", []string{"list_companies"}); err != nil {
		log.Printf("[migrate] mainagent 补绑 list_companies 失败: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}

// seedAutoDefaultBindings adds "auto" to the task-op + platform tools' bindings
// ONCE (guarded by a settings flag), so existing DBs whose tool rows were seeded
// before Auto existed still give Auto its default toolset — without re-adding it
// after a user deliberately unbinds.
func (s *Server) seedAutoDefaultBindings() {
	const flag = "auto_default_bindings_v3" // v3: 替换旧资产工具名，加入 insert_assets/add_company_scope
	if v, _, _ := s.m.pg.GetSetting(flag); v == "true" {
		return
	}
	keys := make([]string, 0, len(platformToolKeys)+12)
	for _, t := range s.orchestrationTools() {
		keys = append(keys, t.Name())
	}
	keys = append(keys, platformToolKeys...)
	// 资产工具：Auto 操作平台常要看/登记资产、管理公司范围。
	keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
	if err := s.m.pg.AddAgentToToolBinding("auto", keys); err != nil {
		log.Printf("[auto] 默认绑定失败: %v", err)
		return
	}
	if err := s.m.pg.SetSetting(flag, "true"); err != nil {
		log.Printf("[migrate] 记录一次性迁移标记 %s 失败: %v（下次启动会重跑，操作本身幂等）", flag, err)
	}
}
