package db

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Scopes     string     `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

func HashAPIToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func (d *DB) CreateAPIToken(name string) (*APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, "", fmt.Errorf("令牌名称不能为空")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", err
	}
	plain := "blt_" + base64.RawURLEncoding.EncodeToString(raw)
	var token APIToken
	err := d.QueryRow(`INSERT INTO api_tokens(name,token_hash,scopes) VALUES($1,$2,'admin')
RETURNING id,name,scopes,created_at`, name, HashAPIToken(plain)).Scan(&token.ID, &token.Name, &token.Scopes, &token.CreatedAt)
	if err != nil {
		return nil, "", err
	}
	return &token, plain, nil
}

func (d *DB) ListAPITokens() ([]*APIToken, error) {
	rows, err := d.Query(`SELECT id,name,scopes,created_at,last_used_at,revoked_at
FROM api_tokens ORDER BY (revoked_at IS NULL) DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*APIToken{}
	for rows.Next() {
		var item APIToken
		var lastUsed, revoked sql.NullTime
		if err := rows.Scan(&item.ID, &item.Name, &item.Scopes, &item.CreatedAt, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			item.LastUsedAt = &t
		}
		if revoked.Valid {
			t := revoked.Time
			item.RevokedAt = &t
		}
		out = append(out, &item)
	}
	return out, rows.Err()
}

// ValidateAPIToken returns the token's display name when the hash matches an
// active token, and refreshes last_used_at best-effort.
func (d *DB) ValidateAPIToken(token string) (string, bool, error) {
	var name string
	err := d.QueryRow(`SELECT name FROM api_tokens WHERE token_hash=$1 AND revoked_at IS NULL`, HashAPIToken(token)).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_, _ = d.Exec(`UPDATE api_tokens SET last_used_at=now() WHERE token_hash=$1`, HashAPIToken(token))
	return name, true, nil
}

func (d *DB) RevokeAPIToken(id int64) (int64, error) {
	res, err := d.Exec(`UPDATE api_tokens SET revoked_at=COALESCE(revoked_at, now()) WHERE id=$1`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
