//go:build unix && !aix

package sqliteprovider

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func withCreatedTargetParentUnixPlatform(
	t *testing.T,
) (string, *stagedTargetParentPlatform) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repository_reviews")
	platform, err := createRetainedStagedTargetParentPlatform(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeRetainedStagedTargetParentPlatform(platform, false) })
	return path, platform
}

func TestStagedTargetParentUnixCreationFaultCoverage(t *testing.T) {
	canary := errors.New("staged target parent Unix canary")
	if platform, err := createRetainedStagedTargetParentPlatformWithOps(
		t.Context(), "/missing", stagedTargetParentUnixOps{},
	); platform != nil || err == nil {
		t.Fatalf("incomplete Unix creation ops = %#v, %v", platform, err)
	}

	base := t.TempDir()
	path := filepath.Join(base, "one", "two")
	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps)
	}{
		{
			name: "initial lstat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "open ancestor",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.open = func(string, int, uint32) (int, error) { return -1, canary }
			},
		},
		{
			name: "mkdir race",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.mkdirat = func(int, string, uint32) error { return unix.EEXIST }
			},
		},
		{
			name: "mkdir error",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.mkdirat = func(int, string, uint32) error { return canary }
			},
		},
		{
			name: "created open",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.openat
				calls := 0
				ops.openat = func(directory int, name string, flags int, mode uint32) (int, error) {
					calls++
					if calls > 1 {
						return -1, canary
					}
					return original(directory, name, flags, mode)
				}
			},
		},
		{
			name: "new file",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.newFile
				calls := 0
				ops.newFile = func(fd uintptr, name string) *os.File {
					calls++
					if calls > 1 {
						return nil
					}
					return original(fd, name)
				}
			},
		},
		{
			name: "chmod",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.fchmod = func(int, uint32) error { return canary }
			},
		},
		{
			name: "parent sync",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.fsync = func(int) error { return canary }
			},
		},
		{
			name: "child sync",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fsync
				calls := 0
				ops.fsync = func(fd int) error {
					calls++
					if calls == 2 {
						return canary
					}
					return original(fd)
				}
			},
		},
		{
			name: "final fstat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fstat
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					calls++
					if calls > 4 {
						return canary
					}
					return original(fd, stat)
				}
			},
		},
		{
			name: "final identity",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.opened = func(*os.File) (
					fileidentity.Identity,
					fileidentity.ObjectType,
					error,
				) {
					return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultStagedTargetParentUnixOps()
			test.mutate(&ops)
			platform, err := createRetainedStagedTargetParentPlatformWithOps(
				t.Context(), path, ops,
			)
			if platform != nil {
				_ = closeRetainedStagedTargetParentPlatform(platform, false)
			}
			if err == nil {
				t.Fatal("faulted Unix parent creation succeeded")
			}
			_ = os.RemoveAll(filepath.Join(base, "one"))
		})
	}
}

