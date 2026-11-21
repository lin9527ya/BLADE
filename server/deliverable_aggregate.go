package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/lin9527ya/blade/db"
)

const maxAggregateTasks = 200

// captureResponseWriter lets the aggregate endpoint reuse the exact task-report
// handler without an HTTP round trip. It captures status/body only; reports are
// small enough to materialize (the per-task handler already does).
type captureResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (w *captureResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *captureResponseWriter) WriteHeader(code int) { w.status = code }
func (w *captureResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

type aggregateTask struct {
	ID     int64
	Name   string
	Status string
	Report string
	Error  string
}

// aggregateDeliverable creates one deliverable from a campaign/company scope
// on the server. It still runs synchronously (with the same 200-task cap); the
// important improvement is that report generation is no longer N browser calls.
func (s *Server) aggregateDeliverable(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		ScopeKind    string `json:"scope_kind"`
		ScopeID      int64  `json:"scope_id"`
		Name         string `json:"name"`
		Kind         string `json:"kind"`
		Note         string `json:"note"`
		OperationKey string `json:"operation_key"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	req.ScopeKind = strings.ToLower(strings.TrimSpace(req.ScopeKind))
	req.Name = strings.TrimSpace(req.Name)
	if req.ScopeID <= 0 || (req.ScopeKind != "campaign" && req.ScopeKind != "company") {
		writeErr(w, 400, "scope_kind 必须是 campaign/company，scope_id 必须为正数")
		return
	}
	if req.Name == "" {
		writeErr(w, 400, "名称不能为空")
		return
	}

	req.Note = strings.TrimSpace(req.Note)
	req.OperationKey = strings.TrimSpace(req.OperationKey)
	if len(req.OperationKey) > 128 {
		writeErr(w, 400, "操作号最长128字节")
		return
	}
	owner := principalFrom(r.Context()).ID
	if req.OperationKey != "" && owner == "" {
		writeErr(w, 401, "缺少可信操作主体")
		return
	}
	encoded, _ := json.Marshal(struct {
		ScopeKind        string
		ScopeID          int64
		Name, Kind, Note string
	}{req.ScopeKind, req.ScopeID, req.Name, req.Kind, req.Note})
	sum := sha256.Sum256(encoded)
	requestHash := hex.EncodeToString(sum[:])
	if req.OperationKey != "" {
		detail, found, err := pg.AggregateRequest(r.Context(), owner, req.OperationKey, requestHash)
		if err != nil {
			writeAggregateError(w, err)
			return
		}
		if found {
			writeAggregateResult(w, 200, detail, req.OperationKey, true)
			return
		}
	}
	started := time.Now()
	pg = pg.WithContext(r.Context())
	ids := []int64{}
	scopeName := ""
	if req.ScopeKind == "campaign" {
		c, err := pg.GetCampaign(req.ScopeID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if c == nil {
			writeErr(w, 404, "战役不存在")
			return
		}
		scopeName = c.Name
		ids, err = pg.CampaignTaskIDs(req.ScopeID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	} else {
		company, err := pg.Companies().GetCompany(req.ScopeID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if company == nil {
			writeErr(w, 404, "公司不存在")
			return
		}
		scopeName = company.Name
		for _, t := range s.m.List() {
			for _, cid := range t.CompanyIDs {
				if cid == req.ScopeID {
					if id, err := parseTaskID(t.ID); err == nil {
						ids = append(ids, id)
					}
					break
				}
			}
		}
	}
	if len(ids) == 0 {
		writeErr(w, 400, "所选范围没有任务")
		return
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	if len(ids) > maxAggregateTasks {
		writeErr(w, 400, fmt.Sprintf("一次最多聚合 %d 个任务，当前 %d 个", maxAggregateTasks, len(ids)))
		return
	}

	tasks := make([]aggregateTask, 0, len(ids))
	reportOK := 0
	for _, id := range ids {
		if r.Context().Err() != nil {
			return
		}
		t := s.m.ResolveTask(fmt.Sprint(id))
		if t == nil {
			tasks = append(tasks, aggregateTask{ID: id, Name: fmt.Sprintf("任务 #%d", id), Error: "任务未加载（可能已归档或删除）"})
			continue
		}
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = t.Description
		}
		row := aggregateTask{ID: id, Name: name, Status: s.resolvedTaskStatus(t)}
		rec := &captureResponseWriter{}
		query := fmt.Sprintf("/api/report?task=%d", id)
		reportReq, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, query, nil)
		s.getReport(rec, reportReq)
		if rec.status >= 200 && rec.status < 300 {
			row.Report = rec.body.String()
			reportOK++
		} else {
			row.Error = strings.TrimSpace(rec.body.String())
			if row.Error == "" {
				row.Error = fmt.Sprintf("报告生成失败（HTTP %d）", rec.status)
			}
		}
		tasks = append(tasks, row)
	}
	if reportOK == 0 {
		writeErr(w, 503, "所有任务报告都生成失败，未创建交付物")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	md := buildAggregateMarkdown(req.Name, req.ScopeKind, scopeName, tasks)
	actor := actorFrom(r)
	if actor == "" {
		actor = "operator"
	}
	var campaignID, companyID *int64
	if req.ScopeKind == "campaign" {
		campaignID = &req.ScopeID
	} else {
		companyID = &req.ScopeID
	}
	input := db.DeliverableInput{Name: req.Name, Kind: req.Kind, CampaignID: campaignID, CompanyID: companyID, ReportMD: md, Note: req.Note, CreatedBy: actor}
	var detail *db.DeliverableDetail
	var err error
	replayed := false
	if req.OperationKey == "" {
		detail, err = pg.CreateDeliverableContext(r.Context(), input)
	} else {
		detail, replayed, err = pg.CreateAggregateDeliverable(r.Context(), input, owner, req.OperationKey, requestHash)
	}
	log.Printf("[aggregate] scope=%s id=%d reports=%d/%d ms=%d replayed=%t error=%t", req.ScopeKind, req.ScopeID, reportOK, len(tasks), time.Since(started).Milliseconds(), replayed, err != nil)
	if err != nil {
		if r.Context().Err() == nil {
			writeAggregateError(w, err)
		}
		return
	}
	status := 201
	if replayed {
		status = 200
	}
	writeAggregateResult(w, status, detail, req.OperationKey, replayed)

}

func buildAggregateMarkdown(name, scopeKind, scopeName string, tasks []aggregateTask) string {
	now := time.Now().Format("2006-01-02 15:04:05")
	lines := []string{
		"# " + name,
		"",
		"- 生成时间：" + now,
		"- 范围：" + map[string]string{"campaign": "战役", "company": "公司"}[scopeKind] + " · " + scopeName,
		fmt.Sprintf("- 任务数：%d", len(tasks)),
		"",
		"## 任务清单",
		"",
		"| 任务 | 状态 | 报告 |",
		"| --- | --- | --- |",
	}
	for _, t := range tasks {
		reportState := "已收录"
		if t.Error != "" {
			reportState = "失败：" + strings.ReplaceAll(t.Error, "|", "\\|")
		}
		lines = append(lines, fmt.Sprintf("| #%d %s | %s | %s |", t.ID, strings.ReplaceAll(t.Name, "|", "\\|"), t.Status, reportState))
	}
	lines = append(lines, "", "## 分任务报告", "")
	for _, t := range tasks {
		lines = append(lines, fmt.Sprintf("### 任务 #%d · %s", t.ID, t.Name), "")
		if t.Error != "" {
			lines = append(lines, "> 报告读取失败："+t.Error, "")
		} else if strings.TrimSpace(t.Report) == "" {
			lines = append(lines, "_（任务报告为空）_", "")
		} else {
			lines = append(lines, strings.TrimSpace(t.Report), "")
		}
	}
	return strings.Join(lines, "\n")
}

func writeAggregateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrDeliverableRequestConflict):
		writeErr(w, 409, err.Error())
	case errors.Is(err, db.ErrDeliverableRequestGone):
		writeErr(w, 410, err.Error())
	default:
		writeErr(w, 503, "聚合结果未确认，请用相同操作号重试或查看交付物列表")
	}
}
func writeAggregateResult(w http.ResponseWriter, status int, detail *db.DeliverableDetail, key string, replayed bool) {
	writeJSON(w, status, struct {
		*db.DeliverableDetail
		OperationKey string `json:"operation_key,omitempty"`
		Replayed     bool   `json:"replayed"`
	}{detail, key, replayed})
}
