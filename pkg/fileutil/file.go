// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

// Package fileutil provides file manipulation utilities.
package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic atomically writes data to a file using a temp file + rename pattern.
//
// This guarantees that the target file is either:
// - Completely written with the new data
// - Unchanged (if any step fails before rename)
//
// The function:
// 1. Creates a temp file in the same directory (original untouched)
// 2. Writes data and sets the file permissions
// 3. Syncs data and metadata to disk (critical for SD cards/flash storage)
// 4. Atomically renames the temp file to the target path
// 5. Syncs directory metadata (ensures rename is durable)
//
// Safety guarantees:
// - Original file is NEVER modified until successful rename
// - Temp file is always cleaned up on error
// - Data is flushed to physical storage before rename
// - Directory entry is synced to prevent orphaned inodes
//
// Parameters:
//   - path: Target file path
//   - data: Data to write
//   - perm: File permission mode (e.g., 0o600 for secure, 0o644 for readable)
//
// Returns:
//   - Error if any step fails, nil on success
//
// Example:
//
//	// Secure config file (owner read/write only)
//	err := utils.WriteFileAtomic("config.json", data, 0o600)
//
//	// Public readable file
//	err := utils.WriteFileAtomic("public.txt", data, 0o644)
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomicWithHooks(
		path,
		data,
		perm,
		replaceFile,
		syncDirectory,
	)
}

func writeFileAtomicWithHooks(
	path string,
	data []byte,
	perm os.FileMode,
	replace func(string, string) error,
	syncDir func(string) error,
) error {
	dir := filepath.Dir(path)
	if err := MkdirAllDurable(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create temp file in the same directory (ensures atomic rename works)
	// Using a hidden prefix (.tmp-) to avoid issues with some tools
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	tmpPath := tmpFile.Name()
	cleanup := true

	defer func() {
		if cleanup {
			tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	// Write data to temp file
	// Note: Original file is untouched at this point
	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Set file permissions before the final sync so both the data and mode
	// metadata reach stable storage before the file becomes visible.
	if err := tmpFile.Chmod(perm); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	// CRITICAL: Force data and metadata to the storage medium before rename.
	// This is essential for SD cards, eMMC, and other flash storage.
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Close file before rename (required on Windows)
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic replacement: temp file becomes the target. POSIX uses rename;
	// Windows uses MoveFileEx with replace-existing and write-through flags.
	if err := replace(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	// The source name no longer belongs to this operation after replacement.
	// Stop deferred cleanup before any later failure so it cannot remove a new
	// file another process creates at the old temporary pathname.
	cleanup = false

	// Sync directory to ensure rename is durable on POSIX. Windows uses a
	// write-through replacement in replaceFile.
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("failed to sync target directory: %w", err)
	}

	return nil
}

func CopyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return WriteFileAtomic(dst, data, perm)
}
