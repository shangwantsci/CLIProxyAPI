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
)

func TestClaudeProbeJobDetectsDisabledAndHealthyAccounts(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		authHeader := r.Header.Get("Authorization")
		if strings.Contains(authHeader, "banned-token") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"This organization has been disabled."}}`))
			return
		}
		switch r.URL.Path {
		case "/api/oauth/profile":
			_, _ = w.Write([]byte(`{"account":{"email":"ok@example.test","has_claude_pro":true},"organization":{"subscription_status":"active"}}`))
		case "/api/oauth/usage":
			_, _ = w.Write([]byte(`{"five_hour":{"utilization":12,"resets_at":"2030-01-01T00:00:00Z"},"seven_day":{"utilization":20,"resets_at":"2030-01-02T00:00:00Z"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	originalProfileURL := claudeOAuthProfileURL
	originalUsageURL := claudeOAuthUsageURL
	claudeOAuthProfileURL = upstream.URL + "/api/oauth/profile"
	claudeOAuthUsageURL = upstream.URL + "/api/oauth/usage"
	t.Cleanup(func() {
		claudeOAuthProfileURL = originalProfileURL
		claudeOAuthUsageURL = originalUsageURL
	})

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{
			ID:       "claude-ok",
			FileName: "claude-ok.json",
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": "claude-ok.json",
			},
			Metadata: map[string]any{
				"type":         "claude",
				"email":        "ok@example.test",
				"access_token": "ok-token",
			},
		},
		{
			ID:       "claude-banned",
			FileName: "claude-banned.json",
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": "claude-banned.json",
			},
			Metadata: map[string]any{
				"type":         "claude",
				"email":        "banned@example.test",
				"access_token": "banned-token",
			},
		},
		{
			ID:       "gemini-ignored",
			FileName: "gemini-ignored.json",
			Provider: "gemini",
			Metadata: map[string]any{"type": "gemini"},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/claude-probe-jobs", strings.NewReader(`{"concurrency":2}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostClaudeProbeJob(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var started struct {
		ID    string `json:"id"`
		Total int    `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if started.ID == "" || started.Total != 2 {
		t.Fatalf("started = %#v, want id and total 2", started)
	}

	job := waitForClaudeProbeJob(t, h, started.ID)
	if job.Completed != 2 || job.OK != 1 || job.Disabled != 1 || job.PermanentDisabled != 1 || job.ManualDisabled != 0 || job.Failed != 1 {
		t.Fatalf("job summary = completed=%d ok=%d disabled=%d permanent=%d manual=%d failed=%d",
			job.Completed, job.OK, job.Disabled, job.PermanentDisabled, job.ManualDisabled, job.Failed)
	}
	if len(job.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(job.Results))
	}
	var bannedResult *claudeProbeResult
	for i := range job.Results {
		if job.Results[i].Name == "claude-banned.json" {
			bannedResult = &job.Results[i]
			break
		}
	}
	if bannedResult == nil {
		t.Fatalf("banned probe result missing: %#v", job.Results)
	}
	if bannedResult.Status != "permanent_disabled" || bannedResult.RouteState != "permanent_disabled" || bannedResult.Recoverability != "permanent" || !bannedResult.CleanupRecommended {
		t.Fatalf("banned result = %#v, want permanent disabled cleanup recommendation", *bannedResult)
	}

	updatedBanned, ok := manager.GetByID("claude-banned")
	if !ok {
		t.Fatal("banned auth not found")
	}
	if !updatedBanned.Disabled || updatedBanned.Status != coreauth.StatusDisabled {
		t.Fatalf("banned disabled/status = %v/%s, want true/disabled", updatedBanned.Disabled, updatedBanned.Status)
	}
	if updatedBanned.LastError == nil || updatedBanned.LastError.Code != "organization_disabled" {
		t.Fatalf("banned LastError = %#v, want organization_disabled", updatedBanned.LastError)
	}
}

func TestClaudeProbeJobDetectsUsageOAuthNotAllowedForOrganization(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/oauth/profile":
			_, _ = w.Write([]byte(`{"account":{"email":"blocked@example.test","has_claude_pro":true},"organization":{"subscription_status":"active"}}`))
		case "/api/oauth/usage":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."},"request_id":"req_123"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	originalProfileURL := claudeOAuthProfileURL
	originalUsageURL := claudeOAuthUsageURL
	claudeOAuthProfileURL = upstream.URL + "/api/oauth/profile"
	claudeOAuthUsageURL = upstream.URL + "/api/oauth/usage"
	t.Cleanup(func() {
		claudeOAuthProfileURL = originalProfileURL
		claudeOAuthUsageURL = originalUsageURL
	})

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-oauth-not-allowed",
		FileName: "claude-oauth-not-allowed.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": "claude-oauth-not-allowed.json",
		},
		Metadata: map[string]any{
			"type":         "claude",
			"email":        "blocked@example.test",
			"access_token": "blocked-token",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/claude-probe-jobs", strings.NewReader(`{"names":["claude-oauth-not-allowed.json"]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostClaudeProbeJob(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start response: %v", err)
	}

	job := waitForClaudeProbeJob(t, h, started.ID)
	if job.Completed != 1 || job.PermanentDisabled != 1 || job.Disabled != 1 || job.Failed != 1 {
		t.Fatalf("job summary = completed=%d permanent=%d disabled=%d failed=%d", job.Completed, job.PermanentDisabled, job.Disabled, job.Failed)
	}
	if len(job.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(job.Results))
	}
	result := job.Results[0]
	if result.Status != "permanent_disabled" || result.Reason != "organization_disabled" || result.RouteState != "permanent_disabled" || result.Recoverability != "permanent" {
		t.Fatalf("result = %#v, want organization permanent disabled", result)
	}

	updated, ok := manager.GetByID("claude-oauth-not-allowed")
	if !ok {
		t.Fatal("updated auth not found")
	}
	if !updated.Disabled || updated.Status != coreauth.StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want true/disabled", updated.Disabled, updated.Status)
	}
	if updated.LastError == nil || updated.LastError.Code != "organization_disabled" || updated.LastError.HTTPStatus != http.StatusForbidden {
		t.Fatalf("LastError = %#v, want organization_disabled 403", updated.LastError)
	}
}

