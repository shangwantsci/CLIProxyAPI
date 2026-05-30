package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"})
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			GeminiKey: []config.GeminiKey{{
				APIKey:   "gemini-key",
				ProxyURL: "http://gemini-proxy.example.com:8080",
			}},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "gemini",
			auth: &coreauth.Auth{
				Provider:   "gemini",
				Attributes: map[string]string{"api_key": "gemini-key"},
			},
			wantProxy: "http://gemini-proxy.example.com:8080",
		},
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth)
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	geminiAuth := &coreauth.Auth{
		ID:       "gemini:apikey:123",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), geminiAuth); errRegister != nil {
		t.Fatalf("register gemini auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	geminiIndex := geminiAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if geminiIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", geminiIndex)
	}

	h := &Handler{authManager: manager}

	gotGemini := h.authByIndex(geminiIndex)
	if gotGemini == nil {
		t.Fatal("expected gemini auth by index")
	}
	if gotGemini.ID != geminiAuth.ID {
		t.Fatalf("authByIndex(gemini) returned %q, want %q", gotGemini.ID, geminiAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}

func TestAPICallRecordsClaudeOAuthAccountBanned(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q, want bearer access-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"account_banned","details":{"error_code":"account_banned"}}}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-banned",
		FileName: "claude-banned.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/profile")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload apiCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.StatusCode != http.StatusForbidden {
		t.Fatalf("upstream status = %d, want 403", payload.StatusCode)
	}

	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if !updated.Disabled || updated.Status != coreauth.StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want true/disabled", updated.Disabled, updated.Status)
	}
	if updated.LastError == nil {
		t.Fatal("LastError is nil")
	}
	if updated.LastError.Code != "account_banned" || updated.LastError.HTTPStatus != http.StatusForbidden {
		t.Fatalf("LastError = %#v, want account_banned 403", updated.LastError)
	}
	entry := gin.H{}
	addClaudeAuthHealthFields(entry, updated, time.Now())
	if got, _ := entry["status_reason"].(string); got != "account_banned" {
		encoded, _ := json.Marshal(entry)
		t.Fatalf("status_reason = %q, want account_banned (entry=%s)", got, encoded)
	}
	if got, _ := entry["route_state"].(string); got != "permanent_disabled" {
		t.Fatalf("route_state = %q, want permanent_disabled", got)
	}
}

func TestAPICallRecordsClaudeOAuthOrganizationDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"This organization has been disabled."},"request_id":"req_123"}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-org-disabled",
		FileName: "claude-org-disabled.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/profile")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if !updated.Disabled || updated.Status != coreauth.StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want true/disabled", updated.Disabled, updated.Status)
	}
	if updated.LastError == nil || updated.LastError.Code != "organization_disabled" || updated.LastError.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("LastError = %#v, want organization_disabled 400", updated.LastError)
	}
	entry := gin.H{}
	addClaudeAuthHealthFields(entry, updated, time.Now())
	if got, _ := entry["status_reason"].(string); got != "organization_disabled" {
		encoded, _ := json.Marshal(entry)
		t.Fatalf("status_reason = %q, want organization_disabled (entry=%s)", got, encoded)
	}
	if got, _ := entry["route_state"].(string); got != "permanent_disabled" {
		t.Fatalf("route_state = %q, want permanent_disabled", got)
	}
}

func TestAPICallRecordsClaudeOAuthUsageNotAllowedForOrganization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."},"request_id":"req_123"}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-oauth-not-allowed",
		FileName: "claude-oauth-not-allowed.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/usage")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if !updated.Disabled || updated.Status != coreauth.StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want true/disabled", updated.Disabled, updated.Status)
	}
	if updated.LastError == nil || updated.LastError.Code != "organization_disabled" || updated.LastError.HTTPStatus != http.StatusForbidden {
		t.Fatalf("LastError = %#v, want organization_disabled 403", updated.LastError)
	}
	entry := gin.H{}
	addClaudeAuthHealthFields(entry, updated, time.Now())
	if got, _ := entry["status_reason"].(string); got != "organization_disabled" {
		encoded, _ := json.Marshal(entry)
		t.Fatalf("status_reason = %q, want organization_disabled (entry=%s)", got, encoded)
	}
	if got, _ := entry["route_state"].(string); got != "permanent_disabled" {
		t.Fatalf("route_state = %q, want permanent_disabled", got)
	}
}

