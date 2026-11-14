package db

// FindingReportPatrol is one confirmed finding whose detailed report is still
// empty and which a patrol run may hand to the reporter agent.
type FindingReportPatrol struct {
	FindingID int64
	NodeID    int64
	TaskID    int64
	Name      string
	Severity  string
}

// ClaimFindingReportPatrol atomically leases up to limit findings that need a
// detailed report. A row is eligible only when it is confirmed (or high
// confidence / reproduced|fixed by a retest), has an exploration node and task,
// is older than ten minutes, and has not exhausted its two attempts. The lease
// increments the attempt counter immediately, so overlapping ticks/instances
// (and a reporter failure loop) cannot hot-loop the same finding.
func (d *DB) ClaimFindingReportPatrol(limit int) ([]FindingReportPatrol, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := d.Query(`
WITH picked AS (
    SELECT f.id
      FROM findings f
     WHERE COALESCE(f.report, '') = ''
       AND f.node_id IS NOT NULL
       AND f.task_id IS NOT NULL
       AND COALESCE(f.status, 'pending') NOT IN ('retracted','false_positive','duplicate','ruled_out','ignored')
       AND f.created_at < now() - interval '10 minutes'
       AND f.report_patrol_attempts < 2
       AND (f.report_patrol_at IS NULL OR f.report_patrol_at < now() - interval '30 minutes')
       AND (
            COALESCE(f.confidence, '') = 'high'
            OR COALESCE(f.status, '') = 'confirmed'
            OR EXISTS (
                SELECT 1 FROM finding_retests r
                 WHERE r.finding_id = f.id AND r.verdict IN ('reproduced','fixed')
            )
       )
     ORDER BY f.id
     LIMIT $1
     FOR UPDATE SKIP LOCKED
)
UPDATE findings f
   SET report_patrol_attempts = f.report_patrol_attempts + 1,
       report_patrol_at = now()
  FROM picked
 WHERE f.id = picked.id
RETURNING f.id, f.node_id, f.task_id, COALESCE(f.name, ''), COALESCE(f.severity, '')`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FindingReportPatrol
	for rows.Next() {
		var p FindingReportPatrol
		if err := rows.Scan(&p.FindingID, &p.NodeID, &p.TaskID, &p.Name, &p.Severity); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
