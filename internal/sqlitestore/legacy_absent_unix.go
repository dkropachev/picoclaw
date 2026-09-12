//go:build unix && !aix

package sqlitestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type sealedAbsentLegacyRootPlatform struct {
	ancestor *os.File
	identity fileidentity.Identity
}

var (
	sealedAbsentUnixOpen       = unix.Open
	sealedAbsentUnixOpenat     = unix.Openat
	sealedAbsentUnixFstat      = unix.Fstat
	sealedAbsentUnixFstatat    = unix.Fstatat
	sealedAbsentUnixCloseFD    = unix.Close
	sealedAbsentUnixNewFile    = os.NewFile
	sealedAbsentUnixFileOpened = fileidentity.Opened
	sealedAbsentUnixFileStat   = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
	sealedAbsentUnixFileClose  = func(file *os.File) error { return file.Close() }
)

func captureSealedAbsentLegacyRootPlatform(
	ctx context.Context,
	path string,
) (_ *sealedAbsentLegacyRootPlatform, _ string, _ []string, returnErr error) {
	components, err := sealedAbsentLegacyRelativeComponents(path, string(os.PathSeparator))
	if err != nil {
		return nil, "", nil, err
	}
	current, err := openSealedAbsentUnixFilesystemRoot()
	if err != nil {
		return nil, "", nil, err
	}
	currentPath := string(os.PathSeparator)
	defer func() {
		if returnErr != nil && current != nil {
			returnErr = errors.Join(returnErr, sealedAbsentUnixFileClose(current))
		}
	}()

	for index, component := range components {
		if err := context.Cause(ctx); err != nil {
			return nil, "", nil, err
		}
		next, openErr := openSealedAbsentUnixDirectoryAt(current, component)
		if errors.Is(openErr, unix.ENOENT) {
			identity, identityErr := sealedAbsentUnixTrustedDirectoryIdentity(current)
			if identityErr != nil {
				return nil, "", nil, identityErr
			}
			platform := &sealedAbsentLegacyRootPlatform{
				ancestor: current,
				identity: identity,
			}
			current = nil
			return platform, filepath.Clean(currentPath), append([]string(nil), components[index:]...), nil
		}
		if openErr != nil {
			return nil, "", nil, fmt.Errorf(
				"inspect sealed absent legacy root component without following links: %w",
				openErr,
			)
		}
		if closeErr := sealedAbsentUnixFileClose(current); closeErr != nil {
			return nil, "", nil, errors.Join(closeErr, sealedAbsentUnixFileClose(next))
		}
		current = next
		currentPath = filepath.Join(currentPath, component)
	}
	return nil, "", nil, errors.New("sealed absent legacy root unexpectedly exists")
}

func revalidateSealedAbsentLegacyRootPlatform(
	ctx context.Context,
	_ string,
	ancestorPath string,
	suffix []string,
	platform *sealedAbsentLegacyRootPlatform,
) error {
	if ctx == nil || platform == nil || platform.ancestor == nil ||
		!platform.identity.Valid() || len(suffix) == 0 {
		return errors.New("sealed absent legacy root Unix proof is unavailable")
	}
	if err := validateSealedAbsentUnixRetainedAncestor(platform); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := validateSealedAbsentUnixNamedAncestor(ctx, ancestorPath, platform.identity); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := requireSealedAbsentUnixName(platform.ancestor, suffix[0]); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := validateSealedAbsentUnixNamedAncestor(ctx, ancestorPath, platform.identity); err != nil {
		return err
	}
	return validateSealedAbsentUnixRetainedAncestor(platform)
}

func closeSealedAbsentLegacyRootPlatform(platform *sealedAbsentLegacyRootPlatform) error {
	if platform == nil || platform.ancestor == nil {
		return nil
	}
	ancestor := platform.ancestor
	platform.ancestor = nil
	return sealedAbsentUnixFileClose(ancestor)
}

func openSealedAbsentUnixFilesystemRoot() (*os.File, error) {
	fd, err := sealedAbsentUnixOpen(
		string(os.PathSeparator),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open sealed absent legacy filesystem root: %w", err)
	}
	file := sealedAbsentUnixNewFile(uintptr(fd), string(os.PathSeparator))
	if file == nil {
		_ = sealedAbsentUnixCloseFD(fd)
		return nil, errors.New("sealed absent legacy filesystem root handle is unavailable")
	}
	return file, nil
}

