package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 凭据工作台 / 据点与隧道面板（P0 执行面）。
//
// 这些接口**不直接执行目标动作**：验证/存活检查只生成一条 origin='human' 的高优先级
// 意图，真正的 loot_exec/foothold_exec 仍由 worker 在 guard（RoE/时间窗/停机/规则/
// 裁判/审批）之下发起。标记失效与记录复核只写平台台账。

func (s *Server) listCredentialsWorkbench(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	q := r.URL.Query()
	f := db.CredentialWorkbenchFilter{
		TaskID:     parseIntDefault(q.Get("task")),
		CompanyID:  parseIntDefault(q.Get("company")),
		CampaignID: parseIntDefault(q.Get("campaign")),
		State:      strings.TrimSpace(q.Get("state")),
		Host:       strings.TrimSpace(q.Get("host")),
		Service:    strings.TrimSpace(q.Get("service")),
		Lifecycle:  strings.TrimSpace(q.Get("lifecycle")),
		Query:      strings.TrimSpace(q.Get("q")),
		Page:       int(parseIntDefault(q.Get("page"))),
		Size:       int(parseIntDefault(q.Get("size"))),
	}
	switch strings.ToLower(strings.TrimSpace(q.Get("verified"))) {
	case "true", "1":
		v := true
		f.Verified = &v
	case "false", "0":
		v := false
		f.Verified = &v
	}
	items, total, err := pg.ListCredentialWorkbench(f, s.credentialPlaintextEnabled())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size <= 0 {
		f.Size = 50
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": f.Page, "size": f.Size})
}

func (s *Server) listCredentialChecks(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad credential id")
		return
	}
	_, kind, err := pg.NodeLocation(id)
	if err != nil || kind != db.KindCredential {
		writeErr(w, 404, "credential not found")
		return
	}
	limit := int(parseIntDefault(r.URL.Query().Get("limit")))
	items, err := pg.ListCredentialChecks(id, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) recordCredentialCheck(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad credential id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Host       string `json:"host"`
		Port       int    `json:"port"`
		Service    string `json:"service"`
		Protocol   string `json:"protocol"`
		Result     string `json:"result"`
		Evidence   string `json:"evidence"`
		Note       string `json:"note"`
		ObservedAt string `json:"observed_at"`
		ValidUntil string `json:"valid_until"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	result := strings.ToLower(strings.TrimSpace(req.Result))
	if !db.ValidCredentialCheckResult(result) {
		writeErr(w, 400, "result 必须是 "+strings.Join(db.CredentialCheckResults, "/"))
		return
	}
	if strings.TrimSpace(req.Host) == "" {
		writeErr(w, 400, "host is required")
		return
	}
	if result != "unknown" && strings.TrimSpace(req.Evidence) == "" {
		writeErr(w, 400, "result="+result+" 时必须给 evidence")
		return
	}
	node, task, err := s.nodeTask(pg, id, db.KindCredential)
	if err != nil {
		writeErr(w, 404, "credential not found")
		return
	}
	if err := task.Store.ValidateEvidenceMetadata(&db.EvidenceMetadata{ObservedAt: req.ObservedAt, ValidUntil: req.ValidUntil}); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(node.Payload, &payload); err != nil || payload == nil {
		payload = map[string]any{}
	}
	cur := db.ParseCredential(node)
	verified := result == "verified"
	merged := db.MergeCredentialTarget(cur.Targets, db.CredentialTarget{
		Host: strings.TrimSpace(req.Host), Port: req.Port, Service: req.Service, Protocol: req.Protocol,
		ObservedAt: req.ObservedAt, ValidUntil: req.ValidUntil, ValidationStatus: result,
		Verified: verified, Evidence: strings.TrimSpace(req.Evidence), Note: req.Note,
	})
	if !verified {
		for i := range merged {
			if strings.EqualFold(strings.TrimSpace(merged[i].Host), strings.TrimSpace(req.Host)) &&
				merged[i].Port == req.Port &&
				strings.EqualFold(strings.TrimSpace(merged[i].Service), strings.TrimSpace(req.Service)) {
				merged[i].ValidationStatus = result
				if req.Note != "" {
					merged[i].Note = req.Note
				}
			}
		}
	}
	db.SortCredentialTargets(merged)
	payload["targets"] = merged
	if err := task.Store.SetNodePayload(id, db.KindCredential, payload); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	checkID, err := pg.InsertCredentialCheck(task.Store.ID(), id, req.Host, req.Port, req.Service, result,
		req.Evidence, req.Note, req.ObservedAt, req.ValidUntil, "human", "")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "check_id": checkID, "lifecycle": db.ComputeCredentialLifecycle(db.CredentialLifecycleInput{
		State: node.State, Targets: merged, LastResult: result, Now: time.Now(),
	})})
}

func (s *Server) markCredentialLost(w http.ResponseWriter, r *http.Request) {
	s.markNodeLost(w, r, db.KindCredential)
}

func (s *Server) markFootholdLost(w http.ResponseWriter, r *http.Request) {
	s.markNodeLost(w, r, db.KindFoothold)
}

func (s *Server) markNodeLost(w http.ResponseWriter, r *http.Request, wantKind string) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var req struct {
		Reason   string `json:"reason"`
		Evidence string `json:"evidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	reason := strings.TrimSpace(req.Reason)
	evidence := strings.TrimSpace(req.Evidence)
	if reason == "" || evidence == "" {
		writeErr(w, 400, "reason 与 evidence 都必填")
		return
	}
	_, task, err := s.nodeTask(pg, id, wantKind)
	if err != nil {
		writeErr(w, 404, "node not found")
		return
	}
	if err := task.Store.SetNodeState(id, db.StateLootLost); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := task.Store.AppendActivity(db.Activity{
		NodeID: &id, Kind: "mark_loot_lost", Summary: "人工标记失效：" + reason, Detail: evidence,
	}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// batchMarkCredentialsLost applies the same human decision to up to 200 credentials.
// Each node still gets its own activity entry; partial failures are returned per id.
func (s *Server) batchMarkCredentialsLost(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		IDs      []int64 `json:"ids"`
		Reason   string  `json:"reason"`
		Evidence string  `json:"evidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	reason := strings.TrimSpace(req.Reason)
	evidence := strings.TrimSpace(req.Evidence)
	if len(req.IDs) == 0 || len(req.IDs) > 200 {
		writeErr(w, 400, "ids 必须包含 1–200 条")
		return
	}
	if reason == "" || evidence == "" {
		writeErr(w, 400, "reason 与 evidence 都必填")
		return
	}
	type errEntry struct {
		ID    int64  `json:"id"`
		Error string `json:"error"`
	}
	updated := 0
	errs := []errEntry{}
	for _, id := range req.IDs {
		_, task, err := s.nodeTask(pg, id, db.KindCredential)
		if err != nil {
			errs = append(errs, errEntry{ID: id, Error: "凭据不存在"})
			continue
		}
		if err := task.Store.SetNodeState(id, db.StateLootLost); err != nil {
			errs = append(errs, errEntry{ID: id, Error: err.Error()})
			continue
		}
		if _, err := task.Store.AppendActivity(db.Activity{
			NodeID: &id, Kind: "mark_loot_lost", Summary: "批量标记失效：" + reason, Detail: evidence,
		}); err != nil {
			errs = append(errs, errEntry{ID: id, Error: err.Error()})
			continue
		}
		updated++
	}
	writeJSON(w, 200, map[string]any{"updated": updated, "errors": errs})
}

func (s *Server) credentialVerifyIntent(w http.ResponseWriter, r *http.Request) {
	s.nodeFollowUpIntent(w, r, db.KindCredential, "验证凭据")
}

func (s *Server) footholdCheckIntent(w http.ResponseWriter, r *http.Request) {
	s.nodeFollowUpIntent(w, r, db.KindFoothold, "据点存活检查")
}

// nodeFollowUpIntent 生成一条 human 意图；真正执行仍由 worker 在 guard 下发起。
func (s *Server) nodeFollowUpIntent(w http.ResponseWriter, r *http.Request, wantKind, action string) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var req struct {
		Description string `json:"description"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	node, task, err := s.nodeTask(pg, id, wantKind)
	if err != nil {
		writeErr(w, 404, "node not found")
		return
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		switch wantKind {
		case db.KindCredential:
			cv := db.ParseCredential(node)
			description = fmt.Sprintf("验证凭据 #%d（%s，指纹 %s）能否登录：按 targets 用 loot_exec/foothold_exec 逐条实测；"+
				"成功用 verify_credential，失败用 record_credential_check 记录原因。禁止爆破/口令喷洒。",
				id, cv.Username, cv.Fingerprint())
		default:
			description = fmt.Sprintf("检查据点 #%d 是否仍可用：用 foothold_exec 执行只读命令（id; hostname），"+
				"成功 record_fact，失败用 mark_loot_lost 并附证据。", id)
		}
	}
	anchors, err := pg.NodeAnchorIDs(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	intentID, err := task.Store.AddIntentGraph(map[string]any{
		"summary": description, "source_node_id": id, "source_node_kind": wantKind,
	}, 10, anchors, "human", []db.IntentEdge{{From: id, Rel: db.RelDerivedFrom, To: 0}})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	queued, err := s.admitTask(task, "resume")
	if err != nil {
		_ = task.Store.DiscardOpenIntent(intentID)
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "intent_id": intentID, "queued": queued, "action": action, "description": description})
}

func (s *Server) listFootholdsWorkbench(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	q := r.URL.Query()
	f := db.FootholdWorkbenchFilter{
		TaskID:     parseIntDefault(q.Get("task")),
		CompanyID:  parseIntDefault(q.Get("company")),
		CampaignID: parseIntDefault(q.Get("campaign")),
		State:      strings.TrimSpace(q.Get("state")),
		Host:       strings.TrimSpace(q.Get("host")),
		Query:      strings.TrimSpace(q.Get("q")),
		Page:       int(parseIntDefault(q.Get("page"))),
		Size:       int(parseIntDefault(q.Get("size"))),
	}
	items, total, err := pg.ListFootholdWorkbench(f)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size <= 0 {
		f.Size = 50
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": f.Page, "size": f.Size})
}

func (s *Server) listTunnels(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	items, err := pg.ListTunnels(int(parseIntDefault(r.URL.Query().Get("limit"))))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, it := range items {
		if !db.ExecutionActive(it.Status) || it.LocalPort <= 0 {
			continue
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", it.LocalPort), 300*time.Millisecond)
		if err == nil {
			it.Listening = true
			_ = conn.Close()
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) cancelExecution(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	eid, err := strconv.ParseInt(r.PathValue("eid"), 10, 64)
	if err != nil || eid <= 0 {
		writeErr(w, 400, "bad execution id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		writeErr(w, 400, "reason is required")
		return
	}
	row, err := pg.GetExecution(eid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if row == nil {
		writeErr(w, 404, "执行记录不存在")
		return
	}
	if !db.ExecutionActive(row.Status) {
		writeErr(w, 409, "执行已是终态，不能取消")
		return
	}
	ok, err := s.m.Executions().Cancel(r.Context(), eid, reason)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": ok})
}

// nodeTask 校验节点存在且 kind 匹配，并取到所属任务的运行态。
func (s *Server) nodeTask(pg *db.DB, nodeID int64, wantKind string) (*db.Node, *Task, error) {
	expID, kind, err := pg.NodeLocation(nodeID)
	if err != nil {
		if errors.Is(err, db.ErrNodeNotFound) {
			return nil, nil, sql.ErrNoRows
		}
		return nil, nil, err
	}
	if kind != wantKind {
		return nil, nil, sql.ErrNoRows
	}
	node, err := pg.Exploration(expID).GetNode(nodeID)
	if err != nil || node == nil {
		return nil, nil, sql.ErrNoRows
	}
	taskID, err := pg.TaskForNode(nodeID)
	if err != nil {
		return nil, nil, err
	}
	task, ok := s.m.Task(strconv.FormatInt(taskID, 10))
	if !ok || task == nil {
		return nil, nil, sql.ErrNoRows
	}
	return node, task, nil
}
