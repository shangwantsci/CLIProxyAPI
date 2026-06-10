package executor

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

// ClaudeExecutor is a stateless executor for Anthropic Claude over the messages API.
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type ClaudeExecutor struct {
	cfg *config.Config
	// concurrencyMu protects concurrencyInFlight. The limit is read from cfg at
	// acquire time so config hot reloads can change the cap without restarting.
	concurrencyMu       sync.Mutex
	concurrencyInFlight int
	// reauthViaCookieFn, when non-nil, overrides the real CookieAuth exchange in
	// reauthViaSessionKey. Production leaves it nil (defaultReauthViaCookie is used);
	// tests inject a stub to avoid network calls.
	reauthViaCookieFn func(ctx context.Context, cfg *config.Config, proxyURL, sessionKey string) (*claudeauth.ClaudeTokenStorage, error)
	// refreshTokensFn, when non-nil, overrides the real refresh-token refresh used
	// inside Refresh. Tests inject a stub to drive the failure/fallback branch.
	refreshTokensFn func(ctx context.Context, refreshToken string) error
}

// claudeToolPrefix is empty to match real Claude Code behavior (no tool name prefix).
// Previously "proxy_" was used but this is a detectable fingerprint difference.
const claudeToolPrefix = ""

// oauthToolRenameMap maps OpenCode-style (lowercase) tool names to Claude Code-style
// (TitleCase) names. Anthropic uses tool name fingerprinting to detect third-party
// clients on OAuth traffic. Renaming to official names avoids extra-usage billing.
// All tools are mapped to TitleCase equivalents to match Claude Code naming patterns.
var oauthToolRenameMap = map[string]string{
	"bash":         "Bash",
	"read":         "Read",
	"write":        "Write",
	"edit":         "Edit",
	"multiedit":    "MultiEdit",
	"glob":         "Glob",
	"grep":         "Grep",
	"task":         "Task",
	"agent":        "Agent",
	"webfetch":     "WebFetch",
	"web_fetch":    "WebFetch",
	"web_search":   "WebSearch",
	"websearch":    "WebSearch",
	"todowrite":    "TodoWrite",
	"question":     "Question",
	"skill":        "Skill",
	"ls":           "LS",
	"todoread":     "TodoRead",
	"notebookedit": "NotebookEdit",

	// OpenClaw-style tools observed in subscription-billing bridge projects.
	"exec":                 "Bash",
	"process":              "BashSession",
	"browser":              "BrowserControl",
	"canvas":               "CanvasView",
	"nodes":                "DeviceControl",
	"cron":                 "Scheduler",
	"cronjob":              "Scheduler",
	"message":              "SendMessage",
	"tts":                  "Speech",
	"gateway":              "SystemCtl",
	"agents_list":          "AgentList",
	"list_tasks":           "TaskList",
	"get_history":          "TaskHistory",
	"send_to_task":         "TaskSend",
	"create_task":          "TaskCreate",
	"subagents":            "AgentControl",
	"session_status":       "StatusCheck",
	"pdf":                  "PdfParse",
	"image_generate":       "ImageCreate",
	"music_generate":       "MusicCreate",
	"video_generate":       "VideoCreate",
	"memory_search":        "KnowledgeSearch",
	"memory_get":           "KnowledgeGet",
	"lcm_expand_query":     "ContextQuery",
	"lcm_grep":             "ContextGrep",
	"lcm_describe":         "ContextDescribe",
	"lcm_expand":           "ContextExpand",
	"yield_task":           "TaskYield",
	"delegate_task":        "Task",
	"task_store":           "TaskStore",
	"task_yield_interrupt": "TaskYieldInterrupt",
	"skill_view":           "Skill",

	// Hermes/OpenCode MCP shims often emit mcp_<tool>. Real Claude Code SDK
	// MCP tools use mcp__<server>__<tool>, so normalize the outward shape while
	// preserving the original client name for response restoration.
	"mcp_bash":      "mcp__hermes__Bash",
	"mcp_read":      "mcp__hermes__Read",
	"mcp_write":     "mcp__hermes__Write",
	"mcp_edit":      "mcp__hermes__Edit",
	"mcp_multiedit": "mcp__hermes__MultiEdit",
	"mcp_glob":      "mcp__hermes__Glob",
	"mcp_grep":      "mcp__hermes__Grep",
	"mcp_task":      "mcp__hermes__Task",
	"mcp_webfetch":  "mcp__hermes__WebFetch",
	"mcp_Bash":      "mcp__opencode__Bash",
	"mcp_Read":      "mcp__opencode__Read",
	"mcp_Write":     "mcp__opencode__Write",
	"mcp_Edit":      "mcp__opencode__Edit",
	"mcp_MultiEdit": "mcp__opencode__MultiEdit",
	"mcp_Glob":      "mcp__opencode__Glob",
	"mcp_Grep":      "mcp__opencode__Grep",
}

// The reverse map is now computed per-request in remapOAuthToolNames so that
// only names the client actually caused us to rewrite are restored on the
// response. A global reverse map — as used previously — corrupted responses
// for clients that sent mixed casing (e.g. Amp CLI sends `Bash` TitleCase
// alongside `glob` lowercase; the request flagged renames via `glob→Glob`,
// then the global reverse map incorrectly rewrote every `Bash` in the
// response to `bash`, causing Amp to reject the tool_use as unknown).

// oauthToolsToRemove lists tool names that must be stripped from OAuth requests
// even after remapping. Currently empty — all tools are mapped instead of removed.
var oauthToolsToRemove = map[string]bool{}

var oauthSchemaPropertyRenameMap = map[string]string{
	"session_id":      "thread_id",
	"conversation_id": "thread_ref",
	"summaryIds":      "chunk_ids",
	"summary_id":      "chunk_id",
	"system_event":    "event_text",
	"agent_id":        "worker_id",
	"wake_at":         "trigger_at",
	"wake_event":      "trigger_event",
}

type oauthToolReverseMap struct {
	ToolNames     map[string]string
	PropertyNames map[string]string
}

func (m oauthToolReverseMap) empty() bool {
	return len(m.ToolNames) == 0 && len(m.PropertyNames) == 0
}

func (m *oauthToolReverseMap) recordTool(original, renamed string) {
	if original == "" || renamed == "" || original == renamed {
		return
	}
	if m.ToolNames == nil {
		m.ToolNames = make(map[string]string)
	}
	if _, exists := m.ToolNames[renamed]; !exists {
		m.ToolNames[renamed] = original
	}
}

func (m *oauthToolReverseMap) recordProperty(original, renamed string) {
	if original == "" || renamed == "" || original == renamed {
		return
	}
	if m.PropertyNames == nil {
		m.PropertyNames = make(map[string]string)
	}
	if _, exists := m.PropertyNames[renamed]; !exists {
		m.PropertyNames[renamed] = original
	}
}

// Anthropic-compatible upstreams may reject or even crash when Claude models
// omit max_tokens. Prefer registered model metadata before using a fallback.
const defaultModelMaxTokens = 1024

func NewClaudeExecutor(cfg *config.Config) *ClaudeExecutor {
	return &ClaudeExecutor{cfg: cfg}
}

// tryAcquireConcurrencySlot 非阻塞地获取一个并发槽位。返回 true 表示拿到。
// 返回 false 表示当前并发已满,调用方应立即拒绝请求。
func (e *ClaudeExecutor) tryAcquireConcurrencySlot() bool {
	e.concurrencyMu.Lock()
	defer e.concurrencyMu.Unlock()
	limit := e.claudeMaxConcurrentRequests()
	if limit > 0 && e.concurrencyInFlight >= limit {
		return false
	}
	e.concurrencyInFlight++
	return true
}

// releaseConcurrencySlot 释放一个并发槽位。即使被误调多次也不会 panic 或阻塞。
func (e *ClaudeExecutor) releaseConcurrencySlot() {
	e.concurrencyMu.Lock()
	defer e.concurrencyMu.Unlock()
	if e.concurrencyInFlight <= 0 {
		return
	}
	e.concurrencyInFlight--
}

func (e *ClaudeExecutor) claudeMaxConcurrentRequests() int {
	if e == nil || e.cfg == nil {
		return 0
	}
	return e.cfg.ClaudeMaxConcurrentRequests
}

// claudeConcurrencyLimitError 在全局并发已满时返回。Retryable=false 确保上层
// 调度器不会换账号重试(全局满了换哪个账号都无济于事),直接以 429 弹回上游。
func claudeConcurrencyLimitError() error {
	return &cliproxyauth.Error{
		Code:       cliproxyauth.LocalRequestGuardErrorCode,
		Message:    "Claude upstream concurrency limit reached",
		Retryable:  false,
		HTTPStatus: http.StatusTooManyRequests,
	}
}

func (e *ClaudeExecutor) Identifier() string { return "claude" }

// PrepareRequest injects Claude credentials into the outgoing HTTP request.
func (e *ClaudeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := claudeCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		return nil
	}
	useAPIKey := auth != nil && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != ""
	isAnthropicBase := req.URL != nil && strings.EqualFold(req.URL.Scheme, "https") && strings.EqualFold(req.URL.Host, "api.anthropic.com")
	if isAnthropicBase && useAPIKey {
		req.Header.Del("Authorization")
		req.Header.Set("x-api-key", apiKey)
	} else {
		req.Header.Del("x-api-key")
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Claude credentials into the request and executes it.
func (e *ClaudeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("claude executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewUtlsHTTPClient(e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *ClaudeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	// Use streaming translation to preserve function calling, except for claude.
	stream := from != to
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, stream)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body = repairClaudeRequestShapeBeforeThinking(body)
	applyDefaultAdaptiveThinking := shouldApplyClaudeDefaultAdaptiveThinking(body, req.Model)
	ctx = helps.WithClaudeBillableUsage(ctx, config.ClaudeBillableUsageEnabled(e.cfg), baseModel, from.String(), originalPayloadSource)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	// Apply cloaking (system prompt injection, fake user ID, sensitive word obfuscation)
	// based on client type and configuration.
	var cloaked bool
	body, cloaked = applyCloaking(ctx, e.cfg, auth, body, baseModel, apiKey)

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = ensureModelMaxTokens(body, baseModel)
	if applyDefaultAdaptiveThinking {
		body = applyClaudeDefaultAdaptiveThinking(body)
	}
	body = repairClaudeRequestShape(body)

	// Disable thinking if tool_choice forces tool use (Anthropic API constraint)
	body = disableThinkingIfToolChoiceForced(body)
	body = normalizeClaudeTemperatureForThinking(body)

	// Auto-inject cache_control if missing (optimization for ClawdBot/clients without caching support)
	if countCacheControls(body) == 0 {
		body = ensureCacheControl(body)
	}

	// Enforce Anthropic's cache_control block limit (max 4 breakpoints per request).
	// Cloaking and ensureCacheControl may push the total over 4 when the client
	// (e.g. Amp CLI) already sends multiple cache_control blocks.
	body = enforceCacheControlLimit(body, 4)

	// Normalize TTL values to prevent ordering violations under prompt-caching-scope-2026-01-05.
	// A 1h-TTL block must not appear after a 5m-TTL block in evaluation order (tools→system→messages).
	body = normalizeCacheControlTTL(body)

	// Extract betas from body and convert to header
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	extraBetas = inferClaudeBetasFromBody(body, extraBetas)
	bodyForTranslation := body
	bodyForUpstream := body
	oauthToken := isClaudeOAuthToken(apiKey)
	var oauthToolNamesReverseMap oauthToolReverseMap
	if oauthToken {
		bodyForUpstream, oauthToolNamesReverseMap = prepareClaudeOAuthToolNamesForUpstream(bodyForUpstream, claudeToolPrefix, auth.ToolPrefixDisabled())
	}
	// Enable cch signing by default for OAuth tokens (not just experimental flag).
	// Claude Code always computes cch; missing or invalid cch is a detectable fingerprint.
	if oauthToken || experimentalCCHSigningEnabled(e.cfg, auth) {
		bodyForUpstream = signAnthropicMessagesBody(bodyForUpstream)
	}

	if err = checkClaudeUpstreamBodySize(bodyForUpstream, e.cfg.ClaudeMaxRequestBytes); err != nil {
		return resp, err
	}

	if !e.tryAcquireConcurrencySlot() {
		msg := fmt.Sprintf("claude concurrency limit reached (cap=%d), rejecting request", e.cfg.ClaudeMaxConcurrentRequests)
		helps.LogWithRequestID(ctx).Warn(msg)
		return resp, claudeConcurrencyLimitError()
	}
	defer e.releaseConcurrencySlot()

	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyForUpstream))
	if err != nil {
		return resp, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas, e.cfg, baseModel)
	mimicryEvent, _, errGuard := prepareClaudeMimicryGuardEvent(ctx, e.cfg, auth, opts, baseModel, "/v1/messages", bodyForUpstream, httpReq.Header, cloaked)
	if errGuard != nil {
		return resp, errGuard
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      bodyForUpstream,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordClaudeMimicryGuardEvent(mimicryEvent, true, 0, err.Error())
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Decompress error responses — pass the Content-Encoding value (may be empty)
		// and let decodeResponseBody handle both header-declared and magic-byte-detected
		// compression.  This keeps error-path behaviour consistent with the success path.
		errBody, decErr := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if decErr != nil {
			recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, decErr.Error())
			helps.RecordAPIResponseError(ctx, e.cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			return resp, newClaudeStatusErr(httpResp.StatusCode, []byte(msg), httpResp.Header)
		}
		b, readErr := io.ReadAll(errBody)
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			msg := fmt.Sprintf("failed to read error response body: %v", readErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			b = []byte(msg)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newClaudeStatusErr(httpResp.StatusCode, b, httpResp.Header)
		recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, err.Error())
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(decodedBody)
	if err != nil {
		recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, err.Error())
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if stream {
		if errValidate := validateClaudeStreamingResponse(data); errValidate != nil {
			recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, errValidate.Error())
			helps.RecordAPIResponseError(ctx, e.cfg, errValidate)
			return resp, errValidate
		}
		if detail, ok := helps.MergeClaudeStreamUsageLines(data); ok {
			detail = helps.ClaudeBillableUsageDetail(ctx, baseModel, from.String(), originalPayloadSource, detail)
			reporter.Publish(ctx, detail)
		}
	} else {
		detail := helps.ParseClaudeUsage(data)
		detail = helps.ClaudeBillableUsageDetail(ctx, baseModel, from.String(), originalPayloadSource, detail)
		reporter.Publish(ctx, detail)
	}
	data = restoreClaudeOAuthToolNamesFromResponse(data, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
	if from == to {
		data = helps.RewriteClaudeUsageForBillable(ctx, data)
	}
	var param any
	out := sdktranslator.TranslateNonStream(
		ctx,
		to,
		from,
		req.Model,
		opts.OriginalRequest,
		bodyForTranslation,
		data,
		&param,
	)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, "")
	return resp, nil
}

