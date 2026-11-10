package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/lin9527ya/Norma/tool"
)

func TestReporterReceivesUnifiedReportGuidance(t *testing.T) {
	old := FindingTrafficBindingEnabled
	FindingTrafficBindingEnabled = func() bool { return true }
	t.Cleanup(func() { FindingTrafficBindingEnabled = old })
	mk := func(name string) actool.CoreTool {
		return actool.Build(actool.Spec{Name: name, Description: name, Schema: map[string]any{"type": "object"},
			Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
				return actool.Text(""), nil
			}})
	}
	tools := []actool.CoreTool{mk("report_finding"), mk("get_finding_case"), mk("update_finding_case_report"), mk("merge_finding_records"), mk("suggest_finding_merge"), mk("search_finding_duplicates")}
	_, guidance := findingWorkflowTools("reporter", tools)
	for _, want := range []string{"统一报告", "suggest_finding_merge", "版本冲突", "severity_reason"} {
		if !strings.Contains(guidance, want) {
			t.Fatalf("reporter guidance missing %q: %s", want, guidance)
		}
	}
	_, workerGuidance := findingWorkflowTools("worker", tools)
	if strings.Contains(workerGuidance, "统一报告") {
		t.Fatal("worker must not receive reporter-only case guidance")
	}
}
