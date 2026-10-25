package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 遗留物的人工销账入口（任务总览「遗留物未清」卡片）。
//
// 与 agent 侧 mark_artifact_cleared / accept_artifact_risk 同一套规矩：
//   - 标「已清理」必须给清理时的那行输出——没有证据，"清掉了"只是个说法；
//   - 接受风险必须写理由，结论以 fact 落图并进报告语境。
// 这里只是把同一件事开放给人：agent 忘了收尾时，操作者不必改库。

const (
	artifactEvidenceMaxRunes = 4000
	artifactReasonMaxRunes   = 2000
)

type ArtifactDTO struct {
	ID              string `json:"id"`
	State           string `json:"state"`
	StateLabel      string `json:"state_label"`
	Kind            string `json:"kind,omitempty"`
	KindLabel       string `json:"kind_label,omitempty"`
	Summary         string `json:"summary"`
	Detail          string `json:"detail,omitempty"`
	Host            string `json:"host,omitempty"`
	Locator         string `json:"locator,omitempty"`
	URL             string `json:"url,omitempty"`
	Cleanup         string `json:"cleanup,omitempty"`
	Evidence        string `json:"evidence,omitempty"`
	Confidence      string `json:"confidence,omitempty"`
	RemovedEvidence string `json:"removed_evidence,omitempty"`
	RemovedNote     string `json:"removed_note,omitempty"`
	TS              string `json:"ts"`
}

func artifactDTO(n *db.Node) ArtifactDTO {
	a := db.ParseArtifact(n)
	return ArtifactDTO{
		ID:              i64s(n.ID),
		State:           n.State,
		StateLabel:      db.ArtifactStateLabel(n.State),
		Kind:            a.Kind,
		KindLabel:       db.ArtifactKindLabel(a.Kind),
		Summary:         a.Summary,
		Detail:          a.Detail,
		Host:            a.Host,
		Locator:         a.Locator,
		URL:             a.URL,
		Cleanup:         a.Cleanup,
		Evidence:        a.Evidence,
		Confidence:      a.Confidence,
		RemovedEvidence: a.RemovedEvidence,
		RemovedNote:     a.RemovedNote,
		TS:              rfc3339(n.CreatedAt),
	}
}

// taskArtifactRiskAccepted 报告本任务是否已经显式接受过遗留物风险（与 agent 的
// cleanupGate 读同一个 fact 标记；常量由 agent 导出，避免两处各写一份字面量）。
func taskArtifactRiskAccepted(t *Task) bool {
	facts, err := t.Store.ListByKind(db.KindFact, 500)
	if err != nil {
		return false
	}
	for _, n := range facts {
		var p map[string]any
		if json.Unmarshal(n.Payload, &p) != nil {
			continue
		}
		if accepted, _ := p[agent.ArtifactRiskAcceptedKey].(bool); accepted {
			return true
		}
	}
	return false
}

func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	nodes, err := t.Store.ListByKind(db.KindArtifact, 500)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]ArtifactDTO, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, artifactDTO(n))
	}
	writeJSON(w, 200, map[string]any{"artifacts": out, "risk_accepted": taskArtifactRiskAccepted(t)})
}

func (s *Server) clearArtifact(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法修改遗留物")
		return
	}
	defer s.engine.decInflight(t.ID)

	aid, err := strconv.ParseInt(r.PathValue("aid"), 10, 64)
	if err != nil || aid <= 0 {
		writeErr(w, 400, "bad artifact id")
		return
	}
	node, err := t.Store.GetNode(aid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindArtifact {
		writeErr(w, 404, "遗留物不存在")
		return
	}
	var body struct {
		Evidence string `json:"evidence"`
		Note     string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	evidence := strings.TrimSpace(body.Evidence)
	if evidence == "" {
		writeErr(w, 400, "清理证据必填：清理命令 + 关键输出——没有它，「清掉了」只是个说法")
		return
	}
	if utf8.RuneCountInString(evidence) > artifactEvidenceMaxRunes {
		writeErr(w, 400, "清理证据过长（最多 "+strconv.Itoa(artifactEvidenceMaxRunes)+" 个字符）")
		return
	}
	if utf8.RuneCountInString(body.Note) > artifactReasonMaxRunes {
		writeErr(w, 400, "备注过长（最多 "+strconv.Itoa(artifactReasonMaxRunes)+" 个字符）")
		return
	}
	changed, err := t.Store.MarkArtifactCleared(aid, evidence, body.Note)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !changed {
		writeErr(w, 409, "当前状态不可标记已清理（可能已经清理过）")
		return
	}
	updated, _ := t.Store.GetNode(aid)
	if updated == nil {
		writeErr(w, 500, "遗留物更新后读取失败")
		return
	}
	writeJSON(w, 200, artifactDTO(updated))
}

func (s *Server) acceptArtifactRisk(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法接受风险")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	reason := strings.TrimSpace(body.Reason)
	if reason == "" {
		writeErr(w, 400, "理由必填：接受遗留物风险是一条要进报告的结论")
		return
	}
	if utf8.RuneCountInString(reason) > artifactReasonMaxRunes {
		writeErr(w, 400, "理由过长（最多 "+strconv.Itoa(artifactReasonMaxRunes)+" 个字符）")
		return
	}
	id, err := t.Store.AddNode(db.KindFact, map[string]any{
		"summary":                     "接受遗留物风险：" + reason,
		agent.ArtifactRiskAcceptedKey: true,
		"confidence":                  "observed",
	}, 0, "confirmed", "human", nil)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "fact_id": i64s(id), "risk_accepted": true})
}
