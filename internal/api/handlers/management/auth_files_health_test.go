package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			"path":         "claude-1.json",
			"rpm_limit":    "60",
			"max_sessions": "5",
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
	if got, _ := account["rpm_limit"].(float64); got != 60 {
		t.Fatalf("rpm_limit = %#v, want 60", account["rpm_limit"])
	}
	if got, _ := account["current_rpm"].(float64); got != 0 {
		t.Fatalf("current_rpm = %#v, want 0", account["current_rpm"])
	}
	if got, _ := account["max_sessions"].(float64); got != 5 {
		t.Fatalf("max_sessions = %#v, want 5", account["max_sessions"])
	}
	if got, _ := account["active_sessions"].(float64); got != 0 {
		t.Fatalf("active_sessions = %#v, want 0", account["active_sessions"])
	}
	if got, _ := account["status_reason"].(string); got != "quota_cooldown" {
		t.Fatalf("status_reason = %q, want quota_cooldown", got)
	}
	if got, _ := account["status_reason_label"].(string); got != "限额冷却" {
		t.Fatalf("status_reason_label = %q, want 限额冷却", got)
	}
	quality, ok := account["quality_24h"].(map[string]any)
	if !ok {
		t.Fatalf("quality_24h = %T, want map[string]any", account["quality_24h"])
	}
	if got, _ := quality["requests"].(float64); got != 0 {
		t.Fatalf("quality_24h.requests = %#v, want 0", quality["requests"])
	}
}

func TestClaudeAuthHealth_429ResultIsStableQuotaCooldown(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	model := "claude-opus-4-8"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-429",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register claude auth: %v", err)
	}

	manager.MarkResult(context.Background(), coreauth.Result{
		AuthID:   "claude-429",
		Provider: "claude",
		Model:    model,
		Success:  false,
		Error: &coreauth.Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "rate limit exceeded",
			Retryable:  true,
		},
	})

	updated, ok := manager.GetByID("claude-429")
	if !ok {
		t.Fatal("updated auth not found")
	}
	now := time.Now()
	if got := claudeAuthStatusReason(updated, now); got != "quota_cooldown" {
		t.Fatalf("status_reason = %q, want quota_cooldown", got)
	}
	if got := claudeAuthHealthStatus(updated, now); got != "cooling" {
		t.Fatalf("health_status = %q, want cooling", got)
	}
	if got := claudeAuthRouteState(updated, now, claudeAuthStatusReason(updated, now)); got != "cooling" {
		t.Fatalf("route_state = %q, want cooling", got)
	}
}

