package db

import (
	"context"
	"fmt"
	"time"
)

// 数据保留：按时间清理「只增不减」的表。
//
// 契约与 server/storage_cleanup.go 的文件清理**刻意同构**，不另发明一套：
//   - 保留天数为 0/未设 = **不清理**。装上平台不会因为你没配就自己删东西。
//   - 「过期」用行自己的时间戳（ts / created_at）判定，不是启发式猜测。
//   - **非终态任务的记录一律不删**：调用方把受保护的任务/探索 id 传进来，这里
//     只负责「不碰它们」，不去猜哪些任务在跑（那是 DB 里的事实，不是猜）。
//   - 删除**分批**执行，避免一条 DELETE 长时间持锁把在线写入堵住。
//
// 本文件只管「按给定条件删」，不读设置、不做策略判断——策略只在 server 层有一份。

// defaultRetentionBatch 是一次 DELETE 删除的行数上限。分批的理由是锁：一条删
// 百万行的 DELETE 会长时间持有行锁、撑大 WAL，期间在线写入（activity 是每个
// agent 步骤一行、llm_usage 是每次 LLM 调用一行）会被堵住。2000 是「删得动、
// 又不至于锁太久」的取值。
const defaultRetentionBatch = 2000

// RetentionTable 标识一张可清理的表。取值只来自本文件的常量——表名要拼进 SQL，
// 绝不能来自请求或设置。
type RetentionTable string

const (
	RetentionActivity   RetentionTable = "activity"
	RetentionLLMUsage   RetentionTable = "llm_usage"
	RetentionServerLogs RetentionTable = "server_logs"
	RetentionLLMRecords RetentionTable = "llm_records"
)

// RetentionStat 是一张表在一次体检里的计数与体积（试算：只读）。
type RetentionStat struct {
	Rows        int64 `json:"rows"`
	ExpiredRows int64 `json:"expired_rows"`
	// ExpiredRowsAll 是**不考虑保护**、单看时间就过期的行数。它与 ExpiredRows 的
	// 差额就是「被非终态任务挡下的行」——页面上「受保护」显示的就是这个，让人看得见
	// 「明明有一堆旧数据却删不掉」的原因，而不是一个说不清的数字。
	ExpiredRowsAll int64 `json:"expired_rows_all"`
	// Bytes 是整表磁盘占用（含索引与 TOAST），与文件分类的「占用」同口径。
	Bytes int64 `json:"bytes"`
	// ExpiredBytes 是过期行的**行载荷**之和，不含索引/TOAST 开销——所以它总小于
	// Bytes。索引空间要等 vacuum 才真正还回去，如实呈现这个差额，不虚报。
	ExpiredBytes int64 `json:"expired_bytes"`
}

// retentionFilter 返回一张表「过期且不受保护」的行条件与参数。
// 列名与条件都来自本文件的常量，不接受外部输入。
func retentionFilter(
	table RetentionTable, cutoff time.Time,
	protectedTasks []string, protectedExplorations []int64,
) (timeCol, where string, args []any, err error) {
	// nil 切片会被 pgx 当成 SQL NULL，而 `id = ANY(NULL)` 求值是 NULL（不是 false），
	// 于是 `NOT (...)` 也是 NULL，所有行都不匹配——清理会静默地什么都不做。
	// 统一归一成空数组：`= ANY('{}')` 才是 false，`NOT false` 才是 true。
	if protectedTasks == nil {
		protectedTasks = []string{}
	}
	if protectedExplorations == nil {
		protectedExplorations = []int64{}
	}
	switch table {
	case RetentionActivity:
		// activity 按 exploration_id 归属任务，受保护的是「非终态任务的探索」。
		return "created_at",
			"created_at < $1 AND NOT (exploration_id = ANY($2::bigint[]))",
			[]any{cutoff, protectedExplorations}, nil
	case RetentionLLMUsage, RetentionLLMRecords:
		// task_id 可空：空的是「不属于任何任务的调用」（旁路提问、非任务 agent）。
		// 它们没有任务可保护，应当可清——所以写成 `IS NULL OR NOT (...)`。
		// 漏掉前半句会让这些行**永远删不掉**（NULL 参与比较得到 NULL，不是 true）。
		return "ts",
			"ts < $1 AND (task_id IS NULL OR NOT (task_id = ANY($2::text[])))",
			[]any{cutoff, protectedTasks}, nil
	case RetentionServerLogs:
		// 服务端日志不归属任务，没有需要保护的集合。
		return "created_at", "created_at < $1", []any{cutoff}, nil
	}
	return "", "", nil, fmt.Errorf("未知的清理目标表 %q", table)
}

