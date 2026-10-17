package db

import "time"

// P43 凭据尝试台账（表结构见 schema.sql）。
//
// 这张表回答两个问题，而且两个都要答得准：
//   - 护栏问：「本任务对 host 的 account，最近窗口里试了几次？」→ CountCredentialAttempts
//   - 交付物问：「这次作业到底动过谁的账号？」→ ListCredentialAttempts
//
// 刻意**不建外键**到 tasks：它是一份作业证据，任务被删不该把"我们动过哪个账号"一起抹掉。
// 孤儿行在这里是特性，不是缺陷。

// CredentialAttempt 是一次对某个目标的某个账号的凭据尝试。
type CredentialAttempt struct {
	ID      int64  `json:"id"`
	TaskID  int64  `json:"task_id"`
	Host    string `json:"host"`
	Account string `json:"account,omitempty"`
	Tool    string `json:"tool,omitempty"`
	// Command 是触发这次记录的**命令原文**。它是证据：审批卡上要能一眼看到自己敲了什么，
	// 报告里也要能核对"这次尝试是不是真在打这个账号"。
	Command string `json:"command,omitempty"`
	// Spray 为真 = 这条命令本身就是成批尝试（爆破器 / 带口令表）。
	// 它不参与计数——一次调用可能是几百次真实尝试，计数没有意义，它单独就构成越界。
	Spray bool      `json:"spray,omitempty"`
	At    time.Time `json:"at"`
}

// RecordCredentialAttempt 记一次凭据尝试。护栏与台账共用这一条写入路径。
func (d *DB) RecordCredentialAttempt(taskID int64, host, account, tool, command string, spray bool) error {
	_, err := d.Exec(`
INSERT INTO credential_attempts(task_id, host, account, tool, command, spray)
VALUES ($1,$2,$3,$4,$5,$6)`, taskID, host, account, tool, command, spray)
	return err
}

// CountCredentialAttempts 数窗口内对 (task, host, account) 的**非成批**尝试次数。
//
// 成批尝试（spray）不计数，理由是计数对它们没有意义（见 CredentialAttempt.Spray）——
// 判定侧对成批是另一条分支，不靠这个数。
//
// account 为空串时数的是"该主机上没抽出账号的那些尝试"，与抽得出账号的分开计：
// 把两者混在一起会让"对 svc_backup 试了 2 次"被别的匿名尝试顶掉预算。
func (d *DB) CountCredentialAttempts(taskID int64, host, account string, since time.Time) (int, error) {
	var n int
	err := d.QueryRow(`
SELECT count(*) FROM credential_attempts
 WHERE task_id=$1 AND host=$2 AND account=$3 AND spray=false AND at >= $4`,
		taskID, host, account, since).Scan(&n)
	return n, err
}

// ListCredentialAttempts 列出一个任务的全部凭据尝试（报告用，按时间正序）。
func (d *DB) ListCredentialAttempts(taskID int64) ([]CredentialAttempt, error) {
	rows, err := d.Query(`
SELECT id, task_id, host, account, tool, command, spray, at
  FROM credential_attempts WHERE task_id=$1 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CredentialAttempt
	for rows.Next() {
		var a CredentialAttempt
		if err := rows.Scan(&a.ID, &a.TaskID, &a.Host, &a.Account, &a.Tool, &a.Command, &a.Spray, &a.At); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
