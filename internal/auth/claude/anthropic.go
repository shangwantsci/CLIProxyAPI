package claude

const (
	AuthSourceClaudeCodeCLI    = "claude_code_cli"
	AuthSourceClaudePlatform   = "claude_platform"
	AuthSourceClaudeSetupToken = "claude_setup_token"
)

// PKCECodes holds PKCE verification codes for OAuth2 PKCE flow
type PKCECodes struct {
	// CodeVerifier is the cryptographically random string used to correlate
	// the authorization request to the token request
	CodeVerifier string `json:"code_verifier"`
	// CodeChallenge is the SHA256 hash of the code verifier, base64url-encoded
	CodeChallenge string `json:"code_challenge"`
}

// ClaudeTokenData holds OAuth token information from Anthropic
type ClaudeTokenData struct {
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refresh_token"`
	// TokenType is the OAuth token type returned by Claude.
	TokenType string `json:"token_type,omitempty"`
	// ExpiresIn is the token lifetime returned by Claude, in seconds.
	ExpiresIn int `json:"expires_in,omitempty"`
	// Email is the Anthropic account email
	Email string `json:"email"`
	// OrganizationUUID is the Claude organization selected during OAuth.
	OrganizationUUID string `json:"organization_uuid,omitempty"`
	// OrganizationName is the selected Claude organization display name.
	OrganizationName string `json:"organization_name,omitempty"`
	// AccountUUID is the Claude account UUID returned during OAuth.
	AccountUUID string `json:"account_uuid,omitempty"`
	// PlanType records the selected Claude subscription plan, when known.
	PlanType string `json:"plan_type,omitempty"`
	// SubscriptionTier records the upstream subscription tier, when known.
	SubscriptionTier string `json:"subscription_tier,omitempty"`
	// SubscriptionStatus records active/canceled state from profile, when known.
	SubscriptionStatus string `json:"subscription_status,omitempty"`
	// Scope is the granted OAuth scope.
	Scope string `json:"scope,omitempty"`
	// Expire is the timestamp of the token expire
	Expire string `json:"expired"`
	// AuthSource records which Claude OAuth flow produced the token.
	AuthSource string `json:"auth_source,omitempty"`
	// TokenEndpoint records which endpoint should refresh this token.
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	// RedirectURI records the OAuth redirect URI used to produce the token.
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// ClaudeAuthBundle aggregates authentication data after OAuth flow completion
type ClaudeAuthBundle struct {
	// APIKey is the Anthropic API key obtained from token exchange
	APIKey string `json:"api_key"`
	// TokenData contains the OAuth tokens from the authentication flow
	TokenData ClaudeTokenData `json:"token_data"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
}
