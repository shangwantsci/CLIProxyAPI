package config

import "testing"

func TestParseConfigBytesClaudeProxyDefaults(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("port: 8317\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	if cfg.RequestRetry != DefaultRequestRetry {
		t.Fatalf("RequestRetry = %d, want %d", cfg.RequestRetry, DefaultRequestRetry)
	}
	if cfg.MaxRetryCredentials != DefaultMaxRetryCredentials {
		t.Fatalf("MaxRetryCredentials = %d, want %d", cfg.MaxRetryCredentials, DefaultMaxRetryCredentials)
	}
	if cfg.MaxRetryInterval != DefaultMaxRetryInterval {
		t.Fatalf("MaxRetryInterval = %d, want %d", cfg.MaxRetryInterval, DefaultMaxRetryInterval)
	}
	if !cfg.Routing.SessionAffinity {
		t.Fatal("Routing.SessionAffinity = false, want true")
	}
	if cfg.Routing.SessionAffinityTTL != DefaultSessionAffinityTTL {
		t.Fatalf("Routing.SessionAffinityTTL = %q, want %q", cfg.Routing.SessionAffinityTTL, DefaultSessionAffinityTTL)
	}
}
