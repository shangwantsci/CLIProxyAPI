package config

import "time"

const (
	DefaultAccountLimitWaitSeconds = 5
	MaxAccountLimitWaitSeconds     = 10
)

// NormalizeAccountLimitWaitSeconds clamps an explicit wait override. A missing
// value is left unset so runtime code can apply the default of 5 seconds.
func (cfg *Config) NormalizeAccountLimitWaitSeconds() {
	if cfg == nil || cfg.AccountLimitWaitSeconds == nil {
		return
	}
	n := *cfg.AccountLimitWaitSeconds
	if n < 0 {
		n = DefaultAccountLimitWaitSeconds
	} else if n > MaxAccountLimitWaitSeconds {
		n = MaxAccountLimitWaitSeconds
	}
	*cfg.AccountLimitWaitSeconds = n
}

// SetAccountLimitWaitSeconds stores a clamped explicit wait override.
func (cfg *Config) SetAccountLimitWaitSeconds(seconds int) {
	if cfg == nil {
		return
	}
	if seconds < 0 {
		seconds = DefaultAccountLimitWaitSeconds
	} else if seconds > MaxAccountLimitWaitSeconds {
		seconds = MaxAccountLimitWaitSeconds
	}
	cfg.AccountLimitWaitSeconds = &seconds
}

// AccountLimitWaitDuration is the runtime wait budget. Unset uses 5 seconds;
// 0 means do not wait.
func (cfg *Config) AccountLimitWaitDuration() time.Duration {
	if cfg == nil || cfg.AccountLimitWaitSeconds == nil {
		return time.Duration(DefaultAccountLimitWaitSeconds) * time.Second
	}
	n := *cfg.AccountLimitWaitSeconds
	if n < 0 {
		n = DefaultAccountLimitWaitSeconds
	} else if n > MaxAccountLimitWaitSeconds {
		n = MaxAccountLimitWaitSeconds
	}
	return time.Duration(n) * time.Second
}

// AccountLimitWaitSecondsValue returns the effective wait in seconds.
func (cfg *Config) AccountLimitWaitSecondsValue() int {
	return int(cfg.AccountLimitWaitDuration() / time.Second)
}
