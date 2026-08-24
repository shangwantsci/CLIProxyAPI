package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type accountLimitHook struct {
	NoopHook
	mu      sync.Mutex
	results []Result
}

func (h *accountLimitHook) OnResult(_ context.Context, result Result) {
	h.mu.Lock()
	h.results = append(h.results, result)
	h.mu.Unlock()
}

func (h *accountLimitHook) countFor(authID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, result := range h.results {
		if result.AuthID == authID {
			n++
		}
	}
	return n
}

type accountLimitExecutor struct {
	id string
	mu sync.Mutex

	executeIDs []string
	streamIDs  []string
	countIDs   []string
	prepareErr error

	blockStart chan struct{}
	blockWait  chan struct{}
	holdChunks chan cliproxyexecutor.StreamChunk
}

func (e *accountLimitExecutor) Identifier() string { return e.id }

func (e *accountLimitExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return e.prepareErr != nil && auth != nil && auth.ID == "auth-a"
}

func (e *accountLimitExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	if e.prepareErr != nil && auth != nil && auth.ID == "auth-a" {
		return nil, e.prepareErr
	}
	return auth, nil
}

func (e *accountLimitExecutor) record(ids *[]string, auth *Auth) {
	e.mu.Lock()
	*ids = append(*ids, auth.ID)
	e.mu.Unlock()
	if e.blockStart != nil {
		select {
		case <-e.blockStart:
		default:
			close(e.blockStart)
		}
	}
	if e.blockWait != nil {
		<-e.blockWait
	}
}

func (e *accountLimitExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(&e.executeIDs, auth)
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *accountLimitExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(&e.streamIDs, auth)
	if e.holdChunks != nil {
		return &cliproxyexecutor.StreamResult{Chunks: e.holdChunks}, nil
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *accountLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *accountLimitExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(&e.countIDs, auth)
	return cliproxyexecutor.Response{Payload: []byte("1")}, nil
}

func (e *accountLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *accountLimitExecutor) ids(kind string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch kind {
	case "stream":
		return append([]string(nil), e.streamIDs...)
	case "count":
		return append([]string(nil), e.countIDs...)
	default:
		return append([]string(nil), e.executeIDs...)
	}
}

func registerAccountLimitAuths(t *testing.T, manager *Manager, provider, model string, metas map[string]map[string]any) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for id, meta := range metas {
		reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
		copied := map[string]any{"disable_cooling": true}
		for key, value := range meta {
			copied[key] = value
		}
		auth := &Auth{
			ID:         id,
			Provider:   provider,
			Metadata:   copied,
			Attributes: map[string]string{},
		}
		if rawPriority, ok := copied["priority"]; ok {
			if parsed, okParse := parseIntAny(rawPriority); okParse {
				auth.Attributes["priority"] = strconv.Itoa(parsed)
			}
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
	}
}

func drainStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()
	if result == nil || result.Chunks == nil {
		return
	}
	for range result.Chunks {
	}
}

func TestExecuteFailsOverWhenAccountConcurrencyFull(t *testing.T) {
	hook := &accountLimitHook{}
	manager := NewManager(nil, nil, hook)
	manager.SetRetryConfig(0, 0, 0)
	executor := &accountLimitExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1, "priority": 10},
		"auth-b": {"priority": 1},
	})

	lease, _, ok := manager.limiter.TryAcquire(context.Background(), &Auth{ID: "auth-a", Metadata: map[string]any{"max_concurrent": 1}})
	if !ok {
		t.Fatal("seed acquire failed")
	}
	t.Cleanup(lease.Release)

	resp, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	if string(resp.Payload) != "auth-b" {
		t.Fatalf("payload = %s, want auth-b", resp.Payload)
	}
	if hook.countFor("auth-a") != 0 {
		t.Fatalf("limiter-rejected auth-a was marked: %#v", hook.results)
	}
	if got := executor.ids("execute"); len(got) != 1 || got[0] != "auth-b" {
		t.Fatalf("execute ids = %v, want [auth-b]", got)
	}
}

func TestAccountLimitRejectDoesNotConsumeMaxRetryCredentials(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 1)
	executor := &accountLimitExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1},
		"auth-b": {},
	})
	lease, _, ok := manager.limiter.TryAcquire(context.Background(), &Auth{ID: "auth-a", Metadata: map[string]any{"max_concurrent": 1}})
	if !ok {
		t.Fatal("seed acquire failed")
	}
	t.Cleanup(lease.Release)

	if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	if got := executor.ids("execute"); len(got) != 1 || got[0] != "auth-b" {
		t.Fatalf("execute ids = %v, want [auth-b]", got)
	}
}

