package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"

	"github.com/lin9527ya/blade/db"
)

// 目标侧痕迹工具：与遗留物（report_artifact）分开，记录“目标可能看见什么信号”，
// 不是“我们留下了什么对象”。自动归纳在 server 侧扫描命令历史 + opsec 知识库完成；
// 这里给 worker 补录知识库未覆盖或实测发现的痕迹，并给清理意图销账。

func (t *ToolSet) reportFootprint() actool.CoreTool {
	return t.writeExpTool("report_footprint",
		"记录一条【目标侧痕迹】：这次动作可能在目标上留下什么可检出信号（登录失败/认证日志/EDR 遥测/访问日志/扫描记录/本机 shell 历史等）。"+
			"它跟 report_artifact 的区别：artifact 记我们留下的具体对象（文件/账号/任务），footprint 记可能被目标或防守方看见的信号。"+
			"自动归纳会从实际执行过的命令 + 可检出性知识库生成；知识库没覆盖、但你实测/观察到的痕迹用本工具补录。"+
			"必须给 evidence（观察到/触发时的命令与回显）与 confidence。**不要**把“可能”写成“已清理”。",
		obj(map[string]any{
			"summary":    str("一句话：在哪台目标的哪个面留下了什么痕迹"),
			"category":   str("分类：log（目标系统日志）| edr（EDR/HIDS/AV）| network（边界/IDS/代理）| app（应用/中间件日志）| local（本机 history/临时文件）| other"),
			"where":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "留痕位置（可多条，如 目标系统日志 / 网络侧 / 应用日志）"},
			"signals":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "具体信号（Windows 事件 ID、syslog/auditd 记录、WAF/IDS 告警名等）"},
			"exposure":   str("预计可见度：low | medium | high"),
			"host":       str("哪台目标（域名或 IP）"),
			"locator":    str("具体位置（可选）：日志文件/系统事件通道/账号/路径——报告与清理靠它"),
			"cleanup":    str("建议怎么清理（可选）：写清可执行的定向清理方式；清不了就留空"),
			"state":      str("open（可能还在，默认）| unknown（不确定是否存在）——不能写 cleaned/accepted，那要清理/接受工具来做"),
			"detail":     str("细节：哪一步动作产生、为什么可能被看见、有没有边界设备/EDR 线索"),
			"evidence":   str("【必填】观察到/触发时的命令 + 关键回显，或明确的检测现象"),
			"confidence": str("high（亲手看到）| medium（间接观察）| low（推断）"),
			"intent_id":  idp("当前意图 id（你领到的意图）"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "相关资产 id"},
			"source_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "可选：这条痕迹由哪些事实/漏洞/战果/意图留下（节点 id）。留空就自动连到当前意图；跨步骤的痕迹（例如某条 fact 的结论直接导致的日志记录）在这里点名"},
		}, "summary", "evidence", "confidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				lootBase
				Category  string            `json:"category"`
				Where     []string          `json:"where"`
				Signals   []string          `json:"signals"`
				Exposure  string            `json:"exposure"`
				Host      string            `json:"host"`
				Locator   string            `json:"locator"`
				Cleanup   string            `json:"cleanup"`
				SourceIDs []json.RawMessage `json:"source_ids"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			state := strings.TrimSpace(a.State)
			if state == "" {
				state = db.StateFootprintOpen
			}
			if state != db.StateFootprintOpen && state != db.StateFootprintUnknown {
				return actool.Errorf("state 只能是 open | unknown；cleaned/accepted 由 mark_footprint_cleaned / accept_footprint_risk 处理"), nil
			}
			a.State = state
			extra := map[string]any{
				"category": db.NormalizeFootprintCategory(a.Category),
				"source":   db.FootprintSourceObserved,
				"where":    cleanFootprintStrings(a.Where, 12),
				"signals":  cleanFootprintStrings(a.Signals, 12),
				"exposure": db.NormalizeFootprintExposure(a.Exposure),
				"host":     strings.TrimSpace(a.Host),
				"locator":  strings.TrimSpace(a.Locator),
				"cleanup":  strings.TrimSpace(a.Cleanup),
			}
			if ids := pidList(a.AssetIDs); len(ids) > 0 {
				extra["asset_ids"] = ids
			}
			id, err := t.recordLoot(db.KindFootprint, a.lootBase, extra)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// recordLoot 已经把当前意图连成 leaves；这里再补显式点名的来源节点，
			// 让「痕迹来自哪条事实/漏洞」在图上读得出来。
			for _, src := range pidList(a.SourceIDs) {
				if src == id {
					continue
				}
				if _, lerr := t.ts.LinkFootprintSource(src, id); lerr != nil {
					return actool.Errorf(fmt.Sprintf("痕迹已记录（#%d），但来源连线失败：%v", id, lerr)), nil
				}
			}
			t.writes.Footprints++
			return actool.Text(fmt.Sprintf("footprint recorded: %d", id)), nil
		})
}

func cleanFootprintStrings(in []string, max int) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

func (t *ToolSet) listFootprints() actool.CoreTool {
	return t.readExpTool("list_footprints",
		"列出本任务的目标侧痕迹（未处置/未确认/接受风险/已清理），含 id、摘要、位置、暴露度与清理建议。"+
			"收官前或收到清理意图时用它确认还有哪些没处理。自动归纳来自执行命令 + opsec 知识库，是预期不是实测。",
		obj(map[string]any{
			"state": str("可选：open | unknown | accepted | cleaned；省略 = 全部"),
			"limit": intp("最多返回条数（默认 50，上限 200）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				State string `json:"state"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 || limit > 200 {
				limit = 50
			}
			nodes, err := t.ts.ListByKind(db.KindFootprint, limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			state := strings.TrimSpace(a.State)
			out := make([]map[string]any, 0, len(nodes))
			for _, n := range nodes {
				if state != "" && n.State != state {
					continue
				}
				f := db.ParseFootprint(n)
				item := map[string]any{
					"id": n.ID, "state": n.State, "state_label": db.FootprintStateLabel(n.State),
					"summary": f.Summary, "source": f.Source, "category": db.FootprintCategoryLabel(f.Category),
					"where": f.Where, "signals": f.Signals, "exposure": db.FootprintExposureLabel(f.Exposure),
				}
				if f.Host != "" {
					item["host"] = f.Host
				}
				if f.Locator != "" {
					item["locator"] = f.Locator
				}
				if f.Cleanup != "" {
					item["cleanup"] = f.Cleanup
				}
				out = append(out, item)
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) markFootprintCleaned() actool.CoreTool {
	return t.writeExpTool("mark_footprint_cleaned",
		"把一条目标侧痕迹标成【已清理】。必须给清理时的那行输出（证据）；只允许 open/unknown → cleaned，重复销账会失败。"+
			"只在真的清掉时用：清不了就写 fact 说明并保持 open，不要销账。",
		obj(map[string]any{
			"footprint_id": idp("痕迹节点 id（list_footprints 返回的 id）"),
			"evidence":     str("【必填】清理命令 + 关键输出；日志类写清改了哪条记录/时间窗"),
			"note":         str("可选备注：遗留、需人工确认的部分"),
		}, "footprint_id", "evidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				FootprintID json.RawMessage `json:"footprint_id"`
				Evidence    string          `json:"evidence"`
				Note        string          `json:"note"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.FootprintID)
			if id <= 0 {
				return actool.Errorf("footprint_id 必填"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindFootprint {
				return actool.Errorf("痕迹不存在或不是 footprint 节点"), nil
			}
			changed, err := t.ts.MarkFootprintCleaned(id, a.Evidence, a.Note)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if changed == 0 {
				return actool.Errorf("当前状态不可标记已清理（可能已经清理过，或已被接受风险）"), nil
			}
			t.writes.Footprints++
			return actool.Text(fmt.Sprintf("footprint cleaned: %d", id)), nil
		})
}

func (t *ToolSet) acceptFootprintRisk() actool.CoreTool {
	return t.writeExpTool("accept_footprint_risk",
		"决定【不清理】一条目标侧痕迹并接受风险：必须写理由（为什么清不了/不清，客户与后来人需要知道）。"+
			"只允许 open/unknown → accepted。清理可行时优先清理，而不是直接接受。",
		obj(map[string]any{
			"footprint_id": idp("痕迹节点 id"),
			"reason":       str("【必填】接受风险的理由（无法清理/客户接受/清理会破坏更重要的证据等）"),
		}, "footprint_id", "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				FootprintID json.RawMessage `json:"footprint_id"`
				Reason      string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.FootprintID)
			if id <= 0 {
				return actool.Errorf("footprint_id 必填"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindFootprint {
				return actool.Errorf("痕迹不存在或不是 footprint 节点"), nil
			}
			changed, err := t.ts.AcceptFootprintRisk(id, a.Reason)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if changed == 0 {
				return actool.Errorf("当前状态不可接受风险（可能已清理或已接受）"), nil
			}
			t.writes.Footprints++
			return actool.Text(fmt.Sprintf("footprint risk accepted: %d", id)), nil
		})
}

func (t *ToolSet) markFootprintNotFound() actool.CoreTool {
	return t.writeExpTool("mark_footprint_not_found",
		"核实后确认一条目标侧痕迹**并不存在**（日志没有该记录/文件不在/事件未发生）时使用；必须给核实证据。"+
			"只允许 open/unknown/accepted → not_found。它不同于 cleaned：cleaned 是清掉了，not_found 是本来就没有。",
		obj(map[string]any{
			"footprint_id": idp("痕迹节点 id"),
			"evidence":     str("【必填】在哪查了、看到了什么（命令 + 关键输出）"),
			"note":         str("可选备注"),
		}, "footprint_id", "evidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				FootprintID json.RawMessage `json:"footprint_id"`
				Evidence    string          `json:"evidence"`
				Note        string          `json:"note"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.FootprintID)
			if id <= 0 {
				return actool.Errorf("footprint_id 必填"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil || n == nil || n.Kind != db.KindFootprint {
				return actool.Errorf("痕迹不存在或不是 footprint 节点"), nil
			}
			changed, err := t.ts.MarkFootprintNotFound(id, a.Evidence, a.Note)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if changed == 0 {
				return actool.Errorf("当前状态不可标记未发现（可能已清理/已接受/已核实）"), nil
			}
			t.writes.Footprints++
			return actool.Text(fmt.Sprintf("footprint not found: %d", id)), nil
		})
}

// isCleanupIntentPayload reports whether an intent node came from the footprint
// cleanup flow. Such intents represent an operator's explicit clean-up decision;
// the planner must not drop or kill them to force a task to close.
func isCleanupIntentPayload(n *db.Node) bool {
	if n == nil || len(n.Payload) == 0 {
		return false
	}
	var p struct {
		Cleanup bool `json:"cleanup"`
	}
	return json.Unmarshal(n.Payload, &p) == nil && p.Cleanup
}

// linkIntentSource 让规划者把一条**已经建好**的意图补上真实血缘。
//
// 存在的理由：add_intent 的 parent_ids 是可选的，模型漏传时意图会被兜底连到任务起点，
// 图上就变成「从起点发散」，后续轮次读 parents 也只看得到 origin。重派一条意图可以修
// 血缘，但会丢掉已经跑出来的产出；这个工具只补边、不动节点。
func (t *ToolSet) linkIntentSource() actool.CoreTool {
	return t.writeExpTool("link_intent_source",
		"给一条已存在的意图补上「派生自哪条事实/漏洞」的血缘边（parent_ids 漏传时用）。"+
			"补上真实来源后会自动去掉指向任务起点的兜底边，链路图才读得出真实因果。",
		obj(map[string]any{
			"intent_id":  idp("要补血缘的意图 id"),
			"source_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "来源节点 id（事实/漏洞，0/1/多个）：这条意图是由哪些已确认结论综合出来的"},
		}, "intent_id", "source_ids"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID  json.RawMessage   `json:"intent_id"`
				SourceIDs []json.RawMessage `json:"source_ids"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			sources := pidList(a.SourceIDs)
			if len(sources) == 0 {
				return actool.Errorf("source_ids 必填：要给这条意图补的来源事实/漏洞 id"), nil
			}
			linked := 0
			for _, src := range sources {
				added, err := t.ts.LinkIntentSource(id, src)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if added {
					linked++
				}
			}
			return actool.Text(fmt.Sprintf("intent %d: 已补 %d 条血缘边（来源 %v）", id, linked, sources)), nil
		})
}
