package gitworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxPinnedValidationEntries             = 100_000
	maxPinnedValidationFilesystemNodes     = (maxPinnedValidationEntries * 2) + 2
	maxPinnedValidationPathBytes           = 4 << 10
	maxPinnedValidationPathTotalBytes      = 16 << 20
	maxPinnedValidationFilesystemPathBytes = 64 << 20
	maxPinnedValidationTreeListBytes       = 32 << 20
	maxPinnedValidationBlobBytes           = int64(128 << 20)
	maxPinnedValidationTreeBytes           = int64(1 << 30)
	maxPinnedValidationSymlinkBytes        = int64(16 << 10)
	pinnedValidationPostflightTimeout      = 30 * time.Second
	pinnedValidationRootPrefix             = "picoclaw-pinned-validation-"
)

var errPinnedValidationOutputLimit = errors.New("pinned validation Git output exceeded its limit")

// PinnedCandidateValidationRequest binds one disposable validation view to the
// exact controller-owned candidate previously returned by
// SnapshotPinnedCandidate. No field grants publication or release authority.
type PinnedCandidateValidationRequest struct {
	Pin                     PinnedAcquireRequest
	WorkspaceID             string
	ExpectedParent          string
	ExpectedTree            string
	ExpectedCandidateDigest string
	// NoChanges explicitly declares that ExpectedTree is exactly the parent
	// commit's tree. The manager proves that equality before lending roots;
	// false retains the historical requirement for at least one change.
	NoChanges bool
}

// PinnedTreeManifest is canonical evidence for every leaf in one Git tree.
// Digest is a domain-separated SHA-256 over the tree ID and each ordered path,
// Git mode, materialized type, byte size, and full SHA-256 content digest.
type PinnedTreeManifest struct {
	Tree    string `json:"tree"`
	Digest  string `json:"digest"`
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
}

// PinnedCandidateValidationRoots exists only for the callback passed to
// WithPinnedCandidateValidationRoots. Repository is the canonical normalized
// repository identity. ParentRoot and CandidateRoot are immutable private
// disposable directories containing no Git control directory. Their paths are
// deliberately omitted from JSON so evidence serialization cannot retain them.
type PinnedCandidateValidationRoots struct {
	Repository        string             `json:"repository"`
	ParentRoot        string             `json:"-"`
	CandidateRoot     string             `json:"-"`
	ParentManifest    PinnedTreeManifest `json:"parent_manifest"`
	CandidateManifest PinnedTreeManifest `json:"candidate_manifest"`
}

type pinnedCandidateValidationSnapshot struct {
	workspacePath string
	parentTree    string
}