func (e *ClaudeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body = repairClaudeRequestShapeBeforeThinking(body)
	applyDefaultAdaptiveThinking := shouldApplyClaudeDefaultAdaptiveThinking(body, req.Model)
	ctx = helps.WithClaudeBillableUsage(ctx, config.ClaudeBillableUsageEnabled(e.cfg), baseModel, from.String(), originalPayloadSource)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	// Apply cloaking (system prompt injection, fake user ID, sensitive word obfuscation)
	// based on client type and configuration.
	var cloaked bool
	body, cloaked = applyCloaking(ctx, e.cfg, auth, body, baseModel, apiKey)

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = ensureModelMaxTokens(body, baseModel)
	if applyDefaultAdaptiveThinking {
		body = applyClaudeDefaultAdaptiveThinking(body)
	}
	body = repairClaudeRequestShape(body)

	// Disable thinking if tool_choice forces tool use (Anthropic API constraint)
	body = disableThinkingIfToolChoiceForced(body)
	body = normalizeClaudeTemperatureForThinking(body)

	// Auto-inject cache_control if missing (optimization for ClawdBot/clients without caching support)
	if countCacheControls(body) == 0 {
		body = ensureCacheControl(body)
	}

	// Enforce Anthropic's cache_control block limit (max 4 breakpoints per request).
	body = enforceCacheControlLimit(body, 4)

	// Normalize TTL values to prevent ordering violations under prompt-caching-scope-2026-01-05.
	body = normalizeCacheControlTTL(body)

	// Extract betas from body and convert to header
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	extraBetas = inferClaudeBetasFromBody(body, extraBetas)
	bodyForTranslation := body
	bodyForUpstream := body
	oauthToken := isClaudeOAuthToken(apiKey)
	var oauthToolNamesReverseMap oauthToolReverseMap
	if oauthToken {
		bodyForUpstream, oauthToolNamesReverseMap = prepareClaudeOAuthToolNamesForUpstream(bodyForUpstream, claudeToolPrefix, auth.ToolPrefixDisabled())
	}
	// Enable cch signing by default for OAuth tokens (not just experimental flag).
	if oauthToken || experimentalCCHSigningEnabled(e.cfg, auth) {
		bodyForUpstream = signAnthropicMessagesBody(bodyForUpstream)
	}

	if err = checkClaudeUpstreamBodySize(bodyForUpstream, e.cfg.ClaudeMaxRequestBytes); err != nil {
		return nil, err
	}

	if !e.tryAcquireConcurrencySlot() {
		helps.LogWithRequestID(ctx).Warn(fmt.Sprintf("claude concurrency limit reached (cap=%d), rejecting stream request", e.cfg.ClaudeMaxConcurrentRequests))
		return nil, claudeConcurrencyLimitError()
	}
	// slotReleased 守卫:Acquire 成功后,若在后台 goroutine 接管释放责任之前
	// 发生任何 error return,必须显式释放槽位避免泄漏。goroutine 一旦启动,
	// 由它的 defer 负责释放,届时把 slotReleased 置 true 防止本 defer 重复释放。
	slotReleased := false
	defer func() {
		if !slotReleased {
			e.releaseConcurrencySlot()
		}
	}()

	url := fmt.Sprintf("%s/v1/messages?beta=true", baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyForUpstream))
	if err != nil {
		return nil, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, true, extraBetas, e.cfg, baseModel)
	mimicryEvent, _, errGuard := prepareClaudeMimicryGuardEvent(ctx, e.cfg, auth, opts, baseModel, "/v1/messages", bodyForUpstream, httpReq.Header, cloaked)
	if errGuard != nil {
		return nil, errGuard
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      bodyForUpstream,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordClaudeMimicryGuardEvent(mimicryEvent, true, 0, err.Error())
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		// Decompress error responses — pass the Content-Encoding value (may be empty)
		// and let decodeResponseBody handle both header-declared and magic-byte-detected
		// compression.  This keeps error-path behaviour consistent with the success path.
		errBody, decErr := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
		if decErr != nil {
			recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, decErr.Error())
			helps.RecordAPIResponseError(ctx, e.cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			return nil, newClaudeStatusErr(httpResp.StatusCode, []byte(msg), httpResp.Header)
		}
		b, readErr := io.ReadAll(errBody)
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			msg := fmt.Sprintf("failed to read error response body: %v", readErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			b = []byte(msg)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		err = newClaudeStatusErr(httpResp.StatusCode, b, httpResp.Header)
		return nil, err
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, err.Error())
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	// goroutine 即将接管并发槽位释放责任。置位后,上面 defer 守卫不再
	// 释放(避免双重释放);槽位将持有到流读完(下面 goroutine 的 release defer)。
	slotReleased = true
	go func() {
		defer e.releaseConcurrencySlot()
		recordedMimicryEvent := false
		var streamUsage usage.Detail
		streamUsageSeen := false
		streamFailed := false
		mergeStreamUsage := func(line []byte) {
			if detail, ok := helps.ParseClaudeStreamUsage(line); ok {
				streamUsage = helps.MergeUsageDetail(streamUsage, detail)
				streamUsageSeen = true
			}
		}
		recordStreamMimicryEvent := func(message string) {
			if recordedMimicryEvent {
				return
			}
			recordedMimicryEvent = true
			recordClaudeMimicryGuardEvent(mimicryEvent, true, httpResp.StatusCode, message)
		}
		defer close(out)
		defer func() {
			recordStreamMimicryEvent("")
			if streamUsageSeen && !streamFailed {
				streamUsage = helps.ClaudeBillableUsageDetail(ctx, baseModel, from.String(), originalPayloadSource, streamUsage)
				reporter.Publish(ctx, streamUsage)
			}
			if errClose := decodedBody.Close(); errClose != nil {
				log.Errorf("response body close error: %v", errClose)
			}
		}()

		// If from == to (Claude → Claude), directly forward the SSE stream without translation
		if from == to {
			scanner := bufio.NewScanner(decodedBody)
			scanner.Buffer(nil, 52_428_800) // 50MB
			for scanner.Scan() {
				line := scanner.Bytes()
				helps.AppendAPIResponseChunk(ctx, e.cfg, line)
				mergeStreamUsage(line)
				line = restoreClaudeOAuthToolNamesFromStreamLine(line, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
				line = helps.RewriteClaudeStreamUsageForBillable(ctx, line)
				// Forward the line as-is to preserve SSE format
				cloned := make([]byte, len(line)+1)
				copy(cloned, line)
				cloned[len(line)] = '\n'
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: cloned}:
				case <-ctx.Done():
					return
				}
			}
			if errScan := scanner.Err(); errScan != nil {
				streamFailed = true
				recordStreamMimicryEvent(errScan.Error())
				helps.RecordAPIResponseError(ctx, e.cfg, errScan)
				reporter.PublishFailure(ctx, errScan)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
				case <-ctx.Done():
				}
			}
			return
		}

		// For other formats, use translation
		scanner := bufio.NewScanner(decodedBody)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			mergeStreamUsage(line)
			line = restoreClaudeOAuthToolNamesFromStreamLine(line, claudeToolPrefix, auth.ToolPrefixDisabled(), oauthToolNamesReverseMap)
			chunks := sdktranslator.TranslateStream(
				ctx,
				to,
				from,
				req.Model,
				opts.OriginalRequest,
				bodyForTranslation,
				bytes.Clone(line),
				&param,
			)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			streamFailed = true
			recordStreamMimicryEvent(errScan.Error())
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func validateClaudeStreamingResponse(data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(nil, 52_428_800)

	hasData := false
	hasMessageStart := false
	hasMessageDelta := false

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		hasData = true
		if !gjson.ValidBytes(payload) {
			return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned malformed stream data"}
		}

		root := gjson.ParseBytes(payload)
		switch root.Get("type").String() {
		case "error":
			message := strings.TrimSpace(root.Get("error.message").String())
			if message == "" {
				message = strings.TrimSpace(root.Get("error.type").String())
			}
			if message == "" {
				message = "unknown upstream error"
			}
			return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned error event: " + message}
		case "message_start":
			message := root.Get("message")
			if strings.TrimSpace(message.Get("id").String()) == "" || strings.TrimSpace(message.Get("model").String()) == "" {
				return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream message_start is missing id or model"}
			}
			hasMessageStart = true
		case "message_delta":
			hasMessageDelta = true
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}
	if !hasData {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream returned empty stream response"}
	}
	if !hasMessageStart {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream response is missing message_start"}
	}
	if !hasMessageDelta {
		return statusErr{code: http.StatusBadGateway, msg: "claude executor: upstream stream response ended before message completion"}
	}
	return nil
}

func (e *ClaudeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := claudeCreds(auth)
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}

	from := opts.SourceFormat
	to := sdktranslator.FromString("claude")
	ctx = helps.WithClaudeBillableUsage(ctx, config.ClaudeBillableUsageEnabled(e.cfg), baseModel, from.String(), req.Payload)
	if config.ClaudeBillableUsageEnabled(e.cfg) {
		count, _ := helps.ClaudeBillableInputTokens(ctx, baseModel, from.String(), req.Payload)
		out := sdktranslator.TranslateTokenCount(ctx, to, from, count, helps.BuildClaudeTokenCountJSON(count))
		return cliproxyexecutor.Response{Payload: out}, nil
	}
	// Use streaming translation to preserve function calling, except for claude.
	stream := from != to
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	if shouldApplyClaudeDefaultAdaptiveThinking(body, req.Model) {
		body = applyClaudeDefaultAdaptiveThinking(body)
	}
	body = repairClaudeRequestShape(body)

	var cloaked bool
	body, cloaked = applyCloaking(ctx, e.cfg, auth, body, baseModel, apiKey)

	// Keep count_tokens requests compatible with Anthropic cache-control constraints too.
	body = enforceCacheControlLimit(body, 4)
	body = normalizeCacheControlTTL(body)

	// Extract betas from body and convert to header (for count_tokens too)
	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)
	extraBetas = inferClaudeBetasFromBody(body, extraBetas)
	oauthToken := isClaudeOAuthToken(apiKey)
	if oauthToken {
		body, _ = prepareClaudeOAuthToolNamesForUpstream(body, claudeToolPrefix, auth.ToolPrefixDisabled())
	}
	if oauthToken || experimentalCCHSigningEnabled(e.cfg, auth) {
		body = signAnthropicMessagesBody(body)
	}

	if err := checkClaudeUpstreamBodySize(body, e.cfg.ClaudeMaxRequestBytes); err != nil {
		return cliproxyexecutor.Response{}, err
	}

	url := fmt.Sprintf("%s/v1/messages/count_tokens?beta=true", baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas, e.cfg, baseModel)
	mimicryEvent, _, errGuard := prepareClaudeMimicryGuardEvent(ctx, e.cfg, auth, opts, baseModel, "/v1/messages/count_tokens", body, httpReq.Header, cloaked)
	if errGuard != nil {
		return cliproxyexecutor.Response{}, errGuard
	}
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewUtlsHTTPClient(e.cfg, auth, 0)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		recordClaudeMimicryGuardEvent(mimicryEvent, true, 0, err.Error())
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Decompress error responses — pass the Content-Encoding value (may be empty)
		// and let decodeResponseBody handle both header-declared and magic-byte-detected
		// compression.  This keeps error-path behaviour consistent with the success path.
		errBody, decErr := decodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
		if decErr != nil {
			recordClaudeMimicryGuardEvent(mimicryEvent, true, resp.StatusCode, decErr.Error())
			helps.RecordAPIResponseError(ctx, e.cfg, decErr)
			msg := fmt.Sprintf("failed to decode error response body: %v", decErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			return cliproxyexecutor.Response{}, newClaudeStatusErr(resp.StatusCode, []byte(msg), resp.Header)
		}
		b, readErr := io.ReadAll(errBody)
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			msg := fmt.Sprintf("failed to read error response body: %v", readErr)
			helps.LogWithRequestID(ctx).Warn(msg)
			b = []byte(msg)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if errClose := errBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		recordClaudeMimicryGuardEvent(mimicryEvent, true, resp.StatusCode, helps.SummarizeErrorBody(resp.Header.Get("Content-Type"), b))
		return cliproxyexecutor.Response{}, newClaudeStatusErr(resp.StatusCode, b, resp.Header)
	}
	decodedBody, err := decodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		recordClaudeMimicryGuardEvent(mimicryEvent, true, resp.StatusCode, err.Error())
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(decodedBody)
	if err != nil {
		recordClaudeMimicryGuardEvent(mimicryEvent, true, resp.StatusCode, err.Error())
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	count := gjson.GetBytes(data, "input_tokens").Int()
	out := sdktranslator.TranslateTokenCount(ctx, to, from, count, data)
	recordClaudeMimicryGuardEvent(mimicryEvent, true, resp.StatusCode, "")
	return cliproxyexecutor.Response{Payload: out, Headers: resp.Header.Clone()}, nil
}

