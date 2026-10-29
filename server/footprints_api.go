package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/opsec"
)

// 目标侧痕迹台账：自动归纳（命令历史 + opsec 知识库）+ Agent 补录 + 可选清理。
//
// 与遗留物（artifact）刻意分开：artifact 是“我们留下的具体对象”，footprint 是“目标可能
// 看见的信号”。清理可以交给 Agent 在授权/审批下执行，也可以人工销账/接受风险。

const (
	footprintEvidenceMaxRunes = 4000
	footprintReasonMaxRunes   = 2000
	footprintCleanupMaxIDs    = 100
	footprintCleanupChunk     = 20
)

// footprintScanMu 串行化扫描：痕迹页签与报告页可能同时触发，不去重就会重复建节点。
var footprintScanMu sync.Mutex

type FootprintDTO struct {
	ID               string   `json:"id"`
	State            string   `json:"state"`
	StateLabel       string   `json:"state_label"`
	Source           string   `json:"source,omitempty"`
	SourceLabel      string   `json:"source_label,omitempty"`
	Category         string   `json:"category,omitempty"`
	CategoryLabel    string   `json:"category_label,omitempty"`
	Summary          string   `json:"summary"`
	Detail           string   `json:"detail,omitempty"`
	Where            []string `json:"where,omitempty"`
	Signals          []string `json:"signals,omitempty"`
	Exposure         string   `json:"exposure,omitempty"`
	ExposureLabel    string   `json:"exposure_label,omitempty"`
	Host             string   `json:"host,omitempty"`
	Locator          string   `json:"locator,omitempty"`
	Cleanup          string   `json:"cleanup,omitempty"`
	Evidence         string   `json:"evidence,omitempty"`
	Confidence       string   `json:"confidence,omitempty"`
	OpsecNoteID      string   `json:"opsec_note_id,omitempty"`
	Occurrences      int      `json:"occurrences,omitempty"`
	CleanupIntentID  int64    `json:"cleanup_intent_id,omitempty"`
	RemovedEvidence  string   `json:"removed_evidence,omitempty"`
	RemovedNote      string   `json:"removed_note,omitempty"`
	AcceptedReason   string   `json:"accepted_reason,omitempty"`
	NotFoundEvidence string   `json:"not_found_evidence,omitempty"`
	NotFoundNote     string   `json:"not_found_note,omitempty"`
	TS               string   `json:"ts"`
}

func footprintDTO(n *db.Node) FootprintDTO {
	f := db.ParseFootprint(n)
	return FootprintDTO{
		ID: i64s(n.ID), State: n.State, StateLabel: db.FootprintStateLabel(n.State),
		Source: f.Source, SourceLabel: db.FootprintSourceLabel(f.Source),
		Category: f.Category, CategoryLabel: db.FootprintCategoryLabel(f.Category),
		Summary: f.Summary, Detail: f.Detail, Where: f.Where, Signals: f.Signals,
		Exposure: f.Exposure, ExposureLabel: db.FootprintExposureLabel(f.Exposure),
		Host: f.Host, Locator: f.Locator, Cleanup: f.Cleanup, Evidence: f.Evidence,
		Confidence: f.Confidence, OpsecNoteID: f.OpsecNoteID, Occurrences: f.Occurrences,
		CleanupIntentID: f.CleanupIntentID, RemovedEvidence: f.RemovedEvidence,
		RemovedNote: f.RemovedNote, AcceptedReason: f.AcceptedReason,
		NotFoundEvidence: f.NotFoundEvidence, NotFoundNote: f.NotFoundNote,
		TS: rfc3339(n.CreatedAt),
	}
}

func (s *Server) listFootprints(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	nodes, err := t.Store.ListByKind(db.KindFootprint, 500)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]FootprintDTO, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, footprintDTO(n))
	}
	counts, _ := t.Store.CountKindStates(db.KindFootprint)
	writeJSON(w, 200, map[string]any{"footprints": out, "counts": counts, "total": len(out)})
}

