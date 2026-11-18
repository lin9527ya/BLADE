package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/opsec"
)

// ATT&CK 覆盖：只统计**已标注技术**的意图与检测节点，不计算覆盖率百分比。
// 「未标注」不代表没测；本页不把三者混成一个分数。

type attackCoverageRow struct {
	Technique    string           `json:"technique"`
	Intents      int              `json:"intents"`
	IntentStates map[string]int   `json:"intent_states,omitempty"`
	Assets       []int64          `json:"assets,omitempty"`
	Detections   map[string]int   `json:"detections,omitempty"`
	LastAt       *time.Time       `json:"last_at,omitempty"`
	OpsecNotes   []map[string]any `json:"opsec_notes,omitempty"`
}

func coverageRow(m map[string]*attackCoverageRow, tech string) *attackCoverageRow {
	if m[tech] == nil {
		m[tech] = &attackCoverageRow{Technique: tech, IntentStates: map[string]int{}, Detections: map[string]int{}}
	}
	return m[tech]
}

func (s *Server) attackCoverage(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	taskQ := strings.TrimSpace(r.URL.Query().Get("task"))
	campaignQ := strings.TrimSpace(r.URL.Query().Get("campaign"))
	var tasks []*Task
	switch {
	case taskQ != "":
		t := s.m.ResolveTask(taskQ)
		if t == nil {
			writeErr(w, 404, "任务不存在或未加载")
			return
		}
		tasks = append(tasks, t)
	case campaignQ != "":
		id, err := strconv.ParseInt(campaignQ, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, 400, "campaign id 非法")
			return
		}
		ids, err := s.m.pg.CampaignTaskIDs(id)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		for _, tid := range ids {
			if t := s.m.ResolveTask(fmt.Sprint(tid)); t != nil {
				tasks = append(tasks, t)
			}
		}
	default:
		writeErr(w, 400, "需要 task 或 campaign 参数")
		return
	}

	rows := map[string]*attackCoverageRow{}
	unlabeled := 0
	assetSeen := map[string]map[int64]bool{}
	for _, t := range tasks {
		if err := accumulateCoverage(t, rows, assetSeen, &unlabeled); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	out := make([]*attackCoverageRow, 0, len(rows))
	for _, row := range rows {
		for assetID := range assetSeen[row.Technique] {
			row.Assets = append(row.Assets, assetID)
		}
		sort.Slice(row.Assets, func(i, j int) bool { return row.Assets[i] < row.Assets[j] })
		if len(row.Assets) > 50 {
			row.Assets = row.Assets[:50]
		}
		for _, note := range opsec.ByTechnique(row.Technique) {
			row.OpsecNotes = append(row.OpsecNotes, map[string]any{
				"id": note.ID, "title": note.Title, "exposure": note.Exposure,
			})
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Technique < out[j].Technique })
	writeJSON(w, 200, map[string]any{
		"techniques": out, "unlabeled_intents": unlabeled,
		"note": "只统计已标注 ATT&CK 技术的意图与检测记录；未标注不代表没测，本页不计算覆盖率百分比。",
	})
}

func accumulateCoverage(t *Task, rows map[string]*attackCoverageRow, assetSeen map[string]map[int64]bool, unlabeled *int) error {
	intents, err := t.Store.ListByKind(db.KindIntent, 100000)
	if err != nil {
		return err
	}
	detections, err := t.Store.ListByKind(db.KindDetection, 100000)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(intents)+len(detections))
	for _, n := range intents {
		ids = append(ids, n.ID)
	}
	for _, n := range detections {
		ids = append(ids, n.ID)
	}
	anchors, err := t.Store.NodeAssets(ids)
	if err != nil {
		return err
	}
	for _, n := range intents {
		techs := db.NodeTechniques(n)
		if len(techs) == 0 {
			*unlabeled++
			continue
		}
		for _, tech := range techs {
			row := coverageRow(rows, tech)
			row.Intents++
			row.IntentStates[n.State]++
			if !n.CreatedAt.IsZero() && (row.LastAt == nil || n.CreatedAt.After(*row.LastAt)) {
				at := n.CreatedAt
				row.LastAt = &at
			}
			if assetSeen[tech] == nil {
				assetSeen[tech] = map[int64]bool{}
			}
			for _, assetID := range anchors[n.ID] {
				assetSeen[tech][assetID] = true
			}
		}
	}
	for _, n := range detections {
		var p struct {
			Technique  string   `json:"technique"`
			Techniques []string `json:"techniques"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		techs := append([]string{}, p.Techniques...)
		if p.Technique != "" {
			techs = append(techs, p.Technique)
		}
		for _, tech := range techs {
			if tech == "" {
				continue
			}
			row := coverageRow(rows, tech)
			row.Detections[n.State]++
			if !n.CreatedAt.IsZero() && (row.LastAt == nil || n.CreatedAt.After(*row.LastAt)) {
				at := n.CreatedAt
				row.LastAt = &at
			}
			if assetSeen[tech] == nil {
				assetSeen[tech] = map[int64]bool{}
			}
			for _, assetID := range anchors[n.ID] {
				assetSeen[tech][assetID] = true
			}
		}
	}
	return nil
}
