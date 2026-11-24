package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
	"github.com/lin9527ya/blade/im"
)

func newIMFixture(t *testing.T, role string) (*Server, *im.FakeConnector, *db.IMConnection, *db.IMAllowedUser) {
	t.Helper()
	s, _, _ := trafficEvidenceServer(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	conn, err := s.m.PG().CreateIMConnection(t.Context(), &db.IMConnection{
		Name: "im-test", Platform: im.PlatformDingTalk, Enabled: true,
		AppID: "app-" + suffix, AppSecret: "secret", Extra: []byte(`{}`), Status: "connected",
	})
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.m.PG().CreateIMAllowedUser(t.Context(), &db.IMAllowedUser{
		Platform: im.PlatformDingTalk, UserID: "user-" + suffix, DisplayName: "tester", Role: role, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.m.PG().DeleteIMAllowedUser(context.Background(), user.ID)
		_, _ = s.m.PG().Exec(`DELETE FROM conversations WHERE id IN (SELECT conversation_id FROM im_conversations WHERE connection_id=$1)`, conn.ID)
		_, _ = s.m.PG().DeleteIMConnection(context.Background(), conn.ID)
	})
	fake := im.NewFakeConnector()
	s.imSvc.mu.Lock()
	s.imSvc.runtimes[conn.ID] = &imRuntime{connector: fake}
	s.imSvc.mu.Unlock()
	t.Cleanup(func() {
		s.imSvc.mu.Lock()
		delete(s.imSvc.runtimes, conn.ID)
		s.imSvc.mu.Unlock()
	})
	return s, fake, conn, user
}

func imIncoming(conn *db.IMConnection, user *db.IMAllowedUser, msgID, text string) im.Incoming {
	meta := []byte(`{"session_webhook":"https://example.invalid/hook"}`)
	return im.Incoming{
		ConnectionID: conn.ID, Platform: conn.Platform, MessageID: msgID,
		ChatID: "chat-1", ChatType: "private", SenderID: user.UserID, SenderName: user.DisplayName,
		Text: text, CreateAt: time.Now(), ReplyMeta: meta,
	}
}

func drainIMJob(t *testing.T, s *Server) imJob {
	t.Helper()
	select {
	case job := <-s.imSvc.queue:
		return job
	default:
		t.Fatal("no IM job queued")
		return imJob{}
	}
}

func TestIMDuplicateMessageProcessedOnce(t *testing.T) {
	s, fake, conn, user := newIMFixture(t, "operator")
	ctx := t.Context()
	in := imIncoming(conn, user, "dup-1", "/tasks")
	if err := s.imSvc.handleIncoming(ctx, conn.ID, in); err != nil {
		t.Fatal(err)
	}
	if err := s.imSvc.handleIncoming(ctx, conn.ID, in); err != nil {
		t.Fatal(err)
	}
	job := drainIMJob(t, s)
	select {
	case extra := <-s.imSvc.queue:
		t.Fatalf("duplicate message queued twice: %+v", extra)
	default:
	}
	s.imSvc.processJob(ctx, job)
	var count int
	if err := s.m.PG().QueryRow(`SELECT count(*) FROM im_messages WHERE connection_id=$1 AND platform_msg_id=$2`, conn.ID, "dup-1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("duplicate rows=%d", count)
	}
	select {
	case out := <-fake.Outgoing:
		if !strings.Contains(out.Text, "#") || !strings.Contains(out.Text, "[") {
			t.Fatalf("unexpected tasks reply: %q", out.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply sent")
	}
}

func TestIMUnauthorizedUserAuditedNotExecuted(t *testing.T) {
	s, fake, conn, user := newIMFixture(t, "viewer")
	ctx := t.Context()
	before := len(s.m.List())
	in := imIncoming(conn, user, "unauth-1", "/new example.com")
	in.SenderID = "unknown-user"
	if err := s.imSvc.handleIncoming(ctx, conn.ID, in); err != nil {
		t.Fatal(err)
	}
	select {
	case job := <-s.imSvc.queue:
		t.Fatalf("unauthorized message was queued: %+v", job)
	default:
	}
	if got := len(s.m.List()); got != before {
		t.Fatalf("unauthorized message created a task: %d -> %d", before, got)
	}
	var state, reason string
	if err := s.m.PG().QueryRow(`SELECT state,error FROM im_messages WHERE connection_id=$1 AND platform_msg_id=$2`, conn.ID, "unauth-1").Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "ignored" || !strings.Contains(reason, "未授权") {
		t.Fatalf("state=%q reason=%q", state, reason)
	}
	select {
	case out := <-fake.Outgoing:
		if !strings.Contains(out.Text, "未授权") {
			t.Fatalf("unexpected reply: %q", out.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no unauthorized reply sent")
	}
}

func TestIMViewerCannotCreateTask(t *testing.T) {
	s, fake, conn, user := newIMFixture(t, "viewer")
	ctx := t.Context()
	before := len(s.m.List())
	if err := s.imSvc.handleIncoming(ctx, conn.ID, imIncoming(conn, user, "viewer-1", "/new example.com")); err != nil {
		t.Fatal(err)
	}
	job := drainIMJob(t, s)
	s.imSvc.processJob(ctx, job)
	if got := len(s.m.List()); got != before {
		t.Fatalf("viewer created a task: %d -> %d", before, got)
	}
	select {
	case out := <-fake.Outgoing:
		if !strings.Contains(out.Text, "只读") {
			t.Fatalf("unexpected reply: %q", out.Text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply sent")
	}
}
