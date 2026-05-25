package helps

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestEstimateClaudeBillableInputTokensShortMessage(t *testing.T) {
	payload := []byte(`{"model":"claude-opus-4-7","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`)

	count := EstimateClaudeBillableInputTokens("claude-opus-4-7", "claude", payload)

	if count <= 0 {
		t.Fatalf("expected positive billable count, got %d", count)
	}
	if count >= 20 {
		t.Fatalf("expected short prompt to stay small, got %d", count)
	}
}

func TestRewriteClaudeUsageForBillableKeepsOutputAndDropsWrapperInput(t *testing.T) {
	ctx := WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "claude", []byte(`{
		"model":"claude-opus-4-7",
		"max_tokens":50,
		"messages":[{"role":"user","content":"hi"}]
	}`))
	raw := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":1913,"cache_creation_input_tokens":2194,"cache_read_input_tokens":100,"output_tokens":16}}`)

	out := RewriteClaudeUsageForBillable(ctx, raw)

	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got <= 0 || got >= 20 {
		t.Fatalf("billable input_tokens = %d, want small positive count; out=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.cache_creation_input_tokens").Int(); got != 0 {
		t.Fatalf("cache_creation_input_tokens = %d, want 0", got)
	}
	if got := gjson.GetBytes(out, "usage.cache_read_input_tokens").Int(); got != 0 {
		t.Fatalf("cache_read_input_tokens = %d, want 0", got)
	}
	if got := gjson.GetBytes(out, "usage.output_tokens").Int(); got != 16 {
		t.Fatalf("output_tokens = %d, want 16", got)
	}
}

func TestRewriteClaudeStreamUsageForBillable(t *testing.T) {
	ctx := WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "claude", []byte(`{
		"messages":[{"role":"user","content":"hi"}]
	}`))
	line := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":1913,"cache_creation_input_tokens":31,"cache_read_input_tokens":22,"output_tokens":4}}`)

	out := RewriteClaudeStreamUsageForBillable(ctx, line)

	payload := jsonPayload(out)
	if got := gjson.GetBytes(payload, "usage.input_tokens").Int(); got <= 0 || got >= 20 {
		t.Fatalf("stream billable input_tokens = %d, want small positive count; out=%s", got, string(out))
	}
	if got := gjson.GetBytes(payload, "usage.cache_creation_input_tokens").Int(); got != 0 {
		t.Fatalf("stream cache_creation_input_tokens = %d, want 0", got)
	}
	if got := gjson.GetBytes(payload, "usage.cache_read_input_tokens").Int(); got != 0 {
		t.Fatalf("stream cache_read_input_tokens = %d, want 0", got)
	}
}

func TestRewriteClaudeStreamUsageForBillableDoesNotInventInputTokens(t *testing.T) {
	ctx := WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "claude", []byte(`{
		"messages":[{"role":"user","content":"hi"}]
	}`))
	line := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`)

	out := RewriteClaudeStreamUsageForBillable(ctx, line)

	payload := jsonPayload(out)
	if gjson.GetBytes(payload, "usage.input_tokens").Exists() {
		t.Fatalf("message_delta usage unexpectedly gained input_tokens; out=%s", string(out))
	}
	if got := gjson.GetBytes(payload, "usage.output_tokens").Int(); got != 4 {
		t.Fatalf("stream output_tokens = %d, want 4", got)
	}
}

func TestRewriteClaudeStreamUsageForBillableZerosCacheWithoutInventingInput(t *testing.T) {
	ctx := WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "claude", []byte(`{
		"messages":[{"role":"user","content":"hi"}]
	}`))
	line := []byte(`data: {"type":"message_delta","usage":{"output_tokens":4,"cache_creation_input_tokens":31,"cache_read_input_tokens":22,"cached_tokens":22,"cache_creation":{"ephemeral_5m_input_tokens":9,"ephemeral_1h_input_tokens":10}}}`)

	out := RewriteClaudeStreamUsageForBillable(ctx, line)

	payload := jsonPayload(out)
	if gjson.GetBytes(payload, "usage.input_tokens").Exists() {
		t.Fatalf("message_delta cache usage unexpectedly gained input_tokens; out=%s", string(out))
	}
	if got := gjson.GetBytes(payload, "usage.cache_creation_input_tokens").Int(); got != 0 {
		t.Fatalf("stream cache_creation_input_tokens = %d, want 0", got)
	}
	if got := gjson.GetBytes(payload, "usage.cache_read_input_tokens").Int(); got != 0 {
		t.Fatalf("stream cache_read_input_tokens = %d, want 0", got)
	}
	if got := gjson.GetBytes(payload, "usage.cached_tokens").Int(); got != 0 {
		t.Fatalf("stream cached_tokens = %d, want 0", got)
	}
}

func TestRewriteClaudeStreamUsageForBillableMessageStartUsage(t *testing.T) {
	ctx := WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "claude", []byte(`{
		"messages":[{"role":"user","content":"hi"}]
	}`))
	line := []byte(`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1913,"cache_creation_input_tokens":31,"cache_read_input_tokens":22,"cache_creation":{"ephemeral_5m_input_tokens":9,"ephemeral_1h_input_tokens":10},"output_tokens":0}}}`)

	out := RewriteClaudeStreamUsageForBillable(ctx, line)

	payload := jsonPayload(out)
	if got := gjson.GetBytes(payload, "message.usage.input_tokens").Int(); got <= 0 || got >= 20 {
		t.Fatalf("message_start billable input_tokens = %d, want small positive count; out=%s", got, string(out))
	}
	if got := gjson.GetBytes(payload, "message.usage.cache_creation_input_tokens").Int(); got != 0 {
		t.Fatalf("message_start cache_creation_input_tokens = %d, want 0", got)
	}
	if got := gjson.GetBytes(payload, "message.usage.cache_creation.ephemeral_5m_input_tokens").Int(); got != 0 {
		t.Fatalf("message_start cache_creation ephemeral_5m = %d, want 0", got)
	}
	if got := gjson.GetBytes(payload, "message.usage.cache_creation.ephemeral_1h_input_tokens").Int(); got != 0 {
		t.Fatalf("message_start cache_creation ephemeral_1h = %d, want 0", got)
	}
	if got := gjson.GetBytes(payload, "message.usage.cache_read_input_tokens").Int(); got != 0 {
		t.Fatalf("message_start cache_read_input_tokens = %d, want 0", got)
	}
	if got := gjson.GetBytes(payload, "message.usage.output_tokens").Int(); got != 0 {
		t.Fatalf("message_start output_tokens = %d, want 0", got)
	}
}
