package executor

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func buildSignedClaudeMimicryTestPayload(t *testing.T) []byte {
	t.Helper()

	payload := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"max_tokens":1024}`)
	payload = checkSystemInstructionsWithSigningModeForced(payload, false, true, true, helps.DefaultClaudeVersion(&config.Config{}), "cli", "", true)
	payload = injectFakeUserID(payload, "sk-ant-oat-test", true, true)
	payload = ensureCacheControl(payload)
	payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"name":"Bash","description":"Run shell commands","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]`))
	payload = signAnthropicMessagesBody(payload)
	if got := gjson.GetBytes(payload, "system.0.text").String(); got == "" {
		t.Fatalf("test payload did not include billing header")
	}
	return payload
}

func TestAuditClaudeMimicryRequest_AlignedSignedClaudeCodeShape(t *testing.T) {
	resetClaudeDeviceProfileCache()

	payload := buildSignedClaudeMimicryTestPayload(t)
	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"claude-cli/2.1.148 (external, cli)"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})

	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})

	if audit.Status != ClaudeMimicryStatusAligned {
		t.Fatalf("audit.Status = %q, want %q; failures=%v warnings=%v", audit.Status, ClaudeMimicryStatusAligned, audit.Failures, audit.Warnings)
	}
	if audit.CCH.Status != ClaudeMimicryStatusAligned || !audit.CCH.Signed {
		t.Fatalf("CCH = %#v, want aligned signed cch", audit.CCH)
	}
	if audit.System.Status != ClaudeMimicryStatusAligned {
		t.Fatalf("System = %#v, want aligned", audit.System)
	}
	if audit.Betas.Status != ClaudeMimicryStatusAligned {
		t.Fatalf("Betas = %#v, want aligned", audit.Betas)
	}
	if audit.Headers.Status != ClaudeMimicryStatusAligned {
		t.Fatalf("Headers = %#v, want aligned", audit.Headers)
	}
}

func TestBuildClaudeMimicryBaseline_Opus48IncludesMidConversationSystemBeta(t *testing.T) {
	baseline := BuildClaudeMimicryBaseline("claude-opus-4-8", &config.Config{})

	if !stringInSlice("mid-conversation-system-2026-04-07", baseline.ExpectedBetas) {
		t.Fatalf("ExpectedBetas = %#v, want Opus 4.8 mid-conversation system beta", baseline.ExpectedBetas)
	}
	if baseline.ExpectedBetaCount != len(baseline.ExpectedBetas) {
		t.Fatalf("ExpectedBetaCount = %d, want len(ExpectedBetas)=%d", baseline.ExpectedBetaCount, len(baseline.ExpectedBetas))
	}
}

