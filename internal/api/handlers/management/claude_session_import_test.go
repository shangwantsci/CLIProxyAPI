package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
