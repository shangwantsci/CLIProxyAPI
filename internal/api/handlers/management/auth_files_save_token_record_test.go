package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSaveTokenRecordRegistersSavedClaudeAccountInManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = sdkAuth.NewFileTokenStore()

	record := &coreauth.Auth{
		ID:       "claude-user@example.com.json",
		Provider: "claude",
		FileName: "claude-user@example.com.json",
		Metadata: map[string]any{
			"type":          "claude",
			"email":         "user@example.com",
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
		},
		Attributes: map[string]string{
			"cloak_mode":          "auto",
			"cloak_cache_user_id": "true",
		},
	}

	savedPath, err := h.saveTokenRecord(context.Background(), record)
	if err != nil {
		t.Fatalf("saveTokenRecord() error: %v", err)
	}
	if _, err := os.Stat(savedPath); err != nil {
		t.Fatalf("saved auth file missing: %v", err)
	}
	if filepath.Base(savedPath) != record.FileName {
		t.Fatalf("saved file = %q, want %q", filepath.Base(savedPath), record.FileName)
	}
	if _, ok := manager.GetByID(record.ID); !ok {
		t.Fatalf("saved auth %q was not registered in manager", record.ID)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	h.ListAuthFiles(c)
	if w.Code != http.StatusOK {
		t.Fatalf("ListAuthFiles status = %d, body = %s", w.Code, w.Body.String())
	}

	var response struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal ListAuthFiles response: %v", err)
	}
	if len(response.Files) != 1 {
		t.Fatalf("ListAuthFiles returned %d files, want 1 (body=%s)", len(response.Files), w.Body.String())
	}
	if got := response.Files[0]["email"]; got != "user@example.com" {
		t.Fatalf("listed email = %v, want user@example.com", got)
	}
}
