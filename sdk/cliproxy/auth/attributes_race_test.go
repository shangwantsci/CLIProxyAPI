package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

// prepareWritingExecutor mimics provider executors (xai/kimi and custom-header
// injection) that mutate auth.Attributes while preparing an outbound request.
// InjectCredentials hands these executors the *Auth pointer stored in m.auths
// without holding m.mu, so any concurrent scheduler read of the same shared
// Attributes map is an unsynchronised access.
type prepareWritingExecutor struct{ schedulerTestExecutor }

func (prepareWritingExecutor) PrepareRequest(req *http.Request, auth *Auth) error {
	if auth == nil {
		return nil
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	// Same shape as kimi_executor.go:78 / xai_executor.go:461-463.
	auth.Attributes["base_url"] = "https://example.invalid"
	auth.Attributes["auth_kind"] = "oauth"
	return nil
}

// TestManagerUpdateAndInjectCredentialsShareAttributes reproduces the production
// `fatal error: concurrent map read and map write` on auth.Attributes.
//
// Reader path: Manager.Update -> scheduler.upsertAuth -> buildScheduledAuthMeta
// -> authPriority/authWebsocketsEnabled reading Attributes.
// Writer path: Manager.InjectCredentials -> PrepareRequest, writing the same
// m.auths[id] pointer's Attributes outside m.mu.
//
// The Go runtime's concurrent-map detector triggers this even without -race when
// the scheduler and the executor share one *Auth; the fix gives the scheduler an
// independent Clone so the two never touch the same map.
func TestManagerUpdateAndInjectCredentialsShareAttributes(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(prepareWritingExecutor{})

	const authID = "race-auth-1"
	base := &Auth{
		ID:       authID,
		Provider: "test",
		Status:   StatusActive,
		Attributes: map[string]string{
			"priority":   "1",
			"websockets": "true",
		},
	}
	if _, err := manager.Register(context.Background(), base); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	const iterations = 2000
	var wg sync.WaitGroup
	wg.Add(4)

	// Writer: request-time credential injection mutates the shared Attributes map.
	go func() {
		defer wg.Done()
		req, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
		for i := 0; i < iterations; i++ {
			_ = manager.InjectCredentials(req, authID)
		}
	}()

	// Reader: config hot-reload / import re-upserts the auth, and the scheduler
	// reads Attributes off the same pointer.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			snapshot := &Auth{
				ID:       authID,
				Provider: "test",
				Status:   StatusActive,
				Attributes: map[string]string{
					"priority":   "1",
					"websockets": "true",
				},
			}
			_, _ = manager.Update(context.Background(), snapshot)
		}
	}()

	// MarkResult mutates Quota/ModelStates under the lock and re-upserts a clone
	// into the scheduler: another real path that touches the shared pointer.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			manager.MarkResult(context.Background(), Result{
				AuthID:   authID,
				Provider: "test",
				Model:    "test-model",
				Success:  i%2 == 0,
			})
		}
	}()

	// List clones every auth under the lock; exercises concurrent map reads of
	// Attributes/ModelStates against the writers above.
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = manager.List()
		}
	}()

	wg.Wait()
}
