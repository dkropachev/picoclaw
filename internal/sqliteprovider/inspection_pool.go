package sqliteprovider

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var inspectedPools = struct {
	sync.Mutex
	values map[string]inspectedPool
}{values: make(map[string]inspectedPool)}

type inspectedPool struct {
	database    *sql.DB
	main        os.FileInfo
	generation  [4]os.FileInfo
	busyTimeout time.Duration
	owners      map[*atomic.Bool]struct{}
}

func retainInspectedPool(
	path string,
	database *sql.DB,
	main os.FileInfo,
	busyTimeout time.Duration,
	owner *atomic.Bool,
) (*sql.DB, error) {
	if database == nil || owner == nil {
		return nil, errors.New("SQLite inspected pool is unavailable")
	}
	key, err := inspectedPoolKey(path)
	if err != nil {
		return nil, err
	}
	if main == nil || !main.Mode().IsRegular() || main.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("SQLite inspected generation identity is invalid")
	}
	generation, err := inspectedGenerationIdentity(path, main)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	inspectedPools.Lock()
	if existing := inspectedPools.values[key]; existing.database != nil {
		if !sameInspectedGeneration(existing.generation, generation) {
			inspectedPools.Unlock()
			_ = database.Close()
			return nil, errors.New("SQLite inspected generation changed")
		}
		if existing.busyTimeout != busyTimeout {
			inspectedPools.Unlock()
			_ = database.Close()
			return nil, errors.New("SQLite inspected pool configuration changed")
		}
		if existing.owners == nil {
			existing.owners = make(map[*atomic.Bool]struct{})
		}
		existing.owners[owner] = struct{}{}
		inspectedPools.values[key] = existing
		inspectedPools.Unlock()
		_ = database.Close()
		return existing.database, nil
	}
	inspectedPools.values[key] = inspectedPool{
		database: database, main: main, busyTimeout: busyTimeout,
		generation: generation, owners: map[*atomic.Bool]struct{}{owner: {}},
	}
	inspectedPools.Unlock()
	return database, nil
}

func inspectedPoolFor(
	path string,
	main os.FileInfo,
	busyTimeout time.Duration,
	owner *atomic.Bool,
) (*sql.DB, error) {
	if owner == nil {
		return nil, errors.New("SQLite inspected pool owner is unavailable")
	}
	key, err := inspectedPoolKey(path)
	if err != nil {
		return nil, err
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	if entry.database == nil {
		inspectedPools.Unlock()
		return nil, nil
	}
	generation, identityErr := inspectedGenerationIdentity(path, main)
	if identityErr != nil || !sameInspectedGeneration(entry.generation, generation) {
		inspectedPools.Unlock()
		return nil, errors.Join(errors.New("SQLite inspected generation changed"), identityErr)
	}
	if entry.busyTimeout != busyTimeout {
		inspectedPools.Unlock()
		return nil, errors.New("SQLite inspected pool configuration changed")
	}
	if entry.owners == nil {
		entry.owners = make(map[*atomic.Bool]struct{})
	}
	entry.owners[owner] = struct{}{}
	inspectedPools.values[key] = entry
	inspectedPools.Unlock()
	return entry.database, nil
}

func adoptInspectedPool(
	path string,
	busyTimeout time.Duration,
	owner *atomic.Bool,
) (*sql.DB, error) {
	return adoptInspectedPoolWithGeneration(
		path, busyTimeout, owner, os.Lstat, EnsurePrivateDirectory, SecureGeneration,
	)
}

func adoptInspectedPoolWithGeneration(
	path string,
	busyTimeout time.Duration,
	owner *atomic.Bool,
	lstat func(string) (os.FileInfo, error),
	ensureDirectory func(string) error,
	secureGeneration func(string) error,
) (*sql.DB, error) {
	key, err := inspectedPoolKey(path)
	if err != nil {
		return nil, err
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	if entry.database == nil {
		inspectedPools.Unlock()
		return nil, nil
	}
	if owner == nil || owner.Load() {
		inspectedPools.Unlock()
		return nil, errors.New("SQLite inspected pool owner is unavailable")
	}
	if _, owns := entry.owners[owner]; !owns {
		inspectedPools.Unlock()
		return nil, errors.New("SQLite inspected pool owner does not match")
	}
	if len(entry.owners) != 1 {
		inspectedPools.Unlock()
		return nil, errors.New("SQLite inspected pool still has readiness owners")
	}
	if entry.busyTimeout != busyTimeout {
		inspectedPools.Unlock()
		return nil, errors.New("SQLite inspected pool configuration does not match owner")
	}
	current, statErr := lstat(path)
	if statErr != nil || entry.main == nil || current == nil ||
		!current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(entry.main, current) {
		inspectedPools.Unlock()
		return nil, errors.Join(errors.New("SQLite inspected generation changed before adoption"), statErr)
	}
	if err := ensureDirectory(filepath.Dir(path)); err != nil {
		inspectedPools.Unlock()
		return nil, err
	}
	if err := secureGeneration(path); err != nil {
		inspectedPools.Unlock()
		return nil, err
	}
	generation, identityErr := inspectedGenerationIdentity(path, current)
	if identityErr != nil || !sameInspectedGeneration(entry.generation, generation) {
		inspectedPools.Unlock()
		return nil, errors.Join(
			errors.New("SQLite inspected generation changed before adoption"), identityErr,
		)
	}
	for owner := range entry.owners {
		owner.Store(true)
	}
	delete(inspectedPools.values, key)
	inspectedPools.Unlock()
	return entry.database, nil
}

func sameInspectedGeneration(left, right [4]os.FileInfo) bool {
	for index := range left {
		if (left[index] == nil) != (right[index] == nil) {
			return false
		}
		if left[index] != nil && !os.SameFile(left[index], right[index]) {
			return false
		}
	}
	return true
}

func releaseInspectedPool(path string, expected *sql.DB, owner *atomic.Bool) error {
	return releaseInspectedPoolWithKey(path, expected, owner, inspectedPoolKey)
}

func releaseInspectedPoolWithKey(
	path string,
	expected *sql.DB,
	owner *atomic.Bool,
	keyForPath func(string) (string, error),
) error {
	if owner == nil {
		return nil
	}
	key, err := keyForPath(path)
	if err != nil {
		return err
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	if entry.database == nil || entry.database != expected {
		inspectedPools.Unlock()
		return nil
	}
	if _, exists := entry.owners[owner]; !exists {
		inspectedPools.Unlock()
		return nil
	}
	delete(entry.owners, owner)
	if len(entry.owners) > 0 {
		inspectedPools.values[key] = entry
		inspectedPools.Unlock()
		return nil
	}
	delete(inspectedPools.values, key)
	inspectedPools.Unlock()
	return entry.database.Close()
}
