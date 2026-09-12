//go:build unix && !aix

package sqliteprovider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type stagedRetirementPlatform struct {
	parentPath string
	leaf       string
	quarantine string
	parent     *os.File
	stage      *os.File
	identity   fileidentity.Identity
	stat       unix.Stat_t
	unlinked   bool
}

func retainStagedGenerationPlatform(
	path string,
) (result *stagedRetirementPlatform, returnErr error) {
	parentPath := filepath.Dir(path)
	leaf := filepath.Base(path)
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		return nil, fmt.Errorf("inspect SQLite staged retirement parent: %w", err)
	}
	parentFD, err := unix.Open(
		parentPath,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("retain SQLite staged retirement parent: %w", err)
	}
	parent := os.NewFile(uintptr(parentFD), parentPath)
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, parent.Close())
		}
	}()
	var parentStat unix.Stat_t
	if statErr := unix.Fstat(parentFD, &parentStat); statErr != nil {
		return nil, fmt.Errorf("inspect retained SQLite staged retirement parent: %w", statErr)
	}
	if !sameUnixProviderFileInfoAndStat(parentInfo, &parentStat) {
		return nil, errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement parent changed while retaining"),
		)
	}
	if validationErr := validateUnixProviderParentStat(
		&parentStat,
		uint32(os.Geteuid()),
	); validationErr != nil {
		return nil, validationErr
	}

	stageFD, err := unix.Openat(
		parentFD,
		leaf,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("retain SQLite staged retirement identity: %w", err)
	}
	stage := os.NewFile(uintptr(stageFD), path)
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, stage.Close())
		}
	}()

	identity, objectType, err := fileidentity.Opened(stage)
	if err != nil || objectType != fileidentity.ObjectTypeRegular || !identity.Valid() {
		return nil, errors.Join(
			errors.New("SQLite staged retirement identity is unsafe"),
			err,
		)
	}
	var retainedStat unix.Stat_t
	if err := unix.Fstat(stageFD, &retainedStat); err != nil {
		return nil, fmt.Errorf("inspect SQLite staged retirement identity: %w", err)
	}
	if err := validateUnixProviderLiveStat(
		&retainedStat,
		uint32(os.Geteuid()),
		true,
	); err != nil {
		return nil, err
	}
	var pathStat unix.Stat_t
	if err := unix.Fstatat(parentFD, leaf, &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("inspect SQLite staged retirement path: %w", err)
	}
	if !sameUnixStagedRetirementIdentity(&retainedStat, &pathStat) || pathStat.Nlink != 1 {
		return nil, errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement path changed while retaining"),
		)
	}

	result = &stagedRetirementPlatform{
		parentPath: parentPath,
		leaf:       leaf,
		parent:     parent,
		stage:      stage,
		identity:   identity,
		stat:       retainedStat,
	}
	if err := result.validateParent(); err != nil {
		return nil, err
	}
	return result, nil
}

func (retained *stagedRetirementPlatform) retire(ctx context.Context) error {
	if retained == nil || retained.parent == nil || retained.stage == nil ||
		!retained.identity.Valid() {
		return errors.New("SQLite staged retirement handles are unavailable")
	}
	if retained.unlinked {
		return retained.finishUnlinked()
	}
	if retained.quarantine == "" {
		if err := retained.validateOriginal(); err != nil {
			return err
		}
		quarantine, err := unusedStagedRetirementLeaf(retained.quarantineAvailable)
		if err != nil {
			return err
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		// Once this rename begins, retirement is a short synchronous critical
		// section. Cancellation is deliberately not observed halfway through it:
		// either the exact retained object remains quarantined or all retirement
		// proofs and durability work complete before this method returns.
		if err := renameStagedRetirementNoReplace(
			int(retained.parent.Fd()),
			retained.leaf,
			int(retained.parent.Fd()),
			quarantine,
		); err != nil {
			return fmt.Errorf("quarantine SQLite staged retirement identity: %w", err)
		}
		retained.quarantine = quarantine
	}
	if err := retained.validateQuarantine(); err != nil {
		return err
	}
	if err := unix.Unlinkat(
		int(retained.parent.Fd()),
		retained.quarantine,
		0,
	); err != nil {
		var current unix.Stat_t
		if statErr := unix.Fstat(int(retained.stage.Fd()), &current); statErr == nil &&
			sameUnixStagedRetirementIdentity(&retained.stat, &current) && current.Nlink == 0 {
			retained.unlinked = true
			return errors.Join(
				fmt.Errorf("unlink quarantined SQLite stage: %w", err),
				retained.finishUnlinked(),
			)
		}
		return fmt.Errorf("unlink quarantined SQLite stage: %w", err)
	}
	retained.unlinked = true
	return retained.finishUnlinked()
}

func (retained *stagedRetirementPlatform) validateOriginal() error {
	return retained.check(filepath.Join(retained.parentPath, retained.leaf))
}

func (retained *stagedRetirementPlatform) check(path string) error {
	if filepath.Dir(path) != retained.parentPath {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retention path has a different parent"),
		)
	}
	if err := retained.validateParent(); err != nil {
		return err
	}
	if err := retained.validateHandleLinked(); err != nil {
		return err
	}
	var pathStat unix.Stat_t
	if err := unix.Fstatat(
		int(retained.parent.Fd()),
		filepath.Base(path),
		&pathStat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return errors.Join(
			errProviderGenerationTransition,
			fmt.Errorf("inspect retained SQLite stage path: %w", err),
		)
	}
	if !sameUnixStagedRetirementIdentity(&retained.stat, &pathStat) ||
		pathStat.Mode&unix.S_IFMT != unix.S_IFREG || pathStat.Nlink != 1 {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement path no longer names its retained identity"),
		)
	}
	return nil
}

