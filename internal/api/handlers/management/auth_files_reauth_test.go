package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestReauthenticateClaudeAuthFile_RefreshesAndReenablesDisabledAccount(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	account := &coreauth.Auth{
		ID:            "claude-user@example.com.json",
		FileName:      "claude-user@example.com.json",
		Provider:      "claude",
		ProxyURL:      "socks5://127.0.0.1:1080",
		Disabled:      true,
		Unavailable:   true,
		Status:        coreauth.StatusDisabled,
		StatusMessage: "unauthorized",
		LastError: &coreauth.Error{
			Code:       "unauthorized",
			Message:    "Invalid authentication credentials",
			HTTPStatus: http.StatusUnauthorized,
		},
		Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(2 * time.Hour)},
		Metadata: map[string]any{
			"type":          "claude",
			"email":         "user@example.com",
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
			"last_error": map[string]any{
				"code": "unauthorized",
			},
			"status":         "disabled",
			"unavailable":    true,
			"disabled":       true,
			"unified_status": "blocked",
		},
	}
	if _, err := manager.Register(context.Background(), account); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	originalRefresher := refreshClaudeTokenForManagement
	t.Cleanup(func() { refreshClaudeTokenForManagement = originalRefresher })
	refreshClaudeTokenForManagement = func(ctx context.Context, cfg *config.Config, proxyURL, refreshToken string, opts claude.RefreshTokenOptions) (*claude.ClaudeTokenData, error) {
		if refreshToken != "old-refresh" {
			t.Fatalf("refreshToken = %q, want old-refresh", refreshToken)
		}
		if proxyURL != "socks5://127.0.0.1:1080" {
			t.Fatalf("proxyURL = %q, want account proxy", proxyURL)
		}
		if !opts.AllowEndpointFallback {
			t.Fatalf("AllowEndpointFallback = false, want true")
		}
		return &claude.ClaudeTokenData{
			AccessToken:   "new-access",
			RefreshToken:  "new-refresh",
			TokenType:     "bearer",
			ExpiresIn:     3600,
			Email:         "user@example.com",
			Expire:        time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			AuthSource:    claude.AuthSourceClaudeCodeCLI,
			TokenEndpoint: "https://api.anthropic.com/v1/oauth/token",
			RedirectURI:   claude.RedirectURI,
		}, nil
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/reauth", strings.NewReader(`{"name":"claude-user@example.com.json"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.ReauthenticateClaudeAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["status"] != "ok" {
		t.Fatalf("response status = %v, want ok", payload["status"])
	}

	updated, ok := manager.GetByID("claude-user@example.com.json")
	if !ok {
		t.Fatalf("updated auth not found")
	}
	if updated.Disabled || updated.Unavailable || updated.Status != coreauth.StatusActive {
		t.Fatalf("state disabled/unavailable/status = %v/%v/%s, want false/false/active", updated.Disabled, updated.Unavailable, updated.Status)
	}
	if updated.LastError != nil {
		t.Fatalf("LastError = %v, want nil", updated.LastError)
	}
	if got := updated.Metadata["access_token"]; got != "new-access" {
		t.Fatalf("access_token = %v, want new-access", got)
	}
	if got := updated.Metadata["refresh_token"]; got != "new-refresh" {
		t.Fatalf("refresh_token = %v, want new-refresh", got)
	}
	if _, ok := updated.Metadata["last_error"]; ok {
		t.Fatalf("last_error metadata should be cleared")
	}
	if got := updated.Metadata["auth_source"]; got != claude.AuthSourceClaudeCodeCLI {
		t.Fatalf("auth_source = %v, want %s", got, claude.AuthSourceClaudeCodeCLI)
	}
	if updated.Quota.Exceeded {
		t.Fatalf("Quota.Exceeded should be cleared after reauth")
	}
	if _, ok := updated.Metadata["unified_status"]; ok {
		t.Fatalf("passive-quota metadata should be cleared after reauth")
	}
}

func TestReauthenticateClaudeAuthFile_RejectsMissingRefreshToken(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-empty.json",
		FileName: "claude-empty.json",
		Provider: "claude",
		Metadata: map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/reauth", strings.NewReader(`{"name":"claude-empty.json"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.ReauthenticateClaudeAuthFile(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
