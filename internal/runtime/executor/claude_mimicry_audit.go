package executor

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	xxHash64 "github.com/pierrec/xxHash/xxHash64"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ClaudeMimicryStatus string

const (
	ClaudeMimicryStatusAligned ClaudeMimicryStatus = "aligned"
	ClaudeMimicryStatusWarning ClaudeMimicryStatus = "warning"
	ClaudeMimicryStatusFailed  ClaudeMimicryStatus = "failed"
	ClaudeMimicryStatusWaiting ClaudeMimicryStatus = "waiting"
)

type ClaudeMimicryGuardAction string

const (
	ClaudeMimicryGuardActionAllow   ClaudeMimicryGuardAction = "allow"
	ClaudeMimicryGuardActionDegrade ClaudeMimicryGuardAction = "degrade"
	ClaudeMimicryGuardActionBlock   ClaudeMimicryGuardAction = "block"
)

type ClaudeMimicryBaseline struct {
	Model                 string   `json:"model"`
	Family                string   `json:"family"`
	ClaudeVersion         string   `json:"claude_version"`
	CCHSeed               string   `json:"cch_seed"`
	ExpectedBetaCount     int      `json:"expected_beta_count"`
	ExpectedBetas         []string `json:"expected_betas"`
	ExpectedSystemHashes  []string `json:"expected_system_hashes"`
	ExpectedTopFields     []string `json:"expected_top_fields"`
	ExpectedEfforts       []string `json:"expected_efforts"`
	ExpectedToolCountHint string   `json:"expected_tool_count_hint"`
}

type ClaudeMimicrySystemBlock struct {
	Index        int                 `json:"index"`
	Label        string              `json:"label"`
	Status       ClaudeMimicryStatus `json:"status"`
	Hash         string              `json:"hash,omitempty"`
	ExpectedHash string              `json:"expected_hash,omitempty"`
	Length       int                 `json:"length"`
	Detail       string              `json:"detail,omitempty"`
}

type ClaudeMimicrySystemAudit struct {
	Status ClaudeMimicryStatus        `json:"status"`
	Blocks []ClaudeMimicrySystemBlock `json:"blocks"`
}

type ClaudeMimicryCCHAudit struct {
	Status   ClaudeMimicryStatus `json:"status"`
	Signed   bool                `json:"signed"`
	Seed     string              `json:"seed"`
	Actual   string              `json:"actual,omitempty"`
	Expected string              `json:"expected,omitempty"`
	Detail   string              `json:"detail,omitempty"`
}

type ClaudeMimicryBetaAudit struct {
	Status        ClaudeMimicryStatus `json:"status"`
	Count         int                 `json:"count"`
	ExpectedCount int                 `json:"expected_count"`
	Tokens        []string            `json:"tokens"`
	Missing       []string            `json:"missing,omitempty"`
	Unexpected    []string            `json:"unexpected,omitempty"`
}

type ClaudeMimicryThinkingAudit struct {
	Status    ClaudeMimicryStatus `json:"status"`
	Type      string              `json:"type,omitempty"`
	Effort    string              `json:"effort,omitempty"`
	TopFields []string            `json:"top_fields,omitempty"`
	Detail    string              `json:"detail,omitempty"`
}

type ClaudeMimicryToolAudit struct {
	Status              ClaudeMimicryStatus `json:"status"`
	Count               int                 `json:"count"`
	Names               []string            `json:"names,omitempty"`
	SchemaSignature     string              `json:"schema_signature,omitempty"`
	ToolChoice          string              `json:"tool_choice,omitempty"`
	Unknown             []string            `json:"unknown,omitempty"`
	LeakyNames          []string            `json:"leaky_names,omitempty"`
	DescriptionWarnings []string            `json:"description_warnings,omitempty"`
	SchemaWarnings      []string            `json:"schema_warnings,omitempty"`
}

type ClaudeMimicryHeaderAudit struct {
	Status         ClaudeMimicryStatus `json:"status"`
	UserAgent      string              `json:"user_agent,omitempty"`
	PackageVersion string              `json:"package_version,omitempty"`
	RuntimeVersion string              `json:"runtime_version,omitempty"`
	OS             string              `json:"os,omitempty"`
	Arch           string              `json:"arch,omitempty"`
	Missing        []string            `json:"missing,omitempty"`
	Blocked        []string            `json:"blocked,omitempty"`
	Unexpected     []string            `json:"unexpected,omitempty"`
}

