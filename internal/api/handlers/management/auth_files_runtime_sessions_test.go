package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type managementRuntimeSessionExecutor struct{}

func (e *managementRuntimeSessionExecutor) Identifier() string { return "claude" }

func (e *managementRuntimeSessionExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{}`)}, nil
}

func (e *managementRuntimeSessionExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *managementRuntimeSessionExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *managementRuntimeSessionExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{}`)}, nil
}

func (e *managementRuntimeSessionExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestClearAuthRuntimeSessionsClearsClaudeSessionCapacity(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(&managementRuntimeSessionExecutor{})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         "claude-a.json",
		FileName:   "claude-a.json",
		Provider:   "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"max_sessions": "1"},
		Metadata:   map[string]any{"type": "claude"},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	req := cliproxyexecutor.Request{Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{"session-1"}},
	}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	before, ok := manager.GetByID("claude-a.json")
	if !ok || before.RuntimeUsageStats(time.Now()).ActiveSessions != 1 {
		t.Fatalf("active sessions before clear = %#v, want 1", before)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/runtime-sessions/clear", strings.NewReader(`{"provider":"claude"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.ClearAuthRuntimeSessions(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"cleared_accounts":1`) || !strings.Contains(rec.Body.String(), `"cleared_sessions":1`) {
		t.Fatalf("body = %s, want cleared counts", rec.Body.String())
	}
	after, ok := manager.GetByID("claude-a.json")
	if !ok || after.RuntimeUsageStats(time.Now()).ActiveSessions != 0 {
		t.Fatalf("active sessions after clear = %#v, want 0", after)
	}
}
