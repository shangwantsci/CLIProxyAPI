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
	if cfg.ClaudeQuotaCoolingThresholds.FiveHourRemainingPercent != DefaultClaudeFiveHourQuotaCoolingRemainingPercent {
		t.Fatalf("FiveHourRemainingPercent = %d, want %d", cfg.ClaudeQuotaCoolingThresholds.FiveHourRemainingPercent, DefaultClaudeFiveHourQuotaCoolingRemainingPercent)
	}
	if cfg.ClaudeQuotaCoolingThresholds.WeeklyRemainingPercent != DefaultClaudeWeeklyQuotaCoolingRemainingPercent {
		t.Fatalf("WeeklyRemainingPercent = %d, want %d", cfg.ClaudeQuotaCoolingThresholds.WeeklyRemainingPercent, DefaultClaudeWeeklyQuotaCoolingRemainingPercent)
	}
	if !cfg.Routing.SessionAffinity {
		t.Fatal("Routing.SessionAffinity = false, want true")
	}
	if cfg.Routing.SessionAffinityTTL != DefaultSessionAffinityTTL {
		t.Fatalf("Routing.SessionAffinityTTL = %q, want %q", cfg.Routing.SessionAffinityTTL, DefaultSessionAffinityTTL)
	}
}

func TestParseConfigBytesClaudeQuotaCoolingThresholds(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
port: 8317
claude-quota-cooling-thresholds:
  five-hour-remaining-percent: 0
  weekly-remaining-percent: 150
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	if cfg.ClaudeQuotaCoolingThresholds.FiveHourRemainingPercent != 0 {
		t.Fatalf("FiveHourRemainingPercent = %d, want explicit 0", cfg.ClaudeQuotaCoolingThresholds.FiveHourRemainingPercent)
	}
	if cfg.ClaudeQuotaCoolingThresholds.WeeklyRemainingPercent != 100 {
		t.Fatalf("WeeklyRemainingPercent = %d, want clamped 100", cfg.ClaudeQuotaCoolingThresholds.WeeklyRemainingPercent)
	}
}
