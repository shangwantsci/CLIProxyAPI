package executor

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestNewClaudeExecutor_ConcurrencySemDisabledByDefault(t *testing.T) {
	e := NewClaudeExecutor(&config.Config{})
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("tryAcquire should succeed when limit is disabled")
	}
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("second tryAcquire should also succeed when limit is disabled")
	}
	e.releaseConcurrencySlot() // 不应 panic
	e.releaseConcurrencySlot()
}

func TestClaudeExecutor_ConcurrencySlot_AcquireRelease(t *testing.T) {
	e := NewClaudeExecutor(&config.Config{ClaudeMaxConcurrentRequests: 2})
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("1st acquire should succeed")
	}
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("2nd acquire should succeed")
	}
	if e.tryAcquireConcurrencySlot() {
		t.Fatalf("3rd acquire should fail (capacity=2)")
	}
	e.releaseConcurrencySlot()
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("acquire after release should succeed")
	}
}

func TestClaudeExecutor_ReleaseExtraIsSafe(t *testing.T) {
	e := NewClaudeExecutor(&config.Config{ClaudeMaxConcurrentRequests: 1})
	e.releaseConcurrencySlot() // 未 acquire 就 release,不应阻塞/panic
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("acquire should still succeed after spurious release")
	}
}

func TestClaudeConcurrencyLimitError_IsNonRetryable429(t *testing.T) {
	err := claudeConcurrencyLimitError()
	var ce *cliproxyauth.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected *cliproxyauth.Error, got %T", err)
	}
	if ce.Retryable {
		t.Fatalf("concurrency limit error must be non-retryable (must not trigger account retry)")
	}
	if ce.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("expected HTTP 429, got %d", ce.HTTPStatus)
	}
}

func TestStreamingSlotHeldUntilGoroutineExit(t *testing.T) {
	e := NewClaudeExecutor(&config.Config{ClaudeMaxConcurrentRequests: 1})

	// 模拟 ExecuteStream:Acquire 后把释放责任交给后台 goroutine 的 defer。
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("acquire should succeed")
	}
	if e.tryAcquireConcurrencySlot() {
		t.Fatalf("slot must be held while stream in progress")
	}

	streamDone := make(chan struct{})
	go func() {
		defer e.releaseConcurrencySlot() // 对应 ExecuteStream goroutine 的 release defer
		<-streamDone                     // 模拟流读取中
	}()

	if e.tryAcquireConcurrencySlot() {
		t.Fatalf("slot must remain held until stream goroutine exits")
	}
	close(streamDone)

	released := false
	for i := 0; i < 100; i++ {
		if e.tryAcquireConcurrencySlot() {
			released = true
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !released {
		t.Fatalf("slot must be released after stream goroutine exits")
	}
}

func TestClaudeExecutor_ConcurrencyLimitHotReloadsConfig(t *testing.T) {
	cfg := &config.Config{ClaudeMaxConcurrentRequests: 1}
	e := NewClaudeExecutor(cfg)

	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("first acquire should succeed")
	}
	if e.tryAcquireConcurrencySlot() {
		t.Fatalf("second acquire should fail while cap is 1")
	}

	cfg.ClaudeMaxConcurrentRequests = 2
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("second acquire should succeed after hot-reloading cap to 2")
	}

	cfg.ClaudeMaxConcurrentRequests = 0
	if !e.tryAcquireConcurrencySlot() {
		t.Fatalf("acquire should bypass limit after hot-reloading cap to 0")
	}

	e.releaseConcurrencySlot()
	e.releaseConcurrencySlot()
	e.releaseConcurrencySlot()
}
