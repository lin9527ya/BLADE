package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// CompleteTaskArchive performs the hot-store compaction only after the external
// package has been fully written and checksummed. The task/exploration rows remain
// as minimal ID stubs; all heavyweight task-owned rows move into the package.
func (d *DB) CompleteTaskArchive(
	archiveID int64,
	snapshot *TaskArchiveSnapshot,
	archivePath, sha256 string,
	originalSize, compressedSize int64,
) error {
	if snapshot == nil || snapshot.FormatVersion != TaskArchiveFormatVersion {
		return ErrTaskArchiveFormatMismatch
	}
	countsRaw, err := json.Marshal(snapshot.DataCounts)
	if err != nil {
		return err
	}
	statsRaw, err := json.Marshal(snapshot.AggregateStats)
	if err != nil {
		return err
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	var taskID, expID int64
	var state string
	if err := tx.QueryRow(`SELECT archive.task_id,task.exploration_id,archive.state
FROM task_archives archive JOIN tasks task ON task.id=archive.task_id
WHERE archive.id=$1 FOR UPDATE OF archive,task`, archiveID).Scan(&taskID, &expID, &state); err != nil {
		return err
	}
	if state != Archiving || taskID != snapshot.TaskID || expID != snapshot.ExplorationID {
		return fmt.Errorf("%w: archive snapshot identity/state mismatch", ErrTaskArchiveState)
	}
	var dependent int64
	err = tx.QueryRow(`SELECT child.id FROM task_relations relation
JOIN tasks child ON child.id=relation.task_id AND child.deleted_at IS NULL
WHERE relation.source_task_id=$1 LIMIT 1`, taskID).Scan(&dependent)
	if err == nil {
		return fmt.Errorf("%w: task %d", ErrTaskArchiveDependent, dependent)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Prevent task/asset ownership from changing while exclusivity is rechecked.
	if _, err := tx.Exec(`LOCK TABLE assets, exploration_anchors IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	if _, err := tx.Exec(`LOCK TABLE findings, exploration_nodes, finding_comments, finding_retests,
finding_case_members, finding_case_blocks, credential_checks, main_sessions, completion_waivers,
task_planner_state, task_timeout_events
IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	if err := assertArchiveSnapshotComplete(tx, taskID, expID, snapshot); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM intercept_pending WHERE COALESCE(task_id,'')=$1`, strconv.FormatInt(taskID, 10)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM llm_records WHERE task_id=$1`, strconv.FormatInt(taskID, 10)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM llm_usage WHERE task_id=$1 OR exploration_id=$2`, strconv.FormatInt(taskID, 10), expID); err != nil {
		return err
	}
	for _, table := range []string{"skill_usage", "tool_usage"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE task_id=$1 OR exploration_id=$2`, taskID, expID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM side_question_sessions WHERE task_id=$1`, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM findings WHERE task_id=$1`, taskID); err != nil {
		return err
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`DELETE FROM task_relations WHERE task_id=$1 OR source_task_id=$1`, []any{taskID}},
		{`DELETE FROM task_asset_links WHERE task_id=$1`, []any{taskID}},
		{`DELETE FROM task_llm_profiles WHERE task_id=$1`, []any{taskID}},
		{`DELETE FROM task_scope WHERE task_id=$1`, []any{taskID}},
		{`DELETE FROM task_constraints WHERE exploration_id=$1`, []any{expID}},
		{`DELETE FROM activity WHERE exploration_id=$1`, []any{expID}},
		{`DELETE FROM main_sessions WHERE exploration_id=$1`, []any{expID}},
		{`DELETE FROM completion_waivers WHERE exploration_id=$1`, []any{expID}},
		{`DELETE FROM task_planner_state WHERE task_id=$1`, []any{taskID}},
		{`DELETE FROM task_timeout_events WHERE task_id=$1`, []any{taskID}},
		{`DELETE FROM exploration_nodes WHERE exploration_id=$1`, []any{expID}},
	} {
		if _, err := tx.Exec(statement.query, statement.args...); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE assets SET task_ids=array_remove(task_ids,$1) WHERE $1=ANY(task_ids)`, taskID); err != nil {
		return err
	}
	if len(snapshot.ExclusiveAssetIDs) > 0 {
		if _, err := tx.Exec(`DELETE FROM assets asset
WHERE asset.id=ANY($2::bigint[]) AND asset.company_id IS NULL
AND NOT EXISTS (SELECT 1 FROM tasks task WHERE task.id<>$1 AND task.deleted_at IS NULL AND task.id=ANY(asset.task_ids))
AND NOT EXISTS (
 SELECT 1 FROM exploration_anchors anchor JOIN exploration_nodes node ON node.id=anchor.node_id
 JOIN tasks task ON task.exploration_id=node.exploration_id
 WHERE anchor.asset_id=asset.id AND task.id<>$1 AND task.deleted_at IS NULL
)`, taskID, snapshot.ExclusiveAssetIDs); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE explorations SET description='',goal='',status='open' WHERE id=$1`, expID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE tasks SET
 name='',category_id=NULL,description='',goal='',paused=true,queued=false,queued_at=NULL,queue_mode='',
 llm_profile_id=NULL,active_llm_profile_id=NULL,llm_chain_revision=llm_chain_revision+1,
 company_id=NULL,parent_ref=NULL,timeout_seconds=0,coverage_enabled=true,asset_scope_policy='exact',pinned_at=NULL,
 first_run_at=NULL,deadline_at=NULL,archived_at=now(),deleted_at=now()
WHERE id=$1`, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE task_archives SET
	 state=$2,phase='ready',progress=100,error='',archive_path=$3,sha256=$4,
	 original_size=$5,compressed_size=$6,data_counts=$7,aggregate_stats=$8,
	 format_version=$9,archived_at=now(),warnings='[]'
WHERE id=$1`, archiveID, ArchiveReady, archivePath, sha256, originalSize, compressedSize,
		string(countsRaw), string(statsRaw), snapshot.FormatVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// RestoreTaskArchive restores PostgreSQL rows from a verified manifest. It is
// idempotent for accounting/traffic retry scenarios and returns non-fatal
// warnings for global objects that intentionally are not recreated.
func (d *DB) RestoreTaskArchive(archiveID int64, snapshot *TaskArchiveSnapshot, remainingTimeoutSeconds int64) ([]string, error) {
	return d.restoreTaskArchive(archiveID, snapshot, remainingTimeoutSeconds, nil)
}

// RestoreTaskArchiveWithLLMRecords restores a v2 package whose heavyweight LLM
// record history is stored as a sequence of JSON objects outside manifest.json.
func (d *DB) RestoreTaskArchiveWithLLMRecords(
	archiveID int64,
	snapshot *TaskArchiveSnapshot,
	remainingTimeoutSeconds int64,
	llmRecords io.Reader,
) ([]string, error) {
	if llmRecords == nil {
		return nil, errors.New("nil streamed LLM record reader")
	}
	return d.restoreTaskArchive(archiveID, snapshot, remainingTimeoutSeconds, llmRecords)
}

func (d *DB) restoreTaskArchive(
	archiveID int64,
	snapshot *TaskArchiveSnapshot,
	remainingTimeoutSeconds int64,
	llmRecords io.Reader,
) ([]string, error) {
	if snapshot == nil || !IsTaskArchiveFormatSupported(snapshot.FormatVersion) {
		return nil, ErrTaskArchiveFormatMismatch
	}
	streamedLLMRecords := snapshot.StreamedTables["llm_records"]
	if (len(snapshot.StreamedTables) > 0 && snapshot.FormatVersion < 2) ||
		len(snapshot.StreamedTables) > 1 ||
		(len(snapshot.StreamedTables) == 1 && streamedLLMRecords != TaskArchiveLLMRecordsPath) {
		return nil, fmt.Errorf("%w: unsupported streamed table metadata", ErrTaskArchiveFormatMismatch)
	}
	if streamedLLMRecords != "" && llmRecords == nil {
		return nil, fmt.Errorf("%w: streamed LLM records are missing", ErrTaskArchiveFormatMismatch)
	}
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return nil, err
	}
	var taskID, expID int64
	var state string
	if err := tx.QueryRow(`SELECT archive.task_id,task.exploration_id,archive.state
FROM task_archives archive JOIN tasks task ON task.id=archive.task_id
WHERE archive.id=$1 FOR UPDATE OF archive,task`, archiveID).Scan(&taskID, &expID, &state); err != nil {
		return nil, err
	}
	if state != Restoring || taskID != snapshot.TaskID || expID != snapshot.ExplorationID {
		return nil, fmt.Errorf("%w: restore snapshot identity/state mismatch", ErrTaskArchiveState)
	}
	warnings := []string{}
	assetMap, assetWarnings, err := restoreArchiveAssets(tx, taskID, snapshot.Tables["assets"])
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, assetWarnings...)

	// The task row exists as an archived stub. Restore its global references only
	// when the current instance still owns them; never recreate categories/profiles.
	taskRows, err := decodeArchiveRows(snapshot.Tables["tasks"])
	if err != nil || len(taskRows) != 1 {
		return nil, fmt.Errorf("restore task row: expected one row: %w", err)
	}
	taskRow := taskRows[0]
	categoryID, _ := jsonInt64(taskRow["category_id"])
	if categoryID > 0 {
		exists, err := rowExists(tx, "task_categories", categoryID)
		if err != nil {
			return nil, err
		}
		if !exists {
			taskRow["category_id"] = nil
			warnings = append(warnings, fmt.Sprintf("任务分类 %d 已删除，已恢复为未分类", categoryID))
		}
	}
	companyID, _ := jsonInt64(taskRow["company_id"])
	if companyID > 0 {
		exists, err := rowExists(tx, "companies", companyID)
		if err != nil {
			return nil, err
		}
		if !exists {
			taskRow["company_id"] = nil
			warnings = append(warnings, fmt.Sprintf("任务企业 %d 已删除，企业关联已跳过", companyID))
		}
	}
	for _, key := range []string{"llm_profile_id", "active_llm_profile_id"} {
		profileID, _ := jsonInt64(taskRow[key])
		if profileID <= 0 {
			continue
		}
		exists, err := rowExists(tx, "llm_profiles", profileID)
		if err != nil {
			return nil, err
		}
		if !exists {
			taskRow[key] = nil
			warnings = append(warnings, fmt.Sprintf("LLM 配置 %d 已删除，已从任务配置中移除", profileID))
		}
	}
	if err := restoreExplorationStub(tx, snapshot.Tables["explorations"], expID); err != nil {
		return nil, err
	}
	if err := restoreTaskStub(tx, taskRow, taskID, remainingTimeoutSeconds); err != nil {
		return nil, err
	}

	remappedTables, err := remapArchiveAssetReferences(snapshot.Tables, assetMap)
	if err != nil {
		return nil, err
	}
	remappedTables["findings"], err = normalizeArchivedFindingVersions(remappedTables["findings"])
	if err != nil {
		return nil, err
	}
	// Insert graph rows in foreign-key order. The archived stub has no graph rows,
	// so an ID conflict signals external corruption and must stop the restore.
	for _, table := range []string{"exploration_nodes", "exploration_edges", "exploration_anchors", "task_constraints", "activity"} {
		if err := insertArchiveRows(tx, table, remappedTables[table]); err != nil {
			return nil, fmt.Errorf("restore %s: %w", table, err)
		}
	}
	if warning, err := restoreTaskRelations(tx, taskID, remappedTables["task_relations"]); err != nil {
		return nil, err
	} else {
		warnings = append(warnings, warning...)
	}
	for _, table := range []string{"side_question_sessions", "side_question_requests"} {
		if err := insertArchiveRows(tx, table, remappedTables[table]); err != nil {
			return nil, fmt.Errorf("restore %s: %w", table, err)
		}
	}
	if warning, err := restoreTaskScopes(tx, remappedTables["task_scope"]); err != nil {
		return nil, err
	} else {
		warnings = append(warnings, warning...)
	}
	if warning, err := restoreTaskLLMProfiles(tx, remappedTables["task_llm_profiles"]); err != nil {
		return nil, err
	} else {
		warnings = append(warnings, warning...)
	}
	for _, table := range []string{"task_asset_links", "findings"} {
		if err := insertArchiveRows(tx, table, remappedTables[table]); err != nil {
			return nil, fmt.Errorf("restore %s: %w", table, err)
		}
	}
	if findingWarnings, err := restoreFindingChildrenTx(tx, remappedTables); err != nil {
		return nil, fmt.Errorf("restore finding children: %w", err)
	} else {
		warnings = append(warnings, findingWarnings...)
	}
	for _, table := range []string{"credential_checks", "main_sessions", "completion_waivers", "task_planner_state", "task_timeout_events"} {
		if err := insertArchiveRows(tx, table, remappedTables[table]); err != nil {
			return nil, fmt.Errorf("restore %s: %w", table, err)
		}
	}
	if err := restoreFindingTrafficTx(tx, snapshot); err != nil {
		return nil, fmt.Errorf("restore finding traffic: %w", err)
	}
	if streamedLLMRecords != "" {
		count, err := insertArchiveJSONSequenceRows(tx, "llm_records", llmRecords)
		if err != nil {
			return nil, fmt.Errorf("restore llm_records: %w", err)
		}
		if expected := snapshot.DataCounts["llm_records"]; count != expected {
			return nil, fmt.Errorf("restore llm_records: row count %d does not match manifest %d", count, expected)
		}
	} else if err := insertArchiveRows(tx, "llm_records", remappedTables["llm_records"]); err != nil {
		return nil, fmt.Errorf("restore llm_records: %w", err)
	}
	for _, table := range []string{"llm_usage", "skill_usage", "tool_usage"} {
		if err := insertArchiveRows(tx, table, remappedTables[table]); err != nil {
			return nil, fmt.Errorf("restore %s: %w", table, err)
		}
	}
	if err := restoreInterceptRows(tx, remappedTables["intercept_pending"]); err != nil {
		return nil, err
	}
	warningsRaw, _ := json.Marshal(warnings)
	if _, err := tx.Exec(`UPDATE task_archives SET warnings=$2,phase='database_restored',progress=85,error='' WHERE id=$1`, archiveID, string(warningsRaw)); err != nil {
		return nil, err
	}
	return warnings, tx.Commit()
}

func restoreExplorationStub(tx *sql.Tx, raw json.RawMessage, expID int64) error {
	_, err := tx.Exec(`UPDATE explorations current SET
 description=archived.description,goal=archived.goal,status=archived.status,
 created_at=archived.created_at,updated_at=archived.updated_at
FROM json_populate_record(NULL::explorations,$2::json) archived
WHERE current.id=$1 AND archived.id=$1`, expID, string(firstArchiveRow(raw)))
	return err
}

func restoreTaskStub(tx *sql.Tx, row map[string]any, taskID, remaining int64) error {
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE tasks current SET
 name=archived.name,category_id=archived.category_id,description=archived.description,goal=archived.goal,
 status=archived.status,paused=archived.paused,queued=false,queued_at=NULL,queue_mode='',
 llm_profile_id=archived.llm_profile_id,active_llm_profile_id=archived.active_llm_profile_id,
 llm_chain_revision=archived.llm_chain_revision,company_id=archived.company_id,parent_ref=archived.parent_ref,
 timeout_seconds=archived.timeout_seconds,plan_heartbeat_seconds=archived.plan_heartbeat_seconds,
 coverage_enabled=archived.coverage_enabled,asset_scope_policy=COALESCE(archived.asset_scope_policy,'exact'),
 pinned_at=archived.pinned_at,first_run_at=archived.first_run_at,
 deadline_at=CASE WHEN archived.paused AND $3>0 THEN now()+make_interval(secs=>$3::double precision)
                  ELSE archived.deadline_at END,
 deleted_at=NULL,archived_at=NULL,completed_at=archived.completed_at,
 created_at=archived.created_at,updated_at=archived.updated_at
FROM json_populate_record(NULL::tasks,$2::json) archived
WHERE current.id=$1 AND archived.id=$1`, taskID, string(raw), remaining)
	return err
}

func firstArchiveRow(raw json.RawMessage) json.RawMessage {
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil || len(rows) == 0 {
		return json.RawMessage("{}")
	}
	return rows[0]
}

func decodeArchiveRows(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var rows []map[string]any
	if err := decoder.Decode(&rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// rowExists reports whether a referenced global row still exists. A database
// error must not be mistaken for "row deleted": that would silently null out
// category/company/profile references during a transient failure.
func rowExists(tx *sql.Tx, table string, id int64) (bool, error) {
	switch table {
	case "task_categories", "llm_profiles", "companies", "tasks", "assets", "asset_sync_batches", "conversations", "finding_cases", "findings":
	default:
		return false, fmt.Errorf("unsupported archive reference table %q", table)
	}
	var exists bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1)`, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("check archived reference %s#%d: %w", table, id, err)
	}
	return exists, nil
}