func (e *ClaudeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("claude executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("claude executor: auth is nil")
	}
	var refreshToken string
	var authSource string
	var tokenEndpoint string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
		if v, ok := auth.Metadata["auth_source"].(string); ok {
			authSource = strings.TrimSpace(v)
		}
		if v, ok := auth.Metadata["token_endpoint"].(string); ok {
			tokenEndpoint = strings.TrimSpace(v)
		}
	}
	if refreshToken == "" {
		return e.reauthViaSessionKey(ctx, auth, nil)
	}

	// Use the injected stub when set (test path), otherwise call the real service.
	var refreshErr error
	var td *claudeauth.ClaudeTokenData
	if e.refreshTokensFn != nil {
		refreshErr = e.refreshTokensFn(ctx, refreshToken)
	} else {
		svc := claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
		td, refreshErr = svc.RefreshTokensWithRetryOptions(ctx, refreshToken, 3, claudeauth.RefreshTokenOptions{
			AuthSource:            authSource,
			TokenEndpoint:         tokenEndpoint,
			AllowEndpointFallback: true,
		})
	}

	if refreshErr != nil {
		if isCredentialLevelAuthFailure(refreshErr) {
			return e.reauthViaSessionKey(ctx, auth, refreshErr)
		}
		return nil, refreshErr
	}

	// Stub path with no error: nothing to write.
	if e.refreshTokensFn != nil {
		return auth, nil
	}

	// Real path: rewrite metadata from the returned token data.
	// NOTE: keep these metadata writes in sync with reauthViaSessionKey.
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.Email != "" {
		auth.Metadata["email"] = td.Email
	}
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "claude"
	if td.AuthSource != "" {
		auth.Metadata["auth_source"] = td.AuthSource
	}
	if td.TokenEndpoint != "" {
		auth.Metadata["token_endpoint"] = td.TokenEndpoint
	}
	if td.RedirectURI != "" {
		auth.Metadata["redirect_uri"] = td.RedirectURI
	}
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

// defaultReauthViaCookie performs the real session-key re-exchange using the same
// CookieAuth path as the original import (PreferSetupToken=true), returning a fresh
// token storage. Used when ClaudeExecutor.reauthViaCookieFn is nil.
func defaultReauthViaCookie(ctx context.Context, cfg *config.Config, proxyURL, sessionKey string) (*claudeauth.ClaudeTokenStorage, error) {
	svc := claudeauth.NewClaudeAuthWithProxyURL(cfg, proxyURL)
	bundle, err := svc.CookieAuth(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	return svc.CreateTokenStorage(bundle), nil
}

// reauthViaSessionKey re-exchanges Claude credentials using a retained session key
// when refresh-token refresh is impossible (missing) or failed (invalid_grant /
// revoked). On success it rewrites auth.Metadata in place, preserves the session
// key seed, clears the failure counter, and returns the updated auth. On failure it
// returns an error for the conductor to count toward the disable threshold. cause
// carries the original refresh error (may be nil when no refresh_token).
func (e *ClaudeExecutor) reauthViaSessionKey(ctx context.Context, auth *cliproxyauth.Auth, cause error) (*cliproxyauth.Auth, error) {
	sessionKey := ""
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["session_key"].(string); ok {
			sessionKey = strings.TrimSpace(v)
		}
	}
	if sessionKey == "" {
		if cause != nil {
			return nil, cause
		}
		return nil, fmt.Errorf("claude executor: no refresh_token and no session_key for %s", auth.ID)
	}

	exchange := e.reauthViaCookieFn
	if exchange == nil {
		exchange = defaultReauthViaCookie
	}
	ts, err := exchange(ctx, e.cfg, auth.ProxyURL, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("claude executor: session_key reauth failed for %s: %w", auth.ID, err)
	}

	// NOTE: keep these metadata writes in sync with the refresh_token success path in Refresh.
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = ts.AccessToken
	if ts.RefreshToken != "" {
		auth.Metadata["refresh_token"] = ts.RefreshToken
	}
	if ts.Email != "" {
		auth.Metadata["email"] = ts.Email
	}
	auth.Metadata["expired"] = ts.Expire
	auth.Metadata["type"] = "claude"
	if ts.AuthSource != "" {
		auth.Metadata["auth_source"] = ts.AuthSource
	}
	if ts.TokenEndpoint != "" {
		auth.Metadata["token_endpoint"] = ts.TokenEndpoint
	}
	if ts.RedirectURI != "" {
		auth.Metadata["redirect_uri"] = ts.RedirectURI
	}
	auth.Metadata["session_key"] = sessionKey
	delete(auth.Metadata, "session_key_reauth_failures")
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	return auth, nil
}

// isCredentialLevelAuthFailure reports whether a refresh error is a credential-level
// failure worth retrying via session_key (invalid_grant / 401 / token revoked), as
// opposed to a transient/network/5xx error.
func isCredentialLevelAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	raw := strings.ToLower(err.Error())
	for _, p := range []string{
		"invalid_grant", "status 401", "401 unauthorized",
		"token_revoked", "token revoked", "refresh token revoked",
		"refresh token expired", "refresh token not found",
	} {
		if strings.Contains(raw, p) {
			return true
		}
	}
	return false
}

func newClaudeStatusErr(statusCode int, body []byte, headers http.Header) statusErr {
	err := statusErr{code: statusCode, msg: string(body)}
	if statusCode == http.StatusTooManyRequests {
		err.retryAfter = parseClaudeUpstreamRetryAfter(headers, time.Now())
	}
	return err
}

// checkClaudeUpstreamBodySize rejects request bodies larger than limit (bytes)
// before they are sent upstream. The request never reaches Anthropic, so it is
// returned as a local request-guard error (LocalRequestTooLargeErrorCode): the
// conductor short-circuits these without recording them on account health, so an
// oversized client request does not mark an otherwise healthy account as
// request-error. A limit <= 0 disables the check.
func checkClaudeUpstreamBodySize(body []byte, limit int) error {
	if limit <= 0 {
		return nil
	}
	if len(body) > limit {
		return &cliproxyauth.Error{
			Code:       cliproxyauth.LocalRequestTooLargeErrorCode,
			Message:    fmt.Sprintf("request body (%d bytes) exceeds the maximum size (%d bytes); reduce attachments or shorten the conversation history", len(body), limit),
			Retryable:  false,
			HTTPStatus: http.StatusRequestEntityTooLarge,
		}
	}
	return nil
}

func parseClaudeUpstreamRetryAfter(headers http.Header, now time.Time) *time.Duration {
	if headers == nil {
		return nil
	}
	if retryAfter := parseRetryAfterHeaderValue(headers.Get("Retry-After"), now); retryAfter != nil {
		return retryAfter
	}
	if raw := strings.TrimSpace(headers.Get("Retry-After-Ms")); raw != "" {
		if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 {
			d := time.Duration(ms) * time.Millisecond
			return &d
		}
	}

	var earliest *time.Duration
	for _, name := range []string{
		"anthropic-ratelimit-unified-reset",
		"anthropic-ratelimit-requests-reset",
		"anthropic-ratelimit-tokens-reset",
		"anthropic-ratelimit-input-tokens-reset",
		"anthropic-ratelimit-output-tokens-reset",
	} {
		if d := parseResetHeaderDuration(headers.Get(name), now); d != nil && *d > 0 {
			if earliest == nil || *d < *earliest {
				v := *d
				earliest = &v
			}
		}
	}
	return earliest
}

func parseRetryAfterHeaderValue(raw string, now time.Time) *time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 {
		d := time.Duration(seconds) * time.Second
		return &d
	}
	if when, err := http.ParseTime(raw); err == nil {
		d := when.Sub(now)
		if d > 0 {
			return &d
		}
	}
	return nil
}

func parseResetHeaderDuration(raw string, now time.Time) *time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if unixSeconds, err := strconv.ParseInt(raw, 10, 64); err == nil && unixSeconds > 0 {
		d := time.Unix(unixSeconds, 0).Sub(now)
		if d > 0 {
			return &d
		}
		return nil
	}
	if when, err := time.Parse(time.RFC3339, raw); err == nil {
		d := when.Sub(now)
		if d > 0 {
			return &d
		}
		return nil
	}
	if when, err := http.ParseTime(raw); err == nil {
		d := when.Sub(now)
		if d > 0 {
			return &d
		}
	}
	return nil
}

// extractAndRemoveBetas extracts the "betas" array from the body and removes it.
// Returns the extracted betas as a string slice and the modified body.
func extractAndRemoveBetas(body []byte) ([]string, []byte) {
	betasResult := gjson.GetBytes(body, "betas")
	if !betasResult.Exists() {
		return nil, body
	}
	var betas []string
	if betasResult.IsArray() {
		for _, item := range betasResult.Array() {
			if s := strings.TrimSpace(item.String()); s != "" {
				betas = append(betas, s)
			}
		}
	} else if s := strings.TrimSpace(betasResult.String()); s != "" {
		betas = append(betas, s)
	}
	body, _ = sjson.DeleteBytes(body, "betas")
	return betas, body
}

// auditClaudeMimicryForGuard 对请求做伪装审计打分供 guard 评估。
// cloaked=true(请求确实被伪装):写入全局快照 claudeMimicryLatest,驱动顶部
// "CLI 对齐状态"徽章。
// cloaked=false(故意不伪装:cloak=never 或 auto+真实CLI):仍打分供 guard 判断
// 真实泄露(如 leaky tool names),但不写全局快照,避免把"故意不伪装"的请求
// 当作"伪装失败"拉低对齐徽章。
func auditClaudeMimicryForGuard(model, requestPath string, body []byte, upstreamHeaders http.Header, cfg *config.Config, cloaked bool) ClaudeMimicryAuditSnapshot {
	if cloaked {
		return RecordClaudeMimicryAudit(model, requestPath, body, upstreamHeaders, cfg)
	}
	return AuditClaudeMimicryRequest(model, requestPath, body, upstreamHeaders, cfg)
}

func prepareClaudeMimicryGuardEvent(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, opts cliproxyexecutor.Options, model, requestPath string, body []byte, upstreamHeaders http.Header, cloaked bool) (ClaudeMimicryEvent, ClaudeMimicryGuardDecision, error) {
	audit := auditClaudeMimicryForGuard(model, requestPath, body, upstreamHeaders, cfg, cloaked)
	requireSystemBlocks := claudeRequestRequiresSystemBlocks(model)
	decision := EvaluateClaudeMimicryGuardWithPolicy(audit, cfg, ClaudeMimicryGuardPolicy{
		RequireSignedCCH:    requireSystemBlocks && claudeRequestRequiresCCHSigning(cfg, auth),
		RequireSystemBlocks: requireSystemBlocks,
	})
	event := ClaudeMimicryEvent{
		RequestID:    logging.GetRequestID(ctx),
		ClientSource: ClassifyClaudeMimicryClientSource(opts.SourceFormat.String(), inboundClaudeMimicryHeaders(ctx, opts)),
		SourceFormat: strings.TrimSpace(opts.SourceFormat.String()),
		Action:       decision.Action,
		GuardMode:    decision.Mode,
		AuditStatus:  audit.Status,
		Model:        audit.Model,
		RequestPath:  audit.RequestPath,
		Reasons:      append([]string(nil), decision.Reasons...),
		Warnings:     append([]string(nil), audit.Warnings...),
		Failures:     append([]string(nil), audit.Failures...),
	}
	if cfg != nil {
		event.EventLimit = cfg.ClaudeMimicryGuard.EventsLimit
	}
	if auth != nil {
		event.AuthID = auth.ID
		event.AuthLabel = auth.Label
	}
	if decision.Blocked {
		RecordClaudeMimicryEvent(event)
		return event, decision, claudeMimicryGuardBlockedError(decision)
	}
	return event, decision, nil
}

func claudeRequestRequiresCCHSigning(cfg *config.Config, auth *cliproxyauth.Auth) bool {
	apiKey, _ := claudeCreds(auth)
	return isClaudeOAuthToken(apiKey) || experimentalCCHSigningEnabled(cfg, auth)
}

func claudeRequestRequiresSystemBlocks(model string) bool {
	return !strings.HasPrefix(model, "claude-3-5-haiku")
}

func recordClaudeMimicryGuardEvent(event ClaudeMimicryEvent, upstreamAttempted bool, upstreamStatus int, upstreamError string) {
	event.UpstreamAttempted = upstreamAttempted
	event.UpstreamStatus = upstreamStatus
	event.UpstreamError = strings.TrimSpace(upstreamError)
	RecordClaudeMimicryEvent(event)
}

func inboundClaudeMimicryHeaders(ctx context.Context, opts cliproxyexecutor.Options) http.Header {
	if len(opts.Headers) > 0 {
		return opts.Headers
	}
	return ginHeadersFromContext(ctx)
}

func claudeMimicryGuardBlockedError(decision ClaudeMimicryGuardDecision) error {
	message := "Claude Code mimicry guard blocked request"
	if len(decision.Reasons) > 0 {
		message += ": " + strings.Join(decision.Reasons, "; ")
	}
	return &cliproxyauth.Error{
		Code:       cliproxyauth.LocalRequestGuardErrorCode,
		Message:    message,
		Retryable:  false,
		HTTPStatus: http.StatusBadRequest,
	}
}

// disableThinkingIfToolChoiceForced checks if tool_choice forces tool use and disables thinking.
// Anthropic API does not allow thinking when tool_choice is set to "any" or a specific tool.
// See: https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking#important-considerations
func disableThinkingIfToolChoiceForced(body []byte) []byte {
	toolChoiceType := gjson.GetBytes(body, "tool_choice.type").String()
	// "auto" is allowed with thinking, but "any" or "tool" (specific tool) are not
	if toolChoiceType == "any" || toolChoiceType == "tool" {
		// Remove thinking configuration entirely to avoid API error
		body, _ = sjson.DeleteBytes(body, "thinking")
		// Adaptive thinking may also set output_config.effort; remove it to avoid
		// leaking thinking controls when tool_choice forces tool use.
		body, _ = sjson.DeleteBytes(body, "output_config.effort")
		if oc := gjson.GetBytes(body, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "output_config")
		}
	}
	return body
}

// normalizeClaudeTemperatureForThinking keeps Anthropic message requests valid when
// thinking is enabled. Anthropic rejects temperatures other than 1 when
// thinking.type is enabled/adaptive/auto.
func normalizeClaudeTemperatureForThinking(body []byte) []byte {
	if !gjson.GetBytes(body, "temperature").Exists() {
		return body
	}

	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	switch thinkingType {
	case "enabled", "adaptive", "auto":
		if temp := gjson.GetBytes(body, "temperature"); temp.Exists() && temp.Type == gjson.Number && temp.Float() == 1 {
			return body
		}
		body, _ = sjson.SetBytes(body, "temperature", 1)
	}
	return body
}

