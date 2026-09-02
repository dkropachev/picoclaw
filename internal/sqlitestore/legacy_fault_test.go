package sqlitestore

import (
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyRootHelpersPropagateFilesystemFailures(t *testing.T) {
	canary := errors.New("legacy path canary")
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "legacy-json", "component-v1")

	t.Run("validate source absolute", func(t *testing.T) {
		swapTestHook(t, &legacyAbsolutePath, func(string) (string, error) { return "", canary })
		if err := validateLegacyRoots(LegacyOptions{SourceRoot: root, ArchiveRoot: archive}); !errors.Is(err, canary) {
			t.Fatalf("validateLegacyRoots() error = %v", err)
		}
	})
	t.Run("validate archive absolute", func(t *testing.T) {
		original := legacyAbsolutePath
		calls := 0
		swapTestHook(t, &legacyAbsolutePath, func(path string) (string, error) {
			calls++
			if calls == 2 {
				return "", canary
			}
			return original(path)
		})
		if err := validateLegacyRoots(LegacyOptions{SourceRoot: root, ArchiveRoot: archive}); !errors.Is(err, canary) {
			t.Fatalf("validateLegacyRoots() error = %v", err)
		}
	})
	t.Run("validate relative", func(t *testing.T) {
		swapTestHook(t, &legacyRelativePath, func(string, string) (string, error) { return "", canary })
		if err := validateLegacyArchiveAncestors(root, archive); err == nil {
			t.Fatalf("validateLegacyArchiveAncestors() error = %v", err)
		}
	})
	t.Run("validate ancestor lstat", func(t *testing.T) {
		first := filepath.Join(root, "legacy-json")
		if err := os.Mkdir(first, 0o700); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
		original := legacyPathLstat
		swapTestHook(t, &legacyPathLstat, func(path string) (os.FileInfo, error) {
			if path == first {
				return nil, canary
			}
			return original(path)
		})
		if err := validateLegacyArchiveAncestors(root, archive); !errors.Is(err, canary) {
			t.Fatalf("validateLegacyArchiveAncestors() error = %v", err)
		}
	})

	for _, failureCall := range []int{1, 2} {
		t.Run("ensure absolute", func(t *testing.T) {
			original := legacyAbsolutePath
			calls := 0
			swapTestHook(t, &legacyAbsolutePath, func(path string) (string, error) {
				calls++
				if calls == failureCall {
					return "", canary
				}
				return original(path)
			})
			if err := ensureLegacyArchiveRoot(root, archive); !errors.Is(err, canary) {
				t.Fatalf("ensureLegacyArchiveRoot() error = %v", err)
			}
		})
	}
	t.Run("ensure relative", func(t *testing.T) {
		swapTestHook(t, &legacyRelativePath, func(string, string) (string, error) { return "", canary })
		if err := ensureLegacyArchiveRoot(root, archive); err == nil {
			t.Fatalf("ensureLegacyArchiveRoot() error = %v", err)
		}
	})
	t.Run("ensure ancestor lstat", func(t *testing.T) {
		first := filepath.Join(root, "legacy-json")
		original := legacyPathLstat
		swapTestHook(t, &legacyPathLstat, func(path string) (os.FileInfo, error) {
			if path == first {
				return nil, canary
			}
			return original(path)
		})
		if err := ensureLegacyArchiveRoot(root, archive); !errors.Is(err, canary) {
			t.Fatalf("ensureLegacyArchiveRoot() error = %v", err)
		}
	})
	t.Run("require directory lstat", func(t *testing.T) {
		swapTestHook(t, &legacyPathLstat, func(string) (os.FileInfo, error) { return nil, canary })
		if err := requireSafeLegacyDirectory(root, "source"); !errors.Is(err, canary) {
			t.Fatalf("requireSafeLegacyDirectory() error = %v", err)
		}
	})
	t.Run("reject nested source lstat", func(t *testing.T) {
		nested := filepath.Join(root, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		original := legacyPathLstat
		swapTestHook(t, &legacyPathLstat, func(path string) (os.FileInfo, error) {
			if path == nested {
				return nil, canary
			}
			return original(path)
		})
		if err := rejectSymlinkPath(root, "nested/source.json"); !errors.Is(err, canary) {
			t.Fatalf("rejectSymlinkPath() error = %v", err)
		}
	})
}

type faultLegacySyncFile struct {
	legacySyncFile
	statCalls int
	statErrAt int
	syncErr   error
}

func (file *faultLegacySyncFile) Stat() (os.FileInfo, error) {
	file.statCalls++
	if file.statCalls == file.statErrAt {
		return nil, errors.New("sync stat canary")
	}
	return file.legacySyncFile.Stat()
}

func (file *faultLegacySyncFile) Sync() error {
	if file.syncErr != nil {
		return file.syncErr
	}
	return file.legacySyncFile.Sync()
}

func TestFreshArchiveTransitionFailureBoundaries(t *testing.T) {
	canary := errors.New("archive transition canary")
	fixture := func(t *testing.T) (string, string, string, legacyFileSnapshot) {
		t.Helper()
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(root, "source.json")
		if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot, found, inspectErr := inspectLegacyRegularFile(source, 1024)
		if inspectErr != nil || !found {
			t.Fatalf("inspect fixture = %t, %v", found, inspectErr)
		}
		archiveRoot := filepath.Join(root, "archive")
		return source, filepath.Join(archiveRoot, "source.json"), archiveRoot, snapshot
	}

	t.Run("source sync", func(t *testing.T) {
		source, archive, archiveRoot, snapshot := fixture(t)
		swapTestHook(
			t,
			&legacyArchiveSyncSource,
			func(string, legacyFileSnapshot, []byte, int64, os.FileMode) (os.FileInfo, error) {
				return nil, canary
			},
		)
		if err := archiveLegacySource(
			source, archive, archiveRoot, snapshot.digest[:], 1024, snapshot.mode,
		); !errors.Is(err, canary) {
			t.Fatalf("archiveLegacySource() error = %v", err)
		}
	})
	t.Run("immediate replacement", func(t *testing.T) {
		source, archive, archiveRoot, snapshot := fixture(t)
		swapTestHook(t, &legacyArchiveBeforeRemove, func() {
			old := source + ".old"
			if err := os.Rename(source, old); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		if err := archiveLegacySource(
			source, archive, archiveRoot, snapshot.digest[:], 1024, snapshot.mode,
		); err == nil {
			t.Fatal("archiveLegacySource() removed an atomic replacement")
		}
	})
	t.Run("source removal", func(t *testing.T) {
		source, archive, archiveRoot, snapshot := fixture(t)
		swapTestHook(t, &legacyArchiveRemove, func(string) error { return canary })
		if err := archiveLegacySource(
			source, archive, archiveRoot, snapshot.digest[:], 1024, snapshot.mode,
		); !errors.Is(err, canary) {
			t.Fatalf("archiveLegacySource() error = %v", err)
		}
	})
}

func TestSyncLegacySourceRejectsOpenIdentityAndDigestFailures(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.json")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, found, inspectErr := inspectLegacyRegularFile(path, 1024)
	if inspectErr != nil || !found {
		t.Fatalf("inspect fixture = %t, %v", found, inspectErr)
	}
	if _, err := syncLegacySourceFile(
		filepath.Join(root, "missing"), snapshot, snapshot.digest[:], 1024, snapshot.mode,
	); err == nil {
		t.Fatal("syncLegacySourceFile() accepted a missing source")
	}
	other := filepath.Join(root, "other.json")
	if err := os.WriteFile(other, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	otherSnapshot, _, err := inspectLegacyRegularFile(other, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncLegacySourceFile(path, otherSnapshot, snapshot.digest[:], 1024, snapshot.mode); err == nil {
		t.Fatal("syncLegacySourceFile() accepted a different inode")
	}
	wrongDigest := sha256.Sum256([]byte("wrong"))
	if _, err := syncLegacySourceFile(path, snapshot, wrongDigest[:], 1024, snapshot.mode); err == nil {
		t.Fatal("syncLegacySourceFile() accepted a different digest")
	}
}

func TestSyncLegacySourcePropagatesReadSyncAndStatFailures(t *testing.T) {
	canary := errors.New("sync source canary")
	fixture := func(t *testing.T) (string, legacyFileSnapshot) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "source.json")
		if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		snapshot, found, inspectErr := inspectLegacyRegularFile(path, 1024)
		if inspectErr != nil || !found {
			t.Fatalf("inspect fixture = %t, %v", found, inspectErr)
		}
		return path, snapshot
	}
	for _, statCall := range []int{1, 2} {
		t.Run("stat", func(t *testing.T) {
			path, snapshot := fixture(t)
			original := legacySyncOpen
			swapTestHook(t, &legacySyncOpen, func(path string, flag int, mode os.FileMode) (legacySyncFile, error) {
				file, openErr := original(path, flag, mode)
				return &faultLegacySyncFile{legacySyncFile: file, statErrAt: statCall}, openErr
			})
			if _, err := syncLegacySourceFile(
				path, snapshot, snapshot.digest[:], 1024, snapshot.mode,
			); err == nil {
				t.Fatal("syncLegacySourceFile() ignored a stat failure")
			}
		})
	}
	t.Run("copy", func(t *testing.T) {
		path, snapshot := fixture(t)
		swapTestHook(t, &legacySyncCopy, func(io.Writer, io.Reader) (int64, error) {
			return 0, canary
		})
		if _, err := syncLegacySourceFile(
			path, snapshot, snapshot.digest[:], 1024, snapshot.mode,
		); !errors.Is(err, canary) {
			t.Fatalf("syncLegacySourceFile() error = %v", err)
		}
	})
	t.Run("sync", func(t *testing.T) {
		path, snapshot := fixture(t)
		original := legacySyncOpen
		swapTestHook(t, &legacySyncOpen, func(path string, flag int, mode os.FileMode) (legacySyncFile, error) {
			file, openErr := original(path, flag, mode)
			return &faultLegacySyncFile{legacySyncFile: file, syncErr: canary}, openErr
		})
		if _, err := syncLegacySourceFile(
			path, snapshot, snapshot.digest[:], 1024, snapshot.mode,
		); !errors.Is(err, canary) {
			t.Fatalf("syncLegacySourceFile() error = %v", err)
		}
	})
}

func TestEnsureLegacyArchiveRootRejectsUnsafeAndInjectedTransitions(t *testing.T) {
	canary := errors.New("archive root canary")
	t.Run("unsafe source", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o770); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		if err := ensureLegacyArchiveRoot(root, filepath.Join(root, "archive")); err == nil {
			t.Fatal("ensureLegacyArchiveRoot() accepted writable source root")
		}
	})
	t.Run("unsafe ancestor", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		ancestor := filepath.Join(root, "archive")
		if err := os.WriteFile(ancestor, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ensureLegacyArchiveRoot(root, filepath.Join(ancestor, "v1")); err == nil {
			t.Fatal("ensureLegacyArchiveRoot() accepted a file ancestor")
		}
	})
	t.Run("chmod", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		swapTestHook(t, &legacyArchiveChmod, func(string, os.FileMode) error { return canary })
		if err := ensureLegacyArchiveRoot(root, filepath.Join(root, "archive")); !errors.Is(err, canary) {
			t.Fatalf("ensureLegacyArchiveRoot() error = %v", err)
		}
	})
	t.Run("post-create lstat", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "archive")
		original := legacyPathLstat
		calls := 0
		swapTestHook(t, &legacyPathLstat, func(path string) (os.FileInfo, error) {
			if path == target {
				calls++
				if calls == 1 {
					return nil, os.ErrNotExist
				}
				return nil, canary
			}
			return original(path)
		})
		if err := ensureLegacyArchiveRoot(root, target); !errors.Is(err, canary) {
			t.Fatalf("ensureLegacyArchiveRoot() error = %v", err)
		}
	})
}
