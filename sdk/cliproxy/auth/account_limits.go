package auth

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Account-limit enforcement is an execution gate, not scheduler state.
// isAuthBlockedForModel results are cached on scheduler entries and only
// reevaluated on upsertAuth / cooldown expiry, so concurrency/RPM must not
// be expressed through that path.

const (
	accountLimitKindConcurrency = "concurrency"
	accountLimitKindRPM         = "rpm"

	defaultAccountLimitWait      = 5 * time.Second
	concurrencyRetryPollInterval = 200 * time.Millisecond
	// 50 * 200ms is the intentional 10s short-wait ceiling when the pool is full.
	accountLimitWaitRoundCap       = 50
	rpmWindow                      = time.Minute
	accountLimitConfiguredMaxValue = 10000
)

type limitLeaseContextKey struct{}

type accountLimiter struct {
	mu      sync.Mutex
	entries map[string]*limiterEntry
	now     func() time.Time
}

type limiterEntry struct {
	inflight int
	stamps   []time.Time
}

// limitLease is the acquire token for one upstream attempt. Release is
// idempotent and nil-safe so continue/return/stream-close paths can all call it.
type limitLease struct {
	limiter *accountLimiter
	authID  string
	once    sync.Once
}

// AccountLimitError is returned when every eligible credential is
// concurrency- or RPM-limited after failover and the brief wait budget.
type AccountLimitError struct {
	Limit      string
	retryAfter time.Duration
}

func newAccountLimiter() *accountLimiter {
	return &accountLimiter{
		entries: make(map[string]*limiterEntry),
		now:     time.Now,
	}
}

func (l *accountLimiter) TryAcquire(_ context.Context, auth *Auth) (*limitLease, time.Time, bool) {
	if l == nil || auth == nil {
		return nil, time.Time{}, true
	}
	maxConcurrent, hasConcurrent := auth.MaxConcurrentOverride()
	maxRPM, hasRPM := auth.MaxRPMOverride()
	if !hasConcurrent && !hasRPM {
		return nil, time.Time{}, true
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return nil, time.Time{}, true
	}
	now := l.currentTime()
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := l.entries[authID]
	if hasConcurrent && entry != nil && entry.inflight >= maxConcurrent {
		return nil, time.Time{}, false
	}
	if hasRPM {
		if entry != nil {
			entry.pruneStamps(now)
		}
		if entry != nil && len(entry.stamps) >= maxRPM {
			return nil, entry.stamps[0].Add(rpmWindow), false
		}
	}
	if entry == nil {
		entry = &limiterEntry{}
		l.entries[authID] = entry
	}
	entry.inflight++
	if hasRPM {
		entry.stamps = append(entry.stamps, now)
	}
	return &limitLease{limiter: l, authID: authID}, time.Time{}, true
}

func (l *accountLimiter) Forget(authID string) {
	if l == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	l.mu.Lock()
	delete(l.entries, authID)
	l.mu.Unlock()
}

func (l *accountLimiter) release(authID string) {
	if l == nil || authID == "" {
		return
	}
	now := l.currentTime()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[authID]
	if entry == nil {
		return
	}
	if entry.inflight > 0 {
		entry.inflight--
	}
	entry.pruneStamps(now)
	if entry.inflight == 0 && len(entry.stamps) == 0 {
		delete(l.entries, authID)
	}
}

func (l *accountLimiter) currentTime() time.Time {
	if l != nil && l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *accountLimiter) inflightOf(authID string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[authID]
	if entry == nil {
		return 0
	}
	return entry.inflight
}

func (l *accountLimiter) entryCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (entry *limiterEntry) pruneStamps(now time.Time) {
	if entry == nil || len(entry.stamps) == 0 {
		return
	}
	cutoff := now.Add(-rpmWindow)
	idx := 0
	for idx < len(entry.stamps) && !entry.stamps[idx].After(cutoff) {
		idx++
	}
	if idx == 0 {
		return
	}
	entry.stamps = append([]time.Time(nil), entry.stamps[idx:]...)
}

func (lease *limitLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.limiter == nil {
			return
		}
		lease.limiter.release(lease.authID)
	})
}

func withLease(ctx context.Context, lease *limitLease) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if lease == nil {
		return ctx
	}
	return context.WithValue(ctx, limitLeaseContextKey{}, lease)
}