func (retained *stagedRetirementPlatform) validateQuarantine() error {
	if retained.quarantine == "" {
		return errors.New("SQLite staged retirement quarantine is unavailable")
	}
	if err := retained.validateParent(); err != nil {
		return err
	}
	if err := retained.validateHandleLinked(); err != nil {
		return err
	}
	if err := retained.requireMissing(retained.leaf); err != nil {
		return errors.Join(
			errors.New("SQLite staged retirement source name remains after quarantine"),
			err,
		)
	}
	var quarantineStat unix.Stat_t
	if err := unix.Fstatat(
		int(retained.parent.Fd()),
		retained.quarantine,
		&quarantineStat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return fmt.Errorf("inspect quarantined SQLite stage: %w", err)
	}
	if !sameUnixStagedRetirementIdentity(&retained.stat, &quarantineStat) ||
		quarantineStat.Mode&unix.S_IFMT != unix.S_IFREG || quarantineStat.Nlink != 1 {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement quarantine does not name its retained identity"),
		)
	}
	return nil
}

func (retained *stagedRetirementPlatform) validateHandleLinked() error {
	var current unix.Stat_t
	if err := unix.Fstat(int(retained.stage.Fd()), &current); err != nil {
		return fmt.Errorf("inspect retained SQLite stage handle: %w", err)
	}
	identity, objectType, err := fileidentity.Opened(retained.stage)
	if err != nil || identity != retained.identity || objectType != fileidentity.ObjectTypeRegular ||
		!sameUnixStagedRetirementIdentity(&retained.stat, &current) || current.Nlink != 1 {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement retained identity changed or gained a link"),
			err,
		)
	}
	return nil
}

func (retained *stagedRetirementPlatform) validateParent() error {
	var opened unix.Stat_t
	if err := unix.Fstat(int(retained.parent.Fd()), &opened); err != nil {
		return fmt.Errorf("inspect retained SQLite staged retirement parent: %w", err)
	}
	if err := validateUnixProviderParentStat(&opened, uint32(os.Geteuid())); err != nil {
		return err
	}
	current, err := os.Lstat(retained.parentPath)
	if err != nil || !sameUnixProviderFileInfoAndStat(current, &opened) {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement parent path changed"),
			err,
		)
	}
	return nil
}

func (retained *stagedRetirementPlatform) quarantineAvailable(leaf string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(
		int(retained.parent.Fd()),
		leaf,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func (retained *stagedRetirementPlatform) requireMissing(leaf string) error {
	var stat unix.Stat_t
	err := unix.Fstatat(
		int(retained.parent.Fd()),
		leaf,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("SQLite staged retirement path unexpectedly exists")
}

func (retained *stagedRetirementPlatform) finishUnlinked() error {
	var current unix.Stat_t
	statErr := unix.Fstat(int(retained.stage.Fd()), &current)
	var proofErr error
	if statErr != nil || !sameUnixStagedRetirementIdentity(&retained.stat, &current) ||
		current.Mode&unix.S_IFMT != unix.S_IFREG || current.Nlink != 0 {
		proofErr = errors.Join(
			errors.New("SQLite staged retirement retained handle is not unlinked"),
			statErr,
		)
	}
	proofErr = errors.Join(
		proofErr,
		retained.requireMissing(retained.leaf),
		retained.requireMissing(retained.quarantine),
		retained.validateParent(),
	)
	return errors.Join(proofErr, unix.Fsync(int(retained.parent.Fd())))
}

func (retained *stagedRetirementPlatform) close() error {
	if retained == nil {
		return nil
	}
	var result error
	if retained.stage != nil {
		result = errors.Join(result, retained.stage.Close())
		retained.stage = nil
	}
	if retained.parent != nil {
		result = errors.Join(result, retained.parent.Close())
		retained.parent = nil
	}
	return result
}

func sameUnixStagedRetirementIdentity(left, right *unix.Stat_t) bool {
	return left != nil && right != nil && left.Dev == right.Dev && left.Ino == right.Ino
}
