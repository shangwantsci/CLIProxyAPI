package handlers

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"golang.org/x/net/context"
)

func contentGuardEnabled(mode string) *sdkconfig.SDKConfig {
	enabled := true
	return &sdkconfig.SDKConfig{
		ContentGuard: &config.ContentGuardConfig{
			Enabled: &enabled,
			Mode:    mode,
		},
	}
}

func TestContentGuardBlockDoesNotAcquireAccountLimit(t *testing.T) {
	model := "content-guard-limit-model"
	var executeCalls atomic.Int32
	executor := &interceptorCaptureExecutor{
		execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
			executeCalls.Add(1)
			return coreexecutor.Response{Payload: []byte("ok")}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, contentGuardEnabled(config.ContentGuardModeEnforce))
	handler.AuthManager.SetRetryConfig(0, 0, 0)
	auth, ok := handler.AuthManager.GetByID("handler-interceptor-" + model)
	if !ok || auth == nil {
		t.Fatal("registered auth missing")
	}
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	auth.Metadata["max_concurrent"] = 1
	auth.Metadata["max_rpm"] = 1
	if _, err := handler.AuthManager.Update(context.Background(), auth); err != nil {
		t.Fatalf("update account limits: %v", err)
	}

	hitBody := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"how to kill myself"}]}`)
	_, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", model, hitBody, "")
	if errMsg == nil || !errMsg.DirectResponse || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("enforce error = %#v", errMsg)
	}
	if executeCalls.Load() != 0 {
		t.Fatalf("executor ran %d times after enforce hit", executeCalls.Load())
	}

	passBody := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello from a normal coding session"}]}`)
	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", model, passBody, "")
	if errMsg != nil {
		t.Fatalf("follow-up request failed: %#v", errMsg)
	}
	if string(body) != "ok" {
		t.Fatalf("follow-up body = %q", body)
	}
	if executeCalls.Load() != 1 {
		t.Fatalf("executor calls = %d, want 1", executeCalls.Load())
	}
}

func TestContentGuardObserveDoesNotBlockOnHit(t *testing.T) {
	model := "content-guard-observe-model"
	var executeCalls atomic.Int32
	executor := &interceptorCaptureExecutor{
		execute: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
			executeCalls.Add(1)
			return coreexecutor.Response{Payload: []byte("ok")}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, contentGuardEnabled(config.ContentGuardModeObserve))
	hitBody := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"how to kill myself"}]}`)
	body, _, errMsg := handler.ExecuteWithAuthManager(context.Background(), "openai", model, hitBody, "")
	if errMsg != nil {
		t.Fatalf("observe should not 403: %#v", errMsg)
	}
	if string(body) != "ok" {
		t.Fatalf("observe body = %q", body)
	}
	if executeCalls.Load() != 1 {
		t.Fatalf("observe still selects an account; calls = %d", executeCalls.Load())
	}
}

func TestContentGuardDisabledDoesNotCopyPayload(t *testing.T) {
	payload := []byte(`{"model":"disabled-guard-model","messages":[{"role":"user","content":"how to kill myself"}]}`)
	disabled := false
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		ContentGuard: &config.ContentGuardConfig{Enabled: &disabled, Mode: config.ContentGuardModeEnforce},
	}, nil)
	req := coreexecutor.Request{Model: "disabled-guard-model", Payload: payload}
	opts := coreexecutor.Options{OriginalRequest: payload}
	gotReq, gotOpts, errMsg := handler.applyRequestInterceptorsBeforeAuth(context.Background(), "openai", req.Model, "test-req", req, opts, "")
	if errMsg != nil {
		t.Fatalf("disabled guard error = %#v", errMsg)
	}
	if len(gotReq.Payload) != len(payload) || &gotReq.Payload[0] != &payload[0] {
		t.Fatal("request payload was copied")
	}
	if len(gotOpts.OriginalRequest) != len(payload) || &gotOpts.OriginalRequest[0] != &payload[0] {
		t.Fatal("original request was copied")
	}
}

func TestExecuteCountWithAuthManagerContentGuardStillSkipsAcquireOnHit(t *testing.T) {
	model := "content-guard-count-model"
	var countCalls atomic.Int32
	executor := &interceptorCaptureExecutor{
		executeCount: func(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
			countCalls.Add(1)
			return coreexecutor.Response{Payload: []byte("1")}, nil
		},
	}
	handler := newInterceptorHandler(t, model, executor, contentGuardEnabled(config.ContentGuardModeEnforce))
	hitBody := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"how to kill myself"}]}`)
	_, _, errMsg := handler.ExecuteCountWithAuthManager(context.Background(), "openai", model, hitBody, "")
	if errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("count enforce error = %#v", errMsg)
	}
	if countCalls.Load() != 0 {
		t.Fatalf("count executor ran after enforce hit: %d", countCalls.Load())
	}
}
