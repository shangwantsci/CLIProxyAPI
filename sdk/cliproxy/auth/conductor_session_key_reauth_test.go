package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type sessionKeyReauthFailExecutor struct {
	provider string
}

func (e sessionKeyReauthFailExecutor) Identifier() string { return e.provider }
func (e sessionKeyReauthFailExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e sessionKeyReauthFailExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e sessionKeyReauthFailExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return nil, errors.New(`session_key reauth failed: token refresh failed with status 400: {"error":"invalid_grant"}`)
}
func (e sessionKeyReauthFailExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e sessionKeyReauthFailExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRefreshAuthSessionKeyRetryKeepsSchedulable(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(sessionKeyReauthFailExecutor{provider: "claude"})

	auth := &Auth{
		ID:       "claude-retry",
		Provider: "claude",
		Metadata: map[string]any{"session_key": "sk-ant-sid01-seed"},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, _ := manager.GetByID(auth.ID)
	if updated.Disabled {
		t.Fatal("first failure with seed must not disable the account")
	}
	if updated.LastError == nil || updated.LastError.Code != "reauth_pending" {
		t.Fatalf("LastError.Code = %v, want reauth_pending", updated.LastError)
	}
	if hasUnauthorizedAuthFailure(updated) {
		t.Fatal("retry state must not be classified as unauthorized failure")
	}
	if cnt := claudeReauthFailureCount(updated.Metadata); cnt != 1 {
		t.Fatalf("failure count = %d, want 1", cnt)
	}
	if !updated.NextRefreshAfter.After(time.Now()) {
		t.Fatalf("NextRefreshAfter must be in the future, got %v", updated.NextRefreshAfter)
	}
	if !updated.NextRetryAfter.After(time.Now()) {
		t.Fatalf("NextRetryAfter must be in the future for reauth_pending (account must leave request-routing pool), got %v", updated.NextRetryAfter)
	}
}

func TestRefreshAuthSessionKeyExhaustionDisablesAndClearsQuota(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(sessionKeyReauthFailExecutor{provider: "claude"})

	auth := &Auth{
		ID:       "claude-exhaust",
		Provider: "claude",
		Quota:    QuotaState{Exceeded: true, NextRecoverAt: time.Now().Add(time.Hour)},
		Metadata: map[string]any{
			"session_key":                 "sk-ant-sid01-seed",
			"session_key_reauth_failures": float64(claudeSessionKeyReauthMaxAttempts - 1),
			"session_window_status":       "rejected",
			"session_window_utilization":  1.0,
		},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, _ := manager.GetByID(auth.ID)
	if !updated.Disabled || updated.Status != StatusDisabled {
		t.Fatalf("expected permanent disable, got disabled=%v status=%s", updated.Disabled, updated.Status)
	}
	if updated.LastError == nil || updated.LastError.Code != "unauthorized" {
		t.Fatalf("LastError.Code = %v, want unauthorized after exhaustion", updated.LastError)
	}
	if updated.Quota.Exceeded {
		t.Fatal("P3: stale quota must be cleared on permanent disable")
	}
	if _, ok := updated.Metadata["session_window_status"]; ok {
		t.Fatal("P3: stale passive-usage must be cleared on permanent disable")
	}
}

func TestRefreshAuthNoSeedDisablesImmediately(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(sessionKeyReauthFailExecutor{provider: "claude"})

	auth := &Auth{
		ID:       "claude-noseed",
		Provider: "claude",
		Metadata: map[string]any{},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	manager.refreshAuth(ctx, auth.ID)

	updated, _ := manager.GetByID(auth.ID)
	if !updated.Disabled {
		t.Fatal("no seed must disable immediately on unauthorized failure")
	}
}

func TestRefreshAuthSessionKeyProgressionAcrossTicks(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(sessionKeyReauthFailExecutor{provider: "claude"})

	auth := &Auth{
		ID:       "claude-progression",
		Provider: "claude",
		Metadata: map[string]any{"session_key": "sk-ant-sid01-seed"},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Tick 1: failures 0 -> 1, retry, schedulable, backoff set in the future.
	manager.refreshAuth(ctx, auth.ID)
	u1, _ := manager.GetByID(auth.ID)
	if u1.Disabled {
		t.Fatal("tick 1 must not disable")
	}
	if u1.LastError == nil || u1.LastError.Code != "reauth_pending" {
		t.Fatalf("tick 1 Code = %v, want reauth_pending", u1.LastError)
	}
	if c := claudeReauthFailureCount(u1.Metadata); c != 1 {
		t.Fatalf("tick 1 count = %d, want 1", c)
	}
	if !u1.NextRefreshAfter.After(time.Now()) {
		t.Fatalf("tick 1 NextRefreshAfter must be in the future, got %v", u1.NextRefreshAfter)
	}
	if !u1.NextRetryAfter.After(time.Now()) {
		t.Fatalf("tick 1 NextRetryAfter must be in the future, got %v", u1.NextRetryAfter)
	}

	// Tick 2: failures 1 -> 2, still retry (not disabled).
	manager.refreshAuth(ctx, auth.ID)
	u2, _ := manager.GetByID(auth.ID)
	if u2.Disabled {
		t.Fatal("tick 2 must not disable (failures 1->2, still < cap)")
	}
	if u2.LastError == nil || u2.LastError.Code != "reauth_pending" {
		t.Fatalf("tick 2 Code = %v, want reauth_pending", u2.LastError)
	}
	if c := claudeReauthFailureCount(u2.Metadata); c != 2 {
		t.Fatalf("tick 2 count = %d, want 2", c)
	}

	// Tick 3: failures=2, 2+1 < 3 is false -> permanent disable.
	manager.refreshAuth(ctx, auth.ID)
	u3, _ := manager.GetByID(auth.ID)
	if !u3.Disabled || u3.Status != StatusDisabled {
		t.Fatalf("tick 3 must permanently disable, got disabled=%v status=%s", u3.Disabled, u3.Status)
	}
	if u3.LastError == nil || u3.LastError.Code != "unauthorized" {
		t.Fatalf("tick 3 Code = %v, want unauthorized", u3.LastError)
	}
}
