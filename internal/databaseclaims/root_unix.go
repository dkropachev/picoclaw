//go:build unix

package databaseclaims

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/sipeed/picoclaw/pkg/database"
	"golang.org/x/sys/unix"
)

func stableClaimCacheRoot() (string, error) {
	current, err := user.Current()
	return stableClaimCacheRootFor(current, err, os.Geteuid())
}

func stableClaimCacheRootFor(current *user.User, lookupErr error, effectiveUID int) (string, error) {
	if lookupErr != nil || current == nil || current.Uid != strconv.Itoa(effectiveUID) ||
		current.HomeDir == "" || current.HomeDir != strings.TrimSpace(current.HomeDir) ||
		strings.ContainsRune(current.HomeDir, 0) || !filepath.IsAbs(current.HomeDir) {
		return "", database.NewError(database.CodeUnavailable, "physical database claim owner home is unavailable")
	}
	home, err := filepath.EvalSymlinks(filepath.Clean(current.HomeDir))
	if err != nil || !filepath.IsAbs(home) {
		return "", database.NewError(database.CodeUnavailable, "physical database claim owner home is unavailable")
	}
	return filepath.Join(filepath.Clean(home), ".cache"), nil
}

type platformClaimRootOps struct {
	mkdirAll     func(string, os.FileMode) error
	lstat        func(string) (os.FileInfo, error)
	evalSymlinks func(string) (string, error)
	euid         func() int
}

func defaultPlatformClaimRootOps() platformClaimRootOps {
	return platformClaimRootOps{
		mkdirAll: os.MkdirAll, lstat: os.Lstat, evalSymlinks: filepath.EvalSymlinks, euid: os.Geteuid,
	}
}

func preparePlatformClaimRoot(_ string, root string) error {
	if err := createClaimRootNoFollow(root); err != nil {
		return err
	}
	return preparePlatformClaimRootWithOps(root, defaultPlatformClaimRootOps())
}

func createClaimRootNoFollow(root string) error {
	root = filepath.Clean(root)
	ancestor := root
	missing := make([]string, 0, 2)
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return database.NewError(database.CodeIntegrity, "physical database claim root ancestor is unsafe")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return database.NewError(database.CodeIntegrity, "physical database claim root has no trusted ancestor")
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	if err := validateClaimCreationBoundary(ancestor); err != nil {
		return err
	}
	fd, err := unix.Open(
		ancestor, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for index := len(missing) - 1; index >= 0; index-- {
		name := missing[index]
		if err := unix.Mkdirat(fd, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		next, err := unix.Openat(
			fd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
		)
		if err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
			stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o777 != 0o700 {
			_ = unix.Close(next)
			return database.NewError(database.CodeIntegrity, "physical database claim root boundary is invalid")
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

func preparePlatformClaimRootWithOps(root string, ops platformClaimRootOps) error {
	if ops.mkdirAll == nil || ops.lstat == nil || ops.evalSymlinks == nil || ops.euid == nil {
		return database.NewError(database.CodeIntegrity, "physical database claim root operations are invalid")
	}
	if err := validateClaimCreationBoundary(root); err != nil {
		return err
	}
	if err := ops.mkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create physical database claim root: %w", err)
	}
	info, err := ops.lstat(root)
	if err != nil {
		return fmt.Errorf("inspect physical database claim root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return database.NewError(database.CodeIntegrity, "physical database claim root is unsafe")
	}
	if info.Mode().Perm() != 0o700 {
		return database.NewError(database.CodeIntegrity, "physical database claim root must have mode 0700")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return database.NewError(database.CodeIntegrity, "physical database claim root ownership is unavailable")
	}
	if stat.Uid != uint32(ops.euid()) {
		return database.NewError(database.CodeUnauthorized, "physical database claim root is owned by another user")
	}
	resolved, err := ops.evalSymlinks(root)
	if err != nil {
		return fmt.Errorf("canonicalize physical database claim root: %w", err)
	}
	if filepath.Clean(resolved) != root {
		return database.NewError(database.CodeIntegrity, "physical database claim root contains an alias")
	}
	return nil
}

func validateClaimCreationBoundary(path string) error {
	ancestor := path
	var info os.FileInfo
	for {
		current, err := os.Lstat(ancestor)
		if err == nil {
			info = current
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect physical database claim root ancestor: %w", err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return database.NewError(database.CodeIntegrity, "physical database claim root has no trusted ancestor")
		}
		ancestor = parent
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return database.NewError(database.CodeIntegrity, "physical database claim root ancestor is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return database.NewError(database.CodeIntegrity, "physical database claim root ownership is unavailable")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return database.NewError(database.CodeUnauthorized, "physical database claim root ancestor is owned by another user")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return database.NewError(database.CodeIntegrity, "physical database claim root ancestor is writable by another user")
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return fmt.Errorf("canonicalize physical database claim root ancestor: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(ancestor) {
		return database.NewError(database.CodeIntegrity, "physical database claim root ancestor contains an alias")
	}
	return nil
}
