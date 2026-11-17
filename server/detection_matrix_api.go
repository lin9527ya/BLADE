package server

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// 检测矩阵：资产 × ATT&CK 技术，五态分开计数，**不合成检出率**。
// triggered / not_triggered / unknown 来自 report_detection_check 的实测；
// unmeasured = 有意图锚定该资产但没有任何检测记录；not_attempted = 两者都没有。
// unknown 必须独立呈现：把它并进"未检出"会给出一个比不给更危险的结论。

const (
	matrixTriggered    = "triggered"
	matrixNotTriggered = "not_triggered"
	matrixUnknown      = "unknown"
	matrixUnmeasured   = "unmeasured"
	matrixNotAttempted = "not_attempted"
)

type matrixAcc struct {
	// assetID -> technique -> status
	cells map[int64]map[string]string
}

func newMatrixAcc() *matrixAcc {
	return &matrixAcc{cells: map[int64]map[string]string{}}
}

func (m *matrixAcc) set(assetID int64, technique, status string) {
	if technique == "" {
		return
	}
	if m.cells[assetID] == nil {
		m.cells[assetID] = map[string]string{}
	}
	// 实测结果优先于 unmeasured/not_attempted；同格已有实测就不再覆盖。
	if cur := m.cells[assetID][technique]; cur == matrixTriggered || cur == matrixNotTriggered || cur == matrixUnknown {
		return
	}
	m.cells[assetID][technique] = status
}

func (m *matrixAcc) accumulateTask(t *Task) error {
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
			continue
		}
		for _, assetID := range anchors[n.ID] {
			for _, tech := range techs {
				m.set(assetID, tech, matrixUnmeasured)
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
		status := matrixUnknown
		switch n.State {
		case db.StateDetectionTriggered:
			status = matrixTriggered
		case db.StateDetectionNotTriggered:
			status = matrixNotTriggered
		case db.StateDetectionUnknown:
			status = matrixUnknown
		}
		for _, assetID := range anchors[n.ID] {
			for _, tech := range techs {
				m.set(assetID, tech, status)
			}
		}
	}
	return nil
}

func (s *Server) detectionMatrix(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	taskQ := strings.TrimSpace(r.URL.Query().Get("task"))
	campaignQ := strings.TrimSpace(r.URL.Query().Get("campaign"))
	acc := newMatrixAcc()
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
	for _, t := range tasks {
		if err := acc.accumulateTask(t); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}

	techSet := map[string]bool{}
	assetSet := map[int64]bool{}
	for assetID, cells := range acc.cells {
		assetSet[assetID] = true
		for tech := range cells {
			techSet[tech] = true
		}
	}
	assetIDs := make([]int64, 0, len(assetSet))
	for id := range assetSet {
		assetIDs = append(assetIDs, id)
	}
	sort.Slice(assetIDs, func(i, j int) bool { return assetIDs[i] < assetIDs[j] })
	labels, err := s.m.pg.AssetLabels(assetIDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	techniques := make([]string, 0, len(techSet))
	for tech := range techSet {
		techniques = append(techniques, tech)
	}
	sort.Strings(techniques)

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	rows := make([]map[string]any, 0, len(assetIDs))
	counts := map[string]int{
		matrixTriggered: 0, matrixNotTriggered: 0, matrixUnknown: 0,
		matrixUnmeasured: 0, matrixNotAttempted: 0,
	}
	for _, assetID := range assetIDs {
		cells := map[string]string{}
		label := labels[assetID]
		if label == "" {
			label = fmt.Sprintf("#%d", assetID)
		}
		for _, tech := range techniques {
			status := acc.cells[assetID][tech]
			if status == "" {
				status = matrixNotAttempted
			}
			cells[tech] = status
			counts[status]++
		}
		rows = append(rows, map[string]any{"asset_id": assetID, "label": label, "cells": cells})
	}
	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="detection-matrix.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write(append([]string{"asset_id", "label"}, techniques...))
		for _, row := range rows {
			assetID, _ := row["asset_id"].(int64)
			label, _ := row["label"].(string)
			cells, _ := row["cells"].(map[string]string)
			line := []string{fmt.Sprint(assetID), label}
			for _, tech := range techniques {
				line = append(line, cells[tech])
			}
			_ = cw.Write(line)
		}
		cw.Flush()
		return
	}
	writeJSON(w, 200, map[string]any{
		"techniques": techniques, "rows": rows, "counts": counts,
		"note": "unknown 表示无法判定，**不是未检出**；unmeasured 表示有意图但没有检测记录；本页不计算检出率。",
	})
}