func repairClaudeRequestShape(body []byte) []byte {
	body = repairClaudeRequestShapeBeforeThinking(body)
	body = repairClaudeContextManagement(body)
	return body
}

func repairClaudeRequestShapeBeforeThinking(body []byte) []byte {
	body = repairClaudeAdaptiveEffort(body)
	body = repairClaudeThinkingBudget(body)
	body = repairClaudeMessageContent(body)
	return body
}

func shouldApplyClaudeDefaultAdaptiveThinking(body []byte, model string) bool {
	suffix := thinking.ParseSuffix(model)
	if suffix.HasSuffix || !claudeModelUsesAlwaysAdaptiveThinking(suffix.ModelName) {
		return false
	}
	if gjson.GetBytes(body, "thinking").Exists() || gjson.GetBytes(body, "output_config.effort").Exists() {
		return false
	}
	return true
}

func applyClaudeDefaultAdaptiveThinking(body []byte) []byte {
	if gjson.GetBytes(body, "thinking").Exists() || gjson.GetBytes(body, "output_config.effort").Exists() {
		return body
	}
	body, _ = sjson.SetBytes(body, "thinking.type", "adaptive")
	body, _ = sjson.SetBytes(body, "output_config.effort", string(thinking.LevelHigh))
	return body
}

func claudeModelUsesAlwaysAdaptiveThinking(model string) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	model = strings.TrimSuffix(model, "-thinking")
	return model == "claude-fable-5" || strings.HasPrefix(model, "claude-fable-5-") ||
		model == "claude-mythos-5" || strings.HasPrefix(model, "claude-mythos-5-")
}

func repairClaudeAdaptiveEffort(body []byte) []byte {
	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	if thinkingType != "enabled" && thinkingType != "adaptive" && thinkingType != "auto" {
		return body
	}
	effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "output_config.effort").String()))
	if effort != "xhigh" {
		return body
	}
	modelInfo := registry.LookupModelInfo(gjson.GetBytes(body, "model").String(), "claude")
	if modelInfo != nil && modelInfo.Thinking != nil && thinking.HasLevel(modelInfo.Thinking.Levels, effort) {
		return body
	}
	body, _ = sjson.SetBytes(body, "output_config.effort", "high")
	return body
}

func repairClaudeThinkingBudget(body []byte) []byte {
	budgetResult := gjson.GetBytes(body, "thinking.budget_tokens")
	if !budgetResult.Exists() || budgetResult.Type != gjson.Number {
		return body
	}
	budget := int(budgetResult.Int())
	if budget <= 0 {
		return body
	}

	modelInfo := registry.LookupModelInfo(gjson.GetBytes(body, "model").String(), "claude")
	if modelInfo == nil || modelInfo.Thinking == nil {
		return body
	}
	minBudget := modelInfo.Thinking.Min
	maxBudget := modelInfo.Thinking.Max
	if minBudget > 0 && budget < minBudget {
		budget = minBudget
	}
	if maxBudget > 0 && budget > maxBudget {
		budget = maxBudget
	}
	if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() && maxTokens.Type == gjson.Number && maxTokens.Int() > 0 && int(maxTokens.Int()) <= budget {
		adjusted := int(maxTokens.Int()) - 1
		if minBudget <= 0 || adjusted >= minBudget {
			budget = adjusted
		}
	}
	if budget != int(budgetResult.Int()) {
		body, _ = sjson.SetBytes(body, "thinking.budget_tokens", budget)
	}
	return body
}

func repairClaudeContextManagement(body []byte) []byte {
	thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
	if thinkingType == "enabled" || thinkingType == "adaptive" || thinkingType == "auto" {
		return body
	}

	edits := gjson.GetBytes(body, "context_management.edits")
	if !edits.Exists() || !edits.IsArray() {
		return body
	}

	cleaned := make([]any, 0, len(edits.Array()))
	removed := false
	edits.ForEach(func(_, edit gjson.Result) bool {
		if edit.IsObject() && edit.Get("type").String() == "clear_thinking_20251015" {
			removed = true
			return true
		}
		cleaned = append(cleaned, edit.Value())
		return true
	})
	if !removed {
		return body
	}
	if len(cleaned) == 0 {
		body, _ = sjson.DeleteBytes(body, "context_management.edits")
		if cm := gjson.GetBytes(body, "context_management"); cm.Exists() && cm.IsObject() && len(cm.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "context_management")
		}
		return body
	}
	body, _ = sjson.SetBytes(body, "context_management.edits", cleaned)
	return body
}

func repairClaudeMessageContent(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}

	messages.ForEach(func(messageIndex, message gjson.Result) bool {
		content := message.Get("content")
		if !content.Exists() || !content.IsArray() {
			return true
		}
		cleaned := make([]any, 0, len(content.Array()))
		removed := false
		content.ForEach(func(_, block gjson.Result) bool {
			if block.IsObject() && block.Get("type").String() == "text" && block.Get("text").Exists() && block.Get("text").String() == "" {
				removed = true
				return true
			}
			cleaned = append(cleaned, block.Value())
			return true
		})
		if removed && len(cleaned) > 0 {
			body, _ = sjson.SetBytes(body, fmt.Sprintf("messages.%d.content", messageIndex.Int()), cleaned)
		}
		return true
	})
	return body
}

func inferClaudeBetasFromBody(body []byte, betas []string) []string {
	if gjson.GetBytes(body, "context_management").Exists() {
		betas = appendClaudeBetaToken(betas, "context-management-2025-06-27")
	}
	return betas
}

func appendClaudeBetaToken(tokens []string, beta string) []string {
	beta = strings.TrimSpace(beta)
	if beta == "" {
		return tokens
	}
	for _, token := range tokens {
		if strings.TrimSpace(token) == beta {
			return tokens
		}
	}
	return append(tokens, beta)
}

var claudeDroppedBetaTokens = map[string]struct{}{}

var claudeCodeDefaultBetaTokens = []string{
	"claude-code-20250219",
	"interleaved-thinking-2025-05-14",
	"effort-2025-11-24",
}

var claudeCodeFableBetaTokens = []string{
	"claude-code-20250219",
	"interleaved-thinking-2025-05-14",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	claudeCodeMidConversationSystemBeta,
	"advisor-tool-2026-03-01",
	"effort-2025-11-24",
	"fallback-credit-2026-06-01",
}

const (
	claudeCodeMidConversationSystemBeta = "mid-conversation-system-2026-04-07"
	claudeContext1MBeta                 = "context-1m-2025-08-07"
)

var claudeAllowedBetaTokens = func() map[string]struct{} {
	optional := []string{
		"oauth-2025-04-20",
		"advisor-tool-2026-03-01",
		"prompt-caching-scope-2026-01-05",
		"context-management-2025-06-27",
		claudeContext1MBeta,
		"extended-cache-ttl-2025-04-11",
		"fine-grained-tool-streaming-2025-05-14",
		"structured-outputs-2025-12-15",
		"fast-mode-2026-02-01",
		"redact-thinking-2026-02-12",
		"thinking-token-count-2026-05-13",
		"task-budgets-2026-03-13",
		"cache-diagnosis-2026-04-07",
		"server-side-fallback-2026-06-01",
		"fallback-credit-2026-06-01",
		claudeCodeMidConversationSystemBeta,
	}
	allowed := make(map[string]struct{}, len(claudeCodeDefaultBetaTokens)+len(optional))
	for _, beta := range claudeCodeDefaultBetaTokens {
		allowed[beta] = struct{}{}
	}
	for _, beta := range optional {
		allowed[beta] = struct{}{}
	}
	return allowed
}()

var claudeBlockedUpstreamHeaderPrefixes = []string{
	"x-openclaw-",
	"x-hermes-",
	"acp-",
	"x-claude-relay-",
	"x-litellm-",
	"helicone-",
	"x-portkey-",
	"cf-aig-",
	"x-kong-",
	"x-bt-",
	"x-cpa-",
	"x-cliproxy-",
	"x-sub2api-",
	"x-newapi-",
	"x-oneapi-",
	"x-openrouter-",
	"x-lobe-",
	"x-cherry-",
	"x-fastapi-",
	"x-chatnio-",
	"x-aigateway-",
	"x-llm-",
}

func filterClaudeBetaHeader(header string) string {
	return filterClaudeBetaHeaderWithOptions(header, false)
}

func filterClaudeBetaHeaderWithOptions(header string, dropContext1M bool) string {
	parts := strings.Split(header, ",")
	filtered := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		beta := strings.TrimSpace(part)
		if beta == "" {
			continue
		}
		if dropContext1M && beta == claudeContext1MBeta {
			continue
		}
		if _, drop := claudeDroppedBetaTokens[beta]; drop {
			continue
		}
		if _, allowed := claudeAllowedBetaTokens[beta]; !allowed {
			continue
		}
		if _, ok := seen[beta]; ok {
			continue
		}
		seen[beta] = struct{}{}
		filtered = append(filtered, beta)
	}
	return strings.Join(filtered, ",")
}

func filterClaudeBetaList(betas []string) []string {
	return filterClaudeBetaListWithOptions(betas, false)
}

func filterClaudeBetaListWithOptions(betas []string, dropContext1M bool) []string {
	if len(betas) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(betas))
	for _, beta := range betas {
		beta = strings.TrimSpace(beta)
		if beta == "" {
			continue
		}
		if dropContext1M && beta == claudeContext1MBeta {
			continue
		}
		if _, drop := claudeDroppedBetaTokens[beta]; drop {
			continue
		}
		if _, allowed := claudeAllowedBetaTokens[beta]; !allowed {
			continue
		}
		filtered = append(filtered, beta)
	}
	return filtered
}

func sanitizeClaudeUpstreamHeaders(headers http.Header) {
	for key := range headers {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if _, allowed := claudeAllowedUpstreamHeaderNames[lowerKey]; !allowed {
			headers.Del(key)
			continue
		}
		for _, prefix := range claudeBlockedUpstreamHeaderPrefixes {
			if strings.HasPrefix(lowerKey, prefix) {
				headers.Del(key)
				break
			}
		}
	}
}

type compositeReadCloser struct {
	io.Reader
	closers []func() error
}