// WithPinnedCandidateValidationRoots revalidates one exact pinned candidate,
// materializes its parent and candidate Git trees without checkout metadata,
// and lends those disposable roots to run while holding the reservation's
// cross-process operation lock. After callback entry, a detached bounded
// postflight proves both roots retain their callback-entry state before cleanup;
// a second detached postflight then revalidates retained candidate and
// control-plane state. Both run after cancellation or callback failure.
func (m *Manager) WithPinnedCandidateValidationRoots(
	ctx context.Context,
	request PinnedCandidateValidationRequest,
	run func(context.Context, PinnedCandidateValidationRoots) error,
) error {
	if m == nil {
		return errors.New("git workspace manager is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if run == nil {
		return fmt.Errorf(
			"%w: validation callback is required",
			ErrPinnedCommitInvalid,
		)
	}
	repository, err := validatePinnedCandidateValidationRequest(ctx, request)
	if err != nil {
		return err
	}

	return m.WithPinnedOperation(ctx, request.Pin, func(operationCtx context.Context) error {
		environment, cleanupEnvironment, environmentErr := m.newPinnedGitEnvironment()
		if environmentErr != nil {
			return environmentErr
		}
		defer cleanupEnvironment()

		snapshot, snapshotErr := m.snapshotPinnedCandidateValidationState(
			operationCtx,
			request,
			repository,
			environment,
		)
		if snapshotErr != nil {
			return snapshotErr
		}
		temporary, roots, materializeErr := materializePinnedCandidateValidationRoots(
			operationCtx,
			snapshot.workspacePath,
			snapshot.parentTree,
			request.ExpectedTree,
			environment,
		)
		if materializeErr != nil {
			return materializeErr
		}
		roots.Repository = repository

		return func() (returnErr error) {
			defer func() {
				postflightCtx, cancel := context.WithTimeout(
					context.WithoutCancel(operationCtx),
					pinnedValidationPostflightTimeout,
				)
				rootPostflightErr := temporary.revalidateCallbackRoots(
					postflightCtx,
					roots,
				)
				cancel()
				if rootPostflightErr != nil {
					rootPostflightErr = fmt.Errorf(
						"%w: disposable validation roots changed: %w",
						ErrPinnedCommitConflict,
						rootPostflightErr,
					)
				}

				cleanupErr := temporary.cleanup()
				postflightCtx, cancel = context.WithTimeout(
					context.WithoutCancel(operationCtx),
					pinnedValidationPostflightTimeout,
				)
				postflight, postflightErr := m.snapshotPinnedCandidateValidationState(
					postflightCtx,
					request,
					repository,
					environment,
				)
				cancel()
				if postflightErr == nil && postflight.parentTree != snapshot.parentTree {
					postflightErr = fmt.Errorf(
						"%w: pinned validation parent tree changed",
						ErrPinnedCommitConflict,
					)
				}
				if cleanupErr != nil {
					cleanupErr = fmt.Errorf("clean pinned validation roots: %w", cleanupErr)
				}
				if postflightErr != nil {
					postflightErr = fmt.Errorf(
						"pinned validation postflight: %w",
						postflightErr,
					)
				}
				returnErr = errors.Join(
					returnErr,
					rootPostflightErr,
					cleanupErr,
					postflightErr,
				)
			}()
			return run(operationCtx, roots)
		}()
	})
}

func validatePinnedCandidateValidationRequest(
	ctx context.Context,
	request PinnedCandidateValidationRequest,
) (string, error) {
	repository, err := validatePinnedCandidateRequest(ctx, PinnedCandidateRequest{
		Pin:         request.Pin,
		WorkspaceID: request.WorkspaceID,
	})
	if err != nil {
		return "", err
	}
	if !validPinnedCommit(request.ExpectedParent) ||
		!validPinnedCommit(request.ExpectedTree) ||
		len(request.ExpectedParent) != len(request.ExpectedTree) ||
		len(request.ExpectedParent) != len(request.Pin.ExpectedCommit) ||
		!validLowerHex(request.ExpectedCandidateDigest, sha256.Size*2) {
		return "", fmt.Errorf(
			"%w: validation parent, tree, or candidate digest is invalid",
			ErrPinnedCommitInvalid,
		)
	}
	return repository, nil
}

func (m *Manager) snapshotPinnedCandidateValidationState(
	ctx context.Context,
	request PinnedCandidateValidationRequest,
	repository string,
	environment []string,
) (pinnedCandidateValidationSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlockInventory, err := m.lockInventory(ctx)
	if err != nil {
		return pinnedCandidateValidationSnapshot{}, err
	}
	defer unlockInventory()
	state, err := m.loadLocked()
	if err != nil {
		return pinnedCandidateValidationSnapshot{}, err
	}
	if pendingErr := rejectPendingDevelopmentLineReservation(
		state,
		request.Pin.ReservationKey,
	); pendingErr != nil {
		return pinnedCandidateValidationSnapshot{}, pendingErr
	}
	workspace, err := m.pinnedWorkspaceForOperation(
		ctx,
		state,
		request.Pin,
		request.WorkspaceID,
		repository,
		environment,
	)
	if err != nil {
		return pinnedCandidateValidationSnapshot{}, err
	}
	if verifyErr := verifyPinnedCommitOperationState(ctx, workspace.Path, environment); verifyErr != nil {
		return pinnedCandidateValidationSnapshot{}, verifyErr
	}
	if locksErr := rejectPinnedGitLockFiles(workspace.Path); locksErr != nil {
		return pinnedCandidateValidationSnapshot{}, locksErr
	}
	parent, err := resolvePinnedGitCommit(ctx, workspace.Path, "HEAD", environment)
	if err != nil {
		return pinnedCandidateValidationSnapshot{}, fmt.Errorf(
			"resolve pinned validation parent: %w",
			err,
		)
	}
	if parent != request.ExpectedParent {
		return pinnedCandidateValidationSnapshot{}, fmt.Errorf(
			"%w: pinned validation parent changed",
			ErrPinnedCommitConflict,
		)
	}
	if indexErr := requirePinnedIndexTree(ctx, workspace.Path, parent, environment); indexErr != nil {
		return pinnedCandidateValidationSnapshot{}, indexErr
	}
	candidate, err := m.buildPinnedCandidate(
		ctx,
		workspace.Path,
		parent,
		parent,
		environment,
	)
	if err != nil {
		return pinnedCandidateValidationSnapshot{}, err
	}
	noChanges := candidate.Tree == candidate.parentTree
	if noChanges != request.NoChanges {
		description := "pinned validation candidate contains no ordinary changes"
		if request.NoChanges {
			description = "pinned validation candidate contains ordinary changes"
		}
		return pinnedCandidateValidationSnapshot{}, fmt.Errorf(
			"%w: %s",
			ErrPinnedCommitConflict,
			description,
		)
	}
	if candidate.Tree != request.ExpectedTree ||
		candidate.Digest != request.ExpectedCandidateDigest {
		return pinnedCandidateValidationSnapshot{}, fmt.Errorf(
			"%w: pinned validation candidate changed",
			ErrPinnedCommitConflict,
		)
	}
	return pinnedCandidateValidationSnapshot{
		workspacePath: workspace.Path,
		parentTree:    candidate.parentTree,
	}, nil
}

type pinnedValidationTemporaryRoots struct {
	path              string
	root              *os.Root
	parentRoot        *os.Root
	candidateRoot     *os.Root
	baseIdentity      fs.FileInfo
	parentIdentity    fs.FileInfo
	candidateIdentity fs.FileInfo
	parentExpected    map[string]pinnedValidationExpectedLeaf
	candidateExpected map[string]pinnedValidationExpectedLeaf
	parentSnapshot    pinnedValidationFilesystemSnapshot
	candidateSnapshot pinnedValidationFilesystemSnapshot
	cleanupOnce       sync.Once
	cleanupErr        error
}

func newPinnedValidationTemporaryRoots() (*pinnedValidationTemporaryRoots, error) {
	rootPath, err := os.MkdirTemp("", pinnedValidationRootPrefix)
	if err != nil {
		return nil, fmt.Errorf("create pinned validation root: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(rootPath)
		}
	}()
	if chmodErr := os.Chmod(rootPath, 0o700); chmodErr != nil {
		return nil, fmt.Errorf("secure pinned validation root: %w", chmodErr)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open pinned validation root: %w", err)
	}
	if mkdirErr := root.Mkdir("parent", 0o700); mkdirErr != nil {
		_ = root.Close()
		return nil, fmt.Errorf("create pinned validation parent root: %w", mkdirErr)
	}
	if mkdirErr := root.Mkdir("candidate", 0o700); mkdirErr != nil {
		_ = root.Close()
		return nil, fmt.Errorf("create pinned validation candidate root: %w", mkdirErr)
	}
	baseIdentity, err := os.Lstat(rootPath)
	if err != nil || !baseIdentity.IsDir() || baseIdentity.Mode()&os.ModeSymlink != 0 {
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect pinned validation root identity: %w", err)
		}
		return nil, errors.New("pinned validation root is not a real directory")
	}
	anchoredIdentity, err := root.Lstat(".")
	if err != nil || !os.SameFile(baseIdentity, anchoredIdentity) {
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect anchored pinned validation root: %w", err)
		}
		return nil, errors.New("pinned validation root identity changed")
	}
	parentIdentity, err := root.Lstat("parent")
	if err != nil || !parentIdentity.IsDir() || parentIdentity.Mode()&os.ModeSymlink != 0 {
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect pinned validation parent identity: %w", err)
		}
		return nil, errors.New("pinned validation parent is not a real directory")
	}
	candidateIdentity, err := root.Lstat("candidate")
	if err != nil || !candidateIdentity.IsDir() || candidateIdentity.Mode()&os.ModeSymlink != 0 {
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect pinned validation candidate identity: %w", err)
		}
		return nil, errors.New("pinned validation candidate is not a real directory")
	}
	parentRoot, err := root.OpenRoot("parent")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("retain pinned validation parent root: %w", err)
	}
	openedParentIdentity, err := parentRoot.Lstat(".")
	if err != nil || !os.SameFile(parentIdentity, openedParentIdentity) {
		_ = parentRoot.Close()
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect retained pinned validation parent root: %w", err)
		}
		return nil, errors.New("pinned validation parent identity changed while opening")
	}
	candidateRoot, err := root.OpenRoot("candidate")
	if err != nil {
		_ = parentRoot.Close()
		_ = root.Close()
		return nil, fmt.Errorf("retain pinned validation candidate root: %w", err)
	}
	openedCandidateIdentity, err := candidateRoot.Lstat(".")
	if err != nil || !os.SameFile(candidateIdentity, openedCandidateIdentity) {
		_ = candidateRoot.Close()
		_ = parentRoot.Close()
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect retained pinned validation candidate root: %w", err)
		}
		return nil, errors.New("pinned validation candidate identity changed while opening")
	}
	failed = false
	return &pinnedValidationTemporaryRoots{
		path:              rootPath,
		root:              root,
		parentRoot:        parentRoot,
		candidateRoot:     candidateRoot,
		baseIdentity:      baseIdentity,
		parentIdentity:    parentIdentity,
		candidateIdentity: candidateIdentity,
	}, nil
}

