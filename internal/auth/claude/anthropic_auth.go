// Package claude provides OAuth2 authentication functionality for Anthropic's Claude API.
// This package implements the complete OAuth2 flow with PKCE (Proof Key for Code Exchange)
// for secure authentication with Claude API, including token exchange, refresh, and storage.
package claude

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/imroc/req/v3"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

// OAuth configuration constants for Claude/Anthropic
const (
	AuthURL     = "https://claude.ai/oauth/authorize"
	TokenURL    = "https://api.anthropic.com/v1/oauth/token"
	ClientID    = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	RedirectURI = "http://localhost:54545/callback"

	PlatformRedirectURI = "https://platform.claude.com/oauth/code/callback"

	claudeRefreshMinBackoff = 5 * time.Second
	claudeRefreshMaxBackoff = 5 * time.Minute
	claudeCookieScopeAPI    = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	claudeAIBrowserUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var (
	claudeRefreshGroup singleflight.Group
	claudeRefreshMu    sync.Mutex
	claudeRefreshBlock = make(map[string]time.Time)

	claudeAIBaseURL          = "https://claude.ai"
	claudePlatformTokenURL   = "https://platform.claude.com/v1/oauth/token"
	claudePlatformHTTPOrigin = "https://claude.ai"
)

type refreshHTTPError struct {
	status    int
	message   string
	retryable bool
}

func (e *refreshHTTPError) Error() string {
	return fmt.Sprintf("token refresh failed with status %d: %s", e.status, e.message)
}

func (e *refreshHTTPError) Retryable() bool {
	return e != nil && e.retryable
}

func (e *refreshHTTPError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

func resetClaudeRefreshState() {
	claudeRefreshMu.Lock()
	defer claudeRefreshMu.Unlock()
	claudeRefreshBlock = make(map[string]time.Time)
	claudeRefreshGroup = singleflight.Group{}
}

func claudeRefreshBlockedUntil(refreshToken string) time.Time {
	claudeRefreshMu.Lock()
	defer claudeRefreshMu.Unlock()
	return claudeRefreshBlock[refreshToken]
}

func setClaudeRefreshBlockedUntil(refreshToken string, until time.Time) {
	claudeRefreshMu.Lock()
	defer claudeRefreshMu.Unlock()
	claudeRefreshBlock[refreshToken] = until
}

func clearClaudeRefreshBlockedUntil(refreshToken string) {
	claudeRefreshMu.Lock()
	defer claudeRefreshMu.Unlock()
	delete(claudeRefreshBlock, refreshToken)
}

func clampClaudeRefreshBackoff(d time.Duration) time.Duration {
	if d < claudeRefreshMinBackoff {
		return claudeRefreshMinBackoff
	}
	if d > claudeRefreshMaxBackoff {
		return claudeRefreshMaxBackoff
	}
	return d
}

func parseClaudeRetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return claudeRefreshMinBackoff
	}
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
		if seconds, err := time.ParseDuration(raw + "s"); err == nil {
			return clampClaudeRefreshBackoff(seconds)
		}
		if when, err := http.ParseTime(raw); err == nil {
			return clampClaudeRefreshBackoff(time.Until(when))
		}
	}
	if raw := strings.TrimSpace(resp.Header.Get("Retry-After-Ms")); raw != "" {
		if ms, err := time.ParseDuration(raw + "ms"); err == nil {
			return clampClaudeRefreshBackoff(ms)
		}
	}
	return claudeRefreshMinBackoff
}

func isClaudeRefreshRetryable(err error) bool {
	var httpErr *refreshHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Retryable()
	}
	return true
}

// tokenResponse represents the response structure from Anthropic's OAuth token endpoint.
// It contains access token, refresh token, and associated user/organization information.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Organization struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"organization"`
	Account struct {
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
	} `json:"account"`
}

type claudeOrganization struct {
	UUID      string  `json:"uuid"`
	Name      string  `json:"name"`
	RavenType *string `json:"raven_type"`
}

type cookieAuthorizeResponse struct {
	RedirectURI string `json:"redirect_uri"`
}

func tokenDataFromResponse(tokenResp tokenResponse) ClaudeTokenData {
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	return ClaudeTokenData{
		AccessToken:      tokenResp.AccessToken,
		RefreshToken:     tokenResp.RefreshToken,
		TokenType:        tokenResp.TokenType,
		ExpiresIn:        tokenResp.ExpiresIn,
		Email:            tokenResp.Account.EmailAddress,
		OrganizationUUID: tokenResp.Organization.UUID,
		AccountUUID:      tokenResp.Account.UUID,
		Scope:            tokenResp.Scope,
		Expire:           expiresAt.Format(time.RFC3339),
	}
}

