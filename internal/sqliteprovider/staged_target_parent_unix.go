//go:build unix && !aix

package sqliteprovider

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type stagedTargetParentPlatform struct {
	directory *os.File
	identity  fileidentity.Identity
	stat      unix.Stat_t
	created   []stagedTargetParentUnixComponent
}

type stagedTargetParentUnixComponent struct {
	parent    *os.File
	directory *os.File
	leaf      string
	identity  fileidentity.Identity
	stat      unix.Stat_t
}

type stagedTargetParentUnixOps struct {
	lstat    func(string) (os.FileInfo, error)
	open     func(string, int, uint32) (int, error)
	openat   func(int, string, int, uint32) (int, error)
	mkdirat  func(int, string, uint32) error
	fchmod   func(int, uint32) error
	fsync    func(int) error
	fstat    func(int, *unix.Stat_t) error
	fstatat  func(int, string, *unix.Stat_t, int) error
	closeFD  func(int) error
	newFile  func(uintptr, string) *os.File
	opened   func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error)
	stat     func(*os.File) (os.FileInfo, error)
	close    func(*os.File) error
	readDir  func(*os.File, int) ([]os.DirEntry, error)
	unlinkat func(int, string, int) error
	renameAt func(int, string, int, string) error
}

func defaultStagedTargetParentUnixOps() stagedTargetParentUnixOps {
	return stagedTargetParentUnixOps{
		lstat: os.Lstat,
		open:  unix.Open,
		openat: func(directory int, path string, flags int, mode uint32) (int, error) {
			return unix.Openat(directory, path, flags, mode)
		},
		mkdirat:  unix.Mkdirat,
		fchmod:   unix.Fchmod,
		fsync:    unix.Fsync,
		fstat:    unix.Fstat,
		fstatat:  unix.Fstatat,
		closeFD:  unix.Close,
		newFile:  os.NewFile,
		opened:   fileidentity.Opened,
		stat:     func(file *os.File) (os.FileInfo, error) { return file.Stat() },
		close:    func(file *os.File) error { return file.Close() },
		readDir:  func(file *os.File, count int) ([]os.DirEntry, error) { return file.ReadDir(count) },
		unlinkat: unix.Unlinkat,
		renameAt: renameStagedRetirementNoReplace,
	}
}

func createRetainedStagedTargetParentPlatform(
	ctx context.Context,
	path string,
) (_ *stagedTargetParentPlatform, returnErr error) {
	return createRetainedStagedTargetParentPlatformWithOps(
		ctx,
		path,
		defaultStagedTargetParentUnixOps(),
	)
}

