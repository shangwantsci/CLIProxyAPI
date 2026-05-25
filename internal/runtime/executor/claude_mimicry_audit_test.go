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
