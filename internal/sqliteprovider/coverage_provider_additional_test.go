//nolint:govet // Failure-boundary assertions intentionally reuse narrow error names.
package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCoverageProviderOpenAndTransitionRemainingBranches(t *testing.T) {
	canary := errors.New("provider open branch canary")
	memory := openProviderScript(t)
	ops := providerOpenOps{
		validateAncestors: func(string) error { return nil },
		prepare:           func(string) error { return nil },
		dsn:               func(string, time.Duration) (string, error) { return "memory", nil },
		open:              func(string) (*sql.DB, error) { return memory, nil },
		ping:              func(*sql.DB, time.Duration) error { return canary },
		mainIdentity:      func(string) (os.FileInfo, error) { return nil, canary },
		generationIdentity: func(string, os.FileInfo) ([4]os.FileInfo, error) {
			return [4]os.FileInfo{}, canary
		},
		secure: func(string) error { return nil },
	}
	if database, err := openStore(":memory:", time.Second, ops); !errors.Is(err, canary) || database != nil {
		t.Fatalf("memory ping failure = %#v, %v", database, err)
	}
	if database, err := openStore("", time.Second, ops); err == nil || database != nil {
		t.Fatalf("direct invalid open = %#v, %v", database, err)
	}

	root := t.TempDir()
	mainPath := filepath.Join(root, "main.db")
	otherPath := filepath.Join(root, "other.db")
	if err := os.WriteFile(mainPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mainInfo, err := os.Lstat(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Lstat(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		before [4]os.FileInfo
		after  [4]os.FileInfo
		want   bool
	}{
		{name: "missing before main", after: [4]os.FileInfo{mainInfo}},
		{name: "missing after main", before: [4]os.FileInfo{mainInfo}},
		{name: "different main", before: [4]os.FileInfo{mainInfo}, after: [4]os.FileInfo{otherInfo}},
		{
			name: "wal and rollback journal", before: [4]os.FileInfo{mainInfo},
			after: [4]os.FileInfo{mainInfo, otherInfo, nil, otherInfo},
		},
		{
			name: "shm without wal", before: [4]os.FileInfo{mainInfo},
			after: [4]os.FileInfo{mainInfo, nil, otherInfo},
		},
		{
			name: "new rollback journal", before: [4]os.FileInfo{mainInfo},
			after: [4]os.FileInfo{mainInfo, nil, nil, otherInfo},
		},
		{name: "stable main", before: [4]os.FileInfo{mainInfo}, after: [4]os.FileInfo{mainInfo}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := safeProviderGenerationTransition(test.before, test.after); got != test.want {
				t.Fatalf("safeProviderGenerationTransition() = %t, want %t", got, test.want)
			}
		})
	}

	systemOps := systemProviderOpenOps()
	if info, err := systemOps.mainIdentity(root); err == nil || info != nil {
		t.Fatalf("directory main identity = %#v, %v", info, err)
	}
}

func TestCoverageProviderCancellationAndBusyBranches(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	memory, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = memory.Close() })
	if err := Configure(canceled, memory, time.Second, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("Configure canceled error = %v", err)
	}
	if _, err := EnableWAL(canceled, memory, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("EnableWAL canceled error = %v", err)
	}
	if err := ConfigureOffline(canceled, memory, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("ConfigureOffline canceled error = %v", err)
	}
	closed := openProviderScript(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureOffline(t.Context(), closed, time.Second); err == nil {
		t.Fatal("ConfigureOffline accepted a closed pool")
	}

	path := filepath.Join(t.TempDir(), "busy.db")
	owner, err := OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	owner.SetMaxOpenConns(1)
	owner.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = owner.Close() })
	if err := ConfigureOffline(t.Context(), owner, time.Second); err != nil {
		t.Fatal(err)
	}
	connection, err := owner.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if _, err := connection.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = connection.ExecContext(context.Background(), "ROLLBACK") })
	dsn, err := DSN(path, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	contender, err := open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = contender.Close() })
	var journal string
	busyErr := contender.QueryRowContext(t.Context(), "PRAGMA journal_mode = WAL").Scan(&journal)
	if busyErr == nil || !IsBusyOrLocked(busyErr) {
		t.Fatalf("contended journal error = %v", busyErr)
	}
	steps := make([]providerScriptStep, 16)
	for index := range steps {
		steps[index] = providerScriptStep{query: "journal_mode", err: busyErr}
	}
	if _, err := EnableWAL(
		t.Context(), openProviderScript(t, steps...), 90*time.Millisecond,
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded WAL retry error = %v", err)
	}
	if IsBusyOrLocked(errors.New("ordinary error")) {
		t.Fatal("ordinary error classified as SQLite contention")
	}
	if _, err := memory.ExecContext(t.Context(), "invalid SQL statement"); err == nil || IsBusyOrLocked(err) {
		t.Fatalf("SQLite syntax error contention classification = %v", err)
	}
}