func createRetainedStagedTargetParentPlatformWithOps(
	ctx context.Context,
	path string,
	ops stagedTargetParentUnixOps,
) (_ *stagedTargetParentPlatform, returnErr error) {
	if ctx == nil || ops.lstat == nil || ops.open == nil || ops.openat == nil ||
		ops.mkdirat == nil || ops.fchmod == nil || ops.fsync == nil ||
		ops.fstat == nil || ops.fstatat == nil || ops.closeFD == nil ||
		ops.newFile == nil || ops.opened == nil || ops.stat == nil ||
		ops.close == nil || ops.readDir == nil || ops.unlinkat == nil ||
		ops.renameAt == nil {
		return nil, errors.New("SQLite staged target parent Unix context is unavailable")
	}
	ancestor := path
	missing := make([]string, 0, 4)
	for {
		info, statErr := ops.lstat(ancestor)
		if statErr == nil {
			if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("SQLite staged target parent Unix ancestor is unsafe")
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil, errors.New("SQLite staged target parent has no existing ancestor")
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	if len(missing) == 0 {
		return nil, errors.New(
			"SQLite staged target parent creation ownership is ambiguous",
		)
	}
	if len(missing) > maximumStagedTargetParentCreatedComponents {
		return nil, errors.New("SQLite staged target parent has too many missing components")
	}
	current, err := openTrustedProviderUnixDirectoryWithOps(ancestor, ops)
	if err != nil {
		return nil, err
	}
	platform := &stagedTargetParentPlatform{}
	fail := func(cause error) (*stagedTargetParentPlatform, error) {
		if len(platform.created) == 0 {
			return nil, errors.Join(cause, ops.close(current))
		}
		return nil, errors.Join(
			cause,
			closeRetainedStagedTargetParentPlatformWithOps(platform, true, ops),
		)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if cause := context.Cause(ctx); cause != nil {
			return fail(cause)
		}
		leaf := missing[index]
		if mkdirErr := ops.mkdirat(int(current.Fd()), leaf, 0o700); mkdirErr != nil {
			if errors.Is(mkdirErr, unix.EEXIST) {
				return fail(errors.New(
					"SQLite staged target parent creation ownership is ambiguous",
				))
			}
			return fail(mkdirErr)
		}
		var createdStat unix.Stat_t
		if statErr := ops.fstatat(
			int(current.Fd()),
			leaf,
			&createdStat,
			unix.AT_SYMLINK_NOFOLLOW,
		); statErr != nil {
			return fail(statErr)
		}
		if createdStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
			createdStat.Uid != uint32(os.Geteuid()) || createdStat.Mode&0o077 != 0 {
			return fail(errors.New(
				"SQLite staged target parent Unix created name is unsafe",
			))
		}
		platform.created = append(platform.created, stagedTargetParentUnixComponent{
			parent: current,
			leaf:   leaf,
			stat:   createdStat,
		})
		created := &platform.created[len(platform.created)-1]
		nextFD, openErr := ops.openat(
			int(current.Fd()),
			leaf,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
			0,
		)
		if openErr != nil {
			return fail(openErr)
		}
		next := ops.newFile(uintptr(nextFD), filepath.Join(ancestor, leaf))
		if next == nil {
			closeErr := ops.closeFD(nextFD)
			return fail(errors.Join(
				errors.New("SQLite staged target parent Unix handle is unavailable"),
				closeErr,
			))
		}
		created.directory = next
		var openedStat unix.Stat_t
		if statErr := ops.fstat(nextFD, &openedStat); statErr != nil {
			return fail(statErr)
		}
		if !sameUnixStagedTargetParentIdentity(&createdStat, &openedStat) {
			return fail(errors.New(
				"SQLite staged target parent Unix created name changed while opening",
			))
		}
		createdIdentity, objectType, identityErr := ops.opened(next)
		if identityErr != nil || objectType != fileidentity.ObjectTypeDirectory ||
			!createdIdentity.Valid() {
			return fail(errors.Join(
				errors.New("SQLite staged target parent Unix created identity is unavailable"),
				identityErr,
			))
		}
		created.identity = createdIdentity
		if chmodErr := ops.fchmod(nextFD, 0o700); chmodErr != nil {
			return fail(chmodErr)
		}
		if statErr := ops.fstat(nextFD, &createdStat); statErr != nil {
			return fail(statErr)
		}
		if validationErr := validateUnixProviderParentStat(
			&createdStat,
			uint32(os.Geteuid()),
		); validationErr != nil {
			return fail(validationErr)
		}
		created.stat = createdStat
		platform.directory = next
		platform.identity = createdIdentity
		platform.stat = createdStat
		if syncErr := ops.fsync(nextFD); syncErr != nil {
			return fail(syncErr)
		}
		if parentSyncErr := ops.fsync(int(current.Fd())); parentSyncErr != nil {
			return fail(parentSyncErr)
		}
		current = next
		ancestor = filepath.Join(ancestor, leaf)
	}
	fd := int(platform.directory.Fd())
	var stat unix.Stat_t
	if statErr := ops.fstat(fd, &stat); statErr != nil {
		return fail(statErr)
	}
	if validationErr := validateUnixProviderParentStat(
		&stat,
		uint32(os.Geteuid()),
	); validationErr != nil {
		return fail(validationErr)
	}
	identity, objectType, err := ops.opened(platform.directory)
	if err != nil || objectType != fileidentity.ObjectTypeDirectory || !identity.Valid() {
		return fail(errors.Join(
			errors.New("SQLite staged target parent Unix identity is unavailable"),
			err,
		))
	}
	platform.identity = identity
	platform.stat = stat
	platform.created[len(platform.created)-1].identity = identity
	platform.created[len(platform.created)-1].stat = stat
	if _, err := checkRetainedStagedTargetParentPlatformWithOps(
		ctx,
		path,
		nil,
		platform,
		ops,
	); err != nil {
		return fail(err)
	}
	return platform, nil
}

func openTrustedProviderUnixDirectoryWithOps(
	path string,
	ops stagedTargetParentUnixOps,
) (_ *os.File, returnErr error) {
	if !validProviderFilesystemPath(path) || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path || ops.open == nil || ops.openat == nil ||
		ops.fstat == nil || ops.closeFD == nil || ops.newFile == nil {
		return nil, errors.New("SQLite staged target parent Unix creation boundary is invalid")
	}
	fd, err := ops.open(
		string(os.PathSeparator),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed && fd >= 0 {
			returnErr = errors.Join(returnErr, ops.closeFD(fd))
		}
	}()
	protected := false
	var rootStat unix.Stat_t
	if err := ops.fstat(fd, &rootStat); err != nil {
		return nil, err
	}
	protected, trusted := trustedProviderUnixDirectory(rootStat, protected)
	if !trusted {
		return nil, errors.New("SQLite staged target parent Unix root is not trusted")
	}
	components := strings.Split(
		strings.TrimPrefix(path, string(os.PathSeparator)),
		string(os.PathSeparator),
	)
	for _, component := range components {
		if component == "" {
			continue
		}
		next, openErr := ops.openat(
			fd,
			component,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
			0,
		)
		if openErr != nil {
			return nil, openErr
		}
		var componentStat unix.Stat_t
		if err := ops.fstat(next, &componentStat); err != nil {
			return nil, errors.Join(err, ops.closeFD(next))
		}
		protected, trusted = trustedProviderUnixDirectory(componentStat, protected)
		if !trusted {
			return nil, errors.Join(
				errors.New("SQLite staged target parent Unix ancestor is not trusted"),
				ops.closeFD(next),
			)
		}
		closingFD := fd
		fd = -1
		if err := ops.closeFD(closingFD); err != nil {
			return nil, errors.Join(err, ops.closeFD(next))
		}
		fd = next
	}
	file := ops.newFile(uintptr(fd), path)
	if file == nil {
		return nil, errors.New("SQLite staged target parent Unix boundary handle is unavailable")
	}
	failed = false
	return file, nil
}

func checkRetainedStagedTargetParentPlatform(
	ctx context.Context,
	path string,
	observed os.FileInfo,
	platform *stagedTargetParentPlatform,
) (fileidentity.Identity, error) {
	return checkRetainedStagedTargetParentPlatformWithOps(
		ctx,
		path,
		observed,
		platform,
		defaultStagedTargetParentUnixOps(),
	)
}

func checkRetainedStagedTargetParentPlatformWithOps(
	ctx context.Context,
	path string,
	observed os.FileInfo,
	platform *stagedTargetParentPlatform,
	ops stagedTargetParentUnixOps,
) (result fileidentity.Identity, returnErr error) {
	if ctx == nil || platform == nil || platform.directory == nil ||
		!platform.identity.Valid() || ops.fstat == nil || ops.opened == nil ||
		ops.open == nil || ops.openat == nil || ops.closeFD == nil ||
		ops.newFile == nil || ops.stat == nil || ops.close == nil {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent Unix proof is unavailable",
		)
	}
	if cause := context.Cause(ctx); cause != nil {
		return fileidentity.Identity{}, cause
	}
	var retained unix.Stat_t
	if statErr := ops.fstat(int(platform.directory.Fd()), &retained); statErr != nil {
		return fileidentity.Identity{}, statErr
	}
	identity, objectType, err := ops.opened(platform.directory)
	if err != nil || identity != platform.identity ||
		objectType != fileidentity.ObjectTypeDirectory ||
		!sameUnixStagedTargetParentIdentity(&retained, &platform.stat) {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent retained Unix identity changed"),
			err,
		)
	}
	if validationErr := validateUnixProviderParentStat(
		&retained,
		uint32(os.Geteuid()),
	); validationErr != nil {
		return fileidentity.Identity{}, validationErr
	}
	named, err := openTrustedProviderUnixDirectoryWithOps(path, ops)
	if err != nil {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent named Unix identity changed"),
			err,
		)
	}
	defer func() { returnErr = errors.Join(returnErr, ops.close(named)) }()
	namedInfo, statErr := ops.stat(named)
	namedIdentity, namedType, namedIdentityErr := ops.opened(named)
	if statErr != nil || namedIdentityErr != nil || namedInfo == nil || !namedInfo.IsDir() ||
		namedType != fileidentity.ObjectTypeDirectory || namedIdentity != platform.identity {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent named Unix identity changed"),
			statErr,
			namedIdentityErr,
		)
	}
	if observed != nil && (!observed.IsDir() || observed.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(namedInfo, observed) || namedInfo.Mode() != observed.Mode()) {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent Unix observation changed",
		)
	}
	return identity, context.Cause(ctx)
}

