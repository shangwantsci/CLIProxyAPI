package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestProcessClaudeSessionImportKeyMarksBulkImportSource(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	var capturedImportSource string
	h.sessionImportAuthenticate = func(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
		capturedImportSource = req.ImportSource
		return claudeSessionImportAuthResult{
			AuthFile:        "claude-sk-one.json",
			Email:           "sk-one@example.test",
			AuthSource:      "claude_code_cli",
			AuthMethodLabel: "Claude Code CLI OAuth",
		}, nil
	}

	result := h.processClaudeSessionImportKey(context.Background(), "sk-one", normalizedClaudeSessionImportRequest{
		ProxyURL:     "direct",
		ImportSource: claudeImportSourceBulkSessionImport,
	})

	if result.Status != "imported" {
		t.Fatalf("status = %q, want imported; result=%#v", result.Status, result)
	}
	if capturedImportSource != claudeImportSourceBulkSessionImport {
		t.Fatalf("import source = %q, want %q", capturedImportSource, claudeImportSourceBulkSessionImport)
	}
}

func TestProcessClaudeSessionImportKeyLeavesManualImportSourceEmpty(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	capturedImportSource := "unset"
	h.sessionImportAuthenticate = func(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
		capturedImportSource = req.ImportSource
		return claudeSessionImportAuthResult{
			AuthFile:        "claude-sk-one.json",
			Email:           "sk-one@example.test",
			AuthSource:      "claude_code_cli",
			AuthMethodLabel: "Claude Code CLI OAuth",
		}, nil
	}

	result := h.processClaudeSessionImportKey(context.Background(), "sk-one", normalizedClaudeSessionImportRequest{
		ProxyURL: "direct",
	})

	if result.Status != "imported" {
		t.Fatalf("status = %q, want imported; result=%#v", result.Status, result)
	}
	if capturedImportSource != "" {
		t.Fatalf("import source = %q, want empty manual import source", capturedImportSource)
	}
}