func (temporary *pinnedValidationTemporaryRoots) cleanup() error {
	if temporary == nil {
		return nil
	}
	temporary.cleanupOnce.Do(func() {
		var result error
		if temporary.parentRoot != nil {
			result = errors.Join(result, clearPinnedValidationRoot(temporary.parentRoot))
			result = errors.Join(result, temporary.parentRoot.Close())
		}
		if temporary.candidateRoot != nil {
			result = errors.Join(result, clearPinnedValidationRoot(temporary.candidateRoot))
			result = errors.Join(result, temporary.candidateRoot.Close())
		}
		if temporary.root != nil {
			result = errors.Join(result, clearPinnedValidationRoot(temporary.root))
			result = errors.Join(result, temporary.root.Close())
		}
		if temporary.path != "" {
			result = errors.Join(result, os.Remove(temporary.path))
		}
		temporary.cleanupErr = result
	})
	return temporary.cleanupErr
}

func clearPinnedValidationRoot(root *os.Root) error {
	if root == nil {
		return nil
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	var result error
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			result = errors.Join(result, root.RemoveAll(entry.Name()))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			result = errors.Join(result, readErr)
			break
		}
	}
	return errors.Join(result, directory.Close())
}

func materializePinnedCandidateValidationRoots(
	ctx context.Context,
	workspacePath, parentTree, candidateTree string,
	environment []string,
) (*pinnedValidationTemporaryRoots, PinnedCandidateValidationRoots, error) {
	temporary, err := newPinnedValidationTemporaryRoots()
	if err != nil {
		return nil, PinnedCandidateValidationRoots{}, err
	}
	fail := func(cause error) (*pinnedValidationTemporaryRoots, PinnedCandidateValidationRoots, error) {
		cleanupErr := temporary.cleanup()
		if cleanupErr != nil {
			cause = errors.Join(cause, fmt.Errorf("clean pinned validation roots: %w", cleanupErr))
		}
		return nil, PinnedCandidateValidationRoots{}, cause
	}
	parentManifest, parentErr := materializePinnedValidationTree(
		ctx,
		workspacePath,
		parentTree,
		temporary.parentRoot,
		environment,
	)
	if parentErr != nil {
		return fail(parentErr)
	}
	candidateManifest, candidateErr := materializePinnedValidationTree(
		ctx,
		workspacePath,
		candidateTree,
		temporary.candidateRoot,
		environment,
	)
	if candidateErr != nil {
		return fail(candidateErr)
	}
	parentEntries, err := listPinnedValidationTree(ctx, workspacePath, parentTree, environment)
	if err != nil {
		return fail(err)
	}
	candidateEntries, err := listPinnedValidationTree(ctx, workspacePath, candidateTree, environment)
	if err != nil {
		return fail(err)
	}
	temporary.parentExpected = pinnedValidationExpectedLeaves(parentEntries)
	temporary.candidateExpected = pinnedValidationExpectedLeaves(candidateEntries)
	if identityErr := temporary.validateBaseIdentity(); identityErr != nil {
		return fail(fmt.Errorf("validate disposable root before callback: %w", identityErr))
	}
	temporary.parentSnapshot, err = temporary.snapshotValidationRoot(
		ctx,
		"parent",
		parentTree,
		temporary.parentIdentity,
		temporary.parentExpected,
		nil,
	)
	if err != nil {
		return fail(fmt.Errorf("snapshot disposable parent root: %w", err))
	}
	if temporary.parentSnapshot.manifest != parentManifest {
		return fail(errors.New("materialized parent root manifest changed before callback"))
	}
	temporary.candidateSnapshot, err = temporary.snapshotValidationRoot(
		ctx,
		"candidate",
		candidateTree,
		temporary.candidateIdentity,
		temporary.candidateExpected,
		nil,
	)
	if err != nil {
		return fail(fmt.Errorf("snapshot disposable candidate root: %w", err))
	}
	if temporary.candidateSnapshot.manifest != candidateManifest {
		return fail(errors.New("materialized candidate root manifest changed before callback"))
	}
	if err := temporary.validateBaseIdentity(); err != nil {
		return fail(fmt.Errorf("validate disposable root before callback: %w", err))
	}
	return temporary, PinnedCandidateValidationRoots{
		ParentRoot:        filepath.Join(temporary.path, "parent"),
		CandidateRoot:     filepath.Join(temporary.path, "candidate"),
		ParentManifest:    parentManifest,
		CandidateManifest: candidateManifest,
	}, nil
}

