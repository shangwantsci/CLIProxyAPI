package synthesizer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/geminicli"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// FileSynthesizer generates Auth entries from OAuth JSON files.
// It handles file-based authentication and Gemini virtual auth generation.
type FileSynthesizer struct{}

// NewFileSynthesizer creates a new FileSynthesizer instance.
func NewFileSynthesizer() *FileSynthesizer {
	return &FileSynthesizer{}
}

// Synthesize generates Auth entries from auth files in the auth directory.
func (s *FileSynthesizer) Synthesize(ctx *SynthesisContext) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, 16)
	if ctx == nil || ctx.AuthDir == "" {
		return out, nil
	}

	entries, err := os.ReadDir(ctx.AuthDir)
	if err != nil {
		// Not an error if directory doesn't exist
		return out, nil
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(ctx.AuthDir, name)
		data, errRead := os.ReadFile(full)
		if errRead != nil || len(data) == 0 {
			continue
		}
		auths := synthesizeFileAuths(ctx, full, data)
		if len(auths) == 0 {
			continue
		}
		out = append(out, auths...)
	}
	return out, nil
}

// SynthesizeAuthFile generates Auth entries for one auth JSON file payload.
// It shares exactly the same mapping behavior as FileSynthesizer.Synthesize.
func SynthesizeAuthFile(ctx *SynthesisContext, fullPath string, data []byte) []*coreauth.Auth {
	return synthesizeFileAuths(ctx, fullPath, data)
}

func synthesizeFileAuths(ctx *SynthesisContext, fullPath string, data []byte) []*coreauth.Auth {
	if ctx == nil || len(data) == 0 {
		return nil
	}
	now := ctx.Now
	cfg := ctx.Config
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return nil
	}
	t, _ := metadata["type"].(string)
	if t == "" {
		return nil
	}
	provider := strings.ToLower(t)
	if provider == "gemini" {
		provider = "gemini-cli"
	}
	label := provider
	if email, _ := metadata["email"].(string); email != "" {
		label = email
	}
	// Use relative path under authDir as ID to stay consistent with the file-based token store.
	id := fullPath
	if strings.TrimSpace(ctx.AuthDir) != "" {
		if rel, errRel := filepath.Rel(ctx.AuthDir, fullPath); errRel == nil && rel != "" {
			id = rel
		}
	}
	if runtime.GOOS == "windows" {
		id = strings.ToLower(id)
	}

	proxyURL := ""
	if p, ok := metadata["proxy_url"].(string); ok {
		proxyURL = p
	}

	prefix := ""
	if rawPrefix, ok := metadata["prefix"].(string); ok {
		trimmed := strings.TrimSpace(rawPrefix)
		trimmed = strings.Trim(trimmed, "/")
		if trimmed != "" && !strings.Contains(trimmed, "/") {
			prefix = trimmed
		}
	}

	disabled, _ := metadata["disabled"].(bool)
	status := coreauth.StatusActive
	if disabled {
		status = coreauth.StatusDisabled
	}

	// Read per-account excluded models from the OAuth JSON file.
	perAccountExcluded := extractExcludedModelsFromMetadata(metadata)

	a := &coreauth.Auth{
		ID:       id,
		Provider: provider,
		Label:    label,
		Prefix:   prefix,
		Status:   status,
		Disabled: disabled,
		Attributes: map[string]string{
			"source": fullPath,
			"path":   fullPath,
		},
		ProxyURL:  proxyURL,
		Metadata:  metadata,
		CreatedAt: now,
		UpdatedAt: now,
	}
	applyClaudeRuntimeMetadata(a, metadata)
	// Read priority from auth file.
	if rawPriority, ok := metadata["priority"]; ok {
		switch v := rawPriority.(type) {
		case float64:
			a.Attributes["priority"] = strconv.Itoa(int(v))
		case string:
			priority := strings.TrimSpace(v)
			if _, errAtoi := strconv.Atoi(priority); errAtoi == nil {
				a.Attributes["priority"] = priority
			}
		}
	}
	// Read note from auth file.
	if rawNote, ok := metadata["note"]; ok {
		if note, isStr := rawNote.(string); isStr {
			if trimmed := strings.TrimSpace(note); trimmed != "" {
				a.Attributes["note"] = trimmed
			}
		}
	}
	applyClaudeCloakMetadata(a, metadata)
	coreauth.ApplyCustomHeadersFromMetadata(a)
	ApplyAuthExcludedModelsMeta(a, cfg, perAccountExcluded, "oauth")
	// For codex auth files, extract plan_type from the JWT id_token.
	if provider == "codex" {
		if idTokenRaw, ok := metadata["id_token"].(string); ok && strings.TrimSpace(idTokenRaw) != "" {
			if claims, errParse := codex.ParseJWTToken(idTokenRaw); errParse == nil && claims != nil {
				if pt := strings.TrimSpace(claims.CodexAuthInfo.ChatgptPlanType); pt != "" {
					a.Attributes["plan_type"] = pt
				}
			}
		}
	}
	if provider == "gemini-cli" {
		if virtuals := SynthesizeGeminiVirtualAuths(a, metadata, now); len(virtuals) > 0 {
			for _, v := range virtuals {
				ApplyAuthExcludedModelsMeta(v, cfg, perAccountExcluded, "oauth")
			}
			out := make([]*coreauth.Auth, 0, 1+len(virtuals))
			out = append(out, a)
			out = append(out, virtuals...)
			return out
		}
	}
	return []*coreauth.Auth{a}
}

