package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lin9527ya/blade/db"
)

// 运行中战略提示的人工入口（任务详情「战略提示」卡片）。
//
// 提示仍是黑板上已有的 hint 节点，这里只补人和它之间的直连：不新增存储、不改 agent
// 工具语义。新增后立即唤醒 planner；planner 采纳后调用 consume_hint 把状态置
// consumed，此后概览不再带它。删除是人工兜底（planner 只 consume，不删）。

const (
	hintTextMaxRunes   = 2000 // 单条提示上限：一句话方向，不是把整份报告粘进来
	hintConsumedWindow = 50   // 已采纳历史只回看最近一窗（active 全量，上限即 MaxActiveHints）
)

// HintDTO is one strategic hint for the task-detail card.
type HintDTO struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	State  string `json:"state"`
	Origin string `json:"origin,omitempty"`
	TS     string `json:"ts"`
	// Note 是 planner 采纳/判断不适用时写下的理由（consume_note），只在已采纳时有。
	Note string `json:"note,omitempty"`
	// AssetIDs 是这条提示锚定的资产（覆盖缺口一键补测会带上），供界面显示。
	AssetIDs []int64 `json:"asset_ids,omitempty"`
}

type hintPayload struct {
	Text        string  `json:"text"`
	ConsumeNote string  `json:"consume_note"`
	AssetIDs    []int64 `json:"asset_ids"`
}

func parseHintPayload(n *db.Node) hintPayload {
	var p hintPayload
	_ = json.Unmarshal(n.Payload, &p)
	return p
}

func hintDTO(n *db.Node) HintDTO {
	p := parseHintPayload(n)
	return HintDTO{
		ID:       i64s(n.ID),
		Text:     p.Text,
		State:    n.State,
		Origin:   n.Origin,
		TS:       rfc3339(n.CreatedAt),
		Note:     p.ConsumeNote,
		AssetIDs: p.AssetIDs,
	}
}

// hintTextConflict reports whether another ACTIVE hint already carries the same
// text (case-insensitive). excludeID skips the hint being edited/restored.
func hintTextConflict(t *Task, text string, excludeID int64) (bool, error) {
	active, err := t.Store.ListByKindState(db.KindHint, db.StateHintActive, db.MaxActiveHints)
	if err != nil {
		return false, err
	}
	for _, n := range active {
		if n.ID == excludeID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(parseHintPayload(n).Text), text) {
			return true, nil
		}
	}
	return false, nil
}

// hintAtCap reports whether the active window is already full. Restoring a
// consumed hint grows the window, so it must obey the same cap as adding one.
func hintAtCap(t *Task) (bool, error) {
	n, err := t.Store.CountByKindState(db.KindHint, db.StateHintActive)
	if err != nil {
		return false, err
	}
	return n >= db.MaxActiveHints, nil
}

func cleanAssetIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// listHints 返回 active（全部，最多 MaxActiveHints）+ 最近一窗 consumed。
// consumed 也返回而不是藏起来：操作者需要看到"这条方向已被规划者采纳"的反馈。
func (s *Server) listHints(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	active, err := t.Store.ListByKindState(db.KindHint, db.StateHintActive, db.MaxActiveHints)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	consumed, err := t.Store.ListByKindState(db.KindHint, db.StateHintConsumed, hintConsumedWindow)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	nodes := append(active, consumed...)
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].ID > nodes[j].ID })
	out := make([]HintDTO, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, hintDTO(n))
	}
	writeJSON(w, 200, map[string]any{"hints": out})
}

// addHint 人工新增一条提示：落成 active hint 节点 → 记「人新增了提示」触发并唤醒
// planner。**不复活任务**：提示不改变运行状态，暂停/终态任务恢复后下一轮才生效。
func (s *Server) addHint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法新增提示")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text     string  `json:"text"`
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "提示内容不能为空")
		return
	}
	if utf8.RuneCountInString(text) > hintTextMaxRunes {
		writeErr(w, 400, "提示过长：最多 "+strconv.Itoa(hintTextMaxRunes)+" 个字符（提示是一句话方向，不是整份报告）")
		return
	}
	// 与所有 active 提示做大小写不敏感去重：重复提交同一方向只会让 planner 多读一遍。
	dup, err := hintTextConflict(t, text, 0)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if dup {
		writeErr(w, 409, "相同提示已在待采纳列表")
		return
	}
	full, err := hintAtCap(t)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if full {
		writeErr(w, 409, "待采纳提示已达上限（"+strconv.Itoa(db.MaxActiveHints)+" 条），请先让规划者采纳或删除旧提示")
		return
	}

	assetIDs := cleanAssetIDs(body.AssetIDs)
	payload := map[string]any{"text": text}
	if len(assetIDs) > 0 {
		payload["asset_ids"] = assetIDs
	}
	id, err := t.Store.AddNode(db.KindHint, payload, 0, db.StateHintActive, "human", assetIDs)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyHint([]string{text})
	node, err := t.Store.GetNode(id)
	if err != nil || node == nil {
		writeErr(w, 500, "提示写入后读取失败")
		return
	}
	writeJSON(w, 200, hintDTO(node))
}

