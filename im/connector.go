// Package im adapts DingTalk Stream Mode and Feishu long-connection events into
// one inbound/outbound message shape. Platform SDKs stay in this package; the
// server only sees Connector, Incoming and Outgoing.
package im

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	PlatformDingTalk = "dingtalk"
	PlatformFeishu   = "feishu"
)

type Config struct {
	ConnectionID int64
	Name         string
	Platform     string
	AppID        string
	AppSecret    string
	Extra        map[string]any
}

type Incoming struct {
	ConnectionID int64
	Platform     string
	MessageID    string
	ChatID       string
	ChatType     string // private | group
	SenderID     string
	SenderName   string
	Text         string
	CreateAt     time.Time
	ReplyMeta    json.RawMessage
}

type Outgoing struct {
	ConnectionID int64
	ChatID       string
	ReplyTo      string
	Text         string
	ReplyMeta    json.RawMessage
}

type Callbacks struct {
	OnMessage func(context.Context, Incoming) error
	OnStatus  func(status string, err error)
}

type Connector interface {
	Run(ctx context.Context, cb Callbacks) error
	Send(ctx context.Context, out Outgoing) error
	Probe(ctx context.Context) error
	Close() error
}

type Factory func(Config) (Connector, error)

func New(cfg Config) (Connector, error) {
	switch cfg.Platform {
	case PlatformDingTalk:
		return newDingTalk(cfg)
	case PlatformFeishu:
		return newFeishu(cfg)
	default:
		return nil, fmt.Errorf("不支持的 IM 平台: %s", cfg.Platform)
	}
}

func validateConfig(cfg Config) error {
	if strings.TrimSpace(cfg.AppID) == "" {
		return fmt.Errorf("app_id 不能为空")
	}
	if strings.TrimSpace(cfg.AppSecret) == "" {
		return fmt.Errorf("app_secret 不能为空")
	}
	return nil
}

func reportStatus(cb Callbacks, status string, err error) {
	if cb.OnStatus != nil {
		cb.OnStatus(status, err)
	}
}

// AllowGroup reports whether this connection may answer group chats. Private
// chats are always allowed for an allowlisted user; groups are opt-in.
func AllowGroup(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	switch v := extra["allow_group"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

var mentionPlaceholder = regexp.MustCompile(`@_user_\d+`)

func stripMentions(text string) string {
	text = mentionPlaceholder.ReplaceAllString(text, " ")
	fields := strings.Fields(text)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	return strings.TrimSpace(strings.Join(fields, " "))
}
