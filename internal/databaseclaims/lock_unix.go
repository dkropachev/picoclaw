//go:build unix && !aix

package databaseclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/database"
)

type unixClaimHandle struct {
	rootPath      string
	rootIdentity  unixClaimDirectoryIdentity
	fullHierarchy bool
	file          *os.File
	name          string
}

func acquireClaim(root string, identity string) (claimHandle, error) {
	return acquireUnixClaim(root, identity, true)
}

func acquireClaimForTesting(root string, identity string) (claimHandle, error) {
	return acquireUnixClaim(root, identity, false)
}

func acquireUnixClaim(root string, identity string, fullHierarchy bool) (result claimHandle, resultErr error) {
	if !validClaimIdentity(identity) {
		return nil, database.NewError(database.CodeIntegrity, "physical database claim identity is invalid")
	}
	before, err := inspectUnixClaimRoot(filepath.Clean(root), fullHierarchy)
	if err != nil {
		return nil, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open physical database claim directory: %w", err)
	}
	rootFile := os.NewFile(uintptr(rootFD), root)
	validRoot := false
	defer func() {
		if !validRoot {
			resultErr = errors.Join(resultErr, rootFile.Close())
		}
	}()
	after, err := inspectUnixClaimRoot(filepath.Clean(root), fullHierarchy)
	if err != nil || !slices.Equal(before, after) || len(after) == 0 ||
		!unixDescriptorMatchesIdentity(rootFD, after[len(after)-1]) {
		return nil, errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim hierarchy changed"), err,
		)
	}
	name := identity + ".lock"
	path := filepath.Join(root, name)
	fd, err := unix.Openat(rootFD, name, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, database.NewError(database.CodeIntegrity, "physical database claim lock cannot be a symlink")
		}
		return nil, fmt.Errorf("open physical database claim lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	valid := false
	defer func() {
		if !valid {
			resultErr = errors.Join(resultErr, file.Close())
		}
	}()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect physical database claim lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != 0o600 {
		return nil, database.NewError(database.CodeIntegrity, "physical database claim lock boundary is invalid")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errClaimBusy
		}
		return nil, fmt.Errorf("lock physical database claim: %w", err)
	}
	var current unix.Stat_t
	statErr := unix.Fstatat(rootFD, name, &current, unix.AT_SYMLINK_NOFOLLOW)
	if statErr != nil || !unixStatsSameObject(&stat, &current) || current.Mode&unix.S_IFMT != unix.S_IFREG {
		unlockErr := unix.Flock(fd, unix.LOCK_UN)
		return nil, errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim lock changed"),
			statErr,
			unlockErr,
		)
	}
	rootIdentity := after[len(after)-1]
	closeErr := rootFile.Close()
	validRoot = true
	if closeErr != nil {
		return nil, errors.Join(
			fmt.Errorf("close physical database claim directory: %w", closeErr),
			unix.Flock(fd, unix.LOCK_UN),
		)
	}
	valid = true
	return &unixClaimHandle{
		rootPath: root, rootIdentity: rootIdentity, fullHierarchy: fullHierarchy,
		file: file, name: name,
	}, nil
}

func inspectUnixClaimRoot(root string, fullHierarchy bool) ([]unixClaimDirectoryIdentity, error) {
	if fullHierarchy {
		return walkUnixClaimHierarchy(root, false, true)
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var stat unix.Stat_t
	statErr := unix.Fstat(fd, &stat)
	closeErr := unix.Close(fd)
	if statErr != nil || closeErr != nil {
		return nil, errors.Join(statErr, closeErr)
	}
	if err := validateUnixClaimDirectory(&stat, true); err != nil {
		return nil, err
	}
	return []unixClaimDirectoryIdentity{{
		device: stat.Dev, inode: stat.Ino, uid: stat.Uid, mode: stat.Mode,
	}}, nil
}

func (claim *unixClaimHandle) close() error {
	if claim == nil || claim.file == nil {
		return nil
	}
	file := claim.file
	claim.file = nil
	return errors.Join(unix.Flock(int(file.Fd()), unix.LOCK_UN), file.Close())
}

func (claim *unixClaimHandle) valid() (valid bool) {
	if claim == nil || claim.file == nil {
		return false
	}
	if claim.rootPath == "" || claim.name == "" {
		return false
	}
	chain, err := inspectUnixClaimRoot(claim.rootPath, claim.fullHierarchy)
	if err != nil || len(chain) == 0 || chain[len(chain)-1] != claim.rootIdentity {
		return false
	}
	rootFD, err := unix.Open(
		claim.rootPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		return false
	}
	defer func() {
		if unix.Close(rootFD) != nil {
			valid = false
		}
	}()
	if !unixDescriptorMatchesIdentity(rootFD, claim.rootIdentity) {
		return false
	}
	var opened, current unix.Stat_t
	if unix.Fstat(int(claim.file.Fd()), &opened) != nil ||
		unix.Fstatat(rootFD, claim.name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		!unixStatsSameObject(&opened, &current) || opened.Mode&unix.S_IFMT != unix.S_IFREG ||
		opened.Mode&0o777 != 0o600 || opened.Uid != uint32(os.Geteuid()) || opened.Nlink != 1 {
		return false
	}
	return true
}

func unixDescriptorMatchesIdentity(fd int, identity unixClaimDirectoryIdentity) bool {
	var stat unix.Stat_t
	return unix.Fstat(fd, &stat) == nil && stat.Dev == identity.device &&
		stat.Ino == identity.inode && stat.Uid == identity.uid && stat.Mode == identity.mode
}

func unixStatsSameObject(left, right *unix.Stat_t) bool {
	return left != nil && right != nil && left.Dev == right.Dev && left.Ino == right.Ino
}