type ClaudeMimicryAuditSnapshot struct {
	Status      ClaudeMimicryStatus        `json:"status"`
	CheckedAt   time.Time                  `json:"checked_at"`
	Model       string                     `json:"model"`
	RequestPath string                     `json:"request_path"`
	Baseline    ClaudeMimicryBaseline      `json:"baseline"`
	System      ClaudeMimicrySystemAudit   `json:"system"`
	CCH         ClaudeMimicryCCHAudit      `json:"cch"`
	Betas       ClaudeMimicryBetaAudit     `json:"betas"`
	Thinking    ClaudeMimicryThinkingAudit `json:"thinking"`
	Tools       ClaudeMimicryToolAudit     `json:"tools"`
	Headers     ClaudeMimicryHeaderAudit   `json:"headers"`
	Warnings    []string                   `json:"warnings,omitempty"`
	Failures    []string                   `json:"failures,omitempty"`
}

type ClaudeMimicryGuardDecision struct {
	Mode    string                     `json:"mode"`
	Action  ClaudeMimicryGuardAction   `json:"action"`
	Blocked bool                       `json:"blocked"`
	Reasons []string                   `json:"reasons,omitempty"`
	Status  ClaudeMimicryStatus        `json:"status"`
	Audit   ClaudeMimicryAuditSnapshot `json:"audit"`
}

type ClaudeMimicryGuardPolicy struct {
	RequireSignedCCH    bool
	RequireSystemBlocks bool
}

type ClaudeMimicryEvent struct {
	ID                string                   `json:"id"`
	RequestID         string                   `json:"request_id,omitempty"`
	At                time.Time                `json:"at"`
	ClientSource      string                   `json:"client_source"`
	SourceFormat      string                   `json:"source_format,omitempty"`
	Action            ClaudeMimicryGuardAction `json:"action"`
	GuardMode         string                   `json:"guard_mode"`
	AuditStatus       ClaudeMimicryStatus      `json:"audit_status"`
	Model             string                   `json:"model,omitempty"`
	RequestPath       string                   `json:"request_path,omitempty"`
	AuthID            string                   `json:"auth_id,omitempty"`
	AuthLabel         string                   `json:"auth_label,omitempty"`
	UpstreamAttempted bool                     `json:"upstream_attempted"`
	UpstreamStatus    int                      `json:"upstream_status,omitempty"`
	UpstreamError     string                   `json:"upstream_error,omitempty"`
	Reasons           []string                 `json:"reasons,omitempty"`
	Warnings          []string                 `json:"warnings,omitempty"`
	Failures          []string                 `json:"failures,omitempty"`
	EventLimit        int                      `json:"-"`
}

type ClaudeMimicryClientStat struct {
	ClientSource string    `json:"client_source"`
	Total        int       `json:"total"`
	Allowed      int       `json:"allowed"`
	Degraded     int       `json:"degraded"`
	Blocked      int       `json:"blocked"`
	LastSeen     time.Time `json:"last_seen"`
	LastReason   string    `json:"last_reason,omitempty"`
}

var (
	claudeMimicryAuditMu        sync.RWMutex
	claudeMimicryLatest         ClaudeMimicryAuditSnapshot
	claudeMimicryHasLatest      bool
	claudeMimicryEvents         []ClaudeMimicryEvent
	claudeMimicryEventSeq       int64
	claudeMimicryExpectedSystem = []struct {
		label string
		text  string
	}{
		{"billing_header", ""},
		{"identity", helps.ClaudeCodeAgentIdentity},
		{"core_prompt", helps.ClaudeCodeHarnessPrompt},
	}
)

var claudeAllowedUpstreamHeaderNames = map[string]struct{}{
	"accept":          {},
	"accept-encoding": {},
	"anthropic-beta":  {},
	"anthropic-dangerous-direct-browser-access": {},
	"anthropic-version":                         {},
	"authorization":                             {},
	"connection":                                {},
	"content-type":                              {},
	"user-agent":                                {},
	"x-api-key":                                 {},
	"x-app":                                     {},
	"x-claude-code-session-id":                  {},
	"x-stainless-arch":                          {},
	"x-stainless-lang":                          {},
	"x-stainless-os":                            {},
	"x-stainless-package-version":               {},
	"x-stainless-retry-count":                   {},
	"x-stainless-runtime":                       {},
	"x-stainless-runtime-version":               {},
	"x-stainless-timeout":                       {},
}

