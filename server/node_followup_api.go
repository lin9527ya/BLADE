package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

// 通用「节点 → 人工意图」入口与 AD 路径标记。
// UI 只生成意图或写节点属性，真正执行仍由 worker 在 guard 链下发起。

var followUpKinds = map[string]bool{
	db.KindFinding: true, db.KindCredential: true, db.KindFoothold: true,
	db.KindADObject: true, db.KindCloudObject: true, db.KindDetection: true,
}

func (s *Server) taskFollowUpIntent(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	t := s.taskFromPath(w, r)
	if t == nil {
		return
	}
	nid, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("nid")), 10, 64)
	if err != nil || nid <= 0 {
		writeErr(w, 400, "节点 id 非法")
		return
	}
	var req struct {
		Description string `json:"description"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	description := strings.TrimSpace(req.Description)
	if description == "" {
		writeErr(w, 400, "description is required")
		return
	}
	if len([]rune(description)) > 2000 {
		writeErr(w, 400, "description 最多 2000 个字符")
		return
	}
	node, err := t.Store.GetNode(nid)
	if err != nil || node == nil {
		writeErr(w, 404, "节点不存在或不属于该任务")
		return
	}
	if !followUpKinds[node.Kind] {
		writeErr(w, 400, "该节点类型不支持生成意图："+node.Kind)
		return
	}
	s.writeFollowUpIntent(w, r, t, node, description)
}

// writeFollowUpIntent 创建（或复用）一条 human 意图并唤醒任务。
func (s *Server) writeFollowUpIntent(w http.ResponseWriter, r *http.Request, t *Task, node *db.Node, description string) {
	if existing, err := t.Store.FindOpenFollowUpIntent(node.ID, description); err == nil && existing > 0 {
		writeJSON(w, 200, map[string]any{"ok": true, "intent_id": existing, "queued": false, "reused": true, "description": description})
		return
	}
	anchors, err := s.m.pg.NodeAnchorIDs(node.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	intentID, err := t.Store.AddIntentGraph(map[string]any{
		"summary": description, "source_node_id": node.ID, "source_node_kind": node.Kind,
	}, 10, anchors, "human", []db.IntentEdge{{From: node.ID, Rel: db.RelDerivedFrom, To: 0}})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	queued, err := s.admitTask(t, "resume")
	if err != nil {
		_ = t.Store.DiscardOpenIntent(intentID)
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "intent_id": intentID, "queued": queued, "reused": false, "description": description})
}

// setADObjectOwned 标记一个 AD 对象已被拿下；可关联一条战果写 reaches 边。
// 不会自动执行任何目标动作，也不会因为路径计算而自动标记。
func (s *Server) setADObjectOwned(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	t := s.taskFromPath(w, r)
	if t == nil {
		return
	}
	oid, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("oid")), 10, 64)
	if err != nil || oid <= 0 {
		writeErr(w, 400, "AD 对象 id 非法")
		return
	}
	var req struct {
		Owned        bool   `json:"owned"`
		Evidence     string `json:"evidence"`
		SourceLootID int64  `json:"source_loot_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.Owned && strings.TrimSpace(req.Evidence) == "" {
		writeErr(w, 400, "标记 owned 时必须给 evidence")
		return
	}
	node, err := t.Store.GetNode(oid)
	if err != nil || node == nil || node.Kind != db.KindADObject {
		writeErr(w, 404, "AD 对象不存在或不属于该任务")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(node.Payload, &payload); err != nil || payload == nil {
		payload = map[string]any{}
	}
	if req.Owned {
		payload["owned"] = true
		payload["owned_evidence"] = strings.TrimSpace(req.Evidence)
		payload["owned_at"] = time.Now().UTC().Format(time.RFC3339)
		payload["owned_by"] = "human"
	} else {
		delete(payload, "owned")
		delete(payload, "owned_evidence")
		delete(payload, "owned_at")
		delete(payload, "owned_by")
	}
	if err := t.Store.SetNodePayload(oid, db.KindADObject, payload); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	linked := false
	if req.SourceLootID > 0 {
		loot, err := t.Store.GetNode(req.SourceLootID)
		if err != nil || loot == nil || (loot.Kind != db.KindFoothold && loot.Kind != db.KindCredential) {
			writeErr(w, 400, "source_loot_id 必须是本任务里的据点或凭据")
			return
		}
		if err := t.Store.Link(req.SourceLootID, db.RelReaches, oid); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		linked = true
	}
	writeJSON(w, 200, map[string]any{"ok": true, "owned": req.Owned, "linked": linked})
}

func (s *Server) pursueADObject(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	t := s.taskFromPath(w, r)
	if t == nil {
		return
	}
	oid, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("oid")), 10, 64)
	if err != nil || oid <= 0 {
		writeErr(w, 400, "AD 对象 id 非法")
		return
	}
	node, err := t.Store.GetNode(oid)
	if err != nil || node == nil || node.Kind != db.KindADObject {
		writeErr(w, 404, "AD 对象不存在或不属于该任务")
		return
	}
	var req struct {
		Description string `json:"description"`
	}
	_ = decode(r, &req)
	description := strings.TrimSpace(req.Description)
	if description == "" {
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(node.Payload, &p)
		label := p.Name
		if label == "" {
			label = fmt.Sprintf("#%d", oid)
		}
		description = fmt.Sprintf("从 AD 对象 %s 推进下一步：基于当前已持有凭据/会话，只读确认可达性，再选择最小动作；禁止爆破与持久化部署。", label)
	}
	s.writeFollowUpIntent(w, r, t, node, description)
}

// setIntentTechniques 人工补标 ATT&CK 技术（覆盖页的“采纳建议”入口）。
func (s *Server) setIntentTechniques(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	t := s.taskFromPath(w, r)
	if t == nil {
		return
	}
	nid, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("nid")), 10, 64)
	if err != nil || nid <= 0 {
		writeErr(w, 400, "节点 id 非法")
		return
	}
	var req struct {
		Techniques []string `json:"techniques"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	techs := db.NormalizeTechniques(req.Techniques)
	if len(techs) == 0 {
		writeErr(w, 400, "techniques 里没有合法的 ATT&CK 技术 ID（T#### 或 T####.###）")
		return
	}
	node, err := t.Store.GetNode(nid)
	if err != nil || node == nil || node.Kind != db.KindIntent {
		writeErr(w, 404, "意图不存在或不属于该任务")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(node.Payload, &payload); err != nil || payload == nil {
		payload = map[string]any{}
	}
	payload["techniques"] = techs
	if err := t.Store.SetNodePayload(nid, db.KindIntent, payload); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "techniques": techs})
}

// taskFromPath 解析 {id} 并取运行中的任务；失败时已写响应。
func (s *Server) taskFromPath(w http.ResponseWriter, r *http.Request) *Task {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeErr(w, 400, "task id is required")
		return nil
	}
	t := s.m.ResolveTask(id)
	if t == nil {
		writeErr(w, 404, "任务不存在或未加载")
		return nil
	}
	return t
}