// ClaudeAuth handles Anthropic OAuth2 authentication flow.
// It provides methods for generating authorization URLs, exchanging codes for tokens,
// and refreshing expired tokens using PKCE for enhanced security.
type ClaudeAuth struct {
	httpClient            *http.Client
	proxyURL              string
	claudeAIClientFactory func(proxyURL string) (*req.Client, error)
}

// NewClaudeAuth creates a new Anthropic authentication service.
// It initializes the HTTP client with a custom TLS transport that uses Firefox
// fingerprint to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
//
// Parameters:
//   - cfg: The application configuration containing proxy settings
//
// Returns:
//   - *ClaudeAuth: A new Claude authentication service instance
func NewClaudeAuth(cfg *config.Config) *ClaudeAuth {
	return NewClaudeAuthWithProxyURL(cfg, "")
}

// NewClaudeAuthWithProxyURL creates a new Anthropic authentication service with a proxy override.
// proxyURL takes precedence over cfg.ProxyURL when non-empty.
func NewClaudeAuthWithProxyURL(cfg *config.Config, proxyURL string) *ClaudeAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg *config.SDKConfig
	if cfg != nil {
		sdkCfgCopy := cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
		sdkCfgCopy.ProxyURL = effectiveProxyURL
		sdkCfg = &sdkCfgCopy
	} else if effectiveProxyURL != "" {
		sdkCfgCopy := config.SDKConfig{ProxyURL: effectiveProxyURL}
		sdkCfg = &sdkCfgCopy
	}

	// Use custom HTTP client with Chrome TLS fingerprint for Anthropic domains.
	return &ClaudeAuth{
		httpClient:            NewAnthropicHttpClient(sdkCfg),
		proxyURL:              effectiveProxyURL,
		claudeAIClientFactory: newClaudeAIChromeClient,
	}
}

// GenerateAuthURL creates the OAuth authorization URL with PKCE.
// This method generates a secure authorization URL including PKCE challenge codes
// for the OAuth2 flow with Anthropic's API.
//
// Parameters:
//   - state: A random state parameter for CSRF protection
//   - pkceCodes: The PKCE codes for secure code exchange
//
// Returns:
//   - string: The complete authorization URL
//   - string: The state parameter for verification
//   - error: An error if PKCE codes are missing or URL generation fails
func (o *ClaudeAuth) GenerateAuthURL(state string, pkceCodes *PKCECodes) (string, string, error) {
	if pkceCodes == nil {
		return "", "", fmt.Errorf("PKCE codes are required")
	}

	params := url.Values{
		"code":                  {"true"},
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {RedirectURI},
		"scope":                 {"user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"},
		"code_challenge":        {pkceCodes.CodeChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}

	authURL := fmt.Sprintf("%s?%s", AuthURL, params.Encode())
	return authURL, state, nil
}

// parseCodeAndState extracts the authorization code and state from the callback response.
// It handles the parsing of the code parameter which may contain additional fragments.
//
// Parameters:
//   - code: The raw code parameter from the OAuth callback
//
// Returns:
//   - parsedCode: The extracted authorization code
//   - parsedState: The extracted state parameter if present
func (c *ClaudeAuth) parseCodeAndState(code string) (parsedCode, parsedState string) {
	splits := strings.Split(code, "#")
	parsedCode = splits[0]
	if len(splits) > 1 {
		parsedState = splits[1]
	}
	return
}

// ExchangeCodeForTokens exchanges authorization code for access tokens.
// This method implements the OAuth2 token exchange flow using PKCE for security.
// It sends the authorization code along with PKCE verifier to get access and refresh tokens.
//
// Parameters:
//   - ctx: The context for the request
//   - code: The authorization code received from OAuth callback
//   - state: The state parameter for verification
//   - pkceCodes: The PKCE codes for secure verification
//
// Returns:
//   - *ClaudeAuthBundle: The complete authentication bundle with tokens
//   - error: An error if token exchange fails
func (o *ClaudeAuth) ExchangeCodeForTokens(ctx context.Context, code, state string, pkceCodes *PKCECodes) (*ClaudeAuthBundle, error) {
	if pkceCodes == nil {
		return nil, fmt.Errorf("PKCE codes are required for token exchange")
	}
	newCode, newState := o.parseCodeAndState(code)

	// Prepare token exchange request
	reqBody := map[string]interface{}{
		"code":          newCode,
		"state":         state,
		"grant_type":    "authorization_code",
		"client_id":     ClientID,
		"redirect_uri":  RedirectURI,
		"code_verifier": pkceCodes.CodeVerifier,
	}

	// Include state if present
	if newState != "" {
		reqBody["state"] = newState
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// log.Debugf("Token exchange request: %s", string(jsonBody))

	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}
	// log.Debugf("Token response: %s", string(body))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}
	// log.Debugf("Token response: %s", string(body))

	var tokenResp tokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	tokenData := tokenDataFromResponse(tokenResp)

	// Create auth bundle
	bundle := &ClaudeAuthBundle{
		TokenData:   tokenData,
		LastRefresh: time.Now().Format(time.RFC3339),
	}

	return bundle, nil
}

// RefreshTokens refreshes the access token using the refresh token.
// This method exchanges a valid refresh token for a new access token,
// extending the user's authenticated session.
//
// Parameters:
//   - ctx: The context for the request
//   - refreshToken: The refresh token to use for getting new access token
//
// Returns:
//   - *ClaudeTokenData: The new token data with updated access token
//   - error: An error if token refresh fails
func (o *ClaudeAuth) RefreshTokens(ctx context.Context, refreshToken string) (*ClaudeTokenData, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is required")
	}
	if blockedUntil := claudeRefreshBlockedUntil(refreshToken); blockedUntil.After(time.Now()) {
		return nil, &refreshHTTPError{
			status:    http.StatusTooManyRequests,
			message:   fmt.Sprintf("refresh temporarily blocked until %s", blockedUntil.Format(time.RFC3339)),
			retryable: false,
		}
	}

	result, err, _ := claudeRefreshGroup.Do(refreshToken, func() (interface{}, error) {
		return o.refreshTokensSingleFlight(context.WithoutCancel(ctx), refreshToken)
	})
	if err != nil {
		return nil, err
	}
	tokenData, ok := result.(*ClaudeTokenData)
	if !ok || tokenData == nil {
		return nil, fmt.Errorf("token refresh failed: invalid single-flight result")
	}
	return tokenData, nil
}