type pinnedValidationTreeEntry struct {
	path       string
	mode       string
	kind       string
	object     string
	linkTarget string
	linkPath   string
}

type pinnedValidationExpectedLeaf struct {
	mode string
	kind string
}

type pinnedValidationFilesystemNode struct {
	identity    fs.FileInfo
	mode        fs.FileMode
	kind        string
	changeToken pinnedValidationChangeToken
}

type pinnedValidationFilesystemSnapshot struct {
	manifest PinnedTreeManifest
	nodes    map[string]pinnedValidationFilesystemNode
}

func materializePinnedValidationTree(
	ctx context.Context,
	workspacePath, tree string,
	root *os.Root,
	environment []string,
) (PinnedTreeManifest, error) {
	entries, err := listPinnedValidationTree(ctx, workspacePath, tree, environment)
	if err != nil {
		return PinnedTreeManifest{}, err
	}
	manifestHash := sha256.New()
	_, _ = manifestHash.Write([]byte("picoclaw-pinned-validation-tree-v1\x00"))
	writePinnedDigestField(manifestHash, tree)
	var totalBytes int64
	symlinks := make([]*pinnedValidationTreeEntry, 0)
	for index := range entries {
		entry := &entries[index]
		remaining := maxPinnedValidationTreeBytes - totalBytes
		if remaining < 0 {
			return PinnedTreeManifest{}, fmt.Errorf(
				"%w: pinned validation tree exceeds its byte limit",
				ErrPinnedCommitConflict,
			)
		}
		contentHash := sha256.New()
		var size int64
		switch entry.kind {
		case "regular":
			parent := path.Dir(entry.path)
			if parent != "." {
				if mkdirErr := root.MkdirAll(filepath.FromSlash(parent), 0o700); mkdirErr != nil {
					return PinnedTreeManifest{}, fmt.Errorf(
						"create pinned validation directory: %w",
						mkdirErr,
					)
				}
			}
			name := filepath.FromSlash(entry.path)
			file, openErr := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if openErr != nil {
				return PinnedTreeManifest{}, fmt.Errorf(
					"create pinned validation file: %w",
					openErr,
				)
			}
			limit := min(maxPinnedValidationBlobBytes, remaining)
			size, err = runPinnedValidationGitTo(
				ctx,
				workspacePath,
				environment,
				io.MultiWriter(file, contentHash),
				limit,
				"cat-file",
				"blob",
				entry.object,
			)
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				return PinnedTreeManifest{}, errors.Join(err, closeErr)
			}
			permission := fs.FileMode(0o644)
			if entry.mode == "100755" {
				permission = 0o755
			}
			if chmodErr := root.Chmod(name, permission); chmodErr != nil {
				return PinnedTreeManifest{}, fmt.Errorf(
					"set pinned validation file mode: %w",
					chmodErr,
				)
			}
		case "symlink":
			var content bytes.Buffer
			limit := min(maxPinnedValidationSymlinkBytes, remaining)
			size, err = runPinnedValidationGitTo(
				ctx,
				workspacePath,
				environment,
				io.MultiWriter(&content, contentHash),
				limit,
				"cat-file",
				"blob",
				entry.object,
			)
			if err != nil {
				return PinnedTreeManifest{}, err
			}
			entry.linkTarget = content.String()
			entry.linkPath, err = validatePinnedValidationSymlink(entry.path, entry.linkTarget)
			if err != nil {
				return PinnedTreeManifest{}, err
			}
			symlinks = append(symlinks, entry)
		default:
			return PinnedTreeManifest{}, errors.New("invalid pinned validation entry type")
		}
		totalBytes += size
		writePinnedDigestField(manifestHash, entry.path)
		writePinnedDigestField(manifestHash, entry.mode)
		writePinnedDigestField(manifestHash, entry.kind)
		writePinnedDigestField(manifestHash, strconv.FormatInt(size, 10))
		writePinnedDigestField(manifestHash, hex.EncodeToString(contentHash.Sum(nil)))
	}
	if err := validatePinnedValidationSymlinkGraph(symlinks); err != nil {
		return PinnedTreeManifest{}, err
	}
	for _, entry := range symlinks {
		parent := path.Dir(entry.path)
		if parent != "." {
			if err := root.MkdirAll(filepath.FromSlash(parent), 0o700); err != nil {
				return PinnedTreeManifest{}, fmt.Errorf(
					"create pinned validation symlink directory: %w",
					err,
				)
			}
		}
		if err := root.Symlink(entry.linkTarget, filepath.FromSlash(entry.path)); err != nil {
			return PinnedTreeManifest{}, fmt.Errorf(
				"create pinned validation symlink: %w",
				err,
			)
		}
	}
	writePinnedDigestField(manifestHash, strconv.Itoa(len(entries)))
	writePinnedDigestField(manifestHash, strconv.FormatInt(totalBytes, 10))
	return PinnedTreeManifest{
		Tree:    tree,
		Digest:  hex.EncodeToString(manifestHash.Sum(nil)),
		Entries: len(entries),
		Bytes:   totalBytes,
	}, nil
}

