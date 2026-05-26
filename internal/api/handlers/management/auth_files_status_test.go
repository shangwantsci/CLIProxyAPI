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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPatchAuthFileStatus_EnableClearsClientRequestErrorState(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	staleErr := &coreauth.Error{
		HTTPStatus: http.StatusBadRequest,
		Message:    `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210814 tokens > 200000 maximum"},"request_id":"req_123"}`,
	}
	model := "claude-sonnet-4-6"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:            "claude-client-request.json",
		FileName:      "claude-client-request.json",
		Provider:      "claude",
		Status:        coreauth.StatusError,
		StatusMessage: staleErr.Message,
		LastError:     staleErr,
		ModelStates: map[string]*coreauth.ModelState{
			model: {
				Unavailable:   true,
				Status:        coreauth.StatusError,
				StatusMessage: staleErr.Message,
				LastError:     staleErr,
			},
		},
		Attributes: map[string]string{"path": "claude-client-request.json"},
		Metadata:   map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"claude-client-request.json","disabled":false}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated, ok := manager.GetByID("claude-client-request.json")
	if !ok || updated == nil {
		t.Fatalf("updated auth not found")
	}
	if updated.Disabled || updated.Unavailable || updated.Status != coreauth.StatusActive {
		t.Fatalf("state disabled/unavailable/status = %v/%v/%s, want false/false/active", updated.Disabled, updated.Unavailable, updated.Status)
	}
	if updated.LastError != nil || updated.StatusMessage != "" {
		t.Fatalf("LastError/statusMessage = %#v/%q, want cleared", updated.LastError, updated.StatusMessage)
	}
	entry := h.buildAuthFileEntry(updated)
	if got, _ := entry["status_reason"].(string); got != "healthy" {
		encoded, _ := json.Marshal(entry)
		t.Fatalf("status_reason = %q, want healthy (entry=%s)", got, encoded)
	}
}

func TestPatchAuthFileStatus_EnablePreservesPermanentAuthError(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	permanentErr := &coreauth.Error{
		Code:       "organization_disabled",
		HTTPStatus: http.StatusBadRequest,
		Message:    "This organization has been disabled.",
	}
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:            "claude-disabled.json",
		FileName:      "claude-disabled.json",
		Provider:      "claude",
		Disabled:      true,
		Status:        coreauth.StatusDisabled,
		StatusMessage: "disabled via management API",
		LastError:     permanentErr,
		Attributes:    map[string]string{"path": "claude-disabled.json"},
		Metadata:      map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"claude-disabled.json","disabled":false}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	updated, ok := manager.GetByID("claude-disabled.json")
	if !ok || updated == nil {
		t.Fatalf("updated auth not found")
	}
	if updated.LastError == nil || updated.LastError.Code != "organization_disabled" {
		t.Fatalf("LastError = %#v, want organization_disabled preserved", updated.LastError)
	}
	entry := h.buildAuthFileEntry(updated)
	if got, _ := entry["status_reason"].(string); got != "organization_disabled" {
		encoded, _ := json.Marshal(entry)
		t.Fatalf("status_reason = %q, want organization_disabled (entry=%s)", got, encoded)
	}
}

func TestClaudeAuthHealthClassifiesOAuthNotAllowedForOrganization(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:            "claude-oauth-not-allowed.json",
		FileName:      "claude-oauth-not-allowed.json",
		Provider:      "claude",
		Disabled:      true,
		Status:        coreauth.StatusDisabled,
		StatusMessage: "OAuth authentication is currently not allowed for this organization.",
		LastError: &coreauth.Error{
			Code:       "forbidden",
			HTTPStatus: http.StatusForbidden,
			Message:    "OAuth authentication is currently not allowed for this organization.",
		},
		Attributes: map[string]string{"path": "claude-oauth-not-allowed.json"},
		Metadata:   map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	updated, ok := manager.GetByID("claude-oauth-not-allowed.json")
	if !ok || updated == nil {
		t.Fatalf("auth not found")
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
	if got, _ := entry["recoverability"].(string); got != "permanent" {
		t.Fatalf("recoverability = %q, want permanent", got)
	}
}

func TestClaudeAuthHealthClassifiesHistoricalOAuthNotAllowedUnavailableState(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	auth := &coreauth.Auth{
		ID:            "claude-historical-oauth-not-allowed.json",
		FileName:      "claude-historical-oauth-not-allowed.json",
		Provider:      "claude",
		Status:        coreauth.StatusError,
		Unavailable:   true,
		StatusMessage: "OAuth authentication is currently not allowed for this organization.",
		LastError: &coreauth.Error{
			Code:       "forbidden",
			HTTPStatus: http.StatusForbidden,
			Message:    "OAuth authentication is currently not allowed for this organization.",
		},
		Metadata: map[string]any{"type": "claude"},
	}

	entry := gin.H{}
	addClaudeAuthHealthFields(entry, auth, time.Now())
	if got, _ := entry["health_status"].(string); got != "permanent_disabled" {
		encoded, _ := json.Marshal(entry)
		t.Fatalf("health_status = %q, want permanent_disabled (entry=%s)", got, encoded)
	}
	if got, _ := entry["status_reason"].(string); got != "organization_disabled" {
		t.Fatalf("status_reason = %q, want organization_disabled", got)
	}
	if got, _ := entry["route_state"].(string); got != "permanent_disabled" {
		t.Fatalf("route_state = %q, want permanent_disabled", got)
	}
}
