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
