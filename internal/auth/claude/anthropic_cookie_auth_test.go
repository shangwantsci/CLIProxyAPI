package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/imroc/req/v3"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCookieAuthPrefersClaudeCodeCLIOAuth(t *testing.T) {
	var orgCookie string
	var authorizeCookie string
	var authorizeOrg string
	var tokenRedirectURI string
	var tokenPath string
	var orgHeaders http.Header
	var authorizeHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			orgHeaders = r.Header.Clone()
			if cookie, err := r.Cookie("sessionKey"); err == nil {
				orgCookie = cookie.Value
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"uuid":"personal-org","name":"Personal","raven_type":null},
				{"uuid":"team-org","name":"Team","raven_type":"team"}
			]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/oauth/team-org/authorize":
			authorizeHeaders = r.Header.Clone()
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
			_, _ = w.Write([]byte(`{"redirect_uri":"http://localhost:54545/callback?code=auth-code&state=` + state + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/oauth/token":
			tokenPath = r.URL.Path
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
	oldAPITokenURL := claudeAPITokenURL
	oldClaudePlatformTokenURL := claudePlatformTokenURL
	defer func() {
		claudeAIBaseURL = oldClaudeAIBaseURL
		claudeAPITokenURL = oldAPITokenURL
		claudePlatformTokenURL = oldClaudePlatformTokenURL
	}()
	claudeAIBaseURL = server.URL
	claudeAPITokenURL = server.URL + "/api/oauth/token"
	claudePlatformTokenURL = server.URL + "/v1/oauth/token"

	auth := &ClaudeAuth{
		httpClient: server.Client(),
		claudeAIClientFactory: func(proxyURL string) (*req.Client, error) {
			return req.C().SetCookieJar(nil), nil
		},
	}
	bundle, err := auth.CookieAuth(context.Background(), "session-value")
	if err != nil {
		t.Fatalf("CookieAuth returned error: %v", err)
	}
	if orgCookie != "session-value" || authorizeCookie != "session-value" {
		t.Fatalf("cookies = org %q authorize %q, want session-value", orgCookie, authorizeCookie)
	}
	if got := orgHeaders.Get("Referer"); got != "" {
		t.Fatalf("organization Referer = %q, want empty to match sub2api", got)
	}
	if got := authorizeHeaders.Get("Content-Type"); got != "application/json" {
		t.Fatalf("authorize Content-Type = %q, want application/json", got)
	}
	if got := authorizeHeaders.Get("Origin"); got != "https://claude.ai" {
		t.Fatalf("authorize Origin = %q, want https://claude.ai", got)
	}
	if authorizeOrg != "team-org" {
		t.Fatalf("organization_uuid = %q, want team-org", authorizeOrg)
	}
	if tokenPath != "/api/oauth/token" {
		t.Fatalf("token endpoint path = %q, want API OAuth token endpoint", tokenPath)
	}
	if tokenRedirectURI != RedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", tokenRedirectURI, RedirectURI)
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
	if got := bundle.TokenData.AuthSource; got != AuthSourceClaudeCodeCLI {
		t.Fatalf("auth source = %q, want %q", got, AuthSourceClaudeCodeCLI)
	}
	if got := bundle.TokenData.TokenEndpoint; got != claudeAPITokenURL {
		t.Fatalf("token endpoint = %q, want %q", got, claudeAPITokenURL)
	}
	if got := bundle.TokenData.RedirectURI; got != RedirectURI {
		t.Fatalf("redirect uri = %q, want %q", got, RedirectURI)
	}
	if got := bundle.TokenData.AccountUUID; got != "account-uuid" {
		t.Fatalf("account uuid = %q", got)
	}
	if !strings.HasPrefix(bundle.TokenData.Expire, "20") {
		t.Fatalf("expired timestamp = %q, want RFC3339-like future timestamp", bundle.TokenData.Expire)
	}
}

func TestCookieAuthFallsBackToPlatformOAuth(t *testing.T) {
	var authorizeRedirects []string
	var tokenRedirectURI string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"uuid":"team-org","name":"Team","raven_type":"team"}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/oauth/team-org/authorize":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode authorize body: %v", err)
			}
			redirectURI, _ := body["redirect_uri"].(string)
			authorizeRedirects = append(authorizeRedirects, redirectURI)
			if redirectURI == RedirectURI {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"redirect_uri_not_allowed"}`))
				return
			}
			state, _ := body["state"].(string)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"redirect_uri":"https://platform.claude.com/oauth/code/callback?code=platform-code&state=` + state + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/oauth/token":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode token body: %v", err)
			}
			tokenRedirectURI, _ = body["redirect_uri"].(string)
			if body["code"] != "platform-code" {
				t.Fatalf("token code = %v, want platform-code", body["code"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"access_token":"platform-access",
				"refresh_token":"platform-refresh",
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
	oldAPITokenURL := claudeAPITokenURL
	oldClaudePlatformTokenURL := claudePlatformTokenURL
	defer func() {
		claudeAIBaseURL = oldClaudeAIBaseURL
		claudeAPITokenURL = oldAPITokenURL
		claudePlatformTokenURL = oldClaudePlatformTokenURL
	}()
	claudeAIBaseURL = server.URL
	claudeAPITokenURL = server.URL + "/api/oauth/token"
	claudePlatformTokenURL = server.URL + "/v1/oauth/token"

	auth := &ClaudeAuth{
		httpClient: server.Client(),
		claudeAIClientFactory: func(proxyURL string) (*req.Client, error) {
			return req.C().SetCookieJar(nil), nil
		},
	}
	bundle, err := auth.CookieAuth(context.Background(), "session-value")
	if err != nil {
		t.Fatalf("CookieAuth returned error: %v", err)
	}
	if len(authorizeRedirects) != 2 || authorizeRedirects[0] != RedirectURI || authorizeRedirects[1] != PlatformRedirectURI {
		t.Fatalf("authorize redirect sequence = %#v, want CLI then platform", authorizeRedirects)
	}
	if tokenRedirectURI != PlatformRedirectURI {
		t.Fatalf("token redirect_uri = %q, want %q", tokenRedirectURI, PlatformRedirectURI)
	}
	if bundle.TokenData.AuthSource != AuthSourceClaudePlatform {
		t.Fatalf("auth source = %q, want %q", bundle.TokenData.AuthSource, AuthSourceClaudePlatform)
	}
	if bundle.TokenData.TokenEndpoint != claudePlatformTokenURL {
		t.Fatalf("token endpoint = %q, want %q", bundle.TokenData.TokenEndpoint, claudePlatformTokenURL)
	}
}

func TestCookieOrganizationUUIDReportsHTMLResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/organizations" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>login required</body></html>"))
	}))
	defer server.Close()

	oldClaudeAIBaseURL := claudeAIBaseURL
	defer func() {
		claudeAIBaseURL = oldClaudeAIBaseURL
	}()
	claudeAIBaseURL = server.URL

	auth := &ClaudeAuth{httpClient: server.Client()}
	_, err := auth.getCookieOrganizationUUID(context.Background(), "session-value")
	if err == nil {
		t.Fatal("getCookieOrganizationUUID returned nil error for HTML response")
	}
	if !strings.Contains(err.Error(), "organizations response was not JSON") {
		t.Fatalf("error = %q, want explicit non-JSON diagnostic", err)
	}
	if strings.Contains(err.Error(), "session-value") {
		t.Fatalf("error leaked session key: %q", err)
	}
}

