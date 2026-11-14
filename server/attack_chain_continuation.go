package server

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lin9527ya/blade/db"
)

// attackChainContinuationMaxFindings bounds one hint when a batch contains many
// findings; the planner still has list_findings for the complete set.
const attackChainContinuationMaxFindings = 8

// tickAttackChainContinuation scans new findings independently of custom-agent
// triggers. Using its own watermark keeps the hint from being coupled to the
// P3 trigger configuration (a deployment with no custom triggers must still get
// the default-on continuation cue).
func (sc *Scheduler) tickAttackChainContinuation() {
	if !sc.s.attackChainContinuationEnabled() {
		return
	}
	lastID, _ := strconv.ParseInt(sc.mustState(schedKeyLastAttackChainFinding), 10, 64)
	events, err := sc.pg.NewFindingsSince(lastID)
	if err != nil || len(events) == 0 {
		return
	}
	maxID := lastID
	for _, e := range events {
		if e.NodeID > maxID {
			maxID = e.NodeID
		}
	}
	sc.setWatermark(schedKeyLastAttackChainFinding, maxID)
	sc.fireAttackChainContinuation(events)
}

// fireAttackChainContinuation sends a *non-blocking* planner hint after new
// high/critical findings. It deliberately does not create an intent, gate
// completion, or require a next hop: the blackboard flow stays planner-owned.
// The finding watermark in fireFindings makes this fire at most once per finding.
func (sc *Scheduler) fireAttackChainContinuation(events []db.TaskEvent) {
	if len(events) == 0 || !sc.s.attackChainContinuationEnabled() {
		return
	}
	byTask := map[int64][]db.TaskEvent{}
	for _, e := range events {
		sev := strings.ToLower(strings.TrimSpace(e.Severity))
		if sev != "high" && sev != "critical" {
			continue
		}
		byTask[e.TaskID] = append(byTask[e.TaskID], e)
	}
	if len(byTask) == 0 {
		return
	}
	taskIDs := make([]int64, 0, len(byTask))
	for id := range byTask {
		taskIDs = append(taskIDs, id)
	}
	sort.Slice(taskIDs, func(i, j int) bool { return taskIDs[i] < taskIDs[j] })
	for _, taskID := range taskIDs {
		t, ok := sc.s.m.Task(strconv.FormatInt(taskID, 10))
		if !ok || t == nil {
			continue
		}
		if isTerminalStatus(t.lifecycleSnapshot().Status) {
			continue
		}
		evs := byTask[taskID]
		t.NotifyHint([]string{attackChainContinuationText(evs)})
		sc.s.engine.emitActivity(t, db.Activity{
			Worker:  "system",
			Kind:    "text",
			Summary: fmt.Sprintf("攻击链续跳提醒：%d 条 high/critical 发现待评估下一跳", len(evs)),
		})
	}
}

func attackChainContinuationText(events []db.TaskEvent) string {
	var b strings.Builder
	b.WriteString("【攻击链续跳提醒】本轮新增 high/critical 发现：\n")
	limit := len(events)
	if limit > attackChainContinuationMaxFindings {
		limit = attackChainContinuationMaxFindings
	}
	for _, e := range events[:limit] {
		vulnClass := strings.TrimSpace(e.VulnClass)
		if vulnClass == "" {
			vulnClass = "finding"
		}
		fmt.Fprintf(&b, "- #%d [%s/%s] %s\n", e.NodeID, vulnClass, strings.TrimSpace(e.Severity), clipRunes(strings.TrimSpace(e.Summary), 180))
	}
	if rest := len(events) - limit; rest > 0 {
		fmt.Fprintf(&b, "- …另有 %d 条（用 list_findings 查看）\n", rest)
	}
	b.WriteString("请评估这些发现能否推进到影响面（利用、提权、横向、读取数据），由你决定是否用 add_intent 派下一跳。" +
		"已有意图覆盖、或只是版本/指纹线索时，不要为续跳重复探测；本提醒不构成必须派单。")
	return b.String()
}
