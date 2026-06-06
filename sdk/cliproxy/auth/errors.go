package auth

const LocalRequestGuardErrorCode = "claude_mimicry_guard_blocked"

// LocalRequestTooLargeErrorCode marks a request rejected locally because its
// body exceeds the configured Claude upstream size limit. The request never
// reaches upstream, so this must be treated like other local guard errors and
// excluded from account health, otherwise oversized client requests would
// wrongly mark healthy accounts as request-error.
const LocalRequestTooLargeErrorCode = "claude_request_too_large"

// Error describes an authentication related failure in a provider agnostic format.
type Error struct {
	// Code is a short machine readable identifier.
	Code string `json:"code,omitempty"`
	// Message is a human readable description of the failure.
	Message string `json:"message"`
	// Retryable indicates whether a retry might fix the issue automatically.
	Retryable bool `json:"retryable"`
	// HTTPStatus optionally records an HTTP-like status code for the error.
	HTTPStatus int `json:"http_status,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// StatusCode implements optional status accessor for manager decision making.
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}
