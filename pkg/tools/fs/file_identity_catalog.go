package fstools

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	defaultFileIdentityCatalogEntries   = 131_072
	defaultFileIdentityCatalogPathBytes = int64(32 << 20)
	defaultFileIdentityCatalogDepth     = 32
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

// fileIdentityCatalogDigest is the complete retained state from the first
// pass. Its fixed size is important: catalog construction must not retain a
// path-sized snapshot while it builds the final identity set.
type fileIdentityCatalogDigest struct {
	xor     [sha256.Size]byte
	sum     [sha256.Size]byte
	records uint64
}

// Test seam used to deterministically prove that namespace changes between
// the two complete streaming passes fail closed.
var fileIdentityCatalogBetweenSnapshots func()

// NewFileIdentityCatalog captures a bounded two-pass snapshot. The first pass
// retains only a fixed-size digest; the second pass builds the final distinct
// physical-identity set. Diagnostics are intentionally path-free because the
// protected paths can encode identities or other private runtime details.
func NewFileIdentityCatalog(options FileIdentityCatalogOptions) (*FileIdentityCatalog, error) {
	limit := options.MaxEntries
	if limit == 0 {
		limit = defaultFileIdentityCatalogEntries
	}
	if limit < 1 || limit > maximumFileIdentityCatalogEntries {
		return nil, errors.New("file-identity catalog limit is invalid")
	}
	pathLimit := options.MaxPathBytes
	if pathLimit == 0 {
		pathLimit = defaultFileIdentityCatalogPathBytes
	}
	if pathLimit < 1 || pathLimit > maximumFileIdentityCatalogPathBytes {
		return nil, errors.New("file-identity catalog path-byte limit is invalid")
	}
	depthLimit := options.MaxDepth
	if depthLimit == 0 {
		depthLimit = defaultFileIdentityCatalogDepth
	}
	if depthLimit < 1 || depthLimit > maximumFileIdentityCatalogDepth {
		return nil, errors.New("file-identity catalog depth limit is invalid")
	}
	if len(options.ExactPaths) > limit || len(options.TreeRoots) > limit ||
		len(options.ExcludePaths) > limit ||
		len(options.ExactPaths) > limit-len(options.TreeRoots) ||
		len(options.ExactPaths)+len(options.TreeRoots) > limit-len(options.ExcludePaths) {
		return nil, errors.New("file-identity catalog input limit exceeded")
	}
	inputBudget := fileIdentityCatalogInputBudget{
		entries: limit,
		bytes:   pathLimit,
	}
	exact, err := prepareFileIdentityCatalogInputs(
		options.ExactPaths,
		"exact",
		&inputBudget,
	)
	if err != nil {
		return nil, err
	}
	trees, err := prepareFileIdentityCatalogInputs(
		options.TreeRoots,
		"tree",
		&inputBudget,
	)
	if err != nil {
		return nil, err
	}
	excluded, err := prepareFileIdentityCatalogExclusions(
		options.ExcludePaths,
		&inputBudget,
	)
	if err != nil {
		return nil, err
	}
	limits := fileIdentityCatalogLimits{entries: limit, pathBytes: pathLimit, depth: depthLimit}
	first, _, err := collectFileIdentityCatalogPass(
		exact,
		trees,
		excluded,
		limits,
		false,
	)
	if err != nil {
		return nil, err
	}
	if fileIdentityCatalogBetweenSnapshots != nil {
		fileIdentityCatalogBetweenSnapshots()
	}
	second, identities, err := collectFileIdentityCatalogPass(
		exact,
		trees,
		excluded,
		limits,
		true,
	)
	if err != nil {
		return nil, err
	}
	if first != second {
		return nil, errors.New("file-identity catalog inputs changed during snapshot")
	}
	return &FileIdentityCatalog{identities: identities}, nil
}

type fileIdentityCatalogInputBudget struct {
	entries       int
	bytes         int64
	usedEntries   int
	usedPathBytes int64
}