func insertArchiveRows(tx *sql.Tx, table string, raw json.RawMessage) error {
	// json_populate_recordset inserts NULL for absent columns, bypassing SQL
	// defaults. Preserve compatibility with v3 archives predating side memory.
	if (table == "side_question_sessions" || table == "side_question_requests") && len(raw) > 0 {
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return err
		}
		field := "memory"
		if table == "side_question_requests" {
			field = "context_info"
		}
		for _, row := range rows {
			if len(row[field]) == 0 || string(row[field]) == "null" {
				row[field] = json.RawMessage(`{}`)
			}
			if table == "side_question_requests" {
				var status string
				if rawStatus := row["status"]; len(rawStatus) > 0 {
					if err := json.Unmarshal(rawStatus, &status); err != nil {
						return err
					}
				}
				// 归档快照可能恰好落在回答进行中。恢复后没有进程继续写回答，
				// running 占位会被部分唯一索引永久拒绝该会话的新问题。
				if status == "running" {
					var sequence int64
					if rawSequence := row["sequence"]; len(rawSequence) > 0 && string(rawSequence) != "null" {
						if err := json.Unmarshal(rawSequence, &sequence); err != nil {
							return err
						}
					}
					row["status"] = json.RawMessage(`"interrupted"`)
					row["error"] = json.RawMessage(`"归档恢复：原回答已中断"`)
					row["sequence"] = json.RawMessage(strconv.FormatInt(sequence+1, 10))
				}
			}
		}
		var err error
		raw, err = json.Marshal(rows)
		if err != nil {
			return err
		}
	}
	allowed := map[string]bool{
		"exploration_nodes": true, "exploration_edges": true, "exploration_anchors": true,
		"task_constraints": true, "activity": true, "task_asset_links": true, "findings": true,
		"finding_comments": true, "finding_retests": true, "finding_case_members": true,
		"finding_case_blocks": true, "credential_checks": true, "main_sessions": true,
		"completion_waivers": true, "task_planner_state": true, "task_timeout_events": true,
		"llm_records": true, "llm_usage": true, "skill_usage": true, "tool_usage": true,
		"side_question_sessions": true, "side_question_requests": true,
	}
	if !allowed[table] {
		return fmt.Errorf("archive restore table %q is not allowed", table)
	}
	if rawRowCount(raw) == 0 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO `+table+` SELECT * FROM json_populate_recordset(NULL::`+table+`,$1::json)`, string(raw))
	return err
}

func insertArchiveJSONSequenceRows(tx *sql.Tx, table string, reader io.Reader) (int64, error) {
	if table != "llm_records" {
		return 0, fmt.Errorf("archive streamed restore table %q is not allowed", table)
	}
	statement, err := tx.Prepare(`INSERT INTO llm_records SELECT * FROM json_populate_record(NULL::llm_records,$1::json)`)
	if err != nil {
		return 0, err
	}
	defer statement.Close()
	decoder := json.NewDecoder(reader)
	var count int64
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return 0, err
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return 0, errors.New("streamed archive row must be a JSON object")
		}
		if _, err := statement.Exec(string(trimmed)); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

func restoreArchiveAssets(tx *sql.Tx, taskID int64, raw json.RawMessage) (map[int64]int64, []string, error) {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return nil, nil, err
	}
	mapping := make(map[int64]int64, len(rows))
	warnings := []string{}
	for _, row := range rows {
		oldID, ok := jsonInt64(row["id"])
		if !ok || oldID <= 0 {
			return nil, nil, errors.New("archived asset has invalid id")
		}
		if companyID, ok := jsonInt64(row["company_id"]); ok && companyID > 0 {
			exists, err := rowExists(tx, "companies", companyID)
			if err != nil {
				return nil, nil, err
			}
			if !exists {
				row["company_id"] = nil
				row["company_source"] = "explicit"
				warnings = append(warnings, fmt.Sprintf("资产 %d 的企业 %d 已删除，已恢复为未归属", oldID, companyID))
			}
		}
		if batchID, ok := jsonInt64(row["last_sync_batch_id"]); ok && batchID > 0 {
			exists, err := rowExists(tx, "asset_sync_batches", batchID)
			if err != nil {
				return nil, nil, err
			}
			if !exists {
				row["last_sync_batch_id"] = nil
				warnings = append(warnings, fmt.Sprintf("资产 %d 的同步批次 %d 已删除，已清空批次引用", oldID, batchID))
			}
		}
		// Archived rows were written verbatim by older versions; strip URL
		// userinfo before the natural-id lookup so a credential-bearing URL both
		// dedupes with its clean form and never comes back into the asset graph.
		if rawURL, ok := row["url"].(string); ok && rawURL != "" {
			row["url"] = normalizeURL(rawURL)
		}
		if existing, found, err := findArchiveAssetNaturalID(tx, row); err != nil {
			return nil, nil, err
		} else if found {
			mapping[oldID] = existing
			if _, err := tx.Exec(`UPDATE assets SET task_ids=CASE WHEN $1=ANY(task_ids) THEN task_ids ELSE array_append(task_ids,$1) END WHERE id=$2`, taskID, existing); err != nil {
				return nil, nil, err
			}
			continue
		}
		candidate := oldID
		assetIDTaken, err := rowExists(tx, "assets", oldID)
		if err != nil {
			return nil, nil, err
		}
		if assetIDTaken {
			if err := tx.QueryRow(`SELECT nextval(pg_get_serial_sequence('assets','id'))`).Scan(&candidate); err != nil {
				return nil, nil, err
			}
			row["id"] = candidate
			warnings = append(warnings, fmt.Sprintf("资产 ID %d 已被占用，恢复为 %d", oldID, candidate))
		}
		row["task_ids"] = mergeJSONTaskID(row["task_ids"], taskID)
		assetRaw, _ := json.Marshal(row)
		res, err := tx.Exec(`INSERT INTO assets SELECT * FROM json_populate_record(NULL::assets,$1::json) ON CONFLICT DO NOTHING`, string(assetRaw))
		if err != nil {
			return nil, nil, err
		}
		inserted, _ := res.RowsAffected()
		if inserted == 0 {
			existing, found, findErr := findArchiveAssetNaturalID(tx, row)
			if findErr != nil || !found {
				return nil, nil, errors.Join(findErr, fmt.Errorf("could not restore asset %d", oldID))
			}
			candidate = existing
			if _, err := tx.Exec(`UPDATE assets SET task_ids=CASE WHEN $1=ANY(task_ids) THEN task_ids ELSE array_append(task_ids,$1) END WHERE id=$2`, taskID, existing); err != nil {
				return nil, nil, err
			}
		}
		mapping[oldID] = candidate
	}
	return mapping, warnings, nil
}

func mergeJSONTaskID(value any, taskID int64) []int64 {
	out := []int64{}
	seen := map[int64]bool{}
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if id, ok := jsonInt64(item); ok && id > 0 && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	if !seen[taskID] {
		out = append(out, taskID)
	}
	return out
}

func findArchiveAssetNaturalID(tx *sql.Tx, row map[string]any) (int64, bool, error) {
	typeName, _ := row["type"].(string)
	stringValue := func(key string) string {
		value, _ := row[key].(string)
		return value
	}
	var query string
	var args []any
	switch typeName {
	case "root_domain":
		query, args = `SELECT id FROM assets WHERE type='root_domain' AND domain=$1`, []any{stringValue("domain")}
	case "ip":
		query, args = `SELECT id FROM assets WHERE type='ip' AND ip=$1`, []any{stringValue("ip")}
	case "subdomain":
		query, args = `SELECT id FROM assets WHERE type='subdomain' AND domain=$1 AND COALESCE(record_type,'')=$2`, []any{stringValue("domain"), stringValue("record_type")}
	case "app":
		if bundle := stringValue("bundle_id"); bundle != "" {
			query, args = `SELECT id FROM assets WHERE type='app' AND bundle_id=$1`, []any{bundle}
		} else {
			query, args = `SELECT id FROM assets WHERE type='app' AND bundle_id IS NULL AND app_name=$1`, []any{stringValue("app_name")}
		}
	case "service":
		if stringValue("service_type") == "http" {
			query, args = `SELECT id FROM assets WHERE type='service' AND service_type='http' AND url=$1`, []any{stringValue("url")}
		} else {
			port, _ := jsonInt64(row["port"])
			query, args = `SELECT id FROM assets WHERE type='service' AND service_type='other' AND COALESCE(domain,'')=$1 AND COALESCE(ip,'')=$2 AND port=$3 AND service_name=$4`, []any{stringValue("domain"), stringValue("ip"), port, stringValue("service_name")}
		}
	case "endpoint":
		query, args = `SELECT id FROM assets WHERE type='endpoint' AND url=$1 AND method=$2`, []any{stringValue("url"), stringValue("method")}
	default:
		return 0, false, fmt.Errorf("unsupported archived asset type %q", typeName)
	}
	var id int64
	err := tx.QueryRow(query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func remapArchiveAssetReferences(tables map[string]json.RawMessage, mapping map[int64]int64) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(tables))
	for name, raw := range tables {
		out[name] = raw
	}
	for _, table := range []string{"exploration_anchors", "task_asset_links"} {
		rows, err := decodeArchiveRows(tables[table])
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if old, ok := jsonInt64(row["asset_id"]); ok {
				if replacement, exists := mapping[old]; exists {
					row["asset_id"] = replacement
				}
			}
		}
		out[table], _ = json.Marshal(rows)
	}
	for _, table := range []string{"exploration_nodes", "findings"} {
		rows, err := decodeArchiveRows(tables[table])
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			key := "asset_ids"
			container := row
			if table == "exploration_nodes" {
				payload, ok := row["payload"].(map[string]any)
				if !ok {
					continue
				}
				container = payload
			}
			if values, ok := container[key].([]any); ok {
				for i, value := range values {
					if old, ok := jsonInt64(value); ok {
						if replacement, exists := mapping[old]; exists {
							values[i] = replacement
						}
					}
				}
				container[key] = values
			}
		}
		out[table], _ = json.Marshal(rows)
	}
	return out, nil
}

func restoreTaskRelations(tx *sql.Tx, taskID int64, raw json.RawMessage) ([]string, error) {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	for _, row := range rows {
		sourceID, ok := jsonInt64(row["source_task_id"])
		if !ok || !liveTaskExists(tx, sourceID) {
			warnings = append(warnings, fmt.Sprintf("来源任务 %d 不可用，继承关系已跳过", sourceID))
			continue
		}
		created, _ := row["created_at"].(string)
		if _, err := tx.Exec(`INSERT INTO task_relations(task_id,source_task_id,created_at) VALUES($1,$2,COALESCE($3::timestamptz,now())) ON CONFLICT DO NOTHING`, taskID, sourceID, nilIfEmptyString(created)); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}

func restoreTaskScopes(tx *sql.Tx, raw json.RawMessage) ([]string, error) {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	kept := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if companyID, ok := jsonInt64(row["company_id"]); ok && companyID > 0 {
			exists, err := rowExists(tx, "companies", companyID)
			if err != nil {
				return nil, err
			}
			if !exists {
				warnings = append(warnings, fmt.Sprintf("企业 %d 已删除，关联范围已跳过", companyID))
				continue
			}
		}
		kept = append(kept, row)
	}
	if len(kept) == 0 {
		return warnings, nil
	}
	encoded, _ := json.Marshal(kept)
	_, err = tx.Exec(`INSERT INTO task_scope SELECT * FROM json_populate_recordset(NULL::task_scope,$1::json) ON CONFLICT DO NOTHING`, string(encoded))
	return warnings, err
}

func restoreTaskLLMProfiles(tx *sql.Tx, raw json.RawMessage) ([]string, error) {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return nil, err
	}
	warnings := []string{}
	position := 0
	for _, row := range rows {
		profileID, ok := jsonInt64(row["profile_id"])
		if !ok {
			warnings = append(warnings, fmt.Sprintf("LLM 配置 %d 无效，配置链项已跳过", profileID))
			continue
		}
		exists, err := rowExists(tx, "llm_profiles", profileID)
		if err != nil {
			return nil, err
		}
		if !exists {
			warnings = append(warnings, fmt.Sprintf("LLM 配置 %d 已删除，配置链项已跳过", profileID))
			continue
		}
		taskID, _ := jsonInt64(row["task_id"])
		status, _ := row["status"].(string)
		lastError, _ := row["last_error"].(string)
		exhaustedAt, _ := row["exhausted_at"].(string)
		createdAt, _ := row["created_at"].(string)
		updatedAt, _ := row["updated_at"].(string)
		if _, err := tx.Exec(`INSERT INTO task_llm_profiles(task_id,profile_id,position,status,last_error,exhausted_at,created_at,updated_at)
	VALUES($1,$2,$3,$4,NULLIF($5,''),$6::timestamptz,COALESCE($7::timestamptz,now()),COALESCE($8::timestamptz,now()))
	ON CONFLICT DO NOTHING`, taskID, profileID, position, status, lastError,
			nilIfEmptyString(exhaustedAt), nilIfEmptyString(createdAt), nilIfEmptyString(updatedAt)); err != nil {
			return nil, err
		}
		position++
	}
	return warnings, nil
}

func restoreInterceptRows(tx *sql.Tx, raw json.RawMessage) error {
	rows, err := decodeArchiveRows(raw)
	if err != nil {
		return err
	}
	for _, row := range rows {
		// Archives predating approval snapshots lack this NOT NULL column.
		if source, _ := row["decision_source"].(string); source == "" {
			source = "unknown"
			if row["rule_id"] != nil {
				source = "rule"
			} else if reason, _ := row["reason"].(string); strings.HasPrefix(reason, "[模型]") {
				source = "model"
			}
			row["decision_source"] = source
		}
		if status, _ := row["status"].(string); status == "pending" {
			row["status"] = "timeout"
			row["reason"] = "任务归档期间审批已超时"
			row["decided_at"] = time.Now().UTC()
			if audit, ok := row["audit"].(map[string]any); ok {
				audit["effective_action"] = "deny"
				audit["decision_reason"] = "任务归档期间审批已超时"
				audit["execution_status"] = "not_executed"
			}
		} else if audit, ok := row["audit"].(map[string]any); ok && audit["execution_status"] == "awaiting_result" {
			// An archived run cannot resume its former result callback.
			audit["execution_status"] = "unknown"
		}
	}
	if len(rows) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(rows)
	_, err = tx.Exec(`INSERT INTO intercept_pending SELECT * FROM json_populate_recordset(NULL::intercept_pending,$1::json)`, string(encoded))
	return err
}

func liveTaskExists(tx *sql.Tx, id int64) bool {
	var exists bool
	_ = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&exists)
	return exists
}

func nilIfEmptyString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// CompleteTaskArchiveRestore removes compact metadata after every external
// component has been verified and the package has been consumed.
func (d *DB) CompleteTaskArchiveRestore(archiveID int64) error {
	res, err := d.Exec(`DELETE FROM task_archives archive USING tasks task