func TestAuditClaudeMimicryRequest_ClaudeCode214OfficialOAuthBearerShape(t *testing.T) {
	payload := []byte(`{
		"model":"claude-opus-4-8",
		"messages":[{"role":"user","content":[
			{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-05-28.\n\n      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>\n\n"},
			{"type":"text","text":"Reply exactly: OFFICIAL_OAUTH_CAPTURE_OK","cache_control":{"type":"ephemeral"}}
		]}],
		"system":[
			{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"CWD: F:\\claude反代\\CLIProxyAPI\nDate: 2026-05-28\n\ngitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.\n\nCurrent branch: xiaoyu/claude-oauth-cookie-mimicry\n\nMain branch (you will usually use this for PRs): main\n\nStatus:\nM internal/runtime/executor/claude_executor.go\n\nRecent commits:\nd88de94f docs: record production deployment","cache_control":{"type":"ephemeral"}}
		],
		"tools":[
			{"name":"Bash","description":"execute shell commands","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}},
			{"name":"Edit","description":"modify file contents in place","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"old_string":{"type":"string"},"new_string":{"type":"string"}},"required":["file_path","old_string","new_string"],"additionalProperties":false}},
			{"name":"Read","description":"read files, images, PDFs, notebooks","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}},"required":["file_path"],"additionalProperties":false}}
		],
		"metadata":{"user_id":"<redacted>"},
		"max_tokens":64000,
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"high"},
		"stream":true
	}`)
	headers := http.Header{
		"Accept":                                    []string{"application/json"},
		"Accept-Encoding":                           []string{"gzip, deflate, br, zstd"},
		"Authorization":                             []string{"Bearer sk-ant-oat-test"},
		"Content-Type":                              []string{"application/json"},
		"User-Agent":                                []string{"claude-cli/2.1.154 (external, sdk-cli)"},
		"X-Claude-Code-Session-Id":                  []string{"530ccdf0-8c3b-4b4b-9169-c84d07c76165"},
		"X-Stainless-Arch":                          []string{"x64"},
		"X-Stainless-Lang":                          []string{"js"},
		"X-Stainless-Os":                            []string{"Windows"},
		"X-Stainless-Package-Version":               []string{"0.94.0"},
		"X-Stainless-Retry-Count":                   []string{"0"},
		"X-Stainless-Runtime":                       []string{"node"},
		"X-Stainless-Runtime-Version":               []string{"v24.3.0"},
		"X-Stainless-Timeout":                       []string{"600"},
		"Anthropic-Beta":                            []string{"claude-code-20250219,interleaved-thinking-2025-05-14,mid-conversation-system-2026-04-07,effort-2025-11-24"},
		"Anthropic-Dangerous-Direct-Browser-Access": []string{"true"},
		"Anthropic-Version":                         []string{"2023-06-01"},
		"X-App":                                     []string{"cli"},
	}

	audit := AuditClaudeMimicryRequest("claude-opus-4-8", "/v1/messages", payload, headers, &config.Config{})

	if audit.System.Status != ClaudeMimicryStatusAligned {
		t.Fatalf("System = %#v, want aligned official 2.1.154 bearer-token system shape", audit.System)
	}
	if audit.CCH.Status != ClaudeMimicryStatusWarning {
		t.Fatalf("CCH = %#v, want warning for official bearer-token shape without billing header", audit.CCH)
	}
	if audit.Betas.Status != ClaudeMimicryStatusAligned {
		t.Fatalf("Betas = %#v, want aligned Opus 4.8 beta set", audit.Betas)
	}
	if audit.Headers.Status != ClaudeMimicryStatusAligned {
		t.Fatalf("Headers = %#v, want aligned official 2.1.154 headers", audit.Headers)
	}
}

