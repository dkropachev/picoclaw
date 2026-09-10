package databasemigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type preparedGenerationMember struct {
	role     string
	path     string
	identity fileidentity.Identity
	info     os.FileInfo
	file     *os.File
	size     int64
	digest   string
}

// preparedGeneration is a descriptor-pinned, immutable proof that disposable
// source paths still name bytes reconstructed from one committed snapshot.
// Downstream migration must call sourcePath immediately inside its guarded
// provider operation; no raw string is returned by prepareGeneration.
type preparedGeneration struct {
	mu             sync.Mutex
	root           string
	path           string
	storeID        string
	liveSourcePath string
	manifestDigest string
	rootIdentity   fileidentity.Identity
	rootHandle     *os.Root
	rootFile       *os.File
	members        []preparedGenerationMember
	closed         bool
}

func sealPreparedGenerationProvenance(
	ctx context.Context,
	path string,
	manifest BackupManifest,
	provenance backupStoreProvenance,
) (*preparedGeneration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !validBackupAbsolutePath(path) {
		return nil, errors.New("prepared database generation path is invalid")
	}
	if err := validateBackupManifest(manifest); err != nil {
		return nil, fmt.Errorf("validate prepared generation manifest: %w", err)
	}
	if _, err := backupManifestStoreForProvenance(manifest, provenance); err != nil {
		return nil, err
	}
	rootPath := filepath.Dir(path)
	rootIdentity, err := backupExistingIdentity(rootPath, fileidentity.ObjectTypeDirectory)
	if err != nil {
		return nil, errors.Join(errors.New("prepared generation root identity is unavailable"), err)
	}
	rootHandle, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	rootFile, err := rootHandle.Open(".")
	if err != nil {
		return nil, errors.Join(err, rootHandle.Close())
	}
	fail := func(cause error, members []preparedGenerationMember) (*preparedGeneration, error) {
		for index := range members {
			cause = errors.Join(cause, members[index].file.Close())
		}
		return nil, errors.Join(cause, rootFile.Close(), rootHandle.Close())
	}
	manifestBytes, err := marshalBackupManifest(manifest)
	if err != nil {
		return fail(err, nil)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	prepared := &preparedGeneration{
		root: rootPath, path: path, storeID: provenance.storeID, liveSourcePath: provenance.path,
		manifestDigest: hex.EncodeToString(manifestHash[:]), rootIdentity: rootIdentity,
		rootHandle: rootHandle, rootFile: rootFile,
	}
	roles := []string{"database", "wal", "shm", "journal"}
	expected := make(map[string]BackupFileManifest, len(roles))
	for _, record := range manifest.Files {
		if record.StoreID != provenance.storeID {
			continue
		}
		switch record.Role {
		case "database", "wal", "shm", "journal":
			expected[record.Role] = record
		}
	}
	paths := generationPaths(path)
	identities := make(map[fileidentity.Identity]string, len(expected))
	for index, memberPath := range paths {
		if err := ctx.Err(); err != nil {
			return fail(err, prepared.members)
		}
		record, required := expected[roles[index]]
		info, statErr := rootHandle.Lstat(filepath.Base(memberPath))
		if errors.Is(statErr, os.ErrNotExist) {
			if required {
				return fail(errors.New("prepared generation omits a manifest member"), prepared.members)
			}
			continue
		}
		if !required {
			return fail(errors.New("prepared generation contains an unmanifested member"), prepared.members)
		}
		if statErr != nil || info == nil || !info.Mode().IsRegular() ||
			info.Mode()&os.ModeSymlink != 0 {
			return fail(errors.Join(errors.New("prepared generation member is unsafe"), statErr), prepared.members)
		}
		file, openErr := rootHandle.Open(filepath.Base(memberPath))
		if openErr != nil {
			return fail(openErr, prepared.members)
		}
		opened, statErr := file.Stat()
		identity, identityErr := backupPathMatchesOpened(
			memberPath, file, fileidentity.ObjectTypeRegular,
		)
		if statErr != nil || identityErr != nil {
			_ = file.Close()
			return fail(errors.Join(
				errors.New("prepared generation member changed while sealing"), statErr, identityErr,
			), prepared.members)
		}
		if previous, duplicate := identities[identity]; duplicate {
			_ = file.Close()
			return fail(fmt.Errorf(
				"prepared generation members %s and %s physically alias",
				previous, roles[index],
			), prepared.members)
		}
		identities[identity] = roles[index]
		if privateErr := fileutil.ValidatePrivateFile(memberPath, opened); privateErr != nil {
			_ = file.Close()
			return fail(fmt.Errorf("validate prepared generation member: %w", privateErr), prepared.members)
		}
		if metadataErr := validateBackupPlatformFile(opened, file, 0o600); metadataErr != nil {
			_ = file.Close()
			return fail(metadataErr, prepared.members)
		}
		if opened.Size() != record.Size {
			_ = file.Close()
			return fail(errors.New("prepared generation member size differs from manifest"), prepared.members)
		}
		digest, hashErr := hashPreparedGenerationMember(ctx, file, record.Size)
		if hashErr != nil || digest != record.SHA256 {
			_ = file.Close()
			return fail(errors.Join(
				errors.New("prepared generation member bytes differ from manifest"), hashErr,
			), prepared.members)
		}
		prepared.members = append(prepared.members, preparedGenerationMember{
			role: roles[index], path: memberPath, identity: identity,
			info: opened, file: file, size: record.Size, digest: record.SHA256,
		})
	}
	if err := prepared.guardLocked(ctx); err != nil {
		return fail(err, prepared.members)
	}
	return prepared, nil
}

func (p *preparedGeneration) guard(ctx context.Context) error {
	if p == nil {
		return errors.New("prepared database generation is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.guardLocked(ctx)
}

// use keeps the seal locked around one downstream provider operation and
// verifies it both before and after. The callback must open and bind the source
// generation synchronously; retaining path for later use violates the contract.
func (p *preparedGeneration) use(
	ctx context.Context,
	operation func(context.Context, string) error,
) error {
	if p == nil || operation == nil {
		return errors.New("prepared database generation operation is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.guardLocked(ctx); err != nil {
		return err
	}
	operationErr := operation(ctx, p.path)
	return errors.Join(operationErr, p.guardLocked(context.WithoutCancel(ctx)))
}

func (p *preparedGeneration) guardLocked(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if p.closed || p.rootHandle == nil || p.rootFile == nil || !p.rootIdentity.Valid() ||
		!validBackupDigest(p.manifestDigest) {
		return errors.New("prepared database generation seal is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootInfo, rootStatErr := p.rootHandle.Stat(".")
	if rootStatErr != nil || rootInfo == nil || !rootInfo.IsDir() {
		return errors.Join(errors.New("prepared generation root handle is invalid"), rootStatErr)
	}
	currentRootIdentity, identityErr := backupPathMatchesOpened(
		p.root, p.rootFile, fileidentity.ObjectTypeDirectory,
	)
	if identityErr != nil || currentRootIdentity != p.rootIdentity {
		return errors.Join(errors.New("prepared generation root identity changed"), identityErr)
	}
	if privateErr := fileutil.ValidatePrivateDirectory(p.root, rootInfo); privateErr != nil {
		return fmt.Errorf("validate prepared generation root: %w", privateErr)
	}
	directory, err := p.rootHandle.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	expectedNames := make(map[string]struct{}, len(p.members))
	for _, member := range p.members {
		expectedNames[filepath.Base(member.path)] = struct{}{}
	}
	seenNames := make(map[string]struct{}, len(expectedNames))
	for {
		entries, readErr := directory.ReadDir(len(expectedNames) + 1)
		for _, entry := range entries {
			if len(seenNames) >= len(expectedNames) {
				return errors.New("prepared generation inventory entry limit exceeded")
			}
			if _, expected := expectedNames[entry.Name()]; !expected {
				return errors.New("prepared generation inventory changed")
			}
			if _, duplicate := seenNames[entry.Name()]; duplicate {
				return errors.New("prepared generation inventory is duplicated")
			}
			seenNames[entry.Name()] = struct{}{}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		if len(entries) == 0 {
			return errors.New("prepared generation inventory read made no progress")
		}
	}
	if len(seenNames) != len(expectedNames) {
		return errors.New("prepared generation inventory changed")
	}
	for index := range p.members {
		member := &p.members[index]
		current, statErr := p.rootHandle.Lstat(filepath.Base(member.path))
		opened, openedErr := member.file.Stat()
		identity, identityErr := backupPathMatchesOpened(
			member.path, member.file, fileidentity.ObjectTypeRegular,
		)
		if statErr != nil || openedErr != nil || identityErr != nil ||
			identity != member.identity || current == nil || !current.Mode().IsRegular() ||
			opened.Size() != member.info.Size() || opened.Mode() != member.info.Mode() ||
			!opened.ModTime().Equal(member.info.ModTime()) {
			return errors.Join(
				errors.New("prepared generation member seal changed"),
				statErr, openedErr, identityErr,
			)
		}
		if privateErr := fileutil.ValidatePrivateFile(member.path, opened); privateErr != nil {
			return fmt.Errorf("validate prepared generation member: %w", privateErr)
		}
		if metadataErr := validateBackupPlatformFile(opened, member.file, 0o600); metadataErr != nil {
			return metadataErr
		}
		digest, hashErr := hashPreparedGenerationMember(ctx, member.file, member.size)
		if hashErr != nil || digest != member.digest {
			return errors.Join(errors.New("prepared generation member bytes changed"), hashErr)
		}
	}
	return nil
}

func (p *preparedGeneration) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
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

func (p *preparedGeneration) String() string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("prepared generation for %s", p.storeID)
}