func TestStagedTargetParentUnixAdditionalCreationBranchCoverage(t *testing.T) {
	canary := errors.New("staged target parent Unix creation branch canary")
	t.Run("unsafe discovered ancestor", func(t *testing.T) {
		regular := filepath.Join(t.TempDir(), "regular")
		if err := os.WriteFile(regular, []byte("regular"), 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(regular)
		if err != nil {
			t.Fatal(err)
		}
		ops := defaultStagedTargetParentUnixOps()
		ops.lstat = func(string) (os.FileInfo, error) { return info, nil }
		if platform, err := createRetainedStagedTargetParentPlatformWithOps(
			t.Context(), "/unsafe", ops,
		); platform != nil || err == nil {
			t.Fatalf("unsafe ancestor = %#v, %v", platform, err)
		}
	})

	t.Run("no existing ancestor", func(t *testing.T) {
		ops := defaultStagedTargetParentUnixOps()
		ops.lstat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
		if platform, err := createRetainedStagedTargetParentPlatformWithOps(
			t.Context(), "/missing", ops,
		); platform != nil || err == nil {
			t.Fatalf("missing filesystem root = %#v, %v", platform, err)
		}
	})

	t.Run("component bound", func(t *testing.T) {
		path := t.TempDir()
		for index := 0; index <= maximumStagedTargetParentCreatedComponents; index++ {
			path = filepath.Join(path, "component")
		}
		if platform, err := createRetainedStagedTargetParentPlatformWithOps(
			t.Context(), path, defaultStagedTargetParentUnixOps(),
		); platform != nil || err == nil {
			t.Fatalf("excessive missing components = %#v, %v", platform, err)
		}
	})

	t.Run("canceled creation loop", func(t *testing.T) {
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if platform, err := createRetainedStagedTargetParentPlatformWithOps(
			canceled,
			filepath.Join(t.TempDir(), "missing"),
			defaultStagedTargetParentUnixOps(),
		); platform != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled creation loop = %#v, %v", platform, err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps, *bool)
	}{
		{
			name: "created stat lookup",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.fstatat
				failed := false
				ops.fstatat = func(fd int, path string, stat *unix.Stat_t, flags int) error {
					if *created && !failed {
						failed = true
						return canary
					}
					return original(fd, path, stat, flags)
				}
			},
		},
		{
			name: "unsafe created stat",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.fstatat
				changed := false
				ops.fstatat = func(fd int, path string, stat *unix.Stat_t, flags int) error {
					err := original(fd, path, stat, flags)
					if err == nil && *created && !changed {
						changed = true
						stat.Mode = unix.S_IFREG | 0o600
					}
					return err
				}
			},
		},
		{
			name: "opened stat",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.fstat
				failed := false
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					if *created && !failed {
						failed = true
						return canary
					}
					return original(fd, stat)
				}
			},
		},
		{
			name: "opened stat mismatch",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.fstat
				changed := false
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					err := original(fd, stat)
					if err == nil && *created && !changed {
						changed = true
						stat.Ino++
					}
					return err
				}
			},
		},
		{
			name: "post chmod stat",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.fstat
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					if *created {
						calls++
						if calls == 2 {
							return canary
						}
					}
					return original(fd, stat)
				}
			},
		},
		{
			name: "post chmod validation",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.fstat
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					err := original(fd, stat)
					if *created {
						calls++
						if err == nil && calls == 2 {
							stat.Mode = unix.S_IFDIR | 0o755
						}
					}
					return err
				}
			},
		},
		{
			name: "final stat",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.fstat
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					if *created {
						calls++
						if calls == 3 {
							return canary
						}
					}
					return original(fd, stat)
				}
			},
		},
		{
			name: "final stat validation",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.fstat
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					err := original(fd, stat)
					if *created {
						calls++
						if err == nil && calls == 3 {
							stat.Mode = unix.S_IFDIR | 0o755
						}
					}
					return err
				}
			},
		},
		{
			name: "final identity",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.opened
				calls := 0
				ops.opened = func(file *os.File) (
					fileidentity.Identity, fileidentity.ObjectType, error,
				) {
					if *created {
						calls++
						if calls == 2 {
							return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
						}
					}
					return original(file)
				}
			},
		},
		{
			name: "final named stat",
			mutate: func(ops *stagedTargetParentUnixOps, _ *bool) {
				ops.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "created")
			ops := defaultStagedTargetParentUnixOps()
			created := false
			originalMkdir := ops.mkdirat
			ops.mkdirat = func(fd int, path string, mode uint32) error {
				err := originalMkdir(fd, path, mode)
				if err == nil {
					created = true
				}
				return err
			}
			test.mutate(&ops, &created)
			platform, err := createRetainedStagedTargetParentPlatformWithOps(
				t.Context(), path, ops,
			)
			if platform != nil {
				_ = closeRetainedStagedTargetParentPlatform(platform, false)
			}
			if err == nil {
				t.Fatal("faulted Unix parent creation succeeded")
			}
			_ = os.RemoveAll(path)
		})
	}
}

func TestStagedTargetParentUnixTrustedOpenAdditionalBranches(t *testing.T) {
	canary := errors.New("staged target parent Unix trusted-open branch canary")
	t.Run("root path", func(t *testing.T) {
		file, err := openTrustedProviderUnixDirectoryWithOps(
			string(os.PathSeparator), defaultStagedTargetParentUnixOps(),
		)
		if err != nil || file == nil {
			t.Fatalf("open trusted root = %#v, %v", file, err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps)
	}{
		{
			name: "untrusted root",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fstat
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					err := original(fd, stat)
					stat.Mode = unix.S_IFREG | 0o600
					return err
				}
			},
		},
		{
			name: "component stat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fstat
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					calls++
					if calls == 2 {
						return canary
					}
					return original(fd, stat)
				}
			},
		},
		{
			name: "untrusted component",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fstat
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					err := original(fd, stat)
					calls++
					if err == nil && calls == 2 {
						stat.Mode = unix.S_IFREG | 0o600
					}
					return err
				}
			},
		},
		{
			name: "previous close",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.closeFD
				failed := false
				ops.closeFD = func(fd int) error {
					err := original(fd)
					if !failed {
						failed = true
						return errors.Join(canary, err)
					}
					return err
				}
			},
		},
		{
			name: "boundary handle",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.newFile = func(uintptr, string) *os.File { return nil }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultStagedTargetParentUnixOps()
			test.mutate(&ops)
			file, err := openTrustedProviderUnixDirectoryWithOps(
				filepath.Join(string(os.PathSeparator), "tmp"), ops,
			)
			if file != nil {
				_ = file.Close()
			}
			if err == nil {
				t.Fatal("faulted trusted-directory open succeeded")
			}
		})
	}
}

