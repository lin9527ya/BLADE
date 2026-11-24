package im

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestStripMentions(t *testing.T) {
	cases := map[string]string{
		"@机器人 /tasks":        "/tasks",
		"@_user_1 /show 42":  "/show 42",
		"  /help  ":          "/help",
		"普通自然语言":             "普通自然语言",
		"@bot @_user_2 创建任务": "创建任务",
	}
	for in, want := range cases {
		if got := stripMentions(in); got != want {
			t.Fatalf("stripMentions(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAllowGroup(t *testing.T) {
	if AllowGroup(nil) {
		t.Fatal("nil extra must not allow groups")
	}
	if AllowGroup(map[string]any{"allow_group": false}) {
		t.Fatal("false must not allow groups")
	}
	if !AllowGroup(map[string]any{"allow_group": true}) {
		t.Fatal("true must allow groups")
	}
	if !AllowGroup(map[string]any{"allow_group": "true"}) {
		t.Fatal("string true must allow groups")
	}
}

func TestFeishuText(t *testing.T) {
	raw := `{"text":"@_user_1 /tasks"}`
	if got := feishuText(&raw); got != "/tasks" {
		t.Fatalf("feishuText=%q", got)
	}
	if got := feishuText(nil); got != "" {
		t.Fatalf("nil content should be empty, got %q", got)
	}
	bad := "not-json"
	if got := feishuText(&bad); got != "" {
		t.Fatalf("bad json should be empty, got %q", got)
	}
}

func TestFakeConnectorRoundTrip(t *testing.T) {
	f := NewFakeConnector()
	got := make(chan Incoming, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = f.Run(ctx, Callbacks{OnMessage: func(_ context.Context, in Incoming) error {
			got <- in
			return nil
		}})
	}()
	meta, _ := json.Marshal(map[string]string{"session_webhook": "https://example.invalid/hook"})
	f.Incoming <- Incoming{ConnectionID: 1, Platform: PlatformDingTalk, MessageID: "m1", SenderID: "u1", Text: "/tasks", ReplyMeta: meta}
	select {
	case in := <-got:
		if in.MessageID != "m1" || in.Text != "/tasks" {
			t.Fatalf("unexpected incoming: %+v", in)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake connector did not deliver message")
	}
	if err := f.Send(ctx, Outgoing{ConnectionID: 1, Text: "ok", ReplyMeta: meta}); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-f.Outgoing:
		if out.Text != "ok" {
			t.Fatalf("unexpected outgoing: %+v", out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake connector did not send reply")
	}
}