func TestAPICallIgnoresSetupTokenScopeRequirementForUsageProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"OAuth token does not meet scope requirement any_of(user:profile, user:office)"}}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-setup-token",
		FileName: "claude-setup-token.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "setup-access-token",
			"auth_source":  "claude_setup_token",
			"auth_kind":    "setup_token",
			"scope":        "user:inference",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/usage")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if updated.Disabled || updated.Status != coreauth.StatusActive || updated.Unavailable {
		t.Fatalf("state = disabled=%v status=%s unavailable=%v, want active and available", updated.Disabled, updated.Status, updated.Unavailable)
	}
	if updated.LastError != nil {
		t.Fatalf("LastError = %#v, want nil for setup-token scope mismatch", updated.LastError)
	}
}

func TestBuildAuthFileEntryRepairsSetupTokenScopeRequirementState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:            "claude-setup-token-stale",
		FileName:      "claude-setup-token-stale.json",
		Provider:      "claude",
		Status:        coreauth.StatusError,
		StatusMessage: "OAuth token does not meet scope requirement any_of(user:profile, user:office)",
		Unavailable:   true,
		Attributes: map[string]string{
			"path": "claude-setup-token-stale.json",
		},
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "setup-access-token",
			"auth_source":  "claude_setup_token",
			"scope":        "user:inference",
		},
		LastError: &coreauth.Error{
			Code:       "forbidden",
			Message:    "OAuth token does not meet scope requirement any_of(user:profile, user:office)",
			HTTPStatus: http.StatusForbidden,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	entry := h.buildAuthFileEntry(auth)
	if entry == nil {
		t.Fatal("entry is nil")
	}
	if got := entry["status"]; got != coreauth.StatusActive {
		t.Fatalf("entry status = %v, want active", got)
	}
	if got := entry["unavailable"]; got != false {
		t.Fatalf("entry unavailable = %v, want false", got)
	}
	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("updated auth not found")
	}
	if updated.Status != coreauth.StatusActive || updated.Unavailable || updated.LastError != nil {
		t.Fatalf("updated state = status=%s unavailable=%v lastError=%#v, want repaired", updated.Status, updated.Unavailable, updated.LastError)
	}
}

func TestBuildAuthFileEntryExposesClaudePassiveQuotaState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	reset5h := time.Now().UTC().Truncate(time.Second).Add(4 * time.Hour)
	reset7d := time.Now().UTC().Truncate(time.Second).Add(72 * time.Hour)
	auth := &coreauth.Auth{
		ID:       "claude-setup-token-passive-quota",
		FileName: "claude-setup-token-passive-quota.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"type":                         "claude",
			"auth_source":                  "claude_setup_token",
			"scope":                        "user:inference",
			"session_window_start":         reset5h.Add(-5 * time.Hour).Format(time.RFC3339),
			"session_window_end":           reset5h.Format(time.RFC3339),
			"session_window_status":        "allowed_warning",
			"session_window_utilization":   0.82,
			"passive_usage_7d_utilization": 0.44,
			"passive_usage_7d_reset":       reset7d.Unix(),
			"passive_usage_sampled_at":     time.Now().UTC().Format(time.RFC3339),
		},
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(&memoryAuthStore{}, nil, nil))

	entry := h.buildAuthFileEntry(auth)
	if entry == nil {
		t.Fatal("entry is nil")
	}
	if got := entry["session_window_status"]; got != "allowed_warning" {
		t.Fatalf("session_window_status = %v, want allowed_warning", got)
	}
	if got := entry["session_window_utilization"]; got != 0.82 {
		t.Fatalf("session_window_utilization = %v, want 0.82", got)
	}
	if got := entry["passive_usage_7d_utilization"]; got != 0.44 {
		t.Fatalf("passive_usage_7d_utilization = %v, want 0.44", got)
	}
	if got := entry["passive_usage_7d_reset"]; got != reset7d.Unix() {
		t.Fatalf("passive_usage_7d_reset = %v, want %v", got, reset7d.Unix())
	}
}