func TestStagedTargetParentUnixPostCreateFaultsRollbackLineage(t *testing.T) {
	canary := errors.New("staged target parent Unix post-create rollback canary")
	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps, *bool)
	}{
		{
			name: "created open",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.openat
				failed := false
				ops.openat = func(fd int, path string, flags int, mode uint32) (int, error) {
					if *created && !failed {
						failed = true
						return -1, canary
					}
					return original(fd, path, flags, mode)
				}
			},
		},
		{
			name: "created handle",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.newFile
				failed := false
				ops.newFile = func(fd uintptr, path string) *os.File {
					if *created && !failed {
						failed = true
						return nil
					}
					return original(fd, path)
				}
			},
		},
		{
			name: "created chmod",
			mutate: func(ops *stagedTargetParentUnixOps, _ *bool) {
				failed := false
				original := ops.fchmod
				ops.fchmod = func(fd int, mode uint32) error {
					if !failed {
						failed = true
						return canary
					}
					return original(fd, mode)
				}
			},
		},
		{
			name: "created identity",
			mutate: func(ops *stagedTargetParentUnixOps, created *bool) {
				original := ops.opened
				failed := false
				ops.opened = func(file *os.File) (
					fileidentity.Identity,
					fileidentity.ObjectType,
					error,
				) {
					if *created && !failed {
						failed = true
						return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
					}
					return original(file)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "one", "two")
			ops := defaultStagedTargetParentUnixOps()
			created := false
			originalMkdir := ops.mkdirat
			ops.mkdirat = func(fd int, path string, mode uint32) error {
				err := originalMkdir(fd, path, mode)
				if err == nil {
					created = true
				}
				return err
			}
			test.mutate(&ops, &created)
			platform, err := createRetainedStagedTargetParentPlatformWithOps(
				t.Context(), path, ops,
			)
			if platform != nil || err == nil {
				t.Fatalf("post-create fault = platform:%#v error:%v", platform, err)
			}
			if _, err := os.Lstat(filepath.Join(base, "one")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("post-create fault leaked lineage: %v", err)
			}
		})
	}
}

func TestStagedTargetParentUnixRejectsBlockingSpecialEntry(t *testing.T) {
	parentPath, platform := withCreatedTargetParentUnixPlatform(t)
	stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-fifo")
	if err := unix.Mkfifo(stage, 0o600); err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	identitySource := filepath.Join(t.TempDir(), "identity-source")
	if err := os.WriteFile(identitySource, []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := stagedTargetParentTestStageIdentity(t, identitySource)
	done := make(chan error, 1)
	go func() {
		_, checkErr := checkRetainedStagedTargetParentSoleStagePlatformWithOps(
			t.Context(), parentPath, stage, stageInfo, identity, platform,
			defaultStagedTargetParentUnixOps(),
		)
		done <- checkErr
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO stage passed exact target-parent inventory")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FIFO stage blocked exact target-parent inventory")
	}
}

func TestStagedTargetParentUnixRollbackPreservesRenamedAndSubstituteDirectories(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "workspace", "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil || parent == nil {
		t.Fatalf("prepare retained parent = %#v, %v", parent, err)
	}
	moved := parentPath + ".moved"
	if err := os.Rename(parentPath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err == nil {
		t.Fatal("rollback accepted a substituted created directory name")
	}
	for _, path := range []string{parentPath, moved} {
		info, err := os.Lstat(path)
		if err != nil || info == nil || !info.IsDir() {
			t.Fatalf("rollback removed protected directory %q: %v", path, err)
		}
	}
}

func TestStagedTargetParentUnixCloseJoinsErrorsAndReleasesHandles(t *testing.T) {
	_, platform := withCreatedTargetParentUnixPlatform(t)
	canary := errors.New("staged target parent Unix close canary")
	ops := defaultStagedTargetParentUnixOps()
	ops.close = func(file *os.File) error {
		return errors.Join(canary, file.Close())
	}
	if err := closeRetainedStagedTargetParentPlatformWithOps(
		platform,
		false,
		ops,
	); !errors.Is(err, canary) {
		t.Fatalf("target-parent close error = %v", err)
	}
	if platform.directory != nil || len(platform.created) != 0 {
		t.Fatalf("target-parent handles remained after close: %#v", platform)
	}
}

