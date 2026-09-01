package fstools

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// FileMutationPolicy denies writes to each protected root and all of its
// descendants. Roots are detached and resolved when the tool is constructed;
// existing filesystem aliases are revalidated before every write.
type FileMutationPolicy struct {
	ProtectedRoots      []string
	ProtectedIdentities *FileIdentityCatalog
}

type fileMutationProtectedRoot struct {
	lexical   string
	canonical string
}

// protectedMutationFS is deliberately below write_file, edit_file, and
// append_file. This keeps every mutation path on one policy boundary while
// leaving reads and directory listings unchanged.
type protectedMutationFS struct {
	delegate   fileSystem
	workspace  string
	restrict   bool
	patterns   []*regexp.Regexp
	roots      []fileMutationProtectedRoot
	identities *FileIdentityCatalog

	// Package-test seam executed after the destination parent is pinned and
	// before namespace/root revalidation.
	beforePinnedWrite func()
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
	roots, err := prepareFileMutationProtectedRoots(workspace, policy.ProtectedRoots)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 && policy.ProtectedIdentities == nil {
		return delegate, nil
	}
	return &protectedMutationFS{
		delegate:   delegate,
		workspace:  workspace,
		restrict:   restrict,
		patterns:   append([]*regexp.Regexp(nil), patterns...),
		roots:      roots,
		identities: policy.ProtectedIdentities,
	}, nil
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

		identity := fileMutationPathKey(lexical) + "\x00" + fileMutationPathKey(canonical)
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
	if err := p.validateAccess(path); err != nil {
		return nil, err
	}
	return p.delegate.ReadFile(path)
}

func (p *protectedMutationFS) ReadDir(path string) ([]os.DirEntry, error) {
	if err := p.validateAccess(path); err != nil {
		return nil, err
	}
	return p.delegate.ReadDir(path)
}

func (p *protectedMutationFS) Open(path string) (fs.File, error) {
	if err := p.validateAccess(path); err != nil {
		return nil, err
	}
	return p.delegate.Open(path)
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
	candidate, err := fileMutationAbsolutePath(p.workspace, p.restrict, path)
	if err != nil {
		return fileMutationPolicyDenied()
	}
	if platformErr := fileMutationValidatePlatform(candidate); platformErr != nil {
		return fileMutationPolicyDenied()
	}
	canonicalCandidate, err := resolvePathAgainstExistingAncestor(candidate)
	if err != nil {
		return fileMutationPolicyDenied()
	}
	if p.identities != nil {
		candidateInfo, statErr := os.Stat(canonicalCandidate)
		if statErr != nil && !os.IsNotExist(statErr) {
			return fileMutationPolicyDenied()
		}
		if statErr == nil && candidateInfo.Mode().IsRegular() {
			protected, identityErr := p.identities.ProtectsPath(canonicalCandidate, candidateInfo)
			if identityErr != nil || protected {
				return fileMutationPolicyDenied()
			}
		}
	}

	for _, root := range p.roots {
		currentCanonical, resolveErr := resolvePathAgainstExistingAncestor(root.lexical)
		if resolveErr != nil ||
			fileMutationPathKey(currentCanonical) != fileMutationPathKey(root.canonical) {
			return fileMutationPolicyDenied()
		}
		if fileMutationPathWithin(candidate, root.lexical) ||
			fileMutationPathWithin(candidate, root.canonical) ||
			fileMutationPathWithin(canonicalCandidate, root.lexical) ||
			fileMutationPathWithin(canonicalCandidate, currentCanonical) {
			return fileMutationPolicyDenied()
		}
		sameFile, sameFileErr := fileMutationSameExistingFile(
			canonicalCandidate,
			currentCanonical,
		)
		if sameFileErr != nil || sameFile {
			return fileMutationPolicyDenied()
		}
	}
	return nil
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