func BuildClaudeMimicryBaseline(model string, cfg *config.Config) ClaudeMimicryBaseline {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	baseline := ClaudeMimicryBaseline{
		Model:                 model,
		Family:                claudeMimicryFamily(model),
		ClaudeVersion:         helps.DefaultClaudeVersion(cfg),
		CCHSeed:               fmt.Sprintf("0x%016x", claudeCCHSeed),
		ExpectedBetaCount:     len(claudeCodeDefaultBetaTokens),
		ExpectedBetas:         append([]string(nil), claudeCodeDefaultBetaTokens...),
		ExpectedSystemHashes:  expectedClaudeSystemHashes(),
		ExpectedTopFields:     []string{"thinking", "output_config"},
		ExpectedEfforts:       []string{"high", "xhigh", "max"},
		ExpectedToolCountHint: "Claude Code core tools or MCP namespaced tools",
	}
	if strings.Contains(model, "haiku-4-5") {
		baseline.ExpectedTopFields = []string{"thinking"}
		baseline.ExpectedEfforts = nil
		baseline.ExpectedToolCountHint = "Claude Code extended tool surface"
	}
	return baseline
}

func WaitingClaudeMimicryAudit(model string, cfg *config.Config) ClaudeMimicryAuditSnapshot {
	baseline := BuildClaudeMimicryBaseline(model, cfg)
	return ClaudeMimicryAuditSnapshot{
		Status:      ClaudeMimicryStatusWaiting,
		CheckedAt:   time.Now().UTC(),
		Model:       baseline.Model,
		RequestPath: "/v1/messages",
		Baseline:    baseline,
		Warnings:    []string{"尚未观察到真实出站 Claude 请求，当前只展示静态基线"},
	}
}

func RecordClaudeMimicryAudit(model string, requestPath string, body []byte, headers http.Header, cfg *config.Config) ClaudeMimicryAuditSnapshot {
	snapshot := AuditClaudeMimicryRequest(model, requestPath, body, headers, cfg)
	claudeMimicryAuditMu.Lock()
	claudeMimicryLatest = snapshot
	claudeMimicryHasLatest = true
	claudeMimicryAuditMu.Unlock()
	return snapshot
}

func LatestClaudeMimicryAudit() (ClaudeMimicryAuditSnapshot, bool) {
	claudeMimicryAuditMu.RLock()
	defer claudeMimicryAuditMu.RUnlock()
	return claudeMimicryLatest, claudeMimicryHasLatest
}

func ResetClaudeMimicryAuditForTest() {
	claudeMimicryAuditMu.Lock()
	claudeMimicryLatest = ClaudeMimicryAuditSnapshot{}
	claudeMimicryHasLatest = false
	claudeMimicryAuditMu.Unlock()
}

func ResetClaudeMimicryEventsForTest() {
	claudeMimicryAuditMu.Lock()
	claudeMimicryEvents = nil
	claudeMimicryEventSeq = 0
	claudeMimicryAuditMu.Unlock()
}

func EvaluateClaudeMimicryGuard(audit ClaudeMimicryAuditSnapshot, cfg *config.Config) ClaudeMimicryGuardDecision {
	return EvaluateClaudeMimicryGuardWithPolicy(audit, cfg, ClaudeMimicryGuardPolicy{RequireSignedCCH: true, RequireSystemBlocks: true})
}

func EvaluateClaudeMimicryGuardWithPolicy(audit ClaudeMimicryAuditSnapshot, cfg *config.Config, policy ClaudeMimicryGuardPolicy) ClaudeMimicryGuardDecision {
	mode := normalizeClaudeMimicryGuardMode(cfg)
	decision := ClaudeMimicryGuardDecision{
		Mode:   mode,
		Action: ClaudeMimicryGuardActionAllow,
		Status: audit.Status,
		Audit:  audit,
	}
	if mode == "observe" {
		return decision
	}

	hardReasons := hardClaudeMimicryFailures(audit, policy)
	if mode == "strict" {
		hardReasons = append(append(hardReasons, audit.Failures...), audit.Warnings...)
	}
	if len(hardReasons) > 0 {
		decision.Action = ClaudeMimicryGuardActionBlock
		decision.Blocked = true
		decision.Reasons = uniqueSortedStrings(hardReasons)
		return decision
	}
	if audit.Status != ClaudeMimicryStatusAligned {
		decision.Action = ClaudeMimicryGuardActionDegrade
		decision.Reasons = uniqueSortedStrings(append(append([]string{}, audit.Warnings...), audit.Failures...))
	}
	return decision
}