func TestStagedTargetParentUnixCloseAndRollbackFaultCoverage(t *testing.T) {
	canary := errors.New("staged target parent Unix rollback branch canary")
	if err := closeRetainedStagedTargetParentPlatformWithOps(
		nil, true, stagedTargetParentUnixOps{},
	); err != nil {
		t.Fatalf("nil target-parent platform close = %v", err)
	}

	t.Run("fallback close", func(t *testing.T) {
		_, platform := withCreatedTargetParentUnixPlatform(t)
		ops := defaultStagedTargetParentUnixOps()
		ops.close = nil
		if err := closeRetainedStagedTargetParentPlatformWithOps(
			platform, false, ops,
		); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unavailable cleanup still closes", func(t *testing.T) {
		_, platform := withCreatedTargetParentUnixPlatform(t)
		ops := stagedTargetParentUnixOps{
			close: func(file *os.File) error { return file.Close() },
		}
		if err := closeRetainedStagedTargetParentPlatformWithOps(
			platform, true, ops,
		); err == nil {
			t.Fatal("unavailable rollback operations reported success")
		}
		if platform.directory != nil || len(platform.created) != 0 {
			t.Fatalf("unavailable rollback leaked handles: %#v", platform)
		}
	})

	if err := rollbackStagedTargetParentUnixComponent(
		nil, defaultStagedTargetParentUnixOps(),
	); err == nil {
		t.Fatal("nil rollback component succeeded")
	}

	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps)
	}{
		{
			name: "retained stat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.fstat = func(int, *unix.Stat_t) error { return canary }
			},
		},
		{
			name: "named open",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.openat = func(int, string, int, uint32) (int, error) { return -1, canary }
			},
		},
		{
			name: "named handle",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.newFile = func(uintptr, string) *os.File { return nil }
			},
		},
		{
			name: "named stat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fstat
				calls := 0
				ops.fstat = func(fd int, stat *unix.Stat_t) error {
					calls++
					if calls == 2 {
						return canary
					}
					return original(fd, stat)
				}
			},
		},
		{
			name: "named identity",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.opened
				calls := 0
				ops.opened = func(file *os.File) (
					fileidentity.Identity, fileidentity.ObjectType, error,
				) {
					calls++
					if calls == 2 {
						return fileidentity.Identity{}, fileidentity.ObjectTypeDirectory, nil
					}
					return original(file)
				}
			},
		},
		{
			name: "directory read",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.readDir = func(*os.File, int) ([]os.DirEntry, error) { return nil, canary }
			},
		},
		{
			name: "named close",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.close = func(file *os.File) error {
					return errors.Join(canary, file.Close())
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, platform := withCreatedTargetParentUnixPlatform(t)
			component := &platform.created[len(platform.created)-1]
			ops := defaultStagedTargetParentUnixOps()
			test.mutate(&ops)
			if err := rollbackStagedTargetParentUnixComponent(component, ops); err == nil {
				t.Fatal("faulted rollback component succeeded")
			}
			_ = closeRetainedStagedTargetParentPlatform(platform, false)
		})
	}
}

func TestStagedTargetParentUnixUnlinkFaultCoverage(t *testing.T) {
	canary := errors.New("staged target parent Unix unlink branch canary")
	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps)
	}{
		{
			name: "initial stat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.fstatat = func(int, string, *unix.Stat_t, int) error { return canary }
			},
		},
		{
			name: "substituted stat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fstatat
				ops.fstatat = func(fd int, path string, stat *unix.Stat_t, flags int) error {
					err := original(fd, path, stat, flags)
					if err == nil {
						stat.Ino++
					}
					return err
				}
			},
		},
		{
			name: "unlink",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.unlinkat = func(int, string, int) error { return canary }
			},
		},
		{
			name: "parent sync",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.fsync = func(int) error { return canary }
			},
		},
		{
			name: "name reappeared",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fstatat
				calls := 0
				ops.fstatat = func(fd int, path string, stat *unix.Stat_t, flags int) error {
					calls++
					if calls == 2 {
						*stat = unix.Stat_t{}
						return nil
					}
					return original(fd, path, stat, flags)
				}
			},
		},
		{
			name: "final stat error",
			mutate: func(ops *stagedTargetParentUnixOps) {
				original := ops.fstatat
				calls := 0
				ops.fstatat = func(fd int, path string, stat *unix.Stat_t, flags int) error {
					calls++
					if calls == 2 {
						return canary
					}
					return original(fd, path, stat, flags)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, platform := withCreatedTargetParentUnixPlatform(t)
			component := &platform.created[len(platform.created)-1]
			ops := defaultStagedTargetParentUnixOps()
			test.mutate(&ops)
			if err := unlinkStagedTargetParentUnixComponent(component, ops); err == nil {
				t.Fatal("faulted target-parent unlink succeeded")
			}
			_ = closeRetainedStagedTargetParentPlatform(platform, false)
		})
	}
}

