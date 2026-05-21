package auth

import (
	"context"
	"sync/atomic"
	"testing"
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
