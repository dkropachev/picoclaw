package fstools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maximumFileIdentityCatalogEntries   = 2_000_000
	maximumFileIdentityCatalogPathBytes = int64(1 << 30)
	maximumFileIdentityCatalogDepth     = 64
	fileIdentityCatalogReadBatch        = 256
)

// FileIdentityCatalogOptions describes an immutable snapshot of protected
// regular-file identities. ExactPaths may name missing files or directories;
// TreeRoots recursively capture every regular file below an existing real
// directory. Missing inputs are remembered during the two-pass snapshot so a
// concurrent creation fails construction instead of producing a partial
// catalog.
type FileIdentityCatalogOptions struct {
	ExactPaths []string
	TreeRoots  []string
	// ExcludePaths omits only these exact regular-file paths from TreeRoots. It
	// is intended for volatile SQLite files that already have lexical root
	// protection while adjacent legacy files still need identity capture.
	ExcludePaths []string
	MaxEntries   int
	// MaxPathBytes bounds the aggregate byte length of examined exact and
	// tree-relative path names. MaxDepth bounds recursive tree nesting.
	MaxPathBytes int64
	MaxDepth     int
}

// FileIdentityCatalog is an immutable, generation-shareable set of physical
// regular-file identities. A single catalog pointer can be shared by root
// tools, every owner factory product, apply_patch, and local repair without
// copying a potentially large identity set.
type FileIdentityCatalog struct {
	identities map[string]struct{}
}

type fileIdentityCatalogEntry struct {
	info     os.FileInfo
	identity string
}

type fileIdentityCatalogSnapshot struct {
	entries map[string]fileIdentityCatalogEntry
	missing map[string]struct{}
}

// Test seam used to deterministically prove that namespace changes between
// the two complete snapshots fail closed.
var fileIdentityCatalogBetweenSnapshots func()

// NewFileIdentityCatalog captures a bounded two-pass snapshot. Diagnostics are
// intentionally path-free because the protected paths can encode identities
// or other private runtime details.
func NewFileIdentityCatalog(options FileIdentityCatalogOptions) (*FileIdentityCatalog, error) {
	limit := options.MaxEntries
	if limit == 0 {
		limit = maximumFileIdentityCatalogEntries
	}
	if limit < 1 || limit > maximumFileIdentityCatalogEntries {
		return nil, errors.New("file-identity catalog limit is invalid")
	}
	pathLimit := options.MaxPathBytes
	if pathLimit == 0 {
		pathLimit = maximumFileIdentityCatalogPathBytes
	}
	if pathLimit < 1 || pathLimit > maximumFileIdentityCatalogPathBytes {
		return nil, errors.New("file-identity catalog path-byte limit is invalid")
	}
	depthLimit := options.MaxDepth
	if depthLimit == 0 {
		depthLimit = maximumFileIdentityCatalogDepth
	}
	if depthLimit < 1 || depthLimit > maximumFileIdentityCatalogDepth {
		return nil, errors.New("file-identity catalog depth limit is invalid")
	}
	if len(options.ExactPaths) > limit || len(options.TreeRoots) > limit ||
		len(options.ExcludePaths) > 1024 {
		return nil, errors.New("file-identity catalog input limit exceeded")
	}
	exact, err := prepareFileIdentityCatalogInputs(options.ExactPaths, "exact")
	if err != nil {
		return nil, err
	}
	trees, err := prepareFileIdentityCatalogInputs(options.TreeRoots, "tree")
	if err != nil {
		return nil, err
	}
	excluded, err := prepareFileIdentityCatalogExclusions(options.ExcludePaths)
	if err != nil {
		return nil, err
	}
	first, err := collectFileIdentityCatalogSnapshot(
		exact,
		trees,
		excluded,
		fileIdentityCatalogLimits{entries: limit, pathBytes: pathLimit, depth: depthLimit},
	)
	if err != nil {
		return nil, err
	}
	if fileIdentityCatalogBetweenSnapshots != nil {
		fileIdentityCatalogBetweenSnapshots()
	}
	second, err := collectFileIdentityCatalogSnapshot(
		exact,
		trees,
		excluded,
		fileIdentityCatalogLimits{entries: limit, pathBytes: pathLimit, depth: depthLimit},
	)
	if err != nil {
		return nil, err
	}
	if !equalFileIdentityCatalogSnapshots(first, second) {
		return nil, errors.New("file-identity catalog inputs changed during snapshot")
	}
	identities := make(map[string]struct{}, len(second.entries))
	for _, entry := range second.entries {
		if entry.identity != "" {
			identities[entry.identity] = struct{}{}
		}
	}
	return &FileIdentityCatalog{identities: identities}, nil
}

