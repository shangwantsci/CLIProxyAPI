package auth

import (
	"testing"
	"time"
)

func TestClearClaudeQuotaAndPassiveUsage(t *testing.T) {
	auth := &Auth{
		ID: "claude-x",
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota_exhausted",
			NextRecoverAt: time.Now().Add(time.Hour),
			BackoffLevel:  2,
		},
		Metadata: map[string]any{
			"session_window_start":         "2026-06-01T00:00:00Z",
			"session_window_end":           "2026-06-01T05:00:00Z",
			"session_window_status":        "rejected",
			"session_window_utilization":   1.0,
			"passive_usage_7d_utilization": 0.95,
			"passive_usage_7d_reset":       int64(1750000000),
			"passive_usage_sampled_at":     "2026-06-01T01:00:00Z",
			"unified_status":               "rejected",
			"unified_representative_claim": "seven_day",
			"access_token":                 "keep-me",
			"session_key":                  "keep-me-too",
		},
	}

	clearClaudeQuotaAndPassiveUsage(auth)

	if auth.Quota != (QuotaState{}) {
		t.Fatalf("Quota not cleared: %#v", auth.Quota)
	}
	for _, k := range []string{
		"session_window_start", "session_window_end", "session_window_status",
		"session_window_utilization", "passive_usage_7d_utilization",
		"passive_usage_7d_reset", "passive_usage_sampled_at",
		"unified_status", "unified_representative_claim",
	} {
		if _, ok := auth.Metadata[k]; ok {
			t.Fatalf("metadata[%q] should have been deleted", k)
		}
	}
	if auth.Metadata["access_token"] != "keep-me" {
		t.Fatal("access_token must not be cleared")
	}
	if auth.Metadata["session_key"] != "keep-me-too" {
		t.Fatal("session_key must not be cleared")
	}
}

func TestClearClaudeQuotaAndPassiveUsageNilMetadata(t *testing.T) {
	auth := &Auth{ID: "claude-nil", Quota: QuotaState{Exceeded: true}}
	clearClaudeQuotaAndPassiveUsage(auth) // must not panic
	if auth.Quota.Exceeded {
		t.Fatal("Quota should be cleared even with nil metadata")
	}
}
