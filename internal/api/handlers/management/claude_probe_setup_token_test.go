package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
