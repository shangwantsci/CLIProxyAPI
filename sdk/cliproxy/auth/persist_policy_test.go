package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

type countingStore struct {
	saveCount atomic.Int32
	last      *Auth
}

func (s *countingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *countingStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.saveCount.Add(1)
	if auth != nil {
		s.last = auth.Clone()
	}
	return "", nil
}

func (s *countingStore) Delete(context.Context, string) error { return nil }

func TestWithSkipPersist_DisablesUpdatePersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected 1 Save call, got %d", got)
	}

	ctxSkip := WithSkipPersist(context.Background())
	if _, err := mgr.Update(ctxSkip, auth); err != nil {
		t.Fatalf("Update(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected Save call count to remain 1, got %d", got)
	}
}

func TestWithSkipPersist_DisablesRegisterPersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}
}

func TestManagerMarkResultSuccessDoesNotPersistRuntimeOnlyStats(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "claude"},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "claude",
		Model:    "claude-opus-4-8",
		Success:  true,
	})

	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected successful result without persistent state changes to skip Save, got %d", got)
	}
	updated, ok := mgr.GetByID("auth-1")
	if !ok {
		t.Fatal("auth not found")
	}
	if stats := updated.Quality24hStats(time.Now()); stats.Requests != 1 || stats.Success != 1 {
		t.Fatalf("Quality24hStats = %#v, want one successful request", stats)
	}
}

func TestManagerMarkResultSuccessWithPassiveQuotaHeadersDoesNotPersistAllowedState(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "claude"},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}

	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	ctx := logging.WithResponseHeadersHolder(context.Background())
	logging.SetResponseHeaders(ctx, headers)
	mgr.MarkResult(ctx, Result{
		AuthID:   "auth-1",
		Provider: "claude",
		Model:    "claude-opus-4-8",
		Success:  true,
	})

	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("successful passive quota sample should stay in memory without Save, got %d", got)
	}
	updated, ok := mgr.GetByID("auth-1")
	if !ok {
		t.Fatal("auth not found")
	}
	if got := updated.Metadata["session_window_status"]; got != "allowed" {
		t.Fatalf("session_window_status = %v, want allowed", got)
	}
}

func TestManagerMarkResultQuotaFailurePersistsAndInvalidatesSessionAffinity(t *testing.T) {
	store := &countingStore{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      5 * time.Minute,
	})
	mgr := NewManager(store, selector, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"type": "claude"},
	}
	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	mgr.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "claude",
		Model:    "claude-sonnet-4-6",
		Success:  true,
	})
	cacheKey := "mixed::header:s1::claude-opus-4-8"
	selector.cache.Set(cacheKey, "auth-1")

	mgr.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "claude",
		Model:    "claude-opus-4-8",
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "rate limit exceeded",
			Retryable:  true,
		},
	})

	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected quota failure to persist once, got %d", got)
	}
	if _, ok := selector.cache.Get(cacheKey); ok {
		t.Fatalf("expected quota failure to invalidate session-affinity cache for auth")
	}
}

func TestManager_PersistRuntimeMetadataOnlyMergesClaudeDeviceProfile(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	if _, err := mgr.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"type": "claude", "access_token": "stored-token"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	runtimeAuth := &Auth{
		ID: "auth-1",
		Metadata: map[string]any{
			"access_token": "runtime-token",
			"claude_device_profile": map[string]any{
				"user_agent":      "claude-cli/2.1.93 (external, cli)",
				"package_version": "0.71.0",
				"runtime_version": "v24.14.0",
				"os":              "MacOS",
				"arch":            "arm64",
			},
			"claude_device_profile_updated_at": "2026-05-21T12:00:00Z",
		},
	}

	mgr.persistRuntimeMetadata(context.Background(), runtimeAuth)
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("Save count = %d, want 1", got)
	}

	updated, ok := mgr.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to remain registered")
	}
	if got, _ := updated.Metadata["access_token"].(string); got != "stored-token" {
		t.Fatalf("access_token = %q, want stored-token", got)
	}
	if got, _ := updated.Metadata["claude_device_profile_updated_at"].(string); got != "2026-05-21T12:00:00Z" {
		t.Fatalf("claude_device_profile_updated_at = %q, want runtime timestamp", got)
	}
	profile, ok := updated.Metadata["claude_device_profile"].(map[string]any)
	if !ok {
		t.Fatalf("claude_device_profile = %T, want map[string]any", updated.Metadata["claude_device_profile"])
	}
	if got, _ := profile["user_agent"].(string); got != "claude-cli/2.1.93 (external, cli)" {
		t.Fatalf("claude_device_profile.user_agent = %q, want learned UA", got)
	}

	mgr.persistRuntimeMetadata(context.Background(), runtimeAuth)
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("Save count after unchanged metadata = %d, want 1", got)
	}
}
