package db

import "time"

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

func (d *DB) ListAuditEvents(limit int) ([]*AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := d.Query(`SELECT id,actor,method,path,status,ip,user_agent,created_at
FROM audit_events ORDER BY id DESC LIMIT $1`, limit)
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
