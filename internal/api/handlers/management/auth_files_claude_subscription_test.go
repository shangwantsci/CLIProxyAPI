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
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestApplyClaudeTokenStorageMetadataPersistsSubscriptionFields(t *testing.T) {
	metadata := map[string]any{}
	applyClaudeTokenStorageMetadata(metadata, &claudeauth.ClaudeTokenStorage{
		OrganizationName: "LiteracyIndia",
		PlanType:         "team",
		SubscriptionTier: "team",
	})

	if got := metadata["organization_name"]; got != "LiteracyIndia" {
		t.Fatalf("organization_name = %v, want LiteracyIndia", got)
	}
	if got := metadata["plan_type"]; got != "team" {
		t.Fatalf("plan_type = %v, want team", got)
	}
	if got := metadata["subscription_tier"]; got != "team" {
		t.Fatalf("subscription_tier = %v, want team", got)
	}
}

func TestApplyClaudeOAuthProfileSubscriptionMetadataPersistsPlan(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/profile" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q, want Bearer access-token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"account":{"email":"user@example.test","has_claude_pro":false,"has_claude_max":false},
			"organization":{"name":"Team Org","organization_type":"claude_team","subscription_status":"active"}
		}`))
	}))
	defer upstream.Close()

	oldProfileURL := claudeOAuthProfileURL
	claudeOAuthProfileURL = upstream.URL + "/api/oauth/profile"
	defer func() { claudeOAuthProfileURL = oldProfileURL }()

	metadata := map[string]any{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.applyClaudeOAuthProfileSubscriptionMetadata(context.Background(), metadata, &claudeauth.ClaudeTokenStorage{
		AccessToken: "access-token",
		Scope:       "user:profile user:inference",
	}, "")

	if got := metadata["plan_type"]; got != "team" {
		t.Fatalf("plan_type = %v, want team", got)
	}
	if got := metadata["organization_name"]; got != "Team Org" {
		t.Fatalf("organization_name = %v, want Team Org", got)
	}
	if got := metadata["subscription_status"]; got != "active" {
		t.Fatalf("subscription_status = %v, want active", got)
	}
}

func TestApplyClaudeProfileSubscriptionMetadataDistinguishesMax20x(t *testing.T) {
	metadata := map[string]any{}
	applyClaudeProfileSubscriptionMetadata(metadata, []byte(`{
		"account":{"email":"max@example.test","has_claude_max":true},
		"organization":{
			"name":"Max Org",
			"subscription_status":"active",
			"rate_limit_tier":"default_claude_max_20x"
		}
	}`))

	if got := metadata["plan_type"]; got != "max20x" {
		t.Fatalf("plan_type = %v, want max20x", got)
	}
	if got := metadata["subscription_multiplier"]; got != 20 {
		t.Fatalf("subscription_multiplier = %v, want 20", got)
	}
	if got := metadata["subscription_precision"]; got != "exact" {
		t.Fatalf("subscription_precision = %v, want exact", got)
	}
}

func TestListClaudeAuthHealthCapacitySummaryExcludesUnknownMax(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	now := time.Now().UTC()
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{
			ID:       "claude-pro",
			FileName: "claude-pro.json",
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{"type": "claude", "plan_type": "pro"},
		},
		{
			ID:       "claude-max5",
			FileName: "claude-max5.json",
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{"type": "claude", "plan_type": "max5x", "subscription_multiplier": 5},
		},
		{
			ID:       "claude-max-unknown",
			FileName: "claude-max-unknown.json",
			Provider: "claude",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{"type": "claude", "plan_type": "max"},
		},
		{
			ID:             "claude-max20-cooling",
			FileName:       "claude-max20-cooling.json",
			Provider:       "claude",
			Status:         coreauth.StatusError,
			Unavailable:    true,
			NextRetryAfter: now.Add(30 * time.Minute),
			Quota: coreauth.QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: now.Add(30 * time.Minute),
			},
			Metadata: map[string]any{"type": "claude", "plan_type": "max20x", "subscription_multiplier": 20},
		},
	}
	for _, auth := range auths {
		path := filepath.Join(authDir, auth.FileName)
		if err := os.WriteFile(path, []byte(`{"type":"claude"}`), 0o600); err != nil {
			t.Fatalf("write %s: %v", auth.FileName, err)
		}
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
		auth.Attributes["path"] = path
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register %s: %v", auth.ID, err)
		}
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
		Summary map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	capacity, ok := payload.Summary["capacity"].(map[string]any)
	if !ok {
		t.Fatalf("summary.capacity = %T, want object; summary=%#v", payload.Summary["capacity"], payload.Summary)
	}
	if got, _ := capacity["known_total_units"].(float64); got != 26 {
		t.Fatalf("known_total_units = %v, want 26", capacity["known_total_units"])
	}
	if got, _ := capacity["available_units"].(float64); got != 6 {
		t.Fatalf("available_units = %v, want 6", capacity["available_units"])
	}
	if got, _ := capacity["cooling_units"].(float64); got != 20 {
		t.Fatalf("cooling_units = %v, want 20", capacity["cooling_units"])
	}
	if got, _ := capacity["unknown_max_accounts"].(float64); got != 1 {
		t.Fatalf("unknown_max_accounts = %v, want 1", capacity["unknown_max_accounts"])
	}
}

func TestBuildAuthFileEntryExposesClaudeSubscriptionFields(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "claude-user@example.test.json")
	if err := os.WriteFile(path, []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	entry := h.buildAuthFileEntry(&coreauth.Auth{
		ID:       "claude-user@example.test.json",
		Provider: "claude",
		FileName: "claude-user@example.test.json",
		Metadata: map[string]any{
			"plan_type":               "max5x",
			"subscription_multiplier": 5,
			"subscription_precision":  "exact",
			"subscription_status":     "active",
			"organization_name":       "Team Org",
		},
		Attributes: map[string]string{"path": path},
	})

	if got := entry["plan_type"]; got != "max5x" {
		t.Fatalf("plan_type = %v, want max5x", got)
	}
	if got := entry["subscription_multiplier"]; got != "5" {
		t.Fatalf("subscription_multiplier = %v, want 5", got)
	}
	if got := entry["subscription_precision"]; got != "exact" {
		t.Fatalf("subscription_precision = %v, want exact", got)
	}
	if got := entry["subscription_status"]; got != "active" {
		t.Fatalf("subscription_status = %v, want active", got)
	}
	if got := entry["organization_name"]; got != "Team Org" {
		t.Fatalf("organization_name = %v, want Team Org", got)
	}
}