func TestAuditClaudeMimicryRequest_ClaudeCode214ToolSurfaceIsKnown(t *testing.T) {
	payload := buildSignedClaudeMimicryTestPayload(t)
	payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[
		{"name":"AskUserQuestion","description":"Ask the user a question.","input_schema":{"type":"object","properties":{"question":{"type":"string"}}}},
		{"name":"CronCreate","description":"Create a scheduled task.","input_schema":{"type":"object","properties":{"prompt":{"type":"string"}}}},
		{"name":"CronDelete","description":"Delete a scheduled task.","input_schema":{"type":"object","properties":{"id":{"type":"string"}}}},
		{"name":"CronList","description":"List scheduled tasks.","input_schema":{"type":"object","properties":{}}},
		{"name":"EnterPlanMode","description":"Enter planning mode.","input_schema":{"type":"object","properties":{}}},
		{"name":"EnterWorktree","description":"Enter a worktree.","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}},
		{"name":"ExitPlanMode","description":"Exit planning mode.","input_schema":{"type":"object","properties":{"plan":{"type":"string"}}}},
		{"name":"ExitWorktree","description":"Exit a worktree.","input_schema":{"type":"object","properties":{}}},
		{"name":"LSP","description":"Query language server information.","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}},
		{"name":"ScheduleWakeup","description":"Schedule a wakeup.","input_schema":{"type":"object","properties":{"prompt":{"type":"string"}}}},
		{"name":"SendMessage","description":"Send a message.","input_schema":{"type":"object","properties":{"content":{"type":"string"}}}},
		{"name":"TaskCreate","description":"Create a task.","input_schema":{"type":"object","properties":{"title":{"type":"string"}}}},
		{"name":"TaskGet","description":"Get a task.","input_schema":{"type":"object","properties":{"id":{"type":"string"}}}},
		{"name":"TaskList","description":"List tasks.","input_schema":{"type":"object","properties":{}}},
		{"name":"TaskOutput","description":"Read task output.","input_schema":{"type":"object","properties":{"id":{"type":"string"}}}},
		{"name":"TaskStop","description":"Stop a task.","input_schema":{"type":"object","properties":{"id":{"type":"string"}}}},
		{"name":"TaskUpdate","description":"Update a task.","input_schema":{"type":"object","properties":{"id":{"type":"string"}}}},
		{"name":"TeamCreate","description":"Create a team.","input_schema":{"type":"object","properties":{"name":{"type":"string"}}}},
		{"name":"TeamDelete","description":"Delete a team.","input_schema":{"type":"object","properties":{"id":{"type":"string"}}}},
		{"name":"Workflow","description":"Run a workflow.","input_schema":{"type":"object","properties":{"name":{"type":"string"}}}}
	]`))
	payload = signAnthropicMessagesBody(payload)

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"claude-cli/2.1.154 (external, sdk-cli)"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})

	if audit.Tools.Status != ClaudeMimicryStatusAligned {
		t.Fatalf("Tools = %#v, want official Claude Code 2.1.154 tools aligned", audit.Tools)
	}
	if len(audit.Tools.Unknown) != 0 {
		t.Fatalf("Tools.Unknown = %#v, want no unknown official Claude Code tools", audit.Tools.Unknown)
	}
}

func TestAuditClaudeMimicryRequest_FlagsLeakyThirdPartyShape(t *testing.T) {
	payload := []byte(`{
		"model":"claude-sonnet-4-6",
		"system":[{"type":"text","text":"You are Hermes proxy."}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"tools":[{"name":"cronjob","description":"Hermes cronjob tool","input_schema":{"type":"object","properties":{"wake_at":{"type":"string"}}}}],
		"tool_choice":{"type":"any"}
	}`)
	headers := http.Header{
		"User-Agent":                  []string{"Hermes/1.0"},
		"Anthropic-Beta":              []string{"context-1m-2025-08-07,unknown-client-beta"},
		"X-Hermes-Version":            []string{"1.0"},
		"X-Stainless-Package-Version": []string{"0.98.0"},
		"X-Stainless-Runtime-Version": []string{"v24.13.0"},
		"X-Stainless-Os":              []string{"MacOS"},
		"X-Stainless-Arch":            []string{"arm64"},
	}

	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, headers, &config.Config{})

	if audit.Status != ClaudeMimicryStatusFailed {
		t.Fatalf("audit.Status = %q, want failed; failures=%v warnings=%v", audit.Status, audit.Failures, audit.Warnings)
	}
	if audit.CCH.Status != ClaudeMimicryStatusFailed {
		t.Fatalf("CCH = %#v, want failed", audit.CCH)
	}
	if audit.System.Status != ClaudeMimicryStatusFailed {
		t.Fatalf("System = %#v, want failed", audit.System)
	}
	if len(audit.Headers.Blocked) == 0 {
		t.Fatalf("Headers.Blocked = %#v, want leaked header detected", audit.Headers.Blocked)
	}
	if len(audit.Tools.DescriptionWarnings) == 0 {
		t.Fatalf("Tools.DescriptionWarnings = %#v, want tool description leak detected", audit.Tools.DescriptionWarnings)
	}
	if len(audit.Betas.Unexpected) == 0 {
		t.Fatalf("Betas.Unexpected = %#v, want unexpected beta detected", audit.Betas.Unexpected)
	}
}

func TestRecordClaudeMimicryAuditStoresLatestSnapshot(t *testing.T) {
	ResetClaudeMimicryAuditForTest()
	t.Cleanup(ResetClaudeMimicryAuditForTest)

	payload := buildSignedClaudeMimicryTestPayload(t)
	req := newClaudeHeaderTestRequest(t, nil)
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})

	RecordClaudeMimicryAudit("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})
	snapshot, ok := LatestClaudeMimicryAudit()
	if !ok {
		t.Fatalf("LatestClaudeMimicryAudit ok = false, want true")
	}
	if snapshot.Model != "claude-sonnet-4-6" || snapshot.RequestPath != "/v1/messages" {
		t.Fatalf("snapshot model/path = %q/%q", snapshot.Model, snapshot.RequestPath)
	}
}

func TestEvaluateClaudeMimicryGuard_DegradeAllowsUnknownTools(t *testing.T) {
	payload := buildSignedClaudeMimicryTestPayload(t)
	payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"name":"CustomWorkflow","description":"Run a custom workflow","input_schema":{"type":"object","properties":{"task":{"type":"string"}}}}]`))
	payload = signAnthropicMessagesBody(payload)

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"CherryStudio/1.0"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})
	if audit.Tools.Status != ClaudeMimicryStatusWarning {
		t.Fatalf("tools status = %q, want warning for unknown but non-leaky tool", audit.Tools.Status)
	}

	decision := EvaluateClaudeMimicryGuard(audit, &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	})

	if decision.Action != ClaudeMimicryGuardActionDegrade {
		t.Fatalf("decision.Action = %q, want degrade; reasons=%v", decision.Action, decision.Reasons)
	}
	if decision.Blocked {
		t.Fatalf("decision.Blocked = true, want false; reasons=%v", decision.Reasons)
	}
}

