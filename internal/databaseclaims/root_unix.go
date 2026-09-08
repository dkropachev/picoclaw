//go:build unix

package databaseclaims

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/pkg/database"
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

func preparePlatformClaimRoot(cache string, root string) error {
	if filepath.Dir(filepath.Dir(root)) != filepath.Clean(cache) {
		return database.NewError(database.CodeIntegrity, "physical database claim root layout is invalid")
	}
	if err := createClaimRootNoFollow(root); err != nil {
		return err
	}
	return preparePlatformClaimRootWithOps(root, defaultPlatformClaimRootOps())
}

func createClaimRootNoFollow(root string) error {
	first, err := walkUnixClaimHierarchy(root, true, true)
	if err != nil {
		return err
	}
	second, err := walkUnixClaimHierarchy(root, false, true)
	if err != nil || !slices.Equal(first, second) {
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim root changed during creation"),
			err,
		)
	}
	return nil
}

type unixClaimDirectoryIdentity struct {
	device any
	inode  uint64
	uid    uint32
	mode   any
}

func walkUnixClaimHierarchy(
	path string,
	create, privateLeaf bool,
) (identities []unixClaimDirectoryIdentity, resultErr error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(path) || clean != path {
		return nil, database.NewError(
			database.CodeIntegrity,
			"physical database claim root must be canonical and absolute",
		)
	}
	trimmed := strings.TrimPrefix(clean, string(os.PathSeparator))
	var components []string
	if trimmed != "" {
		components = strings.Split(trimmed, string(os.PathSeparator))
	}
	fd, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unixClaimHierarchyOpenError(err)
	}
	defer func() {
		if fd >= 0 {
			resultErr = errors.Join(resultErr, unix.Close(fd))
		}
	}()
	identities = make([]unixClaimDirectoryIdentity, 0, len(components)+1)
	appendIdentity := func(descriptor int, leaf bool) error {
		var stat unix.Stat_t
		if statErr := unix.Fstat(descriptor, &stat); statErr != nil {
			return fmt.Errorf("inspect physical database claim root ancestor: %w", statErr)
		}
		if err := validateUnixClaimDirectory(&stat, leaf && privateLeaf); err != nil {
			return err
		}
		identities = append(identities, unixClaimDirectoryIdentity{
			device: stat.Dev, inode: stat.Ino, uid: stat.Uid, mode: stat.Mode,
		})
		return nil
	}
	if err := appendIdentity(fd, len(components) == 0); err != nil {
		return nil, err
	}
	for index, component := range components {
		next, openErr := unix.Openat(
			fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
		)
		if errors.Is(openErr, unix.ENOENT) && create {
			var parent unix.Stat_t
			if statErr := unix.Fstat(fd, &parent); statErr != nil {
				return nil, fmt.Errorf("inspect physical database claim creation parent: %w", statErr)
			}
			if parent.Uid != uint32(os.Geteuid()) || parent.Mode&0o022 != 0 {
				return nil, database.NewError(
					database.CodeUnauthorized,
					"physical database claim parent is not exclusively mutable by its owner",
				)
			}
			if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil &&
				!errors.Is(mkdirErr, unix.EEXIST) {
				return nil, fmt.Errorf("create physical database claim root: %w", mkdirErr)
			}
			next, openErr = unix.Openat(
				fd, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0,
			)
		}
		if openErr != nil {
			return nil, unixClaimHierarchyOpenError(openErr)
		}
		if err := appendIdentity(next, index == len(components)-1); err != nil {
			return nil, errors.Join(err, unix.Close(next))
		}
		if closeErr := unix.Close(fd); closeErr != nil {
			fd = -1
			return nil, errors.Join(
				fmt.Errorf("close physical database claim root ancestor: %w", closeErr),
				unix.Close(next),
			)
		}
		fd = next
	}
	return identities, nil
}

func unixClaimHierarchyOpenError(err error) error {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim root ancestor is unsafe"),
			err,
		)
	}
	return fmt.Errorf("open physical database claim root ancestor: %w", err)
}

func validateUnixClaimDirectory(stat *unix.Stat_t, private bool) error {
	if stat == nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return database.NewError(database.CodeIntegrity, "physical database claim root ancestor is unsafe")
	}
	euid := uint32(os.Geteuid())
	if stat.Uid != euid && stat.Uid != 0 {
		return database.NewError(
			database.CodeUnauthorized,
			"physical database claim root ancestor has an untrusted owner",
		)
	}
	if stat.Mode&0o022 != 0 {
		return database.NewError(
			database.CodeIntegrity,
			"physical database claim root ancestor is mutable by another user",
		)
	}
	if private && (stat.Uid != euid || stat.Mode&0o777 != 0o700) {
		return database.NewError(database.CodeIntegrity, "physical database claim root boundary is invalid")
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
	clean := filepath.Clean(path)
	if !filepath.IsAbs(path) || clean != path {
		return database.NewError(database.CodeIntegrity, "physical database claim root must be canonical and absolute")
	}
	ancestor := clean
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
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
	chain, err := walkUnixClaimHierarchy(ancestor, false, false)
	if err != nil {
		return err
	}
	if len(chain) == 0 || chain[len(chain)-1].uid != uint32(os.Geteuid()) {
		return database.NewError(
			database.CodeUnauthorized,
			"physical database claim root ancestor is owned by another user",
		)
	}
	return nil
}
