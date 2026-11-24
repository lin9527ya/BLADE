package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// IMConnection is one platform bot connection. AppSecret is stored like the
// notification/LLM credentials: plaintext in the database, masked at the API
// boundary, never logged.
type IMConnection struct {
	ID              int64           `json:"id"`
	Name            string          `json:"name"`
	Platform        string          `json:"platform"`
	Enabled         bool            `json:"enabled"`
	AppID           string          `json:"app_id"`
	AppSecret       string          `json:"-"`
	Extra           json.RawMessage `json:"extra"`
	Status          string          `json:"status"`
	LastError       string          `json:"last_error"`
	LastConnectedAt *time.Time      `json:"last_connected_at,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

const imConnectionCols = `id,name,platform,enabled,app_id,app_secret,extra,status,last_error,last_connected_at,created_at,updated_at`

func scanIMConnection(row interface{ Scan(...any) error }) (*IMConnection, error) {
	var c IMConnection
	var extra []byte
	var connected sql.NullTime
	if err := row.Scan(&c.ID, &c.Name, &c.Platform, &c.Enabled, &c.AppID, &c.AppSecret,
		&extra, &c.Status, &c.LastError, &connected, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if len(extra) > 0 {
		c.Extra = append(json.RawMessage(nil), extra...)
	} else {
		c.Extra = json.RawMessage(`{}`)
	}
	if connected.Valid {
		t := connected.Time
		c.LastConnectedAt = &t
	}
	return &c, nil
}

func (d *DB) ListIMConnections() ([]*IMConnection, error) {
	rows, err := d.Query(`SELECT ` + imConnectionCols + ` FROM im_connections ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*IMConnection{}
	for rows.Next() {
		c, err := scanIMConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) GetIMConnection(id int64) (*IMConnection, error) {
	c, err := scanIMConnection(d.QueryRow(`SELECT `+imConnectionCols+` FROM im_connections WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func (d *DB) CreateIMConnection(ctx context.Context, c *IMConnection) (*IMConnection, error) {
	if c.Extra == nil {
		c.Extra = json.RawMessage(`{}`)
	}
	out, err := scanIMConnection(d.QueryRowContext(ctx, `INSERT INTO im_connections
(name,platform,enabled,app_id,app_secret,extra,status,last_error)
VALUES($1,$2,$3,$4,$5,$6,COALESCE(NULLIF($7,''),'disabled'),$8)
RETURNING `+imConnectionCols,
		c.Name, c.Platform, c.Enabled, c.AppID, c.AppSecret, string(c.Extra), c.Status, c.LastError))
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) UpdateIMConnection(ctx context.Context, c *IMConnection) (*IMConnection, error) {
	if c.Extra == nil {
		c.Extra = json.RawMessage(`{}`)
	}
	out, err := scanIMConnection(d.QueryRowContext(ctx, `UPDATE im_connections
SET name=$2,platform=$3,enabled=$4,app_id=$5,app_secret=$6,extra=$7
WHERE id=$1 RETURNING `+imConnectionCols,
		c.ID, c.Name, c.Platform, c.Enabled, c.AppID, c.AppSecret, string(c.Extra)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return out, err
}

func (d *DB) DeleteIMConnection(ctx context.Context, id int64) (bool, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM im_connections WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (d *DB) SetIMConnectionStatus(ctx context.Context, id int64, status, lastError string, connected bool) error {
	_, err := d.ExecContext(ctx, `UPDATE im_connections
SET status=$2,last_error=$3,last_connected_at=CASE WHEN $4 THEN now() ELSE last_connected_at END
WHERE id=$1`, id, status, lastError, connected)
	return err
}

// IMAllowedUser is one platform sender allowed to command BLADE.
type IMAllowedUser struct {
	ID          int64     `json:"id"`
	Platform    string    `json:"platform"`
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"` // viewer | operator
	Enabled     bool      `json:"enabled"`
	Note        string    `json:"note"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const imAllowedUserCols = `id,platform,user_id,display_name,role,enabled,note,created_at,updated_at`

func scanIMAllowedUser(row interface{ Scan(...any) error }) (*IMAllowedUser, error) {
	var u IMAllowedUser
	if err := row.Scan(&u.ID, &u.Platform, &u.UserID, &u.DisplayName, &u.Role, &u.Enabled, &u.Note, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DB) ListIMAllowedUsers() ([]*IMAllowedUser, error) {
	rows, err := d.Query(`SELECT ` + imAllowedUserCols + ` FROM im_allowed_users ORDER BY platform,user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*IMAllowedUser{}
	for rows.Next() {
		u, err := scanIMAllowedUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) GetIMAllowedUser(platform, userID string) (*IMAllowedUser, error) {
	u, err := scanIMAllowedUser(d.QueryRow(`SELECT `+imAllowedUserCols+` FROM im_allowed_users WHERE platform=$1 AND user_id=$2`, platform, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

func (d *DB) CreateIMAllowedUser(ctx context.Context, u *IMAllowedUser) (*IMAllowedUser, error) {
	out, err := scanIMAllowedUser(d.QueryRowContext(ctx, `INSERT INTO im_allowed_users
(platform,user_id,display_name,role,enabled,note) VALUES($1,$2,$3,$4,$5,$6)
RETURNING `+imAllowedUserCols, u.Platform, u.UserID, u.DisplayName, u.Role, u.Enabled, u.Note))
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) UpdateIMAllowedUser(ctx context.Context, u *IMAllowedUser) (*IMAllowedUser, error) {
	out, err := scanIMAllowedUser(d.QueryRowContext(ctx, `UPDATE im_allowed_users
SET display_name=$2,role=$3,enabled=$4,note=$5 WHERE id=$1
RETURNING `+imAllowedUserCols, u.ID, u.DisplayName, u.Role, u.Enabled, u.Note))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return out, err
}

func (d *DB) DeleteIMAllowedUser(ctx context.Context, id int64) (bool, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM im_allowed_users WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// IMMessage is one inbound platform message. PlatformMsgID is the idempotency key.
type IMMessage struct {
	ID             int64      `json:"id"`
	ConnectionID   int64      `json:"connection_id"`
	ConnectionName string     `json:"connection_name,omitempty"`
	PlatformMsgID  string     `json:"platform_msg_id"`
	ChatID         string     `json:"chat_id"`
	ChatType       string     `json:"chat_type"`
	SenderID       string     `json:"sender_id"`
	SenderName     string     `json:"sender_name"`
	Text           string     `json:"text"`
	State          string     `json:"state"`
	TaskID         *int64     `json:"task_id,omitempty"`
	ConversationID *int64     `json:"conversation_id,omitempty"`
	Reply          string     `json:"reply"`
	Error          string     `json:"error"`
	CreatedAt      time.Time  `json:"created_at"`
	ProcessedAt    *time.Time `json:"processed_at,omitempty"`
}

func (d *DB) InsertIMMessage(ctx context.Context, m *IMMessage) (int64, bool, error) {
	state := m.State
	if state == "" {
		state = "received"
	}
	var id int64
	err := d.QueryRowContext(ctx, `INSERT INTO im_messages
(connection_id,platform_msg_id,chat_id,chat_type,sender_id,sender_name,text,state)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(connection_id,platform_msg_id) DO NOTHING
RETURNING id`, m.ConnectionID, m.PlatformMsgID, m.ChatID, m.ChatType, m.SenderID, m.SenderName, m.Text, state).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func (d *DB) UpdateIMMessage(ctx context.Context, id int64, state string, taskID, conversationID *int64, reply, errMsg string) error {
	var taskVal, convVal any
	if taskID != nil {
		taskVal = *taskID
	}
	if conversationID != nil {
		convVal = *conversationID
	}
	_, err := d.ExecContext(ctx, `UPDATE im_messages
SET state=$2,task_id=$3,conversation_id=$4,reply=$5,error=$6,
    processed_at=CASE WHEN $2 IN ('replied','failed','ignored') THEN now() ELSE processed_at END
WHERE id=$1`, id, state, taskVal, convVal, reply, errMsg)
	return err
}

func (d *DB) RecentIMMessages(ctx context.Context, limit int) ([]*IMMessage, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.QueryContext(ctx, `SELECT m.id,m.connection_id,COALESCE(c.name,''),m.platform_msg_id,
m.chat_id,m.chat_type,m.sender_id,m.sender_name,m.text,m.state,m.task_id,m.conversation_id,
m.reply,m.error,m.created_at,m.processed_at
FROM im_messages m LEFT JOIN im_connections c ON c.id=m.connection_id
ORDER BY m.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*IMMessage{}
	for rows.Next() {
		var m IMMessage
		var taskID, convID sql.NullInt64
		var processed sql.NullTime
		if err := rows.Scan(&m.ID, &m.ConnectionID, &m.ConnectionName, &m.PlatformMsgID,
			&m.ChatID, &m.ChatType, &m.SenderID, &m.SenderName, &m.Text, &m.State,
			&taskID, &convID, &m.Reply, &m.Error, &m.CreatedAt, &processed); err != nil {
			return nil, err
		}
		if taskID.Valid {
			v := taskID.Int64
			m.TaskID = &v
		}
		if convID.Valid {
			v := convID.Int64
			m.ConversationID = &v
		}
		if processed.Valid {
			t := processed.Time
			m.ProcessedAt = &t
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

// RecoverStaleIMMessages marks messages that were in flight when the process
// stopped as failed. It deliberately does not replay them: an IM command can
// create or pause a task, so automatic replay is more dangerous than a visible
// failed row.
func (d *DB) RecoverStaleIMMessages(ctx context.Context, olderThan time.Duration) (int64, error) {
	if olderThan <= 0 {
		olderThan = 10 * time.Minute
	}
	res, err := d.ExecContext(ctx, `UPDATE im_messages
SET state='failed',error='服务重启，消息未完成',processed_at=now()
WHERE state IN ('received','processing') AND created_at < $1`, time.Now().Add(-olderThan))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetOrCreateIMConversation maps one (connection, chat, sender) to a persisted
// auto-agent conversation. The advisory lock serializes first-use creation per
// mapping key, so concurrent platform retries cannot create two conversations.
func (d *DB) GetOrCreateIMConversation(ctx context.Context, connectionID int64, chatID, senderID, title string) (*Conversation, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	lockKey := fmt.Sprintf("im:%d:%s:%s", connectionID, chatID, senderID)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return nil, err
	}
	var convID int64
	err = tx.QueryRowContext(ctx, `SELECT conversation_id FROM im_conversations
WHERE connection_id=$1 AND chat_id=$2 AND sender_id=$3`, connectionID, chatID, senderID).Scan(&convID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.QueryRowContext(ctx, `INSERT INTO conversations(agent_key,title,llm_profile_id)
VALUES('auto',$1,NULL) RETURNING id`, title).Scan(&convID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO im_conversations(connection_id,chat_id,sender_id,conversation_id)
VALUES($1,$2,$3,$4)`, connectionID, chatID, senderID, convID); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE im_conversations SET last_message_at=now()
WHERE connection_id=$1 AND chat_id=$2 AND sender_id=$3`, connectionID, chatID, senderID); err != nil {
		return nil, err
	}
	c, err := scanConv(tx.QueryRowContext(ctx, `SELECT `+convCols+` FROM conversations WHERE id=$1`, convID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (d *DB) RecordIMAction(ctx context.Context, messageID int64, action string, taskID *int64, status, detail, actor string) error {
	var taskVal any
	if taskID != nil {
		taskVal = *taskID
	}
	_, err := d.ExecContext(ctx, `INSERT INTO im_actions(message_id,action,task_id,status,detail,actor)
VALUES($1,$2,$3,$4,$5,$6)`, messageID, action, taskVal, status, detail, actor)
	return err
}

// RecordIMAudit writes the same audit trail as an HTTP mutation: actor is
// im:<platform>:<user>, method is IM, path is /im/<action>.
func (d *DB) RecordIMAudit(ctx context.Context, actor, action string, status int, detail string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO audit_events(actor,method,path,status,ip,user_agent)
VALUES($1,'IM',$2,$3,$4,$5)`, actor, "/im/"+action, status, "", detail)
	return err
}
