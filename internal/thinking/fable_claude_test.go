package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_ClaudeFableNoneOmitsUnsupportedDisabled(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5","thinking":{"type":"disabled","budget_tokens":1024},"output_config":{"effort":"high","keep":true}}`)

	out, err := thinking.ApplyThinking(body, "claude-fable-5", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("thinking should be omitted for claude-fable-5 none mode, body=%s", string(out))
	}
	if gjson.GetBytes(out, "output_config.effort").Exists() {
		t.Fatalf("output_config.effort should be removed for claude-fable-5 none mode, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "output_config.keep").Bool(); !got {
		t.Fatalf("output_config.keep should be preserved, body=%s", string(out))
	}
}

func TestApplyThinking_ClaudeFableBudgetConvertsToAdaptiveEffort(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5","thinking":{"type":"enabled","budget_tokens":9000}}`)

	out, err := thinking.ApplyThinking(body, "claude-fable-5", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive, body=%s", got, string(out))
	}
	if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
		t.Fatalf("thinking.budget_tokens should be removed for claude-fable-5, body=%s", string(out))
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
		t.Fatalf("output_config.effort = %q, want high, body=%s", got, string(out))
	}
}

func TestApplyThinking_ClaudeFableAutoUsesClaudeCodeDefaultEffort(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5","thinking":{"type":"enabled"}}`)

	out, err := thinking.ApplyThinking(body, "claude-fable-5", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive, body=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
		t.Fatalf("output_config.effort = %q, want high, body=%s", got, string(out))
	}
}
