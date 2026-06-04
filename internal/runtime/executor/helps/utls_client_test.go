package helps

import (
	"net/http"
	"sync"
	"testing"
	"time"

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