func (c *compositeReadCloser) Close() error {
	var firstErr error
	for i := range c.closers {
		if c.closers[i] == nil {
			continue
		}
		if err := c.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// peekableBody wraps a bufio.Reader around the original ReadCloser so that
// magic bytes can be inspected without consuming them from the stream.
type peekableBody struct {
	*bufio.Reader
	closer io.Closer
}

func (p *peekableBody) Close() error {
	return p.closer.Close()
}

func decodeResponseBody(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error) {
	if body == nil {
		return nil, fmt.Errorf("response body is nil")
	}
	if contentEncoding == "" {
		// No Content-Encoding header.  Attempt best-effort magic-byte detection to
		// handle misbehaving upstreams that compress without setting the header.
		// Only gzip (1f 8b) and zstd (28 b5 2f fd) have reliable magic sequences;
		// br and deflate have none and are left as-is.
		// The bufio wrapper preserves unread bytes so callers always see the full
		// stream regardless of whether decompression was applied.
		pb := &peekableBody{Reader: bufio.NewReader(body), closer: body}
		magic, peekErr := pb.Peek(4)
		if peekErr == nil || (peekErr == io.EOF && len(magic) >= 2) {
			switch {
			case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
				gzipReader, gzErr := gzip.NewReader(pb)
				if gzErr != nil {
					_ = pb.Close()
					return nil, fmt.Errorf("magic-byte gzip: failed to create reader: %w", gzErr)
				}
				return &compositeReadCloser{
					Reader: gzipReader,
					closers: []func() error{
						gzipReader.Close,
						pb.Close,
					},
				}, nil
			case len(magic) >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
				decoder, zdErr := zstd.NewReader(pb)
				if zdErr != nil {
					_ = pb.Close()
					return nil, fmt.Errorf("magic-byte zstd: failed to create reader: %w", zdErr)
				}
				return &compositeReadCloser{
					Reader: decoder,
					closers: []func() error{
						func() error { decoder.Close(); return nil },
						pb.Close,
					},
				}, nil
			}
		}
		return pb, nil
	}
	encodings := strings.Split(contentEncoding, ",")
	for _, raw := range encodings {
		encoding := strings.TrimSpace(strings.ToLower(raw))
		switch encoding {
		case "", "identity":
			continue
		case "gzip":
			gzipReader, err := gzip.NewReader(body)
			if err != nil {
				_ = body.Close()
				return nil, fmt.Errorf("failed to create gzip reader: %w", err)
			}
			return &compositeReadCloser{
				Reader: gzipReader,
				closers: []func() error{
					gzipReader.Close,
					func() error { return body.Close() },
				},
			}, nil
		case "deflate":
			deflateReader := flate.NewReader(body)
			return &compositeReadCloser{
				Reader: deflateReader,
				closers: []func() error{
					deflateReader.Close,
					func() error { return body.Close() },
				},
			}, nil
		case "br":
			return &compositeReadCloser{
				Reader: brotli.NewReader(body),
				closers: []func() error{
					func() error { return body.Close() },
				},
			}, nil
		case "zstd":
			decoder, err := zstd.NewReader(body)
			if err != nil {
				_ = body.Close()
				return nil, fmt.Errorf("failed to create zstd reader: %w", err)
			}
			return &compositeReadCloser{
				Reader: decoder,
				closers: []func() error{
					func() error { decoder.Close(); return nil },
					func() error { return body.Close() },
				},
			}, nil
		default:
			continue
		}
	}
	return body, nil
}

func ginHeadersFromContext(ctx context.Context) http.Header {
	if ctx == nil {
		return nil
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.Request.Header
	}
	return nil
}

func claudeCodeDefaultBetaTokensForModel(model string) []string {
	if claudeModelUsesAlwaysAdaptiveThinking(model) {
		return append([]string(nil), claudeCodeFableBetaTokens...)
	}
	if !claudeModelUsesMidConversationSystemBeta(model) {
		return append([]string(nil), claudeCodeDefaultBetaTokens...)
	}
	betas := make([]string, 0, len(claudeCodeDefaultBetaTokens)+1)
	for _, beta := range claudeCodeDefaultBetaTokens {
		if beta == "effort-2025-11-24" {
			betas = append(betas, claudeCodeMidConversationSystemBeta)
		}
		betas = append(betas, beta)
	}
	return betas
}

func claudeModelUsesMidConversationSystemBeta(model string) bool {
	model = strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if model == "" {
		return false
	}
	model = strings.ToLower(model)
	model = strings.TrimSuffix(model, "-thinking")
	return model == "claude-opus-4-8" || strings.HasPrefix(model, "claude-opus-4-8-")
}

func buildClaudeBetaHeader(ginHeaders http.Header, extraBetas []string, dropContext1M bool, model ...string) string {
	baseBetas := ""
	modelName := ""
	if len(model) > 0 {
		modelName = model[0]
	}
	for _, beta := range claudeCodeDefaultBetaTokensForModel(modelName) {
		baseBetas = appendClaudeBeta(baseBetas, beta)
	}
	for _, beta := range strings.Split(filterClaudeBetaHeaderWithOptions(ginHeaders.Get("Anthropic-Beta"), dropContext1M), ",") {
		baseBetas = appendClaudeBeta(baseBetas, beta)
	}
	for _, beta := range filterClaudeBetaListWithOptions(extraBetas, dropContext1M) {
		baseBetas = appendClaudeBeta(baseBetas, beta)
	}
	return filterClaudeBetaHeaderWithOptions(baseBetas, dropContext1M)
}

func applyClaudeHeaders(r *http.Request, auth *cliproxyauth.Auth, apiKey string, stream bool, extraBetas []string, cfg *config.Config, model ...string) {
	hdrDefault := func(cfgVal, fallback string) string {
		if cfgVal != "" {
			return cfgVal
		}
		return fallback
	}

	var hd config.ClaudeHeaderDefaults
	if cfg != nil {
		hd = cfg.ClaudeHeaderDefaults
	}

	useAPIKey := auth != nil && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != ""
	isAnthropicBase := r.URL != nil && strings.EqualFold(r.URL.Scheme, "https") && strings.EqualFold(r.URL.Host, "api.anthropic.com")
	ginHeaders := ginHeadersFromContext(r.Context())
	stabilizeDeviceProfile := helps.ClaudeDeviceProfileStabilizationEnabled(cfg)
	var deviceProfile helps.ClaudeDeviceProfile
	if stabilizeDeviceProfile {
		deviceProfile = helps.ResolveClaudeDeviceProfile(auth, apiKey, ginHeaders, cfg)
	}

	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)

	for _, headerName := range []string{
		"User-Agent",
		"X-Stainless-Package-Version",
		"X-Stainless-Runtime-Version",
		"X-Stainless-Os",
		"X-Stainless-Arch",
		"X-Stainless-Retry-Count",
		"X-Stainless-Runtime",
		"X-Stainless-Lang",
		"X-Stainless-Timeout",
		"X-App",
		"Anthropic-Version",
		"Anthropic-Beta",
		"Anthropic-Dangerous-Direct-Browser-Access",
		"X-Claude-Code-Session-Id",
		"Authorization",
		"x-api-key",
	} {
		r.Header.Del(headerName)
	}

	if isAnthropicBase && useAPIKey {
		r.Header.Set("x-api-key", apiKey)
	} else {
		r.Header.Set("Authorization", "Bearer "+apiKey)
	}
	r.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	r.Header.Set("Anthropic-Beta", buildClaudeBetaHeader(ginHeaders, extraBetas, isClaudeOAuthToken(apiKey), model...))
	r.Header.Set("X-App", "cli")
	r.Header.Set("X-Stainless-Retry-Count", "0")
	r.Header.Set("X-Stainless-Runtime", "node")
	r.Header.Set("X-Stainless-Lang", "js")
	r.Header.Set("X-Stainless-Timeout", hdrDefault(hd.Timeout, "600"))
	r.Header.Set("X-Claude-Code-Session-Id", helps.CachedSessionID(apiKey))
	r.Header.Set("Connection", "keep-alive")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	if stabilizeDeviceProfile {
		helps.ApplyClaudeDeviceProfileHeaders(r, deviceProfile)
	} else {
		helps.ApplyClaudeLegacyDeviceHeaders(r, ginHeaders, cfg)
	}
	sanitizeClaudeUpstreamHeaders(r.Header)
}

func appendClaudeBeta(header string, beta string) string {
	beta = strings.TrimSpace(beta)
	if beta == "" {
		return strings.TrimSpace(header)
	}
	for _, existing := range strings.Split(header, ",") {
		if strings.TrimSpace(existing) == beta {
			return strings.TrimSpace(header)
		}
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return beta
	}
	return header + "," + beta
}

func claudeCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func checkSystemInstructions(payload []byte) []byte {
	return checkSystemInstructionsWithSigningMode(payload, false, false, false, helps.DefaultClaudeVersion(nil), "", "")
}

func isClaudeOAuthToken(apiKey string) bool {
	return strings.Contains(apiKey, "sk-ant-oat")
}

// prepareClaudeOAuthToolNamesForUpstream applies the Claude OAuth tool-name
// transforms in the same order across request paths. Remap runs before prefixing
// so any future non-empty prefix still composes correctly with the per-request
// reverse map.
func prepareClaudeOAuthToolNamesForUpstream(body []byte, prefix string, prefixDisabled bool) ([]byte, oauthToolReverseMap) {
	body = stripForwardedThinkingBlocks(body)
	body, reverseMap := remapOAuthToolNames(body)
	if !prefixDisabled {
		body = applyClaudeToolPrefix(body, prefix)
	}
	return body, reverseMap
}

func stripForwardedThinkingBlocks(body []byte) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}

	original := body
	changed := false
	messages.ForEach(func(msgIndex, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			return true
		}

		var contentJSON strings.Builder
		contentJSON.WriteByte('[')
		count := 0
		removed := false
		content.ForEach(func(_, part gjson.Result) bool {
			if part.Get("type").String() == "thinking" {
				removed = true
				return true
			}
			if count > 0 {
				contentJSON.WriteByte(',')
			}
			contentJSON.WriteString(part.Raw)
			count++
			return true
		})
		contentJSON.WriteByte(']')
		if !removed {
			return true
		}

		path := fmt.Sprintf("messages.%d.content", msgIndex.Int())
		updated, err := sjson.SetRawBytes(body, path, []byte(contentJSON.String()))
		if err == nil {
			body = updated
			changed = true
		}
		return true
	})
	if !changed {
		return original
	}
	return body
}

// restoreClaudeOAuthToolNamesFromResponse undoes the Claude OAuth tool-name
// transforms for non-stream responses in reverse order.
func restoreClaudeOAuthToolNamesFromResponse(body []byte, prefix string, prefixDisabled bool, reverseMap oauthToolReverseMap) []byte {
	if !prefixDisabled {
		body = stripClaudeToolPrefixFromResponse(body, prefix)
	}
	return reverseRemapOAuthToolNames(body, reverseMap)
}

// restoreClaudeOAuthToolNamesFromStreamLine undoes the Claude OAuth tool-name
// transforms for SSE lines in reverse order.
func restoreClaudeOAuthToolNamesFromStreamLine(line []byte, prefix string, prefixDisabled bool, reverseMap oauthToolReverseMap) []byte {
	if !prefixDisabled {
		line = stripClaudeToolPrefixFromStreamLine(line, prefix)
	}
	return reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
}

func renameJSONKeyObject(raw string, mapping map[string]string, reverseMap *oauthToolReverseMap) (string, bool) {
	obj := gjson.Parse(raw)
	if !obj.IsObject() {
		return raw, false
	}

	var b strings.Builder
	b.WriteByte('{')
	count := 0
	changed := false
	obj.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if renamed, ok := mapping[name]; ok {
			if reverseMap != nil {
				reverseMap.recordProperty(name, renamed)
			}
			name = renamed
			changed = true
		}

		valueRaw := value.Raw
		if value.IsObject() {
			if renamedRaw, nestedChanged := renameJSONKeyObject(value.Raw, mapping, reverseMap); nestedChanged {
				valueRaw = renamedRaw
				changed = true
			}
		} else if value.IsArray() {
			if renamedRaw, nestedChanged := renameJSONKeyArray(value.Raw, mapping, reverseMap); nestedChanged {
				valueRaw = renamedRaw
				changed = true
			}
		}

		if count > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(name))
		b.WriteByte(':')
		b.WriteString(valueRaw)
		count++
		return true
	})
	b.WriteByte('}')

	if !changed {
		return raw, false
	}
	return b.String(), true
}

func renameJSONKeyArray(raw string, mapping map[string]string, reverseMap *oauthToolReverseMap) (string, bool) {
	arr := gjson.Parse(raw)
	if !arr.IsArray() {
		return raw, false
	}

	var b strings.Builder
	b.WriteByte('[')
	count := 0
	changed := false
	arr.ForEach(func(_, value gjson.Result) bool {
		valueRaw := value.Raw
		if value.IsObject() {
			if renamedRaw, nestedChanged := renameJSONKeyObject(value.Raw, mapping, reverseMap); nestedChanged {
				valueRaw = renamedRaw
				changed = true
			}
		} else if value.IsArray() {
			if renamedRaw, nestedChanged := renameJSONKeyArray(value.Raw, mapping, reverseMap); nestedChanged {
				valueRaw = renamedRaw
				changed = true
			}
		}
		if count > 0 {
			b.WriteByte(',')
		}
		b.WriteString(valueRaw)
		count++
		return true
	})
	b.WriteByte(']')

	if !changed {
		return raw, false
	}
	return b.String(), true
}

func renameRequiredArray(raw string, mapping map[string]string, reverseMap *oauthToolReverseMap) (string, bool) {
	arr := gjson.Parse(raw)
	if !arr.IsArray() {
		return raw, false
	}

	var b strings.Builder
	b.WriteByte('[')
	count := 0
	changed := false
	arr.ForEach(func(_, value gjson.Result) bool {
		item := value.String()
		if renamed, ok := mapping[item]; ok {
			if reverseMap != nil {
				reverseMap.recordProperty(item, renamed)
			}
			item = renamed
			changed = true
		}
		if count > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Quote(item))
		count++
		return true
	})
	b.WriteByte(']')

	if !changed {
		return raw, false
	}
	return b.String(), true
}

func normalizeOAuthToolSchema(toolJSON string, stripDescription bool, reverseMap *oauthToolReverseMap) (string, bool) {
	changed := false
	if stripDescription && gjson.Get(toolJSON, "description").Exists() {
		if updated, err := sjson.Set(toolJSON, "description", ""); err == nil {
			toolJSON = updated
			changed = true
		}
	}

	properties := gjson.Get(toolJSON, "input_schema.properties")
	if properties.Exists() && properties.IsObject() {
		if renamed, ok := renameJSONKeyObject(properties.Raw, oauthSchemaPropertyRenameMap, reverseMap); ok {
			if updated, err := sjson.SetRaw(toolJSON, "input_schema.properties", renamed); err == nil {
				toolJSON = updated
				changed = true
			}
		}
	}

	required := gjson.Get(toolJSON, "input_schema.required")
	if required.Exists() && required.IsArray() {
		if renamed, ok := renameRequiredArray(required.Raw, oauthSchemaPropertyRenameMap, reverseMap); ok {
			if updated, err := sjson.SetRaw(toolJSON, "input_schema.required", renamed); err == nil {
				toolJSON = updated
				changed = true
			}
		}
	}

	return toolJSON, changed
}

func renameJSONObjectAtPath(body []byte, path string, mapping map[string]string, reverseMap *oauthToolReverseMap) ([]byte, bool) {
	value := gjson.GetBytes(body, path)
	if !value.Exists() || !value.IsObject() {
		return body, false
	}
	renamed, changed := renameJSONKeyObject(value.Raw, mapping, reverseMap)
	if !changed {
		return body, false
	}
	updated, err := sjson.SetRawBytes(body, path, []byte(renamed))
	if err != nil {
		return body, false
	}
	return updated, true
}

func reversePartialJSONProperties(partial string, reverseMap oauthToolReverseMap) (string, bool) {
	if len(reverseMap.PropertyNames) == 0 || partial == "" {
		return partial, false
	}
	updated := partial
	for upstream, original := range reverseMap.PropertyNames {
		updated = strings.ReplaceAll(updated, strconv.Quote(upstream), strconv.Quote(original))
	}
	return updated, updated != partial
}

