package intercept

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lin9527ya/blade/opsec"
)

// 凭据尝试预算：对同一个目标的同一个账号，别试太多次。
//
// 它补的是后渗透阶段唯一没人管、却最容易伤到客户的一件事——**账号锁定**。
// verify_credential 的工具描述明写「用在：你真的拿它登进去了」，也就是在鼓励 agent 去目标上
// 实测凭据；而在此之前，执行层（裸 Bash / run_execution）对"试了多少次"一无所知。
// 模型对着 AD 试 500 个口令会锁掉客户账号，很多作业里这等于业务中断。
//
// 它跟已有的护栏是**同一类**，不是新的一类：内置规则已经硬拒 `rm -rf /`、`mkfs`、
// `DROP TABLE`——那些保护的是目标的可用性，账号锁定伤的是同一个东西。所以这里不新增立场，
// 只是把"别把目标搞坏"补到它一直没覆盖到的那一格。
//
// 与 opsec 知识库的分工：P38 的 `bruteforce` 条目**早就写着**"会真实触发账户锁定、影响客户
// 业务账号"。知识一直在，缺的是让它会记账。这个文件就是那本账。
//
// 两条刻意的口径，报告与审批卡上都照写：
//   - **计的是尝试次数，不是失败次数。** shell 层分不出成败，宁可多问一句——误报的代价是
//     人工点一下，漏报的代价是客户的账号被锁。这里不假装在精确建模目标的锁定策略。
//   - **成批尝试不计数。** `hydra -P rockyou.txt` 是一次**调用**、几百次真实尝试，
//     按次数计毫无意义，它单独就构成越界（见 opsec.Attempt.Spray）。

const (
	// SettingCredentialBudget 是设置键：窗口内允许的尝试次数。0 = 不限（靶场/本地自用
	// 需要这个逃生舱）。未设时取 defaultCredentialBudget。
	SettingCredentialBudget = "credential_attempt_budget"
	// SettingCredentialBudgetAction 是超预算后的裁决：allow|ask|deny，默认 ask。
	//
	// 默认取 ask 而不是 deny：人一看就知道"这是靶场"或"这个账号没有锁定策略"，而平台不知道。
	// 这也与既有词汇表（Decision.Action ∈ allow|ask|deny）和"人在环里"的立场一致。
	SettingCredentialBudgetAction = "credential_attempt_budget_action"
)

const (
	defaultCredentialBudget = 3
	// credentialAttemptWindow 是计数窗口。**刻意不做成设置**：典型锁定观察窗就是 15–30 分钟，
	// 多一个旋钮就多一种配错的方式，而这个数并不需要按环境调。
	credentialAttemptWindow = 30 * time.Minute
)

// CredentialBudget 返回窗口内允许的尝试次数。0 表示不限。
//
// 设置读不出来/写错时退回默认值（而非 0）：失效方向要偏保守——一条写坏的设置不该把
// 护栏整条关掉（同 P12「范围读不出来时不放行」）。
func (i *Interceptor) CredentialBudget() int {
	if i.db == nil {
		return defaultCredentialBudget
	}
	return i.getSettingInt(SettingCredentialBudget, defaultCredentialBudget)
}

// CredentialBudgetAction 返回超预算后的裁决动作。
func (i *Interceptor) CredentialBudgetAction() string {
	if i.db == nil {
		return "ask"
	}
	return i.getSettingChoice(SettingCredentialBudgetAction, "ask", "allow", "ask", "deny")
}

