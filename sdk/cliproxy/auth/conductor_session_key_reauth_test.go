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
