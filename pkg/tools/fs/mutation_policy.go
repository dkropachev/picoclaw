package fstools

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// FileMutationPolicy denies writes to each protected root and all of its
// descendants. Roots are detached and resolved when the tool is constructed;
// existing filesystem aliases are revalidated before every write.
type FileMutationPolicy struct {
	ProtectedRoots           []string
	ProtectedSiblingPrefixes []FileMutationSiblingPrefix
	ProtectedIdentities      *FileIdentityCatalog
	// Prepared is an immutable root/identity policy resolved once for a whole
	// runtime generation. It is mutually exclusive with the source fields.
	Prepared *PreparedFileMutationPolicy
}

// FileMutationSiblingPrefix protects every child in one exact absolute parent
// whose basename begins with Prefix. It covers legacy sibling sidecars whose
// bounded suffix is not known until another process creates the file.
type FileMutationSiblingPrefix struct {
	Parent string
	Prefix string
}

type fileMutationProtectedRoot struct {
	lexical   string
	canonical string
}

type fileMutationProtectedSiblingPrefix struct {
	lexicalParent   string
	canonicalParent string
	prefix          string
}

// PreparedFileMutationPolicy is an immutable, generation-shareable policy.
// Its resolved roots are never exposed or copied into individual tools.
type PreparedFileMutationPolicy struct {
	roots           []fileMutationProtectedRoot
	siblingPrefixes []fileMutationProtectedSiblingPrefix
	identities      *FileIdentityCatalog
}

// NewPreparedFileMutationPolicy detaches and resolves one source policy for
// reuse by every root tool and owner-factory product in a runtime generation.
func NewPreparedFileMutationPolicy(
	workspace string,
	policy FileMutationPolicy,
) (*PreparedFileMutationPolicy, error) {
	if policy.Prepared != nil {
		return nil, errors.New("file-mutation policy is already prepared")
	}
	roots, err := prepareFileMutationProtectedRoots(workspace, policy.ProtectedRoots)
	if err != nil {
		return nil, err
	}
	prefixes, err := prepareFileMutationProtectedSiblingPrefixes(
		policy.ProtectedSiblingPrefixes,
	)
	if err != nil {
		return nil, err
	}
	return &PreparedFileMutationPolicy{
		roots:           roots,
		siblingPrefixes: prefixes,
		identities:      policy.ProtectedIdentities,
	}, nil
}

// ProtectsPath reports whether one absolute path is covered by this immutable
// policy. It revalidates current root namespaces and physical file aliases.
func (policy *PreparedFileMutationPolicy) ProtectsPath(path string) (bool, error) {
	if policy == nil || path == "" || !filepath.IsAbs(path) {
		return false, errors.New("prepared file-mutation path is invalid")
	}
	candidate, err := fileMutationAbs(filepath.Clean(path))
	if err != nil {
		return false, errors.New("prepared file-mutation path is invalid")
	}
	canonicalCandidate, err := resolvePathAgainstExistingAncestor(candidate)
	if err != nil {
		return false, errors.New("prepared file-mutation path cannot be resolved")
	}
	candidateInfo, statErr := os.Stat(canonicalCandidate)
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, errors.New("prepared file-mutation path cannot be inspected")
	}
	if os.IsNotExist(statErr) {
		candidateInfo = nil
	}
	if policy.identities != nil {
		if candidateInfo != nil && candidateInfo.Mode().IsRegular() {
			protected, identityErr := policy.identities.ProtectsPath(
				canonicalCandidate,
				candidateInfo,
			)
			if identityErr != nil || protected {
				return protected, identityErr
			}
		}
	}
	for _, prefix := range policy.siblingPrefixes {
		protected, prefixErr := fileMutationProtectedBySiblingPrefix(
			candidate,
			canonicalCandidate,
			prefix,
		)
		if prefixErr != nil || protected {
			return protected, prefixErr
		}
	}
	for _, root := range policy.roots {
		currentCanonical, resolveErr := resolvePathAgainstExistingAncestor(root.lexical)
		if resolveErr != nil ||
			fileMutationPathKey(currentCanonical) != fileMutationPathKey(root.canonical) {
			return false, errors.New("prepared file-mutation root changed")
		}
		if fileMutationPathWithin(candidate, root.lexical) ||
			fileMutationPathWithin(candidate, root.canonical) ||
			fileMutationPathWithin(canonicalCandidate, root.lexical) ||
			fileMutationPathWithin(canonicalCandidate, currentCanonical) {
			return true, nil
		}
		sameFile, sameFileErr := fileMutationSameInfoExistingFile(
			candidateInfo,
			currentCanonical,
		)
		if sameFileErr != nil {
			return false, sameFileErr
		}
		if sameFile {
			return true, nil
		}
	}
	return false, nil
}