func RecordClaudeMimicryEvent(event ClaudeMimicryEvent) {
	claudeMimicryAuditMu.Lock()
	defer claudeMimicryAuditMu.Unlock()

	claudeMimicryEventSeq++
	if event.ID == "" {
		event.ID = fmt.Sprintf("mimicry-%d", claudeMimicryEventSeq)
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	if event.ClientSource == "" {
		event.ClientSource = "unknown"
	}
	claudeMimicryEvents = append(claudeMimicryEvents, event)
	limit := event.EventLimit
	if limit <= 0 {
		limit = config.DefaultClaudeMimicryGuardEventsLimit
	}
	if limit > 2000 {
		limit = 2000
	}
	if len(claudeMimicryEvents) > limit {
		claudeMimicryEvents = claudeMimicryEvents[len(claudeMimicryEvents)-limit:]
	}
}

func LatestClaudeMimicryEvents(limit int) []ClaudeMimicryEvent {
	claudeMimicryAuditMu.RLock()
	defer claudeMimicryAuditMu.RUnlock()

	if limit <= 0 || limit > len(claudeMimicryEvents) {
		limit = len(claudeMimicryEvents)
	}
	out := make([]ClaudeMimicryEvent, 0, limit)
	for i := len(claudeMimicryEvents) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, claudeMimicryEvents[i])
	}
	return out
}

func ClaudeMimicryClientStats() map[string]ClaudeMimicryClientStat {
	claudeMimicryAuditMu.RLock()
	defer claudeMimicryAuditMu.RUnlock()

	stats := make(map[string]ClaudeMimicryClientStat)
	for _, event := range claudeMimicryEvents {
		source := event.ClientSource
		if source == "" {
			source = "unknown"
		}
		stat := stats[source]
		stat.ClientSource = source
		stat.Total++
		switch event.Action {
		case ClaudeMimicryGuardActionBlock:
			stat.Blocked++
		case ClaudeMimicryGuardActionDegrade:
			stat.Degraded++
		default:
			stat.Allowed++
		}
		if event.At.After(stat.LastSeen) {
			stat.LastSeen = event.At
			if len(event.Reasons) > 0 {
				stat.LastReason = event.Reasons[0]
			} else if event.UpstreamError != "" {
				stat.LastReason = event.UpstreamError
			}
		}
		stats[source] = stat
	}
	return stats
}

func ClassifyClaudeMimicryClientSource(sourceFormat string, headers http.Header) string {
	sourceFormat = strings.ToLower(strings.TrimSpace(sourceFormat))
	userAgent := strings.ToLower(strings.TrimSpace(headers.Get("User-Agent")))
	headerText := strings.ToLower(headersToSourceText(headers))
	combined := userAgent + "\n" + headerText
	switch {
	case strings.HasPrefix(userAgent, "claude-cli/"):
		return "claude-code"
	case strings.Contains(combined, "cherrystudio") || strings.Contains(combined, "cherry studio"):
		return "cherrystudio"
	case strings.Contains(combined, "hermes"):
		return "hermes"
	case strings.Contains(combined, "openclaw"):
		return "openclaw"
	case strings.Contains(combined, "opencode"):
		return "opencode"
	case sourceFormat == "openai" || sourceFormat == "openai-response":
		return "openai-compatible"
	case sourceFormat != "":
		return sourceFormat
	default:
		return "unknown"
	}
}

