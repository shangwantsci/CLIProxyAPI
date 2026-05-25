package helps

import (
	"bytes"
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type claudeBillableUsageContextKey struct{}

type claudeBillableUsageState struct {
	Enabled      bool
	Model        string
	SourceFormat string
	Original     []byte
}

// WithClaudeBillableUsage stores the original downstream request used to compute
// customer-facing usage. The upstream raw usage remains recorded separately.
func WithClaudeBillableUsage(ctx context.Context, enabled bool, model, sourceFormat string, original []byte) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	state := claudeBillableUsageState{
		Enabled:      enabled,
		Model:        strings.TrimSpace(model),
		SourceFormat: strings.TrimSpace(sourceFormat),
	}
	if len(original) > 0 {
		state.Original = bytes.Clone(original)
	}
	return context.WithValue(ctx, claudeBillableUsageContextKey{}, state)
}

func claudeBillableUsageStateFromContext(ctx context.Context) (claudeBillableUsageState, bool) {
	if ctx == nil {
		return claudeBillableUsageState{}, false
	}
	state, ok := ctx.Value(claudeBillableUsageContextKey{}).(claudeBillableUsageState)
	return state, ok
}

// EstimateClaudeBillableInputTokens estimates only user-supplied request
// material, excluding Claude Code wrapper/system/billing blocks injected later.
func EstimateClaudeBillableInputTokens(model, sourceFormat string, payload []byte) int64 {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return 0
	}
	enc, err := TokenizerForModel(model)
	if err != nil || enc == nil {
		return 0
	}

	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)
	collectClaudeBillableSystem(root.Get("system"), &segments)
	collectOpenAIMessages(root.Get("messages"), &segments)
	collectOpenAITools(root.Get("tools"), &segments)
	collectOpenAIFunctions(root.Get("functions"), &segments)
	collectOpenAIToolChoice(root.Get("tool_choice"), &segments)
	collectOpenAIResponseFormat(root.Get("response_format"), &segments)
	collectOpenAIContent(root.Get("input"), &segments)
	addIfNotEmpty(&segments, root.Get("instructions").String())
	addIfNotEmpty(&segments, root.Get("prompt").String())

	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return 0
	}
	count, err := enc.Count(joined)
	if err != nil {
		return 0
	}
	if count <= 0 {
		return 0
	}
	return int64(count)
}

func ClaudeBillableInputTokens(ctx context.Context, fallbackModel, fallbackSourceFormat string, fallbackOriginal []byte) (int64, bool) {
	state, hasState := claudeBillableUsageStateFromContext(ctx)
	if hasState && !state.Enabled {
		return 0, false
	}

	model := strings.TrimSpace(fallbackModel)
	sourceFormat := strings.TrimSpace(fallbackSourceFormat)
	original := fallbackOriginal
	if hasState {
		if state.Model != "" {
			model = state.Model
		}
		if state.SourceFormat != "" {
			sourceFormat = state.SourceFormat
		}
		if len(original) == 0 && len(state.Original) > 0 {
			original = state.Original
		}
	}
	count := EstimateClaudeBillableInputTokens(model, sourceFormat, original)
	return count, count > 0
}

func RewriteClaudeUsageForBillable(ctx context.Context, payload []byte) []byte {
	if !gjson.ValidBytes(payload) {
		return payload
	}
	count, ok := ClaudeBillableInputTokens(ctx, gjson.GetBytes(payload, "model").String(), "claude", nil)
	if !ok {
		return payload
	}
	out := payload
	rewrote := false
	for _, path := range []string{"usage", "message.usage"} {
		if !gjson.GetBytes(out, path).Exists() {
			continue
		}
		var ok bool
		out, ok = rewriteClaudeUsageAtPathForBillable(out, path, count)
		rewrote = rewrote || ok
	}
	if !rewrote {
		return payload
	}
	return out
}

func rewriteClaudeUsageAtPathForBillable(payload []byte, path string, count int64) ([]byte, bool) {
	usageNode := gjson.GetBytes(payload, path)
	if !usageNode.Exists() {
		return payload, false
	}
	out := payload
	changed := false
	if usageNode.Get("input_tokens").Exists() {
		var err error
		out, err = sjson.SetBytes(out, path+".input_tokens", count)
		if err != nil {
			return payload, false
		}
		changed = true
	}
	if usageNode.Get("cache_creation_input_tokens").Exists() {
		out, _ = sjson.SetBytes(out, path+".cache_creation_input_tokens", 0)
		changed = true
	}
	if usageNode.Get("cache_read_input_tokens").Exists() {
		out, _ = sjson.SetBytes(out, path+".cache_read_input_tokens", 0)
		changed = true
	}
	if usageNode.Get("cached_tokens").Exists() {
		out, _ = sjson.SetBytes(out, path+".cached_tokens", 0)
		changed = true
	}
	if gjson.GetBytes(out, path+".cache_creation").Exists() {
		out, _ = sjson.SetBytes(out, path+".cache_creation.ephemeral_5m_input_tokens", 0)
		out, _ = sjson.SetBytes(out, path+".cache_creation.ephemeral_1h_input_tokens", 0)
		changed = true
	}
	return out, changed
}

func RewriteClaudeStreamUsageForBillable(ctx context.Context, line []byte) []byte {
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}
	updated := RewriteClaudeUsageForBillable(ctx, payload)
	if bytes.Equal(updated, payload) {
		return line
	}
	if bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:")) {
		return append([]byte("data: "), updated...)
	}
	return updated
}

func BuildClaudeTokenCountJSON(count int64) []byte {
	if count < 0 {
		count = 0
	}
	out := []byte(`{"input_tokens":0}`)
	out, _ = sjson.SetBytes(out, "input_tokens", count)
	return out
}

func collectClaudeBillableSystem(system gjson.Result, segments *[]string) {
	if !system.Exists() {
		return
	}
	if system.Type == gjson.String {
		addIfNotEmpty(segments, system.String())
		return
	}
	if system.IsArray() {
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "text" {
				addIfNotEmpty(segments, item.Get("text").String())
				return true
			}
			if item.Type == gjson.JSON {
				addIfNotEmpty(segments, item.Raw)
			}
			return true
		})
		return
	}
	if system.Type == gjson.JSON {
		addIfNotEmpty(segments, system.Raw)
	}
}
