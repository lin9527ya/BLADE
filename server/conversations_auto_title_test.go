package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lin9527ya/blade/db"
)

func TestAttachmentTitleHelpers(t *testing.T) {
	if got := attachmentDefaultTitle(nil); got != "附件消息" {
		t.Fatalf("empty attachments default = %q, want 附件消息", got)
	}
	if got := attachmentTitlePromptSeed(nil); got != "" {
		t.Fatalf("empty attachments seed = %q, want empty", got)
	}

	single := []chatAttachment{{Name: "../uploads/burp_report.xml", Path: "uploads/burp_report.xml"}}
	if got := attachmentDefaultTitle(single); got != "burp_report.xml" {
		t.Fatalf("single attachment default = %q", got)
	}
	if got := attachmentTitlePromptSeed(single); !strings.Contains(got, "burp_report.xml") || strings.Contains(got, "..") {
		t.Fatalf("single attachment seed = %q", got)
	}

	multi := []chatAttachment{
		{Name: "access.log"},
		{Name: "users.csv"},
		{Name: "notes.md"},
	}
	if got := attachmentDefaultTitle(multi); got != "access.log 等 3 个附件" {
		t.Fatalf("multi attachment default = %q", got)
	}
	seed := attachmentTitlePromptSeed(multi)
	if !strings.Contains(seed, "access.log") || !strings.Contains(seed, "users.csv") || !strings.Contains(seed, "notes.md") {
		t.Fatalf("multi attachment seed = %q", seed)
	}
}

