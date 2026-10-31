package db

// 任务状态的两套集合：库里的（持久化）与 API 暴露的（含派生）。
//
// 背景：任务状态有四套表示——db.Task（库行）、server.Task（运行句柄）、TaskDTO
// （JSON）、resolvedTaskStatus（推导）。它们各自维护一份状态字符串集合，于是
// "加一个终态"要改六处，其中三处还写错过（漏 policy_blocked）。
//
// 这里把**状态值本身**收敛成一份：持久化集合与派生集合都从这里取，SQL 的 CHECK
// 由 PersistedTaskStatuses 断言（见 task_status_test.go），前端联合类型也由测试
// 与它对齐。剩下的只是"用哪个值"的推导逻辑，那属于 server 层（resolvedTaskStatus）。

// TaskStatus 是任务的**持久化**状态（tasks.status 列的合法取值）。
type TaskStatus = string

const (
	TaskStatusCreated       TaskStatus = "created"
	TaskStatusRunning       TaskStatus = "running"
	TaskStatusPaused        TaskStatus = "paused"
	TaskStatusDone          TaskStatus = "done"
	TaskStatusFailed        TaskStatus = "failed"
	TaskStatusTimeout       TaskStatus = "timeout"
	TaskStatusPolicyBlocked TaskStatus = "policy_blocked"
)

// TaskStatusQueued 是**派生**状态：库里没有这个值（排队由 queued 布尔列 + queued_at 表达），
// 但 API 与前端需要把它当成一个状态展示与筛选。它属于 DerivedTaskStatuses，不属于
// PersistedTaskStatuses——两者混用正是"库里存不下 queued"那个坑的来源。
const TaskStatusQueued TaskStatus = "queued"

// PersistedTaskStatuses 是 tasks.status 能存的所有值（与 schema 的 CHECK 一致）。
var PersistedTaskStatuses = []TaskStatus{
	TaskStatusCreated, TaskStatusRunning, TaskStatusPaused,
	TaskStatusDone, TaskStatusFailed, TaskStatusTimeout, TaskStatusPolicyBlocked,
}

// DerivedTaskStatuses 是 API 可能返回的全部状态 = 持久化集合 + 派生的 queued。
var DerivedTaskStatuses = append(append([]TaskStatus{}, PersistedTaskStatuses...), TaskStatusQueued)

// IsPersistedTaskStatus 报告 v 是否能直接写进 tasks.status。
func IsPersistedTaskStatus(v string) bool {
	for _, s := range PersistedTaskStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// IsDerivedTaskStatus 报告 v 是否可能出现在 API 的 status 字段里。
func IsDerivedTaskStatus(v string) bool {
	for _, s := range DerivedTaskStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// TaskStatusInput 是推导任务状态所需的全部输入（都是值，不引用运行时对象）。
//
// 为什么要这么一个结构体：状态推导此前只存在于 server.resolvedTaskStatus 里，
// 而 db 侧的 SQL（findings 分组）另写了一份近似实现——两份推导迟早会漂移。
// 把"输入"显式列出来之后，纯函数 DeriveTaskStatus 可以被两侧共用，也可以被
// 表驱动测试穷举，不需要起一个 Server。
type TaskStatusInput struct {
	// Persisted 是 tasks.status 列的值。
	Persisted string
	Queued    bool
	// Paused 包含"库里的 paused 列"与"引擎内存态的暂停"——两者任一为真即暂停。
	Paused bool
	// Started 表示引擎已经为该任务拉起循环。
	Started bool
	// Ready 表示该任务当前能解析出可运行的 LLM（才可能真的在跑）。
	Ready bool
	// LLMFailoverExhausted 表示任务级 LLM 链已耗尽：这属于执行依赖，不是生命周期状态，
	// 任务仍算 running，用户可以就地改配置。
	LLMFailoverExhausted bool
}

// DeriveTaskStatus 是任务状态的**唯一**推导实现。
//
// 优先级：终态 > 排队 > 暂停 > 运行 > 未开始。
// 注意 queued 只存在于推导结果里（库里没有这个值，见 PersistedTaskStatuses）。
func DeriveTaskStatus(in TaskStatusInput) string {
	switch {
	case IsTerminal(in.Persisted):
		return in.Persisted
	case in.Queued:
		return TaskStatusQueued
	case in.Paused:
		return TaskStatusPaused
	case in.LLMFailoverExhausted && in.Started:
		return TaskStatusRunning
	case in.Started && in.Ready:
		return TaskStatusRunning
	default:
		return TaskStatusCreated
	}
}
