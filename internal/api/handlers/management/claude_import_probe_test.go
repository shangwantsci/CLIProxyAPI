package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func newProbeTestHandler() *Handler {
	return &Handler{cfg: &config.Config{}}
}

// TestSingleProbePermanentErrorRejects verifies that a 403 response with
// "organization has been disabled" body is classified as *claudeImportPermanentError
// with Code == "organization_disabled".
func TestSingleProbePermanentErrorRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"type":"forbidden","message":"This organization has been disabled."}}`))
	}))
	defer srv.Close()

	h := newProbeTestHandler()
	err := h.singleClaudeImportProbeTo(context.Background(), srv.URL, "", "tok", "claude-sonnet-4-5")
	if err == nil {
		t.Fatal("expected permanent error, got nil")
	}
	var permErr *claudeImportPermanentError
	if !errors.As(err, &permErr) {
		t.Fatalf("expected *claudeImportPermanentError, got %T: %v", err, err)
	}
	if permErr.Code != "organization_disabled" {
		t.Fatalf("code = %q, want organization_disabled", permErr.Code)
	}
}

// TestSingleProbeTransientAllows verifies that a 429 (rate limit) response
// returns nil — transient errors must not reject good accounts.
func TestSingleProbeTransientAllows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	defer srv.Close()

	h := newProbeTestHandler()
	if err := h.singleClaudeImportProbeTo(context.Background(), srv.URL, "", "tok", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("expected nil (transient allows import), got %v", err)
	}
}

// TestSingleProbe2xxAllows verifies that a 200 OK response returns nil.
func TestSingleProbe2xxAllows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_1","content":[]}`))
	}))
	defer srv.Close()

	h := newProbeTestHandler()
	if err := h.singleClaudeImportProbeTo(context.Background(), srv.URL, "", "tok", "claude-sonnet-4-5"); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestDualProbeAnyPermanentRejects verifies that probeClaudeImportLivenessTo
// rejects when the second probe returns a permanent error, even if the first
// probe succeeds.
func TestDualProbeAnyPermanentRejects(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"msg_1"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"type":"forbidden","message":"This organization has been disabled."}}`))
	}))
	defer srv.Close()

	h := newProbeTestHandler()
	if err := h.probeClaudeImportLivenessTo(context.Background(), srv.URL, "", "tok", "claude-sonnet-4-5"); err == nil {
		t.Fatal("expected rejection when second probe is permanent error")
	}
}

// TestDualProbeFirstPermanentRejectsImmediately verifies that probeClaudeImportLivenessTo
// short-circuits: a permanent error on the first probe rejects immediately without
// issuing the second request.
func TestDualProbeFirstPermanentRejectsImmediately(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"type":"forbidden","message":"This organization has been disabled."}}`))
	}))
	defer srv.Close()

	h := newProbeTestHandler()
	err := h.probeClaudeImportLivenessTo(context.Background(), srv.URL, "", "tok", "claude-sonnet-4-5")
	if err == nil {
		t.Fatal("expected rejection when first probe is permanent error")
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("expected short-circuit after first probe (1 request), got %d requests", got)
	}
}

// TestProcessKeyPermanentErrorIsRejected verifies that processClaudeSessionImportKey
// maps a *claudeImportPermanentError from the authenticate seam to Status="rejected"
// and Reason=permErr.Code.
func TestProcessKeyPermanentErrorIsRejected(t *testing.T) {
	h := newProbeTestHandler()
	h.sessionImportAuthenticate = func(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
		return claudeSessionImportAuthResult{}, &claudeImportPermanentError{Code: "organization_disabled", Message: "This organization has been disabled."}
	}
	res := h.processClaudeSessionImportKey(context.Background(), "sk-test", normalizedClaudeSessionImportRequest{})
	if res.Status != "rejected" {
		t.Fatalf("status = %q, want rejected", res.Status)
	}
	if res.Reason != "organization_disabled" {
		t.Fatalf("reason = %q, want organization_disabled", res.Reason)
	}
}

// TestProcessKeyTransientErrorIsFailed verifies that a transient (non-permanent)
// error from the authenticate seam results in Status == "failed" — not rejected,
// and not silently promoted to imported.
func TestProcessKeyTransientErrorIsFailed(t *testing.T) {
	h := newProbeTestHandler()
	h.sessionImportAuthenticate = func(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
		return claudeSessionImportAuthResult{}, context.DeadlineExceeded
	}
	res := h.processClaudeSessionImportKey(context.Background(), "sk-test", normalizedClaudeSessionImportRequest{})
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
}
