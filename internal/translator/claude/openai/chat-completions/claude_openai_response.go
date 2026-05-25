// Package openai provides response translation functionality for Claude Code to OpenAI API compatibility.
// This package handles the conversion of Claude Code API responses into OpenAI Chat Completions-compatible
// JSON format, transforming streaming events and non-streaming responses into the format
// expected by OpenAI API clients. It supports both streaming and non-streaming modes,
// handling text content, tool calls, reasoning content, and usage metadata appropriately.
package chat_completions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	dataTag = []byte("data:")
)

// ConvertAnthropicResponseToOpenAIParams holds parameters for response conversion
type ConvertAnthropicResponseToOpenAIParams struct {
	CreatedAt    int64
	ResponseID   string
	FinishReason string
	Usage        claudeUsageTokens
	StopFilter   *openAIStopFilter
	// Tool calls accumulator for streaming
	ToolCallsAccumulator map[int]*ToolCallAccumulator
}

type claudeUsageTokens struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	HasUsage                 bool
}

// ToolCallAccumulator holds the state for accumulating tool call data
type ToolCallAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

func (u *claudeUsageTokens) Merge(usage gjson.Result) {
	if !usage.Exists() {
		return
	}
	u.HasUsage = true
	if inputTokens := usage.Get("input_tokens"); inputTokens.Exists() {
		u.InputTokens = inputTokens.Int()
	}
	if outputTokens := usage.Get("output_tokens"); outputTokens.Exists() {
		u.OutputTokens = outputTokens.Int()
	}
	if cacheCreationInputTokens := usage.Get("cache_creation_input_tokens"); cacheCreationInputTokens.Exists() {
		u.CacheCreationInputTokens = cacheCreationInputTokens.Int()
	} else if cacheCreation := usage.Get("cache_creation"); cacheCreation.Exists() {
		u.CacheCreationInputTokens = cacheCreation.Get("ephemeral_5m_input_tokens").Int() +
			cacheCreation.Get("ephemeral_1h_input_tokens").Int()
	}
	if cacheReadInputTokens := usage.Get("cache_read_input_tokens"); cacheReadInputTokens.Exists() {
		u.CacheReadInputTokens = cacheReadInputTokens.Int()
	} else if cachedTokens := usage.Get("cached_tokens"); cachedTokens.Exists() {
		u.CacheReadInputTokens = cachedTokens.Int()
	}
}

func (u claudeUsageTokens) OpenAIUsage() (promptTokens, completionTokens, totalTokens, cachedTokens int64) {
	cachedTokens = u.CacheReadInputTokens
	promptTokens = u.InputTokens + u.CacheCreationInputTokens + cachedTokens
	completionTokens = u.OutputTokens
	totalTokens = promptTokens + completionTokens
	return promptTokens, completionTokens, totalTokens, cachedTokens
}

func openAIUsageWithBillableInput(ctx context.Context, modelName string, originalRequestRawJSON []byte, usage claudeUsageTokens) (promptTokens, completionTokens, totalTokens, cachedTokens int64) {
	promptTokens, completionTokens, totalTokens, cachedTokens = usage.OpenAIUsage()
	if billableInput, ok := helps.ClaudeBillableInputTokens(ctx, modelName, "openai", originalRequestRawJSON); ok {
		promptTokens = billableInput
		cachedTokens = 0
		totalTokens = promptTokens + completionTokens
	}
	return promptTokens, completionTokens, totalTokens, cachedTokens
}

