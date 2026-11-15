package db

// TaskRelationRef is one directly related task, as shown in the task overview.
type TaskRelationRef struct {
	ID          int64  `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Direction   string `json:"direction"` // source | dependent
}

// TaskRelations returns both directions of the direct task relation graph. Only
// one hop is expanded; deeper ancestry stays visible through each task's own page.
func (d *DB) TaskRelations(taskID int64) ([]TaskRelationRef, error) {
	rows, err := d.Query(`
SELECT t.id, COALESCE(t.name,''), COALESCE(t.description,''), t.status, direction FROM (
  SELECT source.id, source.name, source.description, source.status, 'source' AS direction
  FROM task_relations r JOIN tasks source ON source.id=r.source_task_id AND source.deleted_at IS NULL
  WHERE r.task_id=$1
  UNION ALL
  SELECT dependent.id, dependent.name, dependent.description, dependent.status, 'dependent' AS direction
  FROM task_relations r JOIN tasks dependent ON dependent.id=r.task_id AND dependent.deleted_at IS NULL
  WHERE r.source_task_id=$1
) t ORDER BY direction, id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskRelationRef{}
	for rows.Next() {
		var item TaskRelationRef
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.Status, &item.Direction); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
