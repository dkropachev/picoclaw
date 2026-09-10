package databasemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type backupStatusPathLock struct {
	mu   sync.Mutex
	refs int
}

var backupStatusPathLocks = struct {
	sync.Mutex
	locks map[string]*backupStatusPathLock
}{locks: make(map[string]*backupStatusPathLock)}

func backupMigrationStatusPath(root, parent string) (string, error) {
	if !validBackupAbsolutePath(root) || !validBackupAbsolutePath(parent) ||
		filepath.Dir(root) != parent {
		return "", errors.New("database backup migration status namespace is invalid")
	}
	path := root + backupStatusSuffix
	if !validBackupAbsolutePath(path) || filepath.Dir(path) != parent ||
		filepath.Base(path) != filepath.Base(root)+backupStatusSuffix {
		return "", errors.New("database backup migration status path is invalid")
	}
	return path, nil
}

func lockBackupStatusPath(path string) func() {
	key := backupPathKey(path)
	backupStatusPathLocks.Lock()
	lock := backupStatusPathLocks.locks[key]
	if lock == nil {
		lock = &backupStatusPathLock{}
		backupStatusPathLocks.locks[key] = lock
	}
	lock.refs++
	backupStatusPathLocks.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		backupStatusPathLocks.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(backupStatusPathLocks.locks, key)
		}
		backupStatusPathLocks.Unlock()
	}
}

func (b *backupSession) finishMigrationStatusWithOps(
	outcome string,
	migrationErr error,
	ops backupFinishOps,
) error {
	if !validBackupMigrationOutcome(outcome, migrationErr) ||
		backupPathHasSuffix(b.root, backupPartialSuffix) {
		return errors.New("database backup migration status is invalid")
	}
	if ops.verify == nil || ops.marshal == nil || ops.marshalStatus == nil ||
		ops.read == nil || ops.readStatus == nil ||
		ops.createStatus == nil || ops.replaceStatus == nil || ops.syncDir == nil {
		return errors.New("database backup migration status operations are unavailable")
	}
	statusPath, pathErr := backupMigrationStatusPath(b.root, b.parent)
	if pathErr != nil {
		return pathErr
	}
	unlock := lockBackupStatusPath(statusPath)
	defer unlock()

	snapshotDigest, verifyErr := b.verifyStatusArchiveWithOps(ops)
	if verifyErr != nil {
		return fmt.Errorf("verify committed database backup before status update: %w", verifyErr)
	}
	current, currentPayload, currentIdentity, exists, readErr := b.readStatusRecordWithOps(
		statusPath, snapshotDigest, ops,
	)
	if readErr != nil {
		return readErr
	}
	if stateErr := b.matchOrAdoptStatusState(
		current, currentPayload, currentIdentity, exists,
	); stateErr != nil {
		return stateErr
	}

	revision := uint64(2)
	if outcome == "migration_in_progress" {
		revision = 1
		if exists || b.statusRevision != 0 {
			return errors.New("database backup migration status is already initialized")
		}
	} else if !exists || current.Revision != 1 ||
		current.Outcome != "migration_in_progress" || b.statusRevision != 1 ||
		b.statusOutcome != "migration_in_progress" {
		return errors.New("database backup migration status transition is invalid")
	}

	next := backupMigrationStatus{
		Version: backupStatusVersion, SnapshotDigest: snapshotDigest,
		Revision: revision, Outcome: outcome,
	}
	if migrationErr != nil {
		next.Error = boundedBackupError(migrationErr)
	}
	nextPayload, marshalErr := ops.marshalStatus(next)
	if marshalErr != nil {
		return marshalErr
	}

	var nextIdentity fileidentity.Identity
	if revision == 1 {
		var createErr error
		nextIdentity, createErr = ops.createStatus(b, statusPath, nextPayload)
		if createErr != nil {
			return fmt.Errorf("create database backup migration status: %w", createErr)
		}
	} else {
		var replaceErr error
		nextIdentity, replaceErr = ops.replaceStatus(
			b, statusPath, nextPayload, currentIdentity, currentPayload,
		)
		if replaceErr != nil {
			return fmt.Errorf("replace database backup migration status: %w", replaceErr)
		}
	}
	if syncErr := ops.syncDir(b.parent); syncErr != nil {
		return fmt.Errorf("sync database backup migration status: %w", syncErr)
	}
	observed, observedPayload, observedIdentity, observedExists, observationErr := b.readStatusRecordWithOps(
		statusPath, snapshotDigest, ops,
	)
	installedIdentityChanged := !nextIdentity.Valid() || observedIdentity != nextIdentity
	if observationErr != nil || !observedExists || observed != next ||
		!bytes.Equal(observedPayload, nextPayload) || installedIdentityChanged {
		return errors.Join(
			errors.New("database backup migration status changed while committing"),
			observationErr,
		)
	}
	b.setStatusState(observed, observedPayload, observedIdentity, true)

	finalDigest, finalVerifyErr := b.verifyStatusArchiveWithOps(ops)
	if finalVerifyErr != nil {
		return fmt.Errorf(
			"reverify committed database backup after status update: %w", finalVerifyErr,
		)
	}
	if finalDigest != snapshotDigest {
		return errors.New("database backup snapshot digest changed during status update")
	}
	return nil
}