// ConvertClaudeResponseToOpenAI converts Claude Code streaming response format to OpenAI Chat Completions format.
// This function processes various Claude Code event types and transforms them into OpenAI-compatible JSON responses.
// It handles text content, tool calls, reasoning content, and usage metadata, outputting responses that match
// the OpenAI API format. The function supports incremental updates for streaming responses.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for maintaining state between calls
//
// Returns:
//   - [][]byte: A slice of OpenAI-compatible JSON responses
func ConvertClaudeResponseToOpenAI(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
	if *param == nil {
		*param = &ConvertAnthropicResponseToOpenAIParams{
			CreatedAt:    0,
			ResponseID:   "",
			FinishReason: "",
		}
	}

	if !bytes.HasPrefix(rawJSON, dataTag) {
		return [][]byte{}
	}
	rawJSON = bytes.TrimSpace(rawJSON[5:])

	root := gjson.ParseBytes(rawJSON)
	eventType := root.Get("type").String()

	// Base OpenAI streaming response template
	template := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)

	// Set model
	if modelName != "" {
		template, _ = sjson.SetBytes(template, "model", modelName)
	}

	// Set response ID and creation time
	if (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID != "" {
		template, _ = sjson.SetBytes(template, "id", (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID)
	}
	if (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt > 0 {
		template, _ = sjson.SetBytes(template, "created", (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt)
	}

	switch eventType {
	case "message_start":
		// Initialize response with message metadata when a new message begins
		if message := root.Get("message"); message.Exists() {
			(*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID = message.Get("id").String()
			(*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt = time.Now().Unix()

			template, _ = sjson.SetBytes(template, "id", (*param).(*ConvertAnthropicResponseToOpenAIParams).ResponseID)
			template, _ = sjson.SetBytes(template, "model", modelName)
			template, _ = sjson.SetBytes(template, "created", (*param).(*ConvertAnthropicResponseToOpenAIParams).CreatedAt)

			// Set initial role to assistant for the response
			template, _ = sjson.SetBytes(template, "choices.0.delta.role", "assistant")

			// Initialize tool calls accumulator for tracking tool call progress
			if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator == nil {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
			}
			(*param).(*ConvertAnthropicResponseToOpenAIParams).Usage.Merge(message.Get("usage"))
		}
		return [][]byte{template}

	case "content_block_start":
		// Start of a content block (text, tool use, or reasoning)
		if contentBlock := root.Get("content_block"); contentBlock.Exists() {
			blockType := contentBlock.Get("type").String()

			if blockType == "tool_use" {
				// Start of tool call - initialize accumulator to track arguments
				toolCallID := contentBlock.Get("id").String()
				toolName := contentBlock.Get("name").String()
				index := int(root.Get("index").Int())

				if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator == nil {
					(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator = make(map[int]*ToolCallAccumulator)
				}

				(*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index] = &ToolCallAccumulator{
					ID:   toolCallID,
					Name: toolName,
				}

				// Don't output anything yet - wait for complete tool call
				return [][]byte{}
			}
		}
		return [][]byte{}

	case "content_block_delta":
		// Handle content delta (text, tool use arguments, or reasoning content)
		hasContent := false
		if delta := root.Get("delta"); delta.Exists() {
			deltaType := delta.Get("type").String()

			switch deltaType {
			case "text_delta":
				// Text content delta - send incremental text updates
				if text := delta.Get("text"); text.Exists() {
					filtered, emit := filterOpenAIStreamStopDelta(
						(*param).(*ConvertAnthropicResponseToOpenAIParams),
						originalRequestRawJSON,
						text.String(),
					)
					if emit {
						template, _ = sjson.SetBytes(template, "choices.0.delta.content", filtered)
						hasContent = true
					}
				}
			case "thinking_delta":
				// Accumulate reasoning/thinking content
				if thinking := delta.Get("thinking"); thinking.Exists() {
					template, _ = sjson.SetBytes(template, "choices.0.delta.reasoning_content", thinking.String())
					hasContent = true
				}
			case "input_json_delta":
				// Tool use input delta - accumulate arguments for tool calls
				if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
					index := int(root.Get("index").Int())
					if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator != nil {
						if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index]; exists {
							accumulator.Arguments.WriteString(partialJSON.String())
						}
					}
				}
				// Don't output anything yet - wait for complete tool call
				return [][]byte{}
			}
		}
		if hasContent {
			return [][]byte{template}
		} else {
			return [][]byte{}
		}

	case "content_block_stop":
		// End of content block - output complete tool call if it's a tool_use block
		index := int(root.Get("index").Int())
		if (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator != nil {
			if accumulator, exists := (*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator[index]; exists {
				// Build complete tool call with accumulated arguments
				arguments := repairOpenAIToolArguments(accumulator.Arguments.String())
				if arguments == "" {
					arguments = "{}"
				}
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.index", index)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.id", accumulator.ID)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.type", "function")
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.function.name", accumulator.Name)
				template, _ = sjson.SetBytes(template, "choices.0.delta.tool_calls.0.function.arguments", arguments)

				// Clean up the accumulator for this index
				delete((*param).(*ConvertAnthropicResponseToOpenAIParams).ToolCallsAccumulator, index)

				return [][]byte{template}
			}
		}
		return [][]byte{}

	case "message_delta":
		// Handle message-level changes including stop reason and usage
		if delta := root.Get("delta"); delta.Exists() {
			if stopReason := delta.Get("stop_reason"); stopReason.Exists() {
				(*param).(*ConvertAnthropicResponseToOpenAIParams).FinishReason = mapAnthropicStopReasonToOpenAI(stopReason.String())
				template, _ = sjson.SetBytes(template, "choices.0.finish_reason", (*param).(*ConvertAnthropicResponseToOpenAIParams).FinishReason)
			}
		}

		// Handle usage information for token counts
		if usage := root.Get("usage"); usage.Exists() {
			(*param).(*ConvertAnthropicResponseToOpenAIParams).Usage.Merge(usage)
			promptTokens, completionTokens, totalTokens, cachedTokens := openAIUsageWithBillableInput(
				ctx,
				modelName,
				originalRequestRawJSON,
				(*param).(*ConvertAnthropicResponseToOpenAIParams).Usage,
			)
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens", promptTokens)
			template, _ = sjson.SetBytes(template, "usage.completion_tokens", completionTokens)
			template, _ = sjson.SetBytes(template, "usage.total_tokens", totalTokens)
			template, _ = sjson.SetBytes(template, "usage.prompt_tokens_details.cached_tokens", cachedTokens)
		}
		if pending, ok := flushOpenAIStreamStopFilter((*param).(*ConvertAnthropicResponseToOpenAIParams)); ok {
			contentChunk := template
			contentChunk, _ = sjson.SetBytes(contentChunk, "choices.0.delta.content", pending)
			contentChunk, _ = sjson.SetBytes(contentChunk, "choices.0.finish_reason", nil)
			return [][]byte{contentChunk, template}
		}
		return [][]byte{template}

	case "message_stop":
		// Final message event - no additional output needed
		return [][]byte{}

	case "ping":
		// Ping events for keeping connection alive - no output needed
		return [][]byte{}

	case "error":
		// Error event - format and return error response
		if errorData := root.Get("error"); errorData.Exists() {
			errorJSON := []byte(`{"error":{"message":"","type":""}}`)
			errorJSON, _ = sjson.SetBytes(errorJSON, "error.message", errorData.Get("message").String())
			errorJSON, _ = sjson.SetBytes(errorJSON, "error.type", errorData.Get("type").String())
			return [][]byte{errorJSON}
		}
		return [][]byte{}

	default:
		// Unknown event type - ignore
		return [][]byte{}
	}
}

// mapAnthropicStopReasonToOpenAI maps Anthropic stop reasons to OpenAI stop reasons
func mapAnthropicStopReasonToOpenAI(anthropicReason string) string {
	switch anthropicReason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

// ConvertClaudeResponseToOpenAINonStream converts a non-streaming Claude Code response to a non-streaming OpenAI response.
// This function processes the complete Claude Code response and transforms it into a single OpenAI-compatible
// JSON response. It handles message content, tool calls, reasoning content, and usage metadata, combining all
// the information into a single response that matches the OpenAI API format.
//
// Parameters:
//   - ctx: The context for the request, used for cancellation and timeout handling
//   - modelName: The name of the model being used for the response (unused in current implementation)
//   - rawJSON: The raw JSON response from the Claude Code API
//   - param: A pointer to a parameter object for the conversion (unused in current implementation)
//
// Returns:
//   - []byte: An OpenAI-compatible JSON response containing all message content and metadata
func ConvertClaudeResponseToOpenAINonStream(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) []byte {
	chunks := make([][]byte, 0)

	lines := bytes.Split(rawJSON, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		chunks = append(chunks, bytes.TrimSpace(line[5:]))
	}

	// Base OpenAI non-streaming response template
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)

	var messageID string
	var model string
	var createdAt int64
	var stopReason string
	var contentParts []string
	var reasoningParts []string
	usageTokens := claudeUsageTokens{}
	toolCallsAccumulator := make(map[int]*ToolCallAccumulator)

	for _, chunk := range chunks {
		root := gjson.ParseBytes(chunk)
		eventType := root.Get("type").String()

		switch eventType {
		case "message_start":
			// Extract initial message metadata including ID, model, and input token count
			if message := root.Get("message"); message.Exists() {
				messageID = message.Get("id").String()
				model = message.Get("model").String()
				createdAt = time.Now().Unix()
				usageTokens.Merge(message.Get("usage"))
			}

		case "content_block_start":
			// Handle different content block types at the beginning
			if contentBlock := root.Get("content_block"); contentBlock.Exists() {
				blockType := contentBlock.Get("type").String()
				if blockType == "thinking" {
					// Start of thinking/reasoning content - skip for now as it's handled in delta
					continue
				} else if blockType == "tool_use" {
					// Initialize tool call accumulator for this index
					index := int(root.Get("index").Int())
					toolCallsAccumulator[index] = &ToolCallAccumulator{
						ID:   contentBlock.Get("id").String(),
						Name: contentBlock.Get("name").String(),
					}
				}
			}

		case "content_block_delta":
			// Process incremental content updates
			if delta := root.Get("delta"); delta.Exists() {
				deltaType := delta.Get("type").String()
				switch deltaType {
				case "text_delta":
					// Accumulate text content
					if text := delta.Get("text"); text.Exists() {
						contentParts = append(contentParts, text.String())
					}
				case "thinking_delta":
					// Accumulate reasoning/thinking content
					if thinking := delta.Get("thinking"); thinking.Exists() {
						reasoningParts = append(reasoningParts, thinking.String())
					}
				case "input_json_delta":
					// Accumulate tool call arguments
					if partialJSON := delta.Get("partial_json"); partialJSON.Exists() {
						index := int(root.Get("index").Int())
						if accumulator, exists := toolCallsAccumulator[index]; exists {
							accumulator.Arguments.WriteString(partialJSON.String())
						}
					}
				}
			}

		case "content_block_stop":
			// Finalize tool call arguments for this index when content block ends
			index := int(root.Get("index").Int())
			if accumulator, exists := toolCallsAccumulator[index]; exists {
				if accumulator.Arguments.Len() == 0 {
					accumulator.Arguments.WriteString("{}")
				}
			}

		case "message_delta":
			// Extract stop reason and output token count when message ends
			if delta := root.Get("delta"); delta.Exists() {
				if sr := delta.Get("stop_reason"); sr.Exists() {
					stopReason = sr.String()
				}
			}
			if usage := root.Get("usage"); usage.Exists() {
				usageTokens.Merge(usage)
			}
		}
	}

	if usageTokens.HasUsage {
		if modelName == "" {
			modelName = model
		}
		promptTokens, completionTokens, totalTokens, cachedTokens := openAIUsageWithBillableInput(ctx, modelName, originalRequestRawJSON, usageTokens)
		out, _ = sjson.SetBytes(out, "usage.prompt_tokens", promptTokens)
		out, _ = sjson.SetBytes(out, "usage.completion_tokens", completionTokens)
		out, _ = sjson.SetBytes(out, "usage.total_tokens", totalTokens)
		out, _ = sjson.SetBytes(out, "usage.prompt_tokens_details.cached_tokens", cachedTokens)
	}

	// Set basic response fields including message ID, creation time, and model
	out, _ = sjson.SetBytes(out, "id", messageID)
	out, _ = sjson.SetBytes(out, "created", createdAt)
	out, _ = sjson.SetBytes(out, "model", model)

	// Set message content by combining all text parts
	messageContent := strings.Join(contentParts, "")
	messageContent, _ = applyOpenAIStopSequences(originalRequestRawJSON, messageContent)
	out, _ = sjson.SetBytes(out, "choices.0.message.content", messageContent)

	// Add reasoning content if available (following OpenAI reasoning format)
	if len(reasoningParts) > 0 {
		reasoningContent := strings.Join(reasoningParts, "")
		// Add reasoning as a separate field in the message
		out, _ = sjson.SetBytes(out, "choices.0.message.reasoning", reasoningContent)
	}

	// Set tool calls if any were accumulated during processing
	if len(toolCallsAccumulator) > 0 {
		toolCallsCount := 0
		maxIndex := -1
		for index := range toolCallsAccumulator {
			if index > maxIndex {
				maxIndex = index
			}
		}

		for i := 0; i <= maxIndex; i++ {
			accumulator, exists := toolCallsAccumulator[i]
			if !exists {
				continue
			}

			arguments := repairOpenAIToolArguments(accumulator.Arguments.String())

			idPath := fmt.Sprintf("choices.0.message.tool_calls.%d.id", toolCallsCount)
			typePath := fmt.Sprintf("choices.0.message.tool_calls.%d.type", toolCallsCount)
			namePath := fmt.Sprintf("choices.0.message.tool_calls.%d.function.name", toolCallsCount)
			argumentsPath := fmt.Sprintf("choices.0.message.tool_calls.%d.function.arguments", toolCallsCount)

			out, _ = sjson.SetBytes(out, idPath, accumulator.ID)
			out, _ = sjson.SetBytes(out, typePath, "function")
			out, _ = sjson.SetBytes(out, namePath, accumulator.Name)
			out, _ = sjson.SetBytes(out, argumentsPath, arguments)
			toolCallsCount++
		}
		if toolCallsCount > 0 {
			out, _ = sjson.SetBytes(out, "choices.0.finish_reason", "tool_calls")
		} else {
			out, _ = sjson.SetBytes(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
		}
	} else {
		out, _ = sjson.SetBytes(out, "choices.0.finish_reason", mapAnthropicStopReasonToOpenAI(stopReason))
	}

	return out
}

func openAIStopSequences(originalRequestRawJSON []byte) []string {
	stop := gjson.GetBytes(originalRequestRawJSON, "stop")
	if !stop.Exists() {
		return nil
	}
	var out []string
	if stop.IsArray() {
		stop.ForEach(func(_, item gjson.Result) bool {
			if s := item.String(); s != "" {
				out = append(out, s)
			}
			return true
		})
		return out
	}
	if s := stop.String(); s != "" {
		out = append(out, s)
	}
	return out
}

func applyOpenAIStopSequences(originalRequestRawJSON []byte, text string) (string, bool) {
	stops := openAIStopSequences(originalRequestRawJSON)
	if len(stops) == 0 || text == "" {
		return text, false
	}
	cut := -1
	for _, stop := range stops {
		if stop == "" {
			continue
		}
		if idx := strings.Index(text, stop); idx >= 0 && (cut < 0 || idx < cut) {
			cut = idx
		}
	}
	if cut < 0 {
		return text, false
	}
	return text[:cut], true
}

type openAIStopFilter struct {
	Stops      []string
	MaxLen     int
	Pending    string
	Terminated bool
}

func newOpenAIStopFilter(originalRequestRawJSON []byte) *openAIStopFilter {
	stops := openAIStopSequences(originalRequestRawJSON)
	if len(stops) == 0 {
		return nil
	}
	maxLen := 0
	for _, stop := range stops {
		if len(stop) > maxLen {
			maxLen = len(stop)
		}
	}
	if maxLen <= 0 {
		return nil
	}
	return &openAIStopFilter{Stops: stops, MaxLen: maxLen}
}

func filterOpenAIStreamStopDelta(state *ConvertAnthropicResponseToOpenAIParams, originalRequestRawJSON []byte, delta string) (string, bool) {
	if state == nil {
		return delta, delta != ""
	}
	if state.StopFilter == nil {
		state.StopFilter = newOpenAIStopFilter(originalRequestRawJSON)
	}
	filter := state.StopFilter
	if filter == nil {
		return delta, delta != ""
	}
	if filter.Terminated {
		return "", false
	}
	combined := filter.Pending + delta
	if cutText, stopped := applyOpenAIStopSequences(originalRequestRawJSON, combined); stopped {
		filter.Pending = ""
		filter.Terminated = true
		return cutText, cutText != ""
	}
	hold := filter.MaxLen - 1
	if hold <= 0 || len(combined) <= hold {
		filter.Pending = combined
		return "", false
	}
	emitLen := len(combined) - hold
	filter.Pending = combined[emitLen:]
	return combined[:emitLen], emitLen > 0
}

func flushOpenAIStreamStopFilter(state *ConvertAnthropicResponseToOpenAIParams) (string, bool) {
	if state == nil || state.StopFilter == nil || state.StopFilter.Terminated || state.StopFilter.Pending == "" {
		return "", false
	}
	out := state.StopFilter.Pending
	state.StopFilter.Pending = ""
	return out, true
}

func repairOpenAIToolArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}
	if json.Valid([]byte(trimmed)) && gjson.Parse(trimmed).IsObject() {
		return compactJSONObject(trimmed)
	}

	decoder := json.NewDecoder(strings.NewReader(trimmed))
	var last map[string]any
	for {
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			break
		}
		if value != nil {
			last = value
		}
	}
	if last != nil {
		if b, err := json.Marshal(last); err == nil {
			return string(b)
		}
	}

	for i := len(trimmed) - 1; i >= 0; i-- {
		if trimmed[i] != '{' {
			continue
		}
		candidate := trimmed[i:]
		if json.Valid([]byte(candidate)) && gjson.Parse(candidate).IsObject() {
			return compactJSONObject(candidate)
		}
	}
	return "{}"
}

func compactJSONObject(raw string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
		return "{}"
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return "{}"
	}
	return string(b)
}
