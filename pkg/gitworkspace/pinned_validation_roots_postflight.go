package gitworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
)

type pinnedValidationFilesystemLeaf struct {
	path          string
	mode          string
	kind          string
	size          int64
	contentDigest string
}

type pinnedValidationFilesystemWalk struct {
	ctx            context.Context
	expectedLeaves map[string]pinnedValidationExpectedLeaf
	baseline       *pinnedValidationFilesystemSnapshot
	nodes          map[string]pinnedValidationFilesystemNode
	foldedPaths    map[string]string
	leaves         []pinnedValidationFilesystemLeaf
	symlinks       []*pinnedValidationTreeEntry
	pathBytes      int
	totalBytes     int64
}

func pinnedValidationExpectedLeaves(
	entries []pinnedValidationTreeEntry,
) map[string]pinnedValidationExpectedLeaf {
	result := make(map[string]pinnedValidationExpectedLeaf, len(entries))
	for _, entry := range entries {
		result[entry.path] = pinnedValidationExpectedLeaf{
			mode: entry.mode,
			kind: entry.kind,
		}
	}
	return result
}

func (temporary *pinnedValidationTemporaryRoots) validateBaseIdentity() error {
	if temporary == nil || temporary.root == nil || temporary.path == "" ||
		temporary.baseIdentity == nil {
		return errors.New("pinned validation root identity is unavailable")
	}
	named, err := os.Lstat(temporary.path)
	if err != nil {
		return fmt.Errorf("inspect named pinned validation root: %w", err)
	}
	anchored, err := temporary.root.Lstat(".")
	if err != nil {
		return fmt.Errorf("inspect anchored pinned validation root: %w", err)
	}
	for _, current := range []fs.FileInfo{named, anchored} {
		if current == nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
			current.Mode() != temporary.baseIdentity.Mode() ||
			!os.SameFile(temporary.baseIdentity, current) ||
			!pinnedValidationNodeChangeToken(temporary.baseIdentity).equal(
				pinnedValidationNodeChangeToken(current),
			) {
			return errors.New("pinned validation root identity or mode changed")
		}
	}
	return nil
}

