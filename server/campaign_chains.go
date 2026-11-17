package server

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/report"
)

// 跨任务攻击链：逐任务复用 buildAttackChains，**不合成跨 exploration 的边**。
// 黑板按探索隔离，伪造边会破坏审计与级联语义；跨任务只展示共享资产作为连接线索。

func (s *Server) campaignAttackChains(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	c, err := s.m.pg.GetCampaign(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if c == nil {
		writeErr(w, 404, "战役不存在")
		return
	}
	ids, err := s.m.pg.CampaignTaskIDs(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	tasks := make([]map[string]any, 0, len(ids))
	missing := []map[string]any{}
	for _, tid := range ids {
		t := s.m.ResolveTask(fmt.Sprint(tid))
		if t == nil {
			missing = append(missing, map[string]any{"task_id": tid, "reason": "任务未加载（可能已归档或删除）"})
			continue
		}
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = t.Description
		}
		chains, cerr := attackChainsForTask(t)
		row := map[string]any{"task_id": tid, "name": name}
		if cerr != nil {
			row["error"] = cerr.Error()
			chains = nil
		}
		row["chains"] = chains
		tasks = append(tasks, row)
	}
	shared, err := s.sharedCampaignAssets(ids)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "csv") {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="campaign-attack-chains.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"task_id", "task_name", "chain", "step", "intent_id", "summary", "techniques", "produced", "error"})
		for _, row := range tasks {
			taskID := fmt.Sprint(row["task_id"])
			name, _ := row["name"].(string)
			if errText, ok := row["error"].(string); ok && errText != "" {
				_ = cw.Write([]string{taskID, name, "", "", "", "", "", "", errText})
				continue
			}
			chains, _ := row["chains"].([]report.AttackChain)
			for ci, chain := range chains {
				if len(chain.Steps) == 0 {
					_ = cw.Write([]string{taskID, name, fmt.Sprint(ci + 1), "", "", chain.Head, "", "", ""})
					continue
				}
				for si, step := range chain.Steps {
					_ = cw.Write([]string{taskID, name, fmt.Sprint(ci + 1), fmt.Sprint(si + 1), fmt.Sprint(step.IntentID),
						step.Summary, strings.Join(step.Techniques, " "), strings.Join(step.Produced, " | "), ""})
				}
			}
		}
		for _, row := range missing {
			_ = cw.Write([]string{fmt.Sprint(row["task_id"]), "", "", "", "", "", "", "", fmt.Sprint(row["reason"])})
		}
		cw.Flush()
		return
	}
	writeJSON(w, 200, map[string]any{
		"campaign_id": id, "campaign": c, "tasks": tasks,
		"missing_tasks": missing, "shared_assets": shared,
	})
}

func attackChainsForTask(t *Task) ([]report.AttackChain, error) {
	intents, err := t.Store.ListByKind(db.KindIntent, 5000)
	if err != nil {
		return nil, err
	}
	footholds, _ := t.Store.ListByKind(db.KindFoothold, 2000)
	credentials, _ := t.Store.ListByKind(db.KindCredential, 2000)
	artifacts, _ := t.Store.ListByKind(db.KindArtifact, 2000)
	edges, err := t.Store.Edges(5000)
	if err != nil {
		return nil, err
	}
	intentState := make(map[int64]string, len(intents))
	intentSummary := make(map[int64]string, len(intents))
	intentTech := make(map[int64][]string, len(intents))
	for _, n := range intents {
		intentState[n.ID] = n.State
		var p struct {
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		intentSummary[n.ID] = p.Summary
		intentTech[n.ID] = db.NodeTechniques(n)
	}
	return buildAttackChains(edges, lootLabels(footholds, credentials, artifacts), intentSummary, intentState, intentTech), nil
}

// sharedCampaignAssets 列出被战役内 ≥2 个任务共同锚定的资产。
func (s *Server) sharedCampaignAssets(taskIDs []int64) ([]map[string]any, error) {
	if len(taskIDs) < 2 {
		return []map[string]any{}, nil
	}
	rows, err := s.m.pg.Query(`SELECT id, type, COALESCE(domain,''), COALESCE(url,''), COALESCE(ip,''), task_ids
FROM assets WHERE task_ids && $1::bigint[] ORDER BY id LIMIT 500`, taskIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := map[int64]bool{}
	for _, id := range taskIDs {
		want[id] = true
	}
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var typ, domain, url, ip string
		var ids []int64
		if err := rows.Scan(&id, &typ, &domain, &url, &ip, &ids); err != nil {
			return nil, err
		}
		shared := []int64{}
		for _, tid := range ids {
			if want[tid] {
				shared = append(shared, tid)
			}
		}
		if len(shared) < 2 {
			continue
		}
		sort.Slice(shared, func(i, j int) bool { return shared[i] < shared[j] })
		label := domain
		if label == "" {
			label = url
		}
		if label == "" {
			label = ip
		}
		out = append(out, map[string]any{
			"asset_id": id, "type": typ, "label": label, "task_ids": shared,
		})
		if len(out) >= 50 {
			break
		}
	}
	return out, rows.Err()
}
