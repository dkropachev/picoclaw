//nolint:govet // Backup fault boundaries intentionally use narrow error scopes.
package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func hashBackupFile(
	ctx context.Context,
	path string,
	expected os.FileInfo,
	expectedIdentity fileidentity.Identity,
	maxBytes int64,
) (result string, size int64, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if expected == nil || !expectedIdentity.Valid() {
		return "", 0, errors.New("database backup expected file identity is invalid")
	}
	file, opened, err := openPinnedBackupPath(path, false)
	if err != nil {
		return "", 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if opened == nil || !os.SameFile(expected, opened) ||
		!opened.Mode().IsRegular() ||
		opened.Mode()&os.ModeSymlink != 0 || expected.Size() != opened.Size() ||
		expected.Mode() != opened.Mode() || !expected.ModTime().Equal(opened.ModTime()) {
		return "", 0, errors.Join(errors.New("database backup file changed while opening"), err)
	}
	identity, identityErr := backupOpenedIdentity(file, fileidentity.ObjectTypeRegular)
	if identityErr != nil || identity != expectedIdentity {
		return "", 0, errors.Join(
			errors.New("database backup file identity changed while opening"), identityErr,
		)
	}
	if err := fileutil.ValidatePrivateFile(path, opened); err != nil {
		return "", 0, fmt.Errorf("revalidate database backup file: %w", err)
	}
	if err := validateBackupPlatformFile(opened, file, 0o600); err != nil {
		return "", 0, err
	}
	digest := sha256.New()
	size, err = copyWithContext(ctx, io.Discard, digest, file, maxBytes)
	if err != nil {
		return "", size, err
	}
	after, err := os.Lstat(path)
	afterIdentity, afterType, exists, afterIdentityErr := fileidentity.ExistingWithType(path)
	if err != nil || afterIdentityErr != nil || !exists ||
		afterType != fileidentity.ObjectTypeRegular || afterIdentity != identity ||
		opened.Size() != after.Size() ||
		opened.Mode() != after.Mode() || !opened.ModTime().Equal(after.ModTime()) {
		return "", size, errors.Join(
			errors.New("database backup file changed while hashing"), err, afterIdentityErr,
		)
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}

func secureAndValidateBackupDirectory(path string) error {
	secured, err := fileutil.SecurePrivateDirectory(path)
	if err != nil {
		return err
	}
	return fileutil.ValidatePrivateDirectory(path, secured)
}

func secureAndValidateBackupFile(path string) error {
	secured, err := fileutil.SecurePrivateFile(path)
	if err != nil {
		return err
	}
	return fileutil.ValidatePrivateFile(path, secured)
}

type backupReadOps struct {
	lstat                func(string) (os.FileInfo, error)
	openPinned           func(string, bool) (*os.File, os.FileInfo, error)
	openedIdentity       func(*os.File, fileidentity.ObjectType) (fileidentity.Identity, error)
	validatePrivateFile  func(string, os.FileInfo) error
	validatePlatformFile func(os.FileInfo, *os.File, uint32) error
	readAll              func(io.Reader) ([]byte, error)
	stat                 func(*os.File) (os.FileInfo, error)
	existingWithType     func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)
	seek                 func(*os.File, int64, int) (int64, error)
}

func defaultBackupReadOps() backupReadOps {
	return backupReadOps{
		lstat: os.Lstat, openPinned: openPinnedBackupPath,
		openedIdentity:       backupOpenedIdentity,
		validatePrivateFile:  fileutil.ValidatePrivateFile,
		validatePlatformFile: validateBackupPlatformFile,
		readAll:              io.ReadAll,
		stat:                 func(file *os.File) (os.FileInfo, error) { return file.Stat() },
		existingWithType:     fileidentity.ExistingWithType,
		seek: func(file *os.File, offset int64, whence int) (int64, error) {
			return file.Seek(offset, whence)
		},
	}
}

func readPrivateBackupFile(path string, maxBytes int64) (payload []byte, returnErr error) {
	payload, _, _, err := readPinnedPrivateBackupFileWithOps(
		path, maxBytes, false, defaultBackupReadOps(),
	)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func readPinnedPrivateBackupFileWithOps(
	path string,
	maxBytes int64,
	allowMissing bool,
	ops backupReadOps,
) (payload []byte, identity fileidentity.Identity, exists bool, returnErr error) {
	if maxBytes <= 0 || !validBackupAbsolutePath(path) {
		return nil, fileidentity.Identity{}, false,
			errors.New("database backup private-file read input is invalid")
	}
	info, statErr := ops.lstat(path)
	if errors.Is(statErr, os.ErrNotExist) && allowMissing {
		return nil, fileidentity.Identity{}, false, nil
	}
	if statErr != nil || info == nil || !info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return nil, fileidentity.Identity{}, false, errors.Join(
			errors.New("database backup private file is unsafe"), statErr,
		)
	}
	if metadataErr := ops.validatePrivateFile(path, info); metadataErr != nil {
		return nil, fileidentity.Identity{}, false, metadataErr
	}
	file, opened, openErr := ops.openPinned(path, false)
	if openErr != nil {
		return nil, fileidentity.Identity{}, false, openErr
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	identity, openIdentityErr := ops.openedIdentity(file, fileidentity.ObjectTypeRegular)
	if openIdentityErr != nil || opened == nil || opened.Size() > maxBytes {
		return nil, fileidentity.Identity{}, false, errors.Join(
			errors.New("database backup private-file handle is unsafe"), openIdentityErr,
		)
	}
	if metadataErr := ops.validatePrivateFile(path, opened); metadataErr != nil {
		return nil, fileidentity.Identity{}, false, metadataErr
	}
	if platformErr := ops.validatePlatformFile(opened, file, 0o600); platformErr != nil {
		return nil, fileidentity.Identity{}, false, platformErr
	}
	payload, payloadErr := ops.readAll(io.LimitReader(file, maxBytes+1))
	if payloadErr != nil || int64(len(payload)) > maxBytes {
		return nil, fileidentity.Identity{}, false, errors.Join(
			errors.New("database backup private-file size limit is exceeded"), payloadErr,
		)
	}
	afterInfo, statErr := ops.stat(file)
	afterIdentity, identityErr := ops.openedIdentity(file, fileidentity.ObjectTypeRegular)
	pathIdentity, pathType, pathExists, pathErr := ops.existingWithType(path)
	var finalPrivateErr, finalPlatformErr error
	if afterInfo != nil {
		finalPrivateErr = ops.validatePrivateFile(path, afterInfo)
		finalPlatformErr = ops.validatePlatformFile(afterInfo, file, 0o600)
	}
	if statErr != nil || identityErr != nil || pathErr != nil || !pathExists ||
		pathType != fileidentity.ObjectTypeRegular || afterIdentity != identity ||
		pathIdentity != identity || afterInfo == nil || opened.Size() != afterInfo.Size() ||
		opened.Mode() != afterInfo.Mode() || !opened.ModTime().Equal(afterInfo.ModTime()) ||
		finalPrivateErr != nil || finalPlatformErr != nil {
		return nil, fileidentity.Identity{}, false, errors.Join(
			errors.New("database backup private file changed while reading"),
			statErr, identityErr, pathErr, finalPrivateErr, finalPlatformErr,
		)
	}
	return payload, identity, true, nil
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
			if _, exists, identityErr := fileidentity.Existing(current); identityErr != nil || !exists {
				return errors.Join(
					errors.New("database backup ancestor physical identity is unsafe"), identityErr,
				)
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

func walkLegacyInputs(
	ctx context.Context,
	root string,
	backupRoot string,
	excluded map[string]struct{},
	budget *backupBudget,
	visit func(string) error,
) error {
	return walkLegacyInputsWithExclusions(
		ctx, root, backupRoot, excluded, nil, nil, budget, visit,
	)
}

type legacyExactExclusion struct {
	path                 string
	targetParent         string
	validate             func(context.Context, string, os.FileInfo) error
	validateTargetParent func(
		context.Context,
		string,
		os.FileInfo,
	) (fileidentity.Identity, error)
	seen int
}

func walkProviderCreatedTargetParent(
	ctx context.Context,
	root string,
	exact *legacyExactExclusion,
	budget *backupBudget,
) (returnErr error) {
	return walkProviderCreatedTargetParentWithOps(
		ctx,
		root,
		exact,
		budget,
		providerCreatedTargetParentWalkOps{
			lstat:  os.Lstat,
			open:   func(path string) (*os.File, os.FileInfo, error) { return openPinnedBackupPath(path, true) },
			opened: fileidentity.Opened,
			read:   readBackupDirectoryBatch,
			close:  func(file *os.File) error { return file.Close() },
		},
	)
}

type providerCreatedTargetParentWalkOps struct {
	lstat  func(string) (os.FileInfo, error)
	open   func(string) (*os.File, os.FileInfo, error)
	opened func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error)
	read   func(backupDirectoryReader) ([]os.DirEntry, error)
	close  func(*os.File) error
}

func walkProviderCreatedTargetParentWithOps(
	ctx context.Context,
	root string,
	exact *legacyExactExclusion,
	budget *backupBudget,
	ops providerCreatedTargetParentWalkOps,
) (returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validBackupAbsolutePath(root) || exact == nil ||
		filepath.Clean(root) != exact.targetParent || filepath.Dir(exact.path) != root ||
		exact.validate == nil || exact.validateTargetParent == nil || exact.seen != 0 ||
		budget == nil || ops.lstat == nil || ops.open == nil || ops.opened == nil ||
		ops.read == nil || ops.close == nil {
		return errors.New("provider-created target-parent walk is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := budget.enter("."); err != nil {
		return err
	}
	before, err := ops.lstat(root)
	if err != nil {
		return err
	}
	parentIdentity, err := exact.validateTargetParent(ctx, root, before)
	if err != nil || !parentIdentity.Valid() {
		return errors.Join(
			errors.New("provider-created target-parent identity is unavailable"),
			err,
		)
	}
	directory, opened, err := ops.open(root)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, ops.close(directory)) }()
	openedIdentity, openedType, openedErr := ops.opened(directory)
	if openedErr != nil || opened == nil || !opened.IsDir() ||
		openedType != fileidentity.ObjectTypeDirectory || openedIdentity != parentIdentity ||
		before.Mode() != opened.Mode() || !before.ModTime().Equal(opened.ModTime()) {
		return errors.Join(
			errors.New("provider-created target parent changed while opening"),
			openedErr,
		)
	}
	for {
		entries, readErr := ops.read(directory)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !validBackupPathComponent(entry.Name()) {
				return errors.New(
					"provider-created target parent contains an invalid entry",
				)
			}
			child := filepath.Clean(filepath.Join(root, entry.Name()))
			if child != exact.path {
				return errors.New(
					"provider-created target parent contains an unexpected entry",
				)
			}
			if err := budget.enter(entry.Name()); err != nil {
				return err
			}
			info, err := ops.lstat(child)
			if err != nil {
				return err
			}
			if err := exact.validate(ctx, child, info); err != nil {
				return err
			}
			exact.seen++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if exact.seen != 1 {
		return errors.New(
			"provider-created target parent does not contain exactly its replacement stage",
		)
	}
	after, err := ops.lstat(root)
	if err != nil || after == nil || !after.IsDir() ||
		after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return errors.Join(
			errors.New("provider-created target parent changed during traversal"),
			err,
		)
	}
	afterIdentity, err := exact.validateTargetParent(ctx, root, after)
	if err != nil || afterIdentity != parentIdentity {
		return errors.Join(
			errors.New("provider-created target-parent proof changed during traversal"),
			err,
		)
	}
	return nil
}

func walkLegacyInputsWithExactExclusion(
	ctx context.Context,
	root string,
	backupRoot string,
	excluded map[string]struct{},
	exact *legacyExactExclusion,
	budget *backupBudget,
	visit func(string) error,
) error {
	return walkLegacyInputsWithExclusions(
		ctx, root, backupRoot, excluded, nil, exact, budget, visit,
	)
}

func walkLegacyInputsWithPhysicalExclusions(
	ctx context.Context,
	root string,
	backupRoot string,
	excluded map[string]struct{},
	physicalExcluded map[fileidentity.Identity]struct{},
	budget *backupBudget,
	visit func(string) error,
) error {
	return walkLegacyInputsWithExclusions(
		ctx, root, backupRoot, excluded, physicalExcluded, nil, budget, visit,
	)
}

func walkLegacyInputsWithExclusions(
	ctx context.Context,
	root string,
	backupRoot string,
	excluded map[string]struct{},
	physicalExcluded map[fileidentity.Identity]struct{},
	exact *legacyExactExclusion,
	budget *backupBudget,
	visit func(string) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validBackupAbsolutePath(root) {
		return errors.New("legacy input path is invalid")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if exact != nil && filepath.Clean(root) == exact.path {
		if err := budget.enter("."); err != nil {
			return err
		}
		if exact.validate == nil {
			return errors.New("exact legacy exclusion validation is unavailable")
		}
		if err := exact.validate(ctx, filepath.Clean(root), info); err != nil {
			return err
		}
		exact.seen++
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("legacy input is a symlink")
	}
	if info.Mode().IsRegular() {
		if err := budget.enter("."); err != nil {
			return err
		}
		if _, skip := excluded[backupPathKey(root)]; skip {
			return nil
		}
		return visit(root)
	}
	if !info.IsDir() {
		return errors.New("legacy input is not a regular file or directory")
	}
	return walkLegacyDirectoryWithExclusions(
		ctx, filepath.Clean(root), ".", filepath.Clean(backupRoot),
		excluded, physicalExcluded, exact, budget, visit,
	)
}

func walkLegacyDirectory(
	ctx context.Context,
	path,
	relative,
	backupRoot string,
	excluded map[string]struct{},
	budget *backupBudget,
	visit func(string) error,
) (returnErr error) {
	return walkLegacyDirectoryWithExclusions(
		ctx, path, relative, backupRoot, excluded, nil, nil, budget, visit,
	)
}

func walkLegacyDirectoryWithExclusions(
	ctx context.Context,
	path,
	relative,
	backupRoot string,
	excluded map[string]struct{},
	physicalExcluded map[fileidentity.Identity]struct{},
	exact *legacyExactExclusion,
	budget *backupBudget,
	visit func(string) error,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := budget.enter(relative); err != nil {
		return err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("legacy input tree directory changed before traversal")
	}
	beforeIdentity, beforeType, exists, identityErr := fileidentity.ExistingWithType(path)
	if identityErr != nil || !exists || beforeType != fileidentity.ObjectTypeDirectory {
		return errors.Join(errors.New("legacy input directory identity is unsafe"), identityErr)
	}
	if _, skip := physicalExcluded[beforeIdentity]; skip {
		return errors.New("legacy input physically contains the database backup namespace")
	}
	directory, opened, err := openPinnedBackupPath(path, true)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	openedIdentity, openedType, openedIdentityErr := fileidentity.Opened(directory)
	if openedIdentityErr != nil || openedType != fileidentity.ObjectTypeDirectory ||
		openedIdentity != beforeIdentity || !opened.IsDir() || before.Mode() != opened.Mode() ||
		!before.ModTime().Equal(opened.ModTime()) {
		return errors.Join(
			errors.New("legacy input tree directory changed while opening"), openedIdentityErr,
		)
	}
	for {
		entries, readErr := readBackupDirectoryBatch(directory)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !validBackupPathComponent(entry.Name()) {
				return errors.New("legacy input tree contains an invalid path component")
			}
			child := filepath.Join(path, entry.Name())
			childRelative := entry.Name()
			if relative != "." {
				childRelative = filepath.Join(relative, entry.Name())
			}
			clean := filepath.Clean(child)
			cleanKey, backupRootKey := backupPathKey(clean), backupPathKey(backupRoot)
			if cleanKey == backupRootKey ||
				strings.HasPrefix(cleanKey, backupRootKey+string(os.PathSeparator)) {
				if err := budget.enter(childRelative); err != nil {
					return err
				}
				continue
			}
			info, statErr := os.Lstat(child)
			if statErr != nil {
				return statErr
			}
			if exact != nil && clean == exact.path {
				if err := budget.enter(childRelative); err != nil {
					return err
				}
				if exact.validate == nil {
					return errors.New("exact legacy exclusion validation is unavailable")
				}
				if err := exact.validate(ctx, clean, info); err != nil {
					return err
				}
				exact.seen++
				continue
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("legacy input tree contains a symlink")
			}
			if info.IsDir() {
				if relative == "." && skipLegacyTopLevelDirectory(entry.Name()) {
					if err := budget.enter(childRelative); err != nil {
						return err
					}
					continue
				}
				if err := walkLegacyDirectoryWithExclusions(
					ctx, child, childRelative, backupRoot, excluded,
					physicalExcluded, exact, budget, visit,
				); err != nil {
					return err
				}
				continue
			}
			if err := budget.enter(childRelative); err != nil {
				return err
			}
			if _, skip := excluded[backupPathKey(clean)]; skip {
				continue
			}
			if !info.Mode().IsRegular() {
				return errors.New("legacy input tree contains a non-regular file")
			}
			if err := visit(child); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			after, statErr := os.Lstat(path)
			afterIdentity, afterType, exists, identityErr := fileidentity.ExistingWithType(path)
			if statErr != nil || after == nil || !after.IsDir() ||
				after.Mode()&os.ModeSymlink != 0 || identityErr != nil || !exists ||
				afterType != fileidentity.ObjectTypeDirectory || afterIdentity != beforeIdentity ||
				before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
				return errors.Join(
					errors.New("legacy input tree directory changed during traversal"),
					statErr, identityErr,
				)
			}
			return nil
		}
	}
}

func skipLegacyTopLevelDirectory(name string) bool {
	name = backupPathKey(name)
	return name == backupPathKey("legacy-json") || name == backupPathKey("backups") ||
		name == backupPathKey(database.StateDirectoryName)
}

type backupDirectoryReader interface {
	ReadDir(count int) ([]os.DirEntry, error)
}

func readBackupDirectoryBatch(directory backupDirectoryReader) ([]os.DirEntry, error) {
	if directory == nil {
		return nil, errors.New("legacy input directory reader is unavailable")
	}
	entries, err := directory.ReadDir(128)
	if len(entries) == 0 && err == nil {
		return nil, errors.New("legacy input directory read made no progress")
	}
	return entries, err
}

// backupCopyOps keeps durability fault injection local to one private call.
// Production uses the exact os/fileutil operations returned below.
type backupCopyOps struct {
	lstat      func(string) (os.FileInfo, error)
	openInput  func(string) (*os.File, error)
	stat       func(*os.File) (os.FileInfo, error)
	identity   func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)
	opened     func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error)
	openOutput func(string, int, os.FileMode) (*os.File, error)
	copy       func(context.Context, io.Writer, hash.Hash, io.Reader, int64) (int64, error)
	chmod      func(*os.File, os.FileMode) error
	sync       func(*os.File) error
	close      func(*os.File) error
	remove     func(string, fileidentity.Identity) error
	secure     func(string) error
	validate   func(string, os.FileInfo) error
	syncDir    func(string) error
	rel        func(string, string) (string, error)
}

