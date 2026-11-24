package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestIMConnectionUserAndMessageIdempotency(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	conn, err := d.CreateIMConnection(ctx, &IMConnection{
		Name: "test", Platform: "dingtalk", Enabled: false,
		AppID: "app-" + suffix, AppSecret: "secret", Extra: []byte(`{"allow_group":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteIMConnection(context.Background(), conn.ID) })
	if conn.ID == 0 || conn.Status != "disabled" {
		t.Fatalf("unexpected connection: %+v", conn)
	}

	user, err := d.CreateIMAllowedUser(ctx, &IMAllowedUser{
		Platform: "dingtalk", UserID: "user-" + suffix, DisplayName: "tester", Role: "operator", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.DeleteIMAllowedUser(context.Background(), user.ID) })
	gotUser, err := d.GetIMAllowedUser("dingtalk", "user-"+suffix)
	if err != nil || gotUser == nil || gotUser.Role != "operator" {
		t.Fatalf("allowed user: %+v %v", gotUser, err)
	}

	msg := &IMMessage{
		ConnectionID: conn.ID, PlatformMsgID: "msg-" + suffix, ChatID: "chat-1",
		ChatType: "private", SenderID: user.UserID, SenderName: "tester", Text: "/tasks",
	}
	id, first, err := d.InsertIMMessage(ctx, msg)
	if err != nil || !first || id == 0 {
		t.Fatalf("first insert: id=%d first=%v err=%v", id, first, err)
	}
	_, first, err = d.InsertIMMessage(ctx, msg)
	if err != nil || first {
		t.Fatalf("duplicate insert must be ignored: first=%v err=%v", first, err)
	}
	var count int
	if err := d.QueryRow(`SELECT count(*) FROM im_messages WHERE connection_id=$1 AND platform_msg_id=$2`, conn.ID, msg.PlatformMsgID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate message rows=%d", count)
	}
	taskID := int64(99)
	if err := d.UpdateIMMessage(ctx, id, "replied", &taskID, nil, "done", ""); err != nil {
		t.Fatal(err)
	}
	recent, err := d.RecentIMMessages(ctx, 20)
	if err != nil || len(recent) == 0 || recent[0].State != "replied" {
		t.Fatalf("recent messages: %+v %v", recent, err)
	}

	conv, err := d.GetOrCreateIMConversation(ctx, conn.ID, "chat-1", user.UserID, "IM test")
	if err != nil || conv == nil || conv.ID == 0 {
		t.Fatalf("conversation: %+v %v", conv, err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID) })
	again, err := d.GetOrCreateIMConversation(ctx, conn.ID, "chat-1", user.UserID, "IM test")
	if err != nil || again.ID != conv.ID {
		t.Fatalf("mapping should be stable: %+v vs %+v (%v)", conv, again, err)
	}

	stale := &IMMessage{
		ConnectionID: conn.ID, PlatformMsgID: "stale-" + suffix, ChatID: "chat-1",
		ChatType: "private", SenderID: user.UserID, Text: "old",
	}
	staleID, _, err := d.InsertIMMessage(ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE im_messages SET created_at=now()-interval '20 minutes' WHERE id=$1`, staleID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecoverStaleIMMessages(ctx, 10*time.Minute); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := d.QueryRow(`SELECT state FROM im_messages WHERE id=$1`, staleID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("stale message state=%q", state)
	}
	if err := d.RecordIMAction(ctx, id, "command:tasks", &taskID, "replied", "done", "im:dingtalk:u"); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordIMAudit(ctx, "im:dingtalk:u", "command:tasks", 200, "done"); err != nil {
		t.Fatal(err)
	}
}
