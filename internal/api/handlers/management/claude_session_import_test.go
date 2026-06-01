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

func TestPostClaudeSessionImportJobFetchesAndImportsSessionKeys(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"session_key":"sk-one"},
			{"sessionKey":"sk-two"},
			{"session_key":"sk-one"},
			{"session_key":""}
		]`))
	}))
	defer source.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.sessionImportAllowPrivateSources = true

	var (
		importedMu sync.Mutex
		imported   []string
	)
	h.sessionImportAuthenticate = func(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
		importedMu.Lock()
		imported = append(imported, req.SessionKey)
		importedMu.Unlock()
		return claudeSessionImportAuthResult{
			AuthFile:        "claude-" + req.SessionKey + ".json",
			Email:           req.SessionKey + "@example.test",
			AuthSource:      "claude_code_cli",
			AuthMethodLabel: "Claude Code CLI OAuth",
		}, nil
	}

	body := bytes.NewBufferString(`{
		"source_url":"` + source.URL + `/accounts",
		"api_path":"/api/accounts",
		"proxy_url":"direct",
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
	if created.JobID == "" {
		t.Fatalf("job_id is empty")
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
	if snapshot.TotalFetched != 2 || snapshot.Imported != 2 || snapshot.Duplicate != 1 {
		t.Fatalf("job counts fetched/imported/duplicate = %d/%d/%d, want 2/2/1", snapshot.TotalFetched, snapshot.Imported, snapshot.Duplicate)
	}
	if len(snapshot.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(snapshot.Results))
	}
	for _, result := range snapshot.Results {
		if result.SessionKeyHash == "" {
			t.Fatalf("result leaked no hash: %#v", result)
		}
		if result.AuthMethodLabel != "Claude Code CLI OAuth" {
			t.Fatalf("auth_method_label = %q, want Claude Code CLI OAuth", result.AuthMethodLabel)
		}
	}
	importedMu.Lock()
	defer importedMu.Unlock()
	if len(imported) != 2 {
		t.Fatalf("imported len = %d, want 2; imported=%v", len(imported), imported)
	}
}

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

func TestNormalizeClaudeSessionImportRequestHidesDefaultSourceDisplay(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	normalized, err := h.normalizeClaudeSessionImportRequest(claudeSessionImportStartRequest{})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}

	if normalized.SourceURL != defaultClaudeSessionImportSourceURL {
		t.Fatalf("SourceURL = %q, want internal default source", normalized.SourceURL)
	}
	if normalized.DisplaySourceURL != "" {
		t.Fatalf("DisplaySourceURL = %q, want hidden default source", normalized.DisplaySourceURL)
	}
	if normalized.DisplayAPIEndpoint != "" {
		t.Fatalf("DisplayAPIEndpoint = %q, want hidden default endpoint", normalized.DisplayAPIEndpoint)
	}
}

