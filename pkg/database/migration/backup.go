//nolint:govet // Snapshot stages intentionally use narrow error scopes.
package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	backupManifestVersion = 1
	backupManifestName    = "manifest.json"
)

// BackupManifest is the durable inventory required to verify and restore an
// exact pre-migration generation.
type BackupManifest struct {
	Version   int                   `json:"version"`
	CreatedAt time.Time             `json:"created_at"`
	Outcome   string                `json:"outcome"`
	Error     string                `json:"error,omitempty"`
	Stores    []BackupStoreManifest `json:"stores"`
	Files     []BackupFileManifest  `json:"files"`
}

// BackupStoreManifest identifies one selected logical store without exposing
// it through the normal application catalog API.
type BackupStoreManifest struct {
	StoreID string `json:"store_id"`
	Exists  bool   `json:"exists"`
}

// BackupFileManifest records a copied generation or legacy input.
type BackupFileManifest struct {
	StoreID string `json:"store_id"`
	Role    string `json:"role"`
	Source  string `json:"source"`
	Backup  string `json:"backup"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
}

type backupSession struct {
	root     string
	manifest BackupManifest
}

func (e *Engine) snapshot(
	ctx context.Context,
	physical *storecatalog.Catalog,
	specs []storecatalog.Spec,
	configuredParent string,
) (*backupSession, error) {
	parent, err := validateBackupParent(configuredParent, physical.Home)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		return nil, err
	}
	timestamp := e.now().UTC()
	name := "database-migrate-" + timestamp.Format("20060102T150405.000000000Z")
	root := filepath.Join(parent, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, fmt.Errorf("create database migration backup: %w", err)
	}
	if err := fileutil.SyncDirectory(parent); err != nil {
		return &backupSession{root: root}, fmt.Errorf("sync database backup parent: %w", err)
	}
	session := &backupSession{root: root, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: timestamp, Outcome: "snapshotting",
	}}
	failed := func(snapshotErr error) (*backupSession, error) {
		manifestErr := session.finish("failed", snapshotErr)
		return session, errors.Join(snapshotErr, manifestErr)
	}

	excluded := make(map[string]struct{}, len(physical.Specs)*4)
	for _, known := range physical.Specs {
		for _, generation := range generationPaths(known.Path) {
			excluded[filepath.Clean(generation)] = struct{}{}
		}
	}
	seenLegacy := make(map[string]struct{})
	var generationIdentities []os.FileInfo
	for _, known := range physical.Specs {
		for _, generation := range generationPaths(known.Path) {
			if info, statErr := os.Lstat(generation); statErr == nil {
				generationIdentities = append(generationIdentities, info)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return failed(statErr)
			}
		}
	}
	var legacyIdentities []os.FileInfo
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return failed(err)
		}
		storeRecord := BackupStoreManifest{StoreID: spec.ID}
		mainExists := false
		for generationIndex, source := range generationPaths(spec.Path) {
			info, statErr := os.Lstat(source)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return failed(fmt.Errorf("inspect store %s generation: %w", spec.ID, statErr))
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return failed(fmt.Errorf("store %s generation is unsafe", spec.ID))
			}
			if generationIndex == 0 {
				mainExists = true
				storeRecord.Exists = true
			} else if !mainExists {
				return failed(fmt.Errorf("store %s has a sidecar without its database", spec.ID))
			}
			role := []string{"database", "wal", "shm", "journal"}[generationIndex]
			destination := filepath.Join("stores", spec.ID, "generation", role)
			record, copyErr := copyBackupFile(ctx, session.root, spec.ID, role, source, destination)
			if copyErr != nil {
				return failed(fmt.Errorf("snapshot store %s %s: %w", spec.ID, role, copyErr))
			}
			session.manifest.Files = append(session.manifest.Files, record)
		}
		session.manifest.Stores = append(session.manifest.Stores, storeRecord)

		for _, legacyRoot := range spec.LegacyRoots {
			walkErr := walkLegacyInputs(ctx, legacyRoot, session.root, excluded, func(source string) error {
				canonical := filepath.Clean(source)
				if _, duplicate := seenLegacy[canonical]; duplicate {
					return nil
				}
				seenLegacy[canonical] = struct{}{}
				info, statErr := os.Lstat(source)
				if statErr != nil {
					return statErr
				}
				for _, generationInfo := range generationIdentities {
					if os.SameFile(info, generationInfo) {
						return errors.New("legacy input aliases a database generation")
					}
				}
				for _, previousInfo := range legacyIdentities {
					if os.SameFile(info, previousInfo) {
						return errors.New("legacy inputs contain a physical alias")
					}
				}
				legacyIdentities = append(legacyIdentities, info)
				digest := sha256.Sum256([]byte(canonical))
				destination := filepath.Join(
					"stores", spec.ID, "legacy", hex.EncodeToString(digest[:8]), filepath.Base(source),
				)
				record, copyErr := copyBackupFile(
					ctx, session.root, spec.ID, "legacy", source, destination,
				)
				if copyErr != nil {
					return copyErr
				}
				session.manifest.Files = append(session.manifest.Files, record)
				return nil
			})
			if walkErr != nil {
				return failed(fmt.Errorf("snapshot store %s legacy inputs: %w", spec.ID, walkErr))
			}
		}
	}
	sort.Slice(session.manifest.Files, func(i, j int) bool {
		left, right := session.manifest.Files[i], session.manifest.Files[j]
		if left.StoreID != right.StoreID {
			return left.StoreID < right.StoreID
		}
		if left.Role != right.Role {
			return left.Role < right.Role
		}
		return left.Source < right.Source
	})
	if err := session.finish("snapshot_complete", nil); err != nil {
		return session, fmt.Errorf("write database backup manifest: %w", err)
	}
	return session, nil
}

func (b *backupSession) finish(outcome string, migrationErr error) error {
	if b == nil || b.root == "" {
		return nil
	}
	b.manifest.Outcome = outcome
	if migrationErr != nil {
		b.manifest.Error = migrationErr.Error()
	} else {
		b.manifest.Error = ""
	}
	payload, err := json.MarshalIndent(b.manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := fileutil.WriteFileAtomic(filepath.Join(b.root, backupManifestName), payload, 0o600); err != nil {
		return err
	}
	return fileutil.SyncDirectory(b.root)
}

func ensurePrivateBackupDirectory(path string) error {
	if err := validateBackupAncestors(path); err != nil {
		return err
	}
	if err := fileutil.MkdirAllDurable(path, 0o700); err != nil {
		return fmt.Errorf("create database backup directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("database backup directory is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolvedAbsolute, err := filepath.Abs(resolved)
	if err != nil || filepath.Clean(absolute) != filepath.Clean(resolvedAbsolute) {
		return errors.New("database backup directory contains a symlink")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	if err := validateBackupAncestors(path); err != nil {
		return err
	}
	return fileutil.SyncDirectory(path)
}

func validateBackupAncestors(path string) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	current := absolute
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("database backup path contains an unsafe ancestor")
			}
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return resolveErr
			}
			resolved, resolveErr = filepath.Abs(resolved)
			if resolveErr != nil || filepath.Clean(resolved) != filepath.Clean(current) {
				return errors.New("database backup path contains a symlinked ancestor")
			}
			return nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return statErr
		}
		current = parent
	}
}

func generationPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func walkLegacyInputs(
	ctx context.Context,
	root string,
	backupRoot string,
	excluded map[string]struct{},
	visit func(string) error,
) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("legacy input is a symlink")
	}
	if info.Mode().IsRegular() {
		if _, skip := excluded[filepath.Clean(root)]; skip {
			return nil
		}
		return visit(root)
	}
	if !info.IsDir() {
		return errors.New("legacy input is not a regular file or directory")
	}
	backupRoot = filepath.Clean(backupRoot)
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		clean := filepath.Clean(path)
		if clean == backupRoot || strings.HasPrefix(clean, backupRoot+string(os.PathSeparator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("legacy input tree contains a symlink")
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == "legacy-json" || entry.Name() == "backups") {
				return filepath.SkipDir
			}
			return nil
		}
		if _, skip := excluded[clean]; skip {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("legacy input tree contains a non-regular file")
		}
		return visit(path)
	})
}

func copyBackupFile(
	ctx context.Context,
	backupRoot, storeID, role, source, relativeDestination string,
) (BackupFileManifest, error) {
	var empty BackupFileManifest
	before, err := os.Lstat(source)
	if err != nil {
		return empty, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return empty, errors.New("source is not a regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return empty, err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return empty, errors.New("source changed while opening")
	}
	destination := filepath.Join(backupRoot, relativeDestination)
	if err := fileutil.MkdirAllDurable(filepath.Dir(destination), 0o700); err != nil {
		return empty, err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, before.Mode().Perm())
	if err != nil {
		return empty, err
	}
	keep := false
	defer func() {
		_ = output.Close()
		if !keep {
			_ = os.Remove(destination)
		}
	}()
	digest := sha256.New()
	written, err := copyWithContext(ctx, output, digest, input)
	if err != nil {
		return empty, err
	}
	after, err := os.Lstat(source)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || written != before.Size() {
		return empty, errors.New("source changed while copying")
	}
	if err := output.Chmod(before.Mode().Perm()); err != nil {
		return empty, err
	}
	if err := output.Sync(); err != nil {
		return empty, err
	}
	if err := output.Close(); err != nil {
		return empty, err
	}
	if err := fileutil.SyncDirectory(filepath.Dir(destination)); err != nil {
		return empty, err
	}
	keep = true
	backupRelative, err := filepath.Rel(backupRoot, destination)
	if err != nil {
		return empty, err
	}
	return BackupFileManifest{
		StoreID: storeID,
		Role:    role,
		Source:  source,
		Backup:  filepath.ToSlash(backupRelative),
		SHA256:  hex.EncodeToString(digest.Sum(nil)),
		Size:    written,
		Mode:    uint32(before.Mode().Perm()),
	}, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, digest hash.Hash, source io.Reader) (int64, error) {
	buffer := make([]byte, 1<<20)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			chunk := buffer[:count]
			outputCount, writeErr := destination.Write(chunk)
			if writeErr != nil {
				return written, writeErr
			}
			if outputCount != count {
				return written, io.ErrShortWrite
			}
			if _, writeErr := digest.Write(chunk); writeErr != nil {
				return written, writeErr
			}
			written += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
}