func TestStagedTargetParentUnixOpenAndCheckFaultCoverage(t *testing.T) {
	canary := errors.New("staged target parent Unix check canary")
	file, err := openTrustedProviderUnixDirectoryWithOps("relative", stagedTargetParentUnixOps{})
	if file != nil || err == nil {
		t.Fatalf("invalid trusted directory open = %#v, %v", file, err)
	}
	path := t.TempDir()
	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps)
	}{
		{
			name: "root open",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.open = func(string, int, uint32) (int, error) { return -1, canary }
			},
		},
		{
			name: "root fstat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.fstat = func(int, *unix.Stat_t) error { return canary }
			},
		},
		{
			name: "component open",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.openat = func(int, string, int, uint32) (int, error) { return -1, canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultStagedTargetParentUnixOps()
			test.mutate(&ops)
			file, err := openTrustedProviderUnixDirectoryWithOps(path, ops)
			if file != nil {
				_ = file.Close()
			}
			if err == nil {
				t.Fatal("faulted trusted-directory open succeeded")
			}
		})
	}

	parentPath, platform := withCreatedTargetParentUnixPlatform(t)
	if _, err := checkRetainedStagedTargetParentPlatformWithOps(
		nil, parentPath, nil, platform, stagedTargetParentUnixOps{},
	); err == nil {
		t.Fatal("invalid Unix parent check succeeded")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := checkRetainedStagedTargetParentPlatformWithOps(
		canceled,
		parentPath,
		nil,
		platform,
		defaultStagedTargetParentUnixOps(),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Unix parent check = %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps)
	}{
		{
			name: "fstat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.fstat = func(int, *unix.Stat_t) error { return canary }
			},
		},
		{
			name: "opened identity",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.opened = func(*os.File) (
					fileidentity.Identity,
					fileidentity.ObjectType,
					error,
				) {
					return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
				}
			},
		},
		{
			name: "named open",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.openat = func(int, string, int, uint32) (int, error) { return -1, canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultStagedTargetParentUnixOps()
			test.mutate(&ops)
			if _, err := checkRetainedStagedTargetParentPlatformWithOps(
				t.Context(), parentPath, nil, platform, ops,
			); err == nil {
				t.Fatal("faulted Unix parent check succeeded")
			}
		})
	}

	t.Run("retained validation", func(t *testing.T) {
		ops := defaultStagedTargetParentUnixOps()
		original := ops.fstat
		ops.fstat = func(fd int, stat *unix.Stat_t) error {
			err := original(fd, stat)
			if fd == int(platform.directory.Fd()) && err == nil {
				stat.Mode = unix.S_IFDIR | 0o755
			}
			return err
		}
		if _, err := checkRetainedStagedTargetParentPlatformWithOps(
			t.Context(), parentPath, nil, platform, ops,
		); err == nil {
			t.Fatal("unsafe retained-parent mode passed validation")
		}
	})

	t.Run("named stat", func(t *testing.T) {
		ops := defaultStagedTargetParentUnixOps()
		ops.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
		if _, err := checkRetainedStagedTargetParentPlatformWithOps(
			t.Context(), parentPath, nil, platform, ops,
		); !errors.Is(err, canary) {
			t.Fatalf("named parent stat = %v", err)
		}
	})

	t.Run("named identity", func(t *testing.T) {
		ops := defaultStagedTargetParentUnixOps()
		original := ops.opened
		calls := 0
		ops.opened = func(file *os.File) (
			fileidentity.Identity, fileidentity.ObjectType, error,
		) {
			calls++
			if calls == 2 {
				return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
			}
			return original(file)
		}
		if _, err := checkRetainedStagedTargetParentPlatformWithOps(
			t.Context(), parentPath, nil, platform, ops,
		); !errors.Is(err, canary) {
			t.Fatalf("named parent identity = %v", err)
		}
	})

	t.Run("named close", func(t *testing.T) {
		ops := defaultStagedTargetParentUnixOps()
		ops.close = func(file *os.File) error {
			return errors.Join(canary, file.Close())
		}
		if _, err := checkRetainedStagedTargetParentPlatformWithOps(
			t.Context(), parentPath, nil, platform, ops,
		); !errors.Is(err, canary) {
			t.Fatalf("named parent close = %v", err)
		}
	})
}

func TestStagedTargetParentUnixInventoryFaultCoverage(t *testing.T) {
	parentPath, platform := withCreatedTargetParentUnixPlatform(t)
	stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("staged target parent inventory canary")
	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps)
	}{
		{
			name: "openat",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.openat = func(int, string, int, uint32) (int, error) { return -1, canary }
			},
		},
		{
			name: "new file",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.newFile = func(uintptr, string) *os.File { return nil }
			},
		},
		{
			name: "read dir",
			mutate: func(ops *stagedTargetParentUnixOps) {
				ops.readDir = func(*os.File, int) ([]os.DirEntry, error) { return nil, canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultStagedTargetParentUnixOps()
			test.mutate(&ops)
			if _, err := checkRetainedStagedTargetParentSoleStagePlatformWithOps(
				t.Context(), parentPath, stage, stageInfo,
				stagedTargetParentTestStageIdentity(t, stage), platform, ops,
			); err == nil {
				t.Fatal("faulted Unix parent inventory succeeded")
			}
		})
	}
}