WHERE archive.id=$1 AND archive.task_id=task.id AND task.deleted_at IS NULL AND task.archived_at IS NULL`, archiveID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrTaskArchiveState
	}
	return nil
}

// DeleteTaskArchiveStub permanently removes the cold task after its package has
// been staged for deletion. Dependency protection is rechecked transactionally.
func (d *DB) DeleteTaskArchiveStub(archiveID int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	var taskID, expID int64
	var state string
	if err := tx.QueryRow(`SELECT archive.task_id,task.exploration_id,archive.state
FROM task_archives archive JOIN tasks task ON task.id=archive.task_id
WHERE archive.id=$1 FOR UPDATE OF archive,task`, archiveID).Scan(&taskID, &expID, &state); err != nil {
		return err
	}
	if state != Deleting {
		return ErrTaskArchiveState
	}
	var dependent int64
	err = tx.QueryRow(`SELECT task_id FROM task_archives WHERE id<>$1 AND $2=ANY(source_task_ids) LIMIT 1`, archiveID, taskID).Scan(&dependent)
	if err == nil {
		return fmt.Errorf("%w: task %d", ErrTaskArchiveDeleteBlocked, dependent)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tasks WHERE id=$1 AND archived_at IS NOT NULL`, taskID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM explorations WHERE id=$1`, expID); err != nil {
		return err
	}
	// Graph delete triggers can publish a final blackboard event; clear after.
	if err := deleteBlackboardStateTx(tx, expID); err != nil {
		return err
	}
	return tx.Commit()
}

// ArchivedAggregateStats returns compact summaries used by global dashboards so
// cold data does not disappear from historical totals.
func (d *DB) ArchivedAggregateStats() ([]json.RawMessage, error) {
	rows, err := d.Query(`SELECT archive.aggregate_stats
