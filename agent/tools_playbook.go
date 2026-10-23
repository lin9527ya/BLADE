package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	actool "github.com/lin9527ya/Norma/tool"
	"github.com/lin9527ya/blade/db"
)

// 指纹打法库（agent 侧）：打穿目标后沉淀「目标指纹 → 攻击手法」，后来人按指纹命中后
// 直接复用验证路径。它和 company_knowledge 的分工见 db/playbooks.go 的包注释。
//
// 三个工具：
//   - lookup_playbooks：按资产 id / 指纹查打法（worker/planner/main 都用）；
//   - record_playbook：沉淀一张卡片（playbook agent 自动调用；planner 可手动补记）；
//   - record_playbook_feedback：实战结果回流（worked/failed），卡片按成功率排序。

const playbookFingerprintSchemaDesc = "指纹对象：technologies(数组)、favicon_mmh3、service_name、port、page_title。" +
	"优先传 asset_ids，让平台从资产库取真实指纹；手填只用于库里没有的记录。"

func fingerprintFromAssets(base db.PlaybookFingerprint, assets []*db.Asset) db.PlaybookFingerprint {
	fp := base
	for _, a := range assets {
		if a == nil {
			continue
		}
		fp.Technologies = append(fp.Technologies, a.Technologies...)
		if fp.FaviconMMH3 == "" {
			fp.FaviconMMH3 = a.FaviconMMH3
		}
		if fp.ServiceName == "" {
			fp.ServiceName = a.ServiceName
		}
		if fp.Port == 0 && a.Port != nil {
			fp.Port = *a.Port
		}
		if fp.PageTitle == "" {
			fp.PageTitle = a.PageTitle
		}
	}
	return fp.Normalize()
}

// playbookLines 给常驻索引用的一行式（≤2 行；空 = 不注入）。与 graph_overview 的
// matching_playbooks 同一份匹配，只是窗口更小——索引每次请求都可能重算（3 秒 TTL）。
func (t *ToolSet) playbookLines() []string {
	if t.as == nil || t.taskID <= 0 {
		return nil
	}
	matches, err := t.as.MatchPlaybooksForTask(t.taskID, 2)
	if err != nil || len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, renderPlaybookLine(m))
	}
	return out
}

// renderPlaybookLine 把一张卡片压成态势里的一行（与 company_knowledge 同一条纪律：
// 它是"别忘了历史打法"的提示，不是完整步骤——完整内容用 lookup_playbooks 取）。
func renderPlaybookLine(m db.PlaybookMatch) string {
	fp := m.Fingerprint.Normalize()
	var fpParts []string
	if len(fp.Technologies) > 0 {
		fpParts = append(fpParts, strings.Join(fp.Technologies, "/"))
	}
	if fp.FaviconMMH3 != "" {
		fpParts = append(fpParts, "favicon "+fp.FaviconMMH3)
	}
	if fp.ServiceName != "" {
		if fp.Port > 0 {
			fpParts = append(fpParts, fmt.Sprintf("%s:%d", fp.ServiceName, fp.Port))
		} else {
			fpParts = append(fpParts, fp.ServiceName)
		}
	}
	stats := "尚无实战反馈"
	if total := m.Successes + m.Failures; total > 0 {
		stats = fmt.Sprintf("成功 %d/%d", m.Successes, total)
	}
	line := fmt.Sprintf("[打法 #%d] %s", m.ID, clipKnowledge(m.Title, 80))
	if len(fpParts) > 0 {
		line += "（指纹 " + strings.Join(fpParts, " · ") + " · " + stats + "）"
	} else {
		line += "（" + stats + "）"
	}
	if m.Summary != "" {
		line += " — " + clipKnowledge(m.Summary, 120)
	}
	return line
}

