package executor

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
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

func TestCheckClaudePromptTokenLimitRejectsOversizedPrompt(t *testing.T) {
	const modelID = "test-claude-prompt-limit-model"
	registerClaudeContextLengthModel(t, modelID, 3)

	body := []byte(`{"model":"test-claude-prompt-limit-model","messages":[{"role":"user","content":"alpha beta gamma delta epsilon zeta"}]}`)
	err := checkClaudePromptTokenLimit(body, modelID, nil)
	if err == nil {
		t.Fatal("expected error for oversized prompt, got nil")
	}
	authErr, ok := err.(*cliproxyauth.Error)
	if !ok {
		t.Fatalf("expected *cliproxyauth.Error, got %T", err)
	}
	if authErr.Code != cliproxyauth.LocalPromptTooLongErrorCode {
		t.Fatalf("code = %q, want %q", authErr.Code, cliproxyauth.LocalPromptTooLongErrorCode)
	}
	if authErr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("HTTPStatus = %d, want 400", authErr.HTTPStatus)
	}
	if authErr.Retryable {
		t.Fatal("Retryable = true, want false: an oversized prompt will not shrink on retry")
	}
	if !strings.Contains(authErr.Message, "prompt is too long") || !strings.Contains(authErr.Message, "maximum") {
		t.Fatalf("message should explain token limit, got %q", authErr.Message)
	}
}

func TestCheckClaudePromptTokenLimitUsesContext1MBetaLimit(t *testing.T) {
	const modelID = "test-claude-prompt-limit-context-1m-model"
	registerClaudeContextLengthModel(t, modelID, 3)

	body := []byte(`{"model":"test-claude-prompt-limit-context-1m-model","messages":[{"role":"user","content":"alpha beta gamma delta epsilon zeta"}]}`)
	if err := checkClaudePromptTokenLimit(body, modelID, []string{"context-1m-2025-08-07"}); err != nil {
		t.Fatalf("context-1m beta should still raise legacy prompt limits for compatibility, got %v", err)
	}
}

func TestClaudePromptTokenLimitUsesStaticSonnet46OneMillion(t *testing.T) {
	if got := claudePromptTokenLimit("claude-sonnet-4-6", nil); got != 1_000_000 {
		t.Fatalf("claude-sonnet-4-6 prompt token limit = %d, want 1000000", got)
	}
}

func TestClaudePromptTokenLimitKeepsOfficialOneMillionWhenDynamicCatalogIsStale(t *testing.T) {
	registerClaudeContextLengthModel(t, "claude-sonnet-4-6", 200_000)

	if got := claudePromptTokenLimit("claude-sonnet-4-6", nil); got != 1_000_000 {
		t.Fatalf("claude-sonnet-4-6 prompt token limit = %d, want 1000000", got)
	}
}

func TestCheckClaudePromptTokenLimitSkipsUnknownContextLength(t *testing.T) {
	body := []byte(`{"model":"test-claude-unknown-context","messages":[{"role":"user","content":"alpha beta gamma delta epsilon zeta"}]}`)
	if err := checkClaudePromptTokenLimit(body, "test-claude-unknown-context", nil); err != nil {
		t.Fatalf("unknown context length should not be blocked locally, got %v", err)
	}
}

func registerClaudeContextLengthModel(t *testing.T, modelID string, contextLength int) {
	t.Helper()

	clientID := modelID + "-client"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "claude", []*registry.ModelInfo{{
		ID:            modelID,
		Type:          "claude",
		ContextLength: contextLength,
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })
}
