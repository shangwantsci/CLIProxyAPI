package auth

import (
	"errors"
	"testing"
)

// realWorldSessionInvalidBody is the exact 403 body observed in production when a
// retained session_key is no longer valid (see session_key reauth failures).
const realWorldSessionInvalidBody = `{"type":"error","error":{"type":"permission_error","message":"Invalid authorization","details":{"error_visibility":"user_facing","error_code":"account_session_invalid"}},"request_id":"req_011CbgNKGMZhfhdG5S53RJAG"}`

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