func (b *backupSession) readMigrationStatusWithOps(
	ops backupFinishOps,
) (backupMigrationStatus, bool, error) {
	if b == nil || b.root == "" || b.parent != filepath.Dir(b.root) ||
		!b.identity.Valid() || !b.parentIdentity.Valid() || ops.readStatus == nil {
		return backupMigrationStatus{}, false,
			errors.New("database backup session is unavailable")
	}
	statusPath, pathErr := backupMigrationStatusPath(b.root, b.parent)
	if pathErr != nil {
		return backupMigrationStatus{}, false, pathErr
	}
	unlock := lockBackupStatusPath(statusPath)
	defer unlock()
	snapshotDigest, verifyErr := b.verifyStatusArchiveWithOps(ops)
	if verifyErr != nil {
		return backupMigrationStatus{}, false,
			fmt.Errorf("verify committed database backup before status read: %w", verifyErr)
	}
	status, payload, identity, exists, readErr := b.readStatusRecordWithOps(
		statusPath, snapshotDigest, ops,
	)
	if readErr != nil {
		return backupMigrationStatus{}, false, readErr
	}
	if stateErr := b.matchOrAdoptStatusState(status, payload, identity, exists); stateErr != nil {
		return backupMigrationStatus{}, false, stateErr
	}
	finalDigest, finalVerifyErr := b.verifyStatusArchiveWithOps(ops)
	if finalVerifyErr != nil || finalDigest != snapshotDigest {
		return backupMigrationStatus{}, false, errors.Join(
			errors.New("database backup changed during migration status read"),
			finalVerifyErr,
		)
	}
	return status, exists, nil
}

func (b *backupSession) verifyStatusArchiveWithOps(ops backupFinishOps) (string, error) {
	if b == nil || b.root == "" || b.parent != filepath.Dir(b.root) ||
		!b.identity.Valid() || !b.parentIdentity.Valid() {
		return "", errors.New("database backup status session is unavailable")
	}
	if parentErr := validatePinnedPrivateBackupDirectory(
		b.parent, b.parentIdentity,
	); parentErr != nil {
		return "", fmt.Errorf("validate database backup status parent: %w", parentErr)
	}
	if verifyErr := ops.verify(context.Background(), b); verifyErr != nil {
		return "", verifyErr
	}
	rootIdentity, rootType, rootExists, identityErr := fileidentity.ExistingWithType(b.root)
	if identityErr != nil || !rootExists || rootType != fileidentity.ObjectTypeDirectory ||
		rootIdentity != b.identity {
		return "", errors.Join(
			errors.New("database backup root identity changed before status operation"),
			identityErr,
		)
	}
	digest, digestErr := b.committedStatusDigestWithOps(ops)
	if digestErr != nil {
		return "", digestErr
	}
	if parentErr := validatePinnedPrivateBackupDirectory(
		b.parent, b.parentIdentity,
	); parentErr != nil {
		return "", fmt.Errorf("revalidate database backup status parent: %w", parentErr)
	}
	return digest, nil
}

func (b *backupSession) committedStatusDigestWithOps(ops backupFinishOps) (string, error) {
	manifestPayload, err := ops.read(
		filepath.Join(b.root, backupManifestName), backupMaxManifestSize,
	)
	if err != nil {
		return "", fmt.Errorf("read committed database backup manifest: %w", err)
	}
	canonical, marshalErr := ops.marshal(b.manifest)
	if marshalErr != nil || !bytes.Equal(manifestPayload, canonical) {
		return "", errors.Join(
			errors.New("database backup durable manifest differs from the session"),
			marshalErr,
		)
	}
	digest := sha256.Sum256(manifestPayload)
	digestText := hex.EncodeToString(digest[:])
	marker, markerErr := ops.read(filepath.Join(b.root, backupManifestHash), sha256.Size*2+2)
	if markerErr != nil || !bytes.Equal(marker, append([]byte(digestText), '\n')) {
		return "", errors.Join(
			errors.New("database backup commit marker is unavailable"), markerErr,
		)
	}
	return digestText, nil
}

