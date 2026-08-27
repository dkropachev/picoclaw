package tools

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	applyPatchTransactionStateDirectory        = "apply_patch_transactions"
	applyPatchTransactionInitLockFile          = "init.lock"
	applyPatchTransactionAuthenticationFile    = "auth.key"
	applyPatchTransactionWorkspacesDirectory   = "workspaces"
	applyPatchTransactionWorkspaceBindingFile  = "workspace.binding"
	applyPatchTransactionWorkspaceLockFile     = "workspace.lock"
	applyPatchTransactionAuthenticationBytes   = 32
	applyPatchTransactionWorkspacePathLimit    = 32 * 1024
	applyPatchTransactionWorkspaceBindingMagic = "picoclaw-apply-patch-workspace-binding-v1\x00"
)

// applyPatchTransactionStateRoot is a construction-time snapshot. The path is
// absolute and detached from configuration; every existing component is also
// pinned so initialization cannot silently move beneath a replaced ancestor.
type applyPatchTransactionStateRoot struct {
	path   string
	fences []applyPatchTransactionStateFence
}

type applyPatchTransactionStateFence struct {
	path string
	info os.FileInfo
}

// applyPatchTransactionState owns the pinned private root and authentication
// key. It is opened once per tool execution and closed after its workspace
// lock. The raw key never leaves this type except as a detached fixed array.
type applyPatchTransactionState struct {
	mu sync.Mutex

	prepared       applyPatchTransactionStateRoot
	root           *os.Root
	rootInfo       os.FileInfo
	initLockInfo   os.FileInfo
	authInfo       os.FileInfo
	authentication [applyPatchTransactionAuthenticationBytes]byte
	keyID          string
	active         int
	closed         bool
}

// applyPatchTransactionWorkspaceState retains the persistent workspace
// directory anchor and the handle-owned kernel lock. Closing it releases only
// this exact lock; persistent directories and lock files intentionally remain.
type applyPatchTransactionWorkspaceState struct {
	mu sync.Mutex

	state              *applyPatchTransactionState
	root               *os.Root
	rootInfo           os.FileInfo
	lockInfo           os.FileInfo
	lock               applyPatchTransactionHeldLock
	canonicalWorkspace string
	workspaceDigest    string
	relativeDirectory  string
	absoluteDirectory  string
	closed             bool
}

type applyPatchTransactionHeldLock interface {
	Close() error
	fileInfo() (os.FileInfo, error)
}

func defaultApplyPatchTransactionStateRoot() string {
	return filepath.Join(config.GetHome(), applyPatchTransactionStateDirectory)
}

// prepareApplyPatchTransactionStateRoot freezes and validates the external
// state path without creating it. allowRoots must contain concrete canonical
// write-allow roots, not regular expressions. Any overlap is rejected because
// a sibling filesystem tool could otherwise disclose or alter authentication
// and recovery state.
func prepareApplyPatchTransactionStateRoot(
	workspace string,
	configuredRoot string,
	allowRoots []string,
) (applyPatchTransactionStateRoot, error) {
	workspaceSnapshot, err := snapshotApplyPatchWorkspace(workspace)
	if err != nil {
		return applyPatchTransactionStateRoot{}, fmt.Errorf(
			"prepare apply-patch transaction state: invalid workspace",
		)
	}
	rootPath := configuredRoot
	if rootPath == "" {
		rootPath = defaultApplyPatchTransactionStateRoot()
	}
	if rootPath == "" || rootPath != strings.TrimSpace(rootPath) ||
		!utf8.ValidString(rootPath) || strings.ContainsRune(rootPath, '\x00') {
		return applyPatchTransactionStateRoot{}, errors.New(
			"prepare apply-patch transaction state: invalid root",
		)
	}
	rootPath, err = filepath.Abs(filepath.Clean(rootPath))
	if err != nil {
		return applyPatchTransactionStateRoot{}, errors.New(
			"prepare apply-patch transaction state: invalid root",
		)
	}
	if err = validateApplyPatchTransactionStatePath(rootPath); err != nil {
		return applyPatchTransactionStateRoot{}, err
	}
	rootPath, err = resolveApplyPatchPathAgainstExistingAncestor(rootPath)
	if err != nil {
		return applyPatchTransactionStateRoot{}, errors.New(
			"prepare apply-patch transaction state: root cannot be resolved",
		)
	}
	rootPath = filepath.Clean(rootPath)
	if applyPatchTransactionPathsOverlap(rootPath, workspaceSnapshot.canonical) {
		return applyPatchTransactionStateRoot{}, errors.New(
			"prepare apply-patch transaction state: root overlaps workspace authority",
		)
	}
	for index, allowRoot := range append([]string(nil), allowRoots...) {
		canonical, allowErr := validateApplyPatchTransactionAllowRoot(allowRoot)
		if allowErr != nil {
			return applyPatchTransactionStateRoot{}, fmt.Errorf(
				"prepare apply-patch transaction state: write-allow root %d is invalid",
				index,
			)
		}
		if applyPatchTransactionPathsOverlap(rootPath, canonical) {
			return applyPatchTransactionStateRoot{}, errors.New(
				"prepare apply-patch transaction state: root overlaps write authority",
			)
		}
	}
	fences, err := captureApplyPatchTransactionStateFences(rootPath)
	if err != nil {
		return applyPatchTransactionStateRoot{}, err
	}
	return applyPatchTransactionStateRoot{
		path: rootPath, fences: append([]applyPatchTransactionStateFence(nil), fences...),
	}, nil
}