// remapOAuthToolNames renames third-party tool names to Claude Code equivalents
// and removes tools without an official counterpart. This prevents Anthropic from
// fingerprinting the request as a third-party client via tool naming patterns.
//
// It operates on: tools[].name, tool_choice.name, and all tool_use/tool_reference
// references in messages. Removed tools' corresponding tool_result blocks are preserved
// (they just become orphaned, which is safe for Claude).
//
// The returned map is keyed on the upstream (TitleCase) name and maps to the
// client-supplied original name. Callers MUST pass this map to the reverse
// functions so only names the client actually caused us to rewrite are restored
// on the response. A global reverse map (the previous implementation) incorrectly
// rewrote names the client originally sent in TitleCase (e.g. Amp CLI's `Bash`)
// when any OTHER tool in the same request triggered a forward rename (e.g.
// Amp's `glob`→`Glob`), because the global reverse map contained `Bash`→`bash`
// regardless of what the client originally sent.
func remapOAuthToolNames(body []byte) ([]byte, oauthToolReverseMap) {
	reverseMap := oauthToolReverseMap{
		ToolNames:     make(map[string]string, len(oauthToolRenameMap)),
		PropertyNames: make(map[string]string),
	}
	thirdPartyToolShape := false
	typedBuiltinToolNames := map[string]bool{}
	recordRename := func(original, renamed string) {
		reverseMap.recordTool(original, renamed)
	}

	// 1. Rewrite tools array in a single pass (if present).
	// IMPORTANT: do not mutate names first and then rebuild from an older gjson
	// snapshot. gjson results are snapshots of the original bytes; rebuilding from a
	// stale snapshot will preserve removals but overwrite renamed names back to their
	// original lowercase values.
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {

		var toolsJSON strings.Builder
		toolsJSON.WriteByte('[')
		toolCount := 0
		tools.ForEach(func(_, tool gjson.Result) bool {
			// Keep Anthropic built-in tools (web_search, code_execution, etc.) unchanged.
			if tool.Get("type").Exists() && tool.Get("type").String() != "" {
				if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
					typedBuiltinToolNames[name] = true
				}
				if toolCount > 0 {
					toolsJSON.WriteByte(',')
				}
				toolsJSON.WriteString(tool.Raw)
				toolCount++
				return true
			}

			name := tool.Get("name").String()
			if oauthToolsToRemove[name] {
				return true
			}

			toolJSON := tool.Raw
			renamedTool := false
			if newName, ok := oauthToolRenameMap[name]; ok && newName != name {
				updatedTool, err := sjson.Set(toolJSON, "name", newName)
				if err == nil {
					toolJSON = updatedTool
					recordRename(name, newName)
					renamedTool = true
					thirdPartyToolShape = true
				}
			}
			if normalizedTool, changed := normalizeOAuthToolSchema(toolJSON, renamedTool, &reverseMap); changed {
				toolJSON = normalizedTool
				thirdPartyToolShape = true
			}

			if toolCount > 0 {
				toolsJSON.WriteByte(',')
			}
			toolsJSON.WriteString(toolJSON)
			toolCount++
			return true
		})
		toolsJSON.WriteByte(']')
		body, _ = sjson.SetRawBytes(body, "tools", []byte(toolsJSON.String()))
	}

	// 2. Rename tool_choice if it references a known tool
	toolChoiceType := gjson.GetBytes(body, "tool_choice.type").String()
	if toolChoiceType == "tool" {
		tcName := gjson.GetBytes(body, "tool_choice.name").String()
		if typedBuiltinToolNames[tcName] {
			// Typed Anthropic built-ins such as web_search_20250305 are selected
			// by their declared name; rewriting them to Claude Code-style tool
			// aliases makes Anthropic reject the request as "tool not found".
		} else if oauthToolsToRemove[tcName] {
			// The chosen tool was removed from the tools array, so drop tool_choice to
			// keep the payload internally consistent and fall back to normal auto tool use.
			body, _ = sjson.DeleteBytes(body, "tool_choice")
		} else if newName, ok := oauthToolRenameMap[tcName]; ok && newName != tcName {
			body, _ = sjson.SetBytes(body, "tool_choice.name", newName)
			recordRename(tcName, newName)
			thirdPartyToolShape = true
		}
	} else if toolChoiceType == "any" && thirdPartyToolShape {
		body, _ = sjson.SetRawBytes(body, "tool_choice", []byte(`{"type":"auto"}`))
	}

	// 3. Rename tool references in messages
	messages := gjson.GetBytes(body, "messages")
	if messages.Exists() && messages.IsArray() {
		messages.ForEach(func(msgIndex, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, part gjson.Result) bool {
				partType := part.Get("type").String()
				switch partType {
				case "tool_use":
					name := part.Get("name").String()
					if typedBuiltinToolNames[name] {
						return true
					}
					if newName, ok := oauthToolRenameMap[name]; ok && newName != name {
						path := fmt.Sprintf("messages.%d.content.%d.name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
						recordRename(name, newName)
						thirdPartyToolShape = true
					}
					inputPath := fmt.Sprintf("messages.%d.content.%d.input", msgIndex.Int(), contentIndex.Int())
					if updatedBody, changed := renameJSONObjectAtPath(body, inputPath, oauthSchemaPropertyRenameMap, &reverseMap); changed {
						body = updatedBody
						thirdPartyToolShape = true
					}
				case "tool_reference":
					toolName := part.Get("tool_name").String()
					if typedBuiltinToolNames[toolName] {
						return true
					}
					if newName, ok := oauthToolRenameMap[toolName]; ok && newName != toolName {
						path := fmt.Sprintf("messages.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int())
						body, _ = sjson.SetBytes(body, path, newName)
						recordRename(toolName, newName)
						thirdPartyToolShape = true
					}
				case "tool_result":
					// Handle nested tool_reference blocks inside tool_result.content[]
					toolID := part.Get("tool_use_id").String()
					_ = toolID // tool_use_id stays as-is
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() == "tool_reference" {
								nestedToolName := nestedPart.Get("tool_name").String()
								if typedBuiltinToolNames[nestedToolName] {
									return true
								}
								if newName, ok := oauthToolRenameMap[nestedToolName]; ok && newName != nestedToolName {
									nestedPath := fmt.Sprintf("messages.%d.content.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int(), nestedIndex.Int())
									body, _ = sjson.SetBytes(body, nestedPath, newName)
									recordRename(nestedToolName, newName)
									thirdPartyToolShape = true
								}
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})
	}

	return body, reverseMap
}

// reverseRemapOAuthToolNames reverses the tool name mapping for non-stream responses
// using the per-request map produced by remapOAuthToolNames. Names the client sent
// that were NOT forward-renamed are passed through unchanged.
func reverseRemapOAuthToolNames(body []byte, reverseMap oauthToolReverseMap) []byte {
	if reverseMap.empty() {
		return body
	}
	content := gjson.GetBytes(body, "content")
	if !content.Exists() || !content.IsArray() {
		return body
	}
	content.ForEach(func(index, part gjson.Result) bool {
		partType := part.Get("type").String()
		switch partType {
		case "tool_use":
			name := part.Get("name").String()
			if origName, ok := reverseMap.ToolNames[name]; ok {
				path := fmt.Sprintf("content.%d.name", index.Int())
				body, _ = sjson.SetBytes(body, path, origName)
			}
			inputPath := fmt.Sprintf("content.%d.input", index.Int())
			if updatedBody, changed := renameJSONObjectAtPath(body, inputPath, reverseMap.PropertyNames, nil); changed {
				body = updatedBody
			}
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			if origName, ok := reverseMap.ToolNames[toolName]; ok {
				path := fmt.Sprintf("content.%d.tool_name", index.Int())
				body, _ = sjson.SetBytes(body, path, origName)
			}
		}
		return true
	})
	return body
}

// reverseRemapOAuthToolNamesFromStreamLine reverses the tool name mapping for SSE
// stream lines, using the per-request reverseMap produced by remapOAuthToolNames.
func reverseRemapOAuthToolNamesFromStreamLine(line []byte, reverseMap oauthToolReverseMap) []byte {
	if reverseMap.empty() {
		return line
	}
	payload := helps.JSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}

	updated := payload
	changed := false

	contentBlock := gjson.GetBytes(updated, "content_block")
	if contentBlock.Exists() {
		blockType := contentBlock.Get("type").String()
		switch blockType {
		case "tool_use":
			name := contentBlock.Get("name").String()
			if origName, ok := reverseMap.ToolNames[name]; ok {
				next, err := sjson.SetBytes(updated, "content_block.name", origName)
				if err == nil {
					updated = next
					changed = true
				}
			}
			if next, ok := renameJSONObjectAtPath(updated, "content_block.input", reverseMap.PropertyNames, nil); ok {
				updated = next
				changed = true
			}
		case "tool_reference":
			toolName := contentBlock.Get("tool_name").String()
			if origName, ok := reverseMap.ToolNames[toolName]; ok {
				next, err := sjson.SetBytes(updated, "content_block.tool_name", origName)
				if err == nil {
					updated = next
					changed = true
				}
			}
		}
	}

	if gjson.GetBytes(updated, "delta.type").String() == "input_json_delta" {
		partial := gjson.GetBytes(updated, "delta.partial_json").String()
		if reversedPartial, ok := reversePartialJSONProperties(partial, reverseMap); ok {
			next, err := sjson.SetBytes(updated, "delta.partial_json", reversedPartial)
			if err == nil {
				updated = next
				changed = true
			}
		}
	}

	if !changed {
		return line
	}

	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return append([]byte("data: "), updated...)
	}
	return updated
}

func applyClaudeToolPrefix(body []byte, prefix string) []byte {
	if prefix == "" {
		return body
	}

	// Collect built-in tool names from the authoritative fallback seed list and
	// augment it with any typed built-ins present in the current request body.
	builtinTools := helps.AugmentClaudeBuiltinToolRegistry(body, nil)

	if tools := gjson.GetBytes(body, "tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(index, tool gjson.Result) bool {
			// Skip built-in tools (web_search, code_execution, etc.) which have
			// a "type" field and require their name to remain unchanged.
			if tool.Get("type").Exists() && tool.Get("type").String() != "" {
				if n := tool.Get("name").String(); n != "" {
					builtinTools[n] = true
				}
				return true
			}
			name := tool.Get("name").String()
			if name == "" || strings.HasPrefix(name, prefix) {
				return true
			}
			path := fmt.Sprintf("tools.%d.name", index.Int())
			body, _ = sjson.SetBytes(body, path, prefix+name)
			return true
		})
	}

	if gjson.GetBytes(body, "tool_choice.type").String() == "tool" {
		name := gjson.GetBytes(body, "tool_choice.name").String()
		if name != "" && !strings.HasPrefix(name, prefix) && !builtinTools[name] {
			body, _ = sjson.SetBytes(body, "tool_choice.name", prefix+name)
		}
	}

	if messages := gjson.GetBytes(body, "messages"); messages.Exists() && messages.IsArray() {
		messages.ForEach(func(msgIndex, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.Exists() || !content.IsArray() {
				return true
			}
			content.ForEach(func(contentIndex, part gjson.Result) bool {
				partType := part.Get("type").String()
				switch partType {
				case "tool_use":
					name := part.Get("name").String()
					if name == "" || strings.HasPrefix(name, prefix) || builtinTools[name] {
						return true
					}
					path := fmt.Sprintf("messages.%d.content.%d.name", msgIndex.Int(), contentIndex.Int())
					body, _ = sjson.SetBytes(body, path, prefix+name)
				case "tool_reference":
					toolName := part.Get("tool_name").String()
					if toolName == "" || strings.HasPrefix(toolName, prefix) || builtinTools[toolName] {
						return true
					}
					path := fmt.Sprintf("messages.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int())
					body, _ = sjson.SetBytes(body, path, prefix+toolName)
				case "tool_result":
					// Handle nested tool_reference blocks inside tool_result.content[]
					nestedContent := part.Get("content")
					if nestedContent.Exists() && nestedContent.IsArray() {
						nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
							if nestedPart.Get("type").String() == "tool_reference" {
								nestedToolName := nestedPart.Get("tool_name").String()
								if nestedToolName != "" && !strings.HasPrefix(nestedToolName, prefix) && !builtinTools[nestedToolName] {
									nestedPath := fmt.Sprintf("messages.%d.content.%d.content.%d.tool_name", msgIndex.Int(), contentIndex.Int(), nestedIndex.Int())
									body, _ = sjson.SetBytes(body, nestedPath, prefix+nestedToolName)
								}
							}
							return true
						})
					}
				}
				return true
			})
			return true
		})
	}

	return body
}

func stripClaudeToolPrefixFromResponse(body []byte, prefix string) []byte {
	if prefix == "" {
		return body
	}
	content := gjson.GetBytes(body, "content")
	if !content.Exists() || !content.IsArray() {
		return body
	}
	content.ForEach(func(index, part gjson.Result) bool {
		partType := part.Get("type").String()
		switch partType {
		case "tool_use":
			name := part.Get("name").String()
			if !strings.HasPrefix(name, prefix) {
				return true
			}
			path := fmt.Sprintf("content.%d.name", index.Int())
			body, _ = sjson.SetBytes(body, path, strings.TrimPrefix(name, prefix))
		case "tool_reference":
			toolName := part.Get("tool_name").String()
			if !strings.HasPrefix(toolName, prefix) {
				return true
			}
			path := fmt.Sprintf("content.%d.tool_name", index.Int())
			body, _ = sjson.SetBytes(body, path, strings.TrimPrefix(toolName, prefix))
		case "tool_result":
			// Handle nested tool_reference blocks inside tool_result.content[]
			nestedContent := part.Get("content")
			if nestedContent.Exists() && nestedContent.IsArray() {
				nestedContent.ForEach(func(nestedIndex, nestedPart gjson.Result) bool {
					if nestedPart.Get("type").String() == "tool_reference" {
						nestedToolName := nestedPart.Get("tool_name").String()
						if strings.HasPrefix(nestedToolName, prefix) {
							nestedPath := fmt.Sprintf("content.%d.content.%d.tool_name", index.Int(), nestedIndex.Int())
							body, _ = sjson.SetBytes(body, nestedPath, strings.TrimPrefix(nestedToolName, prefix))
						}
					}
					return true
				})
			}
		}
		return true
	})
	return body
}

func stripClaudeToolPrefixFromStreamLine(line []byte, prefix string) []byte {
	if prefix == "" {
		return line
	}
	payload := helps.JSONPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return line
	}
	contentBlock := gjson.GetBytes(payload, "content_block")
	if !contentBlock.Exists() {
		return line
	}

	blockType := contentBlock.Get("type").String()
	var updated []byte
	var err error

	switch blockType {
	case "tool_use":
		name := contentBlock.Get("name").String()
		if !strings.HasPrefix(name, prefix) {
			return line
		}
		updated, err = sjson.SetBytes(payload, "content_block.name", strings.TrimPrefix(name, prefix))
		if err != nil {
			return line
		}
	case "tool_reference":
		toolName := contentBlock.Get("tool_name").String()
		if !strings.HasPrefix(toolName, prefix) {
			return line
		}
		updated, err = sjson.SetBytes(payload, "content_block.tool_name", strings.TrimPrefix(toolName, prefix))
		if err != nil {
			return line
		}
	default:
		return line
	}

	trimmed := bytes.TrimSpace(line)
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		return append([]byte("data: "), updated...)
	}
	return updated
}

// getClientUserAgent extracts the client User-Agent from the gin context.
func getClientUserAgent(ctx context.Context) string {
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return ginCtx.GetHeader("User-Agent")
	}
	return ""
}