func (temporary *pinnedValidationTemporaryRoots) snapshotValidationRoot(
	ctx context.Context,
	name, tree string,
	expectedIdentity fs.FileInfo,
	expectedLeaves map[string]pinnedValidationExpectedLeaf,
	baseline *pinnedValidationFilesystemSnapshot,
) (pinnedValidationFilesystemSnapshot, error) {
	if temporary == nil || temporary.root == nil || expectedIdentity == nil {
		return pinnedValidationFilesystemSnapshot{}, errors.New(
			"pinned validation filesystem snapshot is unavailable",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return pinnedValidationFilesystemSnapshot{}, err
	}
	namedBefore, err := temporary.root.Lstat(name)
	if err != nil {
		return pinnedValidationFilesystemSnapshot{}, fmt.Errorf(
			"inspect named disposable %s root: %w",
			name,
			err,
		)
	}
	if identityErr := validatePinnedValidationRootInfo(expectedIdentity, namedBefore); identityErr != nil {
		return pinnedValidationFilesystemSnapshot{}, fmt.Errorf(
			"disposable %s root changed before snapshot: %w",
			name,
			identityErr,
		)
	}
	root, err := temporary.root.OpenRoot(name)
	if err != nil {
		return pinnedValidationFilesystemSnapshot{}, fmt.Errorf(
			"open disposable %s root for snapshot: %w",
			name,
			err,
		)
	}
	opened, err := root.Lstat(".")
	if err != nil || validatePinnedValidationNodeStability(namedBefore, opened) != nil {
		_ = root.Close()
		if err != nil {
			return pinnedValidationFilesystemSnapshot{}, fmt.Errorf(
				"inspect opened disposable %s root: %w",
				name,
				err,
			)
		}
		return pinnedValidationFilesystemSnapshot{}, fmt.Errorf(
			"disposable %s root changed while opening",
			name,
		)
	}
	walk := &pinnedValidationFilesystemWalk{
		ctx:            ctx,
		expectedLeaves: expectedLeaves,
		baseline:       baseline,
		nodes:          make(map[string]pinnedValidationFilesystemNode),
		foldedPaths:    make(map[string]string),
		leaves:         make([]pinnedValidationFilesystemLeaf, 0, len(expectedLeaves)),
	}
	if err := walk.admitNode(".", opened, "directory"); err != nil {
		_ = root.Close()
		return pinnedValidationFilesystemSnapshot{}, err
	}
	walkErr := walk.directory(root, "")
	openedAfter, openedAfterErr := root.Lstat(".")
	closeErr := root.Close()
	namedAfter, namedAfterErr := temporary.root.Lstat(name)
	if walkErr != nil || openedAfterErr != nil || namedAfterErr != nil || closeErr != nil {
		return pinnedValidationFilesystemSnapshot{}, errors.Join(
			walkErr,
			openedAfterErr,
			namedAfterErr,
			closeErr,
		)
	}
	if err := validatePinnedValidationNodeStability(opened, openedAfter); err != nil {
		return pinnedValidationFilesystemSnapshot{}, fmt.Errorf(
			"opened disposable %s root changed during snapshot: %w",
			name,
			err,
		)
	}
	if err := validatePinnedValidationNodeStability(opened, namedAfter); err != nil {
		return pinnedValidationFilesystemSnapshot{}, fmt.Errorf(
			"named disposable %s root changed during snapshot: %w",
			name,
			err,
		)
	}
	return walk.finish(tree)
}

func validatePinnedValidationRootInfo(expected, actual fs.FileInfo) error {
	if expected == nil || actual == nil || !actual.IsDir() ||
		actual.Mode()&os.ModeSymlink != 0 || expected.Mode() != actual.Mode() ||
		!os.SameFile(expected, actual) {
		return errors.New("directory identity or mode changed")
	}
	return nil
}

func validatePinnedValidationNodeStability(expected, actual fs.FileInfo) error {
	if err := validatePinnedValidationRootInfo(expected, actual); err != nil {
		return err
	}
	if !pinnedValidationNodeChangeToken(expected).equal(
		pinnedValidationNodeChangeToken(actual),
	) {
		return errors.New("directory change metadata changed")
	}
	return nil
}

func (walk *pinnedValidationFilesystemWalk) directory(
	root *os.Root,
	prefix string,
) error {
	if err := walk.ctx.Err(); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open disposable validation directory for reading: %w", err)
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() {
		if err != nil {
			return fmt.Errorf("inspect disposable validation directory: %w", err)
		}
		return errors.New("disposable validation directory changed while opening")
	}
	anchored, err := root.Lstat(".")
	if err != nil || validatePinnedValidationNodeStability(opened, anchored) != nil {
		if err != nil {
			return fmt.Errorf("inspect anchored disposable validation directory: %w", err)
		}
		return errors.New("disposable validation directory changed while opening")
	}
	for {
		if err := walk.ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if err := walk.ctx.Err(); err != nil {
				return err
			}
			logicalPath := entry.Name()
			if prefix != "" {
				logicalPath = path.Join(prefix, entry.Name())
			}
			if err := validatePinnedValidationPath(logicalPath); err != nil {
				return err
			}
			before, err := root.Lstat(entry.Name())
			if err != nil {
				return fmt.Errorf("inspect disposable validation path %q: %w", logicalPath, err)
			}
			kind, err := pinnedValidationFilesystemKind(before.Mode())
			if err != nil {
				return fmt.Errorf("disposable validation path %q: %w", logicalPath, err)
			}
			if err := walk.admitNode(logicalPath, before, kind); err != nil {
				return err
			}
			switch kind {
			case "directory":
				if _, leaf := walk.expectedLeaves[logicalPath]; leaf {
					return fmt.Errorf("disposable validation leaf %q became a directory", logicalPath)
				}
				if err := walk.subdirectory(root, entry.Name(), logicalPath, before); err != nil {
					return err
				}
			case "regular":
				if err := walk.regular(root, directory, entry.Name(), logicalPath, before); err != nil {
					return err
				}
			case "symlink":
				if err := walk.symlink(root, entry.Name(), logicalPath, before); err != nil {
					return err
				}
			default:
				return fmt.Errorf("disposable validation path %q has an unsupported type", logicalPath)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read disposable validation directory: %w", readErr)
		}
	}
	return nil
}

func (walk *pinnedValidationFilesystemWalk) admitNode(
	logicalPath string,
	info fs.FileInfo,
	kind string,
) error {
	changeToken := pinnedValidationNodeChangeToken(info)
	if len(walk.nodes) >= maxPinnedValidationFilesystemNodes {
		return errors.New("disposable validation root has too many filesystem nodes")
	}
	if logicalPath != "." {
		walk.pathBytes += len(logicalPath)
		if walk.pathBytes > maxPinnedValidationFilesystemPathBytes {
			return errors.New("disposable validation filesystem paths exceed their limit")
		}
		folded := pinnedValidationFoldPath(logicalPath)
		if previous, found := walk.foldedPaths[folded]; found && previous != logicalPath {
			return fmt.Errorf(
				"disposable validation paths collide: %q and %q",
				previous,
				logicalPath,
			)
		}
		walk.foldedPaths[folded] = logicalPath
	}
	if _, duplicate := walk.nodes[logicalPath]; duplicate {
		return fmt.Errorf("duplicate disposable validation path %q", logicalPath)
	}
	if walk.baseline != nil {
		expected, found := walk.baseline.nodes[logicalPath]
		if !found || expected.kind != kind || expected.mode != info.Mode() ||
			!os.SameFile(expected.identity, info) ||
			!expected.changeToken.equal(changeToken) {
			return fmt.Errorf(
				"disposable validation path %q changed identity, type, or mode",
				logicalPath,
			)
		}
	}
	walk.nodes[logicalPath] = pinnedValidationFilesystemNode{
		identity:    info,
		mode:        info.Mode(),
		kind:        kind,
		changeToken: changeToken,
	}
	return nil
}

func (walk *pinnedValidationFilesystemWalk) subdirectory(
	root *os.Root,
	name, logicalPath string,
	before fs.FileInfo,
) error {
	child, err := root.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("open disposable validation directory %q: %w", logicalPath, err)
	}
	opened, err := child.Lstat(".")
	if err != nil || validatePinnedValidationNodeStability(before, opened) != nil {
		_ = child.Close()
		if err != nil {
			return fmt.Errorf("inspect disposable validation directory %q: %w", logicalPath, err)
		}
		return fmt.Errorf("disposable validation directory %q changed while opening", logicalPath)
	}
	walkErr := walk.directory(child, logicalPath)
	openedAfter, openedAfterErr := child.Lstat(".")
	closeErr := child.Close()
	namedAfter, namedAfterErr := root.Lstat(name)
	if walkErr != nil || openedAfterErr != nil || namedAfterErr != nil || closeErr != nil {
		return errors.Join(walkErr, openedAfterErr, namedAfterErr, closeErr)
	}
	if err := validatePinnedValidationNodeStability(opened, openedAfter); err != nil {
		return fmt.Errorf("disposable validation directory %q changed while reading: %w", logicalPath, err)
	}
	if err := validatePinnedValidationNodeStability(opened, namedAfter); err != nil {
		return fmt.Errorf("disposable validation directory %q changed after reading: %w", logicalPath, err)
	}
	return nil
}

