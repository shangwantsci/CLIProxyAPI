package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestNewProxyAwareHTTPClientDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
		0,
	)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestNewProxyAwareHTTPClientInvalidProxyFailsClosed(t *testing.T) {
	t.Parallel()

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewProxyAwareHTTPClient(
		context.Background(),
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "ftp://proxy.example.com:21"}},
		nil,
		0,
	)
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("client.Do returned nil error for invalid proxy-url")
	}
	if !strings.Contains(err.Error(), "invalid proxy-url") {
		t.Fatalf("error = %q, want invalid proxy-url", err)
	}
	if hits != 0 {
		t.Fatalf("invalid proxy-url should fail closed before upstream request, got %d hits", hits)
	}
}

func TestNewUtlsHTTPClientInvalidProxyFailsClosed(t *testing.T) {
	t.Parallel()

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewUtlsHTTPClient(
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "ftp://proxy.example.com:21"}},
		nil,
		0,
	)
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest returned error: %v", err)
	}
	_, err = client.Do(req)
	if err == nil {
		t.Fatal("client.Do returned nil error for invalid proxy-url")
	}
	if !strings.Contains(err.Error(), "invalid proxy-url") {
		t.Fatalf("error = %q, want invalid proxy-url", err)
	}
	if hits != 0 {
		t.Fatalf("invalid proxy-url should fail closed before upstream request, got %d hits", hits)
	}
}