func TestAPICallRecordsClaudeOAuthSubscriptionForbiddenAsRepairRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"type":"permission_error","message":"Subscription billing issue."}}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-subscription",
		FileName: "claude-subscription.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/profile")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("subscription auth not found")
	}
	if updated.Disabled || updated.Status == coreauth.StatusDisabled {
		t.Fatalf("subscription auth unexpectedly disabled: disabled=%v status=%s", updated.Disabled, updated.Status)
	}
	entry := gin.H{}
	addClaudeAuthHealthFields(entry, updated, time.Now())
	if got, _ := entry["status_reason"].(string); got != "subscription_issue" {
		encoded, _ := json.Marshal(entry)
		t.Fatalf("status_reason = %q, want subscription_issue (entry=%s)", got, encoded)
	}
	if got, _ := entry["route_state"].(string); got != "repair_required" {
		t.Fatalf("route_state = %q, want repair_required", got)
	}
}

func TestAPICallRecordsClaudeOAuthRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"Rate limited. Please try again later."}}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-limited",
		FileName: "claude-limited.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	before := time.Now().Add(110 * time.Second)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/usage")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if updated.Disabled || updated.Status == coreauth.StatusDisabled {
		t.Fatalf("auth unexpectedly disabled: disabled=%v status=%s", updated.Disabled, updated.Status)
	}
	if updated.Status != coreauth.StatusError || !updated.Unavailable {
		t.Fatalf("status/unavailable = %s/%v, want error/true", updated.Status, updated.Unavailable)
	}
	if !updated.Quota.Exceeded || updated.Quota.Reason == "" {
		t.Fatalf("quota = %#v, want exceeded with reason", updated.Quota)
	}
	if updated.NextRetryAfter.Before(before) {
		t.Fatalf("NextRetryAfter = %v, want at least %v", updated.NextRetryAfter, before)
	}
	if updated.LastError == nil || updated.LastError.Code != "rate_limited" || updated.LastError.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("LastError = %#v, want rate_limited 429", updated.LastError)
	}
}

func TestAPICallRecordsClaudeOAuthUsageExhaustedFromSuccessfulProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resetAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"five_hour":{"utilization":100,"resets_at":%q},
			"seven_day":{"utilization":30,"resets_at":%q}
		}`, resetAt.Format(time.RFC3339), resetAt.Add(24*time.Hour).Format(time.RFC3339))))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-usage-exhausted",
		FileName: "claude-usage-exhausted.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/usage")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if updated.Disabled || updated.Status == coreauth.StatusDisabled {
		t.Fatalf("auth unexpectedly disabled: disabled=%v status=%s", updated.Disabled, updated.Status)
	}
	if updated.Status != coreauth.StatusError || !updated.Unavailable {
		t.Fatalf("status/unavailable = %s/%v, want error/true", updated.Status, updated.Unavailable)
	}
	if !updated.Quota.Exceeded || !strings.Contains(updated.Quota.Reason, "five_hour") {
		t.Fatalf("quota = %#v, want exhausted five_hour reason", updated.Quota)
	}
	if !updated.Quota.NextRecoverAt.Equal(resetAt) || !updated.NextRetryAfter.Equal(resetAt) {
		t.Fatalf("recover/retry = %v/%v, want %v", updated.Quota.NextRecoverAt, updated.NextRetryAfter, resetAt)
	}
	if updated.LastError == nil || updated.LastError.Code != "quota_exhausted" || updated.LastError.HTTPStatus != http.StatusOK {
		t.Fatalf("LastError = %#v, want quota_exhausted 200", updated.LastError)
	}
}

func TestAPICallRecordsClaudeOAuthUsageFiveHourThresholdCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resetAt := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"five_hour":{"utilization":82,"resets_at":%q},
			"seven_day":{"utilization":30,"resets_at":%q}
		}`, resetAt.Format(time.RFC3339), resetAt.Add(24*time.Hour).Format(time.RFC3339))))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-five-hour-threshold",
		FileName: "claude-five-hour-threshold.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		AuthDir: t.TempDir(),
		ClaudeQuotaCoolingThresholds: config.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/usage")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if updated.Disabled || updated.Status == coreauth.StatusDisabled {
		t.Fatalf("auth unexpectedly disabled: disabled=%v status=%s", updated.Disabled, updated.Status)
	}
	if updated.Status != coreauth.StatusError || !updated.Unavailable {
		t.Fatalf("status/unavailable = %s/%v, want error/true", updated.Status, updated.Unavailable)
	}
	if !updated.Quota.Exceeded || !strings.Contains(updated.Quota.Reason, "five_hour") {
		t.Fatalf("quota = %#v, want threshold five_hour reason", updated.Quota)
	}
	if !updated.Quota.NextRecoverAt.Equal(resetAt) || !updated.NextRetryAfter.Equal(resetAt) {
		t.Fatalf("recover/retry = %v/%v, want %v", updated.Quota.NextRecoverAt, updated.NextRetryAfter, resetAt)
	}
	if updated.LastError == nil || updated.LastError.Code != "quota_exhausted" || updated.LastError.HTTPStatus != http.StatusOK {
		t.Fatalf("LastError = %#v, want quota_exhausted 200", updated.LastError)
	}
}

