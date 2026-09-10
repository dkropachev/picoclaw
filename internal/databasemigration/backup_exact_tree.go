package databasemigration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type backupTreeKind uint8

const (
	backupTreeDirectory backupTreeKind = iota + 1
	backupTreeFile
)

// verifyExactBackupTree rejects every object not named by the immutable
// inventory, as well as aliases, hard links, reparse points, and directory
// transitions. os.Root keeps all descendant access relative to one pinned
// root handle.
func verifyExactBackupTree(ctx context.Context, rootPath string, manifest BackupManifest) (returnErr error) {
	_, err := exactBackupTreeIdentities(ctx, rootPath, manifest)
	return err
}

func exactBackupTreeIdentities(
	ctx context.Context,
	rootPath string,
	manifest BackupManifest,
) (identities map[fileidentity.Identity]string, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	expected, err := expectedBackupTree(manifest)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(rootPath)
	if err != nil || rootInfo == nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(errors.New("database backup root is unsafe"), err)
	}
	if validateErr := fileutil.ValidatePrivateDirectory(rootPath, rootInfo); validateErr != nil {
		return nil, fmt.Errorf("validate database backup root: %w", validateErr)
	}
	root, err := openExactBackupRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open database backup root: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	rootFile, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open database backup root descriptor: %w", err)
	}
	rootPathIdentity, pathType, exists, pathIdentityErr := fileidentity.ExistingWithType(rootPath)
	rootOpenedIdentity, openedType, openedIdentityErr := fileidentity.Opened(rootFile)
	rootMountErr := validateExactBackupRootMount(rootPath, rootFile)
	closeErr := rootFile.Close()
	if pathIdentityErr != nil || openedIdentityErr != nil || rootMountErr != nil || closeErr != nil || !exists ||
		pathType != fileidentity.ObjectTypeDirectory || openedType != fileidentity.ObjectTypeDirectory ||
		rootPathIdentity != rootOpenedIdentity {
		return nil, errors.Join(
			errors.New("database backup root changed while opening"),
			pathIdentityErr, openedIdentityErr, rootMountErr, closeErr,
		)
	}
	seen := make(map[string]struct{}, len(expected))
	objectIdentities := make(map[fileidentity.Identity]string, len(expected))
	objectIdentities[rootPathIdentity] = "."
	seen["."] = struct{}{}
	if err := verifyExactBackupDirectory(
		ctx, rootPath, root, ".", expected, seen, objectIdentities,
	); err != nil {
		return nil, err
	}
	if len(seen) != len(expected) {
		return nil, errors.New("database backup tree omits an inventory path")
	}
	return objectIdentities, nil
}

func expectedBackupTree(manifest BackupManifest) (map[string]backupTreeKind, error) {
	expected := map[string]backupTreeKind{
		".":                backupTreeDirectory,
		backupManifestName: backupTreeFile,
		backupManifestHash: backupTreeFile,
	}
	for _, record := range manifest.Files {
		if !validBackupManifestRelative(record.Backup) {
			return nil, errors.New("database backup inventory path is invalid")
		}
		relative := filepath.Clean(filepath.FromSlash(record.Backup))
		if _, present := expected[relative]; present {
			return nil, errors.New("database backup inventory has a control or path collision")
		}
		expected[relative] = backupTreeFile
		for parent := filepath.Dir(relative); parent != "."; parent = filepath.Dir(parent) {
			if kind, present := expected[parent]; present && kind != backupTreeDirectory {
				return nil, errors.New("database backup inventory has a file-directory collision")
			}
			expected[parent] = backupTreeDirectory
		}
	}
	return expected, nil
}