func (b *backupSession) readStatusRecordWithOps(
	path string,
	snapshotDigest string,
	ops backupFinishOps,
) (
	backupMigrationStatus,
	[]byte,
	fileidentity.Identity,
	bool,
	error,
) {
	payload, identity, exists, readErr := ops.readStatus(path, backupMaxStatusSize)
	if readErr != nil || !exists {
		return backupMigrationStatus{}, nil, fileidentity.Identity{}, exists, readErr
	}
	var status backupMigrationStatus
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&status); decodeErr != nil {
		return backupMigrationStatus{}, nil, fileidentity.Identity{}, false, decodeErr
	}
	var trailing any
	if trailingErr := decoder.Decode(&trailing); !errors.Is(trailingErr, io.EOF) {
		return backupMigrationStatus{}, nil, fileidentity.Identity{}, false,
			errors.New("database backup migration status has trailing data")
	}
	canonical, marshalErr := ops.marshalStatus(status)
	if marshalErr != nil || !bytes.Equal(canonical, payload) {
		return backupMigrationStatus{}, nil, fileidentity.Identity{}, false, errors.Join(
			errors.New("database backup migration status is not canonical"), marshalErr,
		)
	}
	if status.SnapshotDigest != snapshotDigest {
		return backupMigrationStatus{}, nil, fileidentity.Identity{}, false,
			errors.New("database backup migration status names another snapshot")
	}
	if !identity.Valid() {
		return backupMigrationStatus{}, nil, fileidentity.Identity{}, false,
			errors.New("database backup migration status identity is unavailable")
	}
	return status, payload, identity, true, nil
}

func (b *backupSession) matchOrAdoptStatusState(
	status backupMigrationStatus,
	payload []byte,
	identity fileidentity.Identity,
	exists bool,
) error {
	if !b.statusKnown {
		b.setStatusState(status, payload, identity, exists)
		return nil
	}
	if !exists {
		if b.statusRevision != 0 || b.statusIdentity.Valid() || b.statusOutcome != "" {
			return errors.New("database backup migration status disappeared")
		}
		return nil
	}
	if b.statusRevision == 0 || !b.statusIdentity.Valid() ||
		identity != b.statusIdentity || status.Revision != b.statusRevision ||
		status.Outcome != b.statusOutcome || sha256.Sum256(payload) != b.statusDigest {
		return errors.New("database backup migration status identity or revision changed")
	}
	return nil
}

func (b *backupSession) setStatusState(
	status backupMigrationStatus,
	payload []byte,
	identity fileidentity.Identity,
	exists bool,
) {
	b.statusKnown = true
	if !exists {
		b.statusIdentity = fileidentity.Identity{}
		b.statusRevision = 0
		b.statusOutcome = ""
		b.statusDigest = [sha256.Size]byte{}
		return
	}
	b.statusIdentity = identity
	b.statusRevision = status.Revision
	b.statusOutcome = status.Outcome
	b.statusDigest = sha256.Sum256(payload)
}

func validBackupStatusRevision(status backupMigrationStatus) bool {
	if status.Outcome == "migration_in_progress" {
		return status.Revision == 1
	}
	return status.Revision == 2
}

func readPinnedBackupStatus(
	path string,
	maxBytes int64,
) (payload []byte, identity fileidentity.Identity, exists bool, returnErr error) {
	return readPinnedBackupStatusWithOps(path, maxBytes, defaultBackupStatusReadOps())
}

func readPinnedBackupStatusWithOps(
	path string,
	maxBytes int64,
	ops backupStatusReadOps,
) (payload []byte, identity fileidentity.Identity, exists bool, returnErr error) {
	return readPinnedPrivateBackupFileWithOps(path, maxBytes, true, ops)
}

func readLockedBackupStatus(
	file *os.File,
	path string,
	opened os.FileInfo,
	maxBytes int64,
) ([]byte, fileidentity.Identity, error) {
	return readLockedBackupStatusWithOps(
		file, path, opened, maxBytes, defaultBackupStatusReadOps(),
	)
}