func AuditClaudeMimicryRequest(model string, requestPath string, body []byte, headers http.Header, cfg *config.Config) ClaudeMimicryAuditSnapshot {
	baseline := BuildClaudeMimicryBaseline(model, cfg)
	if strings.TrimSpace(requestPath) == "" {
		requestPath = "/v1/messages"
	}
	audit := ClaudeMimicryAuditSnapshot{
		Status:      ClaudeMimicryStatusAligned,
		CheckedAt:   time.Now().UTC(),
		Model:       baseline.Model,
		RequestPath: requestPath,
		Baseline:    baseline,
		System:      auditClaudeMimicrySystem(body),
		CCH:         auditClaudeMimicryCCH(body),
		Betas:       auditClaudeMimicryBetas(headers),
		Thinking:    auditClaudeMimicryThinking(body, baseline),
		Tools:       auditClaudeMimicryTools(body),
		Headers:     auditClaudeMimicryHeaders(headers),
	}
	collect := func(component string, status ClaudeMimicryStatus, detail string) {
		switch status {
		case ClaudeMimicryStatusFailed:
			audit.Failures = append(audit.Failures, component+": "+detail)
		case ClaudeMimicryStatusWarning:
			audit.Warnings = append(audit.Warnings, component+": "+detail)
		}
	}
	collect("system", audit.System.Status, "system blocks do not match Claude Code baseline")
	collect("cch", audit.CCH.Status, audit.CCH.Detail)
	collect("betas", audit.Betas.Status, "Anthropic-Beta differs from Claude Code baseline")
	collect("thinking", audit.Thinking.Status, audit.Thinking.Detail)
	collect("tools", audit.Tools.Status, "tool names or schemas contain third-party fingerprints")
	collect("headers", audit.Headers.Status, "headers contain missing or unexpected values")
	if len(audit.Failures) > 0 {
		audit.Status = ClaudeMimicryStatusFailed
	} else if len(audit.Warnings) > 0 {
		audit.Status = ClaudeMimicryStatusWarning
	}
	return audit
}

func auditClaudeMimicrySystem(body []byte) ClaudeMimicrySystemAudit {
	audit := ClaudeMimicrySystemAudit{Status: ClaudeMimicryStatusAligned}
	system := gjson.GetBytes(body, "system")
	if !system.Exists() || !system.IsArray() {
		return ClaudeMimicrySystemAudit{
			Status: ClaudeMimicryStatusFailed,
			Blocks: []ClaudeMimicrySystemBlock{{
				Index:  0,
				Label:  "system",
				Status: ClaudeMimicryStatusFailed,
				Detail: "system must be an array",
			}},
		}
	}
	for index, expected := range claudeMimicryExpectedSystem {
		text := gjson.GetBytes(body, fmt.Sprintf("system.%d.text", index)).String()
		block := ClaudeMimicrySystemBlock{
			Index:  index,
			Label:  expected.label,
			Status: ClaudeMimicryStatusAligned,
			Hash:   shortSHA256(text),
			Length: len(text),
		}
		if index == 0 {
			if !strings.HasPrefix(text, "x-anthropic-billing-header:") || !claudeBillingHeaderCCHPattern.MatchString(text) {
				block.Status = ClaudeMimicryStatusFailed
				block.Detail = "missing Claude Code billing header with cch"
				audit.Status = ClaudeMimicryStatusFailed
			}
		} else {
			block.ExpectedHash = shortSHA256(expected.text)
			if text != expected.text {
				block.Status = ClaudeMimicryStatusFailed
				block.Detail = "system block hash mismatch"
				audit.Status = ClaudeMimicryStatusFailed
			}
		}
		audit.Blocks = append(audit.Blocks, block)
	}
	return audit
}

func auditClaudeMimicryCCH(body []byte) ClaudeMimicryCCHAudit {
	seed := fmt.Sprintf("0x%016x", claudeCCHSeed)
	billingHeader := gjson.GetBytes(body, "system.0.text").String()
	if !strings.HasPrefix(billingHeader, "x-anthropic-billing-header:") {
		return ClaudeMimicryCCHAudit{Status: ClaudeMimicryStatusFailed, Seed: seed, Detail: "missing billing header"}
	}
	matches := claudeBillingHeaderCCHPattern.FindStringSubmatch(billingHeader)
	if len(matches) != 2 {
		return ClaudeMimicryCCHAudit{Status: ClaudeMimicryStatusFailed, Seed: seed, Detail: "missing cch value"}
	}
	unsignedBillingHeader := claudeBillingHeaderCCHPattern.ReplaceAllString(billingHeader, "cch=00000;")
	unsignedBody, err := sjson.SetBytes(body, "system.0.text", unsignedBillingHeader)
	if err != nil {
		return ClaudeMimicryCCHAudit{Status: ClaudeMimicryStatusFailed, Seed: seed, Actual: matches[1], Detail: "failed to rebuild unsigned body"}
	}
	expected := fmt.Sprintf("%05x", xxHash64.Checksum(unsignedBody, claudeCCHSeed)&0xFFFFF)
	status := ClaudeMimicryStatusAligned
	detail := "cch signature matches final body"
	if matches[1] != expected {
		status = ClaudeMimicryStatusFailed
		detail = "cch signature does not match final body"
	}
	return ClaudeMimicryCCHAudit{
		Status:   status,
		Signed:   matches[1] == expected,
		Seed:     seed,
		Actual:   matches[1],
		Expected: expected,
		Detail:   detail,
	}
}

