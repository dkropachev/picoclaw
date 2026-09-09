package sqliteprovider

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

var inspectedPools = struct {
	sync.Mutex
	values map[string]inspectedPool
}{values: make(map[string]inspectedPool)}

type inspectedGenerationMember struct {
	identity fileidentity.Identity
	present  bool
}

type inspectedGeneration struct {
	members [4]inspectedGenerationMember
}

type inspectedPool struct {
	database    *sql.DB
	generation  inspectedGeneration
	busyTimeout time.Duration
	owners      map[*atomic.Bool]struct{}
}

type inspectionIdentityLookup func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)

func canonicalInspectionLocation(path string) (string, string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", err
	}
	key, err := inspectedPoolKey(absolute)
	if err != nil {
		return "", "", err
	}
	return absolute, key, nil
}

func captureInspectedGeneration(path string) (inspectedGeneration, error) {
	return captureInspectedGenerationWith(path, fileidentity.ExistingWithType)
}

func captureInspectedGenerationWith(
	path string,
	lookup inspectionIdentityLookup,
) (inspectedGeneration, error) {
	if lookup == nil {
		return inspectedGeneration{}, errors.New("SQLite inspected identity lookup is unavailable")
	}
	var result inspectedGeneration
	for index, member := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		identity, objectType, exists, err := lookup(member)
		if err != nil {
			if errors.Is(err, fileidentity.ErrUnsafeType) {
				return inspectedGeneration{}, errors.Join(
					errInspectionIntegrity,
					errors.New("SQLite inspected generation member is unsafe"),
					err,
				)
			}
			return inspectedGeneration{}, err
		}
		if !exists {
			if index == 0 {
				return inspectedGeneration{}, errors.Join(
					errInspectionIntegrity,
					errors.New("SQLite inspected main generation disappeared"),
				)
			}
			continue
		}
		if !identity.Valid() || objectType != fileidentity.ObjectTypeRegular {
			return inspectedGeneration{}, errors.Join(
				errInspectionIntegrity,
				errors.New("SQLite inspected generation member is unsafe"),
			)
		}
		result.members[index] = inspectedGenerationMember{identity: identity, present: true}
	}
	return result, nil
}

func retainInspectedPool(
	key string,
	database *sql.DB,
	generation inspectedGeneration,
	busyTimeout time.Duration,
	owner *atomic.Bool,
) (*sql.DB, error) {
	if key == "" || database == nil || owner == nil || !generation.members[0].present {
		return nil, errors.New("SQLite inspected pool is unavailable")
	}
	inspectedPools.Lock()
	if existing := inspectedPools.values[key]; existing.database != nil {
		if !sameInspectedGeneration(existing.generation, generation) {
			inspectedPools.Unlock()
			closeErr := database.Close()
			if closeErr != nil {
				return nil, errors.Join(
					errors.New("SQLite inspected generation changed"),
					errInspectionInfrastructure,
					closeErr,
				)
			}
			return nil, errors.New("SQLite inspected generation changed")
		}
		if existing.busyTimeout != busyTimeout {
			inspectedPools.Unlock()
			closeErr := database.Close()
			if closeErr != nil {
				return nil, errors.Join(
					errors.New("SQLite inspected pool configuration changed"),
					errInspectionInfrastructure,
					closeErr,
				)
			}
			return nil, errors.New("SQLite inspected pool configuration changed")
		}
		if closeErr := database.Close(); closeErr != nil {
			inspectedPools.Unlock()
			return nil, errors.Join(
				errors.New("SQLite inspected candidate pool could not be closed"),
				errInspectionInfrastructure,
				closeErr,
			)
		}
		if existing.owners == nil {
			existing.owners = make(map[*atomic.Bool]struct{})
		}
		existing.owners[owner] = struct{}{}
		inspectedPools.values[key] = existing
		inspectedPools.Unlock()
		return existing.database, nil
	}
	inspectedPools.values[key] = inspectedPool{
		database: database, busyTimeout: busyTimeout, generation: generation,
		owners: map[*atomic.Bool]struct{}{owner: {}},
	}
	inspectedPools.Unlock()
	return database, nil
}

func inspectedPoolFor(
	key string,
	generation inspectedGeneration,
	busyTimeout time.Duration,
	owner *atomic.Bool,
) (*sql.DB, error) {
	if key == "" || owner == nil || !generation.members[0].present {
		return nil, errors.New("SQLite inspected pool owner is unavailable")
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	if entry.database == nil {
		inspectedPools.Unlock()
		return nil, nil
	}
	if !sameInspectedGeneration(entry.generation, generation) {
		inspectedPools.Unlock()
		return nil, errors.New("SQLite inspected generation changed")
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

func adoptInspectedPool(path, key string, owner *atomic.Bool) (*sql.DB, error) {
	return adoptInspectedPoolWithGeneration(
		path, key, owner, EnsurePrivateDirectory, SecureGeneration, captureInspectedGeneration,
	)
}

func adoptInspectedPoolWithGeneration(
	path string,
	key string,
	owner *atomic.Bool,
	ensureDirectory func(string) error,
	secureGeneration func(string) error,
	capture func(string) (inspectedGeneration, error),
) (*sql.DB, error) {
	if path == "" || key == "" || capture == nil || ensureDirectory == nil || secureGeneration == nil {
		return nil, errors.New("SQLite inspected pool adoption operations are unavailable")
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	if entry.database == nil {
		inspectedPools.Unlock()
		return nil, errors.New("SQLite inspected pool is unavailable")
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
	if err := ensureDirectory(filepath.Dir(path)); err != nil {
		inspectedPools.Unlock()
		return nil, err
	}
	if err := secureGeneration(path); err != nil {
		inspectedPools.Unlock()
		return nil, err
	}
	firstGeneration, firstIdentityErr := capture(path)
	if firstIdentityErr != nil ||
		!sameInspectedGeneration(entry.generation, firstGeneration) {
		inspectedPools.Unlock()
		return nil, errors.Join(
			errors.New("SQLite inspected generation changed before adoption"),
			firstIdentityErr,
		)
	}
	secondGeneration, secondIdentityErr := capture(path)
	if secondIdentityErr != nil ||
		!sameInspectedGeneration(entry.generation, secondGeneration) ||
		!sameInspectedGeneration(firstGeneration, secondGeneration) {
		inspectedPools.Unlock()
		return nil, errors.Join(
			errors.New("SQLite inspected generation changed before adoption"),
			secondIdentityErr,
		)
	}
	if !owner.CompareAndSwap(false, true) {
		inspectedPools.Unlock()
		return nil, errors.New("SQLite inspected pool owner changed during adoption")
	}
	delete(inspectedPools.values, key)
	inspectedPools.Unlock()
	return entry.database, nil
}

func sameInspectedGeneration(left, right inspectedGeneration) bool {
	return left == right
}

func sameInspectedMain(left, right inspectedGeneration) bool {
	return left.members[0].present && right.members[0].present &&
		left.members[0] == right.members[0]
}

func releaseInspectedPool(key string, expected *sql.DB, owner *atomic.Bool) error {
	if key == "" || owner == nil {
		return nil
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