func TestEvaluateClaudeMimicryGuard_CCHMismatchOnlyBlocksWhenSigningRequired(t *testing.T) {
	payload := buildSignedClaudeMimicryTestPayload(t)
	payload, _ = sjson.SetBytes(payload, "metadata.after_signing", true)

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"claude-cli/2.1.148 (external, cli)"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "key-123", false, nil, &config.Config{})
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})
	if audit.CCH.Status != ClaudeMimicryStatusFailed {
		t.Fatalf("CCH = %#v, want failed", audit.CCH)
	}

	cfg := &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	}
	legacyDecision := EvaluateClaudeMimicryGuardWithPolicy(audit, cfg, ClaudeMimicryGuardPolicy{
		RequireSignedCCH: false,
	})
	if legacyDecision.Blocked {
		t.Fatalf("legacy decision blocked = true, want false; reasons=%v", legacyDecision.Reasons)
	}
	if legacyDecision.Action != ClaudeMimicryGuardActionDegrade {
		t.Fatalf("legacy decision action = %q, want degrade; reasons=%v", legacyDecision.Action, legacyDecision.Reasons)
	}

	oauthDecision := EvaluateClaudeMimicryGuardWithPolicy(audit, cfg, ClaudeMimicryGuardPolicy{
		RequireSignedCCH: true,
	})
	if !oauthDecision.Blocked {
		t.Fatalf("oauth decision blocked = false, want true; reasons=%v", oauthDecision.Reasons)
	}
}

func TestEvaluateClaudeMimicryGuard_SystemMismatchOnlyBlocksWhenRequired(t *testing.T) {
	audit := ClaudeMimicryAuditSnapshot{
		Status:   ClaudeMimicryStatusFailed,
		System:   ClaudeMimicrySystemAudit{Status: ClaudeMimicryStatusFailed},
		Failures: []string{"system: system blocks do not match Claude Code baseline"},
	}
	cfg := &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	}

	legacyHaikuDecision := EvaluateClaudeMimicryGuardWithPolicy(audit, cfg, ClaudeMimicryGuardPolicy{
		RequireSystemBlocks: false,
	})
	if legacyHaikuDecision.Blocked {
		t.Fatalf("legacy haiku decision blocked = true, want false; reasons=%v", legacyHaikuDecision.Reasons)
	}
	if legacyHaikuDecision.Action != ClaudeMimicryGuardActionDegrade {
		t.Fatalf("legacy haiku decision action = %q, want degrade", legacyHaikuDecision.Action)
	}

	defaultDecision := EvaluateClaudeMimicryGuardWithPolicy(audit, cfg, ClaudeMimicryGuardPolicy{
		RequireSystemBlocks: true,
	})
	if !defaultDecision.Blocked {
		t.Fatalf("default decision blocked = false, want true; reasons=%v", defaultDecision.Reasons)
	}
}