func listPinnedValidationTree(
	ctx context.Context,
	workspacePath, tree string,
	environment []string,
) ([]pinnedValidationTreeEntry, error) {
	var output bytes.Buffer
	if _, err := runPinnedValidationGitTo(
		ctx,
		workspacePath,
		environment,
		&output,
		maxPinnedValidationTreeListBytes,
		"ls-tree",
		"-r",
		"-z",
		"--full-tree",
		tree,
	); err != nil {
		return nil, err
	}
	value := output.Bytes()
	if len(value) == 0 {
		return nil, nil
	}
	if value[len(value)-1] != 0 {
		return nil, errors.New("Git returned an unterminated pinned validation tree")
	}
	records := bytes.Split(value[:len(value)-1], []byte{0})
	if len(records) > maxPinnedValidationEntries {
		return nil, fmt.Errorf(
			"%w: pinned validation tree has too many entries",
			ErrPinnedCommitConflict,
		)
	}
	entries := make([]pinnedValidationTreeEntry, 0, len(records))
	pathBytes := 0
	for _, record := range records {
		separator := bytes.IndexByte(record, '\t')
		if separator < 1 || separator == len(record)-1 {
			return nil, errors.New("Git returned an invalid pinned validation tree entry")
		}
		header := strings.Fields(string(record[:separator]))
		if len(header) != 3 || !validPinnedCommit(header[2]) ||
			len(header[2]) != len(tree) {
			return nil, errors.New("Git returned invalid pinned validation object evidence")
		}
		entryPath := string(record[separator+1:])
		if err := validatePinnedValidationPath(entryPath); err != nil {
			return nil, err
		}
		pathBytes += len(entryPath)
		if pathBytes > maxPinnedValidationPathTotalBytes {
			return nil, fmt.Errorf(
				"%w: pinned validation paths exceed their aggregate limit",
				ErrPinnedCommitConflict,
			)
		}
		entry := pinnedValidationTreeEntry{
			path:   entryPath,
			mode:   header[0],
			object: header[2],
		}
		switch {
		case header[1] == "blob" && (header[0] == "100644" || header[0] == "100755"):
			entry.kind = "regular"
		case header[1] == "blob" && header[0] == "120000":
			entry.kind = "symlink"
		case header[0] == "160000" || header[1] == "commit":
			return nil, fmt.Errorf(
				"%w: pinned validation trees cannot contain Git links",
				ErrPinnedCommitConflict,
			)
		default:
			return nil, fmt.Errorf(
				"%w: pinned validation tree contains an unsupported entry",
				ErrPinnedCommitConflict,
			)
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].path < entries[right].path
	})
	leaves := make(map[string]struct{}, len(entries))
	foldedLeaves := make(map[string]string, len(entries))
	for _, entry := range entries {
		folded := pinnedValidationFoldPath(entry.path)
		if previous, ok := foldedLeaves[folded]; ok {
			return nil, fmt.Errorf(
				"%w: pinned validation paths collide: %q and %q",
				ErrPinnedCommitConflict,
				previous,
				entry.path,
			)
		}
		leaves[entry.path] = struct{}{}
		foldedLeaves[folded] = entry.path
	}
	foldedPrefixes := make(map[string]string, len(entries))
	for _, entry := range entries {
		prefix := ""
		for _, component := range strings.Split(entry.path, "/") {
			prefix = path.Join(prefix, component)
			folded := pinnedValidationFoldPath(prefix)
			if previous, ok := foldedPrefixes[folded]; ok && previous != prefix {
				return nil, fmt.Errorf(
					"%w: pinned validation path prefixes collide: %q and %q",
					ErrPinnedCommitConflict,
					previous,
					prefix,
				)
			}
			foldedPrefixes[folded] = prefix
		}
		for ancestor := path.Dir(entry.path); ancestor != "."; ancestor = path.Dir(ancestor) {
			if _, ok := leaves[ancestor]; ok {
				return nil, fmt.Errorf(
					"%w: pinned validation path traverses a non-directory",
					ErrPinnedCommitConflict,
				)
			}
			if previous, ok := foldedLeaves[pinnedValidationFoldPath(ancestor)]; ok {
				return nil, fmt.Errorf(
					"%w: pinned validation path collides with %q",
					ErrPinnedCommitConflict,
					previous,
				)
			}
		}
	}
	return entries, nil
}

