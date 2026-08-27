//go:build linux

package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type applyPatchTxnPlatformAnchor struct {
	fd int
}

func applyPatchTxnPlatformRuntimeSupport() error { return nil }

func applyPatchTxnPlatformIdentityFromFileInfo(
	info os.FileInfo,
	expectedKind string,
) (applyPatchTxnIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return applyPatchTxnIdentity{}, errors.New(
			"apply-patch transaction file identity is unavailable",
		)
	}
	var kind string
	switch stat.Mode & syscall.S_IFMT {
	case syscall.S_IFREG:
		kind = "regular"
	case syscall.S_IFDIR:
		kind = "directory"
	case syscall.S_IFLNK:
		kind = "symlink"
	default:
		kind = "special"
	}
	if kind != expectedKind {
		return applyPatchTxnIdentity{}, fmt.Errorf(
			"apply-patch transaction object is %s, want %s",
			kind,
			expectedKind,
		)
	}
	return applyPatchTxnIdentity{
		Device: stat.Dev, File: stat.Ino, Kind: kind,
	}, nil
}

func openApplyPatchTxnPlatformAnchor(
	canonical string,
) (applyPatchTxnPlatformAnchor, applyPatchTxnIdentity, error) {
	rootFD, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return applyPatchTxnPlatformAnchor{}, applyPatchTxnIdentity{},
			fmt.Errorf("open apply-patch transaction filesystem root: %w", err)
	}
	defer unix.Close(rootFD)
	relative, err := filepath.Rel("/", canonical)
	if err != nil || relative == ".." {
		return applyPatchTxnPlatformAnchor{}, applyPatchTxnIdentity{},
			errors.New("apply-patch transaction anchor is invalid")
	}
	var fd int
	if relative == "." {
		fd, err = unix.Dup(rootFD)
		if err == nil {
			unix.CloseOnExec(fd)
		}
	} else {
		fd, err = unix.Openat2(rootFD, filepath.ToSlash(relative), &unix.OpenHow{
			Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Resolve: unix.RESOLVE_BENEATH |
				unix.RESOLVE_NO_SYMLINKS |
				unix.RESOLVE_NO_MAGICLINKS,
		})
	}
	if err != nil {
		return applyPatchTxnPlatformAnchor{}, applyPatchTxnIdentity{},
			wrapApplyPatchTxnLinuxPrimitiveError(
				"open apply-patch transaction anchor safely",
				err,
			)
	}
	anchor := applyPatchTxnPlatformAnchor{fd: fd}
	identity, err := applyPatchTxnPlatformAnchorIdentity(anchor)
	if err != nil {
		unix.Close(fd)
		return applyPatchTxnPlatformAnchor{}, applyPatchTxnIdentity{}, err
	}
	return anchor, identity, nil
}

func closeApplyPatchTxnPlatformAnchor(anchor applyPatchTxnPlatformAnchor) error {
	if anchor.fd < 0 {
		return nil
	}
	if err := unix.Close(anchor.fd); err != nil {
		return fmt.Errorf("close apply-patch transaction anchor: %w", err)
	}
	return nil
}

func applyPatchTxnPlatformAnchorIdentity(
	anchor applyPatchTxnPlatformAnchor,
) (applyPatchTxnIdentity, error) {
	var stat unix.Stat_t
	if anchor.fd < 0 || unix.Fstat(anchor.fd, &stat) != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return applyPatchTxnIdentity{}, errors.New("apply-patch transaction anchor is unavailable")
	}
	return applyPatchTxnIdentity{
		Device: stat.Dev, File: stat.Ino, Kind: "directory",
	}, nil
}

func applyPatchTxnPlatformCreateRegular(
	anchor applyPatchTxnPlatformAnchor,
	name string,
	mode os.FileMode,
) (*os.File, applyPatchTxnIdentity, error) {
	fd, err := unix.Openat(
		anchor.fd,
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		uint32(mode.Perm()),
	)
	if err != nil {
		return nil, applyPatchTxnIdentity{},
			fmt.Errorf("create apply-patch transaction file exclusively: %w", err)
	}
	state, err := applyPatchTxnStateFromFD(fd, "regular")
	if err != nil {
		unix.Close(fd)
		return nil, applyPatchTxnIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, applyPatchTxnIdentity{}, errors.New("open apply-patch transaction file: invalid descriptor")
	}
	return file, state.Identity, nil
}

func applyPatchTxnPlatformInspectAt(
	anchor applyPatchTxnPlatformAnchor,
	name string,
) (applyPatchTxnObjectState, error) {
	fd, err := unix.Openat(
		anchor.fd,
		name,
		unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return applyPatchTxnObjectState{}, err
	}
	defer unix.Close(fd)
	return applyPatchTxnStateFromFD(fd, "")
}