// lookupPlaybooks 按资产 id 或指纹检索匹配的历史打法。
func (t *ToolSet) lookupPlaybooks() actool.CoreTool {
	return readTool("lookup_playbooks",
		"查【指纹打法库】：遇到可识别的技术栈/CMS/中间件/登录口时，先看历史上有没有在同类指纹上打穿过的打法，再决定怎么验证。\n"+
			"优先传 asset_id（平台从资产库取真实指纹）；没有资产时可直接传 technologies/favicon_mmh3/service_name/port。\n"+
			"返回按匹配度排序的卡片（含完整步骤与来源）。**先确认指纹仍然匹配**，再按步骤验证；用完用 record_playbook_feedback 回报结果。\n"+
			"它是先验不是结论：历史打法命中不等于目标一定有洞，仍要实际触发。",
		obj(map[string]any{
			"asset_id":     idp("资产 id（优先；平台从该资产读 technologies/favicon/port 等指纹）"),
			"technologies": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "技术栈（可选，如 [\"wordpress\",\"php\"]）"},
			"favicon_mmh3": str("favicon MMH3 哈希（可选，最强信号）"),
			"service_name": str("服务名（可选，如 http/https/ssh）"),
			"port":         intp("端口（可选）"),
			"page_title":   str("页面标题（可选，弱信号）"),
			"limit":        intp("返回上限，默认 3"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("lookup_playbooks 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				AssetID      json.RawMessage `json:"asset_id"`
				Technologies []string        `json:"technologies"`
				FaviconMMH3  string          `json:"favicon_mmh3"`
				ServiceName  string          `json:"service_name"`
				Port         int             `json:"port"`
				PageTitle    string          `json:"page_title"`
				Limit        int             `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			fp := db.PlaybookFingerprint{
				Technologies: a.Technologies, FaviconMMH3: a.FaviconMMH3,
				ServiceName: a.ServiceName, Port: a.Port, PageTitle: a.PageTitle,
			}
			assetID := pid(a.AssetID)
			var assetView map[string]any
			if assetID > 0 {
				var assets []*db.Asset
				var err error
				if t.taskID > 0 {
					assets, err = t.as.GetByIDsInScope(t.taskID, []int64{assetID})
				} else {
					assets, err = t.as.GetByIDs([]int64{assetID})
				}
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if len(assets) == 0 {
					return actool.Errorf(fmt.Sprintf("asset_id=%d 不在本任务范围内或不存在", assetID)), nil
				}
				fp = fingerprintFromAssets(fp, assets)
				assetView = map[string]any{
					"id": assets[0].ID, "type": assets[0].Type, "url": assets[0].URL,
					"domain": assets[0].Domain, "technologies": assets[0].Technologies,
					"favicon_mmh3": assets[0].FaviconMMH3, "port": assets[0].Port,
				}
			}
			fp = fp.Normalize()
			if len(fp.Technologies) == 0 && fp.FaviconMMH3 == "" && fp.ServiceName == "" {
				return actool.Errorf("没有可用的指纹：传 asset_id，或至少给 technologies/favicon_mmh3/service_name 之一"), nil
			}
			matches, err := t.as.MatchPlaybooks(fp, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			ids := make([]int64, 0, len(matches))
			for _, m := range matches {
				ids = append(ids, m.ID)
			}
			t.as.TouchPlaybookUses(ids) // 命中即计一次"被检索"，页面的使用次数靠它
			out := map[string]any{
				"fingerprint": fp, "matches": matches, "count": len(matches),
				"note": "匹配只是先验：先确认目标指纹与卡片一致，再按 steps 实际验证；" +
					"验证后用 record_playbook_feedback 回报 worked/failed/inconclusive。空结果不代表目标安全。",
			}
			if assetView != nil {
				out["asset"] = assetView
			}
			return jsonResult(out)
		})
}

// recordPlaybook 沉淀一张打法卡片。
func (t *ToolSet) recordPlaybook() actool.CoreTool {
	return writeTool("record_playbook",
		"把一次**真实打穿**沉淀成可复用的打法卡片：目标指纹 + 攻击手法 + 完整步骤 + 证据。\n"+
			"只有 confidence=high（拿到 shell/会话、当场可复现）才收；未确认的不写。\n"+
			"指纹优先由 asset_ids 从资产库取（不要凭空写技术栈）。同 (指纹, 手法) 会更新已有卡片而不是重复创建。\n"+
			"**脱敏**：不写凭据、token、cookie、客户域名/IP、人员信息；主机名用占位符，路径与 payload 可保留。\n"+
			"步骤要能换一个同指纹的目标照着做：识别线索 → 验证 → 利用 → 落地 → 提权/横向 → 坑与失败分支。",
		obj(map[string]any{
			"title":         str("打法名（一句话，如「WordPress + WP File Manager 插件 → 未授权上传 getshell」）"),
			"summary":       str("一行式：适用条件 + 效果"),
			"technique_key": str("手法 slug（如 shiro-rememberme-deser）；留空由 title 归一，用于去重"),
			"steps":         str("完整步骤（Markdown）：识别线索→验证→利用→落地→提权/横向→坑/失败分支"),
			"evidence":      str("原始证据（必填）：证明本次真实打穿的命令/请求与关键回显（脱敏后）"),
			"confidence":    str("必须 high（本次真实拿到据点、可复现）；medium/low 不收"),
			"fingerprint": map[string]any{
				"type": "object", "description": playbookFingerprintSchemaDesc,
				"properties": map[string]any{
					"technologies": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "技术栈（小写，如 wordpress/php/shiro）"},
					"favicon_mmh3": str("favicon MMH3 哈希（最强信号）"),
					"service_name": str("服务名（如 http/https/ssh）"),
					"port":         intp("端口"),
					"page_title":   str("页面标题（弱信号）"),
				},
			},
			"asset_ids":      map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "被攻陷目标的资产 id（平台据此取真实指纹，强烈建议传）"},
			"source_task_id": str("来源任务 id（可回溯）"),
			"source_node_id": idp("来源据点节点 id（report_foothold 返回的那个数）"),
			"source_company": str("来源客户名（审计用；留空时按 source_task_id 自动补）"),
		}, "title", "steps", "evidence", "confidence"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("record_playbook 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				Title         string                 `json:"title"`
				Summary       string                 `json:"summary"`
				TechniqueKey  string                 `json:"technique_key"`
				Steps         string                 `json:"steps"`
				Evidence      string                 `json:"evidence"`
				Confidence    string                 `json:"confidence"`
				Fingerprint   db.PlaybookFingerprint `json:"fingerprint"`
				AssetIDs      []json.RawMessage      `json:"asset_ids"`
				SourceTaskID  string                 `json:"source_task_id"`
				SourceNodeID  json.RawMessage        `json:"source_node_id"`
				SourceCompany string                 `json:"source_company"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if db.NormalizeConfidence(a.Confidence) != db.ConfidenceHigh {
				return actool.Errorf("confidence 必须是 high：只沉淀真实打穿、可复现的打法"), nil
			}
			if strings.TrimSpace(a.Evidence) == "" {
				return actool.Errorf("evidence 必填：给出证明本次真实打穿的命令/请求与关键回显"), nil
			}
			ids := pidList(a.AssetIDs)
			fp := a.Fingerprint.Normalize()
			if len(ids) > 0 {
				var assets []*db.Asset
				var err error
				if t.taskID > 0 {
					assets, err = t.as.GetByIDsInScope(t.taskID, ids)
				} else {
					assets, err = t.as.GetByIDs(ids)
				}
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				fp = fingerprintFromAssets(fp, assets) // 库里的真实指纹优先/补齐
			}
			if len(fp.Technologies) == 0 && fp.FaviconMMH3 == "" && fp.ServiceName == "" {
				return actool.Errorf("指纹为空：传 asset_ids，或在 fingerprint 里给 technologies/favicon_mmh3/service_name"), nil
			}
			taskID := t.taskID
			sourceTask := strings.TrimSpace(a.SourceTaskID)
			if sourceTask == "" && taskID > 0 {
				sourceTask = strconv.FormatInt(taskID, 10)
			}
			company := strings.TrimSpace(a.SourceCompany)
			if company == "" {
				forCompany := taskID
				if forCompany <= 0 {
					forCompany, _ = strconv.ParseInt(sourceTask, 10, 64)
				}
				company = t.as.TaskCompanyName(forCompany)
			}
			saved, inserted, err := t.as.UpsertPlaybook(db.Playbook{
				Fingerprint: fp, TechniqueKey: a.TechniqueKey, Title: a.Title, Summary: a.Summary,
				Steps: a.Steps, Evidence: a.Evidence, Confidence: db.ConfidenceHigh,
				SourceTaskID: sourceTask, SourceNodeID: pid(a.SourceNodeID), SourceCompany: company,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"playbook_id": saved.ID, "inserted": inserted, "revision": saved.Revision,
				"fingerprint": saved.Fingerprint, "technique_key": saved.TechniqueKey,
				"note": "已入打法库（全局）。同一指纹/手法再次沉淀会更新这张卡而不是重复创建。",
			})
		})
}

// recordPlaybookFeedback 记录一次实战反馈。
func (t *ToolSet) recordPlaybookFeedback() actool.CoreTool {
	return writeTool("record_playbook_feedback",
		"回报一条打法在本次目标上的实战结果：worked（按它打通/验证成功）、failed（不适用或被拦）、inconclusive（没试成/无法判断）。\n"+
			"成功会刷新卡片的「最近验证」并抬升排序；失败会降低成功率——打法库靠它越用越准。note 写清目标差异或失败原因。",
		obj(map[string]any{
			"playbook_id": idp("打法 id（lookup_playbooks 返回）"),
			"outcome":     str("worked | failed | inconclusive"),
			"note":        str("一句话：本次怎么用的、结果差异/失败原因（例如「WAF 拦了原 payload，换成编码后成功」）"),
		}, "playbook_id", "outcome"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("record_playbook_feedback 未启用: AssetStore 未初始化"), nil
			}
			var a struct {
				PlaybookID json.RawMessage `json:"playbook_id"`
				Outcome    string          `json:"outcome"`
				Note       string          `json:"note"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			id := pid(a.PlaybookID)
			if id <= 0 {
				return actool.Errorf("playbook_id 无效"), nil
			}
			saved, err := t.as.RecordPlaybookFeedback(id, a.Outcome, a.Note)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"playbook_id": saved.ID, "outcome": saved.LastOutcome,
				"successes": saved.Successes, "failures": saved.Failures,
				"last_verified_at": saved.LastVerifiedAt,
			})
		})
}
