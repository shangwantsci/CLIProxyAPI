package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPostOAuthCallbackReturnsStateForStatusPolling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldSessions := oauthSessions
	oauthSessions = newOAuthSessionStore(time.Minute)
	defer func() { oauthSessions = oldSessions }()

	state := "state-test"
	RegisterOAuthSession(state, "anthropic")
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)

	body := []byte(`{"provider":"anthropic","redirect_url":"http://127.0.0.1:54545/callback?code=abc&state=state-test"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/oauth-callback", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PostOAuthCallback(c)
	if w.Code != http.StatusOK {
		t.Fatalf("PostOAuthCallback status = %d, body = %s", w.Code, w.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response["status"] != "ok" {
		t.Fatalf("status = %q, want ok", response["status"])
	}
	if response["state"] != state {
		t.Fatalf("state = %q, want %q", response["state"], state)
	}
	if response["provider"] != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", response["provider"])
	}
}
