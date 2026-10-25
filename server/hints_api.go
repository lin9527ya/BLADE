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
}

func hintDTO(n *db.Node) HintDTO {
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	return HintDTO{
		ID:     i64s(n.ID),
		Text:   p.Text,
		State:  n.State,
		Origin: n.Origin,
		TS:     rfc3339(n.CreatedAt),
	}
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
		Text string `json:"text"`
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
	active, err := t.Store.ListByKindState(db.KindHint, db.StateHintActive, db.MaxActiveHints)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, n := range active {
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.EqualFold(strings.TrimSpace(p.Text), text) {
			writeErr(w, 409, "相同提示已在待采纳列表")
			return
		}
	}
	n, err := t.Store.CountByKindState(db.KindHint, db.StateHintActive)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if n >= db.MaxActiveHints {
		writeErr(w, 409, "待采纳提示已达上限（"+strconv.Itoa(db.MaxActiveHints)+" 条），请先让规划者采纳或删除旧提示")
		return
	}

	id, err := t.Store.AddNode(db.KindHint, map[string]any{"text": text}, 0, db.StateHintActive, "human", nil)
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