func leaseFromContext(ctx context.Context) *limitLease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(limitLeaseContextKey{}).(*limitLease)
	return lease
}

func newAccountLimitError(kind string, retryAfter time.Duration) *AccountLimitError {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = accountLimitKindConcurrency
	}
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &AccountLimitError{Limit: kind, retryAfter: retryAfter}
}

func newAccountLimitErrorFromMap(limited map[string]time.Time, now time.Time) *AccountLimitError {
	kind := accountLimitKindConcurrency
	var earliest time.Time
	for _, retryAt := range limited {
		if retryAt.IsZero() {
			continue
		}
		if earliest.IsZero() || retryAt.Before(earliest) {
			earliest = retryAt
		}
	}
	retryAfter := time.Second
	if !earliest.IsZero() {
		kind = accountLimitKindRPM
		retryAfter = earliest.Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
	}
	return newAccountLimitError(kind, retryAfter)
}

func (e *AccountLimitError) Error() string {
	if e == nil {
		return ""
	}
	retrySeconds := e.retryAfterSeconds()
	message := "All eligible credentials are at their configured request limits"
	payload := map[string]any{
		"error": map[string]any{
			"code":                "account_rate_limited",
			"message":             message,
			"limit":               e.Limit,
			"retry_after_seconds": retrySeconds,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return `{"error":{"code":"account_rate_limited","message":"` + message + `"}}`
	}
	return string(data)
}

func (e *AccountLimitError) StatusCode() int {
	return http.StatusTooManyRequests
}

func (e *AccountLimitError) Headers() http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Retry-After", strconv.Itoa(e.retryAfterSeconds()))
	return headers
}

func (e *AccountLimitError) RetryAfter() *time.Duration {
	if e == nil || e.retryAfter <= 0 {
		return nil
	}
	value := e.retryAfter
	return &value
}

func (e *AccountLimitError) retryAfterSeconds() int {
	if e == nil {
		return 0
	}
	seconds := int(math.Ceil(e.retryAfter.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

func planAccountLimitWait(now time.Time, limited map[string]time.Time, budget *time.Duration, rounds *int) (time.Duration, bool) {
	if len(limited) == 0 || budget == nil || *budget <= 0 {
		return 0, false
	}
	if rounds != nil && *rounds >= accountLimitWaitRoundCap {
		return 0, false
	}
	wait := concurrencyRetryPollInterval
	hasConcurrency := false
	var earliest time.Time
	for _, retryAt := range limited {
		if retryAt.IsZero() {
			hasConcurrency = true
			continue
		}
		if earliest.IsZero() || retryAt.Before(earliest) {
			earliest = retryAt
		}
	}
	if !hasConcurrency && !earliest.IsZero() {
		until := earliest.Sub(now)
		if until > *budget {
			return 0, false
		}
		wait = until
		if wait > concurrencyRetryPollInterval {
			wait = concurrencyRetryPollInterval
		}
	} else if !earliest.IsZero() {
		until := earliest.Sub(now)
		if until > 0 && until < wait {
			wait = until
		}
	}
	if wait > *budget {
		wait = *budget
	}
	if wait < 0 {
		wait = 0
	}
	*budget -= wait
	if rounds != nil {
		*rounds++
	}
	return wait, true
}

func consumeAccountLimitWait(ctx context.Context, tried map[string]struct{}, limited map[string]time.Time, budget *time.Duration, rounds *int) (bool, error) {
	wait, ok := planAccountLimitWait(time.Now(), limited, budget, rounds)
	if !ok {
		return false, nil
	}
	if err := waitForCooldown(ctx, wait, 0); err != nil {
		return false, err
	}
	for id := range limited {
		delete(tried, id)
	}
	clear(limited)
	return true, nil
}

func (m *Manager) accountLimitsEnabled() bool {
	return m != nil && m.limiter != nil && !m.HomeEnabled()
}

func (m *Manager) tryAccountLimitAcquire(ctx context.Context, auth *Auth, limitedRetryAt map[string]time.Time) (*limitLease, bool) {
	if !m.accountLimitsEnabled() {
		return nil, true
	}
	lease, retryAt, ok := m.limiter.TryAcquire(ctx, auth)
	if ok {
		return lease, true
	}
	if limitedRetryAt != nil && auth != nil {
		limitedRetryAt[auth.ID] = retryAt
	}
	return nil, false
}
