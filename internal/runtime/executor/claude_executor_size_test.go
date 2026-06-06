package executor

import (
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCheckClaudeUpstreamBodySizeRejectsOversized(t *testing.T) {
	err := checkClaudeUpstreamBodySize(make([]byte, 100), 50)
	if err == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
	authErr, ok := err.(*cliproxyauth.Error)
	if !ok {
		t.Fatalf("expected *cliproxyauth.Error, got %T", err)
	}
	if authErr.Code != cliproxyauth.LocalRequestTooLargeErrorCode {
		t.Fatalf("code = %q, want %q (must be a local request-guard error so the conductor excludes it from account health)", authErr.Code, cliproxyauth.LocalRequestTooLargeErrorCode)
	}
	if authErr.HTTPStatus != http.StatusRequestEntityTooLarge {
		t.Fatalf("HTTPStatus = %d, want 413", authErr.HTTPStatus)
	}
	if authErr.Retryable {
		t.Fatal("Retryable = true, want false: an oversized body will not shrink on retry")
	}
	if !strings.Contains(authErr.Message, "exceeds") {
		t.Fatalf("message should explain size limit, got %q", authErr.Message)
	}
}

func TestCheckClaudeUpstreamBodySizeAllowsWithinLimit(t *testing.T) {
	if err := checkClaudeUpstreamBodySize(make([]byte, 50), 100); err != nil {
		t.Fatalf("expected nil for body within limit, got %v", err)
	}
	if err := checkClaudeUpstreamBodySize(make([]byte, 50), 50); err != nil {
		t.Fatalf("body == limit should be allowed (strict >), got %v", err)
	}
}

func TestCheckClaudeUpstreamBodySizeDisabledWhenLimitZeroOrNegative(t *testing.T) {
	if err := checkClaudeUpstreamBodySize(make([]byte, 1<<20), 0); err != nil {
		t.Fatalf("limit 0 should disable check, got %v", err)
	}
	if err := checkClaudeUpstreamBodySize(make([]byte, 1<<20), -1); err != nil {
		t.Fatalf("negative limit should disable check, got %v", err)
	}
}
