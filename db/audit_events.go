package db

import (
	"fmt"
	"strings"
	"time"
)

type AuditEvent struct {
	ID        int64     `json:"id"`
	Actor     string    `json:"actor"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *DB) InsertAuditEvent(actor, method, path string, status int, ip, userAgent string) error {
	_, err := d.Exec(`INSERT INTO audit_events(actor,method,path,status,ip,user_agent)
VALUES($1,$2,$3,$4,$5,$6)`, actor, method, path, status, ip, userAgent)
	return err
}

type AuditFilter struct {
	Actor  string
	Method string
	Query  string
	Limit  int
}

func (d *DB) ListAuditEvents(limit int) ([]*AuditEvent, error) {
	return d.ListAuditEventsFiltered(AuditFilter{Limit: limit})
}

func (d *DB) ListAuditEventsFiltered(f AuditFilter) ([]*AuditEvent, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 10000 {
		f.Limit = 10000
	}
	where := "1=1"
	args := []any{}
	if v := strings.TrimSpace(f.Actor); v != "" {
		args = append(args, "%"+v+"%")
		where += fmt.Sprintf(" AND actor ILIKE $%d", len(args))
	}
	if v := strings.TrimSpace(f.Method); v != "" {
		args = append(args, strings.ToUpper(v))
		where += fmt.Sprintf(" AND method = $%d", len(args))
	}
	if v := strings.TrimSpace(f.Query); v != "" {
		args = append(args, "%"+v+"%")
		where += fmt.Sprintf(" AND path ILIKE $%d", len(args))
	}
	args = append(args, f.Limit)
	rows, err := d.Query(`SELECT id,actor,method,path,status,ip,user_agent,created_at
FROM audit_events WHERE `+where+` ORDER BY id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.Actor, &e.Method, &e.Path, &e.Status, &e.IP, &e.UserAgent, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