func TestCookieOrganizationUUIDFollowsClaudeAIRedirect(t *testing.T) {
	var finalCookie string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/organizations":
			http.Redirect(w, r, "/api/organizations/final", http.StatusFound)
		case "/api/organizations/final":
			if cookie, err := r.Cookie("sessionKey"); err == nil {
				finalCookie = cookie.Value
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"uuid":"redirect-org","name":"Org","raven_type":null}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldClaudeAIBaseURL := claudeAIBaseURL
	defer func() {
		claudeAIBaseURL = oldClaudeAIBaseURL
	}()
	claudeAIBaseURL = server.URL

	auth := NewClaudeAuthWithProxyURL(&config.Config{}, "")
	orgUUID, err := auth.getCookieOrganizationUUID(context.Background(), "session-value")
	if err != nil {
		t.Fatalf("getCookieOrganizationUUID returned error: %v", err)
	}
	if orgUUID != "redirect-org" {
		t.Fatalf("orgUUID = %q, want redirect-org", orgUUID)
	}
	if finalCookie != "session-value" {
		t.Fatalf("redirected cookie = %q, want session-value", finalCookie)
	}
}

func TestCookieAuthInvalidProxyURLFailsClosed(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"uuid":"org","name":"Org","raven_type":null}]`))
	}))
	defer server.Close()

	oldClaudeAIBaseURL := claudeAIBaseURL
	defer func() {
		claudeAIBaseURL = oldClaudeAIBaseURL
	}()
	claudeAIBaseURL = server.URL

	auth := NewClaudeAuthWithProxyURL(&config.Config{}, "ftp://proxy.example.com:21")
	_, err := auth.getCookieOrganizationUUID(context.Background(), "session-value")
	if err == nil {
		t.Fatal("getCookieOrganizationUUID returned nil error for invalid proxy-url")
	}
	if !strings.Contains(err.Error(), "invalid proxy-url") {
		t.Fatalf("error = %q, want invalid proxy-url", err)
	}
	if hits != 0 {
		t.Fatalf("invalid proxy-url should fail closed before upstream request, got %d hits", hits)
	}
}

func TestCookieOrganizationUUIDUsesClaudeAIChromeClientFactory(t *testing.T) {
	var hits int
	var factoryCalls int
	var cookieValue string
	var orgHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		orgHeaders = r.Header.Clone()
		if cookie, err := r.Cookie("sessionKey"); err == nil {
			cookieValue = cookie.Value
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"uuid":"org","name":"Org","raven_type":null}]`))
	}))
	defer server.Close()

	oldClaudeAIBaseURL := claudeAIBaseURL
	defer func() {
		claudeAIBaseURL = oldClaudeAIBaseURL
	}()
	claudeAIBaseURL = server.URL

	auth := &ClaudeAuth{
		httpClient: server.Client(),
		claudeAIClientFactory: func(proxyURL string) (*req.Client, error) {
			factoryCalls++
			return req.C().SetCookieJar(nil), nil
		},
	}
	orgUUID, err := auth.getCookieOrganizationUUID(context.Background(), "session-value")
	if err != nil {
		t.Fatalf("getCookieOrganizationUUID returned error: %v", err)
	}
	if orgUUID != "org" {
		t.Fatalf("orgUUID = %q, want org", orgUUID)
	}
	if factoryCalls != 1 {
		t.Fatalf("factoryCalls = %d, want 1", factoryCalls)
	}
	if hits != 1 {
		t.Fatalf("server hits = %d, want 1", hits)
	}
	if cookieValue != "session-value" {
		t.Fatalf("cookie = %q, want session-value", cookieValue)
	}
	if got := orgHeaders.Get("Referer"); got != "" {
		t.Fatalf("organization Referer = %q, want empty to match sub2api", got)
	}
}