// parseEntrypointFromUA extracts the entrypoint from a Claude Code User-Agent.
// Format: "claude-cli/x.y.z (external, sdk-cli)" → "sdk-cli"
// Format: "claude-cli/x.y.z (external, vscode)" → "vscode"
// Returns "cli" if parsing fails or UA is not Claude Code.
func parseEntrypointFromUA(userAgent string) string {
	// Find content inside parentheses
	start := strings.Index(userAgent, "(")
	end := strings.LastIndex(userAgent, ")")
	if start < 0 || end <= start {
		return "cli"
	}
	inner := userAgent[start+1 : end]
	// Split by comma, take the second part (entrypoint is at index 1, after USER_TYPE)
	// Format: "(USER_TYPE, ENTRYPOINT[, extra...])"
	parts := strings.Split(inner, ",")
	if len(parts) >= 2 {
		ep := strings.TrimSpace(parts[1])
		if ep != "" {
			return ep
		}
	}
	return "cli"
}

// getWorkloadFromContext extracts workload identifier from the gin request headers.
func getWorkloadFromContext(ctx context.Context) string {
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		return strings.TrimSpace(ginCtx.GetHeader("X-CPA-Claude-Workload"))
	}
	return ""
}

// getCloakConfigFromAuth extracts cloak configuration from auth attributes.
// Returns (cloakMode, strictMode, sensitiveWords, cacheUserID).
func getCloakConfigFromAuth(auth *cliproxyauth.Auth) (string, bool, []string, bool) {
	if auth == nil || auth.Attributes == nil {
		return "always", false, nil, true
	}

	cloakMode := auth.Attributes["cloak_mode"]
	if cloakMode == "" {
		cloakMode = "always"
	}

	strictMode := strings.ToLower(auth.Attributes["cloak_strict_mode"]) == "true"

	var sensitiveWords []string
	if wordsStr := auth.Attributes["cloak_sensitive_words"]; wordsStr != "" {
		sensitiveWords = strings.Split(wordsStr, ",")
		for i := range sensitiveWords {
			sensitiveWords[i] = strings.TrimSpace(sensitiveWords[i])
		}
	}

	cacheUserID := true
	if rawCacheUserID := strings.TrimSpace(auth.Attributes["cloak_cache_user_id"]); rawCacheUserID != "" {
		cacheUserID = strings.EqualFold(rawCacheUserID, "true") || rawCacheUserID == "1"
	}

	return cloakMode, strictMode, sensitiveWords, cacheUserID
}

// injectFakeUserID generates and injects a fake user ID into the request metadata.
// When useCache is false, a new user ID is generated for every call.
func injectFakeUserID(payload []byte, apiKey string, useCache bool, force bool) []byte {
	generateID := func() string {
		if useCache {
			return helps.CachedUserID(apiKey)
		}
		return helps.GenerateFakeUserID()
	}

	metadata := gjson.GetBytes(payload, "metadata")
	if !metadata.Exists() {
		payload, _ = sjson.SetBytes(payload, "metadata.user_id", generateID())
		return payload
	}

	existingUserID := gjson.GetBytes(payload, "metadata.user_id").String()
	if force || existingUserID == "" || !helps.IsValidUserID(existingUserID) {
		payload, _ = sjson.SetBytes(payload, "metadata.user_id", generateID())
	}
	return payload
}

// fingerprintSalt is the salt used by Claude Code to compute the 3-char build fingerprint.
const fingerprintSalt = "59cf53e54c78"

// computeFingerprint computes the 3-char build fingerprint that Claude Code embeds in cc_version.
// Algorithm: SHA256(salt + messageText[4] + messageText[7] + messageText[20] + version)[:3]
func computeFingerprint(messageText, version string) string {
	indices := [3]int{4, 7, 20}
	runes := []rune(messageText)
	var sb strings.Builder
	for _, idx := range indices {
		if idx < len(runes) {
			sb.WriteRune(runes[idx])
		} else {
			sb.WriteRune('0')
		}
	}
	input := fingerprintSalt + sb.String() + version
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:3]
}

func extractClaudeFingerprintMessageText(payload []byte) string {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return ""
	}

	text := ""
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}

		content := msg.Get("content")
		if content.Type == gjson.String {
			text = content.String()
			return false
		}
		if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() != "text" {
					return true
				}
				text = part.Get("text").String()
				return false
			})
		}
		return false
	})
	return text
}

// generateBillingHeader creates the x-anthropic-billing-header text block that
// real Claude Code prepends to every system prompt array.
// Format: x-anthropic-billing-header: cc_version=<ver>.<build>; cc_entrypoint=<ep>; cch=<hash>; [cc_workload=<wl>;]
func generateBillingHeader(payload []byte, experimentalCCHSigning bool, version, messageText, entrypoint, workload string) string {
	if entrypoint == "" {
		entrypoint = "cli"
	}
	buildHash := computeFingerprint(messageText, version)
	workloadPart := ""
	if workload != "" {
		workloadPart = fmt.Sprintf(" cc_workload=%s;", workload)
	}

	if experimentalCCHSigning {
		return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=00000;%s", version, buildHash, entrypoint, workloadPart)
	}

	// Generate a deterministic cch hash from the payload content (system + messages + tools).
	h := sha256.Sum256(payload)
	cch := hex.EncodeToString(h[:])[:5]
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=%s;%s", version, buildHash, entrypoint, cch, workloadPart)
}

func checkSystemInstructionsWithMode(payload []byte, strictMode bool) []byte {
	return checkSystemInstructionsWithSigningMode(payload, strictMode, false, false, helps.DefaultClaudeVersion(nil), "", "")
}

// checkSystemInstructionsWithSigningMode injects Claude Code-style system blocks:
//
//	system[0]: billing header (no cache_control)
//	system[1]: agent identifier (cache_control ephemeral, scope=org)
//	system[2]: runtime context prompt (cache_control ephemeral, scope=global)
//	system[3]: user system messages moved to first user message
func checkSystemInstructionsWithSigningMode(payload []byte, strictMode bool, experimentalCCHSigning bool, oauthMode bool, version, entrypoint, workload string) []byte {
	return checkSystemInstructionsWithSigningModeForced(payload, strictMode, experimentalCCHSigning, oauthMode, version, entrypoint, workload, false)
}

func checkSystemInstructionsWithSigningModeForced(payload []byte, strictMode bool, experimentalCCHSigning bool, oauthMode bool, version, entrypoint, workload string, forceBilling bool) []byte {
	system := gjson.GetBytes(payload, "system")

	// Claude Code computes the cc_version build fingerprint from the first
	// user text, before any billing/system prompt injection.
	messageText := extractClaudeFingerprintMessageText(payload)

	// Skip if already injected
	firstText := gjson.GetBytes(payload, "system.0.text").String()
	if strings.HasPrefix(firstText, "x-anthropic-billing-header:") && !forceBilling {
		return payload
	}

	billingText := generateBillingHeader(payload, experimentalCCHSigning, version, messageText, entrypoint, workload)
	billingBlock := buildTextBlock(billingText, nil)

	// Build system blocks matching the current Claude Code Agent SDK shape.
	cacheControl := map[string]string{"type": "ephemeral"}
	agentBlock := buildTextBlock(helps.ClaudeCodeAgentIdentity, cacheControl)
	runtimeContextBlock := buildTextBlock(helps.ClaudeCodeRuntimeContextPrompt(time.Now().Format("2006-01-02")), cacheControl)

	systemResult := "[" + billingBlock + "," + agentBlock + "," + runtimeContextBlock + "]"
	payload, _ = sjson.SetRawBytes(payload, "system", []byte(systemResult))

	// Collect user system instructions and prepend to first user message
	if !strictMode {
		var userSystemParts []string
		var forwardedCacheControl map[string]string
		if system.IsArray() {
			system.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "text" {
					txt := strings.TrimSpace(part.Get("text").String())
					if shouldForwardOriginalSystemText(txt, forceBilling) {
						userSystemParts = append(userSystemParts, txt)
						forwardedCacheControl = validForwardedSystemCacheControl(part)
					}
				}
				return true
			})
		} else if system.Type == gjson.String && shouldForwardOriginalSystemText(strings.TrimSpace(system.String()), forceBilling) {
			userSystemParts = append(userSystemParts, strings.TrimSpace(system.String()))
		}

		if len(userSystemParts) > 0 {
			combined := strings.Join(userSystemParts, "\n\n")
			if oauthMode && forwardedCacheControl == nil {
				combined = sanitizeForwardedSystemPrompt(combined)
			}
			if strings.TrimSpace(combined) != "" {
				payload = prependToFirstUserMessage(payload, combined, forwardedCacheControl)
			}
		}
	}

	return payload
}

func validForwardedSystemCacheControl(part gjson.Result) map[string]string {
	cc := part.Get("cache_control")
	if !cc.IsObject() || cc.Get("type").String() != "ephemeral" {
		return nil
	}
	out := map[string]string{"type": "ephemeral"}
	switch ttl := cc.Get("ttl").String(); ttl {
	case "5m", "1h":
		out["ttl"] = ttl
	}
	return out
}

func shouldForwardOriginalSystemText(text string, forceBilling bool) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	if !forceBilling {
		return true
	}
	if strings.HasPrefix(text, "x-anthropic-billing-header:") {
		return false
	}
	if text == helps.ClaudeCodeAgentIdentity || text == "You are Claude Code, Anthropic's official CLI for Claude." {
		return false
	}
	if strings.Contains(text, helps.ClaudeCodeHarnessPrompt) {
		return false
	}
	if helps.IsClaudeCodeRuntimeContextPrompt(text) {
		return false
	}
	return true
}

// sanitizeForwardedSystemPrompt reduces forwarded third-party system context to a
// tiny neutral reminder for Claude OAuth cloaking. The goal is to preserve only
// the minimum tool/task guidance while removing virtually all client-specific
// prompt structure that Anthropic may classify as third-party agent traffic.
func sanitizeForwardedSystemPrompt(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return strings.TrimSpace(`Use the available tools when needed to help with software engineering tasks.
Keep responses concise and focused on the user's request.
Prefer acting on the user's task over describing product-specific workflows.`)
}

// buildTextBlock constructs a JSON text block object with proper escaping.
// Uses sjson.SetBytes to handle multi-line text, quotes, and control characters.
// cacheControl is optional; pass nil to omit cache_control.
func buildTextBlock(text string, cacheControl map[string]string) string {
	block := []byte(`{"type":"text"}`)
	block, _ = sjson.SetBytes(block, "text", text)
	if cacheControl != nil && len(cacheControl) > 0 {
		// Build cache_control JSON manually to avoid sjson map marshaling issues.
		// sjson.SetBytes with map[string]string may not produce expected structure.
		cc := `{"type":"ephemeral"`
		if t, ok := cacheControl["ttl"]; ok {
			cc += fmt.Sprintf(`,"ttl":"%s"`, t)
		}
		cc += "}"
		block, _ = sjson.SetRawBytes(block, "cache_control", []byte(cc))
	}
	return string(block)
}

// prependToFirstUserMessage prepends text content to the first user message.
// This avoids putting non-Claude-Code system instructions in system[] which
// triggers Anthropic's extra usage billing for OAuth-proxied requests.
func prependToFirstUserMessage(payload []byte, text string, cacheControl map[string]string) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	// Find the first user message index
	firstUserIdx := -1
	messages.ForEach(func(idx, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			firstUserIdx = int(idx.Int())
			return false
		}
		return true
	})

	if firstUserIdx < 0 {
		return payload
	}

	prefixBlock := fmt.Sprintf(`<system-reminder>
As you answer the user's questions, you can use the following context from the system:
%s

IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>
`, text)

	contentPath := fmt.Sprintf("messages.%d.content", firstUserIdx)
	content := gjson.GetBytes(payload, contentPath)

	if content.IsArray() {
		newBlock := fmt.Sprintf(`{"type":"text","text":%q}`, prefixBlock)
		if cacheControl != nil {
			newBlock = buildTextBlock(prefixBlock, cacheControl)
		}
		var newArray string
		if content.Raw == "[]" || content.Raw == "" {
			newArray = "[" + newBlock + "]"
		} else {
			newArray = "[" + newBlock + "," + content.Raw[1:]
		}
		payload, _ = sjson.SetRawBytes(payload, contentPath, []byte(newArray))
	} else if content.Type == gjson.String {
		if cacheControl == nil {
			newText := prefixBlock + content.String()
			payload, _ = sjson.SetBytes(payload, contentPath, newText)
		} else {
			newBlock := buildTextBlock(prefixBlock, cacheControl)
			originalBlock := buildTextBlock(content.String(), nil)
			newArray := "[" + newBlock + "," + originalBlock + "]"
			payload, _ = sjson.SetRawBytes(payload, contentPath, []byte(newArray))
		}
	}

	return payload
}

func resolveClaudeBillingVersion(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, apiKey string) string {
	fallback := helps.DefaultClaudeVersion(cfg)
	if !helps.ClaudeDeviceProfileStabilizationEnabled(cfg) {
		return fallback
	}
	profile := helps.ResolveClaudeDeviceProfile(auth, apiKey, ginHeadersFromContext(ctx), cfg)
	return helps.ClaudeDeviceProfileVersion(profile, fallback)
}