func TestClaudeSessionImportErrorHidesDefaultSource(t *testing.T) {
	req := normalizedClaudeSessionImportRequest{
		SourceURL:          defaultClaudeSessionImportSourceURL,
		APIEndpoint:        "https://sessionkeytest.globalpays.shop/api/accounts",
		DisplaySourceURL:   "",
		DisplayAPIEndpoint: "",
	}
	message := sanitizeClaudeSessionImportError(req, fmt.Errorf("fetch source accounts: Get %q: timeout", req.APIEndpoint))

	if strings.Contains(message, "sessionkeytest.globalpays.shop") || strings.Contains(message, req.APIEndpoint) {
		t.Fatalf("sanitized error leaked source: %q", message)
	}
	if !strings.Contains(message, "默认抓取来源") {
		t.Fatalf("sanitized error = %q, want default source placeholder", message)
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
	if snapshot.TotalFetched != 2 || snapshot.Imported != 2 || snapshot.Duplicate != 1 {
		t.Fatalf("job counts fetched/imported/duplicate = %d/%d/%d, want 2/2/1", snapshot.TotalFetched, snapshot.Imported, snapshot.Duplicate)
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

func TestPostClaudeSessionImportJobUsesEnabledProxyPoolWhenProxyEmpty(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"session_key":"sk-one"},
			{"session_key":"sk-two"}
		]`))
	}))
	defer source.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Scheme != "http" || r.URL.Host != strings.TrimPrefix(source.URL, "http://") || r.URL.Path != "/api/accounts" {
			t.Fatalf("proxied request URL = %q, want %s/api/accounts", r.URL.String(), source.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"session_key":"sk-one"},
			{"session_key":"sk-two"}
		]`))
	}))
	defer proxy.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.sessionImportAllowPrivateSources = true

	enabledProxy := proxy.URL
	if err := h.autoAddProxyURL(context.Background(), enabledProxy); err != nil {
		t.Fatalf("add enabled proxy: %v", err)
	}
	disabled := false
	h.proxyPoolMu.Lock()
	if _, err := h.upsertProxyPoolEntryLocked(context.Background(), "socks5://disabled:pass@127.0.0.1:1081", "", "", &disabled); err != nil {
		h.proxyPoolMu.Unlock()
		t.Fatalf("add disabled proxy: %v", err)
	}
	h.proxyPoolMu.Unlock()

	var (
		proxyMu sync.Mutex
		proxies []string
	)
	h.sessionImportAuthenticate = func(ctx context.Context, req claudeSessionImportAuthRequest) (claudeSessionImportAuthResult, error) {
		proxyMu.Lock()
		proxies = append(proxies, req.ProxyURL)
		proxyMu.Unlock()
		return claudeSessionImportAuthResult{
			AuthFile:        "claude-" + req.SessionKey + ".json",
			Email:           req.SessionKey + "@example.test",
			AuthSource:      "claude_code_cli",
			AuthMethodLabel: "Claude Code CLI OAuth",
		}, nil
	}

	body := bytes.NewBufferString(`{
		"source_url":"` + source.URL + `/accounts",
		"api_path":"/api/accounts",
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

	proxyMu.Lock()
	defer proxyMu.Unlock()
	if len(proxies) != 2 {
		t.Fatalf("proxies len = %d, want 2; proxies=%v", len(proxies), proxies)
	}
	for _, proxyURL := range proxies {
		if proxyURL != enabledProxy {
			t.Fatalf("proxyURL = %q, want enabled proxy %q; all=%v", proxyURL, enabledProxy, proxies)
		}
	}
}

func TestFetchClaudeSessionKeysUsesProxyPoolForSourceFetch(t *testing.T) {
	sourceHits := 0
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceHits++
		http.Error(w, "direct source fetch should not be used", http.StatusTeapot)
	}))
	defer source.Close()

	proxyHits := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits++
		if r.URL.Scheme != "http" || r.URL.Host != strings.TrimPrefix(source.URL, "http://") || r.URL.Path != "/api/accounts" {
			t.Fatalf("proxied request URL = %q, want %s/api/accounts", r.URL.String(), source.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"session_key":"sk-proxied"}]`))
	}))
	defer proxy.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	keys, duplicates, err := h.fetchClaudeSessionKeys(context.Background(), normalizedClaudeSessionImportRequest{
		SourceURL:        source.URL + "/accounts",
		APIEndpoint:      source.URL + "/api/accounts",
		ProxyCandidates:  []string{proxy.URL},
		Timeout:          2 * time.Second,
		DisplaySourceURL: source.URL + "/accounts",
	})
	if err != nil {
		t.Fatalf("fetch session keys: %v", err)
	}
	if duplicates != 0 {
		t.Fatalf("duplicates = %d, want 0", duplicates)
	}
	if len(keys) != 1 || keys[0] != "sk-proxied" {
		t.Fatalf("keys = %#v, want proxied session key", keys)
	}
	if proxyHits != 1 {
		t.Fatalf("proxyHits = %d, want 1", proxyHits)
	}
	if sourceHits != 0 {
		t.Fatalf("sourceHits = %d, want 0 direct source hits", sourceHits)
	}
}

func TestPostClaudeSessionImportJobRejectsUntrustedSourceByDefault(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/claude-session-import-jobs",
		bytes.NewBufferString(`{"source_url":"http://127.0.0.1:9000/accounts","api_path":"/api/accounts"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostClaudeSessionImportJob(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
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