func auditClaudeMimicryBetas(headers http.Header) ClaudeMimicryBetaAudit {
	tokens := splitHeaderTokens(headers.Get("Anthropic-Beta"))
	expected := append([]string(nil), claudeCodeDefaultBetaTokens...)
	missing := missingStrings(expected, tokens)
	unexpected := unexpectedStrings(tokens, claudeAllowedBetaTokens)
	status := ClaudeMimicryStatusAligned
	if len(missing) > 0 || len(unexpected) > 0 {
		status = ClaudeMimicryStatusFailed
	}
	return ClaudeMimicryBetaAudit{
		Status:        status,
		Count:         len(tokens),
		ExpectedCount: len(expected),
		Tokens:        tokens,
		Missing:       missing,
		Unexpected:    unexpected,
	}
}

func auditClaudeMimicryThinking(body []byte, baseline ClaudeMimicryBaseline) ClaudeMimicryThinkingAudit {
	audit := ClaudeMimicryThinkingAudit{
		Status:    ClaudeMimicryStatusAligned,
		Type:      gjson.GetBytes(body, "thinking.type").String(),
		Effort:    gjson.GetBytes(body, "output_config.effort").String(),
		TopFields: presentClaudeTopFields(body),
	}
	if audit.Type == "adaptive" && len(baseline.ExpectedEfforts) > 0 && audit.Effort != "" && !stringInSlice(audit.Effort, baseline.ExpectedEfforts) {
		audit.Status = ClaudeMimicryStatusWarning
		audit.Detail = "adaptive thinking effort is outside the model baseline"
	}
	if audit.Type == "adaptive" && audit.Effort == "" {
		audit.Detail = "adaptive thinking uses upstream default effort"
	}
	return audit
}

func auditClaudeMimicryTools(body []byte) ClaudeMimicryToolAudit {
	audit := ClaudeMimicryToolAudit{Status: ClaudeMimicryStatusAligned}
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || !tools.IsArray() {
		return audit
	}
	knownUpstream := knownClaudeUpstreamToolNames()
	var schemaParts []string
	tools.ForEach(func(_, tool gjson.Result) bool {
		name := strings.TrimSpace(tool.Get("name").String())
		if name == "" {
			return true
		}
		typedBuiltin := isClaudeTypedBuiltinTool(tool)
		audit.Count++
		audit.Names = append(audit.Names, name)
		schemaParts = append(schemaParts, name+"\x00"+tool.Get("input_schema").Raw)
		if typedBuiltin {
			return true
		}
		if _, leaky := oauthToolRenameMap[name]; leaky {
			audit.LeakyNames = append(audit.LeakyNames, name)
		}
		if !knownUpstream[name] && !strings.HasPrefix(name, "mcp__") && !strings.Contains(name, "__") {
			audit.Unknown = append(audit.Unknown, name)
		}
		description := strings.ToLower(tool.Get("description").String())
		if !knownUpstream[name] {
			for _, marker := range []string{"openclaw", "hermes", "opencode", "cherrystudio", "cherry studio", "proxy", "relay", "sub2api", "newapi"} {
				if strings.Contains(description, marker) {
					audit.DescriptionWarnings = append(audit.DescriptionWarnings, name)
					break
				}
			}
		}
		properties := tool.Get("input_schema.properties")
		if properties.Exists() && properties.IsObject() {
			for original := range oauthSchemaPropertyRenameMap {
				if properties.Get(original).Exists() {
					audit.SchemaWarnings = append(audit.SchemaWarnings, name+"."+original)
				}
			}
		}
		return true
	})
	sort.Strings(audit.Names)
	sort.Strings(schemaParts)
	audit.SchemaSignature = shortSHA256(strings.Join(schemaParts, "\n"))
	audit.ToolChoice = gjson.GetBytes(body, "tool_choice.type").String()
	if len(audit.LeakyNames) > 0 || len(hardClaudeToolSchemaWarnings(audit.SchemaWarnings)) > 0 {
		audit.Status = ClaudeMimicryStatusFailed
	} else if len(audit.Unknown) > 0 || len(audit.DescriptionWarnings) > 0 || len(audit.SchemaWarnings) > 0 {
		audit.Status = ClaudeMimicryStatusWarning
	}
	return audit
}

