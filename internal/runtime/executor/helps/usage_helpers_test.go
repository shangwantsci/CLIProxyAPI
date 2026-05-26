package helps

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestParseOpenAIUsageChatCompletions(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":5}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 1 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 1)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 3 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 3)
	}
	if detail.CachedTokens != 4 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 4)
	}
	if detail.ReasoningTokens != 5 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 5)
	}
}

func TestParseOpenAIUsageResponses(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30,"input_tokens_details":{"cached_tokens":7},"output_tokens_details":{"reasoning_tokens":9}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 20 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 20)
	}
	if detail.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 30)
	}
	if detail.CachedTokens != 7 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 7)
	}
	if detail.ReasoningTokens != 9 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 9)
	}
}

func TestParseOpenAIUsageIgnoresNullUsage(t *testing.T) {
	data := []byte(`{"usage":null}`)
	detail := ParseOpenAIUsage(data)
	if detail != (usage.Detail{}) {
		t.Fatalf("detail = %+v, want zero detail", detail)
	}
}

func TestParseOpenAIStreamUsageIgnoresNullUsage(t *testing.T) {
	line := []byte(`data: {"id":"chunk_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}],"usage":null}`)
	if detail, ok := ParseOpenAIStreamUsage(line); ok {
		t.Fatalf("ParseOpenAIStreamUsage() = (%+v, true), want false for null usage", detail)
	}
}

func TestParseOpenAIStreamUsageResponsesFields(t *testing.T) {
	line := []byte(`data: {"id":"chunk_1","object":"chat.completion.chunk","choices":[],"usage":{"input_tokens":8,"output_tokens":5,"total_tokens":13,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}`)
	detail, ok := ParseOpenAIStreamUsage(line)
	if !ok {
		t.Fatal("ParseOpenAIStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 8 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 8)
	}
	if detail.OutputTokens != 5 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 5)
	}
	if detail.TotalTokens != 13 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 13)
	}
	if detail.CachedTokens != 3 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 3)
	}
	if detail.ReasoningTokens != 2 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 2)
	}
}

func TestParseOpenAIUsageCopiesCachedTokensToCacheRead(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":4},"cache_creation_input_tokens":7}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.CachedTokens != 4 || detail.CacheReadTokens != 4 {
		t.Fatalf("cached/cache read tokens = %d/%d, want 4/4", detail.CachedTokens, detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 7 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 7)
	}
}

func TestParseOpenAIUsageReadsCachedCreationTokenDetails(t *testing.T) {
	data := []byte(`{"usage":{"prompt_tokens":13,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":23855,"cached_creation_tokens":422}}}`)
	detail := ParseOpenAIUsage(data)
	if detail.InputTokens != 13 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 13)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.CachedTokens != 23855 || detail.CacheReadTokens != 23855 {
		t.Fatalf("cached/cache read tokens = %d/%d, want 23855/23855", detail.CachedTokens, detail.CacheReadTokens)
	}
	if detail.CacheCreationTokens != 422 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 422)
	}
}

func TestParseClaudeUsageReadsCacheCreationBreakdown(t *testing.T) {
	data := []byte(`{"usage":{"input_tokens":10,"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":4},"cache_read_input_tokens":5}}`)
	detail := ParseClaudeUsage(data)
	if detail.InputTokens != 10 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 10)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.CacheCreationTokens != 7 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 7)
	}
	if detail.CacheReadTokens != 5 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 5)
	}
	if detail.CachedTokens != 5 {
		t.Fatalf("cached tokens = %d, want cache read tokens %d", detail.CachedTokens, 5)
	}
	if detail.TotalTokens != 24 {
		t.Fatalf("total tokens = %d, want input+output+cache %d", detail.TotalTokens, 24)
	}
}

func TestParseClaudeStreamUsageReadsMessageStartUsage(t *testing.T) {
	line := []byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"cache_creation_input_tokens":13,"cache_read_input_tokens":17}}}`)
	detail, ok := ParseClaudeStreamUsage(line)
	if !ok {
		t.Fatal("ParseClaudeStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.CacheCreationTokens != 13 {
		t.Fatalf("cache creation tokens = %d, want %d", detail.CacheCreationTokens, 13)
	}
	if detail.CacheReadTokens != 17 {
		t.Fatalf("cache read tokens = %d, want %d", detail.CacheReadTokens, 17)
	}
}

func TestMergeUsageDetailKeepsStartInputAndFinalOutput(t *testing.T) {
	start := usage.Detail{InputTokens: 11, CacheCreationTokens: 13, CacheReadTokens: 17, CachedTokens: 17}
	final := usage.Detail{OutputTokens: 19}
	merged := MergeUsageDetail(start, final)

	if merged.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", merged.InputTokens, 11)
	}
	if merged.OutputTokens != 19 {
		t.Fatalf("output tokens = %d, want %d", merged.OutputTokens, 19)
	}
	if merged.CacheCreationTokens != 13 {
		t.Fatalf("cache creation tokens = %d, want %d", merged.CacheCreationTokens, 13)
	}
	if merged.CacheReadTokens != 17 || merged.CachedTokens != 17 {
		t.Fatalf("cache read/cached tokens = %d/%d, want 17/17", merged.CacheReadTokens, merged.CachedTokens)
	}
	if merged.TotalTokens != 60 {
		t.Fatalf("total tokens = %d, want input+output+cache %d", merged.TotalTokens, 60)
	}
}

func TestMergeParsedClaudeStreamUsageComputesAggregateTotal(t *testing.T) {
	start, ok := ParseClaudeStreamUsage([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"cache_creation_input_tokens":13,"cache_read_input_tokens":17}}}`))
	if !ok {
		t.Fatal("message_start usage not parsed")
	}
	delta, ok := ParseClaudeStreamUsage([]byte(`data: {"type":"message_delta","usage":{"output_tokens":19}}`))
	if !ok {
		t.Fatal("message_delta usage not parsed")
	}

	merged := MergeUsageDetail(MergeUsageDetail(usage.Detail{}, start), delta)

	if merged.InputTokens != 11 || merged.OutputTokens != 19 {
		t.Fatalf("merged input/output = %d/%d, want 11/19", merged.InputTokens, merged.OutputTokens)
	}
	if merged.CacheCreationTokens != 13 || merged.CacheReadTokens != 17 {
		t.Fatalf("merged cache create/read = %d/%d, want 13/17", merged.CacheCreationTokens, merged.CacheReadTokens)
	}
	if merged.TotalTokens != 60 {
		t.Fatalf("merged total tokens = %d, want input+output+cache 60", merged.TotalTokens)
	}
}