func (o *ClaudeAuth) refreshTokensSingleFlight(ctx context.Context, refreshToken string) (*ClaudeTokenData, error) {
	if blockedUntil := claudeRefreshBlockedUntil(refreshToken); blockedUntil.After(time.Now()) {
		return nil, &refreshHTTPError{
			status:    http.StatusTooManyRequests,
			message:   fmt.Sprintf("refresh temporarily blocked until %s", blockedUntil.Format(time.RFC3339)),
			retryable: false,
		}
	}

	reqBody := map[string]interface{}{
		"client_id":     ClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		message := string(body)
		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := parseClaudeRetryAfter(resp)
			setClaudeRefreshBlockedUntil(refreshToken, time.Now().Add(retryAfter))
			return nil, &refreshHTTPError{status: resp.StatusCode, message: message, retryable: false}
		}
		return nil, &refreshHTTPError{
			status:    resp.StatusCode,
			message:   message,
			retryable: resp.StatusCode >= http.StatusInternalServerError,
		}
	}

	// log.Debugf("Token response: %s", string(body))

	var tokenResp tokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	clearClaudeRefreshBlockedUntil(refreshToken)

	tokenData := tokenDataFromResponse(tokenResp)
	return &tokenData, nil
}

func generateClaudeOAuthState() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func applyClaudeAIBrowserHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Referer", claudePlatformHTTPOrigin+"/new")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", claudeAIBrowserUA)
}

func decodeClaudeAIJSONResponse(name string, resp *http.Response, body []byte, target any) error {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	trimmedBody := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmedBody, "<") || (contentType != "" && !strings.Contains(contentType, "json")) {
		return fmt.Errorf("%s response was not JSON: status %d, content-type %q, body prefix %q", name, resp.StatusCode, resp.Header.Get("Content-Type"), truncateClaudeAIResponseForError(trimmedBody, 180))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("failed to parse %s response: %w", name, err)
	}
	return nil
}

func truncateClaudeAIResponseForError(body string, limit int) string {
	body = strings.ReplaceAll(body, "\r", " ")
	body = strings.ReplaceAll(body, "\n", " ")
	body = strings.Join(strings.Fields(body), " ")
	if len(body) <= limit {
		return body
	}
	return body[:limit] + "..."
}

