package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

var errFileLockBusy = errors.New("database storage lock is busy")

// Fence holds a process lifetime storage-root fence. Closing it releases the
// operating-system lock; the lock file itself intentionally remains in place.
type Fence struct {
	mu        sync.RWMutex
	file      *os.File
	home      string
	homeInfo  os.FileInfo
	stateInfo os.FileInfo
	lockPath  string
	lockInfo  os.FileInfo
	migration bool
	once      sync.Once
	closed    atomic.Bool
}

type migrationContextKey struct{}

type migrationContextAuthority struct {
	fence  *Fence
	target string
}

// AcquireOnlineFence obtains the nonblocking shared lifetime fence used by the
// online broker. An offline migrator holding the exclusive fence makes this
// operation fail closed.
func AcquireOnlineFence(home string) (*Fence, error) {
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		return nil, err
	}
	homeInfo, stateInfo, err := fenceBoundaryIdentity(stateDir)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(stateDir, storageLockFileName)
	file, err := acquirePlatformFileLock(lockPath, true)
	if errors.Is(err, errFileLockBusy) {
		return nil, NewError(CodeConflict, "storage root is exclusively fenced")
	}
	if err != nil {
		return nil, err
	}
	currentHome, currentState, identityErr := fenceBoundaryIdentity(stateDir)
	if identityErr != nil || !os.SameFile(homeInfo, currentHome) || !os.SameFile(stateInfo, currentState) {
		_ = releasePlatformFileLock(file)
		return nil, errors.Join(NewError(CodeIntegrity, "storage fence boundary changed"), identityErr)
	}
	lockInfo, identityErr := fenceLockIdentity(file, lockPath)
	if identityErr != nil {
		_ = releasePlatformFileLock(file)
		return nil, identityErr
	}
	return &Fence{
		file: file, home: filepath.Dir(stateDir), homeInfo: currentHome, stateInfo: currentState,
		lockPath: lockPath, lockInfo: lockInfo,
	}, nil
}

// AcquireMigrationFence obtains the nonblocking exclusive offline migration
// fence. It refuses while any online broker or another migrator holds the root.
func AcquireMigrationFence(home string) (*Fence, error) {
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		return nil, err
	}
	homeInfo, stateInfo, err := fenceBoundaryIdentity(stateDir)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(stateDir, storageLockFileName)
	file, err := acquirePlatformFileLock(lockPath, false)
	if errors.Is(err, errFileLockBusy) {
		return nil, NewError(CodeConflict, "storage root is in use")
	}
	if err != nil {
		return nil, err
	}
	currentHome, currentState, identityErr := fenceBoundaryIdentity(stateDir)
	if identityErr != nil || !os.SameFile(homeInfo, currentHome) || !os.SameFile(stateInfo, currentState) {
		_ = releasePlatformFileLock(file)
		return nil, errors.Join(NewError(CodeIntegrity, "storage fence boundary changed"), identityErr)
	}
	lockInfo, identityErr := fenceLockIdentity(file, lockPath)
	if identityErr != nil {
		_ = releasePlatformFileLock(file)
		return nil, identityErr
	}
	return &Fence{
		file: file, home: filepath.Dir(stateDir), homeInfo: currentHome,
		stateInfo: currentState, lockPath: lockPath, lockInfo: lockInfo, migration: true,
	}, nil
}

// Authorizes reports whether this live fence owns the exact canonical home.
// Physical claims retain the fence and recheck it before exposing catalog data.
func (fence *Fence) Authorizes(home string) bool {
	if fence == nil {
		return false
	}
	fence.mu.RLock()
	defer fence.mu.RUnlock()
	return fence.authorizesLocked(home)
}

func (fence *Fence) authorizesLocked(home string) bool {
	if fence == nil || fence.closed.Load() || fence.home == "" {
		return false
	}
	canonical, err := CanonicalHome(home)
	if err != nil || !sameCanonicalPath(fence.home, canonical) {
		return false
	}
	currentHome, currentState, err := fenceBoundaryIdentity(
		filepath.Join(canonical, StateDirectoryName),
	)
	_, lockErr := fenceLockIdentity(fence.file, fence.lockPath)
	currentLock, currentLockErr := os.Lstat(fence.lockPath)
	return err == nil && lockErr == nil && currentLockErr == nil &&
		os.SameFile(fence.homeInfo, currentHome) && os.SameFile(fence.stateInfo, currentState) &&
		os.SameFile(fence.lockInfo, currentLock) &&
		validateOwnerOnlyDirectory(filepath.Join(canonical, StateDirectoryName), currentState) == nil &&
		validateOwnerOnlyFile(fence.lockPath, currentLock, 0o600) == nil && !fence.closed.Load()
}

// Guard keeps the exact fence boundary live across a short ownership transfer.
// The returned release function must be called exactly once when err is nil.
func (fence *Fence) Guard(home string) (func(), error) {
	if fence == nil {
		return nil, NewError(CodeUnavailable, "storage fence is unavailable")
	}
	fence.mu.RLock()
	if !fence.authorizesLocked(home) {
		fence.mu.RUnlock()
		return nil, NewError(CodeIntegrity, "storage fence lost authority")
	}
	return fence.mu.RUnlock, nil
}

