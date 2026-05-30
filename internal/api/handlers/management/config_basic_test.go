package management

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateConfigValidationTempFileUsesSystemTempDir(t *testing.T) {
	configDir := t.TempDir()

	tmpFile, err := createConfigValidationTempFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("createConfigValidationTempFile() error = %v", err)
	}
	tmpName := tmpFile.Name()
	if errClose := tmpFile.Close(); errClose != nil {
		t.Fatalf("close temp file: %v", errClose)
	}
	t.Cleanup(func() {
		_ = os.Remove(tmpName)
	})

	if filepath.Clean(filepath.Dir(tmpName)) == filepath.Clean(configDir) {
		t.Fatalf("validation temp file was created next to config.yaml: %s", tmpName)
	}
}