func newClaudeAIChromeClient(proxyURL string) (*req.Client, error) {
	client := req.C().
		SetTimeout(60 * time.Second).
		ImpersonateChrome().
		SetCookieJar(nil)
	client.GetClient().CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	setting, err := proxyutil.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy-url %s: %w", proxyutil.Redact(proxyURL), err)
	}
	switch setting.Mode {
	case proxyutil.ModeDirect:
		client.SetProxy(nil)
	case proxyutil.ModeProxy:
		client.SetProxyURL(setting.URL.String())
	}
	return client, nil
}

func (o *ClaudeAuth) newCookieOAuthClient() (*req.Client, error) {
	if o.claudeAIClientFactory == nil {
		return nil, nil
	}
	client, err := o.claudeAIClientFactory(o.proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create claude.ai chrome client: %w", err)
	}
	return client, nil
}

func selectCookieOrganizationUUID(orgs []claudeOrganization) (string, error) {
	if len(orgs) == 0 {
		return "", fmt.Errorf("no organizations found")
	}
	for _, org := range orgs {
		if org.RavenType != nil && *org.RavenType == "team" && strings.TrimSpace(org.UUID) != "" {
			return org.UUID, nil
		}
	}
	if strings.TrimSpace(orgs[0].UUID) == "" {
		return "", fmt.Errorf("organization uuid is empty")
	}
	return orgs[0].UUID, nil
}

func parseCookieAuthorizationCode(redirectURI, state string) (string, error) {
	if strings.TrimSpace(redirectURI) == "" {
		return "", fmt.Errorf("no redirect_uri in authorize response")
	}
	parsedURL, err := url.Parse(redirectURI)
	if err != nil {
		return "", fmt.Errorf("failed to parse redirect_uri: %w", err)
	}
	query := parsedURL.Query()
	authCode := strings.TrimSpace(query.Get("code"))
	responseState := strings.TrimSpace(query.Get("state"))
	if authCode == "" {
		return "", fmt.Errorf("no authorization code in redirect_uri")
	}
	if responseState != "" && responseState != state {
		return "", fmt.Errorf("state mismatch in redirect_uri")
	}
	if responseState != "" {
		return authCode + "#" + responseState, nil
	}
	return authCode, nil
}

// CookieAuth completes Claude OAuth using a claude.ai sessionKey cookie.
func (o *ClaudeAuth) CookieAuth(ctx context.Context, sessionKey string) (*ClaudeAuthBundle, error) {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil, fmt.Errorf("sessionKey is required")
	}
	pkceCodes, err := GeneratePKCECodes()
	if err != nil {
		return nil, err
	}
	state, err := generateClaudeOAuthState()
	if err != nil {
		return nil, err
	}

	orgUUID, err := o.getCookieOrganizationUUID(ctx, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization info: %w", err)
	}
	code, err := o.getCookieAuthorizationCode(ctx, sessionKey, orgUUID, claudeCookieScopeAPI, pkceCodes.CodeChallenge, state)
	if err != nil {
		return nil, fmt.Errorf("failed to get authorization code: %w", err)
	}
	bundle, err := o.exchangePlatformCodeForTokens(ctx, code, state, pkceCodes)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}
	if bundle.TokenData.OrganizationUUID == "" {
		bundle.TokenData.OrganizationUUID = orgUUID
	}
	return bundle, nil
}

func (o *ClaudeAuth) getCookieOrganizationUUID(ctx context.Context, sessionKey string) (string, error) {
	if client, err := o.newCookieOAuthClient(); err != nil {
		return "", err
	} else if client != nil {
		var orgs []claudeOrganization
		resp, err := client.R().
			SetContext(ctx).
			SetCookies(&http.Cookie{Name: "sessionKey", Value: sessionKey}).
			SetHeader("Accept", "application/json, text/plain, */*").
			SetHeader("Accept-Language", "en-US,en;q=0.9").
			SetHeader("Cache-Control", "no-cache").
			SetHeader("Pragma", "no-cache").
			SetHeader("Referer", claudePlatformHTTPOrigin+"/new").
			SetHeader("Sec-Fetch-Dest", "empty").
			SetHeader("Sec-Fetch-Mode", "cors").
			SetHeader("Sec-Fetch-Site", "same-origin").
			SetHeader("User-Agent", claudeAIBrowserUA).
			Get(strings.TrimRight(claudeAIBaseURL, "/") + "/api/organizations")
		if err != nil {
			return "", fmt.Errorf("organizations request failed: %w", err)
		}
		if !resp.IsSuccessState() {
			return "", fmt.Errorf("failed to get organizations: status %d: %s", resp.StatusCode, resp.String())
		}
		if err = decodeClaudeAIJSONResponse("organizations", resp.Response, resp.Bytes(), &orgs); err != nil {
			return "", err
		}
		return selectCookieOrganizationUUID(orgs)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(claudeAIBaseURL, "/")+"/api/organizations", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create organizations request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "sessionKey", Value: sessionKey})
	applyClaudeAIBrowserHeaders(req)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("organizations request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read organizations response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to get organizations: status %d: %s", resp.StatusCode, string(body))
	}

	var orgs []claudeOrganization
	if err = decodeClaudeAIJSONResponse("organizations", resp, body, &orgs); err != nil {
		return "", err
	}
	return selectCookieOrganizationUUID(orgs)
}