func TestEvaluateClaudeMimicryGuard_DegradeDoesNotBlockKnownClaudeCodeToolDescription(t *testing.T) {
	payload := buildSignedClaudeMimicryTestPayload(t)
	payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"name":"Agent","description":"Launch an official Claude Code subagent that may relay work back to the main session.","input_schema":{"type":"object","properties":{"prompt":{"type":"string"}}}}]`))
	payload = signAnthropicMessagesBody(payload)

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"claude-cli/2.1.148 (external, cli)"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})

	decision := EvaluateClaudeMimicryGuard(audit, &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	})

	if decision.Blocked {
		t.Fatalf("decision.Blocked = true, want false for known Claude Code Agent tool; reasons=%v", decision.Reasons)
	}
}

func TestEvaluateClaudeMimicryGuard_DegradeDoesNotBlockTypedWebSearchTool(t *testing.T) {
	payload := buildSignedClaudeMimicryTestPayload(t)
	payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"type":"web_search_20250305","name":"web_search"},{"type":"web_search_20250305","name":"websearch"}]`))
	payload = signAnthropicMessagesBody(payload)

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"claude-cli/2.1.148 (external, cli)"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})

	decision := EvaluateClaudeMimicryGuard(audit, &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	})

	if decision.Blocked {
		t.Fatalf("decision.Blocked = true, want false for typed web search tools; reasons=%v", decision.Reasons)
	}
	if len(audit.Tools.LeakyNames) != 0 {
		t.Fatalf("audit.Tools.LeakyNames = %#v, want none for typed web search tools", audit.Tools.LeakyNames)
	}
}

func TestEvaluateClaudeMimicryGuard_DegradeAllowsFutureClaudeCodeToolSchema(t *testing.T) {
	payload := buildSignedClaudeMimicryTestPayload(t)
	payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"name":"ExitPlanMode","description":"Request permission to leave planning mode.","input_schema":{"type":"object","properties":{"session_id":{"type":"string"},"plan":{"type":"string"}}}}]`))
	payload = signAnthropicMessagesBody(payload)

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"claude-cli/2.1.148 (external, cli)"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})

	decision := EvaluateClaudeMimicryGuard(audit, &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	})

	if decision.Blocked {
		t.Fatalf("decision.Blocked = true, want false for future Claude Code tool schema; reasons=%v", decision.Reasons)
	}
}

func TestEvaluateClaudeMimicryGuard_DegradeAllowsMCPToolSchema(t *testing.T) {
	payload := buildSignedClaudeMimicryTestPayload(t)
	payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"name":"mcp__workspace__query","description":"Query a workspace MCP server.","input_schema":{"type":"object","properties":{"session_id":{"type":"string"},"query":{"type":"string"}}}}]`))
	payload = signAnthropicMessagesBody(payload)

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"claude-cli/2.1.148 (external, cli)"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})

	decision := EvaluateClaudeMimicryGuard(audit, &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	})

	if decision.Blocked {
		t.Fatalf("decision.Blocked = true, want false for MCP tool schema; reasons=%v", decision.Reasons)
	}
}

func TestEvaluateClaudeMimicryGuard_DegradeAllowsSuspiciousDescriptionWithoutHardToolLeak(t *testing.T) {
	payload := buildSignedClaudeMimicryTestPayload(t)
	payload, _ = sjson.SetRawBytes(payload, "tools", []byte(`[{"name":"CustomWorkflow","description":"Relay work to a local helper.","input_schema":{"type":"object","properties":{"task":{"type":"string"}}}}]`))
	payload = signAnthropicMessagesBody(payload)

	req := newClaudeHeaderTestRequest(t, http.Header{
		"User-Agent": []string{"claude-cli/2.1.148 (external, cli)"},
	})
	applyClaudeHeaders(req, &cliproxyauth.Auth{}, "sk-ant-oat-test", false, nil, &config.Config{})
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", payload, req.Header, &config.Config{})

	decision := EvaluateClaudeMimicryGuard(audit, &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	})

	if decision.Blocked {
		t.Fatalf("decision.Blocked = true, want false for description-only suspicion; reasons=%v", decision.Reasons)
	}
}