func isClaudeTypedBuiltinTool(tool gjson.Result) bool {
	toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
	if toolType == "" {
		return false
	}
	for _, prefix := range []string{
		"web_search",
		"code_execution",
		"text_editor",
		"computer",
	} {
		if toolType == prefix || strings.HasPrefix(toolType, prefix+"_") {
			return true
		}
	}
	return false
}

func auditClaudeMimicryHeaders(headers http.Header) ClaudeMimicryHeaderAudit {
	audit := ClaudeMimicryHeaderAudit{
		Status:         ClaudeMimicryStatusAligned,
		UserAgent:      headers.Get("User-Agent"),
		PackageVersion: headers.Get("X-Stainless-Package-Version"),
		RuntimeVersion: headers.Get("X-Stainless-Runtime-Version"),
		OS:             headers.Get("X-Stainless-Os"),
		Arch:           headers.Get("X-Stainless-Arch"),
	}
	for _, name := range []string{
		"User-Agent",
		"X-Stainless-Package-Version",
		"X-Stainless-Runtime-Version",
		"X-Stainless-Os",
		"X-Stainless-Arch",
		"Anthropic-Beta",
		"X-App",
		"X-Stainless-Lang",
		"X-Stainless-Runtime",
		"X-Claude-Code-Session-Id",
		"Anthropic-Dangerous-Direct-Browser-Access",
		"Accept",
		"Accept-Encoding",
		"Content-Type",
		"Anthropic-Version",
	} {
		if strings.TrimSpace(headers.Get(name)) == "" {
			audit.Missing = append(audit.Missing, name)
		}
	}
	if !strings.HasPrefix(audit.UserAgent, "claude-cli/") {
		audit.Unexpected = append(audit.Unexpected, "User-Agent="+audit.UserAgent)
	}
	if headers.Get("X-App") != "cli" {
		audit.Unexpected = append(audit.Unexpected, "X-App="+headers.Get("X-App"))
	}
	if headers.Get("X-Stainless-Runtime") != "node" {
		audit.Unexpected = append(audit.Unexpected, "X-Stainless-Runtime="+headers.Get("X-Stainless-Runtime"))
	}
	if headers.Get("X-Stainless-Lang") != "js" {
		audit.Unexpected = append(audit.Unexpected, "X-Stainless-Lang="+headers.Get("X-Stainless-Lang"))
	}
	for key := range headers {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if _, allowed := claudeAllowedUpstreamHeaderNames[lowerKey]; !allowed {
			audit.Unexpected = append(audit.Unexpected, key)
		}
		for _, prefix := range claudeBlockedUpstreamHeaderPrefixes {
			if strings.HasPrefix(lowerKey, prefix) {
				audit.Blocked = append(audit.Blocked, key)
				break
			}
		}
	}
	sort.Strings(audit.Missing)
	sort.Strings(audit.Blocked)
	sort.Strings(audit.Unexpected)
	if len(audit.Missing) > 0 || len(audit.Blocked) > 0 || len(audit.Unexpected) > 0 {
		audit.Status = ClaudeMimicryStatusFailed
	}
	return audit
}

func expectedClaudeSystemHashes() []string {
	hashes := make([]string, 0, len(claudeMimicryExpectedSystem)-1)
	for _, block := range claudeMimicryExpectedSystem[1:] {
		hashes = append(hashes, shortSHA256(block.text))
	}
	return hashes
}

