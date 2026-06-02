package management

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClearClaudeQuotaCooldownStateClearsAccountAndModelQuota(t *testing.T) {
	future := time.Now().Add(2 * time.Hour)
	auth := &coreauth.Auth{
		Quota: coreauth.QuotaState{Exceeded: true, Reason: "five_hour remaining 20% <= 20%", NextRecoverAt: future},
		Metadata: map[string]any{
			"session_window_start":         "2026-06-02T00:00:00Z",
			"session_window_end":           "2026-06-02T05:00:00Z",
			"session_window_utilization":   0.85,
			"passive_usage_7d_utilization": 0.5,
			"passive_usage_7d_reset":       "2026-06-09T00:00:00Z",
			"passive_usage_sampled_at":     "2026-06-02T01:00:00Z",
			"unified_status":               "allowed_warning",
			"unified_representative_claim": "five_hour",
			"refresh_token":                "keep-me",
		},
		ModelStates: map[string]*coreauth.ModelState{
			"claude-sonnet-4-5": {
				Quota:          coreauth.QuotaState{Exceeded: true, NextRecoverAt: future},
				Unavailable:    true,
				NextRetryAfter: future,
			},
		},
	}

	clearClaudeQuotaCooldownState(auth)

	if auth.Quota.Exceeded {
		t.Fatalf("account Quota.Exceeded should be cleared")
	}
	for _, k := range []string{
		"session_window_start", "session_window_end", "session_window_status",
		"session_window_utilization", "passive_usage_7d_utilization",
		"passive_usage_7d_reset", "passive_usage_sampled_at",
		"unified_status", "unified_representative_claim",
	} {
		if _, ok := auth.Metadata[k]; ok {
			t.Fatalf("metadata key %q should be deleted", k)
		}
	}
	if v, _ := auth.Metadata["refresh_token"].(string); v != "keep-me" {
		t.Fatalf("credential field refresh_token must be preserved, got %q", v)
	}
	ms := auth.ModelStates["claude-sonnet-4-5"]
	if ms.Quota.Exceeded {
		t.Fatalf("per-model Quota.Exceeded should be cleared")
	}
	if !ms.NextRetryAfter.IsZero() {
		t.Fatalf("per-model future NextRetryAfter should be cleared")
	}
}

func TestClearClaudeQuotaCooldownStateNilSafe(t *testing.T) {
	clearClaudeQuotaCooldownState(nil)
	clearClaudeQuotaCooldownState(&coreauth.Auth{})
}
