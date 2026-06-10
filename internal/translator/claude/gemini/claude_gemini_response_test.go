package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToGemini_StreamRefusalMapsSafety(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToGemini(
		ctx,
		"claude-fable-5",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"input_tokens":1,"output_tokens":1}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	if got := gjson.GetBytes(out[0], "candidates.0.finishReason").String(); got != "SAFETY" {
		t.Fatalf("finishReason = %q, want SAFETY; chunk=%s", got, string(out[0]))
	}
}

func TestConvertClaudeResponseToGeminiNonStream_IgnoresFallbackContentBlock(t *testing.T) {
	ctx := context.Background()
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_fallback","model":"claude-fable-5","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"fallback","model":"claude-opus-4-8","reason":"overloaded"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"content_block_stop","index":1}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToGeminiNonStream(ctx, "claude-fable-5", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "candidates.0.content.parts.0.text").String(); got != "ok" {
		t.Fatalf("text part = %q, want ok; body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "candidates.0.content.parts.#").Int(); got != 1 {
		t.Fatalf("parts count = %d, want 1; body=%s", got, string(out))
	}
}

func TestConvertClaudeResponseToGeminiNonStream_RefusalMapsSafety(t *testing.T) {
	ctx := context.Background()
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_refusal","model":"claude-fable-5","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":1}}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToGeminiNonStream(ctx, "claude-fable-5", nil, nil, raw, nil)
	if got := gjson.GetBytes(out, "candidates.0.finishReason").String(); got != "SAFETY" {
		t.Fatalf("finishReason = %q, want SAFETY; body=%s", got, string(out))
	}
}
