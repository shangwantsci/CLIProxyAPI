package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListClaudeAuthHealth_ExposesClaudeAccountRuntimeState(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	expiresAt := time.Now().UTC().Add(2 * time.Hour)
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-1",
		FileName: "claude-1.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		ProxyURL: "socks5://127.0.0.1:1080",
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "rate limited",
			NextRecoverAt: time.Now().UTC().Add(15 * time.Minute),
		},
		Attributes: map[string]string{
			"path": "claude-1.json",
		},
		Metadata: map[string]any{
			"type":           "claude",
			"email":          "x@example.test",
			"expires_at":     expiresAt.Format(time.RFC3339),
			"auth_source":    "claude_code_cli",
			"token_endpoint": "https://api.anthropic.com/v1/oauth/token",
			"redirect_uri":   "http://localhost:54545/callback",
			"claude_device_profile": map[string]any{
				"user_agent":      "claude-cli/2.1.93 (external, cli)",
				"package_version": "0.71.0",
				"runtime_version": "v24.14.0",
				"os":              "MacOS",
				"arch":            "arm64",
			},
		},
	}); err != nil {
		t.Fatalf("register claude auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "gemini-1",
		FileName: "gemini-1.json",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
	}); err != nil {
		t.Fatalf("register gemini auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/claude-health", nil)
	h.ListClaudeAuthHealth(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Accounts) != 1 {
		t.Fatalf("accounts len = %d, want 1", len(payload.Accounts))
	}
	account := payload.Accounts[0]
	if got, _ := account["health_status"].(string); got != "expiring_soon" {
		t.Fatalf("health_status = %q, want expiring_soon", got)
	}
	if got, _ := account["proxy_url"].(string); got != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy_url = %q, want socks5 proxy", got)
	}
	if _, ok := account["claude_device_profile"].(map[string]any); !ok {
		t.Fatalf("claude_device_profile = %T, want map[string]any", account["claude_device_profile"])
	}
	if seconds, _ := account["seconds_until_expiration"].(float64); seconds <= 0 {
		t.Fatalf("seconds_until_expiration = %v, want positive", account["seconds_until_expiration"])
	}
	if got, _ := account["quota_exceeded"].(bool); !got {
		t.Fatalf("quota_exceeded = %v, want true", account["quota_exceeded"])
	}
	if got, _ := account["quota_reason"].(string); got != "rate limited" {
		t.Fatalf("quota_reason = %q, want rate limited", got)
	}
	if got, _ := account["auth_source"].(string); got != "claude_code_cli" {
		t.Fatalf("auth_source = %q, want claude_code_cli", got)
	}
	if got, _ := account["auth_method_label"].(string); got != "Claude Code CLI OAuth" {
		t.Fatalf("auth_method_label = %q, want Claude Code CLI OAuth", got)
	}
}
