package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestRequestAnthropicTokenWebUIUsesPlatformOAuthURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir(), Port: 8317}, nil)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/anthropic-auth-url?is_webui=true", nil)

	h.RequestAnthropicToken(c)
	if w.Code != http.StatusOK {
		t.Fatalf("RequestAnthropicToken status = %d, body = %s", w.Code, w.Body.String())
	}

	var response struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	defer CompleteOAuthSession(response.State)

	parsed, err := url.Parse(response.URL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	query := parsed.Query()
	if got := query.Get("redirect_uri"); got != claude.PlatformRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, claude.PlatformRedirectURI)
	}
	if scope := query.Get("scope"); !strings.Contains(scope, "org:create_api_key") {
		t.Fatalf("scope = %q, want org:create_api_key for platform OAuth", scope)
	}
}
