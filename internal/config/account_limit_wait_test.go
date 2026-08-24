package config

import (
	"testing"
	"time"
)

func TestAccountLimitWaitDuration(t *testing.T) {
	t.Parallel()

	if got := (&Config{}).AccountLimitWaitDuration(); got != DefaultAccountLimitWaitSeconds*time.Second {
		t.Fatalf("unset duration = %s, want 5s", got)
	}

	zero := 0
	if got := (&Config{AccountLimitWaitSeconds: &zero}).AccountLimitWaitDuration(); got != 0 {
		t.Fatalf("zero duration = %s, want 0", got)
	}

	ten := 10
	if got := (&Config{AccountLimitWaitSeconds: &ten}).AccountLimitWaitDuration(); got != 10*time.Second {
		t.Fatalf("explicit duration = %s, want 10s", got)
	}

	cfg := &Config{}
	cfg.SetAccountLimitWaitSeconds(-3)
	if cfg.AccountLimitWaitSecondsValue() != DefaultAccountLimitWaitSeconds {
		t.Fatalf("negative set = %d, want default %d", cfg.AccountLimitWaitSecondsValue(), DefaultAccountLimitWaitSeconds)
	}
	cfg.SetAccountLimitWaitSeconds(90)
	if cfg.AccountLimitWaitSecondsValue() != MaxAccountLimitWaitSeconds {
		t.Fatalf("oversize set = %d, want max %d", cfg.AccountLimitWaitSecondsValue(), MaxAccountLimitWaitSeconds)
	}
}

func TestNormalizeAccountLimitWaitSeconds(t *testing.T) {
	t.Parallel()

	cfg := &Config{}
	cfg.NormalizeAccountLimitWaitSeconds()
	if cfg.AccountLimitWaitSeconds != nil {
		t.Fatalf("unset stayed nil, got %v", cfg.AccountLimitWaitSeconds)
	}

	negative := -1
	cfg.AccountLimitWaitSeconds = &negative
	cfg.NormalizeAccountLimitWaitSeconds()
	if cfg.AccountLimitWaitSeconds == nil || *cfg.AccountLimitWaitSeconds != DefaultAccountLimitWaitSeconds {
		t.Fatalf("negative normalize = %v, want %d", valueOrNilPtr(cfg.AccountLimitWaitSeconds), DefaultAccountLimitWaitSeconds)
	}

	oversize := 120
	cfg.AccountLimitWaitSeconds = &oversize
	cfg.NormalizeAccountLimitWaitSeconds()
	if cfg.AccountLimitWaitSeconds == nil || *cfg.AccountLimitWaitSeconds != MaxAccountLimitWaitSeconds {
		t.Fatalf("oversize normalize = %v, want %d", valueOrNilPtr(cfg.AccountLimitWaitSeconds), MaxAccountLimitWaitSeconds)
	}
}

func TestParseConfigBytesAccountLimitWaitSeconds(t *testing.T) {
	unset, errParse := ParseConfigBytes([]byte("port: 8317\n"))
	if errParse != nil {
		t.Fatalf("parse unset: %v", errParse)
	}
	if unset.AccountLimitWaitSeconds != nil {
		t.Fatalf("unset parse = %v, want nil", unset.AccountLimitWaitSeconds)
	}
	if unset.AccountLimitWaitSecondsValue() != DefaultAccountLimitWaitSeconds {
		t.Fatalf("unset effective = %d, want %d", unset.AccountLimitWaitSecondsValue(), DefaultAccountLimitWaitSeconds)
	}

	zero, errParse := ParseConfigBytes([]byte("account-limit-wait-seconds: 0\n"))
	if errParse != nil {
		t.Fatalf("parse zero: %v", errParse)
	}
	if zero.AccountLimitWaitSeconds == nil || *zero.AccountLimitWaitSeconds != 0 || zero.AccountLimitWaitDuration() != 0 {
		t.Fatalf("zero parse = %#v duration=%s", zero.AccountLimitWaitSeconds, zero.AccountLimitWaitDuration())
	}

	clamped, errParse := ParseConfigBytes([]byte("account-limit-wait-seconds: 90\n"))
	if errParse != nil {
		t.Fatalf("parse oversize: %v", errParse)
	}
	if clamped.AccountLimitWaitSeconds == nil || *clamped.AccountLimitWaitSeconds != MaxAccountLimitWaitSeconds {
		t.Fatalf("oversize parse = %v, want %d", valueOrNilPtr(clamped.AccountLimitWaitSeconds), MaxAccountLimitWaitSeconds)
	}
}

func valueOrNilPtr(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
