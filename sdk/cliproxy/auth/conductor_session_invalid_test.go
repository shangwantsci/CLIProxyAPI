package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// realWorldSessionInvalidBody is the exact 403 body observed in production when a
// retained session_key is no longer valid (see session_key reauth failures).
const realWorldSessionInvalidBody = `{"type":"error","error":{"type":"permission_error","message":"Invalid authorization","details":{"error_visibility":"user_facing","error_code":"account_session_invalid"}},"request_id":"req_011CbgNKGMZhfhdG5S53RJAG"}`

const realWorldIdentityVerificationBody = `{"type":"error","error":{"type":"invalid_request_error","message":"Identity verification is required to continue."},"request_id":"req_011Cbv6j8FG1TWmADrxisbXh"}`

func TestPermanentAuthDisabledDetails_AccountSessionInvalid(t *testing.T) {
	code, message, ok := permanentAuthDisabledDetails(403, realWorldSessionInvalidBody)
	if !ok {
		t.Fatalf("account_session_invalid 403 must be classified as a permanent auth-disabled error, got ok=false")
	}
	if code != "account_session_invalid" {
		t.Fatalf("code = %q, want %q", code, "account_session_invalid")
	}
	if message == "" {
		t.Fatalf("message must be non-empty for account_session_invalid")
	}
}

func TestIsUnauthorizedError_AccountSessionInvalidReauthFailure(t *testing.T) {
	// Mirrors the wrapped error returned from reauthViaSessionKey -> CookieAuth.
	wrapped := errors.New("claude executor: session_key reauth failed for 服务账号-x@gmail.com.json: failed to get organization info: failed to get organizations: status 403: " + realWorldSessionInvalidBody)
	if !isUnauthorizedError(wrapped) {
		t.Fatalf("a session_key reauth failure carrying account_session_invalid must be treated as an unauthorized (auth-level) error so refreshAuth disables the account instead of leaving it active")
	}
}

func TestIsClientRequestResultError_AccountSessionInvalidNotClientError(t *testing.T) {
	// A live request returning 403 account_session_invalid must NOT be misfiled as a
	// transient client/request error (which surfaces to operators as "请求异常").
	resultErr := &Error{HTTPStatus: 403, Message: realWorldSessionInvalidBody}
	if isClientRequestResultError(resultErr) {
		t.Fatalf("account_session_invalid 403 must not be classified as a client/request error")
	}
}

func TestPermanentAuthDisabledDetails_IdentityVerificationRequired(t *testing.T) {
	code, message, ok := permanentAuthDisabledDetails(http.StatusBadRequest, realWorldIdentityVerificationBody)
	if !ok {
		t.Fatalf("identity verification 400 must be classified as a permanent auth-disabled error, got ok=false")
	}
	if code != "identity_verification_required" {
		t.Fatalf("code = %q, want %q", code, "identity_verification_required")
	}
	if message != "Identity verification is required to continue." {
		t.Fatalf("message = %q, want upstream identity verification message", message)
	}
}

func TestIsClientRequestResultError_IdentityVerificationRequiredNotClientError(t *testing.T) {
	resultErr := &Error{HTTPStatus: http.StatusBadRequest, Message: realWorldIdentityVerificationBody}
	if isClientRequestResultError(resultErr) {
		t.Fatalf("identity verification 400 must not be classified as a client/request error")
	}
}

func TestManager_RetriesNextAuthWhenClaudeIdentityVerificationRequired(t *testing.T) {
	m := NewManager(nil, nil, nil)

	executor := &authFallbackExecutor{
		id: "claude",
		executeErrors: map[string]error{
			"claude-identity-required": &Error{
				HTTPStatus: http.StatusBadRequest,
				Message:    realWorldIdentityVerificationBody,
			},
		},
	}
	m.RegisterExecutor(executor)

	model := "claude-haiku-4-5-20251001"
	blockedAuth := &Auth{
		ID:       "claude-identity-required",
		Provider: "claude",
		Attributes: map[string]string{
			"priority": "10",
		},
	}
	healthyAuth := &Auth{ID: "claude-healthy", Provider: "claude"}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(blockedAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(healthyAuth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(blockedAuth.ID)
		reg.UnregisterClient(healthyAuth.ID)
	})

	if _, errRegister := m.Register(context.Background(), blockedAuth); errRegister != nil {
		t.Fatalf("register blocked auth: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), healthyAuth); errRegister != nil {
		t.Fatalf("register healthy auth: %v", errRegister)
	}

	resp, errExecute := m.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute returned error: %v", errExecute)
	}
	if string(resp.Payload) != healthyAuth.ID {
		t.Fatalf("response payload = %q, want %q", string(resp.Payload), healthyAuth.ID)
	}

	updated, ok := m.GetByID(blockedAuth.ID)
	if !ok {
		t.Fatal("updated auth not found")
	}
	if !updated.Disabled || updated.Status != StatusDisabled {
		t.Fatalf("disabled/status = %v/%s, want true/disabled", updated.Disabled, updated.Status)
	}
	if updated.LastError == nil {
		t.Fatal("LastError is nil")
	}
	if updated.LastError.Code != "identity_verification_required" || updated.LastError.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("LastError = %#v, want identity_verification_required 400", updated.LastError)
	}
}