func checkRetainedStagedTargetParentSoleStagePlatform(
	ctx context.Context,
	path string,
	stage string,
	stageInfo os.FileInfo,
	stageIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
) (result fileidentity.Identity, returnErr error) {
	return checkRetainedStagedTargetParentSoleStagePlatformWithOps(
		ctx,
		path,
		stage,
		stageInfo,
		stageIdentity,
		platform,
		defaultStagedTargetParentUnixOps(),
	)
}

func checkRetainedStagedTargetParentSoleStagePlatformWithOps(
	ctx context.Context,
	path string,
	stage string,
	stageInfo os.FileInfo,
	stageIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
	ops stagedTargetParentUnixOps,
) (result fileidentity.Identity, returnErr error) {
	return checkRetainedStagedTargetParentSoleEntryPlatformWithOps(
		ctx,
		path,
		stage,
		stageInfo,
		stageIdentity,
		platform,
		ops,
		"stage",
		true,
	)
}

func checkRetainedStagedTargetParentSoleInstalledPlatform(
	ctx context.Context,
	path string,
	target string,
	targetInfo os.FileInfo,
	targetIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
) (fileidentity.Identity, error) {
	return checkRetainedStagedTargetParentSoleEntryPlatformWithOps(
		ctx,
		path,
		target,
		targetInfo,
		targetIdentity,
		platform,
		defaultStagedTargetParentUnixOps(),
		"installed target",
		false,
	)
}

