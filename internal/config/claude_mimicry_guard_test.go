package config

import "testing"

func TestParseConfigBytes_DefaultsClaudeMimicryGuard(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("api-keys: []\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes error: %v", err)
	}

	if got := cfg.ClaudeMimicryGuard.Mode; got != DefaultClaudeMimicryGuardMode {
		t.Fatalf("ClaudeMimicryGuard.Mode = %q, want %q", got, DefaultClaudeMimicryGuardMode)
	}
	if got := cfg.ClaudeMimicryGuard.EventsLimit; got != DefaultClaudeMimicryGuardEventsLimit {
		t.Fatalf("ClaudeMimicryGuard.EventsLimit = %d, want %d", got, DefaultClaudeMimicryGuardEventsLimit)
	}
}

func TestSanitizeClaudeMimicryGuard(t *testing.T) {
	cfg := &Config{ClaudeMimicryGuard: ClaudeMimicryGuardConfig{
		Mode:        " STRICT ",
		EventsLimit: 5000,
	}}

	cfg.SanitizeClaudeMimicryGuard()

	if got := cfg.ClaudeMimicryGuard.Mode; got != "strict" {
		t.Fatalf("Mode = %q, want strict", got)
	}
	if got := cfg.ClaudeMimicryGuard.EventsLimit; got != 2000 {
		t.Fatalf("EventsLimit = %d, want 2000", got)
	}
}