FROM task_archives archive
JOIN tasks task ON task.id=archive.task_id
WHERE task.archived_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// assertArchiveSnapshotComplete is the guard between "package written" and
// "cascade-delete the hot rows". The manifest counts must match both the JSON
// rows and the live rows under the same predicates; a concurrent human write
// therefore fails the archive instead of silently dropping data.
func assertArchiveSnapshotComplete(tx *sql.Tx, taskID, expID int64, snapshot *TaskArchiveSnapshot) error {
	queries := map[string]struct {
		query string
		args  []any
	}{
		"finding_comments":     {`SELECT count(*) FROM finding_comments c JOIN findings f ON f.id=c.finding_id WHERE f.task_id=$1`, []any{taskID}},
		"finding_retests":      {`SELECT count(*) FROM finding_retests r JOIN findings f ON f.id=r.finding_id WHERE f.task_id=$1`, []any{taskID}},
		"finding_case_members": {`SELECT count(*) FROM finding_case_members m JOIN findings f ON f.id=m.finding_id WHERE f.task_id=$1`, []any{taskID}},
		"finding_case_blocks":  {`SELECT count(*) FROM finding_case_blocks b WHERE b.left_id IN (SELECT id FROM findings WHERE task_id=$1) OR b.right_id IN (SELECT id FROM findings WHERE task_id=$1)`, []any{taskID}},
		"credential_checks":    {`SELECT count(*) FROM credential_checks WHERE exploration_id=$1`, []any{expID}},
		"main_sessions":        {`SELECT count(*) FROM main_sessions WHERE exploration_id=$1`, []any{expID}},
		"completion_waivers":   {`SELECT count(*) FROM completion_waivers WHERE exploration_id=$1`, []any{expID}},
	}
	for table, item := range queries {
		var live int64
		if err := tx.QueryRow(item.query, item.args...).Scan(&live); err != nil {
			return fmt.Errorf("archive completeness %s: %w", table, err)
		}
		manifest := snapshot.DataCounts[table]
		if raw := rawRowCount(snapshot.Tables[table]); raw != manifest || live != manifest {
			return fmt.Errorf("%w: %s live=%d manifest=%d rows=%d", ErrTaskArchiveState, table, live, manifest, raw)
		}
	}
	// 新版快照补充了 task_planner_state / task_timeout_events；若存在则同口径校验，
	// 兼容旧版或手工构造的历史 snapshot。
	for table, item := range map[string]struct {
		query string
		args  []any
	}{
		"task_planner_state":  {`SELECT count(*) FROM task_planner_state WHERE task_id=$1`, []any{taskID}},
		"task_timeout_events": {`SELECT count(*) FROM task_timeout_events WHERE task_id=$1`, []any{taskID}},
	} {
		if _, present := snapshot.Tables[table]; !present {
			continue
		}
		var live int64
		if err := tx.QueryRow(item.query, item.args...).Scan(&live); err != nil {
			return fmt.Errorf("archive completeness %s: %w", table, err)
		}
		manifest := snapshot.DataCounts[table]
		if raw := rawRowCount(snapshot.Tables[table]); raw != manifest || live != manifest {
			return fmt.Errorf("%w: %s live=%d manifest=%d rows=%d", ErrTaskArchiveState, table, live, manifest, raw)
		}
	}
	return assertArchiveContentMatches(tx, taskID, expID, snapshot)
}

