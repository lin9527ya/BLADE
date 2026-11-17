package db

import (
	"database/sql"
	"fmt"
	"strings"
)

// CredentialUsage 描述一次结构化执行（loot_exec/foothold_exec/tunnel_open/文件传输）
// 实际会用哪条凭据、连哪个目标。它只读取黑板节点，不接触目标。
type CredentialUsage struct {
	NodeID         int64
	NodeKind       string // credential | foothold
	CredentialID   int64  // foothold 解析出的凭据节点；credential 时等于 NodeID
	Username       string
	CredentialKind string // password | hash | key | token ...
	Host           string
	Port           int
	Service        string
	State          string
}

// CountsAgainstBudget 只有口令/哈希才会触发目标侧账号锁定；私钥/令牌不计入预算。
func (u *CredentialUsage) CountsAgainstBudget() bool {
	switch strings.ToLower(strings.TrimSpace(u.CredentialKind)) {
	case "password", "hash":
		return true
	}
	return false
}

// CredentialUsage 读取一条战果节点，解析出这次结构化执行实际使用的凭据与目标。
// wantHost 为空时取凭据的第一个登记目标。节点不存在返回 sql.ErrNoRows。
func (d *DB) CredentialUsage(lootID int64, wantHost string) (*CredentialUsage, error) {
	if lootID <= 0 {
		return nil, fmt.Errorf("loot id is required")
	}
	var kind, state string
	var payload []byte
	err := d.QueryRow(`SELECT kind, state, payload FROM exploration_nodes WHERE id=$1`, lootID).
		Scan(&kind, &state, &payload)
	if err != nil {
		return nil, err
	}
	node := &Node{ID: lootID, Kind: kind, State: state, Payload: payload}
	use := &CredentialUsage{NodeID: lootID, NodeKind: kind, State: state}
	switch kind {
	case KindCredential:
		v := ParseCredential(node)
		use.CredentialID = lootID
		use.Username = strings.TrimSpace(v.Username)
		use.CredentialKind = strings.ToLower(strings.TrimSpace(v.Kind))
		target, ok := pickUsageTarget(v.Targets, wantHost)
		if ok {
			use.Host = target.Host
			use.Port = target.Port
			use.Service = target.Service
		}
	case KindFoothold:
		fv := ParseFoothold(node)
		use.Host = strings.TrimSpace(fv.Host)
		use.Port = fv.Port
		use.Service = strings.ToLower(strings.TrimSpace(fv.Protocol))
		use.CredentialID = fv.CredentialID
		if fv.CredentialID > 0 {
			var ckind, cstate string
			var cpayload []byte
			err := d.QueryRow(`SELECT kind, state, payload FROM exploration_nodes WHERE id=$1`, fv.CredentialID).
				Scan(&ckind, &cstate, &cpayload)
			if err != nil && err != sql.ErrNoRows {
				return nil, err
			}
			if err == nil {
				cv := ParseCredential(&Node{ID: fv.CredentialID, Kind: ckind, State: cstate, Payload: cpayload})
				use.Username = strings.TrimSpace(cv.Username)
				use.CredentialKind = strings.ToLower(strings.TrimSpace(cv.Kind))
			}
		}
	default:
		return nil, fmt.Errorf("节点 %d 不是凭据或据点（kind=%s）", lootID, kind)
	}
	if strings.TrimSpace(use.Host) == "" && wantHost != "" {
		use.Host = strings.TrimSpace(wantHost)
	}
	return use, nil
}

func pickUsageTarget(targets []CredentialTarget, wantHost string) (CredentialTarget, bool) {
	want := strings.ToLower(strings.TrimSpace(wantHost))
	if want != "" {
		for _, t := range targets {
			if strings.ToLower(strings.TrimSpace(t.Host)) == want {
				return t, true
			}
		}
	}
	if len(targets) > 0 {
		return targets[0], true
	}
	return CredentialTarget{}, false
}
