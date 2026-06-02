package executor

import (
	"net/http"
	"strings"
	"testing"
)

func TestCheckClaudeUpstreamBodySizeRejectsOversized(t *testing.T) {
	err := checkClaudeUpstreamBodySize(make([]byte, 100), 50)
	if err == nil {
		t.Fatal("expected error for oversized body, got nil")
	}
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if se.code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", se.code)
	}
	if !strings.Contains(se.msg, "exceeds") {
		t.Fatalf("msg should explain size limit, got %q", se.msg)
	}
}

func TestCheckClaudeUpstreamBodySizeAllowsWithinLimit(t *testing.T) {
	if err := checkClaudeUpstreamBodySize(make([]byte, 50), 100); err != nil {
		t.Fatalf("expected nil for body within limit, got %v", err)
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
