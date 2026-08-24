package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestContentGuardCategoryUnmarshalBoolAndMap(t *testing.T) {
	t.Parallel()
	var cfg SDKConfig
	if err := yaml.Unmarshal([]byte(`
content-guard:
  enabled: true
  mode: enforce
  categories:
    child_safety: true
    jailbreak: false
    fraud:
      strength: strict
    cyber_offense: {strength: loose}
`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.ContentGuard == nil {
		t.Fatal("ContentGuard is nil")
	}
	if cfg.ContentGuard.Enabled == nil || !*cfg.ContentGuard.Enabled {
		t.Fatal("enabled")
	}
	if cfg.ContentGuard.Categories["child_safety"].Strength != ContentGuardStrengthStandard {
		t.Fatalf("child_safety = %+v", cfg.ContentGuard.Categories["child_safety"])
	}
	if cfg.ContentGuard.Categories["jailbreak"].Strength != ContentGuardStrengthOff {
		t.Fatalf("jailbreak = %+v", cfg.ContentGuard.Categories["jailbreak"])
	}
	if cfg.ContentGuard.Categories["fraud"].Strength != ContentGuardStrengthStrict {
		t.Fatalf("fraud = %+v", cfg.ContentGuard.Categories["fraud"])
	}
	if cfg.ContentGuard.Categories["cyber_offense"].Strength != ContentGuardStrengthLoose {
		t.Fatalf("cyber = %+v", cfg.ContentGuard.Categories["cyber_offense"])
	}
}

func TestSaveConfigPreserveCommentsDoesNotMaterializeContentGuard(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("debug: true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := &Config{Debug: false}
	if err := SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "content-guard") {
		t.Fatalf("default content-guard was materialized:\n%s", text)
	}
	if !strings.Contains(text, "debug: false") && !strings.Contains(text, "debug: false\n") {
		if !strings.Contains(text, "debug:") {
			t.Fatalf("debug was dropped:\n%s", text)
		}
	}
}

func TestSaveConfigPreserveCommentsKeepsExistingContentGuard(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	original := "debug: true\ncontent-guard:\n  enabled: true\n  mode: observe\n  # keep this comment\n  categories:\n    child_safety:\n      strength: standard\n"
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := ParseConfigBytes([]byte(original))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg.Debug = false
	if err := SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "content-guard:") || !strings.Contains(text, "keep this comment") {
		t.Fatalf("content-guard section was rewritten away:\n%s", text)
	}
	if !strings.Contains(text, "mode: observe") {
		t.Fatalf("mode missing:\n%s", text)
	}
}
