package agent

import (
	"strings"
	"testing"
)

// TestTraceNeutralityPromptBlock 钉住两件事：
//  1. 目标侧痕迹中性化规约随每个 agent 的 system 提示词下发（四个 agent 共用
//     deferredSystem 这一个入口）；
//  2. 出站产物语言规约里不再给出带品牌的目标标记示例。
func TestTraceNeutralityPromptBlock(t *testing.T) {
	sys, _ := deferredSystem("base", DeferredInfo{})
	joined := strings.Join(sys, "\n")
	for _, want := range []string{
		"目标侧痕迹必须中性",
		"用户名、密码、邮箱",
		"User-Agent",
		"回调域名",
		"report_artifact",
		"中性词",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("system 提示词缺少「%s」：模型可能把项目特征带到目标上", want)
		}
	}
	if strings.Contains(artifactLanguageEnBlock, "blade-test-marker") {
		t.Fatal("出站产物语言规约仍在建议使用带品牌的目标标记")
	}
	if !strings.Contains(artifactLanguageEnBlock, "ops-test-marker") {
		t.Fatal("出站产物语言规约应给出中性标记示例")
	}
}