// applyCloaking applies cloaking transformations to the payload based on config and client.
// Cloaking includes: system prompt injection, fake user ID, and sensitive word obfuscation.
func applyCloaking(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, payload []byte, model string, apiKey string) ([]byte, bool) {
	clientUserAgent := getClientUserAgent(ctx)
	// Enable cch signing for OAuth tokens by default (not just experimental flag).
	oauthToken := isClaudeOAuthToken(apiKey)
	useCCHSigning := oauthToken || experimentalCCHSigningEnabled(cfg, auth)

	// Get cloak config from ClaudeKey configuration
	cloakCfg := resolveClaudeKeyCloakConfig(cfg, auth)
	attrMode, attrStrict, attrWords, attrCache := getCloakConfigFromAuth(auth)

	// Determine cloak settings
	cloakMode := attrMode
	strictMode := attrStrict
	sensitiveWords := attrWords
	cacheUserID := attrCache

	if cloakCfg != nil {
		if mode := strings.TrimSpace(cloakCfg.Mode); mode != "" {
			cloakMode = mode
		}
		if cloakCfg.StrictMode {
			strictMode = true
		}
		if len(cloakCfg.SensitiveWords) > 0 {
			sensitiveWords = cloakCfg.SensitiveWords
		}
		if cloakCfg.CacheUserID != nil {
			cacheUserID = *cloakCfg.CacheUserID
		}
	}

	// Determine if cloaking should be applied
	clientUserID := gjson.GetBytes(payload, "metadata.user_id").String()
	if !helps.ShouldCloakRequest(cloakMode, clientUserAgent, clientUserID) {
		return payload, false
	}

	// Skip system instructions for claude-3-5-haiku models
	if !strings.HasPrefix(model, "claude-3-5-haiku") {
		billingVersion := resolveClaudeBillingVersion(ctx, cfg, auth, apiKey)
		entrypoint := parseEntrypointFromUA(clientUserAgent)
		workload := getWorkloadFromContext(ctx)
		payload = checkSystemInstructionsWithSigningModeForced(payload, strictMode, useCCHSigning, oauthToken, billingVersion, entrypoint, workload, true)
	}

	// Inject fake user ID
	payload = injectFakeUserID(payload, apiKey, cacheUserID, true)

	// Apply sensitive word obfuscation
	if len(sensitiveWords) > 0 {
		matcher := helps.BuildSensitiveWordMatcher(sensitiveWords)
		payload = helps.ObfuscateSensitiveWords(payload, matcher)
	}

	return payload, true
}

// ensureCacheControl injects cache_control breakpoints into the payload for optimal prompt caching.
// According to Anthropic's documentation, cache prefixes are created in order: tools -> system -> messages.
// This function adds cache_control to:
// 1. The LAST tool in the tools array (caches all tool definitions)
// 2. The LAST system prompt element
// 3. The SECOND-TO-LAST user turn (caches conversation history for multi-turn)
//
// Up to 4 cache breakpoints are allowed per request. Tools, System, and Messages are INDEPENDENT breakpoints.
// This enables up to 90% cost reduction on cached tokens (cache read = 0.1x base price).
// See: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
func ensureCacheControl(payload []byte) []byte {
	// 1. Inject cache_control into the LAST tool (caches all tool definitions)
	// Tools are cached first in the hierarchy, so this is the most important breakpoint.
	payload = injectToolsCacheControl(payload)

	// 2. Inject cache_control into the LAST system prompt element
	// System is the second level in the cache hierarchy.
	payload = injectSystemCacheControl(payload)

	// 3. Inject cache_control into messages for multi-turn conversation caching
	// This caches the conversation history up to the second-to-last user turn.
	payload = injectMessagesCacheControl(payload)

	return payload
}

func countCacheControls(payload []byte) int {
	count := 0

	// Check system
	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}

	// Check tools
	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
	}

	// Check messages
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, item gjson.Result) bool {
					if item.Get("cache_control").Exists() {
						count++
					}
					return true
				})
			}
			return true
		})
	}

	return count
}

// normalizeCacheControlTTL ensures cache_control TTL values don't violate the
// prompt-caching-scope-2026-01-05 ordering constraint: a 1h-TTL block must not
// appear after a 5m-TTL block anywhere in the evaluation order.
//
// Anthropic evaluates blocks in order: tools → system (index 0..N) → messages.
// Within each section, blocks are evaluated in array order. A 5m (default) block
// followed by a 1h block at ANY later position is an error — including within
// the same section (e.g. system[1]=5m then system[3]=1h).
//
// Strategy: when the request contains ANY explicit 1h block (only clients set
// ttl="1h"; proxy-injected blocks omit ttl), upgrade EVERY ephemeral block to
// 1h. This preserves the client's requested 1h caching instead of silently
// downgrading it to 5m, and trivially satisfies the ordering constraint because
// all blocks end up at the same tier. When no 1h block is present, the payload
// is returned unchanged so clients that never asked for extended caching keep
// the default (5m) behavior.
func normalizeCacheControlTTL(payload []byte) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	original := payload
	modified := false

	type blockRef struct {
		path string
		cc   gjson.Result
	}
	var blocks []blockRef
	hasOneHour := false

	collectBlock := func(path string, obj gjson.Result) {
		cc := obj.Get("cache_control")
		if !cc.IsObject() {
			return
		}
		blocks = append(blocks, blockRef{path: path, cc: cc})
		if ttl := cc.Get("ttl"); ttl.Type == gjson.String && ttl.String() == "1h" {
			hasOneHour = true
		}
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			collectBlock(fmt.Sprintf("tools.%d", int(idx.Int())), item)
			return true
		})
	}

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			collectBlock(fmt.Sprintf("system.%d", int(idx.Int())), item)
			return true
		})
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				collectBlock(fmt.Sprintf("messages.%d.content.%d", int(msgIdx.Int()), int(itemIdx.Int())), item)
				return true
			})
			return true
		})
	}

	if !hasOneHour {
		return original
	}

	for _, block := range blocks {
		if ttl := block.cc.Get("ttl"); ttl.Type == gjson.String && ttl.String() == "1h" {
			continue
		}
		ttlPath := block.path + ".cache_control.ttl"
		updated, errSet := sjson.SetBytes(payload, ttlPath, "1h")
		if errSet != nil {
			continue
		}
		payload = updated
		modified = true
	}

	if !modified {
		return original
	}
	return payload
}

// enforceCacheControlLimit removes excess cache_control blocks from a payload
// so the total does not exceed the Anthropic API limit (currently 4).
//
// Anthropic evaluates cache breakpoints in order: tools → system → messages.
// The most valuable breakpoints are:
//  1. Last tool         — caches ALL tool definitions
//  2. Last system block — caches ALL system content
//  3. Recent messages   — cache conversation context
//
// Removal priority (strip lowest-value first):
//
//	Phase 1: system blocks earliest-first, preserving the last one.
//	Phase 2: tool blocks earliest-first, preserving the last one.
//	Phase 3: message content blocks earliest-first.
//	Phase 4: remaining system blocks (last system).
//	Phase 5: remaining tool blocks (last tool).
func enforceCacheControlLimit(payload []byte, maxBlocks int) []byte {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return payload
	}

	total := countCacheControls(payload)
	if total <= maxBlocks {
		return payload
	}

	excess := total - maxBlocks

	system := gjson.GetBytes(payload, "system")
	if system.IsArray() {
		lastIdx := -1
		system.ForEach(func(idx, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				lastIdx = int(idx.Int())
			}
			return true
		})
		if lastIdx >= 0 {
			system.ForEach(func(idx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				i := int(idx.Int())
				if i == lastIdx {
					return true
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("system.%d.cache_control", i)
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
		}
	}
	if excess <= 0 {
		return payload
	}

	tools := gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		lastIdx := -1
		tools.ForEach(func(idx, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				lastIdx = int(idx.Int())
			}
			return true
		})
		if lastIdx >= 0 {
			tools.ForEach(func(idx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				i := int(idx.Int())
				if i == lastIdx {
					return true
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("tools.%d.cache_control", i)
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
		}
	}
	if excess <= 0 {
		return payload
	}

	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		messages.ForEach(func(msgIdx, msg gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			content := msg.Get("content")
			if !content.IsArray() {
				return true
			}
			content.ForEach(func(itemIdx, item gjson.Result) bool {
				if excess <= 0 {
					return false
				}
				if !item.Get("cache_control").Exists() {
					return true
				}
				path := fmt.Sprintf("messages.%d.content.%d.cache_control", int(msgIdx.Int()), int(itemIdx.Int()))
				updated, errDel := sjson.DeleteBytes(payload, path)
				if errDel != nil {
					return true
				}
				payload = updated
				excess--
				return true
			})
			return true
		})
	}
	if excess <= 0 {
		return payload
	}

	system = gjson.GetBytes(payload, "system")
	if system.IsArray() {
		system.ForEach(func(idx, item gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			if !item.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("system.%d.cache_control", int(idx.Int()))
			updated, errDel := sjson.DeleteBytes(payload, path)
			if errDel != nil {
				return true
			}
			payload = updated
			excess--
			return true
		})
	}
	if excess <= 0 {
		return payload
	}

	tools = gjson.GetBytes(payload, "tools")
	if tools.IsArray() {
		tools.ForEach(func(idx, item gjson.Result) bool {
			if excess <= 0 {
				return false
			}
			if !item.Get("cache_control").Exists() {
				return true
			}
			path := fmt.Sprintf("tools.%d.cache_control", int(idx.Int()))
			updated, errDel := sjson.DeleteBytes(payload, path)
			if errDel != nil {
				return true
			}
			payload = updated
			excess--
			return true
		})
	}

	return payload
}

// injectMessagesCacheControl adds cache_control to the second-to-last user turn for multi-turn caching.
// Per Anthropic docs: "Place cache_control on the second-to-last User message to let the model reuse the earlier cache."
// This enables caching of conversation history, which is especially beneficial for long multi-turn conversations.
// Only adds cache_control if:
// - There are at least 2 user turns in the conversation
// - No message content already has cache_control
func injectMessagesCacheControl(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	// Check if ANY message content already has cache_control
	hasCacheControlInMessages := false
	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, item gjson.Result) bool {
				if item.Get("cache_control").Exists() {
					hasCacheControlInMessages = true
					return false
				}
				return true
			})
		}
		return !hasCacheControlInMessages
	})
	if hasCacheControlInMessages {
		return payload
	}

	// Find all user message indices
	var userMsgIndices []int
	messages.ForEach(func(index gjson.Result, msg gjson.Result) bool {
		if msg.Get("role").String() == "user" {
			userMsgIndices = append(userMsgIndices, int(index.Int()))
		}
		return true
	})

	// Need at least 2 user turns to cache the second-to-last
	if len(userMsgIndices) < 2 {
		return payload
	}

	// Get the second-to-last user message index
	secondToLastUserIdx := userMsgIndices[len(userMsgIndices)-2]

	// Get the content of this message
	contentPath := fmt.Sprintf("messages.%d.content", secondToLastUserIdx)
	content := gjson.GetBytes(payload, contentPath)

	if content.IsArray() {
		// Add cache_control to the last content block of this message
		contentCount := int(content.Get("#").Int())
		if contentCount > 0 {
			cacheControlPath := fmt.Sprintf("messages.%d.content.%d.cache_control", secondToLastUserIdx, contentCount-1)
			result, err := sjson.SetBytes(payload, cacheControlPath, map[string]string{"type": "ephemeral"})
			if err != nil {
				log.Warnf("failed to inject cache_control into messages: %v", err)
				return payload
			}
			payload = result
		}
	} else if content.Type == gjson.String {
		// Convert string content to array with cache_control
		text := content.String()
		newContent := []map[string]interface{}{
			{
				"type": "text",
				"text": text,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		}
		result, err := sjson.SetBytes(payload, contentPath, newContent)
		if err != nil {
			log.Warnf("failed to inject cache_control into message string content: %v", err)
			return payload
		}
		payload = result
	}

	return payload
}

// injectToolsCacheControl adds cache_control to the last tool in the tools array.
// Per Anthropic docs: "The cache_control parameter on the last tool definition caches all tool definitions."
// This only adds cache_control if NO tool in the array already has it.
func injectToolsCacheControl(payload []byte) []byte {
	tools := gjson.GetBytes(payload, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return payload
	}

	toolCount := int(tools.Get("#").Int())
	if toolCount == 0 {
		return payload
	}

	// Check if ANY tool already has cache_control - if so, don't modify tools
	hasCacheControlInTools := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("cache_control").Exists() {
			hasCacheControlInTools = true
			return false
		}
		return true
	})
	if hasCacheControlInTools {
		return payload
	}

	// Add cache_control to the last tool
	lastToolPath := fmt.Sprintf("tools.%d.cache_control", toolCount-1)
	result, err := sjson.SetBytes(payload, lastToolPath, map[string]string{"type": "ephemeral"})
	if err != nil {
		log.Warnf("failed to inject cache_control into tools array: %v", err)
		return payload
	}

	return result
}

// injectSystemCacheControl adds cache_control to the last element in the system prompt.
// Converts string system prompts to array format if needed.
// This only adds cache_control if NO system element already has it.
func injectSystemCacheControl(payload []byte) []byte {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		return payload
	}

	if system.IsArray() {
		count := int(system.Get("#").Int())
		if count == 0 {
			return payload
		}

		// Check if ANY system element already has cache_control
		hasCacheControlInSystem := false
		system.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				hasCacheControlInSystem = true
				return false
			}
			return true
		})
		if hasCacheControlInSystem {
			return payload
		}

		// Add cache_control to the last system element
		lastSystemPath := fmt.Sprintf("system.%d.cache_control", count-1)
		result, err := sjson.SetBytes(payload, lastSystemPath, map[string]string{"type": "ephemeral"})
		if err != nil {
			log.Warnf("failed to inject cache_control into system array: %v", err)
			return payload
		}
		payload = result
	} else if system.Type == gjson.String {
		// Convert string system prompt to array with cache_control
		// "system": "text" -> "system": [{"type": "text", "text": "text", "cache_control": {"type": "ephemeral"}}]
		text := system.String()
		newSystem := []map[string]interface{}{
			{
				"type": "text",
				"text": text,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		}
		result, err := sjson.SetBytes(payload, "system", newSystem)
		if err != nil {
			log.Warnf("failed to inject cache_control into system string: %v", err)
			return payload
		}
		payload = result
	}

	return payload
}

func ensureModelMaxTokens(body []byte, modelID string) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() {
		return body
	}

	modelID = strings.TrimSpace(modelID)
	for _, provider := range registry.GetGlobalRegistry().GetModelProviders(modelID) {
		if strings.EqualFold(provider, "claude") {
			return setClaudeModelMaxTokens(body, registry.GetGlobalRegistry().GetModelInfo(modelID, "claude"))
		}
	}
	if info := registry.LookupModelInfo(modelID, "claude"); info != nil && strings.EqualFold(info.Type, "claude") {
		return setClaudeModelMaxTokens(body, info)
	}

	return body
}

func setClaudeModelMaxTokens(body []byte, info *registry.ModelInfo) []byte {
	maxTokens := defaultModelMaxTokens
	if info != nil && info.MaxCompletionTokens > 0 {
		maxTokens = info.MaxCompletionTokens
	}
	body, _ = sjson.SetBytes(body, "max_tokens", maxTokens)
	return body
}