func defaultBackupCopyOps() backupCopyOps {
	return backupCopyOps{
		lstat: os.Lstat,
		openInput: func(path string) (*os.File, error) {
			file, _, err := openPinnedBackupPath(path, false)
			return file, err
		},
		stat:       func(file *os.File) (os.FileInfo, error) { return file.Stat() },
		identity:   fileidentity.ExistingWithType,
		opened:     fileidentity.Opened,
		openOutput: createPinnedBackupFile,
		copy:       copyWithContext,
		chmod:      func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		sync:       func(file *os.File) error { return file.Sync() },
		close:      func(file *os.File) error { return file.Close() },
		remove:     removePinnedBackupFile,
		secure:     secureAndValidateBackupFile,
		validate:   fileutil.ValidatePrivateFile,
		syncDir:    fileutil.SyncDirectory,
		rel:        filepath.Rel,
	}
}

func copyBackupFile(
	ctx context.Context,
	backupRoot, storeID, role, source, relativeDestination string,
	budget *backupBudget,
) (BackupFileManifest, error) {
	return copyBackupFileWithOps(
		ctx, backupRoot, storeID, role, source, relativeDestination, budget,
		defaultBackupCopyOps(),
	)
}

func copyBackupFileWithOps(
	ctx context.Context,
	backupRoot, storeID, role, source, relativeDestination string,
	budget *backupBudget,
	ops backupCopyOps,
) (record BackupFileManifest, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var empty BackupFileManifest
	if !validBackupAbsolutePath(backupRoot) || !validBackupAbsolutePath(source) {
		return empty, errors.New("database backup copy path is invalid")
	}
	relativeDestination = filepath.Clean(relativeDestination)
	if !safeBackupRelative(relativeDestination) {
		return empty, errors.New("database backup destination is invalid")
	}
	before, err := ops.lstat(source)
	if err != nil {
		return empty, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return empty, errors.New("source is not a regular file")
	}
	if err := budget.reserveArchivePath(relativeDestination); err != nil {
		return empty, err
	}
	sourceIdentity, sourceType, exists, err := ops.identity(source)
	if err != nil || !exists || sourceType != fileidentity.ObjectTypeRegular {
		return empty, errors.Join(errors.New("source identity is unavailable"), err)
	}
	if err := budget.reserveFile(before.Size()); err != nil {
		return empty, err
	}
	input, err := ops.openInput(source)
	if err != nil {
		return empty, err
	}
	inputClosed := false
	defer func() {
		if !inputClosed {
			returnErr = errors.Join(returnErr, ops.close(input))
		}
	}()
	opened, err := ops.stat(input)
	openedIdentity, openedType, openedIdentityErr := ops.opened(input)
	if err != nil || openedIdentityErr != nil || openedType != fileidentity.ObjectTypeRegular ||
		openedIdentity != sourceIdentity {
		return empty, errors.Join(
			errors.New("source changed while opening"), err, openedIdentityErr,
		)
	}
	if err := validateBackupSourceFile(opened, input); err != nil {
		return empty, err
	}
	destination := filepath.Join(backupRoot, relativeDestination)
	if err := ensurePrivateBackupDirectory(filepath.Dir(destination)); err != nil {
		return empty, err
	}
	output, err := ops.openOutput(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return empty, err
	}
	outputIdentity, outputType, identityErr := ops.opened(output)
	if identityErr != nil || outputType != fileidentity.ObjectTypeRegular ||
		!outputIdentity.Valid() {
		return empty, errors.Join(
			errors.New("database backup output handle identity is unsafe"),
			identityErr, ops.close(output),
		)
	}
	keep := false
	outputClosed := false
	defer func() {
		if !outputClosed {
			returnErr = errors.Join(returnErr, ops.close(output))
		}
		if !keep {
			returnErr = errors.Join(
				returnErr, ops.remove(destination, outputIdentity),
				ops.syncDir(filepath.Dir(destination)),
			)
		}
	}()
	if _, outputInfoErr := ops.stat(output); outputInfoErr != nil {
		return empty, outputInfoErr
	}
	pathIdentity, pathType, pathExists, pathIdentityErr := ops.identity(destination)
	if identityErr != nil || pathIdentityErr != nil || !pathExists ||
		outputType != fileidentity.ObjectTypeRegular || pathType != fileidentity.ObjectTypeRegular ||
		outputIdentity != pathIdentity || outputIdentity == sourceIdentity {
		return empty, errors.Join(
			errors.New("database backup output identity is unsafe"), identityErr, pathIdentityErr,
		)
	}
	digest := sha256.New()
	written, err := ops.copy(ctx, output, digest, input, before.Size())
	if err != nil {
		return empty, err
	}
	after, err := ops.lstat(source)
	afterIdentity, afterType, exists, identityErr := ops.identity(source)
	afterOpened, afterOpenedErr := ops.stat(input)
	afterOpenedIdentity, afterOpenedType, afterOpenedIdentityErr := ops.opened(input)
	afterSourceErr := validateBackupSourceFile(afterOpened, input)
	if err != nil || after == nil || identityErr != nil || !exists || afterType != fileidentity.ObjectTypeRegular ||
		afterIdentity != sourceIdentity || before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) ||
		afterOpenedErr != nil || afterOpenedIdentityErr != nil || afterOpened == nil ||
		afterOpenedType != fileidentity.ObjectTypeRegular || afterOpenedIdentity != sourceIdentity ||
		before.Size() != afterOpened.Size() || before.Mode() != afterOpened.Mode() ||
		!before.ModTime().Equal(afterOpened.ModTime()) || written != before.Size() || afterSourceErr != nil {
		return empty, errors.Join(
			errors.New("source changed while copying"), err, identityErr,
			afterOpenedErr, afterOpenedIdentityErr, afterSourceErr,
		)
	}
	if err := ops.close(input); err != nil {
		inputClosed = true
		return empty, err
	}
	inputClosed = true
	if err := ops.chmod(output, 0o600); err != nil {
		return empty, err
	}
	if err := ops.sync(output); err != nil {
		return empty, err
	}
	if err := ops.close(output); err != nil {
		outputClosed = true
		return empty, err
	}
	outputClosed = true
	if err := ops.secure(destination); err != nil {
		return empty, err
	}
	expectedDigest := hex.EncodeToString(digest.Sum(nil))
	copiedFile, copiedInfo, openErr := openPinnedBackupPath(destination, false)
	if openErr != nil {
		return empty, openErr
	}
	metadataErr := validateBackupPlatformFile(copiedInfo, copiedFile, 0o600)
	copiedIdentity, copiedType, copiedExists, identityErr := ops.identity(destination)
	copiedOpenedIdentity, copiedOpenedType, openedIdentityErr := ops.opened(copiedFile)
	verifiedDigest, digestErr := hashPreparedGenerationMember(ctx, copiedFile, written)
	afterHashOpened, afterHashStatErr := ops.stat(copiedFile)
	afterHashOpenedIdentity, afterHashOpenedType, afterHashOpenedIdentityErr := ops.opened(copiedFile)
	afterHashPathIdentityBefore, afterHashPathTypeBefore, afterHashPathExistsBefore,
		afterHashPathIdentityErrBefore := ops.identity(destination)
	afterHashPath, afterHashPathStatErr := ops.lstat(destination)
	afterHashPathIdentity, afterHashPathType, afterHashPathExists,
		afterHashPathIdentityErr := ops.identity(destination)
	afterHashMetadataErr := validateBackupPlatformFile(afterHashOpened, copiedFile, 0o600)
	afterHashPrivateErr := ops.validate(destination, afterHashPath)
	closeErr := copiedFile.Close()
	if metadataErr != nil || identityErr != nil || openedIdentityErr != nil || digestErr != nil ||
		afterHashStatErr != nil || afterHashOpenedIdentityErr != nil ||
		afterHashPathIdentityErrBefore != nil || afterHashPathStatErr != nil ||
		afterHashPathIdentityErr != nil || afterHashMetadataErr != nil || afterHashPrivateErr != nil ||
		!copiedExists || copiedType != fileidentity.ObjectTypeRegular ||
		copiedOpenedType != fileidentity.ObjectTypeRegular || copiedIdentity != outputIdentity ||
		copiedOpenedIdentity != outputIdentity || copiedIdentity == sourceIdentity ||
		!afterHashPathExistsBefore || afterHashPathTypeBefore != fileidentity.ObjectTypeRegular ||
		afterHashPathIdentityBefore != outputIdentity || !afterHashPathExists ||
		afterHashPathType != fileidentity.ObjectTypeRegular || afterHashPathIdentity != outputIdentity ||
		afterHashOpened == nil || afterHashOpenedType != fileidentity.ObjectTypeRegular ||
		afterHashOpenedIdentity != outputIdentity || afterHashPath == nil ||
		!afterHashPath.Mode().IsRegular() || afterHashPath.Mode()&os.ModeSymlink != 0 ||
		copiedInfo.Size() != written || afterHashOpened.Size() != written ||
		afterHashPath.Size() != written || copiedInfo.Mode() != afterHashOpened.Mode() ||
		copiedInfo.Mode() != afterHashPath.Mode() ||
		!copiedInfo.ModTime().Equal(afterHashOpened.ModTime()) ||
		!copiedInfo.ModTime().Equal(afterHashPath.ModTime()) ||
		verifiedDigest != expectedDigest || closeErr != nil {
		return empty, errors.Join(
			errors.New("database backup copy physical identity is unsafe"),
			metadataErr, identityErr, openedIdentityErr, digestErr,
			afterHashStatErr, afterHashOpenedIdentityErr, afterHashPathIdentityErrBefore,
			afterHashPathStatErr, afterHashPathIdentityErr, afterHashMetadataErr,
			afterHashPrivateErr, closeErr,
		)
	}
	if err := ops.syncDir(filepath.Dir(destination)); err != nil {
		return empty, err
	}
	backupRelative, err := ops.rel(backupRoot, destination)
	if err != nil {
		return empty, err
	}
	keep = true
	return BackupFileManifest{
		StoreID:        storeID,
		Role:           role,
		Source:         source,
		SourceIdentity: sourceIdentity.String(),
		Backup:         filepath.ToSlash(backupRelative),
		SHA256:         expectedDigest,
		Size:           written,
		Mode:           0o600,
		SourceMode:     uint32(before.Mode().Perm()),
	}, nil
}

func copyWithContext(
	ctx context.Context,
	destination io.Writer,
	digest hash.Hash,
	source io.Reader,
	maxBytes int64,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxBytes < 0 {
		return 0, errors.New("database backup copy limit is invalid")
	}
	buffer := make([]byte, 1<<20)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if int64(count) > maxBytes-written {
				return written, errors.New("database backup source exceeded its size limit")
			}
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
		if count == 0 {
			return written, errors.New("database backup source read made no progress")
		}
	}
}