func applyClaudeRuntimeMetadata(auth *coreauth.Auth, metadata map[string]any) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return
	}
	auth.Status = fileStatusFromMetadata(metadata, auth.Disabled)
	if statusMessage, ok := metadata["status_message"].(string); ok {
		auth.StatusMessage = strings.TrimSpace(statusMessage)
	}
	if unavailable, ok := metadata["unavailable"].(bool); ok {
		auth.Unavailable = unavailable
	}
	if nextRetryAfter, ok := fileMetadataTimeValue(metadata["next_retry_after"]); ok {
		auth.NextRetryAfter = nextRetryAfter
	}
	if nextRefreshAfter, ok := fileMetadataTimeValue(metadata["next_refresh_after"]); ok {
		auth.NextRefreshAfter = nextRefreshAfter
	}
	auth.Quota = fileQuotaStateFromMetadata(metadata["quota"])
	auth.LastError = fileAuthErrorFromMetadata(metadata["last_error"])
	if auth.Disabled {
		auth.Status = coreauth.StatusDisabled
	}
}

func fileStatusFromMetadata(metadata map[string]any, disabled bool) coreauth.Status {
	if disabled {
		return coreauth.StatusDisabled
	}
	raw, _ := metadata["status"].(string)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(coreauth.StatusUnknown):
		return coreauth.StatusUnknown
	case string(coreauth.StatusPending):
		return coreauth.StatusPending
	case string(coreauth.StatusRefreshing):
		return coreauth.StatusRefreshing
	case string(coreauth.StatusError):
		return coreauth.StatusError
	case string(coreauth.StatusDisabled):
		return coreauth.StatusDisabled
	default:
		return coreauth.StatusActive
	}
}

func fileAuthErrorFromMetadata(raw any) *coreauth.Error {
	values, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	err := &coreauth.Error{}
	if code, ok := values["code"].(string); ok {
		err.Code = strings.TrimSpace(code)
	}
	if message, ok := values["message"].(string); ok {
		err.Message = strings.TrimSpace(message)
	}
	if retryable, ok := values["retryable"].(bool); ok {
		err.Retryable = retryable
	}
	if status, ok := fileMetadataIntValue(values["http_status"]); ok {
		err.HTTPStatus = status
	}
	if err.Code == "" && err.Message == "" && err.HTTPStatus == 0 {
		return nil
	}
	return err
}

func fileQuotaStateFromMetadata(raw any) coreauth.QuotaState {
	values, ok := raw.(map[string]any)
	if !ok {
		return coreauth.QuotaState{}
	}
	quota := coreauth.QuotaState{}
	if exceeded, ok := values["exceeded"].(bool); ok {
		quota.Exceeded = exceeded
	}
	if reason, ok := values["reason"].(string); ok {
		quota.Reason = strings.TrimSpace(reason)
	}
	if recoverAt, ok := fileMetadataTimeValue(values["next_recover_at"]); ok {
		quota.NextRecoverAt = recoverAt
	}
	if level, ok := fileMetadataIntValue(values["backoff_level"]); ok {
		quota.BackoffLevel = level
	}
	return quota
}

