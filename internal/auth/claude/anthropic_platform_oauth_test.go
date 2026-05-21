package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGeneratePlatformAuthURLUsesClaudePlatformCallback(t *testing.T) {
	pkce := &PKCECodes{CodeChallenge: "challenge"}
	auth := NewClaudeAuth(nil)

	authURL, state, err := auth.GeneratePlatformAuthURL("state-value", pkce)
	if err != nil {
		t.Fatalf("GeneratePlatformAuthURL() error: %v", err)
	}
	if state != "state-value" {
		t.Fatalf("state = %q, want state-value", state)
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	query := parsed.Query()
	if got := query.Get("redirect_uri"); got != PlatformRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, PlatformRedirectURI)
	}
	if got := query.Get("scope"); got != claudeOAuthScopeBrowser {
		t.Fatalf("scope = %q, want %q", got, claudeOAuthScopeBrowser)
	}
	if got := query.Get("client_id"); got != ClientID {
		t.Fatalf("client_id = %q, want %q", got, ClientID)
	}
}

func TestGenerateAuthURLKeepsOriginalCPALocalCallback(t *testing.T) {
	pkce := &PKCECodes{CodeChallenge: "challenge"}
	auth := NewClaudeAuth(nil)

	authURL, _, err := auth.GenerateAuthURL("state-value", pkce)
	if err != nil {
		t.Fatalf("GenerateAuthURL() error: %v", err)
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != RedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, RedirectURI)
	}
}

func TestExchangePlatformCodeForTokensUsesSub2APIEndpointAndRedirect(t *testing.T) {
	var capturedBody map[string]any
	var acceptHeader string
	var userAgent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		acceptHeader = r.Header.Get("Accept")
		userAgent = r.Header.Get("User-Agent")
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"access_token":"sk-ant-oat-access",
			"refresh_token":"refresh-token",
			"token_type":"Bearer",
			"expires_in":3600,
			"scope":"user:inference",
			"organization":{"uuid":"org-uuid","name":"Org"},
			"account":{"uuid":"account-uuid","email_address":"user@example.com"}
		}`))
	}))
	defer server.Close()

	oldTokenURL := claudePlatformTokenURL
	defer func() {
		claudePlatformTokenURL = oldTokenURL
	}()
	claudePlatformTokenURL = server.URL

	pkce := &PKCECodes{CodeVerifier: "verifier"}
	auth := NewClaudeAuth(nil)
	bundle, err := auth.ExchangePlatformCodeForTokens(context.Background(), "auth-code#state-from-code", "fallback-state", pkce)
	if err != nil {
		t.Fatalf("ExchangePlatformCodeForTokens() error: %v", err)
	}

	if got := capturedBody["code"]; got != "auth-code" {
		t.Fatalf("code = %v, want auth-code", got)
	}
	if got := capturedBody["state"]; got != "state-from-code" {
		t.Fatalf("state = %v, want state-from-code", got)
	}
	if got := capturedBody["redirect_uri"]; got != PlatformRedirectURI {
		t.Fatalf("redirect_uri = %v, want %s", got, PlatformRedirectURI)
	}
	if got := capturedBody["code_verifier"]; got != "verifier" {
		t.Fatalf("code_verifier = %v, want verifier", got)
	}
	if acceptHeader != "application/json, text/plain, */*" {
		t.Fatalf("Accept = %q, want application/json, text/plain, */*", acceptHeader)
	}
	if userAgent != "axios/1.13.6" {
		t.Fatalf("User-Agent = %q, want axios/1.13.6", userAgent)
	}
	if bundle.TokenData.RefreshToken != "refresh-token" {
		t.Fatalf("refresh token = %q", bundle.TokenData.RefreshToken)
	}
}
