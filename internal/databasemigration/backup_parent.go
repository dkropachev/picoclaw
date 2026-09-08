package databasemigration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func validateBackupParent(
	value,
	canonicalHome string,
	specs []storecatalog.Spec,
) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = filepath.Join(canonicalHome, "backups")
	}
	if value != strings.TrimSpace(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("database backup directory is invalid")
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", err
	}
	for _, spec := range specs {
		for _, generation := range generationPaths(spec.Path) {
			if backupPathsOverlap(absolute, generation) {
				return "", errors.New("database backup directory overlaps a database generation")
			}
		}
		for _, legacyRoot := range spec.LegacyRoots {
			if backupPathsOverlap(absolute, legacyRoot) {
				return "", errors.New("database backup directory overlaps a legacy input")
			}
		}
	}
	if err := validateBackupParentPhysicalAliases(absolute, specs); err != nil {
		return "", err
	}
	return absolute, nil
}

func validateBackupParentPhysicalAliases(parent string, specs []storecatalog.Spec) error {
	parentResolved, parentIdentity, exists, err := existingBackupDirectoryIdentity(parent)
	if err != nil {
		return fmt.Errorf("inspect database backup directory identity: %w", err)
	}
	if !exists {
		return nil
	}

	seen := make(map[string]struct{}, len(specs)*2)
	check := func(path, kind string) error {
		cleaned := filepath.Clean(path)
		key := backupPathKey(cleaned)
		if _, duplicate := seen[key]; duplicate {
			return nil
		}
		seen[key] = struct{}{}

		resolved, identity, inputExists, identityErr := existingBackupDirectoryIdentity(cleaned)
		if identityErr != nil {
			return fmt.Errorf("inspect %s directory identity: %w", kind, identityErr)
		}
		if !inputExists {
			return nil
		}
		if backupPathKey(parentResolved) == backupPathKey(resolved) ||
			parentIdentity.Valid() && identity.Valid() && parentIdentity == identity {
			return fmt.Errorf("database backup directory physically aliases %s", kind)
		}
		return nil
	}

	for _, spec := range specs {
		if err := check(filepath.Dir(spec.Path), "a database generation directory"); err != nil {
			return err
		}
		for _, generation := range generationPaths(spec.Path) {
			if err := check(generation, "a database generation directory"); err != nil {
				return err
			}
		}
		for _, legacyRoot := range spec.LegacyRoots {
			if err := check(legacyRoot, "a legacy input directory"); err != nil {
				return err
			}
		}
	}
	return nil
}

func existingBackupDirectoryIdentity(path string) (
	resolved string,
	identity fileidentity.Identity,
	exists bool,
	err error,
) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fileidentity.Identity{}, false, nil
	}
	if err != nil {
		return "", fileidentity.Identity{}, false, err
	}
	if !info.IsDir() {
		return "", fileidentity.Identity{}, false, nil
	}
	resolved, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fileidentity.Identity{}, false, err
	}
	resolved, err = filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", fileidentity.Identity{}, false, err
	}
	identity, exists, err = fileidentity.Existing(resolved)
	if errors.Is(err, fileidentity.ErrUnsupported) {
		return resolved, fileidentity.Identity{}, true, nil
	}
	if err != nil || !exists {
		return "", fileidentity.Identity{}, false, errors.Join(
			errors.New("physical directory identity is unavailable"), err,
		)
	}
	return resolved, identity, true, nil
}

func backupPathsOverlap(left, right string) bool {
	leftKey := backupPathKey(filepath.Clean(left))
	rightKey := backupPathKey(filepath.Clean(right))
	if leftKey == rightKey {
		return true
	}
	separator := string(os.PathSeparator)
	leftPrefix := leftKey
	if !strings.HasSuffix(leftPrefix, separator) {
		leftPrefix += separator
	}
	rightPrefix := rightKey
	if !strings.HasSuffix(rightPrefix, separator) {
		rightPrefix += separator
	}
	return strings.HasPrefix(leftKey, rightPrefix) ||
		strings.HasPrefix(rightKey, leftPrefix)
}