func TestEvaluateClaudeMimicryGuard_DegradeBlocksHardLeak(t *testing.T) {
	audit := AuditClaudeMimicryRequest("claude-sonnet-4-6", "/v1/messages", []byte(`{
		"system":[{"type":"text","text":"You are Hermes proxy."}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"tools":[{"name":"cronjob","description":"Hermes cronjob tool","input_schema":{"type":"object","properties":{"wake_at":{"type":"string"}}}}]
	}`), http.Header{
		"User-Agent":       []string{"Hermes/1.0"},
		"Anthropic-Beta":   []string{"unknown-client-beta"},
		"X-Hermes-Version": []string{"1.0"},
	}, &config.Config{})

	decision := EvaluateClaudeMimicryGuard(audit, &config.Config{
		ClaudeMimicryGuard: config.ClaudeMimicryGuardConfig{Mode: "degrade"},
	})

	if decision.Action != ClaudeMimicryGuardActionBlock {
		t.Fatalf("decision.Action = %q, want block; reasons=%v", decision.Action, decision.Reasons)
	}
	if !decision.Blocked {
		t.Fatalf("decision.Blocked = false, want true")
	}
}

func TestRecordClaudeMimicryEventTracksRecentEventsAndClientStats(t *testing.T) {
	ResetClaudeMimicryEventsForTest()
	t.Cleanup(ResetClaudeMimicryEventsForTest)

	RecordClaudeMimicryEvent(ClaudeMimicryEvent{
		RequestID:         "req-1",
		ClientSource:      "cherrystudio",
		Action:            ClaudeMimicryGuardActionDegrade,
		Model:             "claude-sonnet-4-6",
		UpstreamAttempted: true,
	})
	RecordClaudeMimicryEvent(ClaudeMimicryEvent{
		RequestID:         "req-2",
		ClientSource:      "hermes",
		Action:            ClaudeMimicryGuardActionBlock,
		Model:             "claude-sonnet-4-6",
		UpstreamAttempted: false,
	})

	events := LatestClaudeMimicryEvents(10)
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].RequestID != "req-2" || events[1].RequestID != "req-1" {
		t.Fatalf("events order = %#v, want newest first", events)
	}
	stats := ClaudeMimicryClientStats()
	if got := stats["cherrystudio"].Degraded; got != 1 {
		t.Fatalf("cherrystudio degraded = %d, want 1", got)
	}
	if got := stats["hermes"].Blocked; got != 1 {
		t.Fatalf("hermes blocked = %d, want 1", got)
	}
}

func TestRecordClaudeMimicryEventUsesConfiguredLimit(t *testing.T) {
	ResetClaudeMimicryEventsForTest()
	t.Cleanup(ResetClaudeMimicryEventsForTest)

	RecordClaudeMimicryEvent(ClaudeMimicryEvent{RequestID: "req-1", EventLimit: 2})
	RecordClaudeMimicryEvent(ClaudeMimicryEvent{RequestID: "req-2", EventLimit: 2})
	RecordClaudeMimicryEvent(ClaudeMimicryEvent{RequestID: "req-3", EventLimit: 2})

	events := LatestClaudeMimicryEvents(10)
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].RequestID != "req-3" || events[1].RequestID != "req-2" {
		t.Fatalf("events order = %#v, want newest two events", events)
	}
}

func TestClassifyClaudeMimicryClientSource(t *testing.T) {
	cases := []struct {
		name         string
		sourceFormat string
		headers      http.Header
		want         string
	}{
		{name: "claude code", sourceFormat: "claude", headers: http.Header{"User-Agent": []string{"claude-cli/2.1.148 (external, cli)"}}, want: "claude-code"},
		{name: "cherry", sourceFormat: "openai", headers: http.Header{"User-Agent": []string{"CherryStudio/1.0"}}, want: "cherrystudio"},
		{name: "hermes header", sourceFormat: "openai", headers: http.Header{"X-Hermes-Version": []string{"1.0"}}, want: "hermes"},
		{name: "openclaw", sourceFormat: "claude", headers: http.Header{"User-Agent": []string{"OpenClaw/0.9"}}, want: "openclaw"},
		{name: "opencode", sourceFormat: "openai", headers: http.Header{"User-Agent": []string{"opencode/1.0"}}, want: "opencode"},
		{name: "openai compat", sourceFormat: "openai", headers: http.Header{"User-Agent": []string{"curl/8.7.1"}}, want: "openai-compatible"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyClaudeMimicryClientSource(tc.sourceFormat, tc.headers); got != tc.want {
				t.Fatalf("source = %q, want %q", got, tc.want)
			}
		})
	}
}
