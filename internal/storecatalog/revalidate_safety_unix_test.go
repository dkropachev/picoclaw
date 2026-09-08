//go:build unix

package storecatalog

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestRevalidateRejectsIrregularLegacyProjection(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	projected, err := Project(Options{Home: home, Config: &config.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, "legacy.pipe")
	if err := syscall.Mkfifo(legacy, 0o600); err != nil {
		t.Skipf("FIFO unavailable: %v", err)
	}
	projected.specs[0].LegacyRoots = []string{legacy}
	if catalog, err := Revalidate(projected); catalog != nil || err == nil {
		t.Fatalf("Revalidate(irregular legacy) = %#v, %v", catalog, err)
	}
}