func TestClaudeProbeTargetsSkipHiddenRemovedAuths(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	for _, auth := range []*coreauth.Auth{
		{
			ID:       "claude-visible",
			FileName: "claude-visible.json",
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": "claude-visible.json",
			},
			Metadata: map[string]any{
				"type":         "claude",
				"access_token": "visible-token",
			},
		},
		{
			ID:            "claude-removed-disabled",
			FileName:      "claude-removed-disabled.json",
			Provider:      "claude",
			Status:        coreauth.StatusDisabled,
			StatusMessage: "removed via management api",
			Disabled:      true,
			Attributes: map[string]string{
				"path": "claude-removed-disabled.json",
			},
			Metadata: map[string]any{
				"type":         "claude",
				"access_token": "removed-token",
			},
		},
		{
			ID:       "claude-hidden-quota",
			FileName: "claude-hidden-quota.json",
			Provider: "claude",
			Status:   coreauth.StatusError,
			Quota: coreauth.QuotaState{
				Exceeded:      true,
				Reason:        "Claude quota cooldown",
				NextRecoverAt: time.Now().Add(time.Hour),
			},
			Metadata: map[string]any{
				"type":         "claude",
				"access_token": "quota-token",
			},
		},
	} {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	targets := h.claudeProbeTargets(claudeProbeJobRequest{})

	if len(targets) != 1 {
		t.Fatalf("targets len = %d, want 1", len(targets))
	}
	if targets[0].ID != "claude-visible" {
		t.Fatalf("target ID = %q, want claude-visible", targets[0].ID)
	}
}