func splitHeaderTokens(header string) []string {
	parts := strings.Split(header, ",")
	tokens := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

func missingStrings(expected, actual []string) []string {
	actualSet := make(map[string]struct{}, len(actual))
	for _, value := range actual {
		actualSet[value] = struct{}{}
	}
	var missing []string
	for _, value := range expected {
		if _, ok := actualSet[value]; !ok {
			missing = append(missing, value)
		}
	}
	sort.Strings(missing)
	return missing
}

func unexpectedStrings(actual []string, allowed map[string]struct{}) []string {
	var unexpected []string
	for _, value := range actual {
		if _, ok := allowed[value]; !ok {
			unexpected = append(unexpected, value)
		}
	}
	sort.Strings(unexpected)
	return unexpected
}

func presentClaudeTopFields(body []byte) []string {
	var fields []string
	for _, name := range []string{"context_management", "output_config", "thinking"} {
		if gjson.GetBytes(body, name).Exists() {
			fields = append(fields, name)
		}
	}
	return fields
}

func knownClaudeUpstreamToolNames() map[string]bool {
	known := map[string]bool{
		"Bash":         true,
		"BashSession":  true,
		"Read":         true,
		"Write":        true,
		"Edit":         true,
		"MultiEdit":    true,
		"Glob":         true,
		"Grep":         true,
		"Task":         true,
		"Agent":        true,
		"WebFetch":     true,
		"WebSearch":    true,
		"TodoWrite":    true,
		"TodoRead":     true,
		"NotebookEdit": true,
		"Question":     true,
		"Skill":        true,
		"LS":           true,
	}
	for _, upstream := range oauthToolRenameMap {
		known[upstream] = true
	}
	return known
}

func isTitleCaseLikeToolName(name string) bool {
	if name == "" {
		return false
	}
	first := name[0]
	return first >= 'A' && first <= 'Z'
}

func hardClaudeToolSchemaWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	var hard []string
	for _, warning := range warnings {
		name := warning
		if idx := strings.Index(warning, "."); idx >= 0 {
			name = warning[:idx]
		}
		if _, leaky := oauthToolRenameMap[name]; leaky {
			hard = append(hard, warning)
		}
	}
	sort.Strings(hard)
	return hard
}

func normalizeClaudeMimicryGuardMode(cfg *config.Config) string {
	if cfg == nil {
		return config.DefaultClaudeMimicryGuardMode
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.ClaudeMimicryGuard.Mode))
	switch mode {
	case "observe", "strict":
		return mode
	case "degrade", "":
		return config.DefaultClaudeMimicryGuardMode
	default:
		return config.DefaultClaudeMimicryGuardMode
	}
}

func hardClaudeMimicryFailures(audit ClaudeMimicryAuditSnapshot, policy ClaudeMimicryGuardPolicy) []string {
	var reasons []string
	if policy.RequireSystemBlocks && audit.System.Status == ClaudeMimicryStatusFailed {
		reasons = append(reasons, "system blocks mismatch")
	}
	if policy.RequireSignedCCH && audit.CCH.Status == ClaudeMimicryStatusFailed {
		reasons = append(reasons, "cch signature mismatch")
	}
	if audit.Betas.Status == ClaudeMimicryStatusFailed {
		if len(audit.Betas.Unexpected) > 0 {
			reasons = append(reasons, "unexpected Anthropic-Beta: "+strings.Join(audit.Betas.Unexpected, ", "))
		}
		if len(audit.Betas.Missing) > 0 {
			reasons = append(reasons, "missing Claude Code beta: "+strings.Join(audit.Betas.Missing, ", "))
		}
	}
	if audit.Headers.Status == ClaudeMimicryStatusFailed {
		if len(audit.Headers.Blocked) > 0 {
			reasons = append(reasons, "blocked third-party headers: "+strings.Join(audit.Headers.Blocked, ", "))
		}
		if len(audit.Headers.Unexpected) > 0 {
			reasons = append(reasons, "unexpected headers: "+strings.Join(audit.Headers.Unexpected, ", "))
		}
		if len(audit.Headers.Missing) > 0 {
			reasons = append(reasons, "missing Claude Code headers: "+strings.Join(audit.Headers.Missing, ", "))
		}
	}
	if len(audit.Tools.LeakyNames) > 0 {
		reasons = append(reasons, "leaky tool names: "+strings.Join(audit.Tools.LeakyNames, ", "))
	}
	if hardSchemas := hardClaudeToolSchemaWarnings(audit.Tools.SchemaWarnings); len(hardSchemas) > 0 {
		reasons = append(reasons, "leaky tool schema properties: "+strings.Join(hardSchemas, ", "))
	}
	return uniqueSortedStrings(reasons)
}

func headersToSourceText(headers http.Header) string {
	if len(headers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(headers))
	for key, values := range headers {
		parts = append(parts, key+"="+strings.Join(values, ","))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func stringInSlice(value string, items []string) bool {
	for _, item := range items {
		if value == item {
			return true
		}
	}
	return false
}

func claudeMimicryFamily(model string) string {
	switch {
	case strings.Contains(model, "opus"):
		return "opus"
	case strings.Contains(model, "sonnet"):
		return "sonnet"
	case strings.Contains(model, "haiku"):
		return "haiku"
	default:
		return "claude"
	}
}

func shortSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}