func TestAccountLimitWaitBudgetDefaultAndZero(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	if got := manager.accountLimitWaitBudget(); got != defaultAccountLimitWait {
		t.Fatalf("default wait = %s, want %s", got, defaultAccountLimitWait)
	}

	manager.SetAccountLimitWait(0)
	if got := manager.accountLimitWaitBudget(); got != 0 {
		t.Fatalf("zero wait = %s, want 0", got)
	}

	manager.SetAccountLimitWait(-time.Second)
	if got := manager.accountLimitWaitBudget(); got != defaultAccountLimitWait {
		t.Fatalf("negative wait = %s, want default", got)
	}

	manager.SetAccountLimitWait(2 * time.Minute)
	if got := manager.accountLimitWaitBudget(); got != time.Duration(internalconfig.MaxAccountLimitWaitSeconds)*time.Second {
		t.Fatalf("clamped wait = %s, want %ds", got, internalconfig.MaxAccountLimitWaitSeconds)
	}
}

func TestExecuteReturns429ImmediatelyWhenWaitBudgetIsZero(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.SetAccountLimitWait(0)
	started := make(chan struct{})
	unblock := make(chan struct{})
	executor := &accountLimitExecutor{id: "codex", blockStart: started, blockWait: unblock}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1},
	})

	firstErr := make(chan error, 1)
	go func() {
		_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{})
		firstErr <- errExecute
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not start")
	}

	startedAt := time.Now()
	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{})
	if time.Since(startedAt) >= 150*time.Millisecond {
		t.Fatalf("zero-budget request waited %s", time.Since(startedAt))
	}
	var limitErr *AccountLimitError
	if !errors.As(errExecute, &limitErr) || limitErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("zero-budget execute = %v, want AccountLimitError 429", errExecute)
	}
	close(unblock)
	select {
	case errFirst := <-firstErr:
		if errFirst != nil {
			t.Fatalf("first execute: %v", errFirst)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first execute timed out")
	}
}

func TestExecuteWaitsWhenPoolIsConcurrencyFull(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	started := make(chan struct{})
	unblock := make(chan struct{})
	executor := &accountLimitExecutor{id: "codex", blockStart: started, blockWait: unblock}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1},
	})

	firstErr := make(chan error, 1)
	go func() {
		_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{})
		firstErr <- errExecute
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not start")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{})
		secondDone <- errExecute
	}()

	time.Sleep(250 * time.Millisecond)
	close(unblock)
	select {
	case errExecute := <-firstErr:
		if errExecute != nil {
			t.Fatalf("first execute: %v", errExecute)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first execute timed out")
	}
	select {
	case errExecute := <-secondDone:
		if errExecute != nil {
			t.Fatalf("second execute after wait: %v", errExecute)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting request did not complete")
	}
}

func TestExecuteReturns429WhenRPMBudgetCannotBeMet(t *testing.T) {
	hook := &accountLimitHook{}
	manager := NewManager(nil, nil, hook)
	manager.SetRetryConfig(3, 30*time.Second, 0)
	executor := &accountLimitExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_rpm": 1},
	})

	if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("first execute: %v", errExecute)
	}
	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{})
	var limitErr *AccountLimitError
	if !errors.As(errExecute, &limitErr) || limitErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("second execute = %v, want AccountLimitError 429", errExecute)
	}
	if wait, shouldRetry := manager.shouldRetryAfterError(errExecute, 0, []string{"codex"}, "gpt-limit", 30*time.Second); shouldRetry || wait != 0 {
		t.Fatalf("outer retry = (%v, %t), want (0, false)", wait, shouldRetry)
	}
	if hook.countFor("auth-a") != 1 {
		t.Fatalf("MarkResult count = %d, want 1 successful first call only", hook.countFor("auth-a"))
	}
}

func TestNonStreamAndPrepareFailureReleaseSlot(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	executor := &accountLimitExecutor{
		id:         "codex",
		prepareErr: &Error{HTTPStatus: http.StatusBadRequest, Message: "prepare failed"},
	}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1},
		"auth-b": {},
	})

	if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	if manager.limiter.inflightOf("auth-a") != 0 {
		t.Fatalf("auth-a inflight = %d, want 0 after prepare failure", manager.limiter.inflightOf("auth-a"))
	}
	if got := executor.ids("execute"); len(got) != 1 || got[0] != "auth-b" {
		t.Fatalf("execute ids = %v, want [auth-b]", got)
	}
}