func validateApplyPatchTransactionAllowRoot(path string) (string, error) {
	if path == "" || path != strings.TrimSpace(path) || !utf8.ValidString(path) ||
		strings.ContainsRune(path, '\x00') || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path {
		return "", errors.New("invalid write-allow root")
	}
	canonical, err := resolveApplyPatchPathAgainstExistingAncestor(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(canonical), nil
}

func applyPatchTransactionPathsOverlap(first, second string) bool {
	return applyPatchPathWithinIdentity(first, second) ||
		applyPatchPathWithinIdentity(second, first)
}

func captureApplyPatchTransactionStateFences(
	path string,
) ([]applyPatchTransactionStateFence, error) {
	if err := validateApplyPatchTransactionStatePath(path); err != nil {
		return nil, err
	}
	fences := make([]applyPatchTransactionStateFence, 0, 8)
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		switch {
		case err == nil:
			if err = validateApplyPatchTransactionStatePathEntry(current, info); err != nil {
				return nil, err
			}
			fences = append(fences, applyPatchTransactionStateFence{path: current, info: info})
		case !os.IsNotExist(err):
			return nil, errors.New("inspect apply-patch transaction state root")
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	if len(fences) == 0 {
		return nil, errors.New("inspect apply-patch transaction state root")
	}
	return fences, nil
}

func revalidateApplyPatchTransactionStateFences(
	prepared applyPatchTransactionStateRoot,
) error {
	if prepared.path == "" || !filepath.IsAbs(prepared.path) ||
		filepath.Clean(prepared.path) != prepared.path || len(prepared.fences) == 0 {
		return errors.New("apply-patch transaction state root is unavailable")
	}
	if err := validateApplyPatchTransactionStatePath(prepared.path); err != nil {
		return err
	}
	for _, fence := range prepared.fences {
		current, err := os.Lstat(fence.path)
		if err != nil || !os.SameFile(fence.info, current) ||
			current.Mode() != fence.info.Mode() {
			return errors.New("apply-patch transaction state root changed")
		}
		if err = validateApplyPatchTransactionStatePathEntry(fence.path, current); err != nil {
			return err
		}
	}
	return nil
}

func openApplyPatchTransactionState(
	ctx context.Context,
	prepared applyPatchTransactionStateRoot,
) (*applyPatchTransactionState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := revalidateApplyPatchTransactionStateFences(prepared); err != nil {
		return nil, err
	}
	rootExisted := true
	if _, err := os.Lstat(prepared.path); os.IsNotExist(err) {
		rootExisted = false
	} else if err != nil {
		return nil, errors.New("inspect apply-patch transaction state root")
	}
	if err := fileutil.MkdirAllDurable(prepared.path, 0o700); err != nil {
		return nil, fmt.Errorf("create apply-patch transaction state root: %w", err)
	}
	if !rootExisted {
		if err := os.Chmod(prepared.path, 0o700); err != nil {
			return nil, fmt.Errorf("secure apply-patch transaction state root: %w", err)
		}
	}
	if err := revalidateApplyPatchTransactionStateFences(prepared); err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(prepared.path)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("apply-patch transaction state root is not a real directory")
	}
	if err = validateApplyPatchTransactionPrivateObject(rootInfo, true); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(prepared.path)
	if err != nil {
		return nil, fmt.Errorf("open apply-patch transaction state root: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = root.Close()
		}
	}()
	anchoredInfo, err := root.Lstat(".")
	if err != nil || !os.SameFile(rootInfo, anchoredInfo) {
		return nil, errors.Join(
			errors.New("apply-patch transaction state root changed while opening"),
			err,
		)
	}
	initLock, err := acquireApplyPatchTransactionFileLock(
		ctx,
		filepath.Join(prepared.path, applyPatchTransactionInitLockFile),
	)
	if err != nil {
		return nil, err
	}
	defer initLock.Close()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = revalidateApplyPatchTransactionNamedRoot(root, prepared.path, rootInfo); err != nil {
		return nil, err
	}
	initLockInfo, err := validateApplyPatchTransactionLockedFile(
		root,
		applyPatchTransactionInitLockFile,
		initLock,
	)
	if err != nil {
		return nil, err
	}
	authentication, authInfo, err := initializeApplyPatchTransactionAuthentication(
		root,
		prepared.path,
	)
	if err != nil {
		return nil, err
	}
	if err = revalidateApplyPatchTransactionNamedRoot(root, prepared.path, rootInfo); err != nil {
		return nil, err
	}
	if err = revalidateApplyPatchTransactionRegular(
		root,
		applyPatchTransactionAuthenticationFile,
		authInfo,
	); err != nil {
		return nil, err
	}
	keyDigest := sha256.Sum256(authentication[:])
	state := &applyPatchTransactionState{
		prepared: applyPatchTransactionStateRoot{
			path:   prepared.path,
			fences: append([]applyPatchTransactionStateFence(nil), prepared.fences...),
		},
		root:           root,
		rootInfo:       rootInfo,
		initLockInfo:   initLockInfo,
		authInfo:       authInfo,
		authentication: authentication,
		keyID:          hex.EncodeToString(keyDigest[:]),
	}
	failed = false
	return state, nil
}

func initializeApplyPatchTransactionAuthentication(
	root *os.Root,
	rootPath string,
) ([applyPatchTransactionAuthenticationBytes]byte, os.FileInfo, error) {
	var authentication [applyPatchTransactionAuthenticationBytes]byte
	data, info, err := readApplyPatchTransactionPrivateRegular(
		root,
		applyPatchTransactionAuthenticationFile,
		applyPatchTransactionAuthenticationBytes,
	)
	if err == nil {
		if cleanupErr := cleanupApplyPatchTransactionPrivateStage(
			root,
			rootPath,
			applyPatchTransactionAuthenticationFile,
			data,
			true,
		); cleanupErr != nil {
			return authentication, nil, cleanupErr
		}
		copy(authentication[:], data)
		return authentication, info, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return authentication, nil, err
	}
	if _, err = rand.Read(authentication[:]); err != nil {
		return authentication, nil, fmt.Errorf(
			"create apply-patch transaction authentication key: %w",
			err,
		)
	}
	if err = publishApplyPatchTransactionPrivateRegular(
		root,
		rootPath,
		applyPatchTransactionAuthenticationFile,
		authentication[:],
	); err != nil {
		clear(authentication[:])
		return authentication, nil, err
	}
	data, info, err = readApplyPatchTransactionPrivateRegular(
		root,
		applyPatchTransactionAuthenticationFile,
		applyPatchTransactionAuthenticationBytes,
	)
	if err != nil {
		clear(authentication[:])
		return authentication, nil, err
	}
	// A create-only loser always trusts the durably reread winner, never its
	// discarded staged bytes.
	copy(authentication[:], data)
	return authentication, info, nil
}

func cleanupApplyPatchTransactionPrivateStage(
	root *os.Root,
	rootPath string,
	name string,
	expected []byte,
	allowDifferent bool,
) error {
	stage := "." + name + ".stage"
	info, err := root.Lstat(stage)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if validationErr := validateApplyPatchTransactionPrivateObject(info, false); validationErr != nil ||
		info.Size() != int64(len(expected)) {
		return errors.Join(
			errors.New("apply-patch transaction private stage conflict"),
			validationErr,
		)
	}
	data, readInfo, err := readApplyPatchTransactionPrivateRegular(
		root,
		stage,
		len(expected),
	)
	if err != nil || !os.SameFile(info, readInfo) ||
		!allowDifferent && !hmac.Equal(data, expected) {
		return errors.Join(
			errors.New("apply-patch transaction private stage content conflict"),
			err,
		)
	}
	if err := removeApplyPatchTransactionExactRootEntry(root, stage, info); err != nil {
		return err
	}
	return fileutil.SyncDirectory(rootPath)
}

func publishApplyPatchTransactionPrivateRegular(
	root *os.Root,
	rootPath string,
	name string,
	data []byte,
) error {
	if root == nil || validateApplyPatchTxnBasename(name) != nil {
		return errors.New("apply-patch transaction private file is invalid")
	}
	stage := "." + name + ".stage"
	stageInfo, inspectErr := root.Lstat(stage)
	if errors.Is(inspectErr, os.ErrNotExist) {
		file, createErr := root.OpenFile(stage, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return fmt.Errorf("create apply-patch transaction private stage: %w", createErr)
		}
		var statErr error
		stageInfo, statErr = file.Stat()
		writeErr := writeApplyPatchTransactionSyncedFile(file, data)
		closeErr := file.Close()
		if statErr != nil || writeErr != nil || closeErr != nil {
			_ = removeApplyPatchTransactionExactRootEntry(root, stage, stageInfo)
			return errors.Join(statErr, writeErr, closeErr)
		}
	} else if inspectErr != nil {
		return inspectErr
	} else {
		validationErr := validateApplyPatchTransactionPrivateObject(
			stageInfo,
			false,
		)
		if validationErr == nil && stageInfo.Size() != int64(len(data)) &&
			name == applyPatchTransactionAuthenticationFile {
			if removeErr := removeApplyPatchTransactionExactRootEntry(
				root,
				stage,
				stageInfo,
			); removeErr != nil {
				return removeErr
			}
			if syncErr := fileutil.SyncDirectory(rootPath); syncErr != nil {
				return syncErr
			}
			return publishApplyPatchTransactionPrivateRegular(root, rootPath, name, data)
		}
		if validationErr != nil || stageInfo.Size() != int64(len(data)) {
			return errors.Join(
				errors.New("apply-patch transaction private stage conflict"),
				validationErr,
			)
		}
		stagedData, _, readErr := readApplyPatchTransactionPrivateRegular(
			root,
			stage,
			len(data),
		)
		if readErr != nil || name != applyPatchTransactionAuthenticationFile &&
			!hmac.Equal(stagedData, data) {
			return errors.Join(
				errors.New("apply-patch transaction private stage content conflict"),
				readErr,
			)
		}
	}
	cleanup := func() error {
		removeErr := removeApplyPatchTransactionExactRootEntry(root, stage, stageInfo)
		if removeErr == nil {
			removeErr = fileutil.SyncDirectory(rootPath)
		}
		return removeErr
	}
	linkErr := root.Link(stage, name)
	if linkErr != nil && !errors.Is(linkErr, os.ErrExist) {
		return errors.Join(
			fmt.Errorf("publish apply-patch transaction private file: %w", linkErr),
			cleanup(),
		)
	}
	if linkErr == nil {
		if syncErr := fileutil.SyncDirectory(rootPath); syncErr != nil {
			return errors.Join(syncErr, cleanup())
		}
	}
	return cleanup()
}

func writeApplyPatchTransactionSyncedFile(file *os.File, data []byte) error {
	if file == nil {
		return errors.New("apply-patch transaction private stage is unavailable")
	}
	if err := file.Chmod(0o600); err != nil {
		return fmt.Errorf("secure apply-patch transaction private stage: %w", err)
	}
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return fmt.Errorf("write apply-patch transaction private stage: %w", err)
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync apply-patch transaction private stage: %w", err)
	}
	return nil
}

