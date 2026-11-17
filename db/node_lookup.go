package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// NodeLocation 返回节点所属探索与 kind（跨任务按全局 id 查，不要求先知道 task）。
func (d *DB) NodeLocation(nodeID int64) (expID int64, kind string, err error) {
	if nodeID <= 0 {
		return 0, "", fmt.Errorf("node id is required")
	}
	err = d.QueryRow(`SELECT exploration_id, kind FROM exploration_nodes WHERE id=$1`, nodeID).Scan(&expID, &kind)
	if err == sql.ErrNoRows {
		return 0, "", ErrNodeNotFound
	}
	return expID, kind, err
}

// ErrNodeNotFound 表示节点不存在（调用方可映射为 404）。
var ErrNodeNotFound = fmt.Errorf("node not found")

// TaskForNode 返回节点所属任务（已删除任务视为不存在）。
func (d *DB) TaskForNode(nodeID int64) (int64, error) {
	var taskID int64
	err := d.QueryRow(`SELECT t.id FROM exploration_nodes n
JOIN tasks t ON t.exploration_id=n.exploration_id
WHERE n.id=$1 AND t.deleted_at IS NULL`, nodeID).Scan(&taskID)
	if err == sql.ErrNoRows {
		return 0, ErrNodeNotFound
	}
	return taskID, err
}

// NodeAnchorIDs 返回节点锚定的资产 id（升序）。
func (d *DB) NodeAnchorIDs(nodeID int64) ([]int64, error) {
	rows, err := d.Query(`SELECT asset_id FROM exploration_anchors WHERE node_id=$1 ORDER BY asset_id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AssetLabels 返回 asset id → 展示标签（domain/url/ip/app_name 依次回退）。
func (d *DB) AssetLabels(ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}
	rows, err := d.Query(`SELECT id, COALESCE(domain,''), COALESCE(url,''), COALESCE(ip,''), COALESCE(app_name,'')
FROM assets WHERE id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var domain, url, ip, app string
		if err := rows.Scan(&id, &domain, &url, &ip, &app); err != nil {
			return nil, err
		}
		label := domain
		if label == "" {
			label = url
		}
		if label == "" {
			label = ip
		}
		if label == "" {
			label = app
		}
		if label == "" {
			label = fmt.Sprintf("#%d", id)
		}
		out[id] = label
	}
	return out, rows.Err()
}