// OverlapsPath reports whether an absolute path contains, equals, or lies
// beneath a protected root. It is used to reject managed checkout/runtime-root
// overlap before any local-repair file capability is exposed.
func (policy *PreparedFileMutationPolicy) OverlapsPath(path string) (bool, error) {
	if policy == nil || path == "" || !filepath.IsAbs(path) {
		return false, errors.New("prepared file-mutation path is invalid")
	}
	candidate, err := fileMutationAbs(filepath.Clean(path))
	if err != nil {
		return false, errors.New("prepared file-mutation path is invalid")
	}
	canonicalCandidate, err := resolvePathAgainstExistingAncestor(candidate)
	if err != nil {
		return false, errors.New("prepared file-mutation path cannot be resolved")
	}
	for _, prefix := range policy.siblingPrefixes {
		currentParent, resolveErr := resolvePathAgainstExistingAncestor(prefix.lexicalParent)
		if resolveErr != nil ||
			fileMutationPathKey(currentParent) != fileMutationPathKey(prefix.canonicalParent) {
			return false, errors.New("prepared file-mutation sibling parent changed")
		}
		if fileMutationPathWithin(prefix.lexicalParent, candidate) ||
			fileMutationPathWithin(currentParent, canonicalCandidate) ||
			fileMutationSiblingPrefixMatches(candidate, prefix.lexicalParent, prefix.prefix) ||
			fileMutationSiblingPrefixMatches(canonicalCandidate, currentParent, prefix.prefix) {
			return true, nil
		}
	}
	for _, root := range policy.roots {
		currentCanonical, resolveErr := resolvePathAgainstExistingAncestor(root.lexical)
		if resolveErr != nil ||
			fileMutationPathKey(currentCanonical) != fileMutationPathKey(root.canonical) {
			return false, errors.New("prepared file-mutation root changed")
		}
		if fileMutationPathWithin(candidate, root.lexical) ||
			fileMutationPathWithin(root.lexical, candidate) ||
			fileMutationPathWithin(canonicalCandidate, currentCanonical) ||
			fileMutationPathWithin(currentCanonical, canonicalCandidate) {
			return true, nil
		}
	}
	return false, nil
}

// ProtectsOpenedFile binds prepared root/identity checks to an already-opened
// regular file, closing the path-to-open race for local-repair readers.
func (policy *PreparedFileMutationPolicy) ProtectsOpenedFile(
	file *os.File,
	expected os.FileInfo,
) (bool, error) {
	return policy.ProtectsOpenedPath("", "", file, expected)
}