func checkRetainedStagedTargetParentSoleEntryPlatformWithOps(
	ctx context.Context,
	path string,
	entry string,
	entryInfo os.FileInfo,
	entryIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
	ops stagedTargetParentUnixOps,
	entryKind string,
	exactMetadata bool,
) (result fileidentity.Identity, returnErr error) {
	if !entryIdentity.Valid() || entryInfo == nil ||
		(entryKind != "stage" && entryKind != "target" &&
			entryKind != "installed target") ||
		ops.openat == nil || ops.newFile == nil || ops.closeFD == nil ||
		ops.close == nil || ops.readDir == nil || ops.fstat == nil ||
		ops.opened == nil {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent Unix entry proof is unavailable",
		)
	}
	result, err := checkRetainedStagedTargetParentPlatformWithOps(
		ctx,
		path,
		nil,
		platform,
		ops,
	)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	fd, err := ops.openat(
		int(platform.directory.Fd()),
		".",
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	directory := ops.newFile(uintptr(fd), path)
	if directory == nil {
		closeErr := ops.closeFD(fd)
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent Unix inventory handle is unavailable"),
			closeErr,
		)
	}
	defer func() { returnErr = errors.Join(returnErr, ops.close(directory)) }()
	entries, err := ops.readDir(directory, 2)
	if err != nil && !errors.Is(err, io.EOF) {
		return fileidentity.Identity{}, err
	}
	leaf := filepath.Base(entry)
	if len(entries) != 1 || entries[0].Name() != leaf {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent does not contain exactly its pinned " + entryKind,
		)
	}
	entryFD, openErr := ops.openat(
		int(platform.directory.Fd()),
		leaf,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if openErr != nil {
		return fileidentity.Identity{}, openErr
	}
	entryFile := ops.newFile(uintptr(entryFD), entry)
	if entryFile == nil {
		closeErr := ops.closeFD(entryFD)
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent Unix entry handle is unavailable"),
			closeErr,
		)
	}
	defer func() { returnErr = errors.Join(returnErr, ops.close(entryFile)) }()
	var entryStat unix.Stat_t
	statErr := ops.fstat(entryFD, &entryStat)
	openedInfo, infoErr := ops.stat(entryFile)
	openedIdentity, objectType, identityErr := ops.opened(entryFile)
	if statErr != nil || infoErr != nil || identityErr != nil ||
		openedIdentity != entryIdentity || objectType != fileidentity.ObjectTypeRegular ||
		!sameUnixProviderFileInfoAndStat(entryInfo, &entryStat) ||
		exactMetadata && !sameValidatedReplacementMetadata(entryInfo, openedInfo) {
		return fileidentity.Identity{}, errors.Join(
			errors.New(
				"SQLite staged target parent contains a different "+entryKind+" identity",
			),
			statErr,
			infoErr,
			identityErr,
		)
	}
	if validationErr := validateUnixProviderLiveStat(
		&entryStat,
		uint32(os.Geteuid()),
		true,
	); validationErr != nil {
		return fileidentity.Identity{}, validationErr
	}
	finalIdentity, err := checkRetainedStagedTargetParentPlatformWithOps(
		ctx,
		path,
		nil,
		platform,
		ops,
	)
	if err != nil || finalIdentity != result {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent changed during inventory"),
			err,
		)
	}
	return result, nil
}

