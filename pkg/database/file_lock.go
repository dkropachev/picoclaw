package database

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

var errFileLockBusy = errors.New("database storage lock is busy")

// Fence holds a process lifetime storage-root fence. Closing it releases the
// operating-system lock; the lock file itself intentionally remains in place.
type Fence struct {
	file      *os.File
	online    bool
	migration bool
	once      sync.Once
}

var (
	onlineFenceCount    atomic.Int64
	migrationFenceCount atomic.Int64
)

// AcquireOnlineFence obtains the nonblocking shared lifetime fence used by the
// online broker. An offline migrator holding the exclusive fence makes this
// operation fail closed.
func AcquireOnlineFence(home string) (*Fence, error) {
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		return nil, err
	}
	file, err := acquirePlatformFileLock(filepath.Join(stateDir, storageLockFileName), true)
	if errors.Is(err, errFileLockBusy) {
		return nil, NewError(CodeConflict, "storage root is exclusively fenced")
	}
	if err != nil {
		return nil, err
	}
	onlineFenceCount.Add(1)
	return &Fence{file: file, online: true}, nil
}

// AcquireMigrationFence obtains the nonblocking exclusive offline migration
// fence. It refuses while any online broker or another migrator holds the root.
func AcquireMigrationFence(home string) (*Fence, error) {
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		return nil, err
	}
	file, err := acquirePlatformFileLock(filepath.Join(stateDir, storageLockFileName), false)
	if errors.Is(err, errFileLockBusy) {
		return nil, NewError(CodeConflict, "storage root is in use")
	}
	if err != nil {
		return nil, err
	}
	migrationFenceCount.Add(1)
	return &Fence{file: file, migration: true}, nil
}

// Close releases the fence. It is safe to call more than once.
func (fence *Fence) Close() error {
	if fence == nil {
		return nil
	}
	var closeErr error
	fence.once.Do(func() {
		closeErr = releasePlatformFileLock(fence.file)
		fence.file = nil
		if fence.online {
			onlineFenceCount.Add(-1)
			fence.online = false
		}
		if fence.migration {
			migrationFenceCount.Add(-1)
			fence.migration = false
		}
	})
	return closeErr
}

// OnlineFenceHeld reports whether this process is operating under an online
// storage-root fence. Store openers use it to forbid implicit upgrades/imports.
func OnlineFenceHeld() bool { return onlineFenceCount.Load() > 0 }

// MigrationFenceHeld reports whether this process owns an exclusive offline
// migration fence. Provider adapters use it to keep actual schema work in an
// exclusive rollback-journal connection rather than switching back to WAL.
func MigrationFenceHeld() bool { return migrationFenceCount.Load() > 0 }

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