func TestCoverageProviderConcurrentPreparationAndLockAccounting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	identityPath := filepath.Join(root, "identity.db")
	if err := os.WriteFile(identityPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	faultFile := &providerFaultFile{info: fileInfo}
	filesystem := systemProviderFilesystem()
	filesystem.validateSyntax = func(string) error { return nil }
	filesystem.validateAncestors = func(string) error { return nil }
	filesystem.mkdirAll = func(string, os.FileMode) error { return nil }
	pathCalls := 0
	filesystem.lstat = func(candidate string) (os.FileInfo, error) {
		switch candidate {
		case root:
			return directoryInfo, nil
		case path:
			pathCalls++
			if pathCalls == 1 {
				return nil, os.ErrNotExist
			}
			return fileInfo, nil
		default:
			return nil, os.ErrNotExist
		}
	}
	openCalls := 0
	filesystem.openFile = func(string, int, os.FileMode) (providerFile, error) {
		openCalls++
		if openCalls == 1 {
			return nil, os.ErrExist
		}
		return faultFile, nil
	}
	filesystem.secureDirectory = func(string) error { return nil }
	filesystem.secureFile = func(string) error { return nil }
	filesystem.syncDirectory = func(string) error { return nil }
	filesystem.linkCount = func(string, os.FileInfo) generationLinkClass { return generationLinkSingle }
	filesystem.owner = func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent }
	if err := prepareStore(path, filesystem); err != nil {
		t.Fatalf("concurrent creator recovery = %v", err)
	}
	if openCalls != 1 {
		t.Fatalf("open calls = %d, want one exclusive-create attempt and no existing-file reopen", openCalls)
	}

	lockPath := filepath.Join(root, "serialized.db")
	releaseFirst, err := acquireProviderOpenLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	second := make(chan func(), 1)
	secondErr := make(chan error, 1)
	go func() {
		release, acquireErr := acquireProviderOpenLock(lockPath)
		if acquireErr != nil {
			secondErr <- acquireErr
			return
		}
		second <- release
	}()
	key, err := inspectedPoolKey(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		providerOpenLocks.Lock()
		refs := 0
		if lock := providerOpenLocks.values[key]; lock != nil {
			refs = lock.refs
		}
		providerOpenLocks.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			releaseFirst()
			t.Fatal("second provider open did not join path lock")
		}
		runtime.Gosched()
	}
	releaseFirst()
	select {
	case err := <-secondErr:
		t.Fatal(err)
	case releaseSecond := <-second:
		releaseSecond()
	case <-time.After(time.Second):
		t.Fatal("second provider open stayed blocked")
	}
}