func closeRetainedStagedTargetParentPlatform(
	platform *stagedTargetParentPlatform,
	rollback bool,
) error {
	return closeRetainedStagedTargetParentPlatformWithOps(
		platform,
		rollback,
		defaultStagedTargetParentUnixOps(),
	)
}

func closeRetainedStagedTargetParentPlatformWithOps(
	platform *stagedTargetParentPlatform,
	rollback bool,
	ops stagedTargetParentUnixOps,
) (returnErr error) {
	if platform == nil {
		return nil
	}
	closeFile := ops.close
	if closeFile == nil {
		closeFile = func(file *os.File) error { return file.Close() }
	}
	ops.close = closeFile
	cleanupAvailable := ops.fstat != nil && ops.fstatat != nil && ops.openat != nil &&
		ops.opened != nil && ops.newFile != nil && ops.closeFD != nil &&
		ops.readDir != nil && ops.unlinkat != nil && ops.fsync != nil
	if rollback && !cleanupAvailable {
		returnErr = errors.Join(
			returnErr,
			errors.New("SQLite staged target parent Unix cleanup operations are unavailable"),
		)
	}
	if rollback && cleanupAvailable {
		for index := len(platform.created) - 1; index >= 0; index-- {
			if err := rollbackStagedTargetParentUnixComponent(
				&platform.created[index],
				ops,
			); err != nil {
				returnErr = errors.Join(returnErr, err)
				break
			}
		}
	}
	platform.directory = nil
	for index := len(platform.created) - 1; index >= 0; index-- {
		component := &platform.created[index]
		if component.directory != nil {
			directory := component.directory
			component.directory = nil
			returnErr = errors.Join(returnErr, closeFile(directory))
		}
	}
	if len(platform.created) > 0 && platform.created[0].parent != nil {
		anchor := platform.created[0].parent
		platform.created[0].parent = nil
		returnErr = errors.Join(returnErr, closeFile(anchor))
	}
	for index := range platform.created {
		platform.created[index].parent = nil
	}
	platform.created = nil
	platform.identity = fileidentity.Identity{}
	platform.stat = unix.Stat_t{}
	return returnErr
}

func rollbackStagedTargetParentUnixComponent(
	component *stagedTargetParentUnixComponent,
	ops stagedTargetParentUnixOps,
) (returnErr error) {
	if component == nil || component.parent == nil || component.leaf == "" ||
		filepath.Base(component.leaf) != component.leaf {
		return errors.New("SQLite staged target parent Unix rollback proof is unavailable")
	}
	if component.directory == nil || !component.identity.Valid() {
		return unlinkStagedTargetParentUnixComponent(component, ops)
	}
	var retained unix.Stat_t
	retainedErr := ops.fstat(int(component.directory.Fd()), &retained)
	retainedIdentity, retainedType, identityErr := ops.opened(component.directory)
	if retainedErr != nil || identityErr != nil ||
		retainedIdentity != component.identity ||
		retainedType != fileidentity.ObjectTypeDirectory ||
		!sameUnixStagedTargetParentIdentity(&retained, &component.stat) {
		return errors.Join(
			errors.New("SQLite staged target parent Unix rollback identity changed"),
			retainedErr,
			identityErr,
		)
	}
	if err := validateUnixProviderParentStat(&retained, uint32(os.Geteuid())); err != nil {
		return err
	}
	namedFD, err := ops.openat(
		int(component.parent.Fd()),
		component.leaf,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return errors.Join(
			errors.New("SQLite staged target parent Unix rollback name changed"),
			err,
		)
	}
	named := ops.newFile(uintptr(namedFD), component.leaf)
	if named == nil {
		return errors.Join(
			errors.New("SQLite staged target parent Unix rollback handle is unavailable"),
			ops.closeFD(namedFD),
		)
	}
	namedClosed := false
	defer func() {
		if !namedClosed {
			returnErr = errors.Join(returnErr, ops.close(named))
		}
	}()
	var namedStat unix.Stat_t
	namedStatErr := ops.fstat(namedFD, &namedStat)
	namedIdentity, namedType, namedIdentityErr := ops.opened(named)
	if namedStatErr != nil || namedIdentityErr != nil ||
		namedIdentity != component.identity ||
		namedType != fileidentity.ObjectTypeDirectory ||
		!sameUnixStagedTargetParentIdentity(&namedStat, &component.stat) {
		return errors.Join(
			errors.New("SQLite staged target parent Unix rollback name was substituted"),
			namedStatErr,
			namedIdentityErr,
		)
	}
	entries, readErr := ops.readDir(named, 1)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if len(entries) != 0 {
		return errors.New("SQLite staged target parent Unix rollback directory is not empty")
	}
	if err := ops.close(named); err != nil {
		namedClosed = true
		return err
	}
	namedClosed = true
	return unlinkStagedTargetParentUnixComponent(component, ops)
}