func (walk *pinnedValidationFilesystemWalk) regular(
	root *os.Root,
	directory *os.File,
	name, logicalPath string,
	before fs.FileInfo,
) error {
	expected, found := walk.expectedLeaves[logicalPath]
	if !found || expected.kind != "regular" {
		return fmt.Errorf("unexpected regular disposable validation path %q", logicalPath)
	}
	file, err := openPinnedValidationRegular(directory, root, name)
	if err != nil {
		return fmt.Errorf("open disposable validation file %q: %w", logicalPath, err)
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode() != before.Mode() ||
		!os.SameFile(before, opened) || !pinnedValidationFileHasSingleLink(file, opened) {
		_ = file.Close()
		if err != nil {
			return fmt.Errorf("inspect disposable validation file %q: %w", logicalPath, err)
		}
		return fmt.Errorf("disposable validation file %q was swapped or hard-linked", logicalPath)
	}
	if opened.Size() < 0 || opened.Size() > maxPinnedValidationBlobBytes ||
		walk.totalBytes > maxPinnedValidationTreeBytes-opened.Size() {
		_ = file.Close()
		return fmt.Errorf("disposable validation file %q exceeds its byte limit", logicalPath)
	}
	digest := sha256.New()
	reader := &pinnedValidationContextReader{ctx: walk.ctx, reader: file}
	read, readErr := io.Copy(digest, io.LimitReader(reader, opened.Size()+1))
	openedAfter, openedAfterErr := file.Stat()
	singleLinkAfter := openedAfterErr == nil &&
		pinnedValidationFileHasSingleLink(file, openedAfter)
	closeErr := file.Close()
	namedAfter, namedAfterErr := root.Lstat(name)
	if readErr != nil || openedAfterErr != nil || namedAfterErr != nil || closeErr != nil {
		return errors.Join(readErr, openedAfterErr, namedAfterErr, closeErr)
	}
	if read != opened.Size() || openedAfter.Size() != opened.Size() ||
		openedAfter.Mode() != opened.Mode() || !os.SameFile(opened, openedAfter) ||
		!pinnedValidationNodeChangeToken(opened).equal(
			pinnedValidationNodeChangeToken(openedAfter),
		) ||
		!singleLinkAfter || namedAfter.Mode() != opened.Mode() ||
		!os.SameFile(opened, namedAfter) ||
		!pinnedValidationNodeChangeToken(openedAfter).equal(
			pinnedValidationNodeChangeToken(namedAfter),
		) {
		return fmt.Errorf("disposable validation file %q changed while reading", logicalPath)
	}
	walk.totalBytes += read
	walk.leaves = append(walk.leaves, pinnedValidationFilesystemLeaf{
		path:          logicalPath,
		mode:          expected.mode,
		kind:          expected.kind,
		size:          read,
		contentDigest: hex.EncodeToString(digest.Sum(nil)),
	})
	return nil
}

func (walk *pinnedValidationFilesystemWalk) symlink(
	root *os.Root,
	name, logicalPath string,
	before fs.FileInfo,
) error {
	expected, found := walk.expectedLeaves[logicalPath]
	if !found || expected.kind != "symlink" || !pinnedValidationSymlinkHasSingleLink(before) {
		return fmt.Errorf("unexpected or hard-linked disposable validation symlink %q", logicalPath)
	}
	target, err := root.Readlink(name)
	if err != nil {
		return fmt.Errorf("read disposable validation symlink %q: %w", logicalPath, err)
	}
	if int64(len(target)) > maxPinnedValidationSymlinkBytes ||
		walk.totalBytes > maxPinnedValidationTreeBytes-int64(len(target)) {
		return fmt.Errorf("disposable validation symlink %q exceeds its byte limit", logicalPath)
	}
	resolved, err := validatePinnedValidationSymlink(logicalPath, target)
	if err != nil {
		return err
	}
	after, err := root.Lstat(name)
	if err != nil || after.Mode() != before.Mode() || !os.SameFile(before, after) ||
		!pinnedValidationNodeChangeToken(before).equal(
			pinnedValidationNodeChangeToken(after),
		) ||
		!pinnedValidationSymlinkHasSingleLink(after) {
		if err != nil {
			return fmt.Errorf("inspect disposable validation symlink %q: %w", logicalPath, err)
		}
		return fmt.Errorf("disposable validation symlink %q changed while reading", logicalPath)
	}
	digest := sha256.Sum256([]byte(target))
	walk.totalBytes += int64(len(target))
	walk.leaves = append(walk.leaves, pinnedValidationFilesystemLeaf{
		path:          logicalPath,
		mode:          expected.mode,
		kind:          expected.kind,
		size:          int64(len(target)),
		contentDigest: hex.EncodeToString(digest[:]),
	})
	walk.symlinks = append(walk.symlinks, &pinnedValidationTreeEntry{
		path:       logicalPath,
		mode:       expected.mode,
		kind:       expected.kind,
		linkTarget: target,
		linkPath:   resolved,
	})
	return nil
}

func (walk *pinnedValidationFilesystemWalk) finish(
	tree string,
) (pinnedValidationFilesystemSnapshot, error) {
	if len(walk.leaves) != len(walk.expectedLeaves) {
		return pinnedValidationFilesystemSnapshot{}, errors.New(
			"disposable validation root is missing expected leaves",
		)
	}
	if err := validatePinnedValidationSymlinkGraph(walk.symlinks); err != nil {
		return pinnedValidationFilesystemSnapshot{}, err
	}
	sort.Slice(walk.leaves, func(left, right int) bool {
		return walk.leaves[left].path < walk.leaves[right].path
	})
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pinned-validation-tree-v1\x00"))
	writePinnedDigestField(digest, tree)
	for _, leaf := range walk.leaves {
		writePinnedDigestField(digest, leaf.path)
		writePinnedDigestField(digest, leaf.mode)
		writePinnedDigestField(digest, leaf.kind)
		writePinnedDigestField(digest, strconv.FormatInt(leaf.size, 10))
		writePinnedDigestField(digest, leaf.contentDigest)
	}
	writePinnedDigestField(digest, strconv.Itoa(len(walk.leaves)))
	writePinnedDigestField(digest, strconv.FormatInt(walk.totalBytes, 10))
	return pinnedValidationFilesystemSnapshot{
		manifest: PinnedTreeManifest{
			Tree:    tree,
			Digest:  hex.EncodeToString(digest.Sum(nil)),
			Entries: len(walk.leaves),
			Bytes:   walk.totalBytes,
		},
		nodes: walk.nodes,
	}, nil
}

func pinnedValidationFilesystemKind(mode fs.FileMode) (string, error) {
	if mode&(os.ModeAppend|os.ModeExclusive|os.ModeTemporary|os.ModeSetuid|
		os.ModeSetgid|os.ModeSticky) != 0 {
		return "", errors.New("unsupported filesystem mode")
	}
	switch mode.Type() {
	case 0:
		return "regular", nil
	case os.ModeDir:
		return "directory", nil
	case os.ModeSymlink:
		return "symlink", nil
	default:
		return "", errors.New("special filesystem nodes are unsupported")
	}
}

type pinnedValidationContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *pinnedValidationContextReader) Read(value []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(value)
}

