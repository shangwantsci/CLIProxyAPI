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

func TestManagerMarkResultFailurePathAppliesPassiveQuotaWhenUtilBelowFull(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	reset5h := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	auth := &Auth{
		ID:       "claude-fail-5h",
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
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.85")
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
	state := updated.ModelStates["claude-sonnet-4-5"]
	if state == nil || !state.Unavailable || !state.Quota.Exceeded {
		t.Fatalf("model quota state = %#v, want passive quota cooldown", state)
	}
	assertTimeNear(t, state.NextRetryAfter, reset5h)
	if state.LastError == nil || state.LastError.Code != "quota_exhausted" {
		t.Fatalf("LastError = %#v, want quota_exhausted", state.LastError)
	}
}

// 缝隙b:非 429 失败(503)带超阈 util,现有路径完全不覆盖,被动配额应冷却。
func TestManagerMarkResultFailurePathAppliesPassiveQuotaOn5xx(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	reset7d := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	auth := &Auth{
		ID:       "claude-fail-5xx",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"auth_source": "claude_setup_token", "scope": "user:inference"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(reset7d.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.95")
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    "claude-sonnet-4-5",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream 503"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	state := updated.ModelStates["claude-sonnet-4-5"]
	if state == nil || !state.Quota.Exceeded {
		t.Fatalf("model quota state = %#v, want passive quota cooldown on 5xx", state)
	}
	assertTimeNear(t, state.NextRetryAfter, reset7d)
}

// 只延长不缩短:429 surpassed-threshold=true 让现有路径算出 7d reset(+5h),
// 被动配额 5h reset 只有 +30min,最终必须保留 +5h。
func TestManagerMarkResultFailurePathPassiveQuotaNeverShortens(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	reset5h := time.Now().UTC().Truncate(time.Second).Add(30 * time.Minute)
	reset7d := time.Now().UTC().Truncate(time.Second).Add(5 * time.Hour)
	auth := &Auth{
		ID:       "claude-fail-noshrink",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"auth_source": "claude_setup_token", "scope": "user:inference"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.85")
	headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(reset7d.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.95")
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
	state := updated.ModelStates["claude-sonnet-4-5"]
	if state == nil {
		t.Fatal("model state missing")
	}
	// 现有 429 路径用 7d reset(+5h);被动配额 5h reset(+30min) 不得缩短它。
	assertTimeNear(t, state.NextRetryAfter, reset7d)
}

// disableCooling 守卫:disable_cooling=true 时,被动配额也不得追加冷却。
func TestManagerMarkResultFailurePathPassiveQuotaRespectsDisableCooling(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	reset5h := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	auth := &Auth{
		ID:       "claude-fail-nocool",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"auth_source":     "claude_setup_token",
			"scope":           "user:inference",
			"disable_cooling": true,
		},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.85")
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
	state := updated.ModelStates["claude-sonnet-4-5"]
	if state == nil {
		t.Fatal("model state missing")
	}
	// The base 429 failure path always sets Quota.Exceeded=true regardless of disable_cooling
	// (the disable_cooling guard only zeroes the cooldown duration, not the Exceeded flag).
	// The discriminating signal that the passive-quota append was correctly skipped is that
	// NextRetryAfter stays zero: had the passive branch run it would have set NextRetryAfter
	// to reset5h (+2h) and replaced LastError with a "quota_exhausted" verdict.
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero under disable_cooling", state.NextRetryAfter)
	}
	if state.LastError != nil && state.LastError.Code == "quota_exhausted" {
		t.Fatalf("passive quota must not append when disable_cooling=true, got %#v", state.LastError)
	}
}

// 永久禁用不被覆盖:organization_disabled 走永久禁用分支,不进 per-model 追加。
func TestManagerMarkResultFailurePathPermanentDisableNotOverridden(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	reset5h := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	auth := &Auth{
		ID:       "claude-fail-perm",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"auth_source": "claude_setup_token", "scope": "user:inference"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(reset5h.Unix(), 10))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.85")
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "claude",
		Model:    "claude-sonnet-4-5",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusBadRequest, Message: `{"type":"error","error":{"type":"invalid_request_error","message":"This organization has been disabled."}}`},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	// The permanent-disable branch must win: the credential is disabled and the model state must
	// not be overwritten with the passive-quota "quota_exhausted" verdict.
	if !updated.Disabled || updated.LastError == nil || updated.LastError.Code != "organization_disabled" {
		t.Fatalf("auth = disabled %v lastError %#v, want organization_disabled permanent disable", updated.Disabled, updated.LastError)
	}
	state := updated.ModelStates["claude-sonnet-4-5"]
	if state != nil && state.LastError != nil && state.LastError.Code == "quota_exhausted" {
		t.Fatalf("permanent disable must not be overridden by passive quota, got %#v", state.LastError)
	}
}

// 被动配额冷却不得清零已累加的 BackoffLevel(只延长不缩短的延伸:
// 冷却的所有维度都不该被削弱)。429 无 reset 头时退避把 BackoffLevel 累加到 1,
// 追加的被动配额冷却必须保留它。
func TestManagerMarkResultFailurePathPassiveQuotaPreservesBackoffLevel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ClaudeQuotaCoolingThresholds: internalconfig.ClaudeQuotaCoolingThresholds{
			FiveHourRemainingPercent: 20,
			WeeklyRemainingPercent:   10,
		},
	})
	auth := &Auth{
		ID:       "claude-fail-backoff",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"auth_source": "claude_setup_token", "scope": "user:inference"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	// 429 带超阈 5h util 但不带任何 reset 头:result.RetryAfter 为 nil,
	// case 429 走 nextQuotaCooldown 指数退避,把 BackoffLevel 累加到 1。
	// 被动配额随后基于 util 触发冷却(recoverAt 回退 now+30min),必须保留 BackoffLevel=1。
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.85")
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
	state := updated.ModelStates["claude-sonnet-4-5"]
	if state == nil || !state.Quota.Exceeded {
		t.Fatalf("model quota state = %#v, want quota cooldown", state)
	}
	if state.Quota.BackoffLevel < 1 {
		t.Fatalf("Quota.BackoffLevel = %d, want >= 1 (passive quota must not reset it)", state.Quota.BackoffLevel)
	}
}
