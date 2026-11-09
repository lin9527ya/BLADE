package db

import (
	"encoding/json"
)

// SettleIntent atomically commits coverage, reason and terminal state under one run token.
func (s *ExplorationStore) SettleIntent(id int64, state string, validated []int64, reason string) error {
	if s.db.authority == nil || s.db.authority.IntentID != id {
		return ErrStaleExecution
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, err := json.Marshal(validated)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE exploration_nodes SET state=$1,blocked_reason=NULLIF($2,''),content_version=content_version+1,lease_expires_at=NULL,completed_at=now(),payload=CASE WHEN $3::jsonb IS NOT NULL AND jsonb_array_length($3::jsonb)>0 THEN payload||jsonb_build_object('validated_asset_ids',$3::jsonb) ELSE payload END ||jsonb_build_object('last_outcome',$1::text) WHERE id=$4 AND exploration_id=$5 AND kind='intent' AND state='running'`, state, reason, string(raw), id, s.expID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
