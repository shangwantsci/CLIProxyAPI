package claude

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
	// AccountUUID is the Claude account UUID returned during OAuth.
	AccountUUID string `json:"account_uuid,omitempty"`
	// Scope is the granted OAuth scope.
	Scope string `json:"scope,omitempty"`
	// Expire is the timestamp of the token expire
	Expire string `json:"expired"`
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
