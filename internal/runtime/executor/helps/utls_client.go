package helps

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// h2Connection is the minimal surface of *http2.ClientConn the connection pool
// needs. It exists so tests can inject fakes that drive State()/Close() without
// real TCP/TLS. Production always uses *http2.ClientConn, which satisfies it.
type h2Connection interface {
	CanTakeNewRequest() bool
	State() http2.ClientConnState
	Close() error
	RoundTrip(*http.Request) (*http.Response, error)
}

// utlsRoundTripper implements http.RoundTripper using utls with Chrome fingerprint
// to bypass Cloudflare's TLS fingerprinting on Anthropic domains.
type utlsRoundTripper struct {
	mu          sync.Mutex
	connections map[string]h2Connection
	pending     map[string]*sync.Cond
	dialer      proxy.Dialer
}

func newUtlsRoundTripper(proxyURL string) (*utlsRoundTripper, error) {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			log.Errorf("utls: failed to configure proxy dialer for %q: %v", proxyutil.Redact(proxyURL), errBuild)
			return nil, errBuild
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{
		connections: make(map[string]h2Connection),
		pending:     make(map[string]*sync.Cond),
		dialer:      dialer,
	}, nil
}

func (t *utlsRoundTripper) getOrCreateConnection(host, addr string) (h2Connection, error) {
	t.mu.Lock()

	if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
		t.mu.Unlock()
		return h2Conn, nil
	}

	if cond, ok := t.pending[host]; ok {
		cond.Wait()
		if h2Conn, ok := t.connections[host]; ok && h2Conn.CanTakeNewRequest() {
			t.mu.Unlock()
			return h2Conn, nil
		}
	}

	cond := sync.NewCond(&t.mu)
	t.pending[host] = cond
	t.mu.Unlock()

	h2Conn, err := t.createConnection(host, addr)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pending, host)
	cond.Broadcast()

	if err != nil {
		return nil, err
	}

	t.connections[host] = h2Conn
	return h2Conn, nil
}

func (t *utlsRoundTripper) createConnection(host, addr string) (h2Connection, error) {
	conn, err := t.dialer.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, err
	}

	tr := &http2.Transport{}
	h2Conn, err := tr.NewClientConn(tlsConn)
	if err != nil {
		tlsConn.Close()
		return nil, err
	}

	return h2Conn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.getOrCreateConnection(hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		t.mu.Lock()
		if cached, ok := t.connections[hostname]; ok && cached == h2Conn {
			delete(t.connections, hostname)
		}
		t.mu.Unlock()
		return nil, err
	}

	return resp, nil
}

// Close closes every cached connection and empties the map. Safe to call
// concurrently with RoundTrip; callers hold no other lock.
func (t *utlsRoundTripper) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for host, conn := range t.connections {
		_ = conn.Close()
		delete(t.connections, host)
	}
	return nil
}

// cleanupIdle closes connections that have no active or reserved streams and
// have been idle longer than threshold, then removes them from the map. It
// returns the number of connections closed. Connections still carrying streams
// (including in-flight SSE streams) are never touched.
func (t *utlsRoundTripper) cleanupIdle(threshold time.Duration) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	closed := 0
	for host, conn := range t.connections {
		st := conn.State()
		if st.StreamsActive == 0 && st.StreamsReserved == 0 && !st.LastIdle.IsZero() && time.Since(st.LastIdle) > threshold {
			_ = conn.Close()
			delete(t.connections, host)
			closed++
		}
	}
	return closed
}

// connectionCount reports how many connections are cached. Used by the pool to
// drop empty roundtrippers.
func (t *utlsRoundTripper) connectionCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.connections)
}

// anthropicHosts contains the hosts that should use utls Chrome TLS fingerprint.
var anthropicHosts = map[string]struct{}{
	"api.anthropic.com": {},
}

// fallbackRoundTripper uses utls for Anthropic HTTPS hosts and falls back to
// standard transport for all other requests (non-HTTPS or non-Anthropic hosts).
type fallbackRoundTripper struct {
	utls     *utlsRoundTripper
	fallback http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "https" {
		if _, ok := anthropicHosts[strings.ToLower(req.URL.Hostname())]; ok {
			return f.utls.RoundTrip(req)
		}
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using utls Chrome TLS fingerprint.
// Use this for Claude API requests to match real Claude Code's TLS behavior.
// Falls back to standard transport for non-HTTPS requests.
func NewUtlsHTTPClient(cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	utlsRT, errBuildDialer := newUtlsRoundTripper(proxyURL)
	if errBuildDialer != nil {
		client := &http.Client{Transport: failingRoundTripper{err: proxyConfigurationError(proxyURL, errBuildDialer)}}
		if timeout > 0 {
			client.Timeout = timeout
		}
		return client
	}

	var standardTransport http.RoundTripper = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
	if proxyURL != "" {
		transport, errBuildTransport := buildProxyTransport(proxyURL)
		if errBuildTransport != nil {
			client := &http.Client{Transport: failingRoundTripper{err: proxyConfigurationError(proxyURL, errBuildTransport)}}
			if timeout > 0 {
				client.Timeout = timeout
			}
			return client
		}
		if transport != nil {
			standardTransport = transport
		}
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			utls:     utlsRT,
			fallback: standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
