package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lin9527ya/blade/notify"
)

func decodeIMJSON[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

func TestIMAPISecretMaskingAndUserLifecycle(t *testing.T) {
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.PG()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	appID := "app-" + suffix
	secret := "super-secret-value"

	w := request(http.MethodPost, "/api/im/connections", fmt.Sprintf(`{
		"name":"dingtalk test","platform":"dingtalk","app_id":%q,"app_secret":%q,"enabled":false,
		"extra":{"allow_group":false,"verification_token":"vt-secret","encrypt_key":"ek-secret"}
	}`, appID, secret))
	if w.Code != http.StatusCreated {
		t.Fatalf("create connection: %d %s", w.Code, w.Body.String())
	}
	conn := decodeIMJSON[imConnectionDTO](t, w)
	if conn.ID == 0 || !strings.HasPrefix(conn.AppSecret, notify.MaskedPrefix) {
		t.Fatalf("secret not masked: %+v", conn)
	}
	for _, key := range []string{"verification_token", "encrypt_key"} {
		if v, _ := conn.Extra[key].(string); !strings.HasPrefix(v, notify.MaskedPrefix) {
			t.Fatalf("extra secret %s not masked: %v", key, conn.Extra)
		}
	}
	var raw string
	if err := pg.QueryRow(`SELECT app_secret FROM im_connections WHERE id=$1`, conn.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != secret {
		t.Fatalf("stored secret changed: %q", raw)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM im_connections WHERE id=$1`, conn.ID) })

	w = request(http.MethodPut, fmt.Sprintf("/api/im/connections/%d", conn.ID), fmt.Sprintf(`{
		"name":"renamed","platform":"dingtalk","app_id":%q,"app_secret":%q,"enabled":false,"extra":{"allow_group":true}
	}`, appID, conn.AppSecret))
	if w.Code != http.StatusOK {
		t.Fatalf("update connection: %d %s", w.Code, w.Body.String())
	}
	if err := pg.QueryRow(`SELECT app_secret FROM im_connections WHERE id=$1`, conn.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != secret {
		t.Fatalf("masked update replaced secret: %q", raw)
	}
	var allowGroup bool
	if err := pg.QueryRow(`SELECT (extra->>'allow_group')::boolean FROM im_connections WHERE id=$1`, conn.ID).Scan(&allowGroup); err != nil {
		t.Fatal(err)
	}
	if !allowGroup {
		t.Fatal("extra update was lost")
	}
	var storedExtra string
	if err := pg.QueryRow(`SELECT extra::text FROM im_connections WHERE id=$1`, conn.ID).Scan(&storedExtra); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(storedExtra, "vt-secret") || !strings.Contains(storedExtra, "ek-secret") {
		t.Fatalf("masked extra update replaced secrets: %s", storedExtra)
	}

	userID := "user-" + suffix
	w = request(http.MethodPost, "/api/im/users", fmt.Sprintf(`{
		"platform":"dingtalk","user_id":%q,"display_name":"tester","role":"viewer","enabled":true,"note":"api test"
	}`, userID))
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	user := decodeIMJSON[map[string]any](t, w)
	userNumericID := int64(user["id"].(float64))
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM im_allowed_users WHERE id=$1`, userNumericID) })

	w = request(http.MethodPut, fmt.Sprintf("/api/im/users/%d", userNumericID), `{"role":"operator","enabled":true,"display_name":"operator"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update user: %d %s", w.Code, w.Body.String())
	}
	updated := decodeIMJSON[map[string]any](t, w)
	if updated["role"] != "operator" {
		t.Fatalf("role not updated: %v", updated)
	}
	w = request(http.MethodGet, "/api/im/messages?limit=5", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"messages"`) {
		t.Fatalf("messages: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodDelete, fmt.Sprintf("/api/im/users/%d", userNumericID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete user: %d %s", w.Code, w.Body.String())
	}
	w = request(http.MethodDelete, fmt.Sprintf("/api/im/connections/%d", conn.ID), "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete connection: %d %s", w.Code, w.Body.String())
	}
}