func validatePinnedValidationPath(value string) error {
	if value == "" || len(value) > maxPinnedValidationPathBytes ||
		!utf8.ValidString(value) || strings.ContainsRune(value, '\x00') ||
		strings.ContainsRune(value, '\\') || path.IsAbs(value) ||
		path.Clean(value) != value || value == "." ||
		filepath.VolumeName(filepath.FromSlash(value)) != "" ||
		!filepath.IsLocal(filepath.FromSlash(value)) {
		return fmt.Errorf(
			"%w: pinned validation tree contains an unsafe path",
			ErrPinnedCommitConflict,
		)
	}
	for _, component := range strings.Split(value, "/") {
		if !validPinnedValidationPathComponent(component) {
			return fmt.Errorf(
				"%w: pinned validation tree contains an unsafe path component",
				ErrPinnedCommitConflict,
			)
		}
	}
	return nil
}

func validPinnedValidationPathComponent(component string) bool {
	if component == "" || component == "." || component == ".." ||
		len(component) > 255 || component != strings.TrimSpace(component) ||
		strings.ContainsRune(component, ':') || strings.HasSuffix(component, ".") {
		return false
	}
	for _, character := range component {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	gitBase := component
	if before, _, found := strings.Cut(gitBase, ":"); found {
		gitBase = before
	}
	gitBase = strings.TrimRight(gitBase, " .")
	if strings.EqualFold(gitBase, ".git") {
		return false
	}
	deviceBase, _, _ := strings.Cut(component, ".")
	switch strings.ToUpper(deviceBase) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}

// pinnedValidationFoldPath returns one stable representative for Go's Unicode
// simple-fold equivalence classes. strings.ToLower is insufficient because
// some case-equivalent runes, such as Greek sigma and final sigma, have distinct
// lowercase forms on filesystems that compare them as the same name.
func pinnedValidationFoldPath(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, character := range value {
		representative := character
		for folded := unicode.SimpleFold(character); folded != character; folded = unicode.SimpleFold(folded) {
			if folded < representative {
				representative = folded
			}
		}
		result.WriteRune(representative)
	}
	return result.String()
}

func validatePinnedValidationSymlink(linkPath, target string) (string, error) {
	if target == "" || int64(len(target)) > maxPinnedValidationSymlinkBytes ||
		!utf8.ValidString(target) || strings.ContainsRune(target, '\x00') ||
		strings.ContainsRune(target, '\\') || path.IsAbs(target) ||
		filepath.VolumeName(filepath.FromSlash(target)) != "" {
		return "", fmt.Errorf(
			"%w: pinned validation tree contains an unsafe symlink",
			ErrPinnedCommitConflict,
		)
	}
	for _, component := range strings.Split(target, "/") {
		if component == "." || component == ".." {
			continue
		}
		if !validPinnedValidationPathComponent(component) {
			return "", fmt.Errorf(
				"%w: pinned validation symlink has an unsafe target component",
				ErrPinnedCommitConflict,
			)
		}
	}
	resolved := path.Clean(path.Join(path.Dir(linkPath), target))
	if resolved == "." {
		return "", fmt.Errorf(
			"%w: pinned validation symlink resolves to its tree root",
			ErrPinnedCommitConflict,
		)
	}
	if err := validatePinnedValidationPath(resolved); err != nil {
		return "", fmt.Errorf(
			"%w: pinned validation symlink escapes its tree",
			ErrPinnedCommitConflict,
		)
	}
	if resolved == linkPath || strings.HasPrefix(resolved, linkPath+"/") {
		return "", fmt.Errorf(
			"%w: pinned validation symlink is recursive",
			ErrPinnedCommitConflict,
		)
	}
	return resolved, nil
}

func validatePinnedValidationSymlinkGraph(entries []*pinnedValidationTreeEntry) error {
	links := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		links[pinnedValidationFoldPath(entry.path)] = struct{}{}
	}
	for _, entry := range entries {
		for current := entry.linkPath; current != "."; current = path.Dir(current) {
			if _, found := links[pinnedValidationFoldPath(current)]; found {
				return fmt.Errorf(
					"%w: pinned validation symlink targets another symlink",
					ErrPinnedCommitConflict,
				)
			}
		}
	}
	return nil
}

