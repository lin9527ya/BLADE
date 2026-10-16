package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// 战役 API：任务之上的一层（见 db/campaigns.go 与 schema.sql 的 campaigns 注释）。
//
// 聚合是这一层的主要价值：单个任务看"打穿了什么"，一次作业则要看"这轮总共拿到了什么"。
// 汇总不引入新存储——任务之间本来就各自独立（各自的探索图），所以这里逐个任务取再相加。
// 一次作业是几个到几十个任务，不为此造跨任务的物化视图。

// ---------- GET /api/campaigns ----------

func (s *Server) listCampaigns(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	campaigns, err := s.m.pg.ListCampaigns()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type row struct {
		*db.Campaign
		TaskCount int `json:"task_count"`
	}
	out := make([]row, 0, len(campaigns))
	for _, c := range campaigns {
		ids, err := s.m.pg.CampaignTaskIDs(c.ID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out = append(out, row{Campaign: c, TaskCount: len(ids)})
	}
	writeJSON(w, 200, map[string]any{"campaigns": out})
}

// ---------- POST /api/campaigns ----------

func (s *Server) createCampaign(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	var req struct {
		Name      string `json:"name"`
		Note      string `json:"note"`
		CompanyID *int64 `json:"company_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	c, err := s.m.pg.CreateCampaign(req.Name, req.Note, req.CompanyID)
	if err != nil {
		if errors.Is(err, db.ErrCampaignInvalid) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, c)
}

// ---------- GET /api/campaigns/{id} ----------

func (s *Server) getCampaign(w http.ResponseWriter, r *http.Request) {
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
	summary, err := s.campaignSummary(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, summary)
}

// campaignTaskBrief 是汇总里每个任务的一行。
type campaignTaskBrief struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Findings    int    `json:"findings"`
	Footholds   int    `json:"footholds"`
	Credentials int    `json:"credentials"`
	Detections  int    `json:"detections"`
}

// campaignSummary 汇总一次作业：它的任务、以及跨任务相加的产出。
//
// 技术覆盖与检测三态**复用全局那份判定**（db.TechniqueTally / 逐条读 state），
// 不在这里另写一套——否则战役视图与任务报告会报出两个不同的数字。
func (s *Server) campaignSummary(id int64) (map[string]any, error) {
	c, err := s.m.pg.GetCampaign(id)
	if err != nil || c == nil {
		return nil, err
	}
	ids, err := s.m.pg.CampaignTaskIDs(id)
	if err != nil {
		return nil, err
	}
	briefs := make([]campaignTaskBrief, 0, len(ids))
	techniques := map[string]int{}
	detectionResults := map[string]int{}
	totals := map[string]int{"tasks": 0, "findings": 0, "footholds": 0, "credentials": 0, "detections": 0}
	for _, tid := range ids {
		t := s.m.ResolveTask(strconv.FormatInt(tid, 10))
		if t == nil {
			continue
		}
		// 显示名与任务列表同口径：没起名字就退回描述。
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = t.Description
		}
		b := campaignTaskBrief{ID: tid, Name: name, Status: t.Status}
		b.Findings, _ = t.Store.CountByKind(db.KindFinding)
		b.Footholds, _ = t.Store.CountByKind(db.KindFoothold)
		b.Credentials, _ = t.Store.CountByKind(db.KindCredential)
		b.Detections, _ = t.Store.CountByKind(db.KindDetection)
		briefs = append(briefs, b)
		totals["tasks"]++
		totals["findings"] += b.Findings
		totals["footholds"] += b.Footholds
		totals["credentials"] += b.Credentials
		totals["detections"] += b.Detections

		// 技术覆盖：与任务报告同一条判定（软删除不计）。
		if intents, err := t.Store.ListByKind(db.KindIntent, 100000); err == nil {
			for k, v := range db.TechniqueTally(intents) {
				techniques[k] += v
			}
		}
		// 检测三态：分别计数，**不合成检出率**（同任务报告的口径）。
		if dets, err := t.Store.ListByKind(db.KindDetection, 100000); err == nil {
			for _, d := range dets {
				switch d.State {
				case db.StateDetectionTriggered, db.StateDetectionNotTriggered, db.StateDetectionUnknown:
					detectionResults[d.State]++
				}
			}
		}
	}
	out := map[string]any{"campaign": c, "tasks": briefs, "totals": totals}
	if len(techniques) > 0 {
		out["techniques"] = techniques
	}
	if len(detectionResults) > 0 {
		out["detection_results"] = detectionResults
	}
	return out, nil
}

// ---------- PATCH /api/campaigns/{id} ----------

func (s *Server) patchCampaign(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	// 先取现状：PATCH 是部分更新，未给的字段保持原值。
	cur, err := s.m.pg.GetCampaign(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if cur == nil {
		writeErr(w, 404, "战役不存在")
		return
	}
	var req struct {
		Name      *string `json:"name"`
		Note      *string `json:"note"`
		CompanyID *int64  `json:"company_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	name, note := cur.Name, cur.Note
	if req.Name != nil {
		name = *req.Name
	}
	if req.Note != nil {
		note = *req.Note
	}
	company := cur.CompanyID
	if req.CompanyID != nil {
		company = req.CompanyID
		if *req.CompanyID == 0 { // 0 = 明确清空
			company = nil
		}
	}
	c, err := s.m.pg.UpdateCampaign(id, name, note, company)
	if err != nil {
		if errors.Is(err, db.ErrCampaignInvalid) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, c)
}

// ---------- DELETE /api/campaigns/{id} ----------

func (s *Server) deleteCampaign(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	// 只解绑、不删任务（FK ON DELETE SET NULL）——任务是被归档与交付的单位。回执里如实说明
	// 解绑了几个，免得用户以为任务也一起没了。
	unbound, err := s.m.pg.DeleteCampaign(id)
	if err != nil {
		if errors.Is(err, db.ErrCampaignInvalid) {
			writeErr(w, 404, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": true, "tasks_unbound": unbound})
}

// ---------- PUT /api/campaigns/{id}/tasks ----------
//
// 声明式：给出这次作业**应当包含的全部任务**，多退少补。比"逐个加/逐个删"少一半路由，
// 也更难用错——调用方不必知道当前成员是谁。

func (s *Server) putCampaignTasks(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	id, ok := s.campaignID(w, r)
	if !ok {
		return
	}
	var req struct {
		TaskIDs []int64 `json:"task_ids"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
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
	want := map[int64]bool{}
	for _, tid := range req.TaskIDs {
		want[tid] = true
	}
	cur, err := s.m.pg.CampaignTaskIDs(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	have := map[int64]bool{}
	for _, tid := range cur {
		have[tid] = true
	}
	var added, removed []int64
	for tid := range want {
		if !have[tid] {
			tid := tid
			if err := s.m.pg.SetTaskCampaign(tid, &id); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			added = append(added, tid)
		}
	}
	for tid := range have {
		if !want[tid] {
			tid := tid
			if err := s.m.pg.SetTaskCampaign(tid, nil); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			removed = append(removed, tid)
		}
	}
	writeJSON(w, 200, map[string]any{"added": added, "removed": removed})
}

// campaignID 解析路径里的战役 id；非法/缺失时已写好响应，返回 ok=false。
func (s *Server) campaignID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "战役 id 非法")
		return 0, false
	}
	return id, true
}