// RetentionStats 统计一张表的总行数/过期行数/占用/可回收载荷（试算，只读，不动数据）。
func (d *DB) RetentionStats(
	ctx context.Context, table RetentionTable, cutoff time.Time,
	protectedTasks []string, protectedExplorations []int64,
) (RetentionStat, error) {
	timeCol, where, args, err := retentionFilter(table, cutoff, protectedTasks, protectedExplorations)
	if err != nil {
		return RetentionStat{}, err
	}
	var st RetentionStat
	// 三个子查询共用同一段 where（$1/$2 复用）。过期行数走时间索引；
	// pg_column_size 只累加过期行的载荷，不扫全表。最后一个是"不考虑保护"的
	// 过期行数（只按时间列过滤），用来算出被保护挡下的行数。
	q := fmt.Sprintf(`
SELECT (SELECT count(*) FROM %[1]s),
       (SELECT count(*) FROM %[1]s WHERE %[2]s),
       (SELECT COALESCE(sum(pg_column_size(x.*)),0) FROM %[1]s x WHERE %[2]s),
       (SELECT count(*) FROM %[1]s WHERE %[3]s < $1)`,
		table, where, timeCol)
	if err := d.QueryRowContext(ctx, q, args...).Scan(
		&st.Rows, &st.ExpiredRows, &st.ExpiredBytes, &st.ExpiredRowsAll); err != nil {
		return RetentionStat{}, fmt.Errorf("统计 %s: %w", table, err)
	}
	// pg_total_relation_size 是 O(1) 的元数据查询，不扫表。
	if err := d.QueryRowContext(ctx, `SELECT pg_total_relation_size($1)`, string(table)).Scan(&st.Bytes); err != nil {
		return RetentionStat{}, fmt.Errorf("统计 %s 占用: %w", table, err)
	}
	return st, nil
}

// LLMRecordPayloadStats 是 llm_records **第一级回收**（清空载荷）自己的视图：
// 统计「仍带着四个大 TEXT 中任意一个」的行数与它们占的载荷字节，分过期/未过期。
//
// 与 RetentionStats 分开是因为两者口径不同：RetentionStats 看的是整表行数（第二级
// 整行删的依据），这里看的是「还有载荷可回收的行」——一行被清空载荷后仍然存在，
// 但不再计入这里。页面上两个数字各自独立，不互相减。
func (d *DB) LLMRecordPayloadStats(
	ctx context.Context, cutoff time.Time, protectedTasks []string,
) (RetentionStat, error) {
	if protectedTasks == nil {
		protectedTasks = []string{}
	}
	const hasBody = `(request_body IS NOT NULL OR response_body IS NOT NULL
           OR raw_request IS NOT NULL OR raw_response IS NOT NULL)`
	var st RetentionStat
	q := fmt.Sprintf(`
SELECT (SELECT count(*) FROM llm_records WHERE %[1]s),
       (SELECT count(*) FROM llm_records WHERE %[1]s AND ts < $1 AND (task_id IS NULL OR NOT (task_id = ANY($2::text[])))),
       (SELECT COALESCE(sum(pg_column_size(x.request_body) + pg_column_size(x.response_body)
                          + pg_column_size(x.raw_request) + pg_column_size(x.raw_response)),0)
          FROM llm_records x WHERE %[1]s AND ts < $1 AND (task_id IS NULL OR NOT (task_id = ANY($2::text[]))))`,
		hasBody)
	if err := d.QueryRowContext(ctx, q, cutoff, protectedTasks).Scan(
		&st.Rows, &st.ExpiredRows, &st.ExpiredBytes); err != nil {
		return RetentionStat{}, fmt.Errorf("统计 llm_records 载荷: %w", err)
	}
	st.ExpiredRowsAll = st.ExpiredRows
	return st, nil
}

