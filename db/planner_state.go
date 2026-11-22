package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// PlannerTodoItem is the persisted form of Norma's TodoWrite item. The status
// values are pending / in_progress / completed; unknown values are kept as-is so
// a future schema extension does not silently drop the model's note.
type PlannerTodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// PlannerState is the planner's cross-process scratchpad. It deliberately holds
// only planning notes and the last conclusion — never facts, findings or evidence.
type PlannerState struct {
	Todos         []PlannerTodoItem
	LastRoundNote string
	UpdatedAt     time.Time
}

// LoadPlannerState reads the planner scratchpad for a task in this exploration.
// found=false means no round has been persisted yet (a new task), not an error.
func (s *ExplorationStore) LoadPlannerState(taskID int64) (PlannerState, bool, error) {
	var out PlannerState
	var raw []byte
	err := s.db.QueryRow(`SELECT p.todos, p.last_round_note, p.updated_at
FROM task_planner_state p
JOIN tasks t ON t.id=p.task_id AND t.exploration_id=$2 AND t.deleted_at IS NULL
WHERE p.task_id=$1`, taskID, s.expID).Scan(&raw, &out.LastRoundNote, &out.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PlannerState{}, false, nil
		}
		return PlannerState{}, false, err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.Todos)
	}
	if out.Todos == nil {
		out.Todos = []PlannerTodoItem{}
	}
	return out, true, nil
}

// SavePlannerState upserts the scratchpad. The EXISTS guard keeps a deleted task
// from being resurrected by a racing planner round.
func (s *ExplorationStore) SavePlannerState(taskID int64, todos []PlannerTodoItem, lastRoundNote string) error {
	if todos == nil {
		todos = []PlannerTodoItem{}
	}
	raw, err := json.Marshal(todos)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO task_planner_state(task_id,todos,last_round_note,updated_at)
SELECT $1,$2::jsonb,$3,now()
WHERE EXISTS(SELECT 1 FROM tasks WHERE id=$1 AND exploration_id=$4 AND deleted_at IS NULL)
ON CONFLICT (task_id) DO UPDATE SET
  todos=EXCLUDED.todos, last_round_note=EXCLUDED.last_round_note, updated_at=now()`,
		taskID, string(raw), lastRoundNote, s.expID)
	return err
}