func (budget *fileIdentityCatalogInputBudget) consumeRaw(value string) error {
	if budget == nil || budget.entries < 1 || budget.bytes < 1 ||
		budget.usedEntries >= budget.entries {
		return errors.New("file-identity catalog input limit exceeded")
	}
	length := int64(len(value))
	if length < 0 || budget.usedPathBytes > budget.bytes-length {
		return errors.New("file-identity catalog input path-byte limit exceeded")
	}
	budget.usedEntries++
	budget.usedPathBytes += length
	return nil
}

func (budget *fileIdentityCatalogInputBudget) accountExpansion(before, after int) error {
	if budget == nil || before < 0 || after < 0 {
		return errors.New("file-identity catalog input budget is invalid")
	}
	if after <= before {
		return nil
	}
	extra := int64(after - before)
	if budget.usedPathBytes > budget.bytes-extra {
		return errors.New("file-identity catalog input path-byte limit exceeded")
	}
	budget.usedPathBytes += extra
	return nil
}

func (budget *fileIdentityCatalogInputBudget) accountRetained(length int) error {
	if budget == nil || length < 0 {
		return errors.New("file-identity catalog input budget is invalid")
	}
	extra := int64(length)
	if budget.usedPathBytes > budget.bytes-extra {
		return errors.New("file-identity catalog input path-byte limit exceeded")
	}
	budget.usedPathBytes += extra
	return nil
}

func prepareFileIdentityCatalogExclusions(
	paths []string,
	budget *fileIdentityCatalogInputBudget,
) (fileIdentityCatalogExclusions, error) {
	prepared := fileIdentityCatalogExclusions{
		paths:      make(map[string]struct{}, len(paths)*2),
		identities: make(map[string]struct{}, len(paths)),
	}
	for index, path := range append([]string(nil), paths...) {
		if path == "" || path != strings.TrimSpace(path) || !utf8.ValidString(path) ||
			strings.ContainsRune(path, '\x00') {
			return fileIdentityCatalogExclusions{}, fmt.Errorf(
				"file-identity catalog exclusion %d is invalid",
				index,
			)
		}
		if err := budget.consumeRaw(path); err != nil {
			return fileIdentityCatalogExclusions{}, err
		}
		absolute, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return fileIdentityCatalogExclusions{}, fmt.Errorf(
				"file-identity catalog exclusion %d is invalid",
				index,
			)
		}
		if err := budget.accountExpansion(len(path), len(absolute)); err != nil {
			return fileIdentityCatalogExclusions{}, err
		}
		prepared.paths[fileMutationDistinctPathKey(absolute)] = struct{}{}
		canonical, resolveErr := resolvePathAgainstExistingAncestor(absolute)
		if resolveErr != nil {
			return fileIdentityCatalogExclusions{}, fmt.Errorf(
				"file-identity catalog exclusion %d cannot be resolved",
				index,
			)
		}
		if err := budget.accountRetained(len(canonical)); err != nil {
			return fileIdentityCatalogExclusions{}, err
		}
		prepared.paths[fileMutationDistinctPathKey(canonical)] = struct{}{}
		info, statErr := os.Lstat(absolute)
		switch {
		case os.IsNotExist(statErr):
			continue
		case statErr != nil:
			return fileIdentityCatalogExclusions{}, errors.New(
				"file-identity catalog exclusion cannot be inspected",
			)
		case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
			return fileIdentityCatalogExclusions{}, errors.New(
				"file-identity catalog exclusion is unsafe",
			)
		}
		file, openErr := os.Open(absolute)
		if openErr != nil {
			return fileIdentityCatalogExclusions{}, errors.New(
				"file-identity catalog exclusion cannot be opened",
			)
		}
		_, identity, identityErr := verifiedOpenedFileIdentity(file, info, true)
		closeErr := file.Close()
		if identityErr != nil || closeErr != nil {
			return fileIdentityCatalogExclusions{}, errors.New(
				"file-identity catalog exclusion changed while opening",
			)
		}
		prepared.identities[identity] = struct{}{}
	}
	return prepared, nil
}

