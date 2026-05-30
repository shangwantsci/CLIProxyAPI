package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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
			"plan_type":           "team",
			"subscription_status": "active",
			"organization_name":   "Team Org",
		},
		Attributes: map[string]string{"path": path},
	})

	if got := entry["plan_type"]; got != "team" {
		t.Fatalf("plan_type = %v, want team", got)
	}
	if got := entry["subscription_status"]; got != "active" {
		t.Fatalf("subscription_status = %v, want active", got)
	}
	if got := entry["organization_name"]; got != "Team Org" {
		t.Fatalf("organization_name = %v, want Team Org", got)
	}
}
