package intercept

import (
	"context"
	"fmt"
	"time"
)

// 裁判重试：模型偶尔会返回一段解析不了的文字（或干脆超时），一次失败就转人工会让
// 审批队列里塞满"其实规则上没问题"的调用。这里在**同一份输入快照**上重试，直到拿到
// allow/ask/deny 之一。
//
// 三条纪律：
//   - 只有「没拿到终态」才重试：deny/ask 是结论，重试只会把结论拖成随机数；
//   - 输入快照固定：重试之间工具参数不会变，不能因为重试而审了另一份内容；
//   - 父 ctx 取消即断：任务被暂停/终止时不该继续为它花模型调用。
const (
	// MaxJudgeRetryCount 是重试次数的硬上限（额外尝试次数）。
	MaxJudgeRetryCount = 20
	// MaxJudgeRetryIntervalMS 是重试间隔的硬上限。
	MaxJudgeRetryIntervalMS = 3600_000
	// defaultJudgeRetryCount 是未配置时的重试次数。0 = 不重试（旧行为）。
	defaultJudgeRetryCount = 2
	// defaultJudgeRetryIntervalMS 是未配置时的重试间隔。
	defaultJudgeRetryIntervalMS = 1000
)

// ValidateRetry 在保存设置前拒绝非法值，避免存进去一个"看着配了、实际没生效"的数。
func (c JudgeConfig) ValidateRetry() error {
	if c.RetryCount < 0 || c.RetryCount > MaxJudgeRetryCount {
		return fmt.Errorf("retry_count 必须在 0 到 %d 之间", MaxJudgeRetryCount)
	}
	if c.RetryIntervalMS < 0 || c.RetryIntervalMS > MaxJudgeRetryIntervalMS {
		return fmt.Errorf("retry_interval_ms 必须在 0 到 %d 之间", MaxJudgeRetryIntervalMS)
	}
	return nil
}

// reviewWithRetry 反复调用 reviewer，直到得到终态裁决或次数/时间用尽。
// 每次尝试各自带超时，父 ctx 取消会立即中断（包括等待间隔）。
//
// finalize 把一次模型回复转成终态裁决：结构化模式要在**循环内**解析，否则
// 「模型回了一段解析不了的文字」会被当成成功返回，重试等于没做。finalize 返回
// error 即视为本次尝试失败、进入下一次重试。
func reviewWithRetry(ctx context.Context, cfg JudgeConfig, rv Reviewer, input ReviewInput, finalize func(Decision) (Decision, error)) (out Decision, err error) {
	if finalize == nil {
		finalize = func(d Decision) (Decision, error) {
			switch d.Action {
			case "allow", "ask", "deny":
				return d, nil
			}
			return d, fmt.Errorf("模型输出无法解析")
		}
	}
	for attempt := 0; attempt <= cfg.RetryCount; attempt++ {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if attempt > 0 && cfg.RetryIntervalMS > 0 {
			timer := time.NewTimer(time.Duration(cfg.RetryIntervalMS) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return out, ctx.Err()
			case <-timer.C:
			}
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		attemptCtx := ctx
		cancel := func() {}
		if cfg.TimeoutSeconds > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
		}
		out, err = rv(attemptCtx, cfg.ProfileID, cfg.Prompt, input)
		if attemptCtx.Err() != nil {
			err = attemptCtx.Err()
		}
		cancel()
		if err == nil {
			var ferr error
			out, ferr = finalize(out)
			if ferr == nil {
				return out, nil
			}
			err = ferr
		}
	}
	return out, err
}
