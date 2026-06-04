package helps

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/net/http2"
)

// fakeH2Conn is a test double for h2Connection so tests can drive State() and
// observe Close() without real TCP/TLS.
type fakeH2Conn struct {
	state    http2.ClientConnState
	canTake  bool
	closed   bool
	closeErr error
}

func (f *fakeH2Conn) CanTakeNewRequest() bool                         { return f.canTake }
func (f *fakeH2Conn) State() http2.ClientConnState                    { return f.state }
func (f *fakeH2Conn) Close() error                                    { f.closed = true; return f.closeErr }
func (f *fakeH2Conn) RoundTrip(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: 200}, nil }

// TestH2ConnectionInterfaceSatisfiedByFake is a compile-time + runtime guard that
// the h2Connection interface exists and our fake implements it.
func TestH2ConnectionInterfaceSatisfiedByFake(t *testing.T) {
	var c h2Connection = &fakeH2Conn{canTake: true}
	if !c.CanTakeNewRequest() {
		t.Fatal("fake should report CanTakeNewRequest=true")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestCleanupIdle_ClosesIdleKeepsActive(t *testing.T) {
	now := time.Now()
	idle := &fakeH2Conn{state: http2.ClientConnState{StreamsActive: 0, StreamsReserved: 0, LastIdle: now.Add(-200 * time.Second)}}
	activeStreams := &fakeH2Conn{state: http2.ClientConnState{StreamsActive: 1, LastIdle: now.Add(-200 * time.Second)}}
	reserved := &fakeH2Conn{state: http2.ClientConnState{StreamsActive: 0, StreamsReserved: 1, LastIdle: now.Add(-200 * time.Second)}}
	freshIdle := &fakeH2Conn{state: http2.ClientConnState{StreamsActive: 0, StreamsReserved: 0, LastIdle: now.Add(-5 * time.Second)}}

	rt := &utlsRoundTripper{
		connections: map[string]h2Connection{
			"host-idle":     idle,
			"host-active":   activeStreams,
			"host-reserved": reserved,
			"host-fresh":    freshIdle,
		},
		pending: map[string]*sync.Cond{},
	}

	closed := rt.cleanupIdle(90 * time.Second)

	if closed != 1 {
		t.Fatalf("cleanupIdle closed = %d, want 1 (only host-idle)", closed)
	}
	if !idle.closed {
		t.Error("idle connection should be closed")
	}
	if activeStreams.closed {
		t.Error("connection with active streams must NOT be closed")
	}
	if reserved.closed {
		t.Error("connection with reserved streams must NOT be closed")
	}
	if freshIdle.closed {
		t.Error("recently-idle connection must NOT be closed")
	}
	if _, ok := rt.connections["host-idle"]; ok {
		t.Error("closed idle connection must be removed from map")
	}
	if _, ok := rt.connections["host-active"]; !ok {
		t.Error("active connection must remain in map")
	}
}

func TestRoundTripperClose_ClosesAll(t *testing.T) {
	a := &fakeH2Conn{}
	b := &fakeH2Conn{}
	rt := &utlsRoundTripper{
		connections: map[string]h2Connection{"a": a, "b": b},
		pending:     map[string]*sync.Cond{},
	}
	if err := rt.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if !a.closed || !b.closed {
		t.Error("Close must close all connections")
	}
	if len(rt.connections) != 0 {
		t.Errorf("Close must empty the connections map, got %d", len(rt.connections))
	}
}

func TestPoolGetRoundTripper_ReuseAndIsolation(t *testing.T) {
	p := newUtlsClientPool()

	rtA1, err := p.getRoundTripper("", "auth-A")
	if err != nil {
		t.Fatalf("getRoundTripper A1: %v", err)
	}
	rtA2, err := p.getRoundTripper("", "auth-A")
	if err != nil {
		t.Fatalf("getRoundTripper A2: %v", err)
	}
	if rtA1 != rtA2 {
		t.Error("same (proxy, authID) must return the same roundtripper pointer")
	}

	rtB, err := p.getRoundTripper("", "auth-B")
	if err != nil {
		t.Fatalf("getRoundTripper B: %v", err)
	}
	if rtA1 == rtB {
		t.Error("different authID must return different roundtrippers")
	}

	rtProxy, err := p.getRoundTripper("socks5://127.0.0.1:1080", "auth-A")
	if err != nil {
		t.Fatalf("getRoundTripper proxy: %v", err)
	}
	if rtProxy == rtA1 {
		t.Error("different proxyURL must return different roundtrippers")
	}
}

func TestPoolGetRoundTripper_InvalidProxyNotCached(t *testing.T) {
	p := newUtlsClientPool()
	_, err := p.getRoundTripper("ftp://bad.example.com:21", "auth-A")
	if err == nil {
		t.Fatal("invalid proxy must return an error")
	}
	if got := p.size(); got != 0 {
		t.Fatalf("invalid proxy must not be cached, pool size = %d", got)
	}
}

func TestPoolCleanupSweep_DropsEmptyRoundtrippers(t *testing.T) {
	p := newUtlsClientPool()
	rt, err := p.getRoundTripper("", "auth-A")
	if err != nil {
		t.Fatalf("getRoundTripper: %v", err)
	}
	// Inject one idle connection that cleanupIdle will close.
	rt.mu.Lock()
	rt.connections["host"] = &fakeH2Conn{state: http2.ClientConnState{LastIdle: time.Now().Add(-300 * time.Second)}}
	rt.mu.Unlock()

	p.sweep(90 * time.Second)

	if got := p.size(); got != 0 {
		t.Fatalf("roundtripper emptied by cleanup must be dropped from pool, size = %d", got)
	}
}

// resetSharedUtlsPoolForTest swaps in a fresh pool (without starting the
// background sweeper) so pool-reuse tests are independent of execution order.
// It re-arms the sync.Once so sharedUtlsPool() returns our injected pool.
// Test-only.
func resetSharedUtlsPoolForTest() {
	globalUtlsPool = newUtlsClientPool()
	globalUtlsPoolOnce = sync.Once{}
	globalUtlsPoolOnce.Do(func() {}) // fire Once as no-op so sharedUtlsPool keeps our pool
}

func TestNewUtlsHTTPClient_ReusesPooledRoundtripper(t *testing.T) {
	resetSharedUtlsPoolForTest()

	auth := &cliproxyauth.Auth{ID: "auth-reuse"}
	c1 := NewUtlsHTTPClient(&config.Config{}, auth, 0)
	c2 := NewUtlsHTTPClient(&config.Config{}, auth, 0)

	f1, ok1 := c1.Transport.(*fallbackRoundTripper)
	f2, ok2 := c2.Transport.(*fallbackRoundTripper)
	if !ok1 || !ok2 {
		t.Fatalf("transport types = %T, %T; want *fallbackRoundTripper", c1.Transport, c2.Transport)
	}
	if f1.utls != f2.utls {
		t.Error("two clients for the same (proxy, authID) must share one utls roundtripper")
	}
}

func TestNewUtlsHTTPClient_DifferentAuthDoesNotShare(t *testing.T) {
	resetSharedUtlsPoolForTest()
	cA := NewUtlsHTTPClient(&config.Config{}, &cliproxyauth.Auth{ID: "auth-A"}, 0)
	cB := NewUtlsHTTPClient(&config.Config{}, &cliproxyauth.Auth{ID: "auth-B"}, 0)
	fA := cA.Transport.(*fallbackRoundTripper)
	fB := cB.Transport.(*fallbackRoundTripper)
	if fA.utls == fB.utls {
		t.Error("different authID must not share a utls roundtripper")
	}
}