// scanFootprints 扫描命令类工具的执行记录，用 opsec 知识库归纳“预计留痕”。
func (s *Server) scanFootprints(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除，无法归纳痕迹")
		return
	}
	defer s.engine.decInflight(t.ID)
	footprintScanMu.Lock()
	defer footprintScanMu.Unlock()
	result, err := s.scanTaskFootprints(t)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// 没有变化时不写活动：页签打开与报告生成都会触发扫描，每次都留一条
	// “扫描 0 条新增”的留言只会把时间线刷满。
	if result.Created+result.Updated+result.Reopened > 0 {
		s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
			Summary: fmt.Sprintf("痕迹归纳：扫描 %d 条命令，新增 %d、更新 %d、复发 %d、未收录 %d",
				result.CommandsScanned, result.Created, result.Updated, result.Reopened, result.Uncovered)})
	}
	writeJSON(w, 200, result)
}

type footprintScanResult struct {
	CommandsScanned  int      `json:"commands_scanned"`
	Created          int      `json:"created"`
	Updated          int      `json:"updated"`
	Reopened         int      `json:"reopened"`
	Uncovered        int      `json:"uncovered"`
	SkippedBlocked   int      `json:"skipped_blocked"`
	WithoutResult    int      `json:"commands_without_result"`
	UncoveredSamples []string `json:"uncovered_samples,omitempty"`
}

// footprintCommandTools 是“真正执行了命令/命令串”的工具。opsec_notes 不在其中：
// 它只是查询，不是执行，扫进来会把自己的输入当成目标痕迹。
var footprintCommandTools = map[string]bool{
	"Bash": true, "run_execution": true, "foothold_exec": true, "loot_exec": true,
	"shell_open": true, "shell_send": true,
}

