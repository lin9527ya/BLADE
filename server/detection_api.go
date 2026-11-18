package server

import (
	"net/http"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// 检测验证的人工录入：与 agent 的 report_detection_check 共用 db.ValidateDetectionResult，
// 落同一类 detection 节点与三态 state。origin='human'，不自动改任何漏洞结论。
func (s *Server) recordDetection(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	t := s.taskFromPath(w, r)
	if t == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Summary    string  `json:"summary"`
		Technique  string  `json:"technique"`
		Result     string  `json:"result"`
		Detail     string  `json:"detail"`
		Evidence   string  `json:"evidence"`
		Confidence string  `json:"confidence"`
		Detector   string  `json:"detector"`
		LogSource  string  `json:"log_source"`
		TimeWindow string  `json:"time_window"`
		AssetIDs   []int64 `json:"asset_ids"`
		IntentID   int64   `json:"intent_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		writeErr(w, 400, "summary is required")
		return
	}
	if len([]rune(summary)) > 2000 {
		writeErr(w, 400, "summary 最多 2000 个字符")
		return
	}
	tech, err := db.ValidateDetectionResult(req.Result, req.Technique, req.Evidence, req.Confidence)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(req.AssetIDs) > 100 {
		writeErr(w, 400, "asset_ids 最多 100 个")
		return
	}
	assets := make([]int64, 0, len(req.AssetIDs))
	for _, id := range req.AssetIDs {
		if id > 0 {
			assets = append(assets, id)
		}
	}
	if req.IntentID > 0 {
		node, err := t.Store.GetNode(req.IntentID)
		if err != nil || node == nil || node.Kind != db.KindIntent {
			writeErr(w, 400, "intent_id 必须是本任务的意图")
			return
		}
	}
	payload := map[string]any{"summary": summary, "technique": tech}
	for k, v := range map[string]string{
		"detail": req.Detail, "evidence": req.Evidence, "confidence": req.Confidence,
		"detector": req.Detector, "log_source": req.LogSource, "time_window": req.TimeWindow,
	} {
		if s := strings.TrimSpace(v); s != "" {
			payload[k] = s
		}
	}
	id, err := t.Store.AddLinkedNode(db.KindDetection, payload, 6, strings.TrimSpace(req.Result), "human", assets, req.IntentID, db.RelYields)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "detection_id": id})
}
