package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCookieAuthExchangesSessionKeyForToken(t *testing.T) {
	var orgCookie string
	var authorizeCookie string
	var authorizeOrg string
	var tokenRedirectURI string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			if cookie, err := r.Cookie("sessionKey"); err == nil {
				orgCookie = cookie.Value
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"uuid":"personal-org","name":"Personal","raven_type":null},
				{"uuid":"team-org","name":"Team","raven_type":"team"}
			]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/oauth/team-org/authorize":
			if cookie, err := r.Cookie("sessionKey"); err == nil {
				authorizeCookie = cookie.Value
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode authorize body: %v", err)
			}
			authorizeOrg, _ = body["organization_uuid"].(string)
			state, _ := body["state"].(string)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"redirect_uri":"https://platform.claude.com/oauth/code/callback?code=auth-code&state=` + state + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/oauth/token":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode token body: %v", err)
			}
			tokenRedirectURI, _ = body["redirect_uri"].(string)
			if body["code"] != "auth-code" {
				t.Fatalf("token code = %v, want auth-code", body["code"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"access_token":"sk-ant-oat-access",
				"refresh_token":"refresh-token",
				"token_type":"Bearer",
				"expires_in":3600,
				"organization":{"uuid":"team-org","name":"Team"},
				"account":{"uuid":"account-uuid","email_address":"user@example.com"}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldClaudeAIBaseURL := claudeAIBaseURL
	oldClaudePlatformTokenURL := claudePlatformTokenURL
	defer func() {
		claudeAIBaseURL = oldClaudeAIBaseURL
		claudePlatformTokenURL = oldClaudePlatformTokenURL
	}()
	claudeAIBaseURL = server.URL
	claudePlatformTokenURL = server.URL + "/v1/oauth/token"

	auth := &ClaudeAuth{httpClient: server.Client()}
	bundle, err := auth.CookieAuth(context.Background(), "session-value")
	if err != nil {
		t.Fatalf("CookieAuth returned error: %v", err)
	}
	if orgCookie != "session-value" || authorizeCookie != "session-value" {
		t.Fatalf("cookies = org %q authorize %q, want session-value", orgCookie, authorizeCookie)
	}
	if authorizeOrg != "team-org" {
		t.Fatalf("organization_uuid = %q, want team-org", authorizeOrg)
	}
	if tokenRedirectURI != PlatformRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", tokenRedirectURI, PlatformRedirectURI)
	}
	if got := bundle.TokenData.AccessToken; got != "sk-ant-oat-access" {
		t.Fatalf("access_token = %q", got)
	}
	if got := bundle.TokenData.RefreshToken; got != "refresh-token" {
		t.Fatalf("refresh_token = %q", got)
	}
	if got := bundle.TokenData.Email; got != "user@example.com" {
		t.Fatalf("email = %q", got)
	}
	if got := bundle.TokenData.OrganizationUUID; got != "team-org" {
		t.Fatalf("organization uuid = %q", got)
	}
	if got := bundle.TokenData.AccountUUID; got != "account-uuid" {
		t.Fatalf("account uuid = %q", got)
	}
	if !strings.HasPrefix(bundle.TokenData.Expire, "20") {
		t.Fatalf("expired timestamp = %q, want RFC3339-like future timestamp", bundle.TokenData.Expire)
	}
}
