//go:build unix && !aix

package fileidentity

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExistingRejectsNamedPipe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	if identity, exists, err := Existing(path); identity.Valid() || exists || !errors.Is(err, ErrUnsafeType) {
		t.Fatalf("Existing(pipe) = %#v, %t, %v", identity, exists, err)
	}
}