func applyPatchTxnPlatformOpenRegular(
	anchor applyPatchTxnPlatformAnchor,
	name string,
) (*os.File, os.FileMode, applyPatchTxnIdentity, error) {
	fd, err := unix.Openat(
		anchor.fd,
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, 0, applyPatchTxnIdentity{}, err
	}
	state, err := applyPatchTxnStateFromFD(fd, "regular")
	if err != nil {
		unix.Close(fd)
		return nil, 0, applyPatchTxnIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, 0, applyPatchTxnIdentity{}, errors.New("open apply-patch transaction file: invalid descriptor")
	}
	return file, state.Mode, state.Identity, nil
}

func applyPatchTxnPlatformOpenRegularWrite(
	anchor applyPatchTxnPlatformAnchor,
	name string,
	expected applyPatchTxnIdentity,
) (*os.File, error) {
	fd, err := unix.Openat(
		anchor.fd,
		name,
		unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, err
	}
	state, err := applyPatchTxnStateFromFD(fd, "regular")
	if err != nil || !state.Identity.equal(expected) {
		_ = unix.Close(fd)
		return nil, errors.Join(
			errors.New("apply-patch transaction restore stage identity changed"),
			err,
		)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open apply-patch transaction restore stage: invalid descriptor")
	}
	return file, nil
}

func applyPatchTxnPlatformLinkNoReplace(
	from applyPatchTxnPlatformAnchor,
	fromName string,
	to applyPatchTxnPlatformAnchor,
	toName string,
) error {
	if err := unix.Linkat(from.fd, fromName, to.fd, toName, 0); err != nil {
		return wrapApplyPatchTxnLinuxPrimitiveError(
			"link apply-patch transaction file without replacement",
			err,
		)
	}
	return nil
}

func applyPatchTxnPlatformRenameNoReplace(
	from applyPatchTxnPlatformAnchor,
	fromName string,
	to applyPatchTxnPlatformAnchor,
	toName string,
) error {
	if err := unix.Renameat2(
		from.fd, fromName, to.fd, toName, unix.RENAME_NOREPLACE,
	); err != nil {
		return wrapApplyPatchTxnLinuxPrimitiveError(
			"move apply-patch transaction object without replacement",
			err,
		)
	}
	return nil
}

func applyPatchTxnPlatformRemoveExact(
	anchor applyPatchTxnPlatformAnchor,
	name string,
	removalName string,
	expected applyPatchTxnIdentity,
	directory bool,
	afterQuarantine func() error,
) error {
	flags := 0
	if directory {
		flags = unix.AT_REMOVEDIR
	}
	removal, removalErr := applyPatchTxnPlatformInspectAt(anchor, removalName)
	if removalErr == nil {
		if !removal.Identity.equal(expected) ||
			directory && removal.Identity.Kind != "directory" ||
			!directory && removal.Identity.Kind != "regular" {
			return errors.New("apply-patch transaction removal quarantine conflict")
		}
		_, sourceErr := applyPatchTxnPlatformInspectAt(anchor, name)
		if sourceErr == nil || !errors.Is(sourceErr, os.ErrNotExist) {
			return errors.Join(
				errors.New("apply-patch transaction removal source reappeared"),
				sourceErr,
			)
		}
	} else if errors.Is(removalErr, os.ErrNotExist) {
		if err := unix.Renameat2(
			anchor.fd,
			name,
			anchor.fd,
			removalName,
			unix.RENAME_NOREPLACE,
		); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("quarantine apply-patch transaction removal: %w", err)
		}
		var inspectErr error
		removal, inspectErr = applyPatchTxnPlatformInspectAt(anchor, removalName)
		if inspectErr != nil || !removal.Identity.equal(expected) ||
			directory && removal.Identity.Kind != "directory" ||
			!directory && removal.Identity.Kind != "regular" {
			restoreErr := unix.Renameat2(
				anchor.fd,
				removalName,
				anchor.fd,
				name,
				unix.RENAME_NOREPLACE,
			)
			return errors.Join(
				errors.New("apply-patch transaction object changed before removal"),
				inspectErr,
				wrapApplyPatchTxnRestoreError(restoreErr),
			)
		}
	} else {
		return removalErr
	}
	expectedRemovalLinks := removal.Links
	if afterQuarantine != nil {
		if err := afterQuarantine(); err != nil {
			return err
		}
	}
	removal, removalErr = applyPatchTxnPlatformInspectAt(anchor, removalName)
	if removalErr != nil || !removal.Identity.equal(expected) ||
		removal.Links != expectedRemovalLinks ||
		directory && removal.Identity.Kind != "directory" ||
		!directory && removal.Identity.Kind != "regular" {
		return errors.Join(
			errors.New("apply-patch transaction removal quarantine changed"),
			removalErr,
		)
	}
	_, sourceErr := applyPatchTxnPlatformInspectAt(anchor, name)
	if sourceErr == nil || !errors.Is(sourceErr, os.ErrNotExist) {
		return errors.Join(
			errors.New("apply-patch transaction removal source reappeared"),
			sourceErr,
		)
	}
	if err := unix.Unlinkat(anchor.fd, removalName, flags); err != nil {
		return fmt.Errorf("remove exact apply-patch transaction object: %w", err)
	}
	return nil
}