func TestClaudeProbeJobMarksMissingClaudeTokenAsAuthExpired(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-missing-token",
		FileName: "claude-missing-token.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": "claude-missing-token.json",
		},
		Metadata: map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/claude-probe-jobs", strings.NewReader(`{"names":["claude-missing-token.json"]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostClaudeProbeJob(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start response: %v", err)
	}

	job := waitForClaudeProbeJob(t, h, started.ID)
	if job.Completed != 1 || job.AuthExpired != 1 || job.Disabled != 1 {
		t.Fatalf("job summary = completed=%d auth_expired=%d disabled=%d", job.Completed, job.AuthExpired, job.Disabled)
	}

	updated, ok := manager.GetByID("claude-missing-token")
	if !ok {
		t.Fatal("auth not found")
	}
	if !updated.Disabled || updated.Status != coreauth.StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want true/disabled", updated.Disabled, updated.Status)
	}
	if updated.LastError == nil || updated.LastError.Code != "auth_expired" {
		t.Fatalf("LastError = %#v, want auth_expired", updated.LastError)
	}
}

func TestClaudeProbeJobDoesNotTreatHTMLProfileAsHealthy(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html></html>`))
	}))
	defer upstream.Close()

	originalProfileURL := claudeOAuthProfileURL
	originalUsageURL := claudeOAuthUsageURL
	claudeOAuthProfileURL = upstream.URL + "/api/oauth/profile"
	claudeOAuthUsageURL = upstream.URL + "/api/oauth/usage"
	t.Cleanup(func() {
		claudeOAuthProfileURL = originalProfileURL
		claudeOAuthUsageURL = originalUsageURL
	})

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-html",
		FileName: "claude-html.json",
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": "claude-html.json",
		},
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "access-token",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/claude-probe-jobs", strings.NewReader(`{"names":["claude-html.json"]}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostClaudeProbeJob(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatalf("decode start response: %v", err)
	}

	job := waitForClaudeProbeJob(t, h, started.ID)
	if job.Completed != 1 || job.OK != 0 || job.Failed != 1 {
		t.Fatalf("job summary = completed=%d ok=%d failed=%d", job.Completed, job.OK, job.Failed)
	}
	updated, ok := manager.GetByID("claude-html")
	if !ok {
		t.Fatal("auth not found")
	}
	if updated.LastError == nil || updated.LastError.Code != "profile_probe_invalid_json" {
		t.Fatalf("LastError = %#v, want profile_probe_invalid_json", updated.LastError)
	}
}

func waitForClaudeProbeJob(t *testing.T, h *Handler, id string) *claudeProbeJobSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := h.claudeProbeJobSnapshot(id)
		if ok && job.Status != "running" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job, ok := h.claudeProbeJobSnapshot(id); ok {
		t.Fatalf("timed out waiting for job %s: status=%s completed=%d/%d", id, job.Status, job.Completed, job.Total)
	}
	t.Fatalf("timed out waiting for missing job %s", id)
	return nil
}

func TestGetClaudeProbeJobReturnsSnapshot(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(&memoryAuthStore{}, nil, nil))
	h.claudeProbeMu.Lock()
	h.claudeProbeJobs["job-test"] = &claudeProbeJob{
		ID:        "job-test",
		Status:    "completed",
		CreatedAt: time.Now(),
		Total:     1,
		Completed: 1,
		OK:        1,
	}
	h.claudeProbeMu.Unlock()

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Params = gin.Params{{Key: "id", Value: "job-test"}}
	ctx.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v0/management/auth-files/claude-probe-jobs/%s", "job-test"), nil)

	h.GetClaudeProbeJob(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload["id"] != "job-test" || payload["status"] != "completed" {
		t.Fatalf("payload = %#v, want completed job-test", payload)
	}
}
