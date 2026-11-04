package intercept

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 只重试「没拿到终态」：deny/ask 是结论，再问一次只会把结论拖成随机数。
func TestReviewWithRetryStopsOnTerminalVerdict(t *testing.T) {
	calls := 0
	out, err := reviewWithRetry(context.Background(), JudgeConfig{RetryCount: 3}, func(context.Context, int64, string, ReviewInput) (Decision, error) {
		calls++
		return Decision{Action: "deny", Message: "破坏性操作"}, nil
	}, ReviewInput{}, nil)
	if err != nil || out.Action != "deny" {
		t.Fatalf("终态应原样返回：%+v err=%v", out, err)
	}
	if calls != 1 {
		t.Fatalf("终态裁决不该重试，实际调用 %d 次", calls)
	}
}

// 模型出错时按次数重试，直到拿到终态。
func TestReviewWithRetryRetriesOnError(t *testing.T) {
	calls := 0
	out, err := reviewWithRetry(context.Background(), JudgeConfig{RetryCount: 2}, func(context.Context, int64, string, ReviewInput) (Decision, error) {
		calls++
		if calls < 3 {
			return Decision{}, errors.New("model unavailable")
		}
		return Decision{Action: "allow", Message: "ok"}, nil
	}, ReviewInput{}, nil)
	if err != nil || out.Action != "allow" {
		t.Fatalf("第三次应成功：%+v err=%v", out, err)
	}
	if calls != 3 {
		t.Fatalf("应调用 3 次，实际 %d", calls)
	}
}

// 次数用尽后返回最后一次错误，由调用方按 FailAction 处理。
func TestReviewWithRetryExhausts(t *testing.T) {
	calls := 0
	_, err := reviewWithRetry(context.Background(), JudgeConfig{RetryCount: 1}, func(context.Context, int64, string, ReviewInput) (Decision, error) {
		calls++
		return Decision{}, errors.New("boom")
	}, ReviewInput{}, nil)
	if err == nil {
		t.Fatal("次数用尽后应返回错误")
	}
	if calls != 2 {
		t.Fatalf("RetryCount=1 应调用 2 次，实际 %d", calls)
	}
}

// 父 ctx 取消必须立即中断（包括等待间隔），不再花模型调用。
func TestReviewWithRetryHonoursParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := reviewWithRetry(ctx, JudgeConfig{RetryCount: 5, RetryIntervalMS: 10_000}, func(context.Context, int64, string, ReviewInput) (Decision, error) {
		calls++
		cancel() // 第一次就取消父 ctx
		return Decision{}, errors.New("boom")
	}, ReviewInput{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 ctx 取消，实际 %v", err)
	}
	if calls != 1 {
		t.Fatalf("取消后不该再调用，实际 %d 次", calls)
	}
}

// 每次尝试各自带超时：第一次超时不应连带取消后续尝试。
func TestReviewWithRetryPerAttemptTimeout(t *testing.T) {
	calls := 0
	start := time.Now()
	out, err := reviewWithRetry(context.Background(), JudgeConfig{RetryCount: 1, TimeoutSeconds: 1}, func(ctx context.Context, _ int64, _ string, _ ReviewInput) (Decision, error) {
		calls++
		if calls == 1 {
			<-ctx.Done() // 第一次耗到自己的超时
			return Decision{}, ctx.Err()
		}
		return Decision{Action: "ask", Message: "人工确认"}, nil
	}, ReviewInput{}, nil)
	if err != nil || out.Action != "ask" {
		t.Fatalf("第二次应成功：%+v err=%v", out, err)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("第一次应等满自己的超时，实际 %s", elapsed)
	}
}

// 设置校验：越界值必须在保存前拒绝。
func TestJudgeConfigValidateRetry(t *testing.T) {
	for _, bad := range []JudgeConfig{
		{RetryCount: -1},
		{RetryCount: MaxJudgeRetryCount + 1},
		{RetryIntervalMS: -1},
		{RetryIntervalMS: MaxJudgeRetryIntervalMS + 1},
	} {
		if err := bad.ValidateRetry(); err == nil {
			t.Fatalf("非法配置应被拒绝：%+v", bad)
		}
	}
	if err := (JudgeConfig{RetryCount: 3, RetryIntervalMS: 500}).ValidateRetry(); err != nil {
		t.Fatalf("合法配置被拒：%v", err)
	}
}

// 结构化模式下「模型回了一段解析不了的文字」必须算失败尝试并重试，
// 而不是带着空动作退出去走失败策略。
func TestReviewWithRetryRetriesUnparseableStructuredReply(t *testing.T) {
	calls := 0
	finalize := func(d Decision) (Decision, error) {
		if d.Action != "" {
			return d, nil
		}
		sv, err := ParseStructuredVerdict(d.RawReply)
		if err != nil {
			return d, err
		}
		action, reason := DecideStructured(sv)
		d.Action, d.Message = action, reason
		return d, nil
	}
	out, err := reviewWithRetry(context.Background(), JudgeConfig{RetryCount: 2}, func(context.Context, int64, string, ReviewInput) (Decision, error) {
		calls++
		if calls == 1 {
			return Decision{RawReply: "我觉得这个命令没问题"}, nil // 解析不了
		}
		return Decision{RawReply: `{"nouls":{"irreversible_data_loss":0.9,"authorized_pentest_payload":0.2},"choice":"allow","choice_conf":0.9,"comment":"改余额"}`}, nil
	}, ReviewInput{}, finalize)
	if err != nil {
		t.Fatalf("第二次应成功：%v", err)
	}
	if out.Action != "deny" {
		t.Fatalf("破坏轴 0.9 必须 deny，实际 %q", out.Action)
	}
	if calls != 2 {
		t.Fatalf("解析失败应重试，实际调用 %d 次", calls)
	}
}
