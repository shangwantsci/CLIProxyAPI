package helps

import (
	"net/http"
	"testing"

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