func fileMetadataTimeValue(raw any) (time.Time, bool) {
	switch value := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04"} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed, true
			}
		}
		if unix, err := strconv.ParseInt(trimmed, 10, 64); err == nil && unix > 0 {
			return fileNormalizeMetadataUnixTime(unix), true
		}
	case float64:
		if value > 0 {
			return fileNormalizeMetadataUnixTime(int64(value)), true
		}
	case int64:
		if value > 0 {
			return fileNormalizeMetadataUnixTime(value), true
		}
	case int:
		if value > 0 {
			return fileNormalizeMetadataUnixTime(int64(value)), true
		}
	case json.Number:
		if unix, err := value.Int64(); err == nil && unix > 0 {
			return fileNormalizeMetadataUnixTime(unix), true
		}
	}
	return time.Time{}, false
}

func fileNormalizeMetadataUnixTime(raw int64) time.Time {
	if raw > 1_000_000_000_000 {
		return time.UnixMilli(raw)
	}
	return time.Unix(raw, 0)
}

func fileMetadataIntValue(raw any) (int, bool) {
	switch value := raw.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func applyClaudeCloakMetadata(auth *coreauth.Auth, metadata map[string]any) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if rawMode, ok := metadata["cloak_mode"].(string); ok {
		mode := strings.ToLower(strings.TrimSpace(rawMode))
		if mode == "auto" || mode == "always" || mode == "never" {
			auth.Attributes["cloak_mode"] = mode
		}
	}
	if rawStrict, ok := metadata["cloak_strict_mode"].(bool); ok {
		auth.Attributes["cloak_strict_mode"] = strconv.FormatBool(rawStrict)
	} else if rawStrict, ok := metadata["cloak_strict_mode"].(string); ok && strings.TrimSpace(rawStrict) != "" {
		auth.Attributes["cloak_strict_mode"] = strconv.FormatBool(strings.EqualFold(strings.TrimSpace(rawStrict), "true") || strings.TrimSpace(rawStrict) == "1")
	}
	if rawCache, ok := metadata["cloak_cache_user_id"].(bool); ok {
		auth.Attributes["cloak_cache_user_id"] = strconv.FormatBool(rawCache)
	} else if rawCache, ok := metadata["cloak_cache_user_id"].(string); ok && strings.TrimSpace(rawCache) != "" {
		auth.Attributes["cloak_cache_user_id"] = strconv.FormatBool(strings.EqualFold(strings.TrimSpace(rawCache), "true") || strings.TrimSpace(rawCache) == "1")
	}
	words := cloakWordsFromMetadata(metadata["cloak_sensitive_words"])
	if len(words) > 0 {
		auth.Attributes["cloak_sensitive_words"] = strings.Join(words, ",")
	}
}