func (o *ClaudeAuth) getCookieAuthorizationCode(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state string) (string, error) {
	authURL := fmt.Sprintf("%s/v1/oauth/%s/authorize", strings.TrimRight(claudeAIBaseURL, "/"), url.PathEscape(orgUUID))
	reqBody := map[string]any{
		"response_type":         "code",
		"client_id":             ClientID,
		"organization_uuid":     orgUUID,
		"redirect_uri":          PlatformRedirectURI,
		"scope":                 scope,
		"state":                 state,
		"code_challenge":        codeChallenge,
		"code_challenge_method": "S256",
	}
	if client, err := o.newCookieOAuthClient(); err != nil {
		return "", err
	} else if client != nil {
		resp, err := client.R().
			SetContext(ctx).
			SetCookies(&http.Cookie{Name: "sessionKey", Value: sessionKey}).
			SetHeader("Accept", "application/json").
			SetHeader("Accept-Language", "en-US,en;q=0.9").
			SetHeader("Cache-Control", "no-cache").
			SetHeader("Pragma", "no-cache").
			SetHeader("Origin", claudePlatformHTTPOrigin).
			SetHeader("Referer", claudePlatformHTTPOrigin+"/new").
			SetHeader("Sec-Fetch-Dest", "empty").
			SetHeader("Sec-Fetch-Mode", "cors").
			SetHeader("Sec-Fetch-Site", "same-origin").
			SetHeader("Content-Type", "application/json").
			SetHeader("User-Agent", claudeAIBrowserUA).
			SetBody(reqBody).
			Post(authURL)
		if err != nil {
			return "", fmt.Errorf("authorize request failed: %w", err)
		}
		if !resp.IsSuccessState() {
			return "", fmt.Errorf("authorization failed: status %d: %s", resp.StatusCode, resp.String())
		}
		var result cookieAuthorizeResponse
		if err = decodeClaudeAIJSONResponse("authorize", resp.Response, resp.Bytes(), &result); err != nil {
			return "", err
		}
		return parseCookieAuthorizationCode(result.RedirectURI, state)
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal authorize request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", fmt.Errorf("failed to create authorize request: %w", err)
	}
	req.AddCookie(&http.Cookie{Name: "sessionKey", Value: sessionKey})
	applyClaudeAIBrowserHeaders(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", claudePlatformHTTPOrigin)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("authorize request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read authorize response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("authorization failed: status %d: %s", resp.StatusCode, string(body))
	}

	var result cookieAuthorizeResponse
	if err = decodeClaudeAIJSONResponse("authorize", resp, body, &result); err != nil {
		return "", err
	}
	return parseCookieAuthorizationCode(result.RedirectURI, state)
}