// purgeBatched 反复执行「删一批」直到没有可删的行，返回删除总数。
// table/timeCol/where 都来自本文件的常量；batch 是本文件的取值。
func (d *DB) purgeBatched(ctx context.Context, table, timeCol, where string, args []any, batch int) (int64, error) {
	if batch <= 0 {
		batch = defaultRetentionBatch
	}
	var total int64
	for {
		// 按时间列排序取最旧的一批：与时间索引方向一致，避免每批都重新扫。
		q := fmt.Sprintf(
			`DELETE FROM %[1]s WHERE id IN (SELECT id FROM %[1]s WHERE %[2]s ORDER BY %[3]s LIMIT %[4]d)`,
			table, where, timeCol, batch)
		res, err := d.ExecContext(ctx, q, args...)
		if err != nil {
			return total, fmt.Errorf("清理 %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("清理 %s: %w", table, err)
		}
		total += n
		if n < int64(batch) {
			return total, nil
		}
	}
}

// PurgeActivityBefore 删除早于 cutoff、且不属于受保护探索的 activity 行。
func (d *DB) PurgeActivityBefore(
	ctx context.Context, cutoff time.Time, protectedExplorations []int64, batch int,
) (int64, error) {
	timeCol, where, args, err := retentionFilter(RetentionActivity, cutoff, nil, protectedExplorations)
	if err != nil {
		return 0, err
	}
	return d.purgeBatched(ctx, string(RetentionActivity), timeCol, where, args, batch)
}

// PurgeLLMUsageBefore 删除早于 cutoff、且不属于受保护任务的 llm_usage 行。
func (d *DB) PurgeLLMUsageBefore(
	ctx context.Context, cutoff time.Time, protectedTasks []string, batch int,
) (int64, error) {
	timeCol, where, args, err := retentionFilter(RetentionLLMUsage, cutoff, protectedTasks, nil)
	if err != nil {
		return 0, err
	}
	return d.purgeBatched(ctx, string(RetentionLLMUsage), timeCol, where, args, batch)
}

// PurgeServerLogsBefore 删除早于 cutoff 的 server_logs 行（不归属任务）。
func (d *DB) PurgeServerLogsBefore(ctx context.Context, cutoff time.Time, batch int) (int64, error) {
	timeCol, where, args, err := retentionFilter(RetentionServerLogs, cutoff, nil, nil)
	if err != nil {
		return 0, err
	}
	return d.purgeBatched(ctx, string(RetentionServerLogs), timeCol, where, args, batch)
}

// PurgeLLMRecordsBefore 删除早于 cutoff、且不属于受保护任务的 llm_records 行。
func (d *DB) PurgeLLMRecordsBefore(
	ctx context.Context, cutoff time.Time, protectedTasks []string, batch int,
) (int64, error) {
	timeCol, where, args, err := retentionFilter(RetentionLLMRecords, cutoff, protectedTasks, nil)
	if err != nil {
		return 0, err
	}
	return d.purgeBatched(ctx, string(RetentionLLMRecords), timeCol, where, args, batch)
}

// StripLLMRecordBodiesBefore 把早于 cutoff 的 llm_records 行的四个大 TEXT 字段置空，
// **保留元数据行**（模型/延迟/token/状态仍可用于统计与选择器），只回收载荷。
//
// 这是 llm_records 的第一级回收：那张表的价值主要在元数据，request_body /
// response_body / raw_request / raw_response 是排查用的重载荷，往往是行体积的绝大部分。
// 比它更早的行再由 PurgeLLMRecordsBefore 整行删掉（第二级）。
func (d *DB) StripLLMRecordBodiesBefore(
	ctx context.Context, cutoff time.Time, protectedTasks []string, batch int,
) (int64, error) {
	if batch <= 0 {
		batch = defaultRetentionBatch
	}
	_, where, args, err := retentionFilter(RetentionLLMRecords, cutoff, protectedTasks, nil)
	if err != nil {
		return 0, err
	}
	var total int64
	for {
		// 「至少还有一个大字段非空」这个条件不能省：UPDATE 即使把值写成相同的值
		// 也算影响行数，没有它就会一直匹配同一批已清空的行，循环永不结束。
		q := fmt.Sprintf(`
UPDATE llm_records SET request_body=NULL, response_body=NULL, raw_request=NULL, raw_response=NULL
WHERE id IN (
    SELECT id FROM llm_records
    WHERE %[1]s
      AND (request_body IS NOT NULL OR response_body IS NOT NULL
           OR raw_request IS NOT NULL OR raw_response IS NOT NULL)
    ORDER BY ts LIMIT %[2]d
)`, where, batch)
		res, err := d.ExecContext(ctx, q, args...)
		if err != nil {
			return total, fmt.Errorf("清空 llm_records 载荷: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("清空 llm_records 载荷: %w", err)
		}
		total += n
		if n < int64(batch) {
			return total, nil
		}
	}
}