func unlinkStagedTargetParentUnixComponent(
	component *stagedTargetParentUnixComponent,
	ops stagedTargetParentUnixOps,
) error {
	var after unix.Stat_t
	if err := ops.fstatat(
		int(component.parent.Fd()),
		component.leaf,
		&after,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return err
	}
	if !sameUnixStagedTargetParentIdentity(&after, &component.stat) ||
		after.Mode&unix.S_IFMT != unix.S_IFDIR ||
		after.Uid != uint32(os.Geteuid()) || after.Mode&0o077 != 0 {
		return errors.New("SQLite staged target parent Unix rollback name was substituted")
	}
	if err := ops.unlinkat(
		int(component.parent.Fd()),
		component.leaf,
		unix.AT_REMOVEDIR,
	); err != nil {
		return err
	}
	if err := ops.fsync(int(component.parent.Fd())); err != nil {
		return err
	}
	if err := ops.fstatat(
		int(component.parent.Fd()),
		component.leaf,
		&after,
		unix.AT_SYMLINK_NOFOLLOW,
	); !errors.Is(err, unix.ENOENT) {
		if err == nil {
			err = errors.New("SQLite staged target parent Unix rollback name reappeared")
		}
		return err
	}
	return nil
}

func replaceRetainedStagedTargetParentStagePlatform(
	ctx context.Context,
	path string,
	stage string,
	target string,
	stageInfo os.FileInfo,
	stageFile *os.File,
	stageIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
) (bool, error) {
	return replaceRetainedStagedTargetParentStagePlatformWithOps(
		ctx,
		path,
		stage,
		target,
		stageInfo,
		stageFile,
		stageIdentity,
		platform,
		defaultStagedTargetParentUnixOps(),
	)
}

func replaceRetainedStagedTargetParentStagePlatformWithOps(
	ctx context.Context,
	path string,
	stage string,
	target string,
	stageInfo os.FileInfo,
	stageFile *os.File,
	stageIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
	ops stagedTargetParentUnixOps,
) (bool, error) {
	if ctx == nil || platform == nil || platform.directory == nil || stageFile == nil ||
		!stageIdentity.Valid() ||
		filepath.Dir(stage) != path || filepath.Dir(target) != path ||
		ops.stat == nil || ops.opened == nil || ops.renameAt == nil ||
		ops.fstatat == nil || ops.fsync == nil || ops.openat == nil ||
		ops.newFile == nil || ops.closeFD == nil || ops.close == nil {
		return false, errors.New("SQLite retained target-parent Unix replacement is invalid")
	}
	if cause := context.Cause(ctx); cause != nil {
		return false, cause
	}
	openedStage, err := ops.stat(stageFile)
	openedIdentity, objectType, identityErr := ops.opened(stageFile)
	if err != nil || identityErr != nil || openedStage == nil ||
		openedIdentity != stageIdentity || objectType != fileidentity.ObjectTypeRegular ||
		!sameValidatedReplacementMetadata(stageInfo, openedStage) {
		return false, errors.Join(
			errors.New("SQLite retained target-parent Unix stage handle changed"),
			err,
			identityErr,
		)
	}
	parentFD := int(platform.directory.Fd())
	stageLeaf := filepath.Base(stage)
	targetLeaf := filepath.Base(target)
	renameErr := ops.renameAt(
		parentFD,
		stageLeaf,
		parentFD,
		targetLeaf,
	)
	stageMatches, targetMatches, postErr := retainedTargetParentUnixRenameState(
		parentFD,
		stageLeaf,
		targetLeaf,
		stageInfo,
		stageIdentity,
		ops,
	)
	if renameErr != nil {
		if stageMatches && !targetMatches && postErr == nil {
			return false, renameErr
		}
		return true, errors.Join(
			errors.New("SQLite retained target-parent Unix rename outcome is ambiguous"),
			renameErr,
			postErr,
		)
	}
	if postErr != nil || stageMatches || !targetMatches {
		return true, errors.Join(
			errors.New("SQLite retained target-parent Unix rename postcondition failed"),
			postErr,
		)
	}
	if err := ops.fsync(parentFD); err != nil {
		return true, err
	}
	if err := requireRetainedTargetParentUnixSidecarsMissingWithOps(
		parentFD,
		stageLeaf,
		targetLeaf,
		ops,
	); err != nil {
		return true, err
	}
	if _, err := checkRetainedStagedTargetParentSoleEntryPlatformWithOps(
		ctx,
		path,
		target,
		stageInfo,
		stageIdentity,
		platform,
		ops,
		"target",
		true,
	); err != nil {
		return true, err
	}
	return true, nil
}

