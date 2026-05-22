package helps

import "testing"

func TestIsValidUserIDAcceptsClaudeCodeJSONFormat(t *testing.T) {
	userID := `{"device_id":"d61f76d0aabbccdd00112233445566778899aabbccddeeff0011223344556677","account_uuid":"","session_id":"c72554f2-1234-5678-abcd-123456789abc"}`
	if !IsValidUserID(userID) {
		t.Fatalf("expected JSON metadata user_id to be valid")
	}
}

func TestShouldCloakRequestRequiresValidClaudeCodeUserID(t *testing.T) {
	userAgent := "claude-cli/2.1.148 (external, cli)"
	validUserID := `{"device_id":"device","account_uuid":"","session_id":"session"}`

	if ShouldCloakRequest("auto", userAgent, validUserID) {
		t.Fatalf("valid Claude Code request should not be cloaked")
	}
	if !ShouldCloakRequest("auto", userAgent, "not-a-valid-user-id") {
		t.Fatalf("spoofed Claude Code UA without valid metadata user_id should be cloaked")
	}
}
