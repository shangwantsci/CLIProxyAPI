package contentguard

import (
	"strings"

	"github.com/tidwall/gjson"
)

const (
	// MaxScanBytes caps the extracted latest-turn text, not the raw request body.
	MaxScanBytes = 64 << 10

	SourceFormatAlphaSearch = "codex-alpha-search"
	SourceFormatOpenAIImage = "openai-image"
	SourceFormatOpenAIVideo = "openai-video"
)

// ExtractLatestUserText returns the latest user turn from a request payload.
// Parse failures and non-text payloads return an empty string (fail-open).
func ExtractLatestUserText(sourceFormat string, payload []byte) string {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	root := gjson.ParseBytes(payload)
	text := extractByFormat(strings.ToLower(strings.TrimSpace(sourceFormat)), root)
	if strings.TrimSpace(text) == "" {
		text = extractFallback(root)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) > MaxScanBytes {
		return text[:MaxScanBytes]
	}
	return text
}

func extractByFormat(sourceFormat string, root gjson.Result) string {
	switch sourceFormat {
	case "openai", "openai-chat":
		if messages := root.Get("messages"); messages.IsArray() {
			return lastRoleText(messages, "user")
		}
		return firstString(root, "prompt")
	case "openai-response", "codex":
		return extractResponsesInput(root.Get("input"))
	case "claude":
		if messages := root.Get("messages"); messages.IsArray() {
			return lastRoleText(messages, "user")
		}
	case "gemini", "gemini-cli", "gemini-interactions", "interactions", "antigravity":
		if contents := root.Get("contents"); contents.IsArray() {
			return lastGeminiUserText(contents)
		}
	case SourceFormatOpenAIImage, SourceFormatOpenAIVideo:
		return firstString(root, "prompt")
	case SourceFormatAlphaSearch:
		if query := firstString(root, "query"); query != "" {
			return query
		}
		if prompt := firstString(root, "prompt"); prompt != "" {
			return prompt
		}
		if messages := root.Get("messages"); messages.IsArray() {
			return lastRoleText(messages, "user")
		}
	}
	return ""
}

func extractFallback(root gjson.Result) string {
	if messages := root.Get("messages"); messages.IsArray() {
		if text := lastRoleText(messages, "user"); text != "" {
			return text
		}
	}
	if input := root.Get("input"); input.Exists() {
		if text := extractResponsesInput(input); text != "" {
			return text
		}
	}
	if contents := root.Get("contents"); contents.IsArray() {
		if text := lastGeminiUserText(contents); text != "" {
			return text
		}
	}
	if prompt := firstString(root, "prompt"); prompt != "" {
		return prompt
	}
	return firstString(root, "query")
}

func extractResponsesInput(input gjson.Result) string {
	if !input.Exists() {
		return ""
	}
	if input.Type == gjson.String {
		return input.String()
	}
	if !input.IsArray() {
		return extractTextValue(input)
	}
	items := input.Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		typ := strings.ToLower(strings.TrimSpace(item.Get("type").String()))
		if role != "user" && typ != "input_text" && !(typ == "message" && role == "user") {
			if role != "" || (typ != "" && typ != "message") {
				continue
			}
		}
		if text := extractItemText(item); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func lastRoleText(arr gjson.Result, role string) string {
	items := arr.Array()
	want := strings.ToLower(strings.TrimSpace(role))
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if strings.ToLower(strings.TrimSpace(item.Get("role").String())) != want {
			continue
		}
		if text := extractItemText(item); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func lastGeminiUserText(contents gjson.Result) string {
	items := contents.Array()
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		role := strings.ToLower(strings.TrimSpace(item.Get("role").String()))
		if role == "model" || role == "assistant" {
			continue
		}
		if role != "" && role != "user" {
			continue
		}
		if text := extractItemText(item); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func extractItemText(item gjson.Result) string {
	if text := extractTextValue(item.Get("content")); strings.TrimSpace(text) != "" {
		return text
	}
	if text := extractTextValue(item.Get("parts")); strings.TrimSpace(text) != "" {
		return text
	}
	return extractPartText(item)
}

func extractTextValue(value gjson.Result) string {
	if !value.Exists() {
		return ""
	}
	if value.Type == gjson.String {
		return value.String()
	}
	if value.IsArray() {
		var builder strings.Builder
		value.ForEach(func(_, item gjson.Result) bool {
			piece := extractPartText(item)
			if strings.TrimSpace(piece) == "" {
				return true
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			builder.WriteString(piece)
			return true
		})
		return builder.String()
	}
	if value.IsObject() {
		return extractPartText(value)
	}
	return ""
}

func extractPartText(item gjson.Result) string {
	if !item.Exists() {
		return ""
	}
	if item.Type == gjson.String {
		return item.String()
	}
	for _, key := range []string{"text", "input_text", "prompt"} {
		if field := item.Get(key); field.Exists() && field.Type == gjson.String {
			return field.String()
		}
	}
	return ""
}

func firstString(root gjson.Result, key string) string {
	field := root.Get(key)
	if field.Exists() && field.Type == gjson.String {
		return field.String()
	}
	return ""
}