func TestListClaudeAuthHealth_SeparatesPermanentAndManualDisabledStates(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	authDir := t.TempDir()
	bannedPath := filepath.Join(authDir, "claude-banned.json")
	manualPath := filepath.Join(authDir, "claude-manual.json")
	if err := os.WriteFile(bannedPath, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatalf("write banned auth file: %v", err)
	}
	if err := os.WriteFile(manualPath, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatalf("write manual auth file: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-banned",
		FileName: "claude-banned.json",
		Provider: "claude",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
		LastError: &coreauth.Error{
			Code:       "organization_disabled",
			Message:    "This organization has been disabled.",
			HTTPStatus: http.StatusBadRequest,
		},
		Attributes: map[string]string{"path": bannedPath},
		Metadata:   map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register banned auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:            "claude-manual",
		FileName:      "claude-manual.json",
		Provider:      "claude",
		Disabled:      true,
		Status:        coreauth.StatusDisabled,
		StatusMessage: "disabled via management API",
		Attributes:    map[string]string{"path": manualPath},
		Metadata:      map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register manual auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
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
	accounts := map[string]map[string]any{}
	for _, account := range payload.Accounts {
		name, _ := account["name"].(string)
		accounts[name] = account
	}

	banned := accounts["claude-banned.json"]
	if banned == nil {
		t.Fatalf("banned account missing from response: %#v", accounts)
	}
	if got, _ := banned["status_reason"].(string); got != "organization_disabled" {
		t.Fatalf("banned status_reason = %q, want organization_disabled", got)
	}
	if got, _ := banned["route_state"].(string); got != "permanent_disabled" {
		t.Fatalf("banned route_state = %q, want permanent_disabled", got)
	}
	if got, _ := banned["recoverability"].(string); got != "permanent" {
		t.Fatalf("banned recoverability = %q, want permanent", got)
	}
	if got, _ := banned["cleanup_recommended"].(bool); !got {
		t.Fatalf("banned cleanup_recommended = %v, want true", banned["cleanup_recommended"])
	}

	manual := accounts["claude-manual.json"]
	if manual == nil {
		t.Fatalf("manual account missing from response: %#v", accounts)
	}
	if got, _ := manual["status_reason"].(string); got != "manual_disabled" {
		t.Fatalf("manual status_reason = %q, want manual_disabled", got)
	}
	if got, _ := manual["route_state"].(string); got != "manual_disabled" {
		t.Fatalf("manual route_state = %q, want manual_disabled", got)
	}
	if got, _ := manual["recoverability"].(string); got != "manual" {
		t.Fatalf("manual recoverability = %q, want manual", got)
	}
	if got, _ := manual["cleanup_recommended"].(bool); got {
		t.Fatalf("manual cleanup_recommended = %v, want false", got)
	}
}

func TestClaudeAuthHealth_BannedPhraseWinsOverManualDisabled(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-banned",
		Provider: "claude",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
		LastError: &coreauth.Error{
			Message:    "Your account has been banned.",
			HTTPStatus: http.StatusForbidden,
		},
	}
	now := time.Now()

	if got := claudeAuthStatusReason(auth, now); got != "account_banned" {
		t.Fatalf("status_reason = %q, want account_banned", got)
	}
	if got := claudeAuthHealthStatus(auth, now); got != "permanent_disabled" {
		t.Fatalf("health_status = %q, want permanent_disabled", got)
	}
	if got := claudeAuthRouteState(auth, now, claudeAuthStatusReason(auth, now)); got != "permanent_disabled" {
		t.Fatalf("route_state = %q, want permanent_disabled", got)
	}
}

func TestListClaudeAuthHealth_DerivesRecoverableReasonBeforeDisabledFlag(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	authDir := t.TempDir()
	subscriptionPath := filepath.Join(authDir, "claude-subscription.json")
	rateLimitedPath := filepath.Join(authDir, "claude-rate-limited.json")
	if err := os.WriteFile(subscriptionPath, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatalf("write subscription auth file: %v", err)
	}
	if err := os.WriteFile(rateLimitedPath, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatalf("write rate-limited auth file: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-subscription",
		FileName: "claude-subscription.json",
		Provider: "claude",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
		LastError: &coreauth.Error{
			Code:       "forbidden",
			Message:    "Subscription or billing issue.",
			HTTPStatus: http.StatusForbidden,
		},
		Attributes: map[string]string{"path": subscriptionPath},
		Metadata:   map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register subscription auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-rate-limited",
		FileName: "claude-rate-limited.json",
		Provider: "claude",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
		LastError: &coreauth.Error{
			Code:       "rate_limited",
			Message:    "Rate limited. Please try again later.",
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
		Attributes: map[string]string{"path": rateLimitedPath},
		Metadata:   map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register rate-limited auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
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
	accounts := map[string]map[string]any{}
	for _, account := range payload.Accounts {
		name, _ := account["name"].(string)
		accounts[name] = account
	}

	subscription := accounts["claude-subscription.json"]
	if subscription == nil {
		t.Fatalf("subscription account missing from response: %#v", accounts)
	}
	if got, _ := subscription["status_reason"].(string); got != "subscription_issue" {
		t.Fatalf("subscription status_reason = %q, want subscription_issue", got)
	}
	if got, _ := subscription["route_state"].(string); got != "repair_required" {
		t.Fatalf("subscription route_state = %q, want repair_required", got)
	}
	if got, _ := subscription["recoverability"].(string); got != "manual" {
		t.Fatalf("subscription recoverability = %q, want manual", got)
	}
	if got, _ := subscription["cleanup_recommended"].(bool); got {
		t.Fatalf("subscription cleanup_recommended = %v, want false", got)
	}

	rateLimited := accounts["claude-rate-limited.json"]
	if rateLimited == nil {
		t.Fatalf("rate-limited account missing from response: %#v", accounts)
	}
	if got, _ := rateLimited["status_reason"].(string); got != "rate_limited" {
		t.Fatalf("rate-limited status_reason = %q, want rate_limited", got)
	}
	if got, _ := rateLimited["route_state"].(string); got != "cooling" {
		t.Fatalf("rate-limited route_state = %q, want cooling", got)
	}
	if got, _ := rateLimited["recoverability"].(string); got != "auto" {
		t.Fatalf("rate-limited recoverability = %q, want auto", got)
	}
	if got, _ := rateLimited["cleanup_recommended"].(bool); got {
		t.Fatalf("rate-limited cleanup_recommended = %v, want false", got)
	}
}