func retainedTargetParentUnixRenameState(
	parentFD int,
	stageLeaf string,
	targetLeaf string,
	expected os.FileInfo,
	expectedIdentity fileidentity.Identity,
	ops stagedTargetParentUnixOps,
) (stageMatches bool, targetMatches bool, returnErr error) {
	inspect := func(leaf string) (_ bool, _ bool, inspectErr error) {
		fd, err := ops.openat(
			parentFD,
			leaf,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
			0,
		)
		if errors.Is(err, unix.ENOENT) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		file := ops.newFile(uintptr(fd), leaf)
		if file == nil {
			return true, false, errors.Join(
				errors.New(
					"SQLite retained target-parent Unix rename-state handle is unavailable",
				),
				ops.closeFD(fd),
			)
		}
		defer func() { inspectErr = errors.Join(inspectErr, ops.close(file)) }()
		info, statErr := ops.stat(file)
		identity, objectType, identityErr := ops.opened(file)
		if statErr != nil || identityErr != nil {
			return true, false, errors.Join(statErr, identityErr)
		}
		return true,
			identity == expectedIdentity && objectType == fileidentity.ObjectTypeRegular &&
				sameValidatedReplacementMetadata(expected, info),
			nil
	}
	stageExists, stageMatches, stageErr := inspect(stageLeaf)
	targetExists, targetMatches, targetErr := inspect(targetLeaf)
	if stageExists && !stageMatches {
		stageErr = errors.Join(
			stageErr,
			errors.New("SQLite retained target-parent Unix stage name was substituted"),
		)
	}
	if targetExists && !targetMatches {
		targetErr = errors.Join(
			targetErr,
			errors.New("SQLite retained target-parent Unix target name was substituted"),
		)
	}
	return stageExists && stageMatches, targetExists && targetMatches, errors.Join(stageErr, targetErr)
}

func requireRetainedTargetParentUnixSidecarsMissing(
	parentFD int,
	stageLeaf string,
	targetLeaf string,
) error {
	return requireRetainedTargetParentUnixSidecarsMissingWithOps(
		parentFD,
		stageLeaf,
		targetLeaf,
		defaultStagedTargetParentUnixOps(),
	)
}

func requireRetainedTargetParentUnixSidecarsMissingWithOps(
	parentFD int,
	stageLeaf string,
	targetLeaf string,
	ops stagedTargetParentUnixOps,
) error {
	for _, leaf := range []string{
		stageLeaf + "-wal",
		stageLeaf + "-shm",
		stageLeaf + "-journal",
		targetLeaf + "-wal",
		targetLeaf + "-shm",
		targetLeaf + "-journal",
	} {
		var stat unix.Stat_t
		err := ops.fstatat(parentFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return err
		}
		return errors.New("SQLite retained target-parent Unix sidecar appeared")
	}
	return nil
}

func sameUnixStagedTargetParentIdentity(left, right *unix.Stat_t) bool {
	return left != nil && right != nil && left.Dev == right.Dev && left.Ino == right.Ino
}