func TestDeleteConversationRemovesNoaArchive(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)

	cSingle, err := m.pg.CreateConversation("chat-noa-test", "single", nil)
	if err != nil {
		t.Fatal(err)
	}
	cBatch, err := m.pg.CreateConversation("chat-noa-test", "batch", nil)
	if err != nil {
		t.Fatal(err)
	}
	cKeep, err := m.pg.CreateConversation("chat-noa-test", "keep", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.DeleteConversations([]int64{cSingle.ID, cBatch.ID, cKeep.ID})
	})

	singleNoa := filepath.Join(m.dir, "noa", "chat-"+s.convBusyKey(cSingle.ID))
	batchNoa := filepath.Join(m.dir, "noa", "chat-"+s.convBusyKey(cBatch.ID))
	keepNoa := filepath.Join(m.dir, "noa", "chat-"+s.convBusyKey(cKeep.ID))
	expNoa := filepath.Join(m.dir, "noa", "exp7-planner")
	for _, dir := range []string{singleNoa, batchNoa, keepNoa, expNoa} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"version":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reqSingle := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/conversations/%d", cSingle.ID), nil)
	reqSingle.SetPathValue("id", strconv.FormatInt(cSingle.ID, 10))
	recSingle := httptest.NewRecorder()
	s.pgDeleteConversation(recSingle, reqSingle)
	if recSingle.Code != http.StatusOK {
		t.Fatalf("pgDeleteConversation code=%d body=%s", recSingle.Code, recSingle.Body.String())
	}
	if _, err := os.Stat(singleNoa); !os.IsNotExist(err) {
		t.Fatalf("single deleted conversation noa dir should be removed: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"ids": []int64{cBatch.ID}})
	reqBatch := httptest.NewRequest(http.MethodPost, "/api/conversations/batch-delete", bytes.NewReader(body))
	recBatch := httptest.NewRecorder()
	s.pgDeleteConversationsBatch(recBatch, reqBatch)
	if recBatch.Code != http.StatusOK {
		t.Fatalf("pgDeleteConversationsBatch code=%d body=%s", recBatch.Code, recBatch.Body.String())
	}
	if _, err := os.Stat(batchNoa); !os.IsNotExist(err) {
		t.Fatalf("batch deleted conversation noa dir should be removed: %v", err)
	}
	if _, err := os.Stat(keepNoa); err != nil {
		t.Fatalf("sibling conversation noa dir must remain: %v", err)
	}
	if _, err := os.Stat(expNoa); err != nil {
		t.Fatalf("task exploration noa dir must remain: %v", err)
	}
}

func TestAutoTitleConversationSendsSessionHeaderUsesPoolAndRecordsWorker(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	td := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, m, td, td, td)

	prevPool := m.LLMPoolEnabled()
	prevBindFallback := m.LLMPoolBindFallback()
	var prevActiveID int64
	if act, err := m.pg.ActiveProfile(); err == nil && act != nil {
		prevActiveID = act.ID
	}
	t.Cleanup(func() {
		_ = m.SetLLMPoolEnabled(prevPool)
		_ = m.SetLLMPoolBindFallback(prevBindFallback)
		if prevActiveID > 0 {
			_ = m.pg.SetActiveProfile(prevActiveID)
		}
	})

	var (
		mu          sync.Mutex
		seenHeaders []string
		primaryHits int
		backupHits  int
	)
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		primaryHits++
		seenHeaders = append(seenHeaders, "primary:"+r.Header.Get("x-opencode-session"))
		mu.Unlock()
		if strings.TrimSpace(r.Header.Get("x-opencode-session")) == "" {
			http.Error(w, `{"error":"MissingSessionID"}`, http.StatusBadRequest)
			return
		}
		// Trigger failover by returning rate-limit 429 on primary.
		http.Error(w, `{"error":{"message":"rate limit"}}`, http.StatusTooManyRequests)
	}))
	defer primarySrv.Close()

	backupSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		backupHits++
		seenHeaders = append(seenHeaders, "backup:"+r.Header.Get("x-opencode-session"))
		mu.Unlock()
		if strings.TrimSpace(r.Header.Get("x-opencode-session")) == "" {
			http.Error(w, `{"error":"MissingSessionID"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-title",
			"object":"chat.completion",
			"choices":[{"index":0,"message":{"role":"assistant","content":"<think>总结输入</think>\n### **登录接口越权排查**"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30}
		}`))
	}))
	defer backupSrv.Close()

	primaryID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name:             "auto-title-primary-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Format:           "openai",
		Model:            "gpt-test",
		BaseURL:          primarySrv.URL + "/v1",
		APIKey:           "sk-primary",
		SessionHeaderKey: "x-opencode-session",
		Priority:         10,
	})
	if err != nil {
		t.Fatal(err)
	}
	backupID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name:             "auto-title-backup-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		Format:           "openai",
		Model:            "gpt-test-backup",
		BaseURL:          backupSrv.URL + "/v1",
		APIKey:           "sk-backup",
		SessionHeaderKey: "x-opencode-session",
		Priority:         20,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM llm_health WHERE profile_id IN ($1,$2)`, primaryID, backupID)
		_, _ = m.pg.Exec(`UPDATE llm_profiles SET is_default=false WHERE id IN ($1,$2)`, primaryID, backupID)
		_ = m.pg.DeleteProfile(primaryID)
		_ = m.pg.DeleteProfile(backupID)
	})
	if err := m.pg.SetActiveProfile(primaryID); err != nil {
		t.Fatal(err)
	}
	if err := m.SetLLMPoolEnabled(true); err != nil {
		t.Fatal(err)
	}

	conv, err := m.pg.CreateConversation("auto-title-test-agent", "新对话", &primaryID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.pg.Exec(`DELETE FROM llm_usage WHERE worker='auto-title'`)
		_ = m.pg.DeleteConversation(conv.ID)
	})

	// When bind_fallback is enabled, pinned primary profile fails over to backup cleanly
	// and sends x-opencode-session=conv-<id> on every HTTP hop.
	if err := m.SetLLMPoolBindFallback(true); err != nil {
		t.Fatal(err)
	}
	const seedTitle = "帮我审计 /api/login 是否存在越权漏洞"
	if err := m.pg.RenameConversation(conv.ID, seedTitle); err != nil {
		t.Fatal(err)
	}
	s.autoTitleConversation(conv.ID, conv.AgentKey, conv.LLMProfileID, seedTitle, seedTitle)

	wantSession := s.convBusyKey(conv.ID)
	deadline := time.Now().Add(8 * time.Second)
	var updatedTitle string
	for time.Now().Before(deadline) {
		gotConv, gerr := m.pg.GetConversation(conv.ID)
		if gerr == nil && gotConv != nil && gotConv.Title != seedTitle {
			updatedTitle = gotConv.Title
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if updatedTitle != "登录接口越权排查" {
		mu.Lock()
		ph, bh, sh := primaryHits, backupHits, append([]string(nil), seenHeaders...)
		mu.Unlock()
		t.Fatalf("auto-title result = %q, want 登录接口越权排查 (primaryHits=%d backupHits=%d headers=%v)", updatedTitle, ph, bh, sh)
	}

	mu.Lock()
	defer mu.Unlock()
	if primaryHits == 0 || backupHits == 0 {
		t.Fatalf("expected pool failover primary->backup, hits primary=%d backup=%d", primaryHits, backupHits)
	}
	expectedPrefixPrimary := "primary:" + wantSession
	expectedPrefixBackup := "backup:" + wantSession
	hasP, hasB := false, false
	for _, h := range seenHeaders {
		if h == expectedPrefixPrimary {
			hasP = true
		}
		if h == expectedPrefixBackup {
			hasB = true
		}
	}
	if !hasP || !hasB {
		t.Fatalf("expected session header %q on both hops, got %v", wantSession, seenHeaders)
	}
}
