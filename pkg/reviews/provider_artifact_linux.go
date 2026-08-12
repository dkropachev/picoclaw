//go:build linux

package reviews

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// acquireProviderArtifact pins the root, parent directory, and exact artifact
// inode before opening it for reading. It can therefore return cleanup
// authority even when the ordinary read open fails. Cleanup first atomically
// quarantines the current basename, verifies the quarantined inode, and only
// removes it when it is the originally pinned file. A raced replacement is
// restored instead of deleted.
func acquireProviderArtifact(
	rootPath string,
	artifactPath string,
	cleanupHook func(string),
) (*providerArtifact, error) {
	root, err := filepath.Abs(strings.TrimSpace(rootPath))
	if err != nil {
		return nil, err
	}
	path, err := filepath.Abs(artifactPath)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("provider artifact outside root")
	}
	parentRelative, base := filepath.Split(relative)
	parentRelative = filepath.Clean(parentRelative)
	if base == "" || base == "." || base == ".." ||
		strings.ContainsRune(base, filepath.Separator) {
		return nil, errors.New("provider artifact path is invalid")
	}

	rootDescriptor, err := unix.Open(
		root,
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open provider artifact root safely: %w", err)
	}
	parentDescriptor, err := openProviderArtifactParent(
		rootDescriptor,
		parentRelative,
	)
	_ = unix.Close(rootDescriptor)
	if err != nil {
		return nil, err
	}
	var parentStat unix.Stat_t
	parentStatErr := unix.Fstat(parentDescriptor, &parentStat)
	if parentStatErr != nil ||
		parentStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		parentStat.Uid != uint32(unix.Geteuid()) ||
		parentStat.Mode&0o022 != 0 {
		_ = unix.Close(parentDescriptor)
		return nil, fmt.Errorf(
			"provider artifact directory is not privately writable (root=%q parent=%q mode=%#o uid=%d euid=%d)",
			root,
			parentRelative,
			parentStat.Mode,
			parentStat.Uid,
			unix.Geteuid(),
		)
	}

	identityDescriptor, err := unix.Openat(
		parentDescriptor,
		base,
		unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		_ = unix.Close(parentDescriptor)
		return nil, fmt.Errorf("pin provider artifact safely: %w", err)
	}
	var identity unix.Stat_t
	if err := unix.Fstat(identityDescriptor, &identity); err != nil ||
		identity.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(identityDescriptor)
		_ = unix.Close(parentDescriptor)
		return nil, errors.New("provider artifact is not regular")
	}

	artifact := &providerArtifact{
		Size: identity.Size,
		consume: func() error {
			return consumeProviderArtifact(
				parentDescriptor,
				identityDescriptor,
				base,
				path,
				cleanupHook,
			)
		},
	}
	readDescriptor, openErr := unix.Openat(
		parentDescriptor,
		base,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if openErr != nil {
		return artifact, fmt.Errorf("open provider artifact for reading: %w", openErr)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(readDescriptor, &opened); err != nil ||
		opened.Dev != identity.Dev || opened.Ino != identity.Ino ||
		opened.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(readDescriptor)
		return artifact, errors.New("provider artifact changed before reading")
	}
	artifact.File = os.NewFile(uintptr(readDescriptor), path)
	if artifact.File == nil {
		_ = unix.Close(readDescriptor)
		return artifact, errors.New("open provider artifact safely: invalid descriptor")
	}
	return artifact, nil
}

func openProviderArtifactParent(rootDescriptor int, relative string) (int, error) {
	if relative == "." || relative == "" {
		descriptor, err := unix.Dup(rootDescriptor)
		if err != nil {
			return -1, fmt.Errorf("pin provider artifact root: %w", err)
		}
		unix.CloseOnExec(descriptor)
		return descriptor, nil
	}
	descriptor, err := unix.Openat2(
		rootDescriptor,
		filepath.ToSlash(relative),
		&unix.OpenHow{
			Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Resolve: unix.RESOLVE_BENEATH |
				unix.RESOLVE_NO_SYMLINKS |
				unix.RESOLVE_NO_MAGICLINKS,
		},
	)
	if err != nil {
		return -1, fmt.Errorf("pin provider artifact directory: %w", err)
	}
	return descriptor, nil
}

func consumeProviderArtifact(
	parentDescriptor int,
	identityDescriptor int,
	base string,
	path string,
	cleanupHook func(string),
) error {
	defer unix.Close(parentDescriptor)
	defer unix.Close(identityDescriptor)
	if cleanupHook != nil {
		cleanupHook(path)
	}
	quarantine, err := providerArtifactQuarantineName()
	if err != nil {
		return err
	}
	if err := unix.Renameat2(
		parentDescriptor,
		base,
		parentDescriptor,
		quarantine,
		unix.RENAME_NOREPLACE,
	); err != nil {
		return fmt.Errorf("quarantine consumed provider artifact: %w", err)
	}
	quarantinedDescriptor, openErr := unix.Openat(
		parentDescriptor,
		quarantine,
		unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if openErr != nil {
		return fmt.Errorf("inspect quarantined provider artifact: %w", openErr)
	}
	defer unix.Close(quarantinedDescriptor)
	var expected, quarantined unix.Stat_t
	expectedErr := unix.Fstat(identityDescriptor, &expected)
	quarantinedErr := unix.Fstat(quarantinedDescriptor, &quarantined)
	if expectedErr != nil || quarantinedErr != nil ||
		expected.Dev != quarantined.Dev || expected.Ino != quarantined.Ino ||
		quarantined.Mode&unix.S_IFMT != unix.S_IFREG {
		restoreErr := unix.Renameat2(
			parentDescriptor,
			quarantine,
			parentDescriptor,
			base,
			unix.RENAME_NOREPLACE,
		)
		return errors.Join(
			errors.New("provider artifact changed before cleanup"),
			restoreProviderArtifactError(restoreErr),
		)
	}
	// The parent is pinned, owner-controlled, and not group/other writable.
	// Once atomically quarantined, no untrusted filesystem principal can replace
	// this unpredictable name between verification and unlink.
	if err := unix.Unlinkat(parentDescriptor, quarantine, 0); err != nil {
		return fmt.Errorf("remove consumed provider artifact: %w", err)
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

func restoreProviderArtifactError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("restore raced provider artifact: %w", err)
}