type fileIdentityCatalogExclusions struct {
	paths      map[string]struct{}
	identities map[string]struct{}
}

func prepareFileIdentityCatalogInputs(
	inputs []string,
	kind string,
	budget *fileIdentityCatalogInputBudget,
) ([]string, error) {
	prepared := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for index, input := range append([]string(nil), inputs...) {
		if input == "" || input != strings.TrimSpace(input) || !utf8.ValidString(input) ||
			strings.ContainsRune(input, '\x00') {
			return nil, fmt.Errorf("file-identity catalog %s input %d is invalid", kind, index)
		}
		if err := budget.consumeRaw(input); err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(filepath.Clean(input))
		if err != nil {
			return nil, fmt.Errorf("file-identity catalog %s input %d is invalid", kind, index)
		}
		if err := budget.accountExpansion(len(input), len(absolute)); err != nil {
			return nil, err
		}
		key := fileMutationDistinctPathKey(absolute)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		prepared = append(prepared, absolute)
	}
	sort.Strings(prepared)
	return prepared, nil
}

type fileIdentityCatalogRecorder struct {
	identities map[string]struct{}
	xor        [sha256.Size]byte
	sum        [sha256.Size]byte
	records    uint64
}

func newFileIdentityCatalogRecorder(captureIdentities bool) *fileIdentityCatalogRecorder {
	recorder := &fileIdentityCatalogRecorder{}
	if captureIdentities {
		recorder.identities = make(map[string]struct{})
	}
	return recorder
}

