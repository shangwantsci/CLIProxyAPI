package chat_completions

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

func TestConvertClaudeResponseToOpenAI_StreamUsageIncludesCachedTokens(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":4,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gotPromptTokens := gjson.GetBytes(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out[0], "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAI_StreamUsageReadsCacheCreationBreakdown(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":13,"output_tokens":4,"cached_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4}}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gotPromptTokens := gjson.GetBytes(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 40 {
		t.Fatalf("expected prompt_tokens %d, got %d", 40, gotPromptTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out[0], "usage.total_tokens").Int(); gotTotalTokens != 44 {
		t.Fatalf("expected total_tokens %d, got %d", 44, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 20 {
		t.Fatalf("expected cached_tokens %d, got %d", 20, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAI_StreamUsageMergesMessageStartUsage(t *testing.T) {
	ctx := context.Background()
	var param any

	ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","model":"claude-opus-4-6","usage":{"input_tokens":13,"output_tokens":1,"cache_read_input_tokens":22000,"cache_creation_input_tokens":31}}}`),
		&param,
	)
	out := ConvertClaudeResponseToOpenAI(
		ctx,
		"claude-opus-4-6",
		nil,
		nil,
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	if gotPromptTokens := gjson.GetBytes(out[0], "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out[0], "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out[0], "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out[0], "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_UsageIncludesCachedTokens(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\"}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":13,\"output_tokens\":4,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotPromptTokens := gjson.GetBytes(out, "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out, "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out, "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_UsageMergesMessageStartUsage(t *testing.T) {
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\",\"usage\":{\"input_tokens\":13,\"output_tokens\":1,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "", nil, nil, rawJSON, nil)

	if gotPromptTokens := gjson.GetBytes(out, "usage.prompt_tokens").Int(); gotPromptTokens != 22044 {
		t.Fatalf("expected prompt_tokens %d, got %d", 22044, gotPromptTokens)
	}
	if gotCompletionTokens := gjson.GetBytes(out, "usage.completion_tokens").Int(); gotCompletionTokens != 4 {
		t.Fatalf("expected completion_tokens %d, got %d", 4, gotCompletionTokens)
	}
	if gotTotalTokens := gjson.GetBytes(out, "usage.total_tokens").Int(); gotTotalTokens != 22048 {
		t.Fatalf("expected total_tokens %d, got %d", 22048, gotTotalTokens)
	}
	if gotCachedTokens := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); gotCachedTokens != 22000 {
		t.Fatalf("expected cached_tokens %d, got %d", 22000, gotCachedTokens)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_DropsProxyCacheBreakdownWhenBillableInputEnabled(t *testing.T) {
	originalReq := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}],"max_tokens":40}`)
	ctx := helps.WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "openai", originalReq)
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-7\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":13,\"output_tokens\":4,\"cache_read_input_tokens\":22000,\"cache_creation_input_tokens\":31}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(ctx, "claude-opus-4-7", originalReq, nil, rawJSON, nil)

	if got := gjson.GetBytes(out, "usage.prompt_tokens").Int(); got <= 0 || got >= 20 {
		t.Fatalf("prompt_tokens = %d, want small billable input; out=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); got != 0 {
		t.Fatalf("cached_tokens = %d, want 0", got)
	}
	if gjson.GetBytes(out, "usage.prompt_tokens_details.cached_creation_tokens").Exists() {
		t.Fatalf("cached_creation_tokens should not be exposed after billable rewrite; out=%s", string(out))
	}
	if gjson.GetBytes(out, "usage.usage_semantic").Exists() {
		t.Fatalf("usage_semantic should not be exposed after billable rewrite; out=%s", string(out))
	}
	if got := gjson.GetBytes(out, "usage.completion_tokens").Int(); got != 4 {
		t.Fatalf("completion_tokens = %d, want 4", got)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_RewritesUsageToBillableInputWithoutCacheBreakdown(t *testing.T) {
	originalReq := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hi"}],"max_tokens":40}`)
	ctx := helps.WithClaudeBillableUsage(context.Background(), true, "claude-opus-4-7", "openai", originalReq)
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-7\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":1913,\"output_tokens\":4}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(ctx, "claude-opus-4-7", originalReq, nil, rawJSON, nil)

	if got := gjson.GetBytes(out, "usage.prompt_tokens").Int(); got <= 0 || got >= 20 {
		t.Fatalf("prompt_tokens = %d, want small billable input; out=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); got != 0 {
		t.Fatalf("cached_tokens = %d, want 0", got)
	}
	if got := gjson.GetBytes(out, "usage.completion_tokens").Int(); got != 4 {
		t.Fatalf("completion_tokens = %d, want 4", got)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_RepairsConcatenatedToolArguments(t *testing.T) {
	originalReq := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"Call weather"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`)
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-7\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"get_weather\",\"input\":{}}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"location\\\":\\\"Tokyo\\\"}\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":100,\"output_tokens\":10}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-opus-4-7", originalReq, nil, rawJSON, nil)

	args := gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String()
	if args != `{"location":"Tokyo"}` {
		t.Fatalf("arguments = %q, want valid last object", args)
	}
	if !gjson.Valid(args) || !gjson.Parse(args).IsObject() {
		t.Fatalf("arguments should be valid JSON object, got %q", args)
	}
}

func TestConvertClaudeResponseToOpenAINonStream_AppliesOriginalStopSequences(t *testing.T) {
	originalReq := []byte(`{"model":"claude-opus-4-7","messages":[{"role":"user","content":"Reply exactly: ALPHA STOP BETA"}],"stop":["STOP"]}`)
	rawJSON := []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-7\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ALPHA STOP BETA\"}}\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":100,\"output_tokens\":5}}\n")

	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-opus-4-7", originalReq, nil, rawJSON, nil)

	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "ALPHA " {
		t.Fatalf("content = %q, want stop-truncated text", got)
	}
}