func comparePinnedValidationFilesystemSnapshots(
	expected, actual pinnedValidationFilesystemSnapshot,
) error {
	if expected.manifest != actual.manifest {
		return errors.New("disposable validation manifest changed")
	}
	if len(expected.nodes) != len(actual.nodes) {
		return errors.New("disposable validation filesystem paths changed")
	}
	for logicalPath, expectedNode := range expected.nodes {
		actualNode, found := actual.nodes[logicalPath]
		if !found || expectedNode.kind != actualNode.kind ||
			expectedNode.mode != actualNode.mode ||
			!os.SameFile(expectedNode.identity, actualNode.identity) ||
			!expectedNode.changeToken.equal(actualNode.changeToken) {
			return fmt.Errorf(
				"disposable validation path %q changed identity, type, or mode",
				logicalPath,
			)
		}
	}
	return nil
}

func (temporary *pinnedValidationTemporaryRoots) revalidateCallbackRoots(
	ctx context.Context,
	public PinnedCandidateValidationRoots,
) error {
	var result error
	result = errors.Join(result, temporary.validateBaseIdentity())
	parent, parentErr := temporary.snapshotValidationRoot(
		ctx,
		"parent",
		public.ParentManifest.Tree,
		temporary.parentIdentity,
		temporary.parentExpected,
		&temporary.parentSnapshot,
	)
	if parentErr != nil {
		result = errors.Join(result, fmt.Errorf("revalidate disposable parent root: %w", parentErr))
	} else {
		if parent.manifest != public.ParentManifest {
			result = errors.Join(result, errors.New("disposable parent manifest changed"))
		}
		result = errors.Join(
			result,
			comparePinnedValidationFilesystemSnapshots(temporary.parentSnapshot, parent),
		)
	}
	candidate, candidateErr := temporary.snapshotValidationRoot(
		ctx,
		"candidate",
		public.CandidateManifest.Tree,
		temporary.candidateIdentity,
		temporary.candidateExpected,
		&temporary.candidateSnapshot,
	)
	if candidateErr != nil {
		result = errors.Join(result, fmt.Errorf("revalidate disposable candidate root: %w", candidateErr))
	} else {
		if candidate.manifest != public.CandidateManifest {
			result = errors.Join(result, errors.New("disposable candidate manifest changed"))
		}
		result = errors.Join(
			result,
			comparePinnedValidationFilesystemSnapshots(temporary.candidateSnapshot, candidate),
		)
	}
	result = errors.Join(result, temporary.validateBaseIdentity())
	return result
}