func (recorder *fileIdentityCatalogRecorder) record(
	kind byte,
	path string,
	info os.FileInfo,
	identity string,
) error {
	if recorder == nil || path == "" || info == nil || identity == "" {
		return errors.New("file-identity catalog record is invalid")
	}
	recorder.records++
	if recorder.records == 0 {
		return errors.New("file-identity catalog record count overflow")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-file-identity-catalog-v3\x00"))
	writeFileIdentityCatalogDigestByte(digest, kind)
	writeFileIdentityCatalogDigestString(digest, fileMutationDistinctPathKey(path))
	writeFileIdentityCatalogDigestUint64(digest, uint64(info.Mode()))
	writeFileIdentityCatalogDigestString(digest, identity)
	if info.IsDir() {
		writeFileIdentityCatalogDigestUint64(digest, uint64(info.Size()))
		writeFileIdentityCatalogDigestUint64(digest, uint64(info.ModTime().UnixNano()))
	}
	recorder.accumulate(digest.Sum(nil))
	if info.Mode().IsRegular() && recorder.identities != nil {
		recorder.identities[identity] = struct{}{}
	}
	return nil
}

func (recorder *fileIdentityCatalogRecorder) recordMissing(kind byte, path string) error {
	if recorder == nil || path == "" {
		return errors.New("file-identity catalog missing record is invalid")
	}
	recorder.records++
	if recorder.records == 0 {
		return errors.New("file-identity catalog record count overflow")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-file-identity-catalog-v3\x00"))
	writeFileIdentityCatalogDigestByte(digest, kind)
	writeFileIdentityCatalogDigestString(digest, fileMutationDistinctPathKey(path))
	recorder.accumulate(digest.Sum(nil))
	return nil
}

// accumulate forms an order-independent multiset digest. XOR preserves every
// bit of each cryptographic record hash while modular addition and the record
// count prevent even-multiplicity cancellation. No path-sized ordering buffer
// is retained between batches or passes.
func (recorder *fileIdentityCatalogRecorder) accumulate(record []byte) {
	if recorder == nil || len(record) != sha256.Size {
		return
	}
	carry := uint16(0)
	for index := sha256.Size - 1; index >= 0; index-- {
		recorder.xor[index] ^= record[index]
		total := uint16(recorder.sum[index]) + uint16(record[index]) + carry
		recorder.sum[index] = byte(total)
		carry = total >> 8
	}
}

func (recorder *fileIdentityCatalogRecorder) finish() fileIdentityCatalogDigest {
	var result fileIdentityCatalogDigest
	if recorder == nil {
		return result
	}
	result.xor = recorder.xor
	result.sum = recorder.sum
	result.records = recorder.records
	return result
}

func writeFileIdentityCatalogDigestByte(digest hash.Hash, value byte) {
	_, _ = digest.Write([]byte{value})
}

func writeFileIdentityCatalogDigestUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}

func writeFileIdentityCatalogDigestString(digest hash.Hash, value string) {
	writeFileIdentityCatalogDigestUint64(digest, uint64(len(value)))
	_, _ = digest.Write([]byte(value))
}

func collectFileIdentityCatalogPass(
	exact []string,
	trees []string,
	exclusions fileIdentityCatalogExclusions,
	limits fileIdentityCatalogLimits,
	captureIdentities bool,
) (fileIdentityCatalogDigest, map[string]struct{}, error) {
	recorder := newFileIdentityCatalogRecorder(captureIdentities)
	budget := fileIdentityCatalogBudget{limits: limits}
	for _, path := range exact {
		if err := budget.consume(path); err != nil {
			return fileIdentityCatalogDigest{}, nil, err
		}
		info, err := os.Lstat(path)
		switch {
		case os.IsNotExist(err):
			if recordErr := recorder.recordMissing('E', path); recordErr != nil {
				return fileIdentityCatalogDigest{}, nil, recordErr
			}
		case err != nil:
			return fileIdentityCatalogDigest{}, nil, errors.New(
				"file-identity catalog exact input cannot be inspected",
			)
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular():
			return fileIdentityCatalogDigest{}, nil, errors.New("file-identity catalog exact input is unsafe")
		default:
			opened, openErr := os.Open(path)
			if openErr != nil {
				return fileIdentityCatalogDigest{}, nil, errors.New(
					"file-identity catalog exact input cannot be opened",
				)
			}
			openedInfo, identity, identityErr := verifiedOpenedFileIdentity(opened, info, info.Mode().IsRegular())
			closeErr := opened.Close()
			if identityErr != nil || closeErr != nil {
				return fileIdentityCatalogDigest{}, nil, errors.New(
					"file-identity catalog exact input changed while opening",
				)
			}
			if recordErr := recorder.record('e', path, openedInfo, identity); recordErr != nil {
				return fileIdentityCatalogDigest{}, nil, recordErr
			}
		}
	}
	for _, root := range trees {
		rootInfo, err := os.Lstat(root)
		switch {
		case os.IsNotExist(err):
			if budgetErr := budget.consume(root); budgetErr != nil {
				return fileIdentityCatalogDigest{}, nil, budgetErr
			}
			if recordErr := recorder.recordMissing('T', root); recordErr != nil {
				return fileIdentityCatalogDigest{}, nil, recordErr
			}
			continue
		case err != nil:
			return fileIdentityCatalogDigest{}, nil, errors.New("file-identity catalog tree cannot be inspected")
		case rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir():
			return fileIdentityCatalogDigest{}, nil, errors.New("file-identity catalog tree is unsafe")
		}
		pinned, openErr := os.OpenRoot(root)
		if openErr != nil {
			return fileIdentityCatalogDigest{}, nil, errors.New("file-identity catalog tree cannot be opened")
		}
		openedInfo, openedErr := pinned.Stat(".")
		if openedErr != nil || !stableFileIdentityCatalogDirectory(rootInfo, openedInfo) {
			_ = pinned.Close()
			return fileIdentityCatalogDigest{}, nil, errors.New("file-identity catalog tree changed while opening")
		}
		walkErr := collectPinnedFileIdentityCatalogTree(
			pinned,
			root,
			".",
			exclusions,
			1,
			&budget,
			recorder,
		)
		closeErr := pinned.Close()
		if walkErr != nil || closeErr != nil {
			return fileIdentityCatalogDigest{}, nil, errors.New("file-identity catalog tree cannot be enumerated")
		}
	}
	return recorder.finish(), recorder.identities, nil
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
	exclusions fileIdentityCatalogExclusions,
	depth int,
	budget *fileIdentityCatalogBudget,
	recorder *fileIdentityCatalogRecorder,
) error {
	if root == nil || recorder == nil || budget == nil || depth < 1 ||
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
	directory, err := root.Open(".")
	if err != nil {
		return errors.New("pinned identity directory cannot be opened")
	}
	opened, directoryIdentity, openedErr := verifiedOpenedFileIdentity(directory, before, false)
	if openedErr != nil || !stableFileIdentityCatalogDirectory(before, opened) {
		_ = directory.Close()
		return errors.New("pinned identity directory changed while opening")
	}
	if recordErr := recorder.record('d', lexical, opened, directoryIdentity); recordErr != nil {
		_ = directory.Close()
		return recordErr
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
				if fileIdentityCatalogPathExcluded(path, exclusions) {
					continue
				}
				openedFile, openFileErr := root.Open(name)
				if openFileErr != nil {
					return closeDirectory(errors.New("pinned identity file cannot be opened"))
				}
				openedFileInfo, identity, identityErr := verifiedOpenedFileIdentity(openedFile, info, true)
				fileCloseErr := openedFile.Close()
				if identityErr != nil || fileCloseErr != nil {
					return closeDirectory(errors.New("pinned identity file changed while opening"))
				}
				if fileIdentityCatalogIdentityExcluded(identity, exclusions) {
					continue
				}
				if recordErr := recorder.record('f', path, openedFileInfo, identity); recordErr != nil {
					return closeDirectory(recordErr)
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
				exclusions,
				depth+1,
				budget,
				recorder,
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

func fileIdentityCatalogPathExcluded(
	path string,
	exclusions fileIdentityCatalogExclusions,
) bool {
	_, found := exclusions.paths[fileMutationDistinctPathKey(path)]
	return found
}

func fileIdentityCatalogIdentityExcluded(
	identity string,
	exclusions fileIdentityCatalogExclusions,
) bool {
	_, found := exclusions.identities[identity]
	return found
}

func verifiedOpenedFileIdentity(
	file *os.File,
	expected os.FileInfo,
	requireRegular bool,
) (os.FileInfo, string, error) {
	if file == nil || expected == nil {
		return nil, "", errors.New("opened file identity is invalid")
	}
	actual, err := file.Stat()
	if err != nil || actual == nil || actual.Mode() != expected.Mode() ||
		!os.SameFile(expected, actual) || requireRegular && !actual.Mode().IsRegular() ||
		!requireRegular && !actual.IsDir() && !actual.Mode().IsRegular() {
		return nil, "", errors.New("opened file identity does not match preflight")
	}
	identity, err := fileIdentityFromOpenedHandle(file, actual)
	if err != nil || identity == "" {
		return nil, "", errors.New("opened file identity is unavailable")
	}
	return actual, identity, nil
}

func snapshotFileIdentity(path string, expected os.FileInfo) (string, error) {
	if path == "" || expected == nil || !expected.Mode().IsRegular() {
		return "", errors.New("file identity is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	_, identity, identityErr := verifiedOpenedFileIdentity(file, expected, true)
	closeErr := file.Close()
	if identityErr != nil {
		return "", identityErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return identity, nil
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

// ProtectsOpenedFile reports whether file is a captured regular-file identity.
// expected must be the caller's preflight FileInfo for the same open handle;
// lookup fails closed if the handle does not still match that preflight.
func (catalog *FileIdentityCatalog) ProtectsOpenedFile(
	file *os.File,
	expected os.FileInfo,
) (bool, error) {
	if catalog == nil {
		return false, nil
	}
	if file == nil || expected == nil || !expected.Mode().IsRegular() {
		return false, errors.New("opened file-identity lookup is invalid")
	}
	_, identity, err := verifiedOpenedFileIdentity(file, expected, true)
	if err != nil {
		return false, errors.New("opened file-identity lookup failed")
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