func prepareFileIdentityCatalogExclusions(paths []string) (map[string]struct{}, error) {
	prepared := make(map[string]struct{}, len(paths)*2)
	for index, path := range append([]string(nil), paths...) {
		if path == "" || path != strings.TrimSpace(path) || !utf8.ValidString(path) ||
			strings.ContainsRune(path, '\x00') {
			return nil, fmt.Errorf("file-identity catalog exclusion %d is invalid", index)
		}
		absolute, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("file-identity catalog exclusion %d is invalid", index)
		}
		prepared[fileMutationPathKey(absolute)] = struct{}{}
		canonical, resolveErr := resolvePathAgainstExistingAncestor(absolute)
		if resolveErr != nil {
			return nil, fmt.Errorf("file-identity catalog exclusion %d cannot be resolved", index)
		}
		prepared[fileMutationPathKey(canonical)] = struct{}{}
	}
	return prepared, nil
}

func prepareFileIdentityCatalogInputs(inputs []string, kind string) ([]string, error) {
	prepared := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for index, input := range append([]string(nil), inputs...) {
		if input == "" || input != strings.TrimSpace(input) || !utf8.ValidString(input) ||
			strings.ContainsRune(input, '\x00') {
			return nil, fmt.Errorf("file-identity catalog %s input %d is invalid", kind, index)
		}
		absolute, err := filepath.Abs(filepath.Clean(input))
		if err != nil {
			return nil, fmt.Errorf("file-identity catalog %s input %d is invalid", kind, index)
		}
		key := fileMutationPathKey(absolute)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		prepared = append(prepared, absolute)
	}
	sort.Strings(prepared)
	return prepared, nil
}

func collectFileIdentityCatalogSnapshot(
	exact []string,
	trees []string,
	excludedPaths map[string]struct{},
	limits fileIdentityCatalogLimits,
) (fileIdentityCatalogSnapshot, error) {
	snapshot := fileIdentityCatalogSnapshot{
		entries: make(map[string]fileIdentityCatalogEntry),
		missing: make(map[string]struct{}),
	}
	add := func(path string, info os.FileInfo) error {
		key := fileMutationPathKey(path)
		if previous, exists := snapshot.entries[key]; exists {
			if previous.info == nil || info == nil || !os.SameFile(previous.info, info) ||
				previous.info.Mode() != info.Mode() {
				return errors.New("file-identity catalog contains an unstable entry")
			}
			return nil
		}
		if len(snapshot.entries) >= limits.entries {
			return errors.New("file-identity catalog entry limit exceeded")
		}
		entry := fileIdentityCatalogEntry{info: info}
		if info.Mode().IsRegular() {
			identity, err := snapshotFileIdentity(path, info)
			if err != nil {
				return errors.New("file-identity catalog entry cannot be identified")
			}
			entry.identity = identity
		}
		snapshot.entries[key] = entry
		return nil
	}
	budget := fileIdentityCatalogBudget{limits: limits}
	for _, path := range exact {
		if err := budget.consume(path); err != nil {
			return fileIdentityCatalogSnapshot{}, err
		}
		info, err := os.Lstat(path)
		switch {
		case os.IsNotExist(err):
			snapshot.missing["exact\x00"+fileMutationPathKey(path)] = struct{}{}
		case err != nil:
			return fileIdentityCatalogSnapshot{}, errors.New("file-identity catalog exact input cannot be inspected")
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular():
			return fileIdentityCatalogSnapshot{}, errors.New("file-identity catalog exact input is unsafe")
		default:
			if err := add(path, info); err != nil {
				return fileIdentityCatalogSnapshot{}, err
			}
		}
	}
	for _, root := range trees {
		rootInfo, err := os.Lstat(root)
		switch {
		case os.IsNotExist(err):
			if budgetErr := budget.consume(root); budgetErr != nil {
				return fileIdentityCatalogSnapshot{}, budgetErr
			}
			snapshot.missing["tree\x00"+fileMutationPathKey(root)] = struct{}{}
			continue
		case err != nil:
			return fileIdentityCatalogSnapshot{}, errors.New("file-identity catalog tree cannot be inspected")
		case rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir():
			return fileIdentityCatalogSnapshot{}, errors.New("file-identity catalog tree is unsafe")
		}
		pinned, openErr := os.OpenRoot(root)
		if openErr != nil {
			return fileIdentityCatalogSnapshot{}, errors.New("file-identity catalog tree cannot be opened")
		}
		openedInfo, openedErr := pinned.Stat(".")
		if openedErr != nil || !stableFileIdentityCatalogDirectory(rootInfo, openedInfo) {
			_ = pinned.Close()
			return fileIdentityCatalogSnapshot{}, errors.New("file-identity catalog tree changed while opening")
		}
		walkErr := collectPinnedFileIdentityCatalogTree(
			pinned,
			root,
			".",
			excludedPaths,
			1,
			&budget,
			add,
		)
		closeErr := pinned.Close()
		if walkErr != nil || closeErr != nil {
			return fileIdentityCatalogSnapshot{}, errors.New("file-identity catalog tree cannot be enumerated")
		}
	}
	return snapshot, nil
}

