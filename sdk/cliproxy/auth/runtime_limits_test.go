package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type runtimeLimitExecutor struct {
	mu    sync.Mutex
	calls []string
}

func (e *runtimeLimitExecutor) Identifier() string { return "claude" }

func (e *runtimeLimitExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *runtimeLimitExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *runtimeLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *runtimeLimitExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, auth.ID)
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *runtimeLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *runtimeLimitExecutor) snapshotCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func registerClaudeRuntimeLimitAuth(t *testing.T, manager *Manager, id string, attrs map[string]string) {
	t.Helper()
	if attrs == nil {
		attrs = make(map[string]string)
	}
	attrs["runtime_only"] = "true"
	if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:         id,
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: attrs,
		Metadata: map[string]any{
			"type": "claude",
		},
	}); err != nil {
		t.Fatalf("Register(%s) returned error: %v", id, err)
	}
}

func TestManagerSkipsClaudeAuthAtRPMLimit(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	exec := &runtimeLimitExecutor{}
	manager.RegisterExecutor(exec)
	registerClaudeRuntimeLimitAuth(t, manager, "auth-a", map[string]string{"rpm_limit": "1"})
	registerClaudeRuntimeLimitAuth(t, manager, "auth-b", map[string]string{"rpm_limit": "1"})
	model := "claude-sonnet-4-5"
	registerSchedulerModels(t, "claude", model, "auth-a", "auth-b")

	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"first"}]}`)}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	req.Payload = []byte(`{"messages":[{"role":"user","content":"second"}]}`)
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}

	got := exec.snapshotCalls()
	want := []string{"auth-a", "auth-b"}
	if len(got) != len(want) {
		t.Fatalf("executor calls = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("executor calls = %#v, want %#v", got, want)
		}
	}
}

func TestManagerSkipsClaudeAuthWhenNewSessionWouldExceedMax(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	exec := &runtimeLimitExecutor{}
	manager.RegisterExecutor(exec)
	registerClaudeRuntimeLimitAuth(t, manager, "auth-a", map[string]string{"max_sessions": "1"})
	registerClaudeRuntimeLimitAuth(t, manager, "auth-b", map[string]string{"max_sessions": "1"})
	model := "claude-sonnet-4-5"
	registerSchedulerModels(t, "claude", model, "auth-a", "auth-b")

	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"session-1"}}}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, opts); err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	opts.Headers = http.Header{"X-Session-Id": []string{"session-2"}}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, opts); err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}

	got := exec.snapshotCalls()
	want := []string{"auth-a", "auth-b"}
	if len(got) != len(want) {
		t.Fatalf("executor calls = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("executor calls = %#v, want %#v", got, want)
		}
	}
}

func TestManagerDoesNotCountMessageHashFallbackTowardMaxSessions(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	exec := &runtimeLimitExecutor{}
	manager.RegisterExecutor(exec)
	registerClaudeRuntimeLimitAuth(t, manager, "auth-a", map[string]string{"max_sessions": "1"})
	model := "claude-sonnet-4-5"
	registerSchedulerModels(t, "claude", model, "auth-a")

	first := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"first unrelated prompt"}]}`)}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, first, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	second := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"second unrelated prompt"}]}`)}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, second, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}

	if got := exec.snapshotCalls(); len(got) != 2 {
		t.Fatalf("executor calls = %#v, want 2 successful calls", got)
	}
}

func TestManagerDoesNotCountClientRequestIDTowardMaxSessions(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	exec := &runtimeLimitExecutor{}
	manager.RegisterExecutor(exec)
	registerClaudeRuntimeLimitAuth(t, manager, "auth-a", map[string]string{"max_sessions": "1"})
	model := "claude-sonnet-4-5"
	registerSchedulerModels(t, "claude", model, "auth-a")
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}

	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{
		Headers: http.Header{"X-Client-Request-Id": []string{"request-1"}},
	}); err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, cliproxyexecutor.Options{
		Headers: http.Header{"X-Client-Request-Id": []string{"request-2"}},
	}); err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}

	if got := exec.snapshotCalls(); len(got) != 2 {
		t.Fatalf("executor calls = %#v, want 2 successful calls", got)
	}
}

func TestManagerAllowsExistingRealSessionWhenMaxSessionsReached(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	exec := &runtimeLimitExecutor{}
	manager.RegisterExecutor(exec)
	registerClaudeRuntimeLimitAuth(t, manager, "auth-a", map[string]string{"max_sessions": "1"})
	model := "claude-sonnet-4-5"
	registerSchedulerModels(t, "claude", model, "auth-a")
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	sessionOne := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"session-1"}}}
	sessionTwo := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"session-2"}}}

	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, sessionOne); err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, sessionTwo); err == nil {
		t.Fatalf("second Execute with new session returned nil error, want session limit error")
	}
	if _, err := manager.Execute(context.Background(), []string{"claude"}, req, sessionOne); err != nil {
		t.Fatalf("existing session Execute returned error: %v", err)
	}

	if got := exec.snapshotCalls(); len(got) != 2 {
		t.Fatalf("executor calls = %#v, want first and existing-session calls only", got)
	}
}

func TestManagerMarkResultRecordsQuality24h(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	registerClaudeRuntimeLimitAuth(t, manager, "auth-a", nil)

	manager.MarkResult(context.Background(), Result{AuthID: "auth-a", Provider: "claude", Model: "claude-sonnet-4-5", Success: true})
	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-a",
		Provider: "claude",
		Model:    "claude-sonnet-4-5",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"},
	})

	gotAuth, ok := manager.GetByID("auth-a")
	if !ok || gotAuth == nil {
		t.Fatalf("GetByID returned ok=%v auth=%v", ok, gotAuth)
	}
	stats := gotAuth.Quality24hStats(time.Now())
	if stats.Requests != 2 || stats.Success != 1 || stats.Failed != 1 || stats.RateLimited != 1 {
		t.Fatalf("Quality24hStats = %#v, want requests=2 success=1 failed=1 rateLimited=1", stats)
	}
	if stats.SuccessRate != 50 {
		t.Fatalf("SuccessRate = %v, want 50", stats.SuccessRate)
	}
}