func verifyExactBackupDirectory(
	ctx context.Context,
	rootPath string,
	root *os.Root,
	relative string,
	expected map[string]backupTreeKind,
	seen map[string]struct{},
	objectIdentities map[fileidentity.Identity]string,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	fullPath := rootPath
	if relative != "." {
		fullPath = filepath.Join(rootPath, relative)
	}
	before, err := root.Lstat(relative)
	if err != nil || before == nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("database backup tree directory is unsafe"), err)
	}
	if validateErr := fileutil.ValidatePrivateDirectory(fullPath, before); validateErr != nil {
		return fmt.Errorf("validate database backup tree directory: %w", validateErr)
	}
	beforeIdentity, objectType, exists, err := fileidentity.ExistingWithType(fullPath)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
		return errors.Join(errors.New("database backup directory identity is unavailable"), err)
	}
	if previous, duplicate := objectIdentities[beforeIdentity]; duplicate && previous != relative {
		return errors.New("database backup tree contains a physical object alias")
	}
	objectIdentities[beforeIdentity] = relative
	directory, err := openExactBackupChild(root, relative)
	if err != nil {
		return fmt.Errorf("open database backup tree directory: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	opened, err := directory.Stat()
	openedIdentity, openedType, identityErr := fileidentity.Opened(directory)
	if err != nil || identityErr != nil || openedType != fileidentity.ObjectTypeDirectory ||
		openedIdentity != beforeIdentity || !opened.IsDir() {
		return errors.Join(
			errors.New("database backup directory changed while opening"), err, identityErr,
		)
	}

	for {
		batch, readErr := directory.ReadDir(128)
		for _, entry := range batch {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			if len(seen) >= len(expected) {
				return errors.New("database backup tree entry limit exceeded")
			}
			if !validBackupPathComponent(entry.Name()) {
				return errors.New("database backup tree contains an invalid path component")
			}
			child := entry.Name()
			if relative != "." {
				child = filepath.Join(relative, child)
			}
			kind, present := expected[child]
			if !present {
				return fmt.Errorf("database backup tree contains unexpected path %q", filepath.ToSlash(child))
			}
			if _, duplicate := seen[child]; duplicate {
				return errors.New("database backup tree contains a duplicate inventory path")
			}
			seen[child] = struct{}{}
			info, statErr := root.Lstat(child)
			if statErr != nil || info == nil || info.Mode()&os.ModeSymlink != 0 {
				return errors.Join(errors.New("database backup tree object is unsafe"), statErr)
			}
			switch kind {
			case backupTreeDirectory:
				if !info.IsDir() {
					return errors.New("database backup inventory directory has wrong type")
				}
				if verifyErr := verifyExactBackupDirectory(
					ctx, rootPath, root, child, expected, seen, objectIdentities,
				); verifyErr != nil {
					return verifyErr
				}
			case backupTreeFile:
				if !info.Mode().IsRegular() {
					return errors.New("database backup inventory file has wrong type")
				}
				if verifyErr := verifyExactBackupRegular(
					rootPath, root, child, info, objectIdentities,
				); verifyErr != nil {
					return verifyErr
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		if len(batch) == 0 {
			return errors.New("database backup directory read made no progress")
		}
	}
	after, err := root.Lstat(relative)
	afterIdentity, afterType, exists, identityErr := fileidentity.ExistingWithType(fullPath)
	if err != nil || identityErr != nil || !exists || afterType != fileidentity.ObjectTypeDirectory ||
		afterIdentity != beforeIdentity || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(after.ModTime()) {
		return errors.Join(
			errors.New("database backup directory changed during traversal"), err, identityErr,
		)
	}
	if validateErr := fileutil.ValidatePrivateDirectory(fullPath, after); validateErr != nil {
		return fmt.Errorf("revalidate database backup tree directory: %w", validateErr)
	}
	if reopenErr := revalidateExactBackupChild(
		root, relative, fullPath, beforeIdentity, fileidentity.ObjectTypeDirectory,
	); reopenErr != nil {
		return fmt.Errorf("reopen database backup tree directory: %w", reopenErr)
	}
	return nil
}

func verifyExactBackupRegular(
	rootPath string,
	root *os.Root,
	relative string,
	before os.FileInfo,
	objectIdentities map[fileidentity.Identity]string,
) (returnErr error) {
	fullPath := filepath.Join(rootPath, relative)
	beforeIdentity, beforeType, exists, identityErr := fileidentity.ExistingWithType(fullPath)
	if identityErr != nil || !exists || beforeType != fileidentity.ObjectTypeRegular {
		return errors.Join(errors.New("database backup file identity is unavailable"), identityErr)
	}
	if previous, duplicate := objectIdentities[beforeIdentity]; duplicate && previous != relative {
		return errors.New("database backup tree contains a physical object alias")
	}
	objectIdentities[beforeIdentity] = relative
	file, err := openExactBackupChild(root, relative)
	if err != nil {
		return fmt.Errorf("open database backup inventory file: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	opened, err := file.Stat()
	openedIdentity, openedType, openedIdentityErr := fileidentity.Opened(file)
	if err != nil || openedIdentityErr != nil || openedType != fileidentity.ObjectTypeRegular ||
		openedIdentity != beforeIdentity || !opened.Mode().IsRegular() ||
		before.Mode() != opened.Mode() || before.Size() != opened.Size() ||
		!before.ModTime().Equal(opened.ModTime()) {
		return errors.Join(
			errors.New("database backup file changed while opening"), err, openedIdentityErr,
		)
	}
	if validateErr := fileutil.ValidatePrivateFile(fullPath, opened); validateErr != nil {
		return fmt.Errorf("validate database backup inventory file: %w", validateErr)
	}
	if metadataErr := validateBackupPlatformFile(opened, file, 0o600); metadataErr != nil {
		return metadataErr
	}
	after, err := root.Lstat(relative)
	afterIdentity, afterType, exists, identityErr := fileidentity.ExistingWithType(fullPath)
	if err != nil || identityErr != nil || !exists || afterType != fileidentity.ObjectTypeRegular ||
		afterIdentity != beforeIdentity || opened.Mode() != after.Mode() ||
		opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return errors.Join(
			errors.New("database backup file changed during inspection"), err, identityErr,
		)
	}
	if validateErr := fileutil.ValidatePrivateFile(fullPath, after); validateErr != nil {
		return fmt.Errorf("revalidate database backup inventory file: %w", validateErr)
	}
	finalOpened, finalStatErr := file.Stat()
	if finalStatErr != nil || finalOpened == nil || finalOpened.Mode() != after.Mode() ||
		finalOpened.Size() != after.Size() || !finalOpened.ModTime().Equal(after.ModTime()) {
		return errors.Join(errors.New("database backup file handle changed during inspection"), finalStatErr)
	}
	if metadataErr := validateBackupPlatformFile(finalOpened, file, 0o600); metadataErr != nil {
		return metadataErr
	}
	if reopenErr := revalidateExactBackupChild(
		root, relative, fullPath, beforeIdentity, fileidentity.ObjectTypeRegular,
	); reopenErr != nil {
		return fmt.Errorf("reopen database backup inventory file: %w", reopenErr)
	}
	return nil
}

func revalidateExactBackupChild(
	root *os.Root,
	relative string,
	fullPath string,
	expected fileidentity.Identity,
	expectedType fileidentity.ObjectType,
) (returnErr error) {
	file, err := openExactBackupChild(root, relative)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	identity, objectType, err := fileidentity.Opened(file)
	if err != nil || !expected.Valid() || identity != expected || objectType != expectedType {
		return errors.Join(errors.New("database backup object changed during mount revalidation"), err)
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if expectedType == fileidentity.ObjectTypeDirectory {
		return fileutil.ValidatePrivateDirectory(fullPath, info)
	}
	return errors.Join(
		fileutil.ValidatePrivateFile(fullPath, info),
		validateBackupPlatformFile(info, file, 0o600),
	)
}
