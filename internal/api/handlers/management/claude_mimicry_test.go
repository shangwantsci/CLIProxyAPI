package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
)

func TestGetClaudeMimicryAuditReturnsWaitingBaselineWithoutTraffic(t *testing.T) {
	executor.ResetClaudeMimicryAuditForTest()
	t.Cleanup(executor.ResetClaudeMimicryAuditForTest)

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-mimicry-audit?model=claude-sonnet-4-6", nil)

	h.GetClaudeMimicryAudit(ctx)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got, _ := payload["has_recent_request"].(bool); got {
		t.Fatalf("has_recent_request = true, want false")
	}
	audit, ok := payload["audit"].(map[string]any)
	if !ok {
		t.Fatalf("audit missing from response: %#v", payload)
	}
	if got, _ := audit["status"].(string); got != string(executor.ClaudeMimicryStatusWaiting) {
		t.Fatalf("audit.status = %q, want waiting", got)
	}
	baseline, ok := audit["baseline"].(map[string]any)
	if !ok || baseline["cch_seed"] == "" || baseline["expected_beta_count"] == nil {
		t.Fatalf("baseline incomplete: %#v", audit["baseline"])
	}
}

func TestGetClaudeMimicryEventsReturnsRecentEventsAndClientStats(t *testing.T) {
	executor.ResetClaudeMimicryEventsForTest()
	t.Cleanup(executor.ResetClaudeMimicryEventsForTest)

	executor.RecordClaudeMimicryEvent(executor.ClaudeMimicryEvent{
		RequestID:    "req-1",
		ClientSource: "cherrystudio",
		Action:       executor.ClaudeMimicryGuardActionDegrade,
		Model:        "claude-sonnet-4-6",
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-mimicry-events?limit=5", nil)

	h.GetClaudeMimicryEvents(ctx)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	events, ok := payload["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events = %#v, want one event", payload["events"])
	}
	stats, ok := payload["client_stats"].(map[string]any)
	if !ok {
		t.Fatalf("client_stats missing: %#v", payload)
	}
	cherry, ok := stats["cherrystudio"].(map[string]any)
	if !ok || cherry["degraded"] == nil {
		t.Fatalf("cherrystudio stats incomplete: %#v", stats["cherrystudio"])
	}
}

func TestGetClaudeMimicryEventsHonorsLimitAboveDefault(t *testing.T) {
	executor.ResetClaudeMimicryEventsForTest()
	t.Cleanup(executor.ResetClaudeMimicryEventsForTest)

	for i := 0; i < 120; i++ {
		executor.RecordClaudeMimicryEvent(executor.ClaudeMimicryEvent{
			RequestID:    "req",
			ClientSource: "cherrystudio",
			Action:       executor.ClaudeMimicryGuardActionAllow,
		})
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-mimicry-events?limit=110", nil)

	h.GetClaudeMimicryEvents(ctx)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	events, ok := payload["events"].([]any)
	if !ok || len(events) != 110 {
		t.Fatalf("events len = %d, want 110", len(events))
	}
}
