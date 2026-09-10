package databasemigration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type preparedLegacyDirectory struct {
	relative string
	path     string
	identity fileidentity.Identity
}

type preparedLegacyMember struct {
	relative string
	path     string
	identity fileidentity.Identity
	info     os.FileInfo
	file     *os.File
	size     int64
	digest   string
}

// preparedLegacyInputs binds an adapter's ordered roots to an exact private
// disposable tree. Paths are exposed only while use holds and verifies the
// seal around one synchronous adapter operation.
type preparedLegacyInputs struct {
	mu           sync.Mutex
	root         string
	rootIdentity fileidentity.Identity
	rootHandle   *os.Root
	rootFile     *os.File
	roots        []string
	expected     map[string]backupTreeKind
	directories  []preparedLegacyDirectory
	members      []preparedLegacyMember
	closed       bool
}

func sealPreparedLegacyInputsProvenance(
	ctx context.Context,
	root string,
	roots []string,
	manifest BackupManifest,
	provenance backupStoreProvenance,
) (*preparedLegacyInputs, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validBackupAbsolutePath(root) || len(roots) != len(provenance.legacyRoots) {
		return nil, errors.New("prepared legacy input root is invalid")
	}
	if err := validateBackupManifest(manifest); err != nil {
		return nil, fmt.Errorf("validate prepared legacy manifest: %w", err)
	}
	store, err := backupManifestStoreForProvenance(manifest, provenance)
	if err != nil {
		return nil, err
	}
	expected, records, err := expectedPreparedLegacyTree(root, roots, manifest, provenance, store)
	if err != nil {
		return nil, err
	}
	rootIdentity, err := backupExistingIdentity(root, fileidentity.ObjectTypeDirectory)
	if err != nil {
		return nil, errors.Join(errors.New("prepared legacy root identity is unavailable"), err)
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	rootFile, err := rootHandle.Open(".")
	if err != nil {
		return nil, errors.Join(err, rootHandle.Close())
	}
	prepared := &preparedLegacyInputs{
		root: root, rootIdentity: rootIdentity, rootHandle: rootHandle, rootFile: rootFile,
		roots: append([]string(nil), roots...), expected: expected,
	}
	fail := func(cause error) (*preparedLegacyInputs, error) {
		return nil, errors.Join(cause, prepared.closeUnlocked())
	}

	paths := make([]string, 0, len(expected))
	for relative := range expected {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	directoryIdentities := make(map[fileidentity.Identity]string)
	memberIdentities := make(map[fileidentity.Identity]string)
	for _, relative := range paths {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		fullPath := root
		if relative != "." {
			fullPath = filepath.Join(root, relative)
		}
		switch expected[relative] {
		case backupTreeDirectory:
			directory, openErr := rootHandle.Open(relative)
			if openErr != nil {
				return fail(openErr)
			}
			identity, identityErr := backupPathMatchesOpened(
				fullPath, directory, fileidentity.ObjectTypeDirectory,
			)
			info, statErr := directory.Stat()
			privateErr := fileutil.ValidatePrivateDirectory(fullPath, info)
			closeErr := directory.Close()
			if identityErr != nil || statErr != nil || privateErr != nil || closeErr != nil {
				return fail(errors.Join(
					errors.New("prepared legacy directory is unsafe"),
					identityErr, statErr, privateErr, closeErr,
				))
			}
			if previous, duplicate := directoryIdentities[identity]; duplicate && previous != relative {
				return fail(errors.New("prepared legacy tree contains a physical directory alias"))
			}
			directoryIdentities[identity] = relative
			prepared.directories = append(prepared.directories, preparedLegacyDirectory{
				relative: relative, path: fullPath, identity: identity,
			})
		case backupTreeFile:
			record, present := records[relative]
			if !present {
				return fail(errors.New("prepared legacy member manifest is missing"))
			}
			file, openErr := rootHandle.Open(relative)
			if openErr != nil {
				return fail(openErr)
			}
			opened, statErr := file.Stat()
			identity, identityErr := backupPathMatchesOpened(
				fullPath, file, fileidentity.ObjectTypeRegular,
			)
			if statErr != nil || identityErr != nil || opened == nil {
				_ = file.Close()
				return fail(errors.Join(
					errors.New("prepared legacy member changed while sealing"),
					statErr, identityErr,
				))
			}
			if previous, duplicate := memberIdentities[identity]; duplicate {
				_ = file.Close()
				return fail(fmt.Errorf(
					"prepared legacy members %q and %q physically alias", previous, relative,
				))
			}
			memberIdentities[identity] = relative
			if privateErr := fileutil.ValidatePrivateFile(fullPath, opened); privateErr != nil {
				_ = file.Close()
				return fail(fmt.Errorf("validate prepared legacy member: %w", privateErr))
			}
			if metadataErr := validateBackupPlatformFile(opened, file, 0o600); metadataErr != nil {
				_ = file.Close()
				return fail(metadataErr)
			}
			if opened.Size() != record.Size {
				_ = file.Close()
				return fail(errors.New("prepared legacy member size differs from manifest"))
			}
			digest, hashErr := hashPreparedGenerationMember(ctx, file, record.Size)
			if hashErr != nil || digest != record.SHA256 {
				_ = file.Close()
				return fail(errors.Join(
					errors.New("prepared legacy member bytes differ from manifest"), hashErr,
				))
			}
			prepared.members = append(prepared.members, preparedLegacyMember{
				relative: relative, path: fullPath, identity: identity,
				info: opened, file: file, size: record.Size, digest: record.SHA256,
			})
		}
	}
	if err := prepared.guardLocked(ctx); err != nil {
		return fail(err)
	}
	return prepared, nil
}

func expectedPreparedLegacyTree(
	root string,
	roots []string,
	manifest BackupManifest,
	provenance backupStoreProvenance,
	store BackupStoreManifest,
) (map[string]backupTreeKind, map[string]BackupFileManifest, error) {
	expected := map[string]backupTreeKind{".": backupTreeDirectory}
	records := make(map[string]BackupFileManifest)
	addDirectory := func(relative string) error {
		for current := relative; current != "."; current = filepath.Dir(current) {
			if kind, present := expected[current]; present && kind != backupTreeDirectory {
				return errors.New("prepared legacy tree has a file-directory collision")
			}
			if _, present := expected[current]; !present && len(expected)-1 >= backupMaxEntries {
				return errors.New("prepared legacy tree entry budget is exceeded")
			}
			expected[current] = backupTreeDirectory
		}
		return nil
	}
	addFile := func(relative string, record BackupFileManifest) error {
		if !safeBackupRelative(relative) {
			return errors.New("prepared legacy member path is invalid")
		}
		if _, present := expected[relative]; present {
			return errors.New("prepared legacy tree has a path collision")
		}
		if err := addDirectory(filepath.Dir(relative)); err != nil {
			return err
		}
		if len(expected)-1 >= backupMaxEntries {
			return errors.New("prepared legacy tree entry budget is exceeded")
		}
		expected[relative] = backupTreeFile
		records[relative] = record
		return nil
	}

	for index, sourceRoot := range provenance.legacyRoots {
		destinationRoot := filepath.Join(
			root, fmt.Sprintf("root-%06d", index), filepath.Base(filepath.Clean(sourceRoot)),
		)
		if roots[index] != destinationRoot {
			return nil, nil, errors.New("prepared legacy ordered-root provenance changed")
		}
		relativeRoot, err := filepath.Rel(root, destinationRoot)
		if err != nil || !safeBackupRelative(relativeRoot) {
			return nil, nil, errors.Join(errors.New("prepared legacy root path is invalid"), err)
		}
		if store.LegacyRootKinds[index] == "directory" {
			if err := addDirectory(relativeRoot); err != nil {
				return nil, nil, err
			}
		}
		for _, record := range manifest.Files {
			if record.StoreID != provenance.storeID || record.Role != "legacy" ||
				record.LegacyRoot != index {
				continue
			}
			relativeSource, inside, relativeErr := legacySourceRelative(sourceRoot, record.Source)
			if relativeErr != nil || !inside {
				return nil, nil, errors.Join(
					errors.New("prepared legacy member is outside its declared root"), relativeErr,
				)
			}
			destination := relativeRoot
			if relativeSource != "." {
				destination = filepath.Join(relativeRoot, relativeSource)
			}
			if err := addFile(destination, record); err != nil {
				return nil, nil, err
			}
		}
	}
	return expected, records, nil
}

func (p *preparedLegacyInputs) guard(ctx context.Context) error {
	if p == nil {
		return errors.New("prepared legacy inputs are unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.guardLocked(ctx)
}

func (p *preparedLegacyInputs) use(
	ctx context.Context,
	operation func(context.Context, []string) error,
) error {
	if p == nil || operation == nil {
		return errors.New("prepared legacy input operation is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.guardLocked(ctx); err != nil {
		return err
	}
	operationErr := operation(ctx, append([]string(nil), p.roots...))
	return errors.Join(operationErr, p.guardLocked(context.WithoutCancel(ctx)))
}

func (p *preparedLegacyInputs) guardLocked(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if p.closed || p.rootHandle == nil || p.rootFile == nil || !p.rootIdentity.Valid() {
		return errors.New("prepared legacy input seal is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootInfo, err := p.rootFile.Stat()
	rootIdentity, identityErr := backupPathMatchesOpened(
		p.root, p.rootFile, fileidentity.ObjectTypeDirectory,
	)
	if err != nil || identityErr != nil || rootIdentity != p.rootIdentity {
		return errors.Join(errors.New("prepared legacy root identity changed"), err, identityErr)
	}
	if privateErr := fileutil.ValidatePrivateDirectory(p.root, rootInfo); privateErr != nil {
		return fmt.Errorf("validate prepared legacy root: %w", privateErr)
	}
	directories := make(map[string]preparedLegacyDirectory, len(p.directories))
	for _, directory := range p.directories {
		directories[directory.relative] = directory
	}
	members := make(map[string]*preparedLegacyMember, len(p.members))
	for index := range p.members {
		members[p.members[index].relative] = &p.members[index]
	}
	seen := make(map[string]struct{}, len(p.expected))
	entryCount := 0
	if err := p.guardDirectory(ctx, ".", directories, members, seen, &entryCount); err != nil {
		return err
	}
	if len(seen) != len(p.expected) {
		return errors.New("prepared legacy inventory omits a sealed path")
	}
	return nil
}

func (p *preparedLegacyInputs) guardDirectory(
	ctx context.Context,
	relative string,
	directories map[string]preparedLegacyDirectory,
	members map[string]*preparedLegacyMember,
	seen map[string]struct{},
	entryCount *int,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	expectedDirectory, present := directories[relative]
	if !present {
		return errors.New("prepared legacy directory seal is missing")
	}
	directory, err := p.rootHandle.Open(relative)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	identity, identityErr := backupPathMatchesOpened(
		expectedDirectory.path, directory, fileidentity.ObjectTypeDirectory,
	)
	info, statErr := directory.Stat()
	if identityErr != nil || statErr != nil || identity != expectedDirectory.identity {
		return errors.Join(
			errors.New("prepared legacy directory identity changed"), identityErr, statErr,
		)
	}
	if privateErr := fileutil.ValidatePrivateDirectory(expectedDirectory.path, info); privateErr != nil {
		return fmt.Errorf("validate prepared legacy directory: %w", privateErr)
	}
	seen[relative] = struct{}{}
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			*entryCount++
			if *entryCount > len(p.expected)-1 || !validBackupPathComponent(entry.Name()) {
				return errors.New("prepared legacy inventory entry limit exceeded")
			}
			child := entry.Name()
			if relative != "." {
				child = filepath.Join(relative, child)
			}
			kind, expected := p.expected[child]
			if !expected {
				return errors.New("prepared legacy inventory contains an unexpected path")
			}
			if _, duplicate := seen[child]; duplicate {
				return errors.New("prepared legacy inventory contains a duplicate path")
			}
			switch kind {
			case backupTreeDirectory:
				if err := p.guardDirectory(ctx, child, directories, members, seen, entryCount); err != nil {
					return err
				}
			case backupTreeFile:
				member, present := members[child]
				if !present {
					return errors.New("prepared legacy member seal is missing")
				}
				if err := guardPreparedLegacyMember(ctx, member); err != nil {
					return err
				}
				seen[child] = struct{}{}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		if len(entries) == 0 {
			return errors.New("prepared legacy directory read made no progress")
		}
	}
	return nil
}

func guardPreparedLegacyMember(ctx context.Context, member *preparedLegacyMember) error {
	opened, statErr := member.file.Stat()
	identity, identityErr := backupPathMatchesOpened(
		member.path, member.file, fileidentity.ObjectTypeRegular,
	)
	if statErr != nil || identityErr != nil || identity != member.identity || opened == nil ||
		opened.Size() != member.info.Size() || opened.Mode() != member.info.Mode() ||
		!opened.ModTime().Equal(member.info.ModTime()) {
		return errors.Join(
			errors.New("prepared legacy member seal changed"), statErr, identityErr,
		)
	}
	if privateErr := fileutil.ValidatePrivateFile(member.path, opened); privateErr != nil {
		return fmt.Errorf("validate prepared legacy member: %w", privateErr)
	}
	if metadataErr := validateBackupPlatformFile(opened, member.file, 0o600); metadataErr != nil {
		return metadataErr
	}
	digest, hashErr := hashPreparedGenerationMember(ctx, member.file, member.size)
	if hashErr != nil || digest != member.digest {
		return errors.Join(errors.New("prepared legacy member bytes changed"), hashErr)
	}
	return nil
}

func (p *preparedLegacyInputs) closeUnlocked() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var result error
	for index := range p.members {
		result = errors.Join(result, p.members[index].file.Close())
	}
	if p.rootFile != nil {
		result = errors.Join(result, p.rootFile.Close())
	}
	if p.rootHandle != nil {
		result = errors.Join(result, p.rootHandle.Close())
	}
	return result
}

func (p *preparedLegacyInputs) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeUnlocked()
}
