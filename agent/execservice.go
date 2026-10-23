package agent

import (
	"context"
	"time"

	"github.com/lin9527ya/blade/db"
)

// P13 执行服务接口：长任务（扫描/爆破/批量枚举）交给 host 侧托管——进程独立存活、
// 输出落盘、状态进台账。agent 包只依赖这个窄接口（实现见 server/execution.go），
// 与 FindingRecorder 等既有注入点同一模式。

// ExecStatus 是一次执行的快照（跨层传输用；字段与 db.ToolExecution 对齐）。
type ExecStatus struct {
	ID          int64  `json:"execution_id"`
	Status      string `json:"status"` // queued|running|completed|failed|cancelled|hard_timeout|orphaned
	IntentID    int64  `json:"intent_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Command     string `json:"command,omitempty"`
	ExitCode    int    `json:"exit_code"` // -1 = 未记录
	OutputPath  string `json:"output_path"`
	OutputBytes int64  `json:"output_bytes"`
	// Head 是输出文件的**头部**（前 8KB）。扫描类命令（nmap/ffuf/nuclei）的关键
	// 信息往往在最前面，而 Tail 只给得到末尾——续跑时只看尾巴会漏掉命中行。
	Head       string `json:"output_head,omitempty"`
	Tail       string `json:"partial_tail,omitempty"`
	CancelNote string `json:"cancel_note,omitempty"`
	Error      string `json:"error,omitempty"`
	// Limits 是本次执行的资源限额快照（P24）：请求了什么、哪些真的生效。空 = 没设限额。
	Limits string `json:"limits,omitempty"`
}

// ExecActive 报告执行是否仍在跑（工具层措辞与收尾门共用同一口径）。
func ExecActive(status string) bool { return status == "queued" || status == "running" }

// ExecService 由 host 注入；未注入时四个执行工具返回可读的拒绝（而非静默消失）。
type ExecService interface {
	// Submit 起一条后台执行并立即返回初始快照（含 execution_id 与输出绝对路径）。
	// timeout>0 为硬超时；到点杀进程组并记 hard_timeout。
	Submit(ctx context.Context, taskID string, intentID int64, worker, command, desc string, timeout time.Duration) (ExecStatus, error)
	// Wait 有界等待：到 timeout 仍未终态时返回当前 running 快照，且**不返回错误**
	// （超时是一次完成的观察，不是失败）。
	Wait(ctx context.Context, id int64, timeout time.Duration) (ExecStatus, error)
	// Get 读当前快照（含部分输出尾部）。
	Get(ctx context.Context, id int64) (ExecStatus, error)
	// Cancel 取消一次执行（杀进程组）并把理由写进台账——理由会回传给模型。
	Cancel(ctx context.Context, id int64, reason string) (bool, error)
	// CountRanForIntent 数本意图名下"真跑过"的执行记录数（终态行；queued/running 不算）：
	// negative 证据门用它判定"这个方向到底有没有真实探针证据"。
	CountRanForIntent(intentID int64) (int64, error)
	// ListForIntent 返回本意图名下的执行台账（最新在前）：get_worker_output 用它回答
	// "这条 work 到底跑过什么、退出码多少、输出落在哪个文件"，不必重跑。
	ListForIntent(intentID int64) ([]*db.ToolExecution, error)
}