func readLockedBackupStatusWithOps(
	file *os.File,
	path string,
	opened os.FileInfo,
	maxBytes int64,
	ops backupStatusReadOps,
) ([]byte, fileidentity.Identity, error) {
	if file == nil || opened == nil || maxBytes <= 0 || opened.Size() > maxBytes {
		return nil, fileidentity.Identity{},
			errors.New("database backup locked status is unavailable")
	}
	identity, identityErr := ops.openedIdentity(file, fileidentity.ObjectTypeRegular)
	if identityErr != nil {
		return nil, fileidentity.Identity{}, identityErr
	}
	if metadataErr := ops.validatePrivateFile(path, opened); metadataErr != nil {
		return nil, fileidentity.Identity{}, metadataErr
	}
	if platformErr := ops.validatePlatformFile(opened, file, 0o600); platformErr != nil {
		return nil, fileidentity.Identity{}, platformErr
	}
	if _, seekErr := ops.seek(file, 0, io.SeekStart); seekErr != nil {
		return nil, fileidentity.Identity{}, seekErr
	}
	payload, readErr := ops.readAll(io.LimitReader(file, maxBytes+1))
	if readErr != nil || int64(len(payload)) > maxBytes {
		return nil, fileidentity.Identity{}, errors.Join(
			errors.New("database backup locked status size limit is exceeded"), readErr,
		)
	}
	afterInfo, statErr := ops.stat(file)
	afterIdentity, openedIdentityErr := ops.openedIdentity(file, fileidentity.ObjectTypeRegular)
	pathIdentity, pathType, pathExists, pathErr := ops.existingWithType(path)
	var finalPrivateErr, finalPlatformErr error
	if afterInfo != nil {
		finalPrivateErr = ops.validatePrivateFile(path, afterInfo)
		finalPlatformErr = ops.validatePlatformFile(afterInfo, file, 0o600)
	}
	if statErr != nil || openedIdentityErr != nil || pathErr != nil || !pathExists ||
		pathType != fileidentity.ObjectTypeRegular || afterIdentity != identity ||
		pathIdentity != identity || afterInfo == nil || opened.Size() != afterInfo.Size() ||
		opened.Mode() != afterInfo.Mode() || !opened.ModTime().Equal(afterInfo.ModTime()) ||
		finalPrivateErr != nil || finalPlatformErr != nil {
		return nil, fileidentity.Identity{}, errors.Join(
			errors.New("database backup locked status changed while reading"),
			statErr, openedIdentityErr, pathErr, finalPrivateErr, finalPlatformErr,
		)
	}
	return payload, identity, nil
}

type stagedBackupStatus struct {
	path     string
	identity fileidentity.Identity
	payload  []byte
	parent   string
}

func stageBackupMigrationStatus(
	session *backupSession,
	payload []byte,
) (result *stagedBackupStatus, returnErr error) {
	return stageBackupMigrationStatusWithOps(
		session, payload, defaultBackupStatusStageOps(),
	)
}

func stageBackupMigrationStatusWithOps(
	session *backupSession,
	payload []byte,
	ops backupStatusStageOps,
) (result *stagedBackupStatus, returnErr error) {
	if session == nil || !validBackupAbsolutePath(session.parent) ||
		len(payload) == 0 || int64(len(payload)) > backupMaxStatusSize {
		return nil, errors.New("database backup staged status input is invalid")
	}
	if parentErr := ops.validateParent(
		session.parent, session.parentIdentity,
	); parentErr != nil {
		return nil, parentErr
	}
	placeholder, createErr := ops.createTemp(
		session.parent, ".database-migration-status-*.partial",
	)
	if createErr != nil {
		return nil, createErr
	}
	path := placeholder.Name()
	if removeErr := removeBackupStatusPlaceholderWithOps(
		path,
		placeholder,
		backupStatusPlaceholderOps{
			openedIdentity: ops.openedIdentity,
			stat:           ops.stat,
			close:          ops.close,
			removePinned:   ops.removePinned,
		},
	); removeErr != nil {
		return nil, removeErr
	}
	if writeErr := ops.writeExclusive(path, payload, 0o600); writeErr != nil {
		return nil, writeErr
	}
	identity, objectType, exists, identityErr := ops.existingWithType(path)
	if identityErr != nil || !exists || objectType != fileidentity.ObjectTypeRegular {
		return nil, errors.Join(
			errors.New("database backup staged status identity is unavailable"), identityErr,
		)
	}
	keep := false
	defer func() {
		if !keep {
			returnErr = errors.Join(returnErr, ops.removePinned(path, identity))
		}
	}()
	actual, readIdentity, readExists, readErr := ops.readStatus(path, backupMaxStatusSize)
	if readErr != nil || !readExists || readIdentity != identity || !bytes.Equal(actual, payload) {
		return nil, errors.Join(errors.New("database backup staged status is invalid"), readErr)
	}
	if parentErr := ops.validateParent(
		session.parent, session.parentIdentity,
	); parentErr != nil {
		return nil, parentErr
	}
	keep = true
	return &stagedBackupStatus{
		path: path, identity: identity,
		payload: actual, parent: session.parent,
	}, nil
}

