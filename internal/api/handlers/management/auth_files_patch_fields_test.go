package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPatchAuthFileFields_MergeHeadersAndDeleteEmptyValues(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "test.json",
		FileName: "test.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":            "/tmp/test.json",
			"header:X-Old":    "old",
			"header:X-Remove": "gone",
		},
		Metadata: map[string]any{
			"type": "claude",
			"headers": map[string]any{
				"X-Old":    "old",
				"X-Remove": "gone",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"test.json","prefix":"p1","proxy_url":"http://proxy.local","headers":{"X-Old":"new","X-New":"v","X-Remove":"  ","X-Nope":""},"priority":2,"rpm_limit":42,"max_sessions":7,"cloak_mode":"always","cloak_strict_mode":true,"cloak_sensitive_words":["Claude"," account ","Claude"],"cloak_cache_user_id":false}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("test.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}

	if updated.Prefix != "p1" {
		t.Fatalf("prefix = %q, want %q", updated.Prefix, "p1")
	}
	if updated.ProxyURL != "http://proxy.local" {
		t.Fatalf("proxy_url = %q, want %q", updated.ProxyURL, "http://proxy.local")
	}

	if updated.Metadata == nil {
		t.Fatalf("expected metadata to be non-nil")
	}
	if got, _ := updated.Metadata["prefix"].(string); got != "p1" {
		t.Fatalf("metadata.prefix = %q, want %q", got, "p1")
	}
	if got, _ := updated.Metadata["proxy_url"].(string); got != "http://proxy.local" {
		t.Fatalf("metadata.proxy_url = %q, want %q", got, "http://proxy.local")
	}

	headersMeta, ok := updated.Metadata["headers"].(map[string]any)
	if !ok {
		raw, _ := json.Marshal(updated.Metadata["headers"])
		t.Fatalf("metadata.headers = %T (%s), want map[string]any", updated.Metadata["headers"], string(raw))
	}
	if got := headersMeta["X-Old"]; got != "new" {
		t.Fatalf("metadata.headers.X-Old = %#v, want %q", got, "new")
	}
	if got := headersMeta["X-New"]; got != "v" {
		t.Fatalf("metadata.headers.X-New = %#v, want %q", got, "v")
	}
	if _, ok := headersMeta["X-Remove"]; ok {
		t.Fatalf("expected metadata.headers.X-Remove to be deleted")
	}
	if _, ok := headersMeta["X-Nope"]; ok {
		t.Fatalf("expected metadata.headers.X-Nope to be absent")
	}

	if got := updated.Attributes["header:X-Old"]; got != "new" {
		t.Fatalf("attrs header:X-Old = %q, want %q", got, "new")
	}
	if got := updated.Attributes["header:X-New"]; got != "v" {
		t.Fatalf("attrs header:X-New = %q, want %q", got, "v")
	}
	if _, ok := updated.Attributes["header:X-Remove"]; ok {
		t.Fatalf("expected attrs header:X-Remove to be deleted")
	}
	if _, ok := updated.Attributes["header:X-Nope"]; ok {
		t.Fatalf("expected attrs header:X-Nope to be absent")
	}
	if got := updated.Attributes["cloak_mode"]; got != "always" {
		t.Fatalf("attrs cloak_mode = %q, want %q", got, "always")
	}
	if got := updated.Attributes["cloak_strict_mode"]; got != "true" {
		t.Fatalf("attrs cloak_strict_mode = %q, want true", got)
	}
	if got := updated.Attributes["cloak_sensitive_words"]; got != "Claude,account" {
		t.Fatalf("attrs cloak_sensitive_words = %q, want Claude,account", got)
	}
	if got := updated.Attributes["cloak_cache_user_id"]; got != "false" {
		t.Fatalf("attrs cloak_cache_user_id = %q, want false", got)
	}
	if got := updated.Attributes["rpm_limit"]; got != "42" {
		t.Fatalf("attrs rpm_limit = %q, want 42", got)
	}
	if got := updated.Attributes["max_sessions"]; got != "7" {
		t.Fatalf("attrs max_sessions = %q, want 7", got)
	}
	if got := updated.EffectiveMaxSessions(); got != 7 {
		t.Fatalf("EffectiveMaxSessions() = %d, want 7", got)
	}
	if got, _ := updated.Metadata["cloak_mode"].(string); got != "always" {
		t.Fatalf("metadata.cloak_mode = %q, want always", got)
	}
	if got, _ := updated.Metadata["cloak_strict_mode"].(bool); !got {
		t.Fatalf("metadata.cloak_strict_mode = %v, want true", got)
	}
	words, ok := updated.Metadata["cloak_sensitive_words"].([]string)
	if !ok {
		t.Fatalf("metadata.cloak_sensitive_words = %T, want []string", updated.Metadata["cloak_sensitive_words"])
	}
	if len(words) != 2 || words[0] != "Claude" || words[1] != "account" {
		t.Fatalf("metadata.cloak_sensitive_words = %#v, want [Claude account]", words)
	}
	if got, _ := updated.Metadata["cloak_cache_user_id"].(bool); got {
		t.Fatalf("metadata.cloak_cache_user_id = %v, want false", got)
	}
	if got, _ := updated.Metadata["rpm_limit"].(int); got != 42 {
		t.Fatalf("metadata.rpm_limit = %#v, want 42", updated.Metadata["rpm_limit"])
	}
	if got, _ := updated.Metadata["max_sessions"].(int); got != 7 {
		t.Fatalf("metadata.max_sessions = %#v, want 7", updated.Metadata["max_sessions"])
	}
}

func TestPatchAuthFileFields_HeadersEmptyMapIsNoop(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       "noop.json",
		FileName: "noop.json",
		Provider: "claude",
		Attributes: map[string]string{
			"path":         "/tmp/noop.json",
			"header:X-Kee": "1",
		},
		Metadata: map[string]any{
			"type": "claude",
			"headers": map[string]any{
				"X-Kee": "1",
			},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	body := `{"name":"noop.json","note":"hello","headers":{}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID("noop.json")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after patch")
	}
	if got := updated.Attributes["header:X-Kee"]; got != "1" {
		t.Fatalf("attrs header:X-Kee = %q, want %q", got, "1")
	}
	headersMeta, ok := updated.Metadata["headers"].(map[string]any)
	if !ok {
		t.Fatalf("expected metadata.headers to remain a map, got %T", updated.Metadata["headers"])
	}
	if got := headersMeta["X-Kee"]; got != "1" {
		t.Fatalf("metadata.headers.X-Kee = %#v, want %q", got, "1")
	}
}
