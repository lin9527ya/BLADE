package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// GoalProofOutcome describes what ProveGoal committed. The goal counts are read
// in the same transaction as the state change, so callers can decide whether the
// task is settled without racing a concurrent abandon/edit.
type GoalProofOutcome struct {
	GoalID         int64
	EvidenceID     int64
	EvidenceKind   string
	AlreadyMet     bool
	TotalGoals     int
	OpenGoals      int
	MetGoals       int
	AbandonedGoals int
}

// ProveGoal atomically records an evidence --proves--> goal edge and, when the
// goal is still open, flips it to met. The proof reason is persisted with both
// writes: per-edge attrs (why this evidence supports the goal) and the goal
// payload (why it first became met).
//
// Why one transaction instead of Link + SetNodeState: the two writes are one
// claim. An edge committed against an open goal leaves the graph saying
// "proved" while the task still looks unfinished; a met goal with no edge
// leaves the report unable to show what proved it. Both halves must commit
// together, and every failure must be returned to the caller instead of being
// logged and swallowed.
//
// This is not a universal semantic predicate — quantified acceptance (coverage,
// privileges, counts) remains the planner's judgement. But an unexplained "met"
// is not auditable, so reason is mandatory.
func (s *ExplorationStore) ProveGoal(goalID, evidenceID int64, reason string) (*GoalProofOutcome, error) {
	if goalID <= 0 || evidenceID <= 0 {
		return nil, fmt.Errorf("goal_id 和 evidence_id 必填")
	}
	reason = strings.TrimSpace(utf8Clean(reason))
	if reason == "" {
		return nil, fmt.Errorf("reason 必填：写清这个证据为什么满足该目标")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var goalKind, goalState string
	var goalVersion int64
	if err := tx.QueryRow(`SELECT kind,state,content_version FROM exploration_nodes
WHERE id=$1 AND exploration_id=$2 FOR UPDATE`, goalID, s.expID).Scan(&goalKind, &goalState, &goalVersion); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("目标 %d 必须是本任务的目标节点（关联任务目标只读）", goalID)
		}
		return nil, err
	}
	if goalKind != KindGoal {
		return nil, fmt.Errorf("%d 是 %q 节点：goal_id 必须是本任务的目标节点", goalID, goalKind)
	}
	switch goalState {
	case "open", "met":
	case "abandoned":
		return nil, fmt.Errorf("目标 %d 已放弃(abandoned)：放弃不可撤销，不能再用 prove_goal 标为 met", goalID)
	default:
		return nil, fmt.Errorf("目标 %d 当前状态是 %q：只有未达成(open)的目标可以证明", goalID, goalState)
	}

	// Evidence may be local or inherited from a directly related task. The edge
	// itself is always written into this exploration, matching Link's behavior
	// for inherited sources (node ids are globally unique).
	var evidenceKind string
	var sourceID, materialVersion int64
	var findingID, findingVersion sql.NullInt64
	var findingStatus sql.NullString
	var showReason bool
	err = tx.QueryRow(`SELECT n.kind,n.exploration_id,n.proof_material_version,f.id,f.evidence_version,f.status,
    goal_review_reason_visible(n.kind,$3,n.payload,f.evidence)
FROM exploration_nodes n LEFT JOIN LATERAL (
  SELECT id,evidence_version,status,evidence FROM findings WHERE n.kind='finding' AND node_id=n.id
      AND exploration_id=n.exploration_id ORDER BY id DESC LIMIT 1
) f ON true
WHERE n.id=$1 AND (
  n.exploration_id=$2 OR n.exploration_id IN (
    SELECT source.exploration_id FROM tasks owner
    JOIN task_relations relation ON relation.task_id=owner.id
    JOIN tasks source ON source.id=relation.source_task_id AND source.deleted_at IS NULL
    WHERE owner.exploration_id=$2 AND owner.deleted_at IS NULL
  )
) FOR SHARE OF n`, evidenceID, s.expID, reason).Scan(&evidenceKind, &sourceID, &materialVersion, &findingID, &findingVersion, &findingStatus, &showReason)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("evidence_id 必须是本任务或直接关联任务的事实/漏洞/据点/凭据节点")
	}
	if err != nil {
		return nil, err
	}
	switch evidenceKind {
	case KindFact, KindFinding, KindFoothold, KindCredential:
	default:
		return nil, fmt.Errorf("evidence_id 必须是事实/漏洞/据点/凭据节点（收到 %q）", evidenceKind)
	}

	// One operation id groups the edge and the state change in the blackboard
	// outbox, and keeps the proof reason in the operation audit trail.
	if _, err := GraphOperationTx(tx, s.expID, "prove_goal",
		fmt.Sprintf("%d --proves--> %d: %s", evidenceID, goalID, reason)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id,attrs)
VALUES($1,$2,$3,$4,jsonb_build_object('reason',$5::text,'proved_at',to_jsonb(now())))
ON CONFLICT (exploration_id,src_id,rel,dst_id) DO UPDATE SET attrs=EXCLUDED.attrs`,
		s.expID, evidenceID, RelProves, goalID, reason); err != nil {
		return nil, err
	}
	out := &GoalProofOutcome{
		GoalID: goalID, EvidenceID: evidenceID, EvidenceKind: evidenceKind,
		AlreadyMet: goalState == "met",
	}
	if goalState == "open" {
		err := tx.QueryRow(`UPDATE exploration_nodes SET
  state='met', blocked_reason=NULL, content_version=content_version+1, updated_at=now(),
  payload = payload || jsonb_build_object(
    'proof_reason',$2::text,'proved_at',to_jsonb(now()),'proved_by_evidence_id',$3::bigint)
WHERE id=$1 AND exploration_id=$4 AND kind='goal' AND state='open' RETURNING content_version`,
			goalID, reason, evidenceID, s.expID).Scan(&goalVersion)
		if err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`INSERT INTO goal_proof_refs
  (goal_id,goal_version,evidence_id,source_exploration_id,evidence_kind,proved_at,material_version,finding_id,finding_version,finding_status,hide_reason)
VALUES ($1,$2,$3,$4,$5,now(),$6,$7,$8,$9,$10)
ON CONFLICT (goal_id,goal_version,evidence_id) DO UPDATE SET
  source_exploration_id=EXCLUDED.source_exploration_id,evidence_kind=EXCLUDED.evidence_kind,proved_at=EXCLUDED.proved_at,
  material_version=EXCLUDED.material_version,finding_id=EXCLUDED.finding_id,finding_version=EXCLUDED.finding_version,
  finding_status=EXCLUDED.finding_status,hide_reason=EXCLUDED.hide_reason`, goalID, goalVersion, evidenceID, sourceID, evidenceKind,
		materialVersion, findingID, findingVersion, findingStatus, !showReason); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*),
  COUNT(*) FILTER (WHERE state='met'),
  COUNT(*) FILTER (WHERE state='abandoned'),
  COUNT(*) FILTER (WHERE state='open')
FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal'`, s.expID).
		Scan(&out.TotalGoals, &out.MetGoals, &out.AbandonedGoals, &out.OpenGoals); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