func (status *stagedBackupStatus) remove() error {
	if status == nil || status.path == "" || status.parent != filepath.Dir(status.path) {
		return errors.New("database backup staged status is unavailable")
	}
	return removePinnedBackupFile(status.path, status.identity)
}

func createInitialBackupMigrationStatus(
	session *backupSession,
	path string,
	payload []byte,
) (result fileidentity.Identity, returnErr error) {
	if session == nil {
		return fileidentity.Identity{}, errors.New("database backup status session is unavailable")
	}
	expectedPath, pathErr := backupMigrationStatusPath(session.root, session.parent)
	if pathErr != nil || path != expectedPath {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup initial status path is invalid"), pathErr,
		)
	}
	staged, stageErr := stageBackupMigrationStatus(session, payload)
	if stageErr != nil {
		return fileidentity.Identity{}, stageErr
	}
	published := false
	defer func() {
		if !published {
			returnErr = errors.Join(returnErr, staged.remove())
		}
	}()
	if parentErr := validatePinnedPrivateBackupDirectory(
		session.parent, session.parentIdentity,
	); parentErr != nil {
		return fileidentity.Identity{}, parentErr
	}
	if publishErr := publishBackupDirectory(staged.path, path); publishErr != nil {
		return fileidentity.Identity{}, publishErr
	}
	published = true
	if syncErr := fileutil.SyncDirectory(session.parent); syncErr != nil {
		return fileidentity.Identity{}, syncErr
	}
	actual, identity, exists, readErr := readPinnedBackupStatus(path, backupMaxStatusSize)
	if readErr != nil || !exists || identity != staged.identity ||
		!bytes.Equal(actual, staged.payload) {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup initial status publication is unproven"), readErr,
		)
	}
	if parentErr := validatePinnedPrivateBackupDirectory(
		session.parent, session.parentIdentity,
	); parentErr != nil {
		return fileidentity.Identity{}, parentErr
	}
	return identity, nil
}

