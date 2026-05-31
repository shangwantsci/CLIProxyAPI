package auth

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

func TestManagerMarkResultStoresClaudePassiveQuotaHeaders(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	reset5h := time.Now().UTC().Truncate(time.Second).Add(4 * time.Hour)
	reset7d := time.Now().UTC().Truncate(time.Second).Add(72 * time.Hour)
	auth := &Auth{
		ID:       "claude-setup-passive",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"auth_source": "claude_setup_token",
			"scope":       "user:inference",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed_warning")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.82")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.44")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(reset7d.Unix(), 10))
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)

	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "claude", Model: "claude-sonnet-4-5", Success: true})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	if got := updated.Metadata["session_window_status"]; got != "allowed_warning" {
		t.Fatalf("session_window_status = %v, want allowed_warning", got)
	}
	if got := updated.Metadata["session_window_start"]; got != reset5h.Add(-5*time.Hour).Format(time.RFC3339) {
		t.Fatalf("session_window_start = %v, want %s", got, reset5h.Add(-5*time.Hour).Format(time.RFC3339))
	}
	if got := updated.Metadata["session_window_end"]; got != reset5h.Format(time.RFC3339) {
		t.Fatalf("session_window_end = %v, want %s", got, reset5h.Format(time.RFC3339))
	}
	if got := metadataFloat(updated.Metadata["session_window_utilization"]); math.Abs(got-0.82) > 1e-9 {
		t.Fatalf("session_window_utilization = %v, want 0.82", got)
	}
	if got := metadataFloat(updated.Metadata["passive_usage_7d_utilization"]); math.Abs(got-0.44) > 1e-9 {
		t.Fatalf("passive_usage_7d_utilization = %v, want 0.44", got)
	}
	if got := metadataInt64(updated.Metadata["passive_usage_7d_reset"]); got != reset7d.Unix() {
		t.Fatalf("passive_usage_7d_reset = %v, want %v", got, reset7d.Unix())
	}
	if updated.Metadata["passive_usage_sampled_at"] == nil {
		t.Fatal("passive_usage_sampled_at is nil")
	}
}

func TestManagerMarkResultUsesClaudeWindowResetFor429Cooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	reset5h := time.Now().UTC().Truncate(time.Second).Add(30 * time.Minute)
	reset7d := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	auth := &Auth{
		ID:       "claude-setup-429",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"auth_source": "claude_setup_token",
			"scope":       "user:inference",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.75")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(reset7d.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-7d-surpassed-threshold", "true")
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    "claude-sonnet-4-5",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	if !updated.Unavailable || !updated.Quota.Exceeded {
		t.Fatalf("quota state = unavailable=%v exceeded=%v, want cooldown", updated.Unavailable, updated.Quota.Exceeded)
	}
	if updated.NextRetryAfter.Sub(reset7d) > 2*time.Second || reset7d.Sub(updated.NextRetryAfter) > 2*time.Second {
		t.Fatalf("NextRetryAfter = %v, want around %v", updated.NextRetryAfter, reset7d)
	}
	if updated.Quota.NextRecoverAt.Sub(reset7d) > 2*time.Second || reset7d.Sub(updated.Quota.NextRecoverAt) > 2*time.Second {
		t.Fatalf("Quota.NextRecoverAt = %v, want around %v", updated.Quota.NextRecoverAt, reset7d)
	}
}

func TestManagerMarkResultAppliesClaudePassiveFiveHourQuotaProtection(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	reset5h := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	auth := &Auth{
		ID:       "claude-setup-passive-5h",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"auth_source": "claude_setup_token",
			"scope":       "user:inference",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed_warning")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.82")
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)

	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "claude", Model: "claude-sonnet-4-5", Success: true})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	state := updated.ModelStates["claude-sonnet-4-5"]
	if state == nil || !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("model quota state = %#v, want passive quota cooldown", state)
	}
	assertTimeNear(t, state.NextRetryAfter, reset5h)
	assertTimeNear(t, updated.NextRetryAfter, reset5h)
	if state.LastError == nil || state.LastError.Code != "quota_exhausted" || !strings.Contains(state.LastError.Message, "five_hour") {
		t.Fatalf("LastError = %#v, want five_hour quota_exhausted", state.LastError)
	}
	if !updated.Unavailable || !updated.Quota.Exceeded {
		t.Fatalf("auth quota state = unavailable=%v exceeded=%v, want cooldown", updated.Unavailable, updated.Quota.Exceeded)
	}
}

