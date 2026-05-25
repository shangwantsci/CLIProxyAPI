package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAIResponsesNonStream_RewritesUsageToBillableInput(t *testing.T) {
	original := []byte(`{"model":"claude-opus-4-7","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	ctx := helps.WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "openai-response", original)
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7","usage":{"input_tokens":1913,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(ctx, "claude-opus-4-7", original, nil, raw, nil)

	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got <= 0 || got >= 30 {
		t.Fatalf("usage.input_tokens = %d, want small billable count; out=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.output_tokens").Int(); got != 5 {
		t.Fatalf("usage.output_tokens = %d, want 5", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_RepairsConcatenatedToolArguments(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"location\":\"Tokyo\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":8}}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-opus-4-7", nil, nil, raw, nil)

	if got := gjson.GetBytes(out, "output.0.arguments").String(); got != `{"location":"Tokyo"}` {
		t.Fatalf("tool arguments = %q, want repaired JSON; out=%s", got, string(out))
	}
}

func TestConvertClaudeResponseToOpenAIResponsesStream_RewritesUsageToBillableInput(t *testing.T) {
	original := []byte(`{"model":"claude-opus-4-7","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	ctx := helps.WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "openai-response", original)
	var param any

	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-opus-4-7", original, nil, []byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7","usage":{"input_tokens":1913,"output_tokens":0}}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-opus-4-7", original, nil, []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`), &param)
	chunks := ConvertClaudeResponseToOpenAIResponses(ctx, "claude-opus-4-7", original, nil, []byte(`data: {"type":"message_stop"}`), &param)

	var completed []byte
	for _, chunk := range chunks {
		if bytes := []byte(chunk); strings.HasPrefix(string(bytes), "event: response.completed") {
			if idx := strings.Index(string(bytes), "\ndata: "); idx >= 0 {
				completed = bytes[idx+7:]
			}
		}
	}
	if len(completed) == 0 {
		t.Fatalf("response.completed event not found: %q", chunks)
	}
	if got := gjson.GetBytes(completed, "response.usage.input_tokens").Int(); got <= 0 || got >= 30 {
		t.Fatalf("stream response.usage.input_tokens = %d, want small billable count; payload=%s", got, string(completed))
	}
}

func TestConvertClaudeResponseToOpenAIResponsesStreamReadsCacheBreakdownWhenBillableDisabled(t *testing.T) {
	ctx := helps.WithClaudeBillableUsage(context.Background(), false, "claude-opus-4-7", "openai-response", nil)
	var param any

	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-opus-4-7", nil, nil, []byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7","usage":{"input_tokens":13,"cached_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4}}}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-opus-4-7", nil, nil, []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`), &param)
	chunks := ConvertClaudeResponseToOpenAIResponses(ctx, "claude-opus-4-7", nil, nil, []byte(`data: {"type":"message_stop"}`), &param)

	var completed []byte
	for _, chunk := range chunks {
		if strings.HasPrefix(string(chunk), "event: response.completed") {
			if idx := strings.Index(string(chunk), "\ndata: "); idx >= 0 {
				completed = chunk[idx+7:]
			}
		}
	}
	if len(completed) == 0 {
		t.Fatalf("response.completed event not found: %q", chunks)
	}
	if got := gjson.GetBytes(completed, "response.usage.input_tokens").Int(); got != 40 {
		t.Fatalf("response.usage.input_tokens = %d, want %d; payload=%s", got, 40, string(completed))
	}
	if got := gjson.GetBytes(completed, "response.usage.input_tokens_details.cached_tokens").Int(); got != 20 {
		t.Fatalf("response.usage cached_tokens = %d, want %d; payload=%s", got, 20, string(completed))
	}
	if got := gjson.GetBytes(completed, "response.usage.total_tokens").Int(); got != 45 {
		t.Fatalf("response.usage.total_tokens = %d, want %d; payload=%s", got, 45, string(completed))
	}
}
