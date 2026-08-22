package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountLimiterUnconfiguredIsNoop(t *testing.T) {
	t.Parallel()
	limiter := newAccountLimiter()
	lease, retryAt, ok := limiter.TryAcquire(context.Background(), &Auth{ID: "a", Metadata: map[string]any{}})
	if !ok || lease != nil || !retryAt.IsZero() {
		t.Fatalf("unconfigured acquire = (%v, %v, %t), want (nil, zero, true)", lease, retryAt, ok)
	}
	if limiter.entryCount() != 0 {
		t.Fatalf("entryCount = %d, want 0", limiter.entryCount())
	}
}

func TestAccountLimiterConcurrencyCap(t *testing.T) {
	t.Parallel()
	limiter := newAccountLimiter()
	auth := &Auth{ID: "a", Metadata: map[string]any{"max_concurrent": 2}}

	first, _, ok := limiter.TryAcquire(context.Background(), auth)
	if !ok || first == nil {
		t.Fatal("first acquire failed")
	}
	second, _, ok := limiter.TryAcquire(context.Background(), auth)
	if !ok || second == nil {
		t.Fatal("second acquire failed")
	}
	if _, retryAt, ok := limiter.TryAcquire(context.Background(), auth); ok || !retryAt.IsZero() {
		t.Fatalf("third acquire should fail with zero retryAt, ok=%t retryAt=%v", ok, retryAt)
	}
	if limiter.inflightOf("a") != 2 {
		t.Fatalf("inflight = %d, want 2", limiter.inflightOf("a"))
	}

	first.Release()
	third, _, ok := limiter.TryAcquire(context.Background(), auth)
	if !ok || third == nil {
		t.Fatal("acquire after release failed")
	}
	first.Release()
	if limiter.inflightOf("a") != 2 {
		t.Fatalf("double release changed inflight to %d, want 2", limiter.inflightOf("a"))
	}

	var none *limitLease
	none.Release()
}

func TestAccountLimiterRPMWindow(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	limiter := newAccountLimiter()
	limiter.now = func() time.Time { return now }
	auth := &Auth{ID: "a", Metadata: map[string]any{"max_rpm": 3}}

	for i := 0; i < 3; i++ {
		lease, _, ok := limiter.TryAcquire(context.Background(), auth)
		if !ok || lease == nil {
			t.Fatalf("acquire %d failed", i+1)
		}
	}
	_, retryAt, ok := limiter.TryAcquire(context.Background(), auth)
	if ok {
		t.Fatal("fourth acquire should fail")
	}
	if !retryAt.Equal(now.Add(rpmWindow)) {
		t.Fatalf("retryAt = %v, want %v", retryAt, now.Add(rpmWindow))
	}

	now = now.Add(rpmWindow + time.Millisecond)
	lease, _, ok := limiter.TryAcquire(context.Background(), auth)
	if !ok || lease == nil {
		t.Fatal("acquire after window slide failed")
	}
}

func TestAccountLimiterForgetAndIdleGC(t *testing.T) {
	t.Parallel()
	limiter := newAccountLimiter()
	auth := &Auth{ID: "a", Metadata: map[string]any{"max_concurrent": 1}}
	lease, _, ok := limiter.TryAcquire(context.Background(), auth)
	if !ok {
		t.Fatal("acquire failed")
	}
	if limiter.entryCount() != 1 {
		t.Fatalf("entryCount = %d, want 1", limiter.entryCount())
	}
	lease.Release()
	if limiter.entryCount() != 0 {
		t.Fatalf("idle entry was not collected, count=%d", limiter.entryCount())
	}

	lease, _, ok = limiter.TryAcquire(context.Background(), auth)
	if !ok {
		t.Fatal("second acquire failed")
	}
	limiter.Forget("a")
	if limiter.entryCount() != 0 {
		t.Fatalf("Forget left %d entries", limiter.entryCount())
	}
	lease.Release()
}

func TestAccountLimiterConcurrentAcquireRelease(t *testing.T) {
	t.Parallel()
	limiter := newAccountLimiter()
	auth := &Auth{ID: "a", Metadata: map[string]any{"max_concurrent": 8}}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, _, ok := limiter.TryAcquire(context.Background(), auth)
			if !ok {
				return
			}
			accepted.Add(1)
			if limiter.inflightOf("a") > 8 {
				t.Errorf("inflight exceeded cap: %d", limiter.inflightOf("a"))
			}
			lease.Release()
		}()
	}
	wg.Wait()
	if limiter.inflightOf("a") != 0 {
		t.Fatalf("final inflight = %d, want 0", limiter.inflightOf("a"))
	}
	if accepted.Load() == 0 {
		t.Fatal("no acquires succeeded")
	}
}

func TestAccountLimitErrorShape(t *testing.T) {
	t.Parallel()
	err := newAccountLimitError(accountLimitKindRPM, 1500*time.Millisecond)
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", err.StatusCode())
	}
	if err.Headers().Get("Retry-After") != "2" {
		t.Fatalf("Retry-After = %q, want 2", err.Headers().Get("Retry-After"))
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(err.Error()), &payload); jsonErr != nil {
		t.Fatalf("error body is not JSON: %v", jsonErr)
	}
	body, _ := payload["error"].(map[string]any)
	if body["code"] != "account_rate_limited" || body["limit"] != accountLimitKindRPM {
		t.Fatalf("error body = %#v", body)
	}
}

func TestPlanAccountLimitWait(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	budget := 5 * time.Second
	rounds := 0
	wait, ok := planAccountLimitWait(now, map[string]time.Time{"a": {}}, &budget, &rounds)
	if !ok || wait != concurrencyRetryPollInterval {
		t.Fatalf("concurrency wait = (%v, %t), want (%v, true)", wait, ok, concurrencyRetryPollInterval)
	}
	if budget != 5*time.Second-concurrencyRetryPollInterval || rounds != 1 {
		t.Fatalf("budget=%v rounds=%d", budget, rounds)
	}

	budget = 5 * time.Second
	rounds = 0
	retryAt := now.Add(100 * time.Millisecond)
	wait, ok = planAccountLimitWait(now, map[string]time.Time{"a": retryAt}, &budget, &rounds)
	if !ok || wait != 100*time.Millisecond {
		t.Fatalf("soon rpm wait = (%v, %t), want 100ms", wait, ok)
	}

	budget = 5 * time.Second
	rounds = 0
	if _, ok = planAccountLimitWait(now, map[string]time.Time{"a": now.Add(time.Minute)}, &budget, &rounds); ok {
		t.Fatal("rpm retry beyond budget should not wait")
	}

	budget = 0
	if _, ok = planAccountLimitWait(now, map[string]time.Time{"a": {}}, &budget, &rounds); ok {
		t.Fatal("zero budget should not wait")
	}
}