func TestManagerMarkResultAppliesClaudePassiveWeeklyQuotaProtection(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	reset7d := time.Now().UTC().Truncate(time.Second).Add(48 * time.Hour)
	auth := &Auth{
		ID:       "claude-setup-passive-weekly",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"auth_source": "claude_setup_token",
			"scope":       "user:inference",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(reset7d.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.91")
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)

	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "claude", Model: "claude-opus-4-5", Success: true})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	state := updated.ModelStates["claude-opus-4-5"]
	if state == nil || !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("model quota state = %#v, want passive quota cooldown", state)
	}
	assertTimeNear(t, state.NextRetryAfter, reset7d)
	if state.LastError == nil || state.LastError.Code != "quota_exhausted" || !strings.Contains(state.LastError.Message, "seven_day") {
		t.Fatalf("LastError = %#v, want seven_day quota_exhausted", state.LastError)
	}
}

func TestManagerMarkResultStoresClaudeUnifiedStatusAndClaim(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	reset5h := time.Now().UTC().Truncate(time.Second).Add(3 * time.Hour)
	auth := &Auth{
		ID:       "claude-unified-headers",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"auth_source": "claude_setup_token",
			"scope":       "user:inference",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.20")
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)

	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "claude", Model: "claude-sonnet-4-5", Success: true})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	if got := updated.Metadata["unified_status"]; got != "allowed" {
		t.Fatalf("unified_status = %v, want allowed", got)
	}
	if got := updated.Metadata["unified_representative_claim"]; got != "five_hour" {
		t.Fatalf("unified_representative_claim = %v, want five_hour", got)
	}
}

func TestManagerMarkResultUsesSevenDayResetWhenClaimIsWeekly(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	// Both windows are over threshold; the 5h window resets sooner, but Anthropic reports the
	// weekly window as the representative (authoritative) claim, so cooldown must follow 7d.
	reset5h := time.Now().UTC().Truncate(time.Second).Add(1 * time.Hour)
	reset7d := time.Now().UTC().Truncate(time.Second).Add(72 * time.Hour)
	auth := &Auth{
		ID:       "claude-claim-weekly",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"auth_source": "claude_setup_token",
			"scope":       "user:inference",
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-representative-claim", "seven_day")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.95")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(reset7d.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.95")
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)

	manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: "claude", Model: "claude-sonnet-4-5", Success: true})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	assertTimeNear(t, updated.NextRetryAfter, reset7d)
}

func metadataFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

func metadataInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

func assertTimeNear(t *testing.T, got, want time.Time) {
	t.Helper()
	if got.Sub(want) > 2*time.Second || want.Sub(got) > 2*time.Second {
		t.Fatalf("time = %v, want around %v", got, want)
	}
}

func TestLaterOf(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	earlier := base.Add(30 * time.Minute)
	later := base.Add(2 * time.Hour)
	zero := time.Time{}

	if got := laterOf(zero, zero); !got.IsZero() {
		t.Fatalf("laterOf(zero,zero) = %v, want zero", got)
	}
	if got := laterOf(zero, later); !got.Equal(later) {
		t.Fatalf("laterOf(zero,later) = %v, want %v", got, later)
	}
	if got := laterOf(later, zero); !got.Equal(later) {
		t.Fatalf("laterOf(later,zero) = %v, want %v", got, later)
	}
	if got := laterOf(earlier, later); !got.Equal(later) {
		t.Fatalf("laterOf(earlier,later) = %v, want %v", got, later)
	}
	if got := laterOf(later, earlier); !got.Equal(later) {
		t.Fatalf("laterOf(later,earlier) = %v, want %v", got, later)
	}
}
