package auth

import (
	"context"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFileTokenStoreSaveSetsPathWhenMetadataUnchanged(t *testing.T) {
	ctx := context.Background()
	store := NewFileTokenStore()
	store.SetBaseDir(t.TempDir())

	first := &cliproxyauth.Auth{
		ID:       "claude-existing.json",
		Provider: "claude",
		FileName: "claude-existing.json",
		Metadata: map[string]any{"type": "claude", "email": "user@example.com"},
	}
	if _, err := store.Save(ctx, first); err != nil {
		t.Fatalf("first Save() error: %v", err)
	}

	second := &cliproxyauth.Auth{
		ID:       "claude-existing.json",
		Provider: "claude",
		FileName: "claude-existing.json",
		Metadata: map[string]any{
			"type":     "claude",
			"email":    "user@example.com",
			"disabled": false,
		},
	}
	savedPath, err := store.Save(ctx, second)
	if err != nil {
		t.Fatalf("second Save() error: %v", err)
	}
	if second.Attributes["path"] != savedPath {
		t.Fatalf("path attribute = %q, want %q", second.Attributes["path"], savedPath)
	}
	if second.FileName != "claude-existing.json" {
		t.Fatalf("FileName = %q, want claude-existing.json", second.FileName)
	}
}
