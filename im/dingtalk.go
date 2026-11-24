package im

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
)

type dingTalkConnector struct {
	cfg     Config
	replier *chatbot.ChatbotReplier
}

func newDingTalk(cfg Config) (Connector, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return &dingTalkConnector{cfg: cfg, replier: chatbot.NewChatbotReplier()}, nil
}

func (c *dingTalkConnector) newClient() *client.StreamClient {
	return client.NewStreamClient(
		client.WithAppCredential(client.NewAppCredentialConfig(c.cfg.AppID, c.cfg.AppSecret)),
		client.WithAutoReconnect(true),
	)
}

func (c *dingTalkConnector) Probe(ctx context.Context) error {
	cli := c.newClient()
	if err := cli.CheckConfigValid(); err != nil {
		return err
	}
	_, err := cli.GetConnectionEndpoint(ctx)
	return err
}

func (c *dingTalkConnector) Run(ctx context.Context, cb Callbacks) error {
	cli := c.newClient()
	if err := cli.CheckConfigValid(); err != nil {
		return err
	}
	cli.RegisterChatBotCallbackRouter(func(cctx context.Context, data *chatbot.BotCallbackDataModel) ([]byte, error) {
		if data == nil || strings.TrimSpace(data.Text.Content) == "" {
			return []byte(""), nil
		}
		text := stripMentions(data.Text.Content)
		if text == "" {
			return []byte(""), nil
		}
		chatType := "private"
		if data.ConversationType == "2" {
			chatType = "group"
		}
		sender := data.SenderStaffId
		if sender == "" {
			sender = data.SenderId
		}
		meta, _ := json.Marshal(map[string]any{
			"session_webhook": data.SessionWebhook,
			"expired_at":      data.SessionWebhookExpiredTime,
		})
		inc := Incoming{
			ConnectionID: c.cfg.ConnectionID,
			Platform:     PlatformDingTalk,
			MessageID:    data.MsgId,
			ChatID:       data.ConversationId,
			ChatType:     chatType,
			SenderID:     sender,
			SenderName:   data.SenderNick,
			Text:         text,
			CreateAt:     time.UnixMilli(data.CreateAt),
			ReplyMeta:    meta,
		}
		if cb.OnMessage != nil {
			if err := cb.OnMessage(cctx, inc); err != nil {
				log.Printf("[im/dingtalk] 处理消息 %s 失败: %v", data.MsgId, err)
			}
		}
		// Empty ACK immediately; all task work happens in the bounded server queue.
		return []byte(""), nil
	})
	reportStatus(cb, "connecting", nil)
	// Fetching the endpoint verifies app credentials before Start blocks forever.
	// It also gives the UI a truthful "connected" transition without waiting for
	// the first user message.
	if _, err := cli.GetConnectionEndpoint(ctx); err != nil {
		return err
	}
	reportStatus(cb, "connected", nil)
	err := cli.Start(ctx)
	cli.Close()
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (c *dingTalkConnector) Send(ctx context.Context, out Outgoing) error {
	var meta struct {
		SessionWebhook string `json:"session_webhook"`
	}
	if err := json.Unmarshal(out.ReplyMeta, &meta); err != nil {
		return fmt.Errorf("解析钉钉回复地址失败: %w", err)
	}
	if strings.TrimSpace(meta.SessionWebhook) == "" {
		return errors.New("钉钉回复缺少 sessionWebhook（可能已过期）")
	}
	return c.replier.SimpleReplyText(ctx, meta.SessionWebhook, []byte(out.Text))
}

func (c *dingTalkConnector) Close() error { return nil }