// GuardMigration keeps the exact exclusive migration fence live across a
// short ownership transfer. Shared online fences are never migration
// capabilities.
func (fence *Fence) GuardMigration(home string) (func(), error) {
	if fence == nil {
		return nil, NewError(CodeUnavailable, "storage migration fence is unavailable")
	}
	fence.mu.RLock()
	if !fence.migration || !fence.authorizesLocked(home) {
		fence.mu.RUnlock()
		return nil, NewError(CodeUnauthorized, "exclusive storage migration fence is required")
	}
	return fence.mu.RUnlock, nil
}

func fenceLockIdentity(file *os.File, path string) (os.FileInfo, error) {
	if file == nil {
		return nil, NewError(CodeIntegrity, "storage fence lock handle is unavailable")
	}
	opened, statErr := file.Stat()
	current, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || opened == nil || current == nil ||
		!opened.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) {
		return nil, errors.Join(
			NewError(CodeIntegrity, "storage fence lock identity changed"), statErr, lstatErr,
		)
	}
	return opened, nil
}

func fenceBoundaryIdentity(stateDir string) (os.FileInfo, os.FileInfo, error) {
	homeInfo, homeErr := os.Lstat(filepath.Dir(stateDir))
	stateInfo, stateErr := os.Lstat(stateDir)
	if homeErr != nil || stateErr != nil || homeInfo == nil || stateInfo == nil ||
		!homeInfo.IsDir() || !stateInfo.IsDir() || homeInfo.Mode()&os.ModeSymlink != 0 ||
		stateInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.Join(
			NewError(CodeIntegrity, "storage fence boundary identity is unavailable"),
			homeErr,
			stateErr,
		)
	}
	return homeInfo, stateInfo, nil
}

// MigrationContext binds this live exclusive fence to one exact provider
// target. Storage compatibility code uses the resulting context to select its
// offline transaction mode without affecting unrelated in-process stores.
func (fence *Fence) MigrationContext(ctx context.Context, target string) (context.Context, error) {
	if fence == nil || fence.closed.Load() || !fence.migration || !fence.Authorizes(fence.home) {
		return nil, NewError(CodeConflict, "migration fence is unavailable")
	}
	canonical, err := canonicalMigrationTarget(target)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, migrationContextKey{}, migrationContextAuthority{
		fence: fence, target: canonical,
	}), nil
}

// MigrationContextAuthorizes reports whether ctx carries a live exclusive
// fence capability for exactly target.
func MigrationContextAuthorizes(ctx context.Context, target string) bool {
	authority, ok := migrationContext(ctx)
	if !ok {
		return false
	}
	canonical, err := canonicalMigrationTarget(target)
	return err == nil && sameCanonicalPath(authority.target, canonical) &&
		!authority.fence.closed.Load()
}

// MigrationContextActive reports whether ctx carries any live exact-target
// migration capability. It is used only after Open verified the target.
func MigrationContextActive(ctx context.Context) bool {
	_, ok := migrationContext(ctx)
	return ok
}

// MigrationContextPresent reports whether ctx carries a migration capability,
// including one that is expired or for another target. Callers use this to
// reject invalid authority instead of silently falling back to online mode.
func MigrationContextPresent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(migrationContextKey{}).(migrationContextAuthority)
	return ok
}

func migrationContext(ctx context.Context) (migrationContextAuthority, bool) {
	if ctx == nil {
		return migrationContextAuthority{}, false
	}
	authority, ok := ctx.Value(migrationContextKey{}).(migrationContextAuthority)
	if !ok || authority.fence == nil || authority.fence.closed.Load() ||
		!authority.fence.migration || authority.target == "" ||
		!authority.fence.Authorizes(authority.fence.home) {
		return migrationContextAuthority{}, false
	}
	return authority, true
}

func canonicalMigrationTarget(target string) (string, error) {
	if target == "" || target != strings.TrimSpace(target) ||
		strings.ContainsRune(target, 0) || strings.HasPrefix(strings.ToLower(target), "file:") ||
		target == ":memory:" {
		return "", NewError(CodeInvalid, "migration target is invalid")
	}
	absolute, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", NewError(CodeInvalid, "migration target is invalid")
	}
	return absolute, nil
}

// Close releases the fence. It is safe to call more than once.
func (fence *Fence) Close() error {
	if fence == nil {
		return nil
	}
	var closeErr error
	fence.once.Do(func() {
		fence.mu.Lock()
		defer fence.mu.Unlock()
		fence.closed.Store(true)
		closeErr = releasePlatformFileLock(fence.file)
	})
	return closeErr
}

type singletonLock struct {
	file *os.File
}

func acquireBrokerSingleton(stateDir string) (*singletonLock, error) {
	file, err := acquirePlatformFileLock(filepath.Join(stateDir, brokerLockFileName), false)
	if errors.Is(err, errFileLockBusy) {
		return nil, NewError(CodeAlreadyExists, "database broker is already running for this home")
	}
	if err != nil {
		return nil, err
	}
	return &singletonLock{file: file}, nil
}

func (lock *singletonLock) close() error {
	if lock == nil {
		return nil
	}
	err := releasePlatformFileLock(lock.file)
	lock.file = nil
	return err
}
