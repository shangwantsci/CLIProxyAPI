package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type streamingTestResponseWriter struct {
	mu     sync.Mutex
	header http.Header
	status int
	chunks chan string
}

func newStreamingTestResponseWriter() *streamingTestResponseWriter {
	return &streamingTestResponseWriter{
		header: make(http.Header),
		chunks: make(chan string, 16),
	}
}

func (w *streamingTestResponseWriter) Header() http.Header {
	return w.header
}

func (w *streamingTestResponseWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.mu.Unlock()
	select {
	case w.chunks <- string(payload):
	default:
	}
	return len(payload), nil
}

func (w *streamingTestResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status = status
}

func (w *streamingTestResponseWriter) Flush() {}

type delayedFirstChunkExecutor struct {
	release chan struct{}
	once    sync.Once
}

func newDelayedFirstChunkExecutor() *delayedFirstChunkExecutor {
	return &delayedFirstChunkExecutor{release: make(chan struct{})}
}

func (e *delayedFirstChunkExecutor) Identifier() string { return "openai" }

func (e *delayedFirstChunkExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "Execute not implemented"}
}

func (e *delayedFirstChunkExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk)
	go func() {
		defer close(ch)
		select {
		case <-ctx.Done():
			return
		case <-e.release:
		}
		select {
		case <-ctx.Done():
		case ch <- coreexecutor.StreamChunk{Payload: []byte(`{"id":"chatcmpl-test","choices":[]}`)}:
		}
	}()
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *delayedFirstChunkExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *delayedFirstChunkExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, &coreauth.Error{Code: "not_implemented", Message: "CountTokens not implemented"}
}

func (e *delayedFirstChunkExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, &coreauth.Error{Code: "not_implemented", Message: "HttpRequest not implemented"}
}

func (e *delayedFirstChunkExecutor) Release() {
	e.once.Do(func() { close(e.release) })
}

func TestChatCompletionsStreamingEmitsKeepAliveBeforeFirstPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := newDelayedFirstChunkExecutor()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "openai-auth", Provider: "openai", Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("manager.Register: %v", err)
	}

	model := "test-openai-keepalive-model"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
		executor.Release()
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1},
	}, manager)
	handler := NewOpenAIAPIHandler(base)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hello"}]}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	writer := newStreamingTestResponseWriter()
	c, _ := gin.CreateTestContext(writer)
	c.Request = req

	done := make(chan struct{})
	go func() {
		handler.ChatCompletions(c)
		close(done)
	}()

	select {
	case chunk := <-writer.chunks:
		if strings.TrimSpace(chunk) != ": keep-alive" {
			t.Fatalf("first streamed chunk = %q, want keepalive comment", chunk)
		}
	case <-done:
		t.Fatalf("handler returned before first payload without emitting keepalive")
	case <-time.After(1500 * time.Millisecond):
		cancel()
		executor.Release()
		t.Fatalf("handler did not emit keepalive before first payload")
	}
}