// ProtectsOpenedPath checks one coherent path snapshot and its opened handle.
// candidate and canonical must be the lexical/canonical names captured before
// a caller's final fstat; empty names retain handle-only compatibility.
func (policy *PreparedFileMutationPolicy) ProtectsOpenedPath(
	candidate string,
	canonical string,
	file *os.File,
	expected os.FileInfo,
) (bool, error) {
	if policy == nil || file == nil || expected == nil {
		return false, errors.New("prepared file-mutation opened file is invalid")
	}
	if candidate != "" || canonical != "" {
		if candidate == "" || canonical == "" || !filepath.IsAbs(candidate) ||
			!filepath.IsAbs(canonical) {
			return false, errors.New("prepared file-mutation opened path is invalid")
		}
		candidate = filepath.Clean(candidate)
		canonical = filepath.Clean(canonical)
	}
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || actual.Mode() != expected.Mode() ||
		!os.SameFile(actual, expected) {
		return false, errors.New("prepared file-mutation opened file changed")
	}
	if policy.identities != nil {
		protected, identityErr := policy.identities.ProtectsOpenedFile(file, expected)
		if identityErr != nil || protected {
			return protected, identityErr
		}
	}
	if candidate != "" {
		for _, prefix := range policy.siblingPrefixes {
			protected, prefixErr := fileMutationProtectedBySiblingPrefix(
				candidate,
				canonical,
				prefix,
			)
			if prefixErr != nil || protected {
				return protected, prefixErr
			}
		}
	}
	for _, root := range policy.roots {
		currentCanonical, resolveErr := resolvePathAgainstExistingAncestor(root.lexical)
		if resolveErr != nil ||
			fileMutationPathKey(currentCanonical) != fileMutationPathKey(root.canonical) {
			return false, errors.New("prepared file-mutation root changed")
		}
		if candidate != "" && (fileMutationPathWithin(candidate, root.lexical) ||
			fileMutationPathWithin(candidate, root.canonical) ||
			fileMutationPathWithin(canonical, root.lexical) ||
			fileMutationPathWithin(canonical, currentCanonical)) {
			return true, nil
		}
		rootInfo, statErr := os.Stat(currentCanonical)
		switch {
		case statErr != nil && !os.IsNotExist(statErr):
			return false, statErr
		case statErr == nil && os.SameFile(actual, rootInfo):
			return true, nil
		}
	}
	return false, nil
}

// protectedMutationFS is deliberately below write_file, edit_file, and
// append_file. This keeps every read-before-write and mutation path on one
// policy boundary and binds reads/listings to their actual opened handles.
type protectedMutationFS struct {
	delegate        fileSystem
	workspace       string
	restrict        bool
	patterns        []*regexp.Regexp
	roots           []fileMutationProtectedRoot
	siblingPrefixes []fileMutationProtectedSiblingPrefix
	identities      *FileIdentityCatalog

	// Package-test seam executed after the destination parent is pinned and
	// before namespace/root revalidation.
	beforePinnedWrite func()
	// Package-test seam executed after the namespace preflight and before the
	// delegate opens a read handle. Production code never sets it.
	beforeProtectedOpen func(string)
}

var (
	fileMutationAbs              = filepath.Abs
	fileMutationEvalSymlinks     = filepath.EvalSymlinks
	fileMutationOpenRoot         = os.OpenRoot
	fileMutationMkdirAll         = os.MkdirAll
	fileMutationSafeRelativePath = getSafeRelPath
	fileMutationValidatePlatform = validateFileMutationPlatformPath
)

func buildMutationFS(
	workspace string,
	restrict bool,
	patterns []*regexp.Regexp,
	policy FileMutationPolicy,
) (fileSystem, error) {
	delegate := buildFs(workspace, restrict, patterns)
	var roots []fileMutationProtectedRoot
	var siblingPrefixes []fileMutationProtectedSiblingPrefix
	identities := policy.ProtectedIdentities
	if policy.Prepared != nil {
		if len(policy.ProtectedRoots) != 0 || len(policy.ProtectedSiblingPrefixes) != 0 ||
			policy.ProtectedIdentities != nil {
			return nil, errors.New("prepared file-mutation policy has source fields")
		}
		roots = policy.Prepared.roots
		siblingPrefixes = policy.Prepared.siblingPrefixes
		identities = policy.Prepared.identities
	} else {
		var err error
		roots, err = prepareFileMutationProtectedRoots(workspace, policy.ProtectedRoots)
		if err != nil {
			return nil, err
		}
		siblingPrefixes, err = prepareFileMutationProtectedSiblingPrefixes(
			policy.ProtectedSiblingPrefixes,
		)
		if err != nil {
			return nil, err
		}
	}
	if len(roots) == 0 && len(siblingPrefixes) == 0 && identities == nil {
		return delegate, nil
	}
	return &protectedMutationFS{
		delegate:        delegate,
		workspace:       workspace,
		restrict:        restrict,
		patterns:        append([]*regexp.Regexp(nil), patterns...),
		roots:           roots,
		siblingPrefixes: siblingPrefixes,
		identities:      identities,
	}, nil
}

