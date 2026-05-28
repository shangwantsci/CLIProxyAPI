package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_PreservesInputTextCacheControl(t *testing.T) {
	inputJSON := `{
		"model": "gpt-4.1",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [
					{"type": "input_text", "text": "Cache this prefix", "cache_control": {"type": "ephemeral"}}
				]
			}
		]
	}`

	result := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if !resultJSON.Get("messages.0.content").IsArray() {
		t.Fatalf("Expected content to remain an array when cache_control is present. Result: %s", string(result))
	}
	if got := resultJSON.Get("messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("Expected input_text cache_control type %q, got %q. Result: %s", "ephemeral", got, string(result))
	}
}
