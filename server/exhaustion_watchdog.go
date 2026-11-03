package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/lin9527ya/blade/agent"
	"github.com/lin9527ya/blade/db"
)

// 穷尽复核看门狗：planner 声称"策略内已无可行方向"时，在**暂停任务之前**让一个
// 独立的只读裁判复核这个判断。
//
// 位置选在引擎侧而不是工具里：工具调用发生在 planner 自己的会话里，而这里要的是一个
// **不同视角**的判断——同一会话接着问，模型只会重复它刚说过的结论。
//
// 失败策略是"不准确"（即继续跑）：看门狗挂了就放行穷尽，等于它最需要的时候恰好不在。
// 代价是任务多跑一轮（几次工具调用），远小于"证据不足却停住"的代价。

const (
	// exhaustionReviewTimeout 是单次复核的硬上限。复核员只读一小段证据，不该跑很久；
	// 超时就按"不准确"放行继续——不能因为看门狗自己慢而拖住任务。
	exhaustionReviewTimeout = 90 * time.Second
	// exhaustionReviewMaxAttempts 是"被判定不准确"后允许继续跑的轮数。复核员也可能错，
	// 连续两轮都被判不准确后放行暂停（但把结论写进时间线，让人看见分歧）。
	exhaustionReviewMaxAttempts = 2
	// exhaustionReviewerLane 是复核调用的用量分档名（与 judge 同理，单独可查）。
	exhaustionReviewerLane = "exhaustion-review"
)

// exhaustionReviewer 是复核员的可注入实现（测试用）。nil = 走真实 LLM。
type exhaustionReviewer func(ctx context.Context, claim agent.ExhaustionClaim) (agent.ExhaustionVerdict, error)

// reviewExhaustion 复核一次穷尽声明。返回 (是否放行暂停, 一句话结论)。
func (e *Engine) reviewExhaustion(ctx context.Context, t *Task, reason string) (bool, string) {
	if e == nil || t == nil {
		return true, ""
	}
	// 未接线（无 LLM / 测试环境）：保持旧行为，直接放行。
	if e.exhaustionReview == nil {
		return true, ""
	}
	attempts := e.exhaustionAttempts(t.ID)
	if attempts >= exhaustionReviewMaxAttempts {
		// 已经连续被判不准确到这个上限：放行，但把分歧写进时间线。
		return true, fmt.Sprintf("已连续 %d 次复核判定证据不足，按上限放行暂停（复核员与规划者结论不一致，见时间线）", attempts)
	}
	claim := agent.BuildExhaustionClaim(t.Store, e.m.Assets(), parseIntDefault(t.ID), reason)
	rctx, cancel := context.WithTimeout(ctx, exhaustionReviewTimeout)
	defer cancel()
	verdict, err := e.exhaustionReview(rctx, claim)
	if err != nil {
		log.Printf("[exhaustion-review] task %s 复核失败，按「不准确」放行继续: %v", t.ID, err)
		e.bumpExhaustionAttempts(t.ID)
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
			Summary: "穷尽复核员不可用，本次穷尽声明未获复核（任务继续）",
			Detail:  "复核调用失败：" + err.Error() + "\n按失败策略「不准确」处理——证据不足时不该停。若要停，下一轮会再次复核。"})
		return false, ""
	}
	if verdict.Accurate {
		e.resetExhaustionAttempts(t.ID)
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
			Summary: "穷尽复核通过：证据支持「策略内已无可行方向」",
			Detail:  "复核结论：" + verdict.Reason})
		return true, verdict.Reason
	}
	e.bumpExhaustionAttempts(t.ID)
	gaps := ""
	if len(verdict.Gaps) > 0 {
		gaps = "\n具体不足：\n- " + strings.Join(verdict.Gaps, "\n- ")
	}
	e.emitActivity(t, db.Activity{Worker: "system", Kind: "system",
		Summary: "穷尽复核未通过：证据不足以支撑「已无可行方向」，任务继续",
		Detail: "复核结论：" + verdict.Reason + gaps +
			"\n处理：planner 会收到这条结论，下一轮据此补测；若确实已穷尽，请补上证据再声明。"})
	t.NoteExhaustionRejected(verdict.Reason, verdict.Gaps)
	return false, verdict.Reason
}

// --- 复核尝试计数（每任务） ---

func (e *Engine) exhaustionAttempts(taskID string) int {
	if v, ok := e.exhaustionTries.Load(taskID); ok {
		return v.(int)
	}
	return 0
}

func (e *Engine) bumpExhaustionAttempts(taskID string) {
	e.exhaustionTries.Store(taskID, e.exhaustionAttempts(taskID)+1)
}

func (e *Engine) resetExhaustionAttempts(taskID string) {
	e.exhaustionTries.Delete(taskID)
}

// exhaustionReviewJSON 把复核请求渲染成给裁判的 user 消息。
func exhaustionReviewJSON(claim agent.ExhaustionClaim) string {
	b, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		return fmt.Sprintf("%+v", claim)
	}
	return string(b)
}