func replaceBackupMigrationStatus(
	session *backupSession,
	path string,
	payload []byte,
	expectedIdentity fileidentity.Identity,
	expectedPayload []byte,
) (result fileidentity.Identity, returnErr error) {
	if session == nil || path == "" || !expectedIdentity.Valid() ||
		len(expectedPayload) == 0 {
		return fileidentity.Identity{},
			errors.New("database backup status replacement input is invalid")
	}
	expectedPath, pathErr := backupMigrationStatusPath(session.root, session.parent)
	if pathErr != nil || path != expectedPath {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup status replacement path is invalid"), pathErr,
		)
	}
	if parentErr := validatePinnedPrivateBackupDirectory(
		session.parent, session.parentIdentity,
	); parentErr != nil {
		return fileidentity.Identity{}, parentErr
	}
	currentFile, currentInfo, openErr := openPinnedBackupPath(path, false)
	if openErr != nil {
		return fileidentity.Identity{}, openErr
	}
	unlockStatus, lockErr := lockBackupStatusHandle(currentFile)
	if lockErr != nil {
		return fileidentity.Identity{}, errors.Join(lockErr, currentFile.Close())
	}
	releasedStatus := false
	releaseStatus := func() error {
		if releasedStatus {
			return nil
		}
		releasedStatus = true
		return errors.Join(unlockStatus(), currentFile.Close())
	}
	defer func() { returnErr = errors.Join(returnErr, releaseStatus()) }()
	lockedPayload, lockedIdentity, lockedErr := readLockedBackupStatus(
		currentFile, path, currentInfo, backupMaxStatusSize,
	)
	if lockedErr != nil || lockedIdentity != expectedIdentity ||
		!bytes.Equal(lockedPayload, expectedPayload) {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup migration status changed before locking"), lockedErr,
		)
	}
	staged, stageErr := stageBackupMigrationStatus(session, payload)
	if stageErr != nil {
		return fileidentity.Identity{}, stageErr
	}
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			returnErr = errors.Join(returnErr, staged.remove())
		}
	}()
	// Windows LockFileEx blocks data reads through another handle, including one
	// opened by this process. Re-read through the handle that owns the lock.
	currentPayload, currentIdentity, currentErr := readLockedBackupStatusForReplacement(
		currentFile, path, currentInfo, backupMaxStatusSize, readLockedBackupStatus,
	)
	if currentErr != nil || currentIdentity != expectedIdentity ||
		!bytes.Equal(currentPayload, expectedPayload) {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup migration status changed before replacement"), currentErr,
		)
	}
	displacedPath, exchangeErr := exchangeBackupStatusFile(staged.path, path)
	if exchangeErr != nil {
		return fileidentity.Identity{}, exchangeErr
	}
	displacedPayload, displacedIdentity, displacedErr := readLockedBackupStatus(
		currentFile, displacedPath, currentInfo, backupMaxStatusSize,
	)
	installedPayload, installedIdentity, installedExists, installedErr := readPinnedBackupStatus(
		path, backupMaxStatusSize,
	)
	if validationErr := validateExchangedBackupStatus(
		expectedPayload,
		expectedIdentity,
		staged,
		displacedPayload,
		displacedIdentity,
		displacedErr,
		installedPayload,
		installedIdentity,
		installedExists,
		installedErr,
		func() error {
			return rollbackBackupStatusFile(staged.path, path, displacedPath)
		},
	); validationErr != nil {
		return fileidentity.Identity{}, validationErr
	}
	if releaseErr := releaseStatus(); releaseErr != nil {
		return fileidentity.Identity{}, releaseErr
	}
	// The new record now owns the destination and the expected old record owns
	// the displaced name. Deferred cleanup must not compare that old identity
	// with the new record's creation metadata.
	keepTemporary = true
	if removeErr := removePinnedBackupFile(displacedPath, displacedIdentity); removeErr != nil {
		return fileidentity.Identity{}, fmt.Errorf(
			"remove displaced database backup status: %w", removeErr,
		)
	}
	finalPayload, finalIdentity, finalExists, finalErr := readPinnedBackupStatus(
		path, backupMaxStatusSize,
	)
	if finalErr != nil || !finalExists || finalIdentity != staged.identity ||
		!bytes.Equal(finalPayload, staged.payload) {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup migration status replacement is unproven"), finalErr,
		)
	}
	if parentErr := validatePinnedPrivateBackupDirectory(
		session.parent, session.parentIdentity,
	); parentErr != nil {
		return fileidentity.Identity{}, parentErr
	}
	return finalIdentity, nil
}

func validateExchangedBackupStatus(
	expectedPayload []byte,
	expectedIdentity fileidentity.Identity,
	staged *stagedBackupStatus,
	displacedPayload []byte,
	displacedIdentity fileidentity.Identity,
	displacedErr error,
	installedPayload []byte,
	installedIdentity fileidentity.Identity,
	installedExists bool,
	installedErr error,
	rollback func() error,
) error {
	displacedExact := displacedErr == nil && displacedIdentity == expectedIdentity &&
		bytes.Equal(displacedPayload, expectedPayload)
	if !displacedExact {
		return errors.Join(
			errors.New("database backup migration status displaced incumbent is unproven"),
			displacedErr,
		)
	}
	installedExact := staged != nil && installedErr == nil && installedExists &&
		installedIdentity == staged.identity && bytes.Equal(installedPayload, staged.payload)
	if installedExact {
		return nil
	}
	var rollbackErr error
	if rollback == nil {
		rollbackErr = errors.New("database backup migration status rollback is unavailable")
	} else {
		rollbackErr = rollback()
	}
	return errors.Join(
		errors.New("database backup migration status installed record is unproven"),
		installedErr,
		rollbackErr,
	)
}

func readLockedBackupStatusForReplacement(
	file *os.File,
	path string,
	opened os.FileInfo,
	maxBytes int64,
	read func(*os.File, string, os.FileInfo, int64) ([]byte, fileidentity.Identity, error),
) ([]byte, fileidentity.Identity, error) {
	if read == nil {
		return nil, fileidentity.Identity{},
			errors.New("database backup locked status reader is unavailable")
	}
	return read(file, path, opened, maxBytes)
}