func removeApplyPatchTransactionExactRootEntry(
	root *os.Root,
	name string,
	expected os.FileInfo,
) error {
	if root == nil || expected == nil {
		return errors.New("apply-patch transaction private stage identity is unavailable")
	}
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(current, expected) || !current.Mode().IsRegular() {
		return errors.New("apply-patch transaction private stage changed")
	}
	return root.Remove(name)
}

func readApplyPatchTransactionPrivateRegular(
	root *os.Root,
	name string,
	expectedLength int,
) ([]byte, os.FileInfo, error) {
	if root == nil || expectedLength < 0 {
		return nil, nil, errors.New("apply-patch transaction private file is invalid")
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() != int64(expectedLength) {
		return nil, nil, errors.New("apply-patch transaction private file is invalid")
	}
	if err = validateApplyPatchTransactionPrivateObject(info, false); err != nil {
		return nil, nil, err
	}
	file, err := root.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return nil, nil, err
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, nil, errors.Join(
			errors.New("apply-patch transaction private file changed while opening"),
			statErr,
		)
	}
	data, readErr := io.ReadAll(io.LimitReader(file, int64(expectedLength)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) != expectedLength {
		return nil, nil, errors.Join(
			errors.New("apply-patch transaction private file has invalid length"),
			readErr,
			closeErr,
		)
	}
	if err = revalidateApplyPatchTransactionRegular(root, name, info); err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

func revalidateApplyPatchTransactionRegular(
	root *os.Root,
	name string,
	expected os.FileInfo,
) error {
	if root == nil || expected == nil {
		return errors.New("apply-patch transaction private file identity is unavailable")
	}
	current, err := root.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(current, expected) ||
		current.Mode() != expected.Mode() || current.Size() != expected.Size() {
		return errors.Join(
			errors.New("apply-patch transaction private file changed"),
			err,
		)
	}
	return validateApplyPatchTransactionPrivateObject(current, false)
}

func revalidateApplyPatchTransactionNamedRoot(
	root *os.Root,
	path string,
	expected os.FileInfo,
) error {
	if root == nil || expected == nil {
		return errors.New("apply-patch transaction state root is unavailable")
	}
	named, err := os.Lstat(path)
	if err != nil || !named.IsDir() || named.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(named, expected) {
		return errors.Join(errors.New("apply-patch transaction state root changed"), err)
	}
	anchored, err := root.Lstat(".")
	if err != nil || !os.SameFile(anchored, expected) {
		return errors.Join(errors.New("apply-patch transaction state root changed"), err)
	}
	return validateApplyPatchTransactionPrivateObject(named, true)
}

func validateApplyPatchTransactionLockedFile(
	root *os.Root,
	name string,
	lock applyPatchTransactionHeldLock,
) (os.FileInfo, error) {
	if root == nil || lock == nil {
		return nil, errors.New("apply-patch transaction lock is unavailable")
	}
	handleInfo, err := lock.fileInfo()
	if err != nil {
		return nil, err
	}
	namedInfo, err := root.Lstat(name)
	if err != nil || !namedInfo.Mode().IsRegular() ||
		namedInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(namedInfo, handleInfo) {
		return nil, errors.Join(errors.New("apply-patch transaction lock changed"), err)
	}
	if err = validateApplyPatchTransactionPrivateObject(namedInfo, false); err != nil {
		return nil, err
	}
	return namedInfo, nil
}

func (state *applyPatchTransactionState) lockWorkspace(
	ctx context.Context,
	canonicalWorkspace string,
) (*applyPatchTransactionWorkspaceState, error) {
	if state == nil {
		return nil, errors.New("apply-patch transaction state is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if canonicalWorkspace == "" || !filepath.IsAbs(canonicalWorkspace) ||
		filepath.Clean(canonicalWorkspace) != canonicalWorkspace ||
		canonicalWorkspace != strings.TrimSpace(canonicalWorkspace) ||
		!utf8.ValidString(canonicalWorkspace) ||
		strings.ContainsRune(canonicalWorkspace, '\x00') ||
		len(canonicalWorkspace) > applyPatchTransactionWorkspacePathLimit {
		return nil, errors.New("apply-patch transaction workspace binding is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workspaceInfo, err := os.Stat(canonicalWorkspace)
	if err != nil || !workspaceInfo.IsDir() {
		return nil, errors.New("apply-patch transaction workspace binding is invalid")
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(canonicalWorkspace)
	if err != nil {
		return nil, errors.New("apply-patch transaction workspace binding is invalid")
	}
	resolvedWorkspace, err = filepath.Abs(filepath.Clean(resolvedWorkspace))
	if err != nil {
		return nil, errors.New("apply-patch transaction workspace binding is invalid")
	}
	resolvedInfo, err := os.Stat(resolvedWorkspace)
	if err != nil || !resolvedInfo.IsDir() || !os.SameFile(workspaceInfo, resolvedInfo) {
		return nil, errors.New("apply-patch transaction workspace binding is invalid")
	}
	canonicalWorkspace = resolvedWorkspace
	state.mu.Lock()
	defer state.mu.Unlock()
	if err = state.revalidateLocked(); err != nil {
		return nil, err
	}
	if applyPatchTransactionPathsOverlap(state.prepared.path, canonicalWorkspace) {
		return nil, errors.New(
			"apply-patch transaction state root overlaps workspace authority",
		)
	}
	physicalIdentity, err := applyPatchTxnIdentityFromFileInfo(resolvedInfo, "directory")
	if err != nil {
		return nil, err
	}
	digest, err := applyPatchTxnWorkspaceIdentityDigest(physicalIdentity)
	if err != nil {
		return nil, err
	}
	workspacesRoot, workspacesInfo, err := ensureApplyPatchTransactionPrivateDirectory(
		state.root,
		state.prepared.path,
		applyPatchTransactionWorkspacesDirectory,
	)
	if err != nil {
		return nil, err
	}
	defer workspacesRoot.Close()
	directoryPath := filepath.Join(
		state.prepared.path,
		applyPatchTransactionWorkspacesDirectory,
		digest,
	)
	workspaceRoot, workspaceInfo, err := ensureApplyPatchTransactionPrivateDirectory(
		workspacesRoot,
		filepath.Join(state.prepared.path, applyPatchTransactionWorkspacesDirectory),
		digest,
	)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = workspaceRoot.Close()
		}
	}()
	if !workspacesInfo.IsDir() || !workspaceInfo.IsDir() {
		return nil, errors.New("apply-patch transaction workspace state is invalid")
	}
	lock, err := acquireApplyPatchTransactionFileLock(
		ctx,
		filepath.Join(directoryPath, applyPatchTransactionWorkspaceLockFile),
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if failed {
			_ = lock.Close()
		}
	}()
	if err = state.revalidateLocked(); err != nil {
		return nil, err
	}
	if err = revalidateApplyPatchTransactionDirectory(
		state.root,
		applyPatchTransactionWorkspacesDirectory,
		workspacesInfo,
	); err != nil {
		return nil, err
	}
	if err = revalidateApplyPatchTransactionDirectory(
		workspacesRoot,
		digest,
		workspaceInfo,
	); err != nil {
		return nil, err
	}
	lockInfo, err := validateApplyPatchTransactionLockedFile(
		workspaceRoot,
		applyPatchTransactionWorkspaceLockFile,
		lock,
	)
	if err != nil {
		return nil, err
	}
	if err = ensureApplyPatchTransactionWorkspaceBinding(
		workspaceRoot,
		directoryPath,
		canonicalWorkspace,
		state.authentication,
	); err != nil {
		return nil, err
	}
	state.active++
	workspaceState := &applyPatchTransactionWorkspaceState{
		state:              state,
		root:               workspaceRoot,
		rootInfo:           workspaceInfo,
		lockInfo:           lockInfo,
		lock:               lock,
		canonicalWorkspace: canonicalWorkspace,
		workspaceDigest:    digest,
		relativeDirectory:  filepath.ToSlash(filepath.Join(applyPatchTransactionWorkspacesDirectory, digest)),
		absoluteDirectory:  directoryPath,
	}
	failed = false
	return workspaceState, nil
}

func applyPatchTransactionWorkspaceDigest(canonicalWorkspace string) string {
	digest := sha256.Sum256([]byte(canonicalWorkspace))
	return hex.EncodeToString(digest[:])
}

func ensureApplyPatchTransactionPrivateDirectory(
	parent *os.Root,
	parentPath string,
	name string,
) (*os.Root, os.FileInfo, error) {
	if parent == nil || validateApplyPatchTxnBasename(name) != nil {
		return nil, nil, errors.New("apply-patch transaction private directory is invalid")
	}
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		createErr := parent.Mkdir(name, 0o700)
		if createErr != nil && !errors.Is(createErr, os.ErrExist) {
			return nil, nil, fmt.Errorf(
				"create apply-patch transaction private directory: %w",
				createErr,
			)
		}
		if createErr == nil {
			if err = parent.Chmod(name, 0o700); err != nil {
				return nil, nil, fmt.Errorf("secure apply-patch transaction private directory: %w", err)
			}
			if err = fileutil.SyncDirectory(parentPath); err != nil {
				return nil, nil, err
			}
		}
		info, err = parent.Lstat(name)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.Join(
			errors.New("apply-patch transaction private directory is invalid"),
			err,
		)
	}
	if err = validateApplyPatchTransactionPrivateObject(info, true); err != nil {
		return nil, nil, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	anchored, err := root.Lstat(".")
	if err != nil || !os.SameFile(info, anchored) {
		_ = root.Close()
		return nil, nil, errors.Join(
			errors.New("apply-patch transaction private directory changed while opening"),
			err,
		)
	}
	return root, info, nil
}

func revalidateApplyPatchTransactionDirectory(
	parent *os.Root,
	name string,
	expected os.FileInfo,
) error {
	if parent == nil || expected == nil {
		return errors.New("apply-patch transaction private directory is unavailable")
	}
	current, err := parent.Lstat(name)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(current, expected) || current.Mode() != expected.Mode() {
		return errors.Join(
			errors.New("apply-patch transaction private directory changed"),
			err,
		)
	}
	return validateApplyPatchTransactionPrivateObject(current, true)
}

func ensureApplyPatchTransactionWorkspaceBinding(
	root *os.Root,
	rootPath string,
	canonicalWorkspace string,
	authentication [applyPatchTransactionAuthenticationBytes]byte,
) error {
	binding, err := encodeApplyPatchTransactionWorkspaceBinding(
		canonicalWorkspace,
		authentication,
	)
	if err != nil {
		return err
	}
	existing, existingInfo, readErr := readApplyPatchTransactionPrivateRegularBounded(
		root,
		applyPatchTransactionWorkspaceBindingFile,
		len(applyPatchTransactionWorkspaceBindingMagic)+4+
			applyPatchTransactionWorkspacePathLimit+sha256.Size,
	)
	if errors.Is(readErr, os.ErrNotExist) {
		if err = publishApplyPatchTransactionPrivateRegular(
			root,
			rootPath,
			applyPatchTransactionWorkspaceBindingFile,
			binding,
		); err != nil {
			return err
		}
		existing, existingInfo, readErr = readApplyPatchTransactionPrivateRegularBounded(
			root,
			applyPatchTransactionWorkspaceBindingFile,
			len(applyPatchTransactionWorkspaceBindingMagic)+4+
				applyPatchTransactionWorkspacePathLimit+sha256.Size,
		)
	}
	if readErr != nil {
		return readErr
	}
	boundWorkspace, err := decodeApplyPatchTransactionWorkspaceBinding(
		existing,
		authentication,
	)
	if err != nil {
		return errors.New("apply-patch transaction workspace binding conflict")
	}
	if boundWorkspace != canonicalWorkspace {
		if err := rebindIdleApplyPatchTransactionWorkspace(
			root,
			rootPath,
			existingInfo,
			binding,
		); err != nil {
			return err
		}
	}
	return cleanupApplyPatchTransactionPrivateStage(
		root,
		rootPath,
		applyPatchTransactionWorkspaceBindingFile,
		binding,
		false,
	)
}

// rebindIdleApplyPatchTransactionWorkspace handles physical inode reuse only
// after the caller holds that identity's kernel lock. A path change with any
// transaction, cleanup, or alien state still fails closed; the two persistent
// control files alone carry no recovery decision and may be rebound safely.
func rebindIdleApplyPatchTransactionWorkspace(
	root *os.Root,
	rootPath string,
	existingInfo os.FileInfo,
	binding []byte,
) error {
	if root == nil || existingInfo == nil {
		return errors.New("apply-patch transaction workspace binding conflict")
	}
	entries, err := readApplyPatchTxnRootEntries(root)
	if err != nil {
		return err
	}
	seenBinding := false
	seenLock := false
	for _, entry := range entries {
		switch entry.Name() {
		case applyPatchTransactionWorkspaceBindingFile:
			seenBinding = true
		case applyPatchTransactionWorkspaceLockFile:
			seenLock = true
		default:
			return errors.New("apply-patch transaction workspace binding conflict")
		}
	}
	if !seenBinding || !seenLock || len(entries) != 2 {
		return errors.New("apply-patch transaction workspace binding conflict")
	}
	if err := removeApplyPatchTransactionExactRootEntry(
		root,
		applyPatchTransactionWorkspaceBindingFile,
		existingInfo,
	); err != nil {
		return err
	}
	if err := fileutil.SyncDirectory(rootPath); err != nil {
		return err
	}
	return publishApplyPatchTransactionPrivateRegular(
		root,
		rootPath,
		applyPatchTransactionWorkspaceBindingFile,
		binding,
	)
}

func encodeApplyPatchTransactionWorkspaceBinding(
	canonicalWorkspace string,
	authentication [applyPatchTransactionAuthenticationBytes]byte,
) ([]byte, error) {
	if canonicalWorkspace == "" || len(canonicalWorkspace) > applyPatchTransactionWorkspacePathLimit ||
		!utf8.ValidString(canonicalWorkspace) || strings.ContainsRune(canonicalWorkspace, '\x00') ||
		!filepath.IsAbs(canonicalWorkspace) || filepath.Clean(canonicalWorkspace) != canonicalWorkspace {
		return nil, errors.New("apply-patch transaction workspace binding is invalid")
	}
	payloadLength := len(applyPatchTransactionWorkspaceBindingMagic) + 4 + len(canonicalWorkspace)
	binding := make([]byte, payloadLength, payloadLength+sha256.Size)
	copy(binding, applyPatchTransactionWorkspaceBindingMagic)
	binary.BigEndian.PutUint32(
		binding[len(applyPatchTransactionWorkspaceBindingMagic):],
		uint32(len(canonicalWorkspace)),
	)
	copy(binding[len(applyPatchTransactionWorkspaceBindingMagic)+4:], canonicalWorkspace)
	mac := hmac.New(sha256.New, authentication[:])
	_, _ = mac.Write(binding)
	binding = mac.Sum(binding)
	return binding, nil
}

func decodeApplyPatchTransactionWorkspaceBinding(
	binding []byte,
	authentication [applyPatchTransactionAuthenticationBytes]byte,
) (string, error) {
	minimum := len(applyPatchTransactionWorkspaceBindingMagic) + 4 + sha256.Size
	if len(binding) < minimum || len(binding) > minimum+applyPatchTransactionWorkspacePathLimit {
		return "", errors.New("apply-patch transaction workspace binding is invalid")
	}
	payload := binding[:len(binding)-sha256.Size]
	providedMAC := binding[len(binding)-sha256.Size:]
	mac := hmac.New(sha256.New, authentication[:])
	_, _ = mac.Write(payload)
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return "", errors.New("apply-patch transaction workspace binding authentication failed")
	}
	if !hmac.Equal(
		payload[:len(applyPatchTransactionWorkspaceBindingMagic)],
		[]byte(applyPatchTransactionWorkspaceBindingMagic),
	) {
		return "", errors.New("apply-patch transaction workspace binding is invalid")
	}
	pathLength := int(binary.BigEndian.Uint32(
		payload[len(applyPatchTransactionWorkspaceBindingMagic):],
	))
	pathStart := len(applyPatchTransactionWorkspaceBindingMagic) + 4
	if pathLength <= 0 || pathLength > applyPatchTransactionWorkspacePathLimit ||
		pathStart+pathLength != len(payload) {
		return "", errors.New("apply-patch transaction workspace binding is invalid")
	}
	workspace := string(payload[pathStart:])
	if !utf8.ValidString(workspace) || strings.ContainsRune(workspace, '\x00') ||
		!filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return "", errors.New("apply-patch transaction workspace binding is invalid")
	}
	return workspace, nil
}

func (state *applyPatchTransactionState) revalidateLocked() error {
	if state.closed || state.root == nil {
		return errors.New("apply-patch transaction state is closed")
	}
	if err := revalidateApplyPatchTransactionStateFences(state.prepared); err != nil {
		return err
	}
	if err := revalidateApplyPatchTransactionNamedRoot(
		state.root,
		state.prepared.path,
		state.rootInfo,
	); err != nil {
		return err
	}
	if err := revalidateApplyPatchTransactionRegular(
		state.root,
		applyPatchTransactionInitLockFile,
		state.initLockInfo,
	); err != nil {
		return err
	}
	data, info, err := readApplyPatchTransactionPrivateRegular(
		state.root,
		applyPatchTransactionAuthenticationFile,
		applyPatchTransactionAuthenticationBytes,
	)
	if err != nil || !os.SameFile(info, state.authInfo) ||
		!hmac.Equal(data, state.authentication[:]) {
		return errors.Join(errors.New("apply-patch transaction authentication key changed"), err)
	}
	return nil
}

func (state *applyPatchTransactionState) authenticationKey() (
	[applyPatchTransactionAuthenticationBytes]byte,
	error,
) {
	if state == nil {
		return [applyPatchTransactionAuthenticationBytes]byte{},
			errors.New("apply-patch transaction state is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.revalidateLocked(); err != nil {
		return [applyPatchTransactionAuthenticationBytes]byte{}, err
	}
	return state.authentication, nil
}

func (state *applyPatchTransactionState) authenticationKeyID() (string, error) {
	if state == nil {
		return "", errors.New("apply-patch transaction state is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.revalidateLocked(); err != nil {
		return "", err
	}
	return state.keyID, nil
}

func (state *applyPatchTransactionState) rootPath() (string, error) {
	if state == nil {
		return "", errors.New("apply-patch transaction state is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.revalidateLocked(); err != nil {
		return "", err
	}
	return state.prepared.path, nil
}

func (state *applyPatchTransactionState) rootIdentity() (applyPatchTxnIdentity, error) {
	if state == nil {
		return applyPatchTxnIdentity{}, errors.New("apply-patch transaction state is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.revalidateLocked(); err != nil {
		return applyPatchTxnIdentity{}, err
	}
	return applyPatchTxnIdentityFromFileInfo(state.rootInfo, "directory")
}

func (state *applyPatchTransactionState) withRootAnchor(
	operation func(*os.Root) error,
) error {
	if state == nil || operation == nil {
		return errors.New("apply-patch transaction rooted operation is invalid")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.revalidateLocked(); err != nil {
		return err
	}
	return operation(state.root)
}

func (state *applyPatchTransactionState) Close() error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	if state.active != 0 {
		return errors.New("apply-patch transaction workspace lock is still active")
	}
	state.closed = true
	clear(state.authentication[:])
	state.keyID = ""
	root := state.root
	state.root = nil
	if root == nil {
		return nil
	}
	return root.Close()
}

func (workspace *applyPatchTransactionWorkspaceState) directoryPath() (string, error) {
	if workspace == nil {
		return "", errors.New("apply-patch transaction workspace state is unavailable")
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if err := workspace.revalidateLocked(); err != nil {
		return "", err
	}
	return workspace.absoluteDirectory, nil
}

func (workspace *applyPatchTransactionWorkspaceState) directoryRelative() (string, error) {
	if workspace == nil {
		return "", errors.New("apply-patch transaction workspace state is unavailable")
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if err := workspace.revalidateLocked(); err != nil {
		return "", err
	}
	return workspace.relativeDirectory, nil
}

func (workspace *applyPatchTransactionWorkspaceState) withDirectoryAnchor(
	operation func(*os.Root) error,
) error {
	if workspace == nil || operation == nil {
		return errors.New("apply-patch transaction workspace rooted operation is invalid")
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if err := workspace.revalidateLocked(); err != nil {
		return err
	}
	return operation(workspace.root)
}

func (workspace *applyPatchTransactionWorkspaceState) revalidateLocked() error {
	if workspace.closed || workspace.root == nil || workspace.lock == nil {
		return errors.New("apply-patch transaction workspace state is closed")
	}
	named, err := os.Lstat(workspace.absoluteDirectory)
	if err != nil || !named.IsDir() || named.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(named, workspace.rootInfo) {
		return errors.Join(errors.New("apply-patch transaction workspace state changed"), err)
	}
	anchored, err := workspace.root.Lstat(".")
	if err != nil || !os.SameFile(anchored, workspace.rootInfo) {
		return errors.Join(errors.New("apply-patch transaction workspace state changed"), err)
	}
	lockInfo, err := workspace.lock.fileInfo()
	if err != nil || !os.SameFile(lockInfo, workspace.lockInfo) {
		return errors.Join(errors.New("apply-patch transaction workspace lock changed"), err)
	}
	if err = revalidateApplyPatchTransactionRegular(
		workspace.root,
		applyPatchTransactionWorkspaceLockFile,
		workspace.lockInfo,
	); err != nil {
		return err
	}
	return validateApplyPatchTransactionPrivateObject(named, true)
}

func (workspace *applyPatchTransactionWorkspaceState) Close() error {
	if workspace == nil {
		return nil
	}
	workspace.mu.Lock()
	if workspace.closed {
		workspace.mu.Unlock()
		return nil
	}
	workspace.closed = true
	root := workspace.root
	workspace.root = nil
	lock := workspace.lock
	workspace.lock = nil
	state := workspace.state
	workspace.state = nil
	workspace.mu.Unlock()

	var rootErr error
	if root != nil {
		rootErr = root.Close()
	}
	var lockErr error
	if lock != nil {
		lockErr = lock.Close()
	}
	if state != nil {
		state.mu.Lock()
		if state.active > 0 {
			state.active--
		}
		state.mu.Unlock()
	}
	return errors.Join(rootErr, lockErr)
}