func commandFromDetail(detail string) string {
	var m map[string]any
	if json.Unmarshal([]byte(detail), &m) != nil {
		return ""
	}
	for _, key := range []string{"command", "cmd", "script"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func platformBlocked(result string, isErr bool) bool {
	if !isErr && !strings.Contains(result, "BLADE 平台") && !strings.Contains(result, "平台安全管控") {
		return false
	}
	for _, marker := range []string{"【BLADE 平台管控", "平台安全管控阻止执行", "人工审批未通过", "拦截规则拒绝"} {
		if strings.Contains(result, marker) {
			return true
		}
	}
	return false
}

func footprintCategoryFromWhere(where []string) string {
	for _, w := range where {
		switch w {
		case opsec.WhereTargetSystem:
			return db.FootprintCatLog
		case opsec.WhereEDR:
			return db.FootprintCatEDR
		case opsec.WhereNetwork:
			return db.FootprintCatNetwork
		case opsec.WhereApp:
			return db.FootprintCatApp
		case opsec.WhereLocal:
			return db.FootprintCatLocal
		}
	}
	return db.FootprintCatOther
}

// webshellDownloadExec 把「curl 了一个 URL」与「下载后执行」分开。opsec 知识库的
// 匹配词包含裸 curl/wget（用于回答 opsec_notes 的“这一手可能留什么”），但自动归纳
// 若照单全收，会把每一次普通 HTTP 请求都算成“落马”，次数很快失真。
var webshellDownloadExec = regexp.MustCompile(`(?i)(?:\|\s*(?:ba|z|k|d)?sh\b|\|\s*python[23]?\b|&&\s*(?:chmod\s+\+x|\./|bash\b|sh\b)|certutil\s+-urlcache|bitsadmin\s+/transfer)`)

// reverseShellExec 同样要求真正的执行/外联特征；单写 ncat 可能只是端口探测。
var reverseShellExec = regexp.MustCompile(`(?i)(?:\bnc(?:at)?\b[^\n]{0,80}\s-e\s|\b(?:bash|sh)\s+-i\b|/dev/tcp/|socat\b[^\n]{0,120}\bexec:|meterpreter|msfconsole)`)

// footprintNoteRelevant 是对自动归纳的降噪：只保留与 note 标题相符的命令。
// 知识库条目本身仍保持宽匹配，供 worker 主动查询。
func footprintNoteRelevant(noteID, cmd string) bool {
	switch noteID {
	case "webshell-download":
		return webshellDownloadExec.MatchString(cmd)
	case "reverse-shell":
		return reverseShellExec.MatchString(cmd)
	}
	return true
}

func footprintNoteSignals(n opsec.Note) []string {
	out := make([]string, 0, len(n.Signals))
	for _, s := range n.Signals {
		// 语言痕迹的动态命中片段会带出命令原文，不写入交付物。
		if strings.HasPrefix(strings.TrimSpace(s), "本次命中：") {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (s *Server) scanTaskFootprints(t *Task) (footprintScanResult, error) {
	var res footprintScanResult
	acts, err := t.Store.ListCommandActivities(2000)
	if err != nil {
		return res, err
	}
	// 倒序取最新，翻成升序按水位处理。
	for i, j := 0, len(acts)-1; i < j; i, j = i+1, j-1 {
		acts[i], acts[j] = acts[j], acts[i]
	}
	type noteAgg struct {
		note        opsec.Note
		activityIDs []int64
		assets      map[int64]bool
	}
	agg := map[string]*noteAgg{}
	assetsCache := map[int64][]int64{}
	for _, a := range acts {
		if !footprintCommandTools[a.Tool] {
			continue
		}
		// 本地 Bash/run_execution 只有挂在 worker 意图上才可能真正打目标；
		// 主 Agent 的本机命令、平台自身 API 调用不应被算成目标侧痕迹。
		if a.Tool == "Bash" || a.Tool == "run_execution" {
			if a.NodeID == nil {
				continue
			}
		}
		cmd := commandFromDetail(a.Detail)
		if cmd == "" {
			continue
		}
		res.CommandsScanned++
		if a.Result == "" {
			res.WithoutResult++
		}
		if platformBlocked(a.Result, a.IsError) {
			res.SkippedBlocked++
			continue
		}
		notes := opsec.Annotate(cmd)
		if notes.Uncovered {
			res.Uncovered++
			if len(res.UncoveredSamples) < 5 {
				res.UncoveredSamples = append(res.UncoveredSamples, firstLine(cmd, 120))
			}
			continue
		}
		var assetIDs []int64
		if a.NodeID != nil {
			if cached, ok := assetsCache[*a.NodeID]; ok {
				assetIDs = cached
			} else {
				assetIDs, _ = t.Store.AssetIDsForNode(*a.NodeID)
				assetsCache[*a.NodeID] = assetIDs
			}
		}
		for _, n := range notes.Notes {
			if n.ID == "anti-forensics" { // 清理动作本身由 guard/审批审计，不再自我生成痕迹
				continue
			}
			if !footprintNoteRelevant(n.ID, cmd) {
				continue
			}
			a2 := agg[n.ID]
			if a2 == nil {
				a2 = &noteAgg{note: n, assets: map[int64]bool{}}
				agg[n.ID] = a2
			}
			a2.activityIDs = append(a2.activityIDs, a.ID)
			for _, id := range assetIDs {
				a2.assets[id] = true
			}
		}
	}
	ids := make([]string, 0, len(agg))
	for id := range agg {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, noteID := range ids {
		a := agg[noteID]
		last := a.activityIDs[len(a.activityIDs)-1]
		occ := len(a.activityIDs)
		assetSet := make([]int64, 0, len(a.assets))
		for id := range a.assets {
			assetSet = append(assetSet, id)
		}
		sort.Slice(assetSet, func(i, j int) bool { return assetSet[i] < assetSet[j] })
		base := map[string]any{
			"summary": a.note.Title, "detail": a.note.Note,
			"source": db.FootprintSourcePredicted, "category": footprintCategoryFromWhere(a.note.Where),
			"where": a.note.Where, "signals": footprintNoteSignals(a.note),
			"exposure":      db.NormalizeFootprintExposure(a.note.Exposure),
			"opsec_note_id": a.note.ID, "last_activity_id": last,
		}
		active, err := t.Store.FindActiveFootprintByNote(a.note.ID)
		if err != nil {
			return res, err
		}
		if active != nil {
			f := db.ParseFootprint(active)
			newCount := 0
			for _, id := range a.activityIDs {
				if id > f.LastActivityID {
					newCount++
				}
			}
			if newCount == 0 {
				continue
			}
			p := mergePayload(active.Payload, base)
			p["occurrences"] = f.Occurrences + newCount
			p["last_activity_id"] = last
			p["asset_ids"] = assetSet
			if err := t.Store.SetNodePayload(active.ID, db.KindFootprint, p); err != nil {
				return res, err
			}
			res.Updated++
			continue
		}
		latest, err := t.Store.FindLatestFootprintByNote(a.note.ID)
		if err != nil {
			return res, err
		}
		if latest != nil && latest.State == db.StateFootprintAccepted {
			// 已经接受风险的类别再次出现：只更新次数，不反复打开。
			f := db.ParseFootprint(latest)
			newCount := 0
			for _, id := range a.activityIDs {
				if id > f.LastActivityID {
					newCount++
				}
			}
			if newCount == 0 {
				continue
			}
			p := mergePayload(latest.Payload, base)
			p["occurrences"] = f.Occurrences + newCount
			p["last_activity_id"] = last
			p["asset_ids"] = assetSet
			if err := t.Store.SetNodePayload(latest.ID, db.KindFootprint, p); err != nil {
				return res, err
			}
			res.Updated++
			continue
		}
		if latest != nil {
			f := db.ParseFootprint(latest)
			if last <= f.LastActivityID {
				continue // 已清理/未发现后重扫旧记录，不重新打开
			}
			base["reopened_from"] = latest.ID
			res.Reopened++
		} else {
			res.Created++
		}
		base["occurrences"] = occ
		base["asset_ids"] = assetSet
		if _, err := t.Store.AddNode(db.KindFootprint, base, 5, db.StateFootprintOpen, "system", assetSet); err != nil {
			return res, err
		}
	}
	return res, nil
}

func mergePayload(raw []byte, patch map[string]any) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	for k, v := range patch {
		out[k] = v
	}
	return out
}

func (s *Server) footprintActionCommon(w http.ResponseWriter, r *http.Request) (*Task, *db.Node, bool) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return nil, nil, false
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除，无法修改痕迹")
		return nil, nil, false
	}
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil || fid <= 0 {
		s.engine.decInflight(t.ID)
		writeErr(w, 400, "bad footprint id")
		return nil, nil, false
	}
	n, err := t.Store.GetNode(fid)
	if err != nil {
		s.engine.decInflight(t.ID)
		writeErr(w, 500, err.Error())
		return nil, nil, false
	}
	if n == nil || n.Kind != db.KindFootprint {
		s.engine.decInflight(t.ID)
		writeErr(w, 404, "痕迹不存在")
		return nil, nil, false
	}
	return t, n, true
}

func (s *Server) clearFootprint(w http.ResponseWriter, r *http.Request) {
	t, n, ok := s.footprintActionCommon(w, r)
	if !ok {
		return
	}
	defer s.engine.decInflight(t.ID)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct{ Evidence, Note string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	if utf8.RuneCountInString(body.Evidence) > footprintEvidenceMaxRunes || utf8.RuneCountInString(body.Note) > footprintReasonMaxRunes {
		writeErr(w, 400, "清理证据/备注过长")
		return
	}
	changed, err := t.Store.MarkFootprintCleaned(n.ID, body.Evidence, body.Note)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if changed == 0 {
		writeErr(w, 409, "当前状态不可标记已清理（可能已经清理/接受风险/核实不存在）")
		return
	}
	updated, _ := t.Store.GetNode(n.ID)
	s.engine.emitActivity(t, db.Activity{NodeID: &n.ID, Worker: "human", Kind: "text", Summary: "人工标记痕迹已清理"})
	writeJSON(w, 200, footprintDTO(updated))
}

func (s *Server) acceptFootprintRisk(w http.ResponseWriter, r *http.Request) {
	t, n, ok := s.footprintActionCommon(w, r)
	if !ok {
		return
	}
	defer s.engine.decInflight(t.ID)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct{ Reason string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	if utf8.RuneCountInString(body.Reason) > footprintReasonMaxRunes {
		writeErr(w, 400, "理由过长")
		return
	}
	changed, err := t.Store.AcceptFootprintRisk(n.ID, body.Reason)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if changed == 0 {
		writeErr(w, 409, "当前状态不可接受风险（可能已清理/已核实不存在）")
		return
	}
	updated, _ := t.Store.GetNode(n.ID)
	s.engine.emitActivity(t, db.Activity{NodeID: &n.ID, Worker: "human", Kind: "text", Summary: "人工接受痕迹风险"})
	writeJSON(w, 200, footprintDTO(updated))
}

func (s *Server) markFootprintNotFound(w http.ResponseWriter, r *http.Request) {
	t, n, ok := s.footprintActionCommon(w, r)
	if !ok {
		return
	}
	defer s.engine.decInflight(t.ID)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct{ Evidence, Note string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	changed, err := t.Store.MarkFootprintNotFound(n.ID, body.Evidence, body.Note)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if changed == 0 {
		writeErr(w, 409, "当前状态不可标记未发现（可能已清理/已接受）")
		return
	}
	updated, _ := t.Store.GetNode(n.ID)
	s.engine.emitActivity(t, db.Activity{NodeID: &n.ID, Worker: "human", Kind: "text", Summary: "人工核实痕迹不存在"})
	writeJSON(w, 200, footprintDTO(updated))
}

func (s *Server) cleanupFootprints(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除，无法提交清理")
		return
	}
	defer s.engine.decInflight(t.ID)
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	seen := map[int64]bool{}
	ids := make([]int64, 0, len(body.IDs))
	for _, id := range body.IDs {
		if id <= 0 {
			writeErr(w, 400, "痕迹 id 无效")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > footprintCleanupMaxIDs {
		writeErr(w, 400, fmt.Sprintf("ids 数量必须为 1-%d", footprintCleanupMaxIDs))
		return
	}
	type item struct {
		node *db.Node
		f    db.Footprint
	}
	items := make([]item, 0, len(ids))
	skipped := 0
	for _, id := range ids {
		n, err := t.Store.GetNode(id)
		if err != nil || n == nil || n.Kind != db.KindFootprint {
			writeErr(w, 404, fmt.Sprintf("痕迹 #%d 不存在", id))
			return
		}
		if n.State != db.StateFootprintOpen && n.State != db.StateFootprintUnknown && n.State != db.StateFootprintAccepted {
			writeErr(w, 400, fmt.Sprintf("痕迹 #%d 当前状态不可清理", id))
			return
		}
		f := db.ParseFootprint(n)
		// 已有未收束的清理意图：跳过，避免重复点击生成多条意图。意图落终态
		// （worker 放弃/被拦截）后仍允许重新提交。
		if f.CleanupIntentID > 0 {
			if in, ierr := t.Store.GetNode(f.CleanupIntentID); ierr == nil && in != nil && in.Kind == db.KindIntent &&
				(in.State == "open" || in.State == "running" || in.State == "paused") {
				skipped++
				continue
			}
		}
		items = append(items, item{node: n, f: f})
	}
	if len(items) == 0 {
		writeErr(w, 409, "所选痕迹均已有清理意图在执行；请等待 worker 收尾或到会话查看进度")
		return
	}
	// 按 host 分组；自动归纳没有 host 时按资产集合分组，避免把多台目标塞进同一条意图。
	// 每组最多 20 条一条清理意图。
	groups := map[string][]item{}
	order := []string{}
	for _, it := range items {
		key := strings.TrimSpace(it.f.Host)
		if key == "" {
			ids := append([]int64(nil), it.f.AssetIDs...)
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			if len(ids) > 0 {
				parts := make([]string, 0, len(ids))
				for _, id := range ids {
					parts = append(parts, strconv.FormatInt(id, 10))
				}
				key = "assets:" + strings.Join(parts, ",")
			}
		}
		if key == "" {
			key = "未指定主机"
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], it)
	}
	var intentIDs []int64
	created := 0
	for _, groupKey := range order {
		g := groups[groupKey]
		host := strings.TrimSpace(g[0].f.Host)
		for start := 0; start < len(g); start += footprintCleanupChunk {
			end := start + footprintCleanupChunk
			if end > len(g) {
				end = len(g)
			}
			chunk := g[start:end]
			chunkIDs := make([]int64, 0, len(chunk))
			var detail strings.Builder
			for _, it := range chunk {
				chunkIDs = append(chunkIDs, it.node.ID)
				fmt.Fprintf(&detail, "- #%d %s", it.node.ID, it.f.Summary)
				if it.f.Host != "" {
					fmt.Fprintf(&detail, "（host=%s）", it.f.Host)
				}
				if it.f.Locator != "" {
					fmt.Fprintf(&detail, " 位置=%s", it.f.Locator)
				}
				if it.f.Cleanup != "" {
					fmt.Fprintf(&detail, " 建议清理=%s", it.f.Cleanup)
				}
				detail.WriteString("\n")
			}
			summary := fmt.Sprintf("清理测试痕迹（人工指定）：%s · %d 条", nzHost(host), len(chunk))
			payload := map[string]any{
				"summary": summary, "risk_area": "other", "intent_kind": "discovery",
				"cleanup": true, "cleanup_host": host, "cleanup_footprint_ids": chunkIDs,
				"detail": "只清理下列痕迹；目标侧清理走 foothold_exec/loot_exec；日志/审计类修改需人工审批；" +
					"命令被拦截就保持 open 并写 fact 说明，不要绕过规则。\n" + detail.String(),
			}
			// 把痕迹上记录的资产带给 worker，让它知道去哪台目标；**不写成意图锚点**，
			// 避免清理动作污染覆盖矩阵与攻击链。
			assetSet := map[int64]bool{}
			for _, it := range chunk {
				for _, id := range it.f.AssetIDs {
					if id > 0 {
						assetSet[id] = true
					}
				}
				if ids, aerr := t.Store.AssetIDsForNode(it.node.ID); aerr == nil {
					for _, id := range ids {
						if id > 0 {
							assetSet[id] = true
						}
					}
				}
			}
			if len(assetSet) > 0 {
				assetIDs := make([]int64, 0, len(assetSet))
				for id := range assetSet {
					assetIDs = append(assetIDs, id)
				}
				sort.Slice(assetIDs, func(i, j int) bool { return assetIDs[i] < assetIDs[j] })
				payload["asset_ids"] = assetIDs
			}
			intentID, err := t.Store.AddIntent(payload, 9, nil, "human")
			if err != nil {
				writeErr(w, 500, "创建清理意图失败: "+err.Error())
				return
			}
			intentIDs = append(intentIDs, intentID)
			created += len(chunk)
			for _, it := range chunk {
				p := mergePayload(it.node.Payload, map[string]any{"cleanup_intent_id": intentID})
				_ = t.Store.SetNodePayload(it.node.ID, db.KindFootprint, p)
			}
		}
	}
	s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
		Summary: fmt.Sprintf("已提交 %d 条痕迹清理意图（%d 条痕迹）", len(intentIDs), created)})
	paused := t.lifecycleSnapshot().Paused || s.engine.IsPaused(t.ID)
	if !paused {
		s.reviveTask(t)
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "intents": intentIDs, "footprints": created, "skipped": skipped, "paused": paused,
	})
}

func nzHost(h string) string {
	if strings.TrimSpace(h) == "" {
		return "未指定主机"
	}
	return h
}