func TestExecuteStreamHoldsAndReleasesLease(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
	executor := &accountLimitExecutor{id: "codex", holdChunks: chunks}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1},
	})

	result, errExecute := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("stream: %v", errExecute)
	}
	if manager.limiter.inflightOf("auth-a") != 1 {
		t.Fatalf("in-flight during stream = %d, want 1", manager.limiter.inflightOf("auth-a"))
	}
	close(chunks)
	drainStream(t, result)
	deadline := time.Now().Add(2 * time.Second)
	for manager.limiter.inflightOf("auth-a") != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("lease was not released after stream close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecuteStreamCancelReleasesLease(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
	executor := &accountLimitExecutor{id: "codex", holdChunks: chunks}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1},
	})

	ctx, cancel := context.WithCancel(context.Background())
	result, errExecute := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{Stream: true})
	if errExecute != nil {
		t.Fatalf("stream: %v", errExecute)
	}
	cancel()
	close(chunks)
	drainStream(t, result)
	deadline := time.Now().Add(2 * time.Second)
	for manager.limiter.inflightOf("auth-a") != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("lease was not released after cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecuteCountDoesNotAcquireAccountLimit(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	executor := &accountLimitExecutor{id: "codex"}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1, "max_rpm": 1},
	})
	if _, errExecute := manager.ExecuteCount(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("count: %v", errExecute)
	}
	if manager.limiter.entryCount() != 0 {
		t.Fatalf("count tokens created limiter entries: %d", manager.limiter.entryCount())
	}
}

func TestUnauthorizedRefreshDoesNotDoubleCountRPM(t *testing.T) {
	manager, executor, _, _, model := newUnauthorizedRefreshFixture(t, false)
	auth, ok := manager.GetByID("aa-primary")
	if !ok {
		t.Fatal("primary missing")
	}
	auth.Metadata["max_rpm"] = 2
	auth.Metadata["disable_cooling"] = true
	if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("update primary: %v", errUpdate)
	}
	backup, _ := manager.GetByID("bb-backup")
	backup.Disabled = true
	if _, errUpdate := manager.Update(context.Background(), backup); errUpdate != nil {
		t.Fatalf("disable backup: %v", errUpdate)
	}

	if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	if manager.limiter.inflightOf("aa-primary") != 0 {
		t.Fatalf("inflight after refresh execute = %d", manager.limiter.inflightOf("aa-primary"))
	}
	if got := len(executor.ExecuteCalls()); got != 2 {
		t.Fatalf("execute calls = %d, want 2 (401 + retry)", got)
	}
	lease, _, okAcquire := manager.limiter.TryAcquire(context.Background(), &Auth{ID: "aa-primary", Metadata: map[string]any{"max_rpm": 2}})
	if !okAcquire {
		t.Fatal("second RPM slot should still be free after one attempt")
	}
	lease.Release()
}

func TestHomeModeDoesNotUseLocalAccountLimiter(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	cfg := &internalconfig.Config{}
	cfg.Home.Enabled = true
	manager.runtimeConfig.Store(cfg)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1},
	})
	if manager.accountLimitsEnabled() {
		t.Fatal("account limits should be disabled in Home mode")
	}
	if manager.limiter.entryCount() != 0 {
		t.Fatalf("home setup created limiter entries: %d", manager.limiter.entryCount())
	}
}

func TestManagerRemoveForgetsLimiter(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 1},
	})
	lease, _, ok := manager.limiter.TryAcquire(context.Background(), &Auth{ID: "auth-a", Metadata: map[string]any{"max_concurrent": 1}})
	if !ok {
		t.Fatal("acquire failed")
	}
	manager.Remove(context.Background(), "auth-a")
	if manager.limiter.entryCount() != 0 {
		t.Fatalf("Remove left limiter entries: %d", manager.limiter.entryCount())
	}
	lease.Release()
}

func TestAccountLimitHotReloadUsesNewCap(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	var inFlight atomic.Int32
	started := make(chan struct{})
	unblock := make(chan struct{})
	executor := &accountLimitExecutor{id: "codex", blockStart: started, blockWait: unblock}
	manager.RegisterExecutor(executor)
	registerAccountLimitAuths(t, manager, "codex", "gpt-limit", map[string]map[string]any{
		"auth-a": {"max_concurrent": 2},
	})

	go func() {
		inFlight.Add(1)
		_, _ = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{})
		inFlight.Add(-1)
	}()
	<-started

	auth, _ := manager.GetByID("auth-a")
	auth.Metadata["max_concurrent"] = 1
	if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}
	if manager.limiter.inflightOf("auth-a") != 1 {
		t.Fatalf("in-progress request should keep its lease, inflight=%d", manager.limiter.inflightOf("auth-a"))
	}

	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-limit"}, cliproxyexecutor.Options{})
	var limitErr *AccountLimitError
	if !errors.As(errExecute, &limitErr) {
		t.Fatalf("new cap should reject extra acquire, err=%v", errExecute)
	}
	close(unblock)
	deadline := time.Now().Add(2 * time.Second)
	for inFlight.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("in-progress request did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