type fileIdentityCatalogLimits struct {
	entries   int
	pathBytes int64
	depth     int
}

type fileIdentityCatalogBudget struct {
	limits    fileIdentityCatalogLimits
	entries   int
	pathBytes int64
}

func (budget *fileIdentityCatalogBudget) consume(relative string) error {
	if budget == nil || budget.limits.entries < 1 || budget.limits.pathBytes < 1 ||
		budget.limits.depth < 1 {
		return errors.New("file-identity catalog budget is invalid")
	}
	budget.entries++
	if budget.entries > budget.limits.entries {
		return errors.New("file-identity catalog entry limit exceeded")
	}
	length := int64(len(relative))
	if length < 0 || budget.pathBytes > budget.limits.pathBytes-length {
		return errors.New("file-identity catalog path-byte limit exceeded")
	}
	budget.pathBytes += length
	return nil
}

func collectPinnedFileIdentityCatalogTree(
	root *os.Root,
	lexical string,
	relative string,
	excludedPaths map[string]struct{},
	depth int,
	budget *fileIdentityCatalogBudget,
	add func(string, os.FileInfo) error,
) error {
	if root == nil || add == nil || budget == nil || depth < 1 ||
		depth > budget.limits.depth {
		return errors.New("pinned identity tree is invalid")
	}
	if err := budget.consume(relative); err != nil {
		return err
	}
	before, err := root.Stat(".")
	if err != nil || !before.IsDir() {
		return errors.New("pinned identity directory is unavailable")
	}
	if addErr := add(lexical, before); addErr != nil {
		return addErr
	}
	directory, err := root.Open(".")
	if err != nil {
		return errors.New("pinned identity directory cannot be opened")
	}
	opened, openedErr := directory.Stat()
	if openedErr != nil || !stableFileIdentityCatalogDirectory(before, opened) {
		_ = directory.Close()
		return errors.New("pinned identity directory changed while opening")
	}
	closeDirectory := func(returnErr error) error {
		if closeErr := directory.Close(); closeErr != nil && returnErr == nil {
			return errors.New("pinned identity directory cannot be closed")
		}
		return returnErr
	}
	for {
		entries, readErr := directory.ReadDir(fileIdentityCatalogReadBatch)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return closeDirectory(errors.New("pinned identity directory cannot be read"))
		}
		for _, entry := range entries {
			name := entry.Name()
			childRelative := filepath.Join(relative, name)
			info, infoErr := root.Lstat(name)
			if infoErr != nil || entry.Type()&os.ModeSymlink != 0 ||
				info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
				return closeDirectory(errors.New("pinned identity tree contains an unsafe entry"))
			}
			entryInfo, entryErr := entry.Info()
			if entryErr != nil || entryInfo.Mode() != info.Mode() || !os.SameFile(entryInfo, info) {
				return closeDirectory(errors.New("pinned identity tree entry changed during inspection"))
			}
			path := filepath.Join(lexical, name)
			if info.Mode().IsRegular() {
				if consumeErr := budget.consume(childRelative); consumeErr != nil {
					return closeDirectory(consumeErr)
				}
				if fileIdentityCatalogExcluded(path, excludedPaths) {
					continue
				}
				if addErr := add(path, info); addErr != nil {
					return closeDirectory(addErr)
				}
				continue
			}
			child, openChildErr := root.OpenRoot(name)
			if openChildErr != nil {
				return closeDirectory(errors.New("pinned identity child cannot be opened"))
			}
			childInfo, childInfoErr := child.Stat(".")
			if childInfoErr != nil || !stableFileIdentityCatalogDirectory(info, childInfo) {
				_ = child.Close()
				return closeDirectory(errors.New("pinned identity child changed while opening"))
			}
			childErr := collectPinnedFileIdentityCatalogTree(
				child,
				path,
				childRelative,
				excludedPaths,
				depth+1,
				budget,
				add,
			)
			childCloseErr := child.Close()
			if childErr != nil || childCloseErr != nil {
				return closeDirectory(errors.New("pinned identity child cannot be enumerated"))
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if closeErr := closeDirectory(nil); closeErr != nil {
		return closeErr
	}
	after, err := root.Stat(".")
	if err != nil || !stableFileIdentityCatalogDirectory(before, after) {
		return errors.New("pinned identity directory changed during enumeration")
	}
	return nil
}

func stableFileIdentityCatalogDirectory(before, after os.FileInfo) bool {
	return before != nil && after != nil && before.IsDir() && after.IsDir() &&
		before.Mode() == after.Mode() && before.Size() == after.Size() &&
		before.ModTime().Equal(after.ModTime()) && os.SameFile(before, after)
}

func fileIdentityCatalogExcluded(path string, excluded map[string]struct{}) bool {
	_, found := excluded[fileMutationPathKey(path)]
	return found
}

func equalFileIdentityCatalogSnapshots(left, right fileIdentityCatalogSnapshot) bool {
	if len(left.entries) != len(right.entries) || len(left.missing) != len(right.missing) {
		return false
	}
	for missing := range left.missing {
		if _, exists := right.missing[missing]; !exists {
			return false
		}
	}
	for key, before := range left.entries {
		after, exists := right.entries[key]
		if !exists || before.info == nil || after.info == nil ||
			before.info.Mode() != after.info.Mode() || !os.SameFile(before.info, after.info) ||
			before.identity != after.identity || before.info.IsDir() &&
			(!before.info.ModTime().Equal(after.info.ModTime()) || before.info.Size() != after.info.Size()) {
			return false
		}
	}
	return true
}

// ProtectsPath reports whether path still names an identity captured by the
// immutable catalog. Callers supply the already inspected FileInfo so the
// platform implementation can reject a path swap while opening its identity.
func (catalog *FileIdentityCatalog) ProtectsPath(path string, info os.FileInfo) (bool, error) {
	if catalog == nil {
		return false, nil
	}
	if path == "" || info == nil || !info.Mode().IsRegular() {
		return false, errors.New("file-identity lookup is invalid")
	}
	identity, err := snapshotFileIdentity(path, info)
	if err != nil {
		return false, errors.New("file-identity lookup failed")
	}
	_, protected := catalog.identities[identity]
	return protected, nil
}

// Len returns the number of distinct physical regular-file identities. It is
// intended for construction diagnostics and tests; the catalog remains
// immutable.
func (catalog *FileIdentityCatalog) Len() int {
	if catalog == nil {
		return 0
	}
	return len(catalog.identities)
}
