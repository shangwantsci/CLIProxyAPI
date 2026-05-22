package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type testTokenStorage struct {
	meta map[string]any
}

func (s *testTokenStorage) SetMetadata(meta map[string]any) { s.meta = meta }

func (s *testTokenStorage) SaveTokenToFile(authFilePath string) error {
	raw, err := json.Marshal(s.meta)
	if err != nil {
		return err
	}
	return os.WriteFile(authFilePath, raw, 0o600)
}

func TestFileTokenStore_Save_DisabledPersistsFlagForTokenStorage(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "disabled.json")

	if err := os.WriteFile(path, []byte(`{"type":"test","disabled":true}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	storage := &testTokenStorage{}

	auth := &cliproxyauth.Auth{
		ID:       "disabled.json",
		Provider: "test",
		FileName: "disabled.json",
		Disabled: true,
		Storage:  storage,
		Metadata: map[string]any{"type": "test"},
	}

	if _, err := store.Save(ctx, auth); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal auth file: %v", err)
	}
	if disabled, _ := meta["disabled"].(bool); !disabled {
		t.Fatalf("disabled=%v, want true (raw=%s)", meta["disabled"], string(raw))
	}
}

func TestFileTokenStore_Save_PersistsRuntimeErrorState(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "claude-disabled.json")
	if err := os.WriteFile(path, []byte(`{"type":"claude","email":"user@example.com","disabled":false}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)

	auth := &cliproxyauth.Auth{
		ID:            "claude-disabled.json",
		Provider:      "claude",
		FileName:      "claude-disabled.json",
		Status:        cliproxyauth.StatusDisabled,
		StatusMessage: "This organization has been disabled.",
		Disabled:      true,
		Unavailable:   true,
		Metadata:      map[string]any{"type": "claude", "email": "user@example.com"},
		LastError: &cliproxyauth.Error{
			Code:       "organization_disabled",
			Message:    "This organization has been disabled.",
			HTTPStatus: http.StatusBadRequest,
			Retryable:  false,
		},
	}

	if _, err := store.Save(ctx, auth); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("List() returned %d auths, want 1", len(loaded))
	}
	got := loaded[0]
	if !got.Disabled || got.Status != cliproxyauth.StatusDisabled || !got.Unavailable {
		t.Fatalf("state disabled/status/unavailable = %v/%s/%v, want true/disabled/true", got.Disabled, got.Status, got.Unavailable)
	}
	if got.StatusMessage != "This organization has been disabled." {
		t.Fatalf("StatusMessage = %q", got.StatusMessage)
	}
	if got.LastError == nil {
		t.Fatal("LastError is nil after reload")
	}
	if got.LastError.Code != "organization_disabled" || got.LastError.HTTPStatus != http.StatusBadRequest || got.LastError.Retryable {
		t.Fatalf("LastError = %#v, want organization_disabled 400 non-retryable", got.LastError)
	}
}
