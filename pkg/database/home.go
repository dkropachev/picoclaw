//nolint:govet // Validation stages intentionally use narrow error scopes.
package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// StateDirectoryName is the owner-only infrastructure directory beneath a
	// canonical PicoClaw home.
	StateDirectoryName  = ".database"
	manifestFileName    = "broker.json"
	brokerLockFileName  = "broker.lock"
	storageLockFileName = "storage.lock"
	socketFileName      = "broker.sock"
)

// CanonicalHome resolves an existing home and rejects symlinked physical
// aliases. The returned value is absolute and clean.
func CanonicalHome(path string) (string, error) {
	return canonicalHome(path, false)
}

// PrepareHome securely creates a missing home, then applies CanonicalHome's
// alias checks. Existing directories are never chmodded by this function.
func PrepareHome(path string) (string, error) {
	return canonicalHome(path, true)
}

// StateDirectory returns the owner-only broker directory for an existing
// canonical home. It does not create filesystem state.
func StateDirectory(home string) (string, error) {
	canonical, err := CanonicalHome(home)
	if err != nil {
		return "", err
	}
	stateDir := filepath.Join(canonical, StateDirectoryName)
	if err := validateStateDirectory(stateDir); err != nil {
		return "", err
	}
	return stateDir, nil
}

func canonicalHome(path string, create bool) (string, error) {
	if path == "" || path != strings.TrimSpace(path) || strings.IndexByte(path, 0) >= 0 {
		return "", NewError(CodeInvalid, "PicoClaw home is invalid")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve PicoClaw home: %w", err)
	}
	absolute = filepath.Clean(absolute)

	info, statErr := os.Lstat(absolute)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect PicoClaw home: %w", statErr)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if !create {
			return "", NewError(CodeUnavailable, "PicoClaw home does not exist")
		}
		if err := rejectExistingAncestorAlias(absolute); err != nil {
			return "", err
		}
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return "", fmt.Errorf("create PicoClaw home: %w", err)
		}
		if err := os.Chmod(absolute, 0o700); err != nil {
			return "", fmt.Errorf("secure PicoClaw home: %w", err)
		}
		info, statErr = os.Lstat(absolute)
	}
	if statErr != nil {
		return "", fmt.Errorf("inspect PicoClaw home: %w", statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", NewError(CodeInvalid, "PicoClaw home must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("canonicalize PicoClaw home: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("canonicalize PicoClaw home: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if !sameCanonicalPath(absolute, resolved) {
		return "", NewError(CodeInvalid, "symlinked PicoClaw home aliases are not allowed")
	}
	return absolute, nil
}

func rejectExistingAncestorAlias(path string) error {
	ancestor := path
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect PicoClaw home ancestor: %w", err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return NewError(CodeInvalid, "PicoClaw home has no existing filesystem ancestor")
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return fmt.Errorf("canonicalize PicoClaw home ancestor: %w", err)
	}
	absAncestor, err := filepath.Abs(ancestor)
	if err != nil {
		return fmt.Errorf("resolve PicoClaw home ancestor: %w", err)
	}
	absResolved, err := filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolve PicoClaw home ancestor: %w", err)
	}
	if !sameCanonicalPath(filepath.Clean(absAncestor), filepath.Clean(absResolved)) {
		return NewError(CodeInvalid, "symlinked PicoClaw home aliases are not allowed")
	}
	return nil
}

func prepareStateDirectory(home string) (string, error) {
	canonical, err := PrepareHome(home)
	if err != nil {
		return "", err
	}
	stateDir := filepath.Join(canonical, StateDirectoryName)
	info, err := os.Lstat(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		if err = createOwnerOnlyDirectory(stateDir); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create database state directory: %w", err)
		}
		info, err = os.Lstat(stateDir)
		if err != nil {
			return "", fmt.Errorf("secure database state directory: %w", err)
		}
	}
	if err != nil {
		return "", fmt.Errorf("inspect database state directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", NewError(CodeIntegrity, "database state boundary is not a real directory")
	}
	if err := validateOwnerOnlyDirectory(stateDir, info); err != nil {
		return "", err
	}
	return stateDir, nil
}

func validateStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewError(CodeUnavailable, "database broker state is unavailable")
		}
		return fmt.Errorf("inspect database state directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return NewError(CodeIntegrity, "database state boundary is not a real directory")
	}
	return validateOwnerOnlyDirectory(path, info)
}