func prepareFileMutationProtectedSiblingPrefixes(
	configured []FileMutationSiblingPrefix,
) ([]fileMutationProtectedSiblingPrefix, error) {
	prepared := make([]fileMutationProtectedSiblingPrefix, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for index, configuredPrefix := range append([]FileMutationSiblingPrefix(nil), configured...) {
		parent := configuredPrefix.Parent
		prefix := configuredPrefix.Prefix
		if parent == "" || parent != strings.TrimSpace(parent) || !filepath.IsAbs(parent) ||
			filepath.Clean(parent) != parent || !utf8.ValidString(parent) ||
			strings.ContainsRune(parent, '\x00') || prefix == "" ||
			prefix != strings.TrimSpace(prefix) || !utf8.ValidString(prefix) ||
			strings.ContainsRune(prefix, '\x00') || filepath.Base(prefix) != prefix ||
			prefix == "." || prefix == ".." {
			return nil, fmt.Errorf("file-mutation protected sibling prefix %d is invalid", index)
		}
		if platformErr := fileMutationValidatePlatform(filepath.Join(parent, prefix)); platformErr != nil {
			return nil, fmt.Errorf("file-mutation protected sibling prefix %d is invalid", index)
		}
		canonicalParent, err := resolvePathAgainstExistingAncestor(parent)
		if err != nil {
			return nil, fmt.Errorf(
				"file-mutation protected sibling prefix %d cannot be resolved",
				index,
			)
		}
		identity := fileMutationDistinctPathKey(parent) + "\x00" +
			fileMutationDistinctPathKey(canonicalParent) + "\x00" +
			fileMutationPathKey(filepath.Join(parent, prefix))
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		seen[identity] = struct{}{}
		prepared = append(prepared, fileMutationProtectedSiblingPrefix{
			lexicalParent: parent, canonicalParent: canonicalParent, prefix: prefix,
		})
	}
	return prepared, nil
}

func fileMutationSiblingPrefixMatches(
	candidate string,
	parent string,
	prefix string,
) bool {
	if candidate == "" || parent == "" || prefix == "" ||
		fileMutationPathKey(filepath.Dir(candidate)) != fileMutationPathKey(parent) {
		return false
	}
	candidateKey := fileMutationPathKey(filepath.Join(parent, filepath.Base(candidate)))
	prefixKey := fileMutationPathKey(filepath.Join(parent, prefix))
	return strings.HasPrefix(candidateKey, prefixKey)
}

func fileMutationProtectedBySiblingPrefix(
	candidate string,
	canonical string,
	prefix fileMutationProtectedSiblingPrefix,
) (bool, error) {
	currentParent, err := resolvePathAgainstExistingAncestor(prefix.lexicalParent)
	if err != nil || fileMutationPathKey(currentParent) != fileMutationPathKey(prefix.canonicalParent) {
		return false, errors.New("file-mutation protected sibling parent changed")
	}
	return fileMutationSiblingPrefixMatches(candidate, prefix.lexicalParent, prefix.prefix) ||
		fileMutationSiblingPrefixMatches(candidate, prefix.canonicalParent, prefix.prefix) ||
		fileMutationSiblingPrefixMatches(canonical, prefix.lexicalParent, prefix.prefix) ||
		fileMutationSiblingPrefixMatches(canonical, currentParent, prefix.prefix), nil
}

func prepareFileMutationProtectedRoots(
	workspace string,
	configured []string,
) ([]fileMutationProtectedRoot, error) {
	if len(configured) == 0 {
		return nil, nil
	}

	workspacePath := workspace
	if workspacePath == "" {
		workspacePath = "."
	}
	workspaceAbs, err := fileMutationAbs(filepath.Clean(workspacePath))
	if err != nil {
		return nil, fmt.Errorf("prepare file-mutation policy: invalid workspace")
	}

	roots := make([]fileMutationProtectedRoot, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for index, configuredRoot := range append([]string(nil), configured...) {
		if configuredRoot == "" || configuredRoot != strings.TrimSpace(configuredRoot) ||
			!utf8.ValidString(configuredRoot) || strings.ContainsRune(configuredRoot, '\x00') {
			return nil, fmt.Errorf("file-mutation protected root %d is invalid", index)
		}

		lexical := filepath.Clean(configuredRoot)
		if !filepath.IsAbs(lexical) {
			lexical = filepath.Join(workspaceAbs, lexical)
		}
		lexical, err = fileMutationAbs(lexical)
		if err != nil {
			return nil, fmt.Errorf("file-mutation protected root %d is invalid", index)
		}
		if platformErr := fileMutationValidatePlatform(lexical); platformErr != nil {
			return nil, fmt.Errorf("file-mutation protected root %d is invalid", index)
		}
		canonical, resolveErr := resolvePathAgainstExistingAncestor(lexical)
		if resolveErr != nil {
			return nil, fmt.Errorf("file-mutation protected root %d cannot be resolved", index)
		}

		identity := fileMutationDistinctPathKey(lexical) + "\x00" +
			fileMutationDistinctPathKey(canonical)
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		roots = append(roots, fileMutationProtectedRoot{
			lexical:   lexical,
			canonical: canonical,
		})
	}
	return roots, nil
}

func (p *protectedMutationFS) ReadFile(path string) ([]byte, error) {
	file, err := p.openValidated(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	return content, nil
}

func (p *protectedMutationFS) ReadDir(path string) ([]os.DirEntry, error) {
	file, err := p.openValidated(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return nil, errors.New("failed to read directory: opened handle cannot enumerate")
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})
	return entries, nil
}

func (p *protectedMutationFS) Open(path string) (fs.File, error) {
	return p.openValidated(path)
}

type fileMutationAccessSnapshot struct {
	canonical string
	info      os.FileInfo
}

// openValidated binds authorization to the actual opened object. Checking a
// pathname and then asking the delegate to open it leaves a rename window in
// which a protected hardlink can replace an ordinary file. No bytes or names
// are consumed until fstat proves the handle is the preflight object and the
// immutable catalog accepts the handle identity.
func (p *protectedMutationFS) openValidated(path string) (fs.File, error) {
	snapshot, err := p.validateAccessSnapshot(path)
	if err != nil {
		return nil, err
	}
	if p.beforeProtectedOpen != nil {
		p.beforeProtectedOpen(path)
	}
	opened, err := p.delegate.Open(path)
	if err != nil {
		return nil, err
	}
	if err = p.validateOpenedAccess(path, snapshot, opened); err != nil {
		_ = opened.Close()
		return nil, err
	}
	return opened, nil
}

func (p *protectedMutationFS) validateOpenedAccess(
	path string,
	before fileMutationAccessSnapshot,
	opened fs.File,
) error {
	if before.info == nil || opened == nil {
		return fileMutationPolicyDenied()
	}
	actual, err := opened.Stat()
	if err != nil || actual == nil || actual.Mode() != before.info.Mode() ||
		!os.SameFile(before.info, actual) {
		return fileMutationPolicyDenied()
	}
	// Re-resolve the namespace after open. A handle to the old ordinary inode is
	// not sufficient authority when the requested name now denotes runtime
	// state; callers must retry against one coherent namespace generation.
	after, err := p.validateAccessSnapshot(path)
	if err != nil || after.info == nil ||
		fileMutationPathKey(after.canonical) != fileMutationPathKey(before.canonical) ||
		after.info.Mode() != actual.Mode() || !os.SameFile(after.info, actual) {
		return fileMutationPolicyDenied()
	}
	if p.identities != nil && actual.Mode().IsRegular() {
		file, ok := opened.(*os.File)
		if !ok {
			return fileMutationPolicyDenied()
		}
		protected, identityErr := p.identities.ProtectsOpenedFile(file, before.info)
		if identityErr != nil || protected {
			return fileMutationPolicyDenied()
		}
	}
	return nil
}

func (p *protectedMutationFS) WriteFile(path string, data []byte) error {
	if err := p.validateAccess(path); err != nil {
		return err
	}
	candidate, err := fileMutationAbsolutePath(p.workspace, p.restrict, path)
	if err != nil {
		return fileMutationPolicyDenied()
	}
	if err = p.prepareMutationParent(candidate); err != nil {
		return err
	}
	parent := filepath.Dir(candidate)
	canonicalParent, err := fileMutationEvalSymlinks(parent)
	if err != nil {
		return fileMutationPolicyDenied()
	}
	pinnedParent, err := fileMutationOpenRoot(canonicalParent)
	if err != nil {
		return fmt.Errorf("failed to open mutation parent: %w", err)
	}
	defer pinnedParent.Close()

	if p.beforePinnedWrite != nil {
		p.beforePinnedWrite()
	}
	if err = p.validateMutationAuthority(candidate); err != nil {
		return err
	}
	if err = p.validateAccess(candidate); err != nil {
		return err
	}
	currentParent, err := fileMutationEvalSymlinks(parent)
	if err != nil || fileMutationPathKey(currentParent) != fileMutationPathKey(canonicalParent) {
		return fileMutationPolicyDenied()
	}
	return writeFileInPinnedRoot(pinnedParent, filepath.Base(candidate), data)
}

func (p *protectedMutationFS) prepareMutationParent(candidate string) error {
	if err := p.validateMutationAuthority(candidate); err != nil {
		return err
	}
	parent := filepath.Dir(candidate)
	if !p.restrict || isAllowedPath(candidate, p.patterns) {
		if err := fileMutationMkdirAll(parent, 0o755); err != nil {
			return fmt.Errorf("failed to create parent directories: %w", err)
		}
		return nil
	}

	workspacePath, err := fileMutationAbs(filepath.Clean(p.workspace))
	if err != nil {
		return fmt.Errorf("failed to resolve workspace path: %w", err)
	}
	root, err := fileMutationOpenRoot(workspacePath)
	if err != nil {
		return fmt.Errorf("failed to open workspace: %w", err)
	}
	defer root.Close()
	relativeParent, err := fileMutationSafeRelativePath(workspacePath, parent)
	if err != nil {
		return err
	}
	if err = root.MkdirAll(normalizeRootRelPath(relativeParent), 0o755); err != nil {
		return fmt.Errorf("failed to create parent directories: %w", err)
	}
	return nil
}

func (p *protectedMutationFS) validateMutationAuthority(candidate string) error {
	if !p.restrict {
		return nil
	}
	_, err := validatePathWithAllowPaths(
		candidate,
		p.workspace,
		true,
		p.patterns,
	)
	return err
}

func writeFileInPinnedRoot(root *os.Root, name string, data []byte) error {
	return writeFileInPinnedRootWithOpen(
		root,
		name,
		data,
		func(name string, flag int, permission os.FileMode) (fileMutationTemporaryFile, error) {
			file, err := root.OpenFile(name, flag, permission)
			if err != nil {
				return nil, err
			}
			return file, nil
		},
	)
}

type fileMutationTemporaryFile interface {
	Write(data []byte) (count int, err error)
	Sync() error
	Close() error
}

func writeFileInPinnedRootWithOpen(
	root *os.Root,
	name string,
	data []byte,
	openFile func(string, int, os.FileMode) (fileMutationTemporaryFile, error),
) error {
	if root == nil || name == "" || name == "." || name != filepath.Base(name) {
		return fileMutationPolicyDenied()
	}
	temporary := fmt.Sprintf(".tmp-%d-%d", os.Getpid(), time.Now().UnixNano())
	temporaryFile, err := openFile(
		temporary,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("failed to open temp file: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = root.Remove(temporary)
		}
	}()
	if _, err = temporaryFile.Write(data); err != nil {
		_ = temporaryFile.Close()
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err = temporaryFile.Sync(); err != nil {
		_ = temporaryFile.Close()
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err = temporaryFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err = root.Rename(temporary, name); err != nil {
		return fmt.Errorf("failed to rename temp file over target: %w", err)
	}
	removeTemporary = false
	if directory, openErr := root.Open("."); openErr == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func (p *protectedMutationFS) validateAccess(path string) error {
	_, err := p.validateAccessSnapshot(path)
	return err
}

func (p *protectedMutationFS) validateAccessSnapshot(
	path string,
) (fileMutationAccessSnapshot, error) {
	candidate, err := fileMutationAbsolutePath(p.workspace, p.restrict, path)
	if err != nil {
		return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
	}
	if platformErr := fileMutationValidatePlatform(candidate); platformErr != nil {
		return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
	}
	canonicalCandidate, err := resolvePathAgainstExistingAncestor(candidate)
	if err != nil {
		return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
	}
	var candidateInfo os.FileInfo
	if p.identities != nil {
		var statErr error
		candidateInfo, statErr = os.Stat(canonicalCandidate)
		if statErr != nil && !os.IsNotExist(statErr) {
			return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
		}
		if statErr == nil && candidateInfo.Mode().IsRegular() {
			protected, identityErr := p.identities.ProtectsPath(canonicalCandidate, candidateInfo)
			if identityErr != nil || protected {
				return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
			}
		}
	} else {
		candidateInfo, err = os.Stat(canonicalCandidate)
		if err != nil && !os.IsNotExist(err) {
			return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
		}
		if os.IsNotExist(err) {
			candidateInfo = nil
		}
	}

	for _, root := range p.roots {
		currentCanonical, resolveErr := resolvePathAgainstExistingAncestor(root.lexical)
		if resolveErr != nil ||
			fileMutationPathKey(currentCanonical) != fileMutationPathKey(root.canonical) {
			return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
		}
		if fileMutationPathWithin(candidate, root.lexical) ||
			fileMutationPathWithin(candidate, root.canonical) ||
			fileMutationPathWithin(canonicalCandidate, root.lexical) ||
			fileMutationPathWithin(canonicalCandidate, currentCanonical) {
			return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
		}
		sameFile, sameFileErr := fileMutationSameInfoExistingFile(
			candidateInfo,
			currentCanonical,
		)
		if sameFileErr != nil || sameFile {
			return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
		}
	}
	for _, prefix := range p.siblingPrefixes {
		protected, prefixErr := fileMutationProtectedBySiblingPrefix(
			candidate,
			canonicalCandidate,
			prefix,
		)
		if prefixErr != nil || protected {
			return fileMutationAccessSnapshot{}, fileMutationPolicyDenied()
		}
	}
	return fileMutationAccessSnapshot{
		canonical: canonicalCandidate, info: candidateInfo,
	}, nil
}

func fileMutationSameExistingFile(left, right string) (bool, error) {
	leftInfo, leftErr := os.Stat(left)
	if leftErr != nil {
		if os.IsNotExist(leftErr) {
			return false, nil
		}
		return false, leftErr
	}
	rightInfo, rightErr := os.Stat(right)
	if rightErr != nil {
		if os.IsNotExist(rightErr) {
			return false, nil
		}
		return false, rightErr
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func fileMutationSameInfoExistingFile(leftInfo os.FileInfo, right string) (bool, error) {
	if leftInfo == nil {
		return false, nil
	}
	rightInfo, rightErr := os.Stat(right)
	if rightErr != nil {
		if os.IsNotExist(rightErr) {
			return false, nil
		}
		return false, rightErr
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func fileMutationAbsolutePath(workspace string, restrict bool, path string) (string, error) {
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) {
		return fileMutationAbs(cleaned)
	}
	if restrict {
		workspacePath := workspace
		if workspacePath == "" {
			workspacePath = "."
		}
		return fileMutationAbs(filepath.Join(workspacePath, cleaned))
	}
	return fileMutationAbs(cleaned)
}

func fileMutationPathWithin(candidate, root string) bool {
	candidate = fileMutationPathKey(candidate)
	root = fileMutationPathKey(root)
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || filepath.IsLocal(relative))
}

func fileMutationPolicyDenied() error {
	return fmt.Errorf("access denied: path is protected runtime state")
}
