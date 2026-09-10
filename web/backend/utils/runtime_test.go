package utils

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestFindPicoclawBinaryPrefersInertExplicitSentinel(t *testing.T) {
	directory := t.TempDir()
	sentinel := filepath.Join(directory, "inert-picoclaw")
	if err := os.WriteFile(sentinel, []byte("not an executable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canaryName := "picoclaw"
	if runtime.GOOS == "windows" {
		canaryName += ".exe"
	}
	canaryDirectory := t.TempDir()
	canary := filepath.Join(canaryDirectory, canaryName)
	if err := os.WriteFile(canary, []byte("ambient canary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvBinary, sentinel)
	t.Setenv("PATH", canaryDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := FindPicoclawBinary(); got != sentinel {
		t.Fatalf("FindPicoclawBinary() = %q, want inert sentinel %q", got, sentinel)
	}
}