type pinnedValidationLimitWriter struct {
	writer   io.Writer
	limit    int64
	written  int64
	exceeded bool
	writeErr error
	cancel   context.CancelFunc
}

func (writer *pinnedValidationLimitWriter) Write(value []byte) (int, error) {
	remaining := writer.limit - writer.written
	if remaining < 0 {
		remaining = 0
	}
	portion := value
	if int64(len(portion)) > remaining {
		portion = portion[:int(remaining)]
	}
	written := 0
	if len(portion) > 0 {
		var err error
		written, err = writer.writer.Write(portion)
		writer.written += int64(written)
		if err != nil {
			writer.writeErr = err
			if writer.cancel != nil {
				writer.cancel()
			}
			return written, err
		}
		if written != len(portion) {
			writer.writeErr = io.ErrShortWrite
			if writer.cancel != nil {
				writer.cancel()
			}
			return written, io.ErrShortWrite
		}
	}
	if len(value) > len(portion) {
		writer.exceeded = true
		if writer.cancel != nil {
			writer.cancel()
		}
		return written, errPinnedValidationOutputLimit
	}
	return written, nil
}

func runPinnedValidationGitTo(
	ctx context.Context,
	directory string,
	environment []string,
	output io.Writer,
	maximumOutput int64,
	args ...string,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maximumOutput < 0 {
		return 0, errors.New("pinned validation Git output limit is invalid")
	}
	if output == nil {
		output = io.Discard
	}
	if environment == nil {
		environment = pinnedGitEnvironment(os.DevNull, os.DevNull)
	}
	commandCtx, cancelCommand := context.WithCancel(ctx)
	defer cancelCommand()
	command := exec.CommandContext(commandCtx, "git", args...)
	command.Dir = directory
	command.Env = pinnedPlumbingEnvironment(environment)
	var errorOutput bytes.Buffer
	boundedOutput := &pinnedValidationLimitWriter{
		writer: output,
		limit:  maximumOutput,
		cancel: cancelCommand,
	}
	boundedError := &pinnedValidationLimitWriter{
		writer: &errorOutput,
		limit:  maxPinnedCommitGitErrorBytes,
		cancel: cancelCommand,
	}
	command.Stdout = boundedOutput
	command.Stderr = boundedError
	err := command.Run()
	if boundedOutput.exceeded || boundedError.exceeded {
		return boundedOutput.written, errPinnedValidationOutputLimit
	}
	if boundedOutput.writeErr != nil {
		return boundedOutput.written, fmt.Errorf(
			"write pinned validation Git output: %w",
			boundedOutput.writeErr,
		)
	}
	if boundedError.writeErr != nil {
		return boundedOutput.written, fmt.Errorf(
			"write pinned validation Git error output: %w",
			boundedError.writeErr,
		)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return boundedOutput.written, ctxErr
	}
	if err != nil {
		message := strings.TrimSpace(errorOutput.String())
		if message == "" {
			message = err.Error()
		}
		operation := "operation"
		if len(args) > 0 {
			operation = args[0]
		}
		return boundedOutput.written, fmt.Errorf(
			"pinned validation Git %s failed: %s",
			operation,
			message,
		)
	}
	return boundedOutput.written, nil
}
