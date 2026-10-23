package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 证据分级：critical/high 没有 evidence 一律拒收，medium/low 保持可选。
// 理由是不对称的——高危会占报告头条、最先被客户复验，一条空证据的高危会把整份
// 报告的可信度拖下水；而逼 medium/low 交证据只会让模型编一句，比空着更糟。
func TestReportFindingRequiresEvidenceForHighSeverity(t *testing.T) {
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTask("evidence gate", "fixture", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := NewToolSet(nil, "")
	ts.ts, ts.taskID = d.Exploration(task.ExplorationID), task.ID
	tool := ts.addFinding()

	call := func(body string) string {
		t.Helper()
		res, err := tool.Call(context.Background(), json.RawMessage(body), nil)
		if err != nil {
			t.Fatalf("调用出错: %v", err)
		}
		return res.Flatten()
	}

	// 大小写/空格不该让人钻过去："HIGH" / " Critical " 同样属高危。
	for _, sev := range []string{"high", "critical", "HIGH", " Critical "} {
		out := call(`{"vulnclass":"sqli","severity":"` + sev + `","summary":"注入","confidence":"high"}`)
		if !strings.Contains(out, "必须给 evidence") {
			t.Fatalf("severity=%q 空证据应被拒收：%s", sev, out)
		}
		if strings.Contains(out, "finding recorded: ") {
			t.Fatalf("severity=%q 空证据不该落库：%s", sev, out)
		}
	}

	// medium/low 无证据仍可登记（避免逼模型编证据）。
	for _, sev := range []string{"medium", "low"} {
		out := call(`{"vulnclass":"sqli","severity":"` + sev + `","summary":"待确认","confidence":"low"}`)
		if !strings.HasPrefix(out, "finding recorded: ") {
			t.Fatalf("severity=%q 无证据应可登记：%s", sev, out)
		}
	}

	// 带证据的高危正常登记。
	out := call(`{"vulnclass":"sqli","severity":"critical","summary":"可复现注入","confidence":"high",` +
		`"evidence":"curl 'http://t/?id=1%20AND%20sleep(5)' → 响应延迟 5s；同接口 id=1 无延迟"}`)
	if !strings.HasPrefix(out, "finding recorded: ") {
		t.Fatalf("有证据的高危应可登记：%s", out)
	}
}
