package helps

import (
	"testing"
)

func TestDefaultClaudeVersion_Is2_1_161(t *testing.T) {
	got := DefaultClaudeVersion(nil)
	if got != "2.1.161" {
		t.Fatalf("expected baseline claude version 2.1.161, got %q", got)
	}
}

func TestDefaultDeviceProfile_KeepsStainlessFingerprints(t *testing.T) {
	p := defaultClaudeDeviceProfile(nil)
	if p.PackageVersion != "0.94.0" {
		t.Fatalf("package version must stay 0.94.0 (vendored in real CLI), got %q", p.PackageVersion)
	}
	if p.RuntimeVersion != "v24.3.0" {
		t.Fatalf("runtime version must stay v24.3.0, got %q", p.RuntimeVersion)
	}
}