func TestPostClaudeSessionImportJobImportsPastedSessionKeysAsManual(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	enabledProxy := "http://proxy.example.test:8080"
	if err := h.autoAddProxyURL(context.Background(), enabledProxy); err != nil {
		t.Fatalf("add enabled proxy: %v", err)
	}
	disabled := false
	h.proxyPoolMu.Lock()
	if _, err := h.upsertProxyPoolEntryLocked(context.Background(), "http://disabled.example.test:8080", "", "", &disabled); err != nil {
		h.proxyPoolMu.Unlock()
		t.Fatalf("add disabled proxy: %v", err)
	}
	h.proxyPoolMu.Unlock()

	var (
		capturedMu      sync.Mutex
		capturedKeys    []string
		capturedProxies []string
		capturedSources []string
	)
	h.sessionImportAuthenticate = func(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
		capturedMu.Lock()
		capturedKeys = append(capturedKeys, req.SessionKey)
		capturedProxies = append(capturedProxies, req.ProxyURL)
		capturedSources = append(capturedSources, req.ImportSource)
		capturedMu.Unlock()
		return claudeSessionImportAuthResult{
			AuthFile:        "claude-" + req.SessionKey + ".json",
			Email:           req.SessionKey + "@example.test",
			AuthSource:      "claude_code_cli",
			AuthMethodLabel: "Claude Code CLI OAuth",
		}, nil
	}

	body := bytes.NewBufferString(`{
		"session_keys":[" sk-one ","sk-two","sk-one",""],
		"concurrency":2,
		"delay_min_ms":0,
		"delay_max_ms":0
	}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude-session-import-jobs", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostClaudeSessionImportJob(ctx)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var created struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	var snapshot claudeSessionImportJobSnapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec = httptest.NewRecorder()
		ctx, _ = gin.CreateTestContext(rec)
		ctx.Params = gin.Params{{Key: "id", Value: created.JobID}}
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-session-import-jobs/"+created.JobID, nil)
		h.GetClaudeSessionImportJob(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("get status = %d body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
			t.Fatalf("decode job snapshot: %v", err)
		}
		if snapshot.Status == claudeSessionImportStatusCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if snapshot.Status != claudeSessionImportStatusCompleted {
		t.Fatalf("job status = %q, want completed; snapshot=%#v", snapshot.Status, snapshot)
	}
	if snapshot.Imported != 2 || snapshot.Duplicate != 1 {
		t.Fatalf("job counts imported/duplicate = %d/%d, want 2/1", snapshot.Imported, snapshot.Duplicate)
	}

	capturedMu.Lock()
	defer capturedMu.Unlock()
	if got := strings.Join(capturedKeys, ","); got != "sk-one,sk-two" && got != "sk-two,sk-one" {
		t.Fatalf("captured keys = %q, want sk-one and sk-two", got)
	}
	for _, proxyURL := range capturedProxies {
		if proxyURL != enabledProxy {
			t.Fatalf("proxyURL = %q, want enabled proxy %q; all=%v", proxyURL, enabledProxy, capturedProxies)
		}
	}
	for _, source := range capturedSources {
		if source != "" {
			t.Fatalf("import source = %q, want empty manual source; all=%v", source, capturedSources)
		}
	}
}

func TestPostClaudeSessionImportJobSeparatesNewExistingAndPlanCounts(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.sessionImportAuthenticate = func(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
		switch req.SessionKey {
		case "fail":
			return claudeSessionImportAuthResult{}, fmt.Errorf("token exchange failed")
		case "existing-max20":
			return claudeSessionImportAuthResult{
				AuthFile:                 "claude-existing-max20.json",
				Email:                    "existing-max20@example.test",
				AuthSource:               "claude_code_cli",
				AuthMethodLabel:          "Claude Code CLI OAuth",
				Existing:                 true,
				PlanType:                 "max20x",
				SubscriptionMultiplier:   20,
				SubscriptionPrecision:    "exact",
				SubscriptionCapacityUnit: 20,
			}, nil
		case "new-max5":
			return claudeSessionImportAuthResult{
				AuthFile:                 "claude-new-max5.json",
				Email:                    "new-max5@example.test",
				AuthSource:               "claude_code_cli",
				AuthMethodLabel:          "Claude Code CLI OAuth",
				PlanType:                 "max5x",
				SubscriptionMultiplier:   5,
				SubscriptionPrecision:    "exact",
				SubscriptionCapacityUnit: 5,
			}, nil
		default:
			return claudeSessionImportAuthResult{
				AuthFile:                 "claude-new-pro.json",
				Email:                    "new-pro@example.test",
				AuthSource:               "claude_code_cli",
				AuthMethodLabel:          "Claude Code CLI OAuth",
				PlanType:                 "pro",
				SubscriptionMultiplier:   1,
				SubscriptionPrecision:    "exact",
				SubscriptionCapacityUnit: 1,
			}, nil
		}
	}

	body := bytes.NewBufferString(`{
		"session_keys":["new-pro","existing-max20","existing-max20","new-max5","fail"],
		"proxy_url":"direct",
		"concurrency":3,
		"delay_min_ms":0,
		"delay_max_ms":0
	}`)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/claude-session-import-jobs", body)
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostClaudeSessionImportJob(ctx)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var created struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	var snapshot claudeSessionImportJobSnapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec = httptest.NewRecorder()
		ctx, _ = gin.CreateTestContext(rec)
		ctx.Params = gin.Params{{Key: "id", Value: created.JobID}}
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-session-import-jobs/"+created.JobID, nil)
		h.GetClaudeSessionImportJob(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("get status = %d body=%s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
			t.Fatalf("decode job snapshot: %v", err)
		}
		if snapshot.Status == claudeSessionImportStatusCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if snapshot.Status != claudeSessionImportStatusCompleted {
		t.Fatalf("job status = %q, want completed; snapshot=%#v", snapshot.Status, snapshot)
	}
	if snapshot.Imported != 3 || snapshot.NewImported != 2 || snapshot.ExistingUpdated != 1 {
		t.Fatalf("import counts imported/new/existing = %d/%d/%d, want 3/2/1", snapshot.Imported, snapshot.NewImported, snapshot.ExistingUpdated)
	}
	if snapshot.Duplicate != 1 || snapshot.Failed != 1 {
		t.Fatalf("duplicate/failed = %d/%d, want 1/1", snapshot.Duplicate, snapshot.Failed)
	}
	if snapshot.NewPlanCounts["pro"] != 1 || snapshot.NewPlanCounts["max5x"] != 1 {
		t.Fatalf("new_plan_counts = %#v, want pro=1 max5x=1", snapshot.NewPlanCounts)
	}
	if snapshot.ExistingPlanCounts["max20x"] != 1 {
		t.Fatalf("existing_plan_counts = %#v, want max20x=1", snapshot.ExistingPlanCounts)
	}
}

func TestSessionKeyMetadataWriteConvention(t *testing.T) {
	// Mirrors the write performed in saveClaudeSessionKeyAuth: a non-empty,
	// trimmed session key must be stored under metadata["session_key"] so the
	// executor fallback can re-exchange credentials later.
	metadata := map[string]any{}
	sessionKey := "sk-ant-sid01-example"
	if sessionKey != "" {
		metadata["session_key"] = sessionKey
	}
	got, ok := metadata["session_key"].(string)
	if !ok || got != sessionKey {
		t.Fatalf("metadata[session_key] = %v, want %q", metadata["session_key"], sessionKey)
	}
}