func openSealedAbsentUnixDirectoryAt(parent *os.File, component string) (*os.File, error) {
	if parent == nil || !validSealedAbsentLegacyComponent(component) {
		return nil, errors.New("sealed absent legacy directory lookup is invalid")
	}
	fd, err := sealedAbsentUnixOpenat(
		int(parent.Fd()),
		component,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := sealedAbsentUnixNewFile(uintptr(fd), component)
	if file == nil {
		_ = sealedAbsentUnixCloseFD(fd)
		return nil, errors.New("sealed absent legacy directory handle is unavailable")
	}
	identity, objectType, err := sealedAbsentUnixFileOpened(file)
	if err != nil || objectType != fileidentity.ObjectTypeDirectory || !identity.Valid() {
		return nil, errors.Join(
			errors.New("sealed absent legacy path component is not a stable directory"),
			err,
			sealedAbsentUnixFileClose(file),
		)
	}
	return file, nil
}

func sealedAbsentUnixTrustedDirectoryIdentity(file *os.File) (fileidentity.Identity, error) {
	if file == nil {
		return fileidentity.Identity{}, errors.New("sealed absent legacy ancestor handle is unavailable")
	}
	identity, objectType, err := sealedAbsentUnixFileOpened(file)
	info, statErr := sealedAbsentUnixFileStat(file)
	var native unix.Stat_t
	nativeErr := sealedAbsentUnixFstat(int(file.Fd()), &native)
	if err != nil || statErr != nil || objectType != fileidentity.ObjectTypeDirectory ||
		!identity.Valid() || !safeLegacyDirectory("", info) || info.Mode().Perm()&0o077 != 0 ||
		nativeErr != nil || native.Mode&unix.S_IFMT != unix.S_IFDIR ||
		native.Uid != uint32(os.Geteuid()) {
		return fileidentity.Identity{}, errors.Join(
			errors.New("sealed absent legacy ancestor is not a private stable directory"),
			err,
			statErr,
			nativeErr,
		)
	}
	return identity, nil
}

func validateSealedAbsentUnixRetainedAncestor(platform *sealedAbsentLegacyRootPlatform) error {
	identity, err := sealedAbsentUnixTrustedDirectoryIdentity(platform.ancestor)
	if err != nil || identity != platform.identity {
		return errors.Join(
			errors.New("sealed absent legacy retained ancestor identity or type changed"),
			err,
		)
	}
	return nil
}

func validateSealedAbsentUnixNamedAncestor(
	ctx context.Context,
	path string,
	expected fileidentity.Identity,
) (returnErr error) {
	if ctx == nil {
		return errors.New("sealed absent legacy named-ancestor context is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	current, err := openSealedAbsentUnixFilesystemRoot()
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, sealedAbsentUnixFileClose(current)) }()
	var components []string
	if filepath.Clean(path) != string(os.PathSeparator) {
		components, err = sealedAbsentLegacyRelativeComponents(path, string(os.PathSeparator))
		if err != nil {
			return err
		}
	}
	for _, component := range components {
		if causeErr := context.Cause(ctx); causeErr != nil {
			return causeErr
		}
		next, openErr := openSealedAbsentUnixDirectoryAt(current, component)
		if openErr != nil {
			return fmt.Errorf("reopen sealed absent legacy named ancestor: %w", openErr)
		}
		if closeErr := sealedAbsentUnixFileClose(current); closeErr != nil {
			_ = sealedAbsentUnixFileClose(next)
			return closeErr
		}
		current = next
	}
	if causeErr := context.Cause(ctx); causeErr != nil {
		return causeErr
	}
	identity, err := sealedAbsentUnixTrustedDirectoryIdentity(current)
	if err != nil || identity != expected {
		return errors.Join(
			errors.New("sealed absent legacy named ancestor identity or type changed"),
			err,
		)
	}
	return nil
}

func requireSealedAbsentUnixName(parent *os.File, component string) error {
	if parent == nil || !validSealedAbsentLegacyComponent(component) {
		return errors.New("sealed absent legacy missing-name probe is invalid")
	}
	var stat unix.Stat_t
	err := sealedAbsentUnixFstatat(
		int(parent.Fd()),
		component,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect sealed absent legacy missing component: %w", err)
	}
	return errors.New("sealed absent legacy root or missing ancestor appeared")
}
