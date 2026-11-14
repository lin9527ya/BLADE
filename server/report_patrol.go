package server

import (
	"fmt"
	"log"
	"strconv"
	"time"
)

// emptyReportPatrolInterval throttles the DB scan. The scheduler ticks every
// five seconds, but this patrol spends LLM tokens, so ten minutes is deliberate.
const (
	emptyReportPatrolInterval = 10 * time.Minute
	emptyReportPatrolBatch    = 5
)

// tickEmptyReportPatrol checks confirmed findings whose detailed report is
// still empty and hands a bounded batch to the existing reporter agent. It is
// opt-in (setting empty_report_patrol, default off) and the DB lease caps each
// finding at two attempts, so a broken reporter cannot hot-loop.
func (sc *Scheduler) tickEmptyReportPatrol() {
	if !sc.s.emptyReportPatrolEnabled() {
		return
	}
	if !sc.lastEmptyReportPatrol.IsZero() && time.Since(sc.lastEmptyReportPatrol) < emptyReportPatrolInterval {
		return
	}
	sc.lastEmptyReportPatrol = time.Now()
	if reporter, _ := sc.pg.GetAgentByKey("reporter"); reporter == nil || !reporter.Enabled {
		return
	}
	items, err := sc.pg.ClaimFindingReportPatrol(emptyReportPatrolBatch)
	if err != nil {
		log.Printf("[report-patrol] claim empty reports: %v", err)
		return
	}
	for _, item := range items {
		t, ok := sc.s.m.Task(strconv.FormatInt(item.TaskID, 10))
		if !ok || t == nil {
			continue
		}
		name := item.Name
		if name == "" {
			name = fmt.Sprintf("finding #%d", item.NodeID)
		}
		msg := fmt.Sprintf("【空报告巡检】漏洞「%s」（node #%d，finding #%d，severity=%s）已登记但仍没有详细报告。"+
			"请按报告撰写流程读取该 finding 的证据与已绑定流量，确认它确实成立后调用 update_finding_report(node_id, report) 补写；"+
			"证据不足就说明缺口，不要臆造，也不要重新探测目标。",
			name, item.NodeID, item.FindingID, item.Severity)
		sc.s.StartTriggeredRun("reporter", fmt.Sprintf("空报告巡检 · finding#%d", item.NodeID), msg, item.TaskID, false, t.Description, t.Goal)
		log.Printf("[report-patrol] finding=%d node=%d task=%d 已派 reporter 补写报告", item.FindingID, item.NodeID, item.TaskID)
	}
}