func TestStagedTargetParentUnixAdditionalInventoryBranchCoverage(t *testing.T) {
	newFixture := func(t *testing.T, mode os.FileMode) (
		string,
		string,
		os.FileInfo,
		fileidentity.Identity,
		*stagedTargetParentPlatform,
	) {
		t.Helper()
		parentPath, platform := withCreatedTargetParentUnixPlatform(t)
		stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
		if err := os.WriteFile(stage, []byte("stage"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stage, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(stage)
		if err != nil {
			t.Fatal(err)
		}
		return parentPath, stage, info,
			stagedTargetParentTestStageIdentity(t, stage), platform
	}
	canary := errors.New("staged target parent Unix inventory branch canary")

	t.Run("invalid entry kind", func(t *testing.T) {
		parentPath, stage, info, identity, platform := newFixture(t, 0o600)
		if _, err := checkRetainedStagedTargetParentSoleEntryPlatformWithOps(
			t.Context(), parentPath, stage, info, identity, platform,
			defaultStagedTargetParentUnixOps(), "invalid", true,
		); err == nil {
			t.Fatal("invalid target-parent entry kind succeeded")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*stagedTargetParentUnixOps, string)
	}{
		{
			name: "inventory open",
			mutate: func(ops *stagedTargetParentUnixOps, _ string) {
				original := ops.openat
				ops.openat = func(fd int, name string, flags int, mode uint32) (int, error) {
					if name == "." {
						return -1, canary
					}
					return original(fd, name, flags, mode)
				}
			},
		},
		{
			name: "inventory handle",
			mutate: func(ops *stagedTargetParentUnixOps, _ string) {
				original := ops.newFile
				calls := 0
				ops.newFile = func(fd uintptr, name string) *os.File {
					calls++
					if calls == 2 {
						return nil
					}
					return original(fd, name)
				}
			},
		},
		{
			name: "entry open",
			mutate: func(ops *stagedTargetParentUnixOps, stage string) {
				original := ops.openat
				ops.openat = func(fd int, name string, flags int, mode uint32) (int, error) {
					if name == filepath.Base(stage) {
						return -1, canary
					}
					return original(fd, name, flags, mode)
				}
			},
		},
		{
			name: "entry handle",
			mutate: func(ops *stagedTargetParentUnixOps, stage string) {
				original := ops.newFile
				ops.newFile = func(fd uintptr, name string) *os.File {
					if name == stage {
						return nil
					}
					return original(fd, name)
				}
			},
		},
		{
			name: "entry stat",
			mutate: func(ops *stagedTargetParentUnixOps, stage string) {
				original := ops.stat
				ops.stat = func(file *os.File) (os.FileInfo, error) {
					if file.Name() == stage {
						return nil, canary
					}
					return original(file)
				}
			},
		},
		{
			name: "entry identity",
			mutate: func(ops *stagedTargetParentUnixOps, stage string) {
				original := ops.opened
				ops.opened = func(file *os.File) (
					fileidentity.Identity, fileidentity.ObjectType, error,
				) {
					if file.Name() == stage {
						return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
					}
					return original(file)
				}
			},
		},
		{
			name: "final parent check",
			mutate: func(ops *stagedTargetParentUnixOps, _ string) {
				original := ops.opened
				calls := 0
				ops.opened = func(file *os.File) (
					fileidentity.Identity, fileidentity.ObjectType, error,
				) {
					calls++
					if calls == 4 {
						return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
					}
					return original(file)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parentPath, stage, info, identity, platform := newFixture(t, 0o600)
			ops := defaultStagedTargetParentUnixOps()
			test.mutate(&ops, stage)
			if _, err := checkRetainedStagedTargetParentSoleStagePlatformWithOps(
				t.Context(), parentPath, stage, info, identity, platform, ops,
			); err == nil {
				t.Fatal("faulted target-parent inventory succeeded")
			}
		})
	}

	t.Run("unsafe entry mode", func(t *testing.T) {
		parentPath, stage, info, identity, platform := newFixture(t, 0o644)
		if _, err := checkRetainedStagedTargetParentSoleStagePlatformWithOps(
			t.Context(), parentPath, stage, info, identity, platform,
			defaultStagedTargetParentUnixOps(),
		); err == nil {
			t.Fatal("unsafe target-parent entry mode succeeded")
		}
	})
}

func TestStagedTargetParentUnixRenameFaultCoverage(t *testing.T) {
	newFixture := func(t *testing.T) (
		string,
		string,
		string,
		os.FileInfo,
		*os.File,
		*stagedTargetParentPlatform,
	) {
		t.Helper()
		parentPath, platform := withCreatedTargetParentUnixPlatform(t)
		stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
		target := filepath.Join(parentPath, "repository-reviews.db")
		if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
			t.Fatal(err)
		}
		stageInfo, err := os.Lstat(stage)
		if err != nil {
			t.Fatal(err)
		}
		stageFile, err := os.OpenFile(stage, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = stageFile.Close() })
		return parentPath, stage, target, stageInfo, stageFile, platform
	}
	canary := errors.New("staged target parent rename canary")

	t.Run("invalid", func(t *testing.T) {
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			nil, "", "", "", nil, nil, fileidentity.Identity{}, nil,
			stagedTargetParentUnixOps{},
		); complete || err == nil {
			t.Fatalf("invalid Unix replacement = complete:%t error:%v", complete, err)
		}
	})

	t.Run("stage stat", func(t *testing.T) {
		parentPath, stage, target, stageInfo, stageFile, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		ops.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			t.Context(), parentPath, stage, target, stageInfo, stageFile,
			stagedTargetParentTestStageIdentity(t, stage), platform, ops,
		); complete || !errors.Is(err, canary) {
			t.Fatalf("stage-stat replacement = complete:%t error:%v", complete, err)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		parentPath, stage, target, stageInfo, stageFile, platform := newFixture(t)
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			canceled, parentPath, stage, target, stageInfo, stageFile,
			stagedTargetParentTestStageIdentity(t, stage), platform,
			defaultStagedTargetParentUnixOps(),
		); complete || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled replacement = complete:%t error:%v", complete, err)
		}
	})

	t.Run("conclusive rename failure", func(t *testing.T) {
		parentPath, stage, target, stageInfo, stageFile, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		ops.renameAt = func(int, string, int, string) error { return canary }
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			t.Context(), parentPath, stage, target, stageInfo, stageFile,
			stagedTargetParentTestStageIdentity(t, stage), platform, ops,
		); complete || !errors.Is(err, canary) {
			t.Fatalf("conclusive rename failure = complete:%t error:%v", complete, err)
		}
	})

	t.Run("ambiguous reported failure", func(t *testing.T) {
		parentPath, stage, target, stageInfo, stageFile, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		original := ops.renameAt
		ops.renameAt = func(oldFD int, old string, newFD int, newName string) error {
			if err := original(oldFD, old, newFD, newName); err != nil {
				return err
			}
			return canary
		}
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			t.Context(), parentPath, stage, target, stageInfo, stageFile,
			stagedTargetParentTestStageIdentity(t, stage), platform, ops,
		); !complete || !errors.Is(err, canary) {
			t.Fatalf("ambiguous rename failure = complete:%t error:%v", complete, err)
		}
	})

	t.Run("parent sync", func(t *testing.T) {
		parentPath, stage, target, stageInfo, stageFile, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		ops.fsync = func(int) error { return canary }
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			t.Context(), parentPath, stage, target, stageInfo, stageFile,
			stagedTargetParentTestStageIdentity(t, stage), platform, ops,
		); !complete || !errors.Is(err, canary) {
			t.Fatalf("parent-sync replacement = complete:%t error:%v", complete, err)
		}
	})

	t.Run("sidecar inspection", func(t *testing.T) {
		parentPath, stage, target, stageInfo, stageFile, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		original := ops.fstatat
		ops.fstatat = func(fd int, path string, stat *unix.Stat_t, flags int) error {
			if path == filepath.Base(stage)+"-wal" {
				return canary
			}
			return original(fd, path, stat, flags)
		}
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			t.Context(), parentPath, stage, target, stageInfo, stageFile,
			stagedTargetParentTestStageIdentity(t, stage), platform, ops,
		); !complete || !errors.Is(err, canary) {
			t.Fatalf("sidecar-inspection replacement = complete:%t error:%v", complete, err)
		}
	})

	t.Run("arbitrary entry after rename", func(t *testing.T) {
		parentPath, stage, target, stageInfo, stageFile, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		original := ops.renameAt
		ops.renameAt = func(oldFD int, old string, newFD int, newName string) error {
			if err := original(oldFD, old, newFD, newName); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(parentPath, "late-extra"), []byte("extra"), 0o600)
		}
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			t.Context(), parentPath, stage, target, stageInfo, stageFile,
			stagedTargetParentTestStageIdentity(t, stage), platform, ops,
		); !complete || err == nil {
			t.Fatalf("late-entry replacement = complete:%t error:%v", complete, err)
		}
	})

	t.Run("stage substitute after rename", func(t *testing.T) {
		parentPath, stage, target, stageInfo, stageFile, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		original := ops.renameAt
		ops.renameAt = func(oldFD int, old string, newFD int, newName string) error {
			if err := original(oldFD, old, newFD, newName); err != nil {
				return err
			}
			return os.WriteFile(stage, []byte("substitute"), 0o600)
		}
		if complete, err := replaceRetainedStagedTargetParentStagePlatformWithOps(
			t.Context(), parentPath, stage, target, stageInfo, stageFile,
			stagedTargetParentTestStageIdentity(t, stage), platform, ops,
		); !complete || err == nil {
			t.Fatalf("post-rename substitute = complete:%t error:%v", complete, err)
		}
	})

	t.Run("rename-state open", func(t *testing.T) {
		_, stage, target, stageInfo, _, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		ops.openat = func(int, string, int, uint32) (int, error) { return -1, canary }
		_, _, err := retainedTargetParentUnixRenameState(
			int(platform.directory.Fd()), filepath.Base(stage), filepath.Base(target),
			stageInfo, stagedTargetParentTestStageIdentity(t, stage), ops,
		)
		if !errors.Is(err, canary) {
			t.Fatalf("rename-state open error = %v", err)
		}
	})

	t.Run("rename-state handle", func(t *testing.T) {
		_, stage, target, stageInfo, _, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		ops.newFile = func(uintptr, string) *os.File { return nil }
		_, _, err := retainedTargetParentUnixRenameState(
			int(platform.directory.Fd()), filepath.Base(stage), filepath.Base(target),
			stageInfo, stagedTargetParentTestStageIdentity(t, stage), ops,
		)
		if err == nil {
			t.Fatal("rename-state missing handle succeeded")
		}
	})

	t.Run("rename-state stat", func(t *testing.T) {
		_, stage, target, stageInfo, _, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		ops.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
		_, _, err := retainedTargetParentUnixRenameState(
			int(platform.directory.Fd()), filepath.Base(stage), filepath.Base(target),
			stageInfo, stagedTargetParentTestStageIdentity(t, stage), ops,
		)
		if !errors.Is(err, canary) {
			t.Fatalf("rename-state stat error = %v", err)
		}
	})

	t.Run("rename-state stage substitution", func(t *testing.T) {
		_, stage, target, stageInfo, _, platform := newFixture(t)
		expectedIdentity := stagedTargetParentTestStageIdentity(t, stage)
		if err := os.Remove(stage); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stage, []byte("substitute"), 0o600); err != nil {
			t.Fatal(err)
		}
		stageMatches, _, err := retainedTargetParentUnixRenameState(
			int(platform.directory.Fd()), filepath.Base(stage), filepath.Base(target),
			stageInfo, expectedIdentity, defaultStagedTargetParentUnixOps(),
		)
		if stageMatches || err == nil {
			t.Fatalf("rename-state stage substitution = match:%t error:%v", stageMatches, err)
		}
	})

	t.Run("rename-state substitutions", func(t *testing.T) {
		_, stage, target, stageInfo, _, platform := newFixture(t)
		if err := os.WriteFile(target, []byte("target decoy"), 0o600); err != nil {
			t.Fatal(err)
		}
		stageMatches, targetMatches, err := retainedTargetParentUnixRenameState(
			int(platform.directory.Fd()),
			filepath.Base(stage),
			filepath.Base(target),
			stageInfo,
			stagedTargetParentTestStageIdentity(t, stage),
			defaultStagedTargetParentUnixOps(),
		)
		if !stageMatches || targetMatches || err == nil {
			t.Fatalf("substituted rename state = stage:%t target:%t error:%v", stageMatches, targetMatches, err)
		}
	})

	t.Run("sidecar present", func(t *testing.T) {
		_, stage, target, _, _, platform := newFixture(t)
		if err := os.WriteFile(stage+"-wal", []byte("sidecar"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := requireRetainedTargetParentUnixSidecarsMissing(
			int(platform.directory.Fd()), filepath.Base(stage), filepath.Base(target),
		); err == nil {
			t.Fatal("present Unix sidecar was accepted")
		}
	})

	t.Run("sidecar IO", func(t *testing.T) {
		_, stage, target, _, _, platform := newFixture(t)
		ops := defaultStagedTargetParentUnixOps()
		ops.fstatat = func(int, string, *unix.Stat_t, int) error { return io.ErrUnexpectedEOF }
		if err := requireRetainedTargetParentUnixSidecarsMissingWithOps(
			int(platform.directory.Fd()), filepath.Base(stage), filepath.Base(target), ops,
		); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("sidecar IO error = %v", err)
		}
	})
}