func TestCoverageProviderConcurrentCreatorRevalidationFailures(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	identityPath := filepath.Join(root, "identity.db")
	if err := os.WriteFile(identityPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	base := func() providerFilesystem {
		filesystem := systemProviderFilesystem()
		filesystem.validateSyntax = func(string) error { return nil }
		filesystem.validateAncestors = func(string) error { return nil }
		filesystem.mkdirAll = func(string, os.FileMode) error { return nil }
		filesystem.secureDirectory = func(string) error { return nil }
		filesystem.secureFile = func(string) error { return nil }
		filesystem.syncDirectory = func(string) error { return nil }
		filesystem.linkCount = func(string, os.FileInfo) generationLinkClass { return generationLinkSingle }
		filesystem.owner = func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent }
		filesystem.openFile = func(string, int, os.FileMode) (providerFile, error) {
			return nil, os.ErrExist
		}
		return filesystem
	}
	for _, test := range []struct {
		name  string
		lstat func(string) (os.FileInfo, error)
	}{
		{
			name: "unsafe replacement",
			lstat: func() func(string) (os.FileInfo, error) {
				pathCalls := 0
				return func(candidate string) (os.FileInfo, error) {
					if candidate == root {
						return directoryInfo, nil
					}
					if candidate == path {
						pathCalls++
						if pathCalls == 1 {
							return nil, os.ErrNotExist
						}
						if pathCalls >= 4 {
							return directoryInfo, nil
						}
						return fileInfo, nil
					}
					return nil, os.ErrNotExist
				}
			}(),
		},
		{
			name: "unsafe sidecar",
			lstat: func() func(string) (os.FileInfo, error) {
				pathCalls := 0
				return func(candidate string) (os.FileInfo, error) {
					if candidate == root {
						return directoryInfo, nil
					}
					if candidate == path {
						pathCalls++
						if pathCalls == 1 {
							return nil, os.ErrNotExist
						}
						return fileInfo, nil
					}
					if candidate == path+"-wal" && pathCalls >= 3 {
						return directoryInfo, nil
					}
					return nil, os.ErrNotExist
				}
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			filesystem := base()
			filesystem.lstat = test.lstat
			if err := prepareStore(path, filesystem); err == nil {
				t.Fatal("concurrent creator revalidation succeeded")
			}
		})
	}
}

func TestCoverageProviderGenerationIdentityAndCoherenceErrors(t *testing.T) {
	root := t.TempDir()
	mainPath := filepath.Join(root, "main.db")
	otherPath := filepath.Join(root, "other.db")
	if err := os.WriteFile(mainPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mainInfo, err := os.Lstat(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	otherInfo, err := os.Lstat(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inspectedGenerationIdentity(filepath.Join(root, "missing.db"), nil); err == nil {
		t.Fatal("missing inspected generation succeeded")
	}
	if _, err := inspectedGenerationIdentity(mainPath, otherInfo); err == nil {
		t.Fatal("mismatched inspected main succeeded")
	}

	filesystem := systemProviderFilesystem()
	mainCalls := 0
	filesystem.lstat = func(candidate string) (os.FileInfo, error) {
		switch candidate {
		case mainPath:
			mainCalls++
			if mainCalls >= 3 {
				return os.Lstat(root)
			}
			return mainInfo, nil
		case mainPath + "-wal":
			return otherInfo, nil
		default:
			return nil, os.ErrNotExist
		}
	}
	filesystem.secureFile = func(string) error { return nil }
	filesystem.linkCount = func(string, os.FileInfo) generationLinkClass { return generationLinkSingle }
	filesystem.owner = func(string, os.FileInfo) generationOwnerClass { return generationOwnerCurrent }
	if err := validateGenerationMembersWithFilesystem(mainPath, true, filesystem); err == nil {
		t.Fatal("unsafe main identity beside sidecar succeeded")
	}
}

func TestCoverageProviderRemainingInputBranches(t *testing.T) {
	longComponent := strings.Repeat("x", 256)
	if validProviderFilesystemPath(filepath.Join(t.TempDir(), longComponent)) {
		t.Fatal("overlong filesystem component accepted")
	}
	if err := PrepareStore(filepath.Join(t.TempDir(), longComponent)); err == nil {
		t.Fatal("overlong store component prepared")
	}
}