func applyPatchTxnPlatformMkdir(
	anchor applyPatchTxnPlatformAnchor,
	name string,
	mode os.FileMode,
) (applyPatchTxnIdentity, error) {
	if err := unix.Mkdirat(anchor.fd, name, uint32(mode.Perm())); err != nil {
		return applyPatchTxnIdentity{}, fmt.Errorf("create apply-patch transaction directory exclusively: %w", err)
	}
	state, err := applyPatchTxnPlatformInspectAt(anchor, name)
	if err != nil {
		return applyPatchTxnIdentity{}, err
	}
	if state.Identity.Kind != "directory" || !state.Mode.IsDir() {
		return applyPatchTxnIdentity{}, errors.New("apply-patch transaction directory is not a directory")
	}
	return state.Identity, nil
}

func applyPatchTxnPlatformOpenChildDirectory(
	anchor applyPatchTxnPlatformAnchor,
	name string,
) (applyPatchTxnPlatformAnchor, applyPatchTxnIdentity, error) {
	fd, err := unix.Openat2(anchor.fd, name, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH |
			unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return applyPatchTxnPlatformAnchor{}, applyPatchTxnIdentity{},
			fmt.Errorf("open apply-patch transaction child directory safely: %w", err)
	}
	child := applyPatchTxnPlatformAnchor{fd: fd}
	identity, err := applyPatchTxnPlatformAnchorIdentity(child)
	if err != nil {
		unix.Close(fd)
		return applyPatchTxnPlatformAnchor{}, applyPatchTxnIdentity{}, err
	}
	return child, identity, nil
}

func applyPatchTxnPlatformSyncDirectory(anchor applyPatchTxnPlatformAnchor) error {
	fd, err := unix.Openat(anchor.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open apply-patch transaction directory for sync: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Fsync(fd); err != nil {
		return fmt.Errorf("sync apply-patch transaction directory: %w", err)
	}
	return nil
}

func applyPatchTxnPlatformReadDirectoryNames(
	anchor applyPatchTxnPlatformAnchor,
	limit int,
) ([]string, error) {
	fd, err := unix.Openat(
		anchor.fd,
		".",
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open apply-patch transaction directory listing: %w", err)
	}
	file := os.NewFile(uintptr(fd), ".")
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open apply-patch transaction directory listing: invalid descriptor")
	}
	entries, readErr := file.ReadDir(limit + 1)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(entries) > limit {
		return nil, errors.New("apply-patch transaction directory has an alien entry")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func applyPatchTxnPlatformProbeNoReplace(
	anchor applyPatchTxnPlatformAnchor,
	name string,
) error {
	err := unix.Renameat2(
		anchor.fd,
		name,
		anchor.fd,
		name,
		unix.RENAME_NOREPLACE,
	)
	if err == nil || errors.Is(err, unix.EEXIST) {
		return nil
	}
	return wrapApplyPatchTxnLinuxPrimitiveError(
		"probe apply-patch transaction no-replace move",
		err,
	)
}

func applyPatchTxnStateFromFD(
	fd int,
	expectedKind string,
) (applyPatchTxnObjectState, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return applyPatchTxnObjectState{}, err
	}
	var kind string
	switch stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = "regular"
	case unix.S_IFDIR:
		kind = "directory"
	case unix.S_IFLNK:
		kind = "symlink"
	default:
		kind = "special"
	}
	if expectedKind != "" && kind != expectedKind {
		return applyPatchTxnObjectState{},
			fmt.Errorf("apply-patch transaction object is %s, want %s", kind, expectedKind)
	}
	mode := os.FileMode(stat.Mode & 0o777)
	switch kind {
	case "directory":
		mode |= os.ModeDir
	case "symlink":
		mode |= os.ModeSymlink
	}
	return applyPatchTxnObjectState{
		Identity: applyPatchTxnIdentity{
			Device: stat.Dev, File: stat.Ino, Kind: kind,
		},
		Mode: mode, Links: stat.Nlink, Size: stat.Size,
	}, nil
}

func wrapApplyPatchTxnLinuxPrimitiveError(operation string, err error) error {
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("%s: %w", operation, err)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) {
		return errors.Join(errApplyPatchTransactionUnsupported, wrapped)
	}
	return wrapped
}
