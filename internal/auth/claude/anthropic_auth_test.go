package claude

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRefreshTokensWithRetry_429BlocksImmediateReplay(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	var calls int32
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`{"error":"rate_limited"}`)),
					Header:     http.Header{"Retry-After": []string{"60"}},
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected 429 refresh error")
	}
	if !strings.Contains(err.Error(), "status 429") {
		t.Fatalf("expected status 429 in error, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 refresh attempt after 429, got %d", got)
	}

	_, err = auth.RefreshTokensWithRetry(context.Background(), "dummy_refresh_token", 3)
	if err == nil {
		t.Fatalf("expected immediate blocked refresh error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected blocked retry to avoid a second refresh call, got %d attempts", got)
	}
	if blockedUntil := claudeRefreshBlockedUntil("dummy_refresh_token"); !blockedUntil.After(time.Now()) {
		t.Fatalf("expected blocked-until timestamp to be set, got %v", blockedUntil)
	}
}

func TestRefreshTokens_DeduplicatesConcurrentRefresh(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	var calls int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				once.Do(func() { close(started) })
				<-release
				return &http.Response{
					StatusCode: http.StatusOK,
					Body: io.NopCloser(strings.NewReader(`{
						"access_token":"new-access",
						"refresh_token":"new-refresh",
						"token_type":"Bearer",
						"expires_in":3600,
						"account":{"email_address":"shared@example.com"}
					}`)),
					Header:  make(http.Header),
					Request: req,
				}, nil
			}),
		},
	}

	results := make(chan *ClaudeTokenData, 2)
	errs := make(chan error, 2)
	runRefresh := func() {
		td, err := auth.RefreshTokens(context.Background(), "shared-refresh-token")
		results <- td
		errs <- err
	}

	go runRefresh()
	go runRefresh()

	<-started
	time.Sleep(20 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected concurrent refresh to share a single upstream call, got %d", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("expected refresh to succeed, got %v", err)
		}
		td := <-results
		if td == nil || td.AccessToken != "new-access" {
			t.Fatalf("expected refreshed access token, got %#v", td)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 upstream refresh call, got %d", got)
	}
}

func TestRefreshTokensWithRetryOptionsFallsBackToPlatformEndpoint(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()

	var apiCalls int32
	var platformCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/token":
			atomic.AddInt32(&apiCalls, 1)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		case "/platform/token":
			atomic.AddInt32(&platformCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"access_token":"platform-access",
				"refresh_token":"platform-refresh",
				"token_type":"Bearer",
				"expires_in":3600,
				"account":{"email_address":"user@example.com"}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldAPITokenURL := claudeAPITokenURL
	oldPlatformTokenURL := claudePlatformTokenURL
	defer func() {
		claudeAPITokenURL = oldAPITokenURL
		claudePlatformTokenURL = oldPlatformTokenURL
	}()
	claudeAPITokenURL = server.URL + "/api/token"
	claudePlatformTokenURL = server.URL + "/platform/token"

	auth := &ClaudeAuth{httpClient: server.Client()}
	td, err := auth.RefreshTokensWithRetryOptions(context.Background(), "refresh-token", 1, RefreshTokenOptions{
		TokenEndpoint:         claudeAPITokenURL,
		AllowEndpointFallback: true,
	})
	if err != nil {
		t.Fatalf("RefreshTokensWithRetryOptions returned error: %v", err)
	}
	if got := atomic.LoadInt32(&apiCalls); got != 1 {
		t.Fatalf("api refresh calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&platformCalls); got != 1 {
		t.Fatalf("platform refresh calls = %d, want 1", got)
	}
	if td.AccessToken != "platform-access" || td.RefreshToken != "platform-refresh" {
		t.Fatalf("token data = %#v, want platform tokens", td)
	}
	if td.TokenEndpoint != claudePlatformTokenURL {
		t.Fatalf("token endpoint = %q, want platform endpoint", td.TokenEndpoint)
	}
	if td.AuthSource != AuthSourceClaudePlatform {
		t.Fatalf("auth source = %q, want %q", td.AuthSource, AuthSourceClaudePlatform)
	}
}
