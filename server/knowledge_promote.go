package server

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// promoteTaskKnowledge 在任务收尾/暂停时把已确认的发现与关键否定结论沉淀到公司知识，
// 让同公司后续任务在态势里看到「这个站点/这套栈以前测过什么」。best-effort：
// 失败只记日志，不影响收尾；按 source_task_id + source_node_id 幂等，重复调用不会重复写。
func (e *Engine) promoteTaskKnowledge(t *Task) {
	if e == nil || t == nil || t.Store == nil {
		return
	}
	as := e.m.Assets()
	if as == nil {
		return
	}
	taskID := parseIntDefault(t.ID)
	if taskID <= 0 {
		return
	}
	scope, err := as.ListTaskScope(taskID)
	if err != nil {
		return
	}
	seenCompany := map[int64]bool{}
	var companyIDs []int64
	for _, r := range scope {
		if r.Kind != "company" || r.CompanyID == nil || *r.CompanyID <= 0 || seenCompany[*r.CompanyID] {
			continue
		}
		seenCompany[*r.CompanyID] = true
		companyIDs = append(companyIDs, *r.CompanyID)
	}
	if len(companyIDs) == 0 {
		return
	}
	// 已有沉淀（含已废弃）按来源节点去重：废弃过的结论不该因为一次收尾又被写回来。
	existing := map[string]bool{}
	for _, cid := range companyIDs {
		rows, err := as.ListCompanyKnowledgeAll(cid, 1000)
		if err != nil {
			continue
		}
		for _, r := range rows {
			existing[fmt.Sprintf("%s#%d", r.SourceTaskID, r.SourceNodeID)] = true
		}
	}
	promoted := 0
	promote := func(kind, summary, detail, confidence string, nodeID int64) {
		if promoted >= 10 || strings.TrimSpace(summary) == "" {
			return
		}
		key := fmt.Sprintf("%s#%d", t.ID, nodeID)
		if existing[key] {
			return
		}
		for _, cid := range companyIDs {
			if _, err := as.CreateCompanyKnowledge(db.CompanyKnowledge{
				CompanyID: cid, Kind: kind,
				Summary: clipRunes(summary, 300), Detail: clipRunes(detail, 1000),
				SourceTaskID: t.ID, SourceNodeID: nodeID, Confidence: confidence,
			}); err != nil {
				log.Printf("[knowledge] task %s 沉淀节点 %d 到公司 %d 失败: %v", t.ID, nodeID, cid, err)
				continue
			}
			existing[key] = true
			promoted++
		}
	}
	// 发现：confirmed 记事实；ruled_out 记否定结论（带反证/理由）。
	if findings, err := t.Store.ListByKind(db.KindFinding, 500); err == nil {
		for _, n := range findings {
			if promoted >= 10 {
				break
			}
			if n.State != "confirmed" && n.State != "ruled_out" {
				continue
			}
			var p map[string]any
			_ = json.Unmarshal(n.Payload, &p)
			summary := knowledgeText(p["summary"])
			detail := knowledgeText(p["detail"])
			conf := knowledgeText(p["confidence"])
			if conf == "" {
				conf = "observed"
			}
			kind := db.KnowledgeKindFact
			if n.State == "ruled_out" {
				kind = db.KnowledgeKindConclusion
				summary = "已否定：" + summary
				if ce := knowledgeText(p["counterevidence"]); ce != "" {
					detail = ce
				}
			} else if sev := knowledgeText(p["severity"]); sev != "" {
				detail = strings.TrimSpace(detail + "（severity=" + sev + "）")
			}
			promote(kind, summary, detail, conf, n.ID)
		}
	}
	// 否定事实：只沉淀 observed 的（推断的否定会把后来人带偏）。
	if facts, err := t.Store.ListByKind(db.KindFact, 500); err == nil {
		for _, n := range facts {
			if promoted >= 10 {
				break
			}
			var p map[string]any
			_ = json.Unmarshal(n.Payload, &p)
			if knowledgeText(p["polarity"]) != "negative" || knowledgeText(p["confidence"]) != "observed" {
				continue
			}
			promote(db.KnowledgeKindConclusion, "已否定："+knowledgeText(p["summary"]), knowledgeText(p["detail"]), "observed", n.ID)
		}
	}
}

func knowledgeText(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}