func TestAPICallRecordsClaudeOAuthUsageWeeklyThresholdCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resetAt := time.Now().Add(22 * time.Hour).UTC().Truncate(time.Second)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{
			"five_hour":{"utilization":45,"resets_at":%q},
			"seven_day":{"utilization":91,"resets_at":%q}
		}`, resetAt.Add(-20*time.Hour).Format(time.RFC3339), resetAt.Format(time.RFC3339))))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:       "claude-weekly-threshold",
		FileName: "claude-weekly-threshold.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		AuthDir: t.TempDir(),
		ClaudeQuotaCoolingThresholds: config.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/usage")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if updated.Status != coreauth.StatusError || !updated.Unavailable {
		t.Fatalf("status/unavailable = %s/%v, want error/true", updated.Status, updated.Unavailable)
	}
	if !updated.Quota.Exceeded || !strings.Contains(updated.Quota.Reason, "seven_day") {
		t.Fatalf("quota = %#v, want threshold seven_day reason", updated.Quota)
	}
	if !updated.Quota.NextRecoverAt.Equal(resetAt) || !updated.NextRetryAfter.Equal(resetAt) {
		t.Fatalf("recover/retry = %v/%v, want %v", updated.Quota.NextRecoverAt, updated.NextRetryAfter, resetAt)
	}
}

func TestAPICallClearsClaudeOAuthQuotaAfterSuccessfulUsageAvailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	resetAt := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"five_hour":{"utilization":40,"resets_at":%q}}`, resetAt.Format(time.RFC3339))))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	auth := &coreauth.Auth{
		ID:             "claude-usage-recovered",
		FileName:       "claude-usage-recovered.json",
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "Claude quota exhausted: five_hour",
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(-time.Minute),
		Quota: coreauth.QuotaState{
			Exceeded:      true,
			Reason:        "Claude quota exhausted: five_hour",
			NextRecoverAt: time.Now().Add(-time.Minute),
		},
		LastError: &coreauth.Error{Code: "quota_exhausted", Message: "Claude quota exhausted: five_hour", HTTPStatus: http.StatusOK},
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	authIndex := auth.EnsureIndex()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := fmt.Sprintf(`{"auth_index":%q,"method":"GET","url":%q,"header":{"Authorization":"Bearer $TOKEN$"}}`, authIndex, upstream.URL+"/api/oauth/usage")
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/api-call", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.APICall(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated := h.authByIndex(authIndex)
	if updated == nil {
		t.Fatal("updated auth not found")
	}
	if updated.Disabled || updated.Status != coreauth.StatusActive || updated.Unavailable {
		t.Fatalf("disabled/status/unavailable = %v/%s/%v, want false/active/false", updated.Disabled, updated.Status, updated.Unavailable)
	}
	if updated.Quota.Exceeded || !updated.Quota.NextRecoverAt.IsZero() || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("quota/retry should be cleared, quota=%#v retry=%v", updated.Quota, updated.NextRetryAfter)
	}
	if updated.LastError != nil || updated.StatusMessage != "" {
		t.Fatalf("LastError/statusMessage = %#v/%q, want cleared", updated.LastError, updated.StatusMessage)
	}
}