func cloakWordsFromMetadata(raw any) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	add := func(raw string) {
		for _, part := range strings.Split(raw, ",") {
			trimmed := strings.TrimSpace(part)
			if trimmed == "" {
				continue
			}
			key := strings.ToLower(trimmed)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, trimmed)
		}
	}
	switch v := raw.(type) {
	case string:
		add(v)
	case []string:
		for _, item := range v {
			add(item)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	return out
}

// SynthesizeGeminiVirtualAuths creates virtual Auth entries for multi-project Gemini credentials.
// It disables the primary auth and creates one virtual auth per project.
func SynthesizeGeminiVirtualAuths(primary *coreauth.Auth, metadata map[string]any, now time.Time) []*coreauth.Auth {
	if primary == nil || metadata == nil {
		return nil
	}
	projects := splitGeminiProjectIDs(metadata)
	if len(projects) <= 1 {
		return nil
	}
	email, _ := metadata["email"].(string)
	shared := geminicli.NewSharedCredential(primary.ID, email, metadata, projects)
	primary.Disabled = true
	primary.Status = coreauth.StatusDisabled
	primary.Runtime = shared
	if primary.Attributes == nil {
		primary.Attributes = make(map[string]string)
	}
	primary.Attributes["gemini_virtual_primary"] = "true"
	primary.Attributes["virtual_children"] = strings.Join(projects, ",")
	source := primary.Attributes["source"]
	authPath := primary.Attributes["path"]
	originalProvider := primary.Provider
	if originalProvider == "" {
		originalProvider = "gemini-cli"
	}
	label := primary.Label
	if label == "" {
		label = originalProvider
	}
	virtuals := make([]*coreauth.Auth, 0, len(projects))
	for _, projectID := range projects {
		attrs := map[string]string{
			"runtime_only":           "true",
			"gemini_virtual_parent":  primary.ID,
			"gemini_virtual_project": projectID,
		}
		if source != "" {
			attrs["source"] = source
		}
		if authPath != "" {
			attrs["path"] = authPath
		}
		// Propagate priority from primary auth to virtual auths
		if priorityVal, hasPriority := primary.Attributes["priority"]; hasPriority && priorityVal != "" {
			attrs["priority"] = priorityVal
		}
		// Propagate note from primary auth to virtual auths
		if noteVal, hasNote := primary.Attributes["note"]; hasNote && noteVal != "" {
			attrs["note"] = noteVal
		}
		for k, v := range primary.Attributes {
			if strings.HasPrefix(k, "header:") && strings.TrimSpace(v) != "" {
				attrs[k] = v
			}
		}
		metadataCopy := map[string]any{
			"email":             email,
			"project_id":        projectID,
			"virtual":           true,
			"virtual_parent_id": primary.ID,
			"type":              metadata["type"],
		}
		if v, ok := metadata["disable_cooling"]; ok {
			metadataCopy["disable_cooling"] = v
		} else if v, ok := metadata["disable-cooling"]; ok {
			metadataCopy["disable_cooling"] = v
		}
		if v, ok := metadata["request_retry"]; ok {
			metadataCopy["request_retry"] = v
		} else if v, ok := metadata["request-retry"]; ok {
			metadataCopy["request_retry"] = v
		}
		proxy := strings.TrimSpace(primary.ProxyURL)
		if proxy != "" {
			metadataCopy["proxy_url"] = proxy
		}
		virtual := &coreauth.Auth{
			ID:         buildGeminiVirtualID(primary.ID, projectID),
			Provider:   originalProvider,
			Label:      fmt.Sprintf("%s [%s]", label, projectID),
			Status:     coreauth.StatusActive,
			Attributes: attrs,
			Metadata:   metadataCopy,
			ProxyURL:   primary.ProxyURL,
			Prefix:     primary.Prefix,
			CreatedAt:  primary.CreatedAt,
			UpdatedAt:  primary.UpdatedAt,
			Runtime:    geminicli.NewVirtualCredential(projectID, shared),
		}
		virtuals = append(virtuals, virtual)
	}
	return virtuals
}

// splitGeminiProjectIDs extracts and deduplicates project IDs from metadata.
func splitGeminiProjectIDs(metadata map[string]any) []string {
	raw, _ := metadata["project_id"].(string)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

// buildGeminiVirtualID constructs a virtual auth ID from base ID and project ID.
func buildGeminiVirtualID(baseID, projectID string) string {
	project := strings.TrimSpace(projectID)
	if project == "" {
		project = "project"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return fmt.Sprintf("%s::%s", baseID, replacer.Replace(project))
}

// extractExcludedModelsFromMetadata reads per-account excluded models from the OAuth JSON metadata.
// Supports both "excluded_models" and "excluded-models" keys, and accepts both []string and []interface{}.
func extractExcludedModelsFromMetadata(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	// Try both key formats
	raw, ok := metadata["excluded_models"]
	if !ok {
		raw, ok = metadata["excluded-models"]
	}
	if !ok || raw == nil {
		return nil
	}
	var stringSlice []string
	switch v := raw.(type) {
	case []string:
		stringSlice = v
	case []interface{}:
		stringSlice = make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				stringSlice = append(stringSlice, s)
			}
		}
	default:
		return nil
	}
	result := make([]string, 0, len(stringSlice))
	for _, s := range stringSlice {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