func TestMergeClaudeStreamUsageLinesKeepsInputOutputAndCache(t *testing.T) {
	data := []byte(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"cache_creation_input_tokens":13,"cache_read_input_tokens":17}}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":19}}`,
	}, "\n"))

	merged, ok := MergeClaudeStreamUsageLines(data)

	if !ok {
		t.Fatal("MergeClaudeStreamUsageLines() ok = false, want true")
	}
	if merged.InputTokens != 11 || merged.OutputTokens != 19 {
		t.Fatalf("merged input/output = %d/%d, want 11/19", merged.InputTokens, merged.OutputTokens)
	}
	if merged.CacheCreationTokens != 13 || merged.CacheReadTokens != 17 {
		t.Fatalf("merged cache create/read = %d/%d, want 13/17", merged.CacheCreationTokens, merged.CacheReadTokens)
	}
	if merged.TotalTokens != 60 {
		t.Fatalf("merged total tokens = %d, want input+output+cache 60", merged.TotalTokens)
	}
}

func TestParseGeminiCLIUsage_TopLevelUsageMetadata(t *testing.T) {
	data := []byte(`{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"thoughtsTokenCount":3,"totalTokenCount":21,"cachedContentTokenCount":5}}`)
	detail := ParseGeminiCLIUsage(data)
	if detail.InputTokens != 11 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 11)
	}
	if detail.OutputTokens != 7 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 7)
	}
	if detail.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens = %d, want %d", detail.ReasoningTokens, 3)
	}
	if detail.TotalTokens != 21 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 21)
	}
	if detail.CachedTokens != 5 {
		t.Fatalf("cached tokens = %d, want %d", detail.CachedTokens, 5)
	}
}

func TestParseGeminiCLIStreamUsage_ResponseSnakeCaseUsageMetadata(t *testing.T) {
	line := []byte(`data: {"response":{"usage_metadata":{"promptTokenCount":13,"candidatesTokenCount":2,"totalTokenCount":15}}}`)
	detail, ok := ParseGeminiCLIStreamUsage(line)
	if !ok {
		t.Fatal("ParseGeminiCLIStreamUsage() ok = false, want true")
	}
	if detail.InputTokens != 13 {
		t.Fatalf("input tokens = %d, want %d", detail.InputTokens, 13)
	}
	if detail.OutputTokens != 2 {
		t.Fatalf("output tokens = %d, want %d", detail.OutputTokens, 2)
	}
	if detail.TotalTokens != 15 {
		t.Fatalf("total tokens = %d, want %d", detail.TotalTokens, 15)
	}
}

func TestParseGeminiCLIStreamUsage_IgnoresTrafficTypeOnlyUsageMetadata(t *testing.T) {
	line := []byte(`data: {"response":{"usageMetadata":{"trafficType":"ON_DEMAND"}}}`)
	if detail, ok := ParseGeminiCLIStreamUsage(line); ok {
		t.Fatalf("ParseGeminiCLIStreamUsage() = (%+v, true), want false for traffic-only usage metadata", detail)
	}
}

func TestUsageReporterBuildRecordIncludesLatency(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "openai",
		model:       "gpt-5.4",
		requestedAt: time.Now().Add(-1500 * time.Millisecond),
	}

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Latency < time.Second {
		t.Fatalf("latency = %v, want >= 1s", record.Latency)
	}
	if record.Latency > 3*time.Second {
		t.Fatalf("latency = %v, want <= 3s", record.Latency)
	}
}

func TestUsageReporterBuildRecordIncludesRequestedModelAlias(t *testing.T) {
	ctx := usage.WithRequestedModelAlias(context.Background(), "client-gpt")
	reporter := NewUsageReporter(ctx, "openai", "gpt-5.4", nil)

	record := reporter.buildRecord(usage.Detail{TotalTokens: 3}, false)
	if record.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want %q", record.Model, "gpt-5.4")
	}
	if record.Alias != "client-gpt" {
		t.Fatalf("alias = %q, want %q", record.Alias, "client-gpt")
	}
}

func TestUsageReporterBuildAdditionalModelRecordSkipsZeroTokens(t *testing.T) {
	reporter := &UsageReporter{
		provider:    "codex",
		model:       "gpt-5.4",
		requestedAt: time.Now(),
	}

	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{}); ok {
		t.Fatalf("expected all-zero token usage to be skipped")
	}
	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{InputTokens: 2}); !ok {
		t.Fatalf("expected non-zero input token usage to be recorded")
	}
	if _, ok := reporter.buildAdditionalModelRecord("gpt-image-2", usage.Detail{CachedTokens: 2}); !ok {
		t.Fatalf("expected non-zero cached token usage to be recorded")
	}
}
