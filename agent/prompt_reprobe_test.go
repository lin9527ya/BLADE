package agent

import (
	"strings"
	"testing"
)

// 复探不是按固定次数，而是按证据条件：身份/入口/手法/版本/网络条件或矛盾证据
// 发生变化才重派；条件没变且证据扎实就尊重否定。任务内的边界也应是「一个可解释的
// 研究问题」，允许必要的连续验证，而不是一条命令失败就收尾。
func TestPromptsUseEvidenceConditionedReprobe(t *testing.T) {
	planner := PlannerPromptBody()
	for _, want := range []string{
		"是否重新实验由证据条件决定，不是固定复核次数",
		"不能推广成整条路线永久封闭",
		"真实的外部等待",
		"重试/复核已有方向时，必须写清这次变了什么",
		"旧结论的 limitations",
		"同一次条件变化只值得一轮新实验",
	} {
		if !strings.Contains(planner, want) {
			t.Errorf("planner 提示词缺少「%s」：复探又会退回机械次数或低价值重复", want)
		}
	}
	if strings.Contains(planner, "同一否定方向至多复核一次") {
		t.Error("planner 提示词仍保留固定复核次数")
	}
	for _, want := range []string{
		"一个可解释的研究问题",
		"同一目标、同一授权范围、同一预算内",
		"调整局部手法",
		"何时收尾（不是越久越好）",
		"同一条件重复尝试没有新信息",
	} {
		if !strings.Contains(workerDefaultTmpl, want) {
			t.Errorf("worker 提示词缺少「%s」：意图边界仍会被读成一条命令", want)
		}
	}
}