// restoreFindingChildrenTx inserts task-owned finding children and tolerates
// cross-task references that no longer exist. It never invents a missing peer.
func restoreFindingChildrenTx(tx *sql.Tx, tables map[string]json.RawMessage) ([]string, error) {
	warnings := []string{}
	if err := insertArchiveRows(tx, "finding_comments", tables["finding_comments"]); err != nil {
		return nil, err
	}
	retests, err := decodeArchiveRows(tables["finding_retests"])
	if err != nil {
		return nil, err
	}
	for _, row := range retests {
		if conversationID, ok := jsonInt64(row["conversation_id"]); ok && conversationID > 0 {
			exists, err := rowExists(tx, "conversations", conversationID)
			if err != nil {
				return nil, err
			}
			if !exists {
				row["conversation_id"] = nil
			}
		}
		status, _ := row["status"].(string)
		if status == "pending" || status == "running" {
			row["status"] = "stopped"
			row["error"] = "归档恢复：原复测已中断"
			if row["finished_at"] == nil {
				row["finished_at"] = time.Now().UTC()
			}
		}
	}
	if raw, err := json.Marshal(retests); err != nil {
		return nil, err
	} else if err := insertArchiveRows(tx, "finding_retests", raw); err != nil {
		return nil, err
	}
	members, err := decodeArchiveRows(tables["finding_case_members"])
	if err != nil {
		return nil, err
	}
	keptMembers := make([]map[string]any, 0, len(members))
	for _, row := range members {
		findingID, _ := jsonInt64(row["finding_id"])
		caseID, _ := jsonInt64(row["case_id"])
		findingOK, err := rowExists(tx, "findings", findingID)
		if err != nil {
			return nil, err
		}
		caseOK, err := rowExists(tx, "finding_cases", caseID)
		if err != nil {
			return nil, err
		}
		if !findingOK || !caseOK {
			warnings = append(warnings, fmt.Sprintf("漏洞 %d 的归并关系已跳过：对端记录不存在", findingID))
			continue
		}
		keptMembers = append(keptMembers, row)
	}
	if raw, err := json.Marshal(keptMembers); err != nil {
		return nil, err
	} else if err := insertArchiveRows(tx, "finding_case_members", raw); err != nil {
		return nil, err
	}
	blocks, err := decodeArchiveRows(tables["finding_case_blocks"])
	if err != nil {
		return nil, err
	}
	keptBlocks := make([]map[string]any, 0, len(blocks))
	for _, row := range blocks {
		left, _ := jsonInt64(row["left_id"])
		right, _ := jsonInt64(row["right_id"])
		leftOK, err := rowExists(tx, "findings", left)
		if err != nil {
			return nil, err
		}
		rightOK, err := rowExists(tx, "findings", right)
		if err != nil {
			return nil, err
		}
		if !leftOK || !rightOK {
			warnings = append(warnings, fmt.Sprintf("漏洞 %d/%d 的归并阻断关系已跳过：对端记录不存在", left, right))
			continue
		}
		keptBlocks = append(keptBlocks, row)
	}
	if raw, err := json.Marshal(keptBlocks); err != nil {
		return nil, err
	} else if err := insertArchiveRows(tx, "finding_case_blocks", raw); err != nil {
		return nil, err
	}
	return warnings, nil
}

