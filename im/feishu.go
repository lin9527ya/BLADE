package im

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

type feishuConnector struct {
	cfg    Config
	client *lark.Client
}

func newFeishu(cfg Config) (Connector, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return &feishuConnector{cfg: cfg, client: lark.NewClient(cfg.AppID, cfg.AppSecret)}, nil
}

func (c *feishuConnector) Probe(ctx context.Context) error {
	return validateConfig(c.cfg)
}

func (c *feishuConnector) Run(ctx context.Context, cb Callbacks) error {
	verificationToken, _ := c.cfg.Extra["verification_token"].(string)
	encryptKey, _ := c.cfg.Extra["encrypt_key"].(string)
	handler := dispatcher.NewEventDispatcher(verificationToken, encryptKey).OnP2MessageReceiveV1(func(cctx context.Context, event *larkim.P2MessageReceiveV1) error {
		if event == nil || event.Event == nil || event.Event.Message == nil {
			return nil
		}
		msg := event.Event.Message
		if msg.MessageType == nil || *msg.MessageType != larkim.MsgTypeText {
			return nil
		}
		if event.Event.Sender != nil && event.Event.Sender.SenderType != nil && *event.Event.Sender.SenderType == "bot" {
			return nil
		}
		text := feishuText(msg.Content)
		if text == "" {
			return nil
		}
		chatType := "private"
		if msg.ChatType != nil && *msg.ChatType == "group" {
			chatType = "group"
		}
		senderID, senderName := feishuSender(event)
		meta, _ := json.Marshal(map[string]string{"message_id": deref(msg.MessageId)})
		inc := Incoming{
			ConnectionID: c.cfg.ConnectionID,
			Platform:     PlatformFeishu,
			MessageID:    deref(msg.MessageId),
			ChatID:       deref(msg.ChatId),
			ChatType:     chatType,
			SenderID:     senderID,
			SenderName:   senderName,
			Text:         text,
			CreateAt:     feishuCreateAt(msg.CreateTime),
			ReplyMeta:    meta,
		}
		if cb.OnMessage != nil {
			if err := cb.OnMessage(cctx, inc); err != nil {
				return nil // ACK the platform even when local processing failed
			}
		}
		return nil
	})
	wsCli := larkws.NewClient(c.cfg.AppID, c.cfg.AppSecret,
		larkws.WithEventHandler(handler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
		larkws.WithAutoReconnect(true),
	)
	wsCli.SetOnReady(func() { reportStatus(cb, "connected", nil) })
	wsCli.SetOnError(func(err error) { reportStatus(cb, "error", err) })
	wsCli.SetOnDisconnected(func() { reportStatus(cb, "connecting", nil) })
	reportStatus(cb, "connecting", nil)
	err := wsCli.Start(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (c *feishuConnector) Send(ctx context.Context, out Outgoing) error {
	var meta struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(out.ReplyMeta, &meta); err != nil {
		return fmt.Errorf("解析飞书回复地址失败: %w", err)
	}
	if strings.TrimSpace(meta.MessageID) == "" {
		return errors.New("飞书回复缺少 message_id")
	}
	content, err := json.Marshal(map[string]string{"text": out.Text})
	if err != nil {
		return err
	}
	body := larkim.NewReplyMessageReqBodyBuilder().
		MsgType(larkim.MsgTypeText).
		Content(string(content)).
		Uuid("blade-" + meta.MessageID).
		Build()
	req := larkim.NewReplyMessageReqBuilder().MessageId(meta.MessageID).Body(body).Build()
	resp, err := c.client.Im.Message.Reply(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("飞书回复失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func (c *feishuConnector) Close() error { return nil }

func feishuText(content *string) string {
	if content == nil {
		return ""
	}
	var body struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(*content), &body) != nil {
		return ""
	}
	return stripMentions(body.Text)
}

func feishuSender(event *larkim.P2MessageReceiveV1) (string, string) {
	if event == nil || event.Event == nil || event.Event.Sender == nil {
		return "", ""
	}
	s := event.Event.Sender
	name := ""
	if s.SenderId != nil {
		if s.SenderId.OpenId != nil {
			name = *s.SenderId.OpenId
		} else if s.SenderId.UserId != nil {
			name = *s.SenderId.UserId
		}
	}
	return name, name
}

func feishuCreateAt(raw *string) time.Time {
	if raw == nil {
		return time.Now()
	}
	var ms int64
	if _, err := fmt.Sscan(*raw, &ms); err != nil || ms <= 0 {
		return time.Now()
	}
	return time.UnixMilli(ms)
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
