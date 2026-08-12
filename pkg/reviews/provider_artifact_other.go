//go:build !linux && !windows && !js && !plan9

package reviews

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// acquireProviderArtifact uses os.Root to keep every operation beneath the
// configured artifact root and pins the exact parent directory and artifact
// identities before returning. Unlike the Linux implementation, the portable
// os.Root API cannot atomically unlink a path only when it still names a given
// file. Cleanup therefore first moves the artifact to an unpredictable name in
// its private, pinned parent and verifies its identity there before removal.
func acquireProviderArtifact(
	rootPath string,
	artifactPath string,
	cleanupHook func(string),
) (*providerArtifact, error) {
	rootPath, artifactPath, relative, err := portableProviderArtifactPaths(
		rootPath,
		artifactPath,
	)
	if err != nil {
		return nil, err
	}

	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		return nil, fmt.Errorf("inspect provider artifact root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("provider artifact root is not a directory")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open provider artifact root safely: %w", err)
	}
	openedRootInfo, err := root.Stat(".")
	if err != nil || !openedRootInfo.IsDir() || !os.SameFile(rootInfo, openedRootInfo) {
		_ = root.Close()
		return nil, errors.Join(
			errors.New("provider artifact root changed before opening"),
			err,
		)
	}

	parentRelative, base := filepath.Split(relative)
	parentRelative = filepath.Clean(parentRelative)
	if base == "" || base == "." || base == ".." ||
		strings.ContainsRune(base, filepath.Separator) {
		_ = root.Close()
		return nil, errors.New("provider artifact path is invalid")
	}
	parentInfo, err := inspectPortableProviderArtifactParent(
		root,
		openedRootInfo,
		parentRelative,
	)
	if err != nil {
		_ = root.Close()
		return nil, err
	}

	var parent *os.Root
	if parentRelative == "." {
		parent = root
		root = nil
	} else {
		parent, err = root.OpenRoot(parentRelative)
		_ = root.Close()
		root = nil
		if err != nil {
			return nil, fmt.Errorf("pin provider artifact directory: %w", err)
		}
	}
	openedParentInfo, err := parent.Stat(".")
	if err != nil || !openedParentInfo.IsDir() || !os.SameFile(parentInfo, openedParentInfo) {
		_ = parent.Close()
		return nil, errors.Join(
			errors.New("provider artifact directory changed before opening"),
			err,
		)
	}
	if err := validatePortableProviderArtifactDirectory(
		rootPath,
		parentRelative,
		openedParentInfo,
	); err != nil {
		_ = parent.Close()
		return nil, err
	}

	identity, err := parent.Lstat(base)
	if err != nil {
		_ = parent.Close()
		return nil, fmt.Errorf("inspect provider artifact safely: %w", err)
	}
	if identity.Mode()&os.ModeSymlink != 0 || !identity.Mode().IsRegular() {
		_ = parent.Close()
		return nil, errors.New("provider artifact is not regular")
	}

	artifact := &providerArtifact{
		Size: identity.Size(),
		consume: func() error {
			return consumePortableProviderArtifact(
				parent,
				base,
				artifactPath,
				identity,
				cleanupHook,
			)
		},
	}
	file, openErr := parent.Open(base)
	if openErr != nil {
		return artifact, fmt.Errorf("open provider artifact for reading: %w", openErr)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(identity, opened) {
		closeErr := file.Close()
		return artifact, errors.Join(
			errors.New("provider artifact changed before reading"),
			statErr,
			closeErr,
		)
	}
	artifact.File = file
	artifact.Size = opened.Size()
	return artifact, nil
}

func portableProviderArtifactPaths(
	rootPath string,
	artifactPath string,
) (string, string, string, error) {
	root, err := filepath.Abs(strings.TrimSpace(rootPath))
	if err != nil {
		return "", "", "", err
	}
	path, err := filepath.Abs(artifactPath)
	if err != nil {
		return "", "", "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." ||
		filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", "", errors.New("provider artifact outside root")
	}
	return root, path, relative, nil
}

func inspectPortableProviderArtifactParent(
	root *os.Root,
	rootInfo os.FileInfo,
	relative string,
) (os.FileInfo, error) {
	if relative == "." || relative == "" {
		return rootInfo, nil
	}
	current := ""
	var info os.FileInfo
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return nil, errors.New("provider artifact directory is invalid")
		}
		current = filepath.Join(current, component)
		var err error
		info, err = root.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("inspect provider artifact directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("provider artifact directory contains a non-directory or symbolic link")
		}
	}
	return info, nil
}

func consumePortableProviderArtifact(
	parent *os.Root,
	base string,
	path string,
	expected os.FileInfo,
	cleanupHook func(string),
) (returnErr error) {
	defer func() {
		returnErr = errors.Join(returnErr, parent.Close())
	}()
	if cleanupHook != nil {
		cleanupHook(path)
	}
	current, err := parent.Lstat(base)
	if err != nil || current.Mode()&os.ModeSymlink != 0 ||
		!current.Mode().IsRegular() || !os.SameFile(expected, current) {
		return errors.Join(
			errors.New("provider artifact changed before cleanup"),
			err,
		)
	}

	quarantine, err := unusedPortableProviderArtifactQuarantine(parent)
	if err != nil {
		return err
	}
	if err := parent.Rename(base, quarantine); err != nil {
		return fmt.Errorf("quarantine consumed provider artifact: %w", err)
	}
	quarantined, inspectErr := parent.Lstat(quarantine)
	if inspectErr != nil || quarantined.Mode()&os.ModeSymlink != 0 ||
		!quarantined.Mode().IsRegular() || !os.SameFile(expected, quarantined) {
		restoreErr := restorePortableProviderArtifact(parent, quarantine, base)
		return errors.Join(
			errors.New("provider artifact changed before cleanup"),
			inspectErr,
			restoreErr,
		)
	}
	if err := parent.Remove(quarantine); err != nil {
		return fmt.Errorf("remove consumed provider artifact: %w", err)
	}
	return nil
}

func unusedPortableProviderArtifactQuarantine(parent *os.Root) (string, error) {
	for range 8 {
		name, err := providerArtifactQuarantineName()
		if err != nil {
			return "", err
		}
		if _, err := parent.Lstat(name); errors.Is(err, os.ErrNotExist) {
			return name, nil
		} else if err != nil {
			return "", fmt.Errorf("inspect provider artifact quarantine name: %w", err)
		}
	}
	return "", errors.New("create unique provider artifact quarantine name")
}

func restorePortableProviderArtifact(
	parent *os.Root,
	quarantine string,
	base string,
) error {
	// Link is an atomic no-replace operation. It cannot overwrite a new raced
	// basename, and Root.Link links a symbolic link itself rather than following
	// it. If restoration fails, the displaced object remains quarantined rather
	// than being deleted.
	if err := parent.Link(quarantine, base); err != nil {
		return fmt.Errorf("restore raced provider artifact: %w", err)
	}
	if err := parent.Remove(quarantine); err != nil {
		return fmt.Errorf("remove restored provider artifact quarantine: %w", err)
	}
	return nil
}

func providerArtifactQuarantineName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create provider artifact quarantine name: %w", err)
	}
	return ".picoclaw-review-artifact-" + hex.EncodeToString(random[:]), nil
}