// CheckCredentialBudget 在动作发起**之前**判定这次凭据尝试要不要交给人。
//
// 它排在 guard.applyIntercept 的 IsToolEnabled **之前**，理由与 RoE 范围门相同、也是踩过的
// 坑：run_execution 曾因不在默认启用集里，导致"规则、裁判、范围门一起跳过"。凭据尝试走的
// 正是 Bash / run_execution 这条路，同样的漏网不能有第二次。
//
// 无论拦不拦，这次尝试都**先记账**：报告问的是"我们动过谁的账号"，那个答案不该取决于
// 当时有没有超预算。账记不上时转人工而不是放行——数不准的护栏比没有护栏更危险。
func (i *Interceptor) CheckCredentialBudget(_ context.Context, taskID int64, toolName string, input json.RawMessage) (Decision, bool) {
	if i.db == nil {
		return Decision{}, false
	}
	budget := i.CredentialBudget()
	cmd := shellCommandOf(toolName, input)
	if cmd == "" {
		return Decision{}, false
	}
	at, ok := opsec.CredentialAttempt(cmd)
	if !ok {
		return Decision{}, false // 认不出来就说认不出来，不猜（同 opsec.Annotate 的 Uncovered）
	}

	// **先记账，且无论拦不拦、也无论预算是否为 0。** 报告问的是"我们动过谁的账号"，
	// 那个答案不该取决于当时有没有超预算，也不该因为有人把护栏关掉就整段消失
	// ——不限的是"拦"，不是"记"。
	recErr := i.db.RecordCredentialAttempt(taskID, at.Host, at.Account, at.Tool, clipRunes(cmd, 500), at.Spray)
	if budget <= 0 {
		return Decision{}, false // 0 = 不限（靶场逃生舱）：只记不拦
	}
	if recErr != nil {
		return i.credentialDecision("ask", fmt.Sprintf(
			"识别到一次凭据尝试（%s → %s），但**台账写入失败**（%v），无法确认这是第几次。先转人工：数不准的护栏比没有护栏更危险。",
			at.Tool, describeCredTarget(at), recErr)), true
	}

	action := i.CredentialBudgetAction()
	who := describeCredTarget(at)

	if at.Spray {
		return i.credentialDecision(action, fmt.Sprintf(
			"这条命令**本身就是成批尝试**（%s → %s）：一次调用可能是几百次真实尝试，按次数预估没有意义。"+
				"域策略常见锁定阈值 5–10，锁掉的是客户的业务账号——请确认这次喷洒在授权范围与客户约定的可接受时段内。",
			at.Tool, who)), true
	}

	n, err := i.db.CountCredentialAttempts(taskID, at.Host, at.Account, time.Now().Add(-credentialAttemptWindow))
	if err != nil {
		return i.credentialDecision("ask", fmt.Sprintf(
			"识别到一次凭据尝试（%s → %s），但**计数失败**（%v），无法确认这是第几次。先转人工。",
			at.Tool, who, err)), true
	}
	if n <= budget {
		return Decision{}, false // 含刚记的这条：预算 3 = 允许 3 次，第 4 次才问
	}
	return i.credentialDecision(action, fmt.Sprintf(
		"这是本任务对 %s 第 %d 次凭据尝试（窗口 %d 分钟内，预算 %d）。"+
			"**平台计的是尝试次数，不是失败次数**——shell 层分不出成败，宁可多问一句。"+
			"域策略常见锁定阈值 5–10，锁掉的是客户的业务账号。",
		who, n, int(credentialAttemptWindow.Minutes()), budget)), true
}

func (i *Interceptor) credentialDecision(action, msg string) Decision {
	return Decision{
		RuleName:       "[凭据尝试预算]",
		Action:         action,
		Message:        msg,
		MatchedSurface: "tool_input",
		// 不设超时：与 RoE 范围门一致——"等"比"替你决定"更安全，等待期间会持续续租。
	}
}

// describeCredTarget 把 (host, account) 说成人话。账号抽不出来时**明说抽不出来**，
// 不含糊成"某账号"——读的人要能判断该不该放行，就得知道平台到底看清了多少。
func describeCredTarget(at opsec.Attempt) string {
	if strings.TrimSpace(at.Account) == "" {
		return fmt.Sprintf("目标 %s（未抽出账号）", at.Host)
	}
	return fmt.Sprintf("目标 %s 的账号 %s", at.Host, at.Account)
}