// Counts cannot detect in-place changes or equal-count replacements. Recheck
// the mutable finding payload and archived children while completion holds their
// table locks; this also protects against old/low-level writers missing a fence.
func assertArchiveContentMatches(tx *sql.Tx, taskID, expID int64, snapshot *TaskArchiveSnapshot) error {
	checks := []struct {
		name, query string
		args        []any
	}{
		{"findings", `SELECT * FROM findings WHERE task_id=$1 ORDER BY id`, []any{taskID}},
		{"finding_comments", `SELECT c.* FROM finding_comments c JOIN findings f ON f.id=c.finding_id WHERE f.task_id=$1 ORDER BY c.id`, []any{taskID}},
		{"finding_retests", `SELECT r.* FROM finding_retests r JOIN findings f ON f.id=r.finding_id WHERE f.task_id=$1 ORDER BY r.id`, []any{taskID}},
		{"finding_case_members", `SELECT m.* FROM finding_case_members m JOIN findings f ON f.id=m.finding_id WHERE f.task_id=$1 ORDER BY m.finding_id`, []any{taskID}},
		{"finding_case_blocks", `SELECT b.* FROM finding_case_blocks b WHERE b.left_id IN (SELECT id FROM findings WHERE task_id=$1) OR b.right_id IN (SELECT id FROM findings WHERE task_id=$1) ORDER BY b.left_id,b.right_id`, []any{taskID}},
		{"credential_checks", `SELECT * FROM credential_checks WHERE exploration_id=$1 ORDER BY id`, []any{expID}},
		{"main_sessions", `SELECT * FROM main_sessions WHERE exploration_id=$1 ORDER BY seq`, []any{expID}},
		{"completion_waivers", `SELECT * FROM completion_waivers WHERE exploration_id=$1 ORDER BY id`, []any{expID}},
	}
	if _, present := snapshot.Tables["task_planner_state"]; present {
		checks = append(checks, struct {
			name, query string
			args        []any
		}{"task_planner_state", `SELECT * FROM task_planner_state WHERE task_id=$1`, []any{taskID}})
	}
	if _, present := snapshot.Tables["task_timeout_events"]; present {
		checks = append(checks, struct {
			name, query string
			args        []any
		}{"task_timeout_events", `SELECT * FROM task_timeout_events WHERE task_id=$1 ORDER BY id`, []any{taskID}})
	}
	canonical := func(raw json.RawMessage) ([]byte, error) {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	for _, check := range checks {
		live, _, err := queryArchiveRows(txRows{tx}, check.query, check.args...)
		if err != nil {
			return err
		}
		a, err := canonical(live)
		if err != nil {
			return err
		}
		b, err := canonical(snapshot.Tables[check.name])
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			return fmt.Errorf("%w: %s changed after snapshot", ErrTaskArchiveState, check.name)
		}
	}
	return nil
}
