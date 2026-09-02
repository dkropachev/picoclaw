//go:build unix

package repoeval

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestEvaluationProviderFileLockErrorContracts(t *testing.T) {
	originalMkdir := repositoryEvaluationMkdirLockDir
	originalFlock := repositoryEvaluationFlock
	defer func() {
		repositoryEvaluationMkdirLockDir = originalMkdir
		repositoryEvaluationFlock = originalFlock
	}()
	sentinel := errors.New("lock failure")
	repositoryEvaluationMkdirLockDir = func(string, os.FileMode) error { return sentinel }
	if _, err := lockRepositoryEvaluationStoreMode(t.TempDir(), false); !errors.Is(err, sentinel) {
		t.Fatalf("mkdir lock failure = %v", err)
	}
	repositoryEvaluationMkdirLockDir = originalMkdir
	repositoryEvaluationFlock = func(int, int) error { return unix.EWOULDBLOCK }
	if _, err := lockRepositoryEvaluationStoreMode(t.TempDir(), true); !errors.Is(err, ErrConflict) {
		t.Fatalf("nonblocking flock conflict = %v", err)
	}
	repositoryEvaluationFlock = func(int, int) error { return sentinel }
	if _, err := lockRepositoryEvaluationStoreMode(t.TempDir(), false); err == nil {
		t.Fatal("flock failure was accepted")
	}
	restoreAuthority := database.SuspendProviderTestAuthority()
	allowUnfencedEvaluationProviderForTests.Store(false)
	_, authorityErr := lockRepositoryEvaluationStoreMode(t.TempDir(), false)
	allowUnfencedEvaluationProviderForTests.Store(true)
	restoreAuthority()
	if database.CodeOf(authorityErr) != database.CodeUnauthorized {
		t.Fatalf("unfenced file lock = %v", authorityErr)
	}
}
