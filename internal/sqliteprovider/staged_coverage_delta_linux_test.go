//go:build linux

package sqliteprovider

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestStagedCoverageLinuxNonSeekableRetainedHandles(t *testing.T) {
	t.Run("replacement fingerprint seek", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "stage.db")
		if err := os.WriteFile(path, []byte("stage"), 0o600); err != nil {
			t.Fatal(err)
		}
		expected, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		file := openStagedCoveragePathHandle(t, path)
		defer file.Close()
		if _, err := fingerprintOpenedValidatedReplacementStage(
			t.Context(), path, expected, file,
		); err == nil {
			t.Fatal("O_PATH replacement fingerprint seek succeeded")
		}
	})

	t.Run("immutable source revalidation seek", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.db")
		if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot, err := captureImmutableGeneration(path)
		if err != nil {
			t.Fatal(err)
		}
		file := openStagedCoveragePathHandle(t, path)
		defer file.Close()
		if err := revalidateImmutableGeneration(
			t.Context(),
			snapshot,
			[]immutableGenerationOpenMember{{
				index:  0,
				path:   path,
				info:   snapshot.infos[0],
				file:   file,
				digest: [sha256.Size]byte{1},
			}},
		); err == nil {
			t.Fatal("O_PATH immutable source seek succeeded")
		}
	})
}

func TestStagedCoverageExternalCopiedRetentionCloseFailure(t *testing.T) {
	root := t.TempDir()
	shared, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	retained := &retainedStagedGeneration{platform: &stagedRetirementPlatform{
		parent: shared,
		stage:  shared,
	}}
	result, err := stagedCoverageExistingMigration(
		t,
		&stagedCoverageAuthority{},
		stagedMigrationOps{
			discard: func(string, time.Duration) error { return nil },
			copySource: func(
				context.Context,
				ImmutableGenerationSource,
				string,
			) (bool, *retainedStagedGeneration, error) {
				return true, retained, nil
			},
		},
		nil,
	)
	if result.installed || err == nil ||
		!strings.Contains(err.Error(), "release SQLite migration source retention") {
		t.Fatalf("external copied retention close result=%#v error=%v", result, err)
	}
}

func openStagedCoveragePathHandle(t *testing.T, path string) *os.File {
	t.Helper()
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		t.Fatal("O_PATH test handle is unavailable")
	}
	return file
}
