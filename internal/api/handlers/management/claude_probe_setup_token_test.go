package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClaudeMessagesProbePOSTSendsPostAndReturnsStatus(t *testing.T) {
	var gotMethod, gotAuth, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1"}`))
	}))
	defer srv.Close()

	h := &Handler{}
	auth := &coreauth.Auth{ID: "a1", Provider: "claude"}
	status, _, body, err := h.claudeMessagesProbePOSTTo(context.Background(), srv.URL, auth, "tok-xyz")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d", status)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", gotMethod)
	}
	if gotAuth != "Bearer tok-xyz" {
		t.Fatalf("expected bearer token header, got %q", gotAuth)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Fatalf("expected anthropic-beta header, got %q", gotBeta)
	}
	if len(body) == 0 {
		t.Fatalf("expected response body")
	}
}

func newMessagesProbeTestHandler(t *testing.T, auth *coreauth.Auth) *Handler {
	t.Helper()
	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	return &Handler{authManager: manager}
}

func TestRecordClaudeMessagesProbeHTTPFailurePermanentSetsReason(t *testing.T) {
	auth := &coreauth.Auth{ID: "perm1", Provider: "claude", Status: coreauth.StatusActive}
	h := newMessagesProbeTestHandler(t, auth)
	body := []byte(`{"type":"error","error":{"type":"forbidden","message":"This organization has been disabled."}}`)

	h.recordClaudeMessagesProbeHTTPFailure(context.Background(), auth, http.StatusForbidden, body)

	updated, ok := h.authManager.GetByID("perm1")
	if !ok {
		t.Fatalf("auth missing after update")
	}
	reason := claudeAuthStatusReason(updated, time.Now())
	if reason != "organization_disabled" {
		t.Fatalf("expected organization_disabled, got %q", reason)
	}
}

func TestRecordClaudeMessagesProbeHTTPFailureRateLimitedSetsReason(t *testing.T) {
	auth := &coreauth.Auth{ID: "rl1", Provider: "claude", Status: coreauth.StatusActive}
	h := newMessagesProbeTestHandler(t, auth)

	h.recordClaudeMessagesProbeHTTPFailure(context.Background(), auth, http.StatusTooManyRequests, []byte(`{"error":{"message":"rate limit"}}`))

	updated, _ := h.authManager.GetByID("rl1")
	if reason := claudeAuthStatusReason(updated, time.Now()); reason != "rate_limited" {
		t.Fatalf("expected rate_limited, got %q", reason)
	}
}

func TestRecordClaudeMessagesProbeHTTPFailureServerErrorSetsUnavailable(t *testing.T) {
	auth := &coreauth.Auth{ID: "se1", Provider: "claude", Status: coreauth.StatusActive}
	h := newMessagesProbeTestHandler(t, auth)

	h.recordClaudeMessagesProbeHTTPFailure(context.Background(), auth, http.StatusInternalServerError, []byte(`{"error":{"message":"internal"}}`))

	updated, _ := h.authManager.GetByID("se1")
	if reason := claudeAuthStatusReason(updated, time.Now()); reason != "unavailable" {
		t.Fatalf("expected unavailable, got %q", reason)
	}
	if updated.LastError == nil || !updated.LastError.Retryable {
		t.Fatalf("expected retryable LastError for 5xx")
	}
}

func TestRunClaudeProbeForSetupTokenHealthyOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1"}`))
	}))
	defer srv.Close()
	origURL := claudeImportProbeURL
	claudeImportProbeURL = srv.URL
	defer func() { claudeImportProbeURL = origURL }()

	auth := &coreauth.Auth{
		ID: "st1", Provider: "claude", Status: coreauth.StatusActive,
		Metadata: map[string]any{"auth_source": "claude_setup_token", "access_token": "tok"},
	}
	h := newMessagesProbeTestHandler(t, auth)
	res := h.runClaudeProbeForAuth(context.Background(), auth)
	if res.Status != "ok" {
		t.Fatalf("expected ok, got %q (msg=%s)", res.Status, res.Message)
	}
}

func TestRunClaudeProbeForSetupTokenPermanentOn403(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"This organization has been disabled."}}`))
	}))
	defer srv.Close()
	origURL := claudeImportProbeURL
	claudeImportProbeURL = srv.URL
	defer func() { claudeImportProbeURL = origURL }()

	auth := &coreauth.Auth{
		ID: "st2", Provider: "claude", Status: coreauth.StatusActive,
		Metadata: map[string]any{"auth_source": "claude_setup_token", "access_token": "tok"},
	}
	h := newMessagesProbeTestHandler(t, auth)
	res := h.runClaudeProbeForAuth(context.Background(), auth)
	if res.Status != "permanent_disabled" {
		t.Fatalf("expected permanent_disabled, got %q", res.Status)
	}
}