func (o *ClaudeAuth) exchangePlatformCodeForTokens(ctx context.Context, code, state string, pkceCodes *PKCECodes) (*ClaudeAuthBundle, error) {
	if pkceCodes == nil {
		return nil, fmt.Errorf("PKCE codes are required for token exchange")
	}
	newCode, newState := o.parseCodeAndState(code)
	reqBody := map[string]any{
		"code":          newCode,
		"grant_type":    "authorization_code",
		"client_id":     ClientID,
		"redirect_uri":  PlatformRedirectURI,
		"code_verifier": pkceCodes.CodeVerifier,
	}
	if newState != "" {
		reqBody["state"] = newState
	} else if strings.TrimSpace(state) != "" {
		reqBody["state"] = state
	}

	if client, err := o.newCookieOAuthClient(); err != nil {
		return nil, err
	} else if client != nil {
		resp, err := client.R().
			SetContext(ctx).
			SetHeader("Accept", "application/json, text/plain, */*").
			SetHeader("Content-Type", "application/json").
			SetHeader("User-Agent", "axios/1.13.6").
			SetBody(reqBody).
			Post(claudePlatformTokenURL)
		if err != nil {
			return nil, fmt.Errorf("token exchange request failed: %w", err)
		}
		if !resp.IsSuccessState() {
			return nil, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, resp.String())
		}
		var tokenResp tokenResponse
		if err = decodeClaudeAIJSONResponse("token exchange", resp.Response, resp.Bytes(), &tokenResp); err != nil {
			return nil, err
		}
		return &ClaudeAuthBundle{
			TokenData:   tokenDataFromResponse(tokenResp),
			LastRefresh: time.Now().Format(time.RFC3339),
		}, nil
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal token request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudePlatformTokenURL, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "axios/1.13.6")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &ClaudeAuthBundle{
		TokenData:   tokenDataFromResponse(tokenResp),
		LastRefresh: time.Now().Format(time.RFC3339),
	}, nil
}

// CreateTokenStorage creates a new ClaudeTokenStorage from auth bundle and user info.
// This method converts the authentication bundle into a token storage structure
// suitable for persistence and later use.
//
// Parameters:
//   - bundle: The authentication bundle containing token data
//
// Returns:
//   - *ClaudeTokenStorage: A new token storage instance
func (o *ClaudeAuth) CreateTokenStorage(bundle *ClaudeAuthBundle) *ClaudeTokenStorage {
	storage := &ClaudeTokenStorage{
		AccessToken:      bundle.TokenData.AccessToken,
		RefreshToken:     bundle.TokenData.RefreshToken,
		TokenType:        bundle.TokenData.TokenType,
		ExpiresIn:        bundle.TokenData.ExpiresIn,
		LastRefresh:      bundle.LastRefresh,
		Email:            bundle.TokenData.Email,
		OrganizationUUID: bundle.TokenData.OrganizationUUID,
		AccountUUID:      bundle.TokenData.AccountUUID,
		Scope:            bundle.TokenData.Scope,
		Expire:           bundle.TokenData.Expire,
	}

	return storage
}

// RefreshTokensWithRetry refreshes tokens with automatic retry logic.
// This method implements exponential backoff retry logic for token refresh operations,
// providing resilience against temporary network or service issues.
//
// Parameters:
//   - ctx: The context for the request
//   - refreshToken: The refresh token to use
//   - maxRetries: The maximum number of retry attempts
//
// Returns:
//   - *ClaudeTokenData: The refreshed token data
//   - error: An error if all retry attempts fail
func (o *ClaudeAuth) RefreshTokensWithRetry(ctx context.Context, refreshToken string, maxRetries int) (*ClaudeTokenData, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		tokenData, err := o.RefreshTokens(ctx, refreshToken)
		if err == nil {
			return tokenData, nil
		}

		lastErr = err
		log.Warnf("Token refresh attempt %d failed: %v", attempt+1, err)
		if !isClaudeRefreshRetryable(err) {
			break
		}
	}

	return nil, fmt.Errorf("token refresh failed after %d attempts: %w", maxRetries, lastErr)
}

// UpdateTokenStorage updates an existing token storage with new token data.
// This method refreshes the token storage with newly obtained access and refresh tokens,
// updating timestamps and expiration information.
//
// Parameters:
//   - storage: The existing token storage to update
//   - tokenData: The new token data to apply
func (o *ClaudeAuth) UpdateTokenStorage(storage *ClaudeTokenStorage, tokenData *ClaudeTokenData) {
	storage.AccessToken = tokenData.AccessToken
	storage.RefreshToken = tokenData.RefreshToken
	if tokenData.TokenType != "" {
		storage.TokenType = tokenData.TokenType
	}
	if tokenData.ExpiresIn > 0 {
		storage.ExpiresIn = tokenData.ExpiresIn
	}
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	if tokenData.Email != "" {
		storage.Email = tokenData.Email
	}
	if tokenData.OrganizationUUID != "" {
		storage.OrganizationUUID = tokenData.OrganizationUUID
	}
	if tokenData.AccountUUID != "" {
		storage.AccountUUID = tokenData.AccountUUID
	}
	if tokenData.Scope != "" {
		storage.Scope = tokenData.Scope
	}
	storage.Expire = tokenData.Expire
}
