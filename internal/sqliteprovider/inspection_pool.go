package sqliteprovider

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var inspectedPools = struct {
	sync.Mutex
	values map[string]inspectedPool
}{values: make(map[string]inspectedPool)}

type inspectedPool struct {
	database *sql.DB
	main     os.FileInfo
}

func inspectedPoolKey(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		absolute = strings.ToLower(absolute)
	}
	return absolute, nil
}

func retainInspectedPool(path string, database *sql.DB, main os.FileInfo) (*sql.DB, error) {
	if database == nil {
		return nil, errors.New("SQLite inspected pool is unavailable")
	}
	key, err := inspectedPoolKey(path)
	if err != nil {
		return nil, err
	}
	if main == nil || !main.Mode().IsRegular() || main.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("SQLite inspected generation identity is invalid")
	}
	inspectedPools.Lock()
	if existing := inspectedPools.values[key]; existing.database != nil {
		inspectedPools.Unlock()
		_ = database.Close()
		if existing.main == nil || !os.SameFile(existing.main, main) {
			return nil, errors.New("SQLite inspected generation changed")
		}
		return existing.database, nil
	}
	inspectedPools.values[key] = inspectedPool{database: database, main: main}
	inspectedPools.Unlock()
	return database, nil
}

func inspectedPoolFor(path string, main os.FileInfo) (*sql.DB, error) {
	key, err := inspectedPoolKey(path)
	if err != nil {
		return nil, err
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	inspectedPools.Unlock()
	if entry.database == nil {
		return nil, nil
	}
	if main == nil || entry.main == nil || !os.SameFile(entry.main, main) {
		return nil, errors.New("SQLite inspected generation changed")
	}
	return entry.database, nil
}

func adoptInspectedPool(path string) (*sql.DB, error) {
	key, err := inspectedPoolKey(path)
	if err != nil {
		return nil, err
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	if entry.database != nil {
		delete(inspectedPools.values, key)
	}
	inspectedPools.Unlock()
	if entry.database == nil {
		return nil, nil
	}
	current, statErr := os.Lstat(path)
	if statErr != nil || entry.main == nil || current == nil ||
		!current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(entry.main, current) {
		_ = entry.database.Close()
		return nil, errors.Join(errors.New("SQLite inspected generation changed before adoption"), statErr)
	}
	if err := EnsurePrivateDirectory(filepath.Dir(path)); err != nil {
		_ = entry.database.Close()
		return nil, err
	}
	if err := SecureGeneration(path); err != nil {
		_ = entry.database.Close()
		return nil, err
	}
	return entry.database, nil
}

func releaseInspectedPool(path string, expected *sql.DB) error {
	key, err := inspectedPoolKey(path)
	if err != nil {
		return err
	}
	inspectedPools.Lock()
	entry := inspectedPools.values[key]
	if entry.database == nil || entry.database != expected {
		inspectedPools.Unlock()
		return nil
	}
	delete(inspectedPools.values, key)
	inspectedPools.Unlock()
	return entry.database.Close()
}

// CloseInspectedPools closes readiness pools that no domain handler consumed.
// Callers pass only paths obtained from the trusted provider catalog.
func CloseInspectedPools(paths []string) error {
	var result error
	for _, path := range paths {
		key, err := inspectedPoolKey(path)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		inspectedPools.Lock()
		entry := inspectedPools.values[key]
		delete(inspectedPools.values, key)
		inspectedPools.Unlock()
		if entry.database != nil {
			result = errors.Join(result, entry.database.Close())
		}
	}
	return result
}
