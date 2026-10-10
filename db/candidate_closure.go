package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CandidateClosureInput closes ONE candidate that must not become a finding:
// ruled_out (disproved — counterevidence names the control point and why it is
// off the attacker path) or open_proof_gap (still open — proof_gap says what is
// missing). strix 三态闭合纪律：候选要么 confirmed（report_finding），要么带
// 反证排除，要么明写缺口——不得静默丢弃（"没找到控制点 ≠ 安全"）。
type CandidateClosureInput struct {
	TaskID, ExplorationID, IntentID                int64
	VulnClass, Name, Severity, Summary, Worker     string
	Outcome, Counterevidence, ProofGap, Confidence string
	AssetIDs                                       []int64
}

// RecordCandidateClosure writes the closure as a finding-graph node (state =
// outcome, so the graph and the report see the same three states) plus the
// standalone findings row that carries the note and the confidence label.
func (s *ExplorationStore) RecordCandidateClosure(ctx context.Context, in CandidateClosureInput) (*RecordedFinding, error) {
	in.ExplorationID = s.expID
	in.Counterevidence = strings.TrimSpace(in.Counterevidence)
	in.ProofGap = strings.TrimSpace(in.ProofGap)
	switch in.Outcome {
	case FindingRuledOut:
		if in.Counterevidence == "" {
			return nil, errors.New("ruled_out 需要 counterevidence：点名阻断该候选的具体控制点/机制，以及它为何不在攻击者路径上")
		}
	case FindingOpenProofGap:
		if in.ProofGap == "" {
			return nil, errors.New("open_proof_gap 需要 proof_gap：写清缺什么信息（哪一步没法验证、需要什么前提），不得默关")
		}
	default:
		return nil, errors.New("outcome 必须是 ruled_out 或 open_proof_gap（确认漏洞请用 report_finding）")
	}
	if !ValidConfidence(in.Confidence) || in.Confidence == "" {
		return nil, errors.New("confidence 必填：high（可复现触发链）/ medium（直接观察到强异常，未构造完整对照）/ low（据指纹/版本/现象推断）")
	}
	out := &RecordedFinding{}
	err := s.db.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		if in.TaskID > 0 {
			var expID int64
			if err := tx.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, in.TaskID).Scan(&expID); err != nil {
				return err
			}
			if expID != in.ExplorationID {
				return errors.New("候选所属任务与探索记录不匹配")
			}
		}
		if in.IntentID > 0 {
			var ok bool
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent')`, in.IntentID, in.ExplorationID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errors.New("intent_id 必须是本任务的意图（关联任务意图只读）")
			}
		}
		payload, _ := json.Marshal(map[string]any{
			"vulnclass": in.VulnClass, "name": in.Name, "severity": in.Severity, "summary": in.Summary,
			"closure": map[string]any{
				"outcome": in.Outcome, "counterevidence": in.Counterevidence,
				"proof_gap": in.ProofGap, "confidence": in.Confidence, "by": in.Worker,
			},
		})
		if err := tx.QueryRow(`INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin)
VALUES($1,'finding',$2,9,$3,$4) RETURNING id`, in.ExplorationID, string(payload), in.Outcome, in.Worker).Scan(&out.NodeID); err != nil {
			return err
		}
		for _, asset := range in.AssetIDs {
			if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, out.NodeID, asset); err != nil {
				return err
			}
		}
		if in.IntentID > 0 {
			if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,$3,$4)`, in.ExplorationID, in.IntentID, RelYields, out.NodeID); err != nil {
				return err
			}
		}
		assets := in.AssetIDs
		if assets == nil {
			assets = []int64{}
		}
		raw, _ := json.Marshal(assets)
		if err := tx.QueryRow(`INSERT INTO findings(task_id,node_id,vulnclass,name,severity,summary,evidence,worker,asset_ids,status,confidence,counterevidence,proof_gap)
VALUES(NULLIF($1,0),$2,$3,$4,$5,$6,'',$7,$8,$9,$10,$11,$12) RETURNING id`,
			in.TaskID, out.NodeID, in.VulnClass, in.Name, in.Severity, in.Summary, in.Worker, string(raw),
			in.Outcome, in.Confidence, in.Counterevidence, in.ProofGap).Scan(&out.FindingID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetFindingClosure persists a human/retester triage of a candidate closure
// (status ruled_out / open_proof_gap) together with its note. Returns false when
// no such finding exists.
func (d *DB) SetFindingClosure(id int64, status, counterevidence, proofGap string) (bool, error) {
	if status != FindingRuledOut && status != FindingOpenProofGap {
		return false, fmt.Errorf("bad closure status: %s", status)
	}
	res, err := d.Exec(`UPDATE findings SET status=$1, counterevidence=$2, proof_gap=$3 WHERE id=$4`,
		status, strings.TrimSpace(counterevidence), strings.TrimSpace(proofGap), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetFindingConfidence stamps the honest-calibration label on one finding.
func (d *DB) SetFindingConfidence(id int64, confidence string) (bool, error) {
	if !ValidConfidence(confidence) {
		return false, fmt.Errorf("bad confidence: %s", confidence)
	}
	res, err := d.Exec(`UPDATE findings SET confidence=$1 WHERE id=$2`, confidence, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