// updateHint 人工修改一条待采纳提示的文本，或撤销一次采纳（consumed → active）。
//
// 文本只在 active 时可改：已采纳提示的文字是"当时采纳了什么"的记录，要改先撤销。
// 两种修改都会重新唤醒 planner（提示内容/可见性变了），但都不会复活任务。
func (s *Server) updateHint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法修改提示")
		return
	}
	defer s.engine.decInflight(t.ID)

	hid, err := strconv.ParseInt(r.PathValue("hid"), 10, 64)
	if err != nil || hid <= 0 {
		writeErr(w, 400, "bad hint id")
		return
	}
	node, err := t.Store.GetNode(hid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindHint {
		writeErr(w, 404, "提示不存在")
		return
	}
	var body struct {
		Text  *string `json:"text"`
		State *string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	if body.Text == nil && body.State == nil {
		writeErr(w, 400, "没有要修改的内容（text 或 state）")
		return
	}
	if body.Text != nil && body.State != nil {
		writeErr(w, 400, "text 与 state 一次只能改一项")
		return
	}

	if body.Text != nil {
		if node.State != db.StateHintActive {
			writeErr(w, 409, "已采纳的提示先撤销采纳再编辑")
			return
		}
		text := strings.TrimSpace(*body.Text)
		if text == "" {
			writeErr(w, 400, "提示内容不能为空")
			return
		}
		if utf8.RuneCountInString(text) > hintTextMaxRunes {
			writeErr(w, 400, "提示过长：最多 "+strconv.Itoa(hintTextMaxRunes)+" 个字符")
			return
		}
		dup, err := hintTextConflict(t, text, hid)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if dup {
			writeErr(w, 409, "相同提示已在待采纳列表")
			return
		}
		changed, err := t.Store.UpdateHintText(hid, text)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if !changed {
			writeErr(w, 409, "提示状态已变化，请刷新后重试")
			return
		}
		t.NotifyHint([]string{text})
	} else {
		state := strings.TrimSpace(*body.State)
		if state != db.StateHintActive {
			writeErr(w, 400, "state 只支持 active（撤销采纳）")
			return
		}
		if node.State == db.StateHintActive {
			writeJSON(w, 200, hintDTO(node)) // 已是待采纳：幂等
			return
		}
		if node.State != db.StateHintConsumed {
			writeErr(w, 409, "当前状态不可撤销采纳")
			return
		}
		text := strings.TrimSpace(parseHintPayload(node).Text)
		dup, err := hintTextConflict(t, text, hid)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if dup {
			writeErr(w, 409, "已有相同内容的待采纳提示，无法撤销这条")
			return
		}
		full, err := hintAtCap(t)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if full {
			writeErr(w, 409, "待采纳提示已达上限（"+strconv.Itoa(db.MaxActiveHints)+" 条），请先清理旧提示")
			return
		}
		changed, err := t.Store.RestoreHint(hid)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if !changed {
			writeErr(w, 409, "提示状态已变化，请刷新后重试")
			return
		}
		t.NotifyHint([]string{text})
	}

	updated, err := t.Store.GetNode(hid)
	if err != nil || updated == nil {
		writeErr(w, 500, "提示更新后读取失败")
		return
	}
	writeJSON(w, 200, hintDTO(updated))
}

// remindHint 把一条仍待采纳的提示重新投递成 planner 触发（不改内容、不改状态）。
// 对应界面上的「再次提醒」：久未采纳时由人决定要不要再敲一次。
func (s *Server) remindHint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	hid, err := strconv.ParseInt(r.PathValue("hid"), 10, 64)
	if err != nil || hid <= 0 {
		writeErr(w, 400, "bad hint id")
		return
	}
	node, err := t.Store.GetNode(hid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindHint {
		writeErr(w, 404, "提示不存在")
		return
	}
	if node.State != db.StateHintActive {
		writeErr(w, 409, "只有待采纳的提示可以再次提醒")
		return
	}
	text := strings.TrimSpace(parseHintPayload(node).Text)
	if text == "" {
		writeErr(w, 409, "提示内容为空，无法提醒")
		return
	}
	t.NotifyHint([]string{text})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// deleteHint 人工删除一条提示（active/consumed 都允许）：硬删除，边随 CASCADE 清理。
// 不唤醒 planner——删除只会让方向从下一轮视野消失，不需要它立刻重规划。
func (s *Server) deleteHint(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法删除提示")
		return
	}
	defer s.engine.decInflight(t.ID)

	hid, err := strconv.ParseInt(r.PathValue("hid"), 10, 64)
	if err != nil || hid <= 0 {
		writeErr(w, 400, "bad hint id")
		return
	}
	node, err := t.Store.GetNode(hid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindHint {
		writeErr(w, 404, "提示不存在")
		return
	}
	if err := t.Store.DeleteHint(hid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
