package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

const maximumStagedTargetParentCreatedComponents = 128

// retainedStagedTargetParent proves that this provider invocation atomically
// created and continuously retained the exact target parent. Existing parents
// deliberately produce no capability and remain on the ordinary live-source
// verification path.
type retainedStagedTargetParent struct {
	sync.Mutex
	path          string
	platform      *stagedTargetParentPlatform
	sealed        os.FileInfo
	stagePath     string
	stageInfo     os.FileInfo
	stageIdentity fileidentity.Identity
	installedPath string
	installedInfo os.FileInfo
	installedID   fileidentity.Identity
	installed     bool
	closed        bool
}

func prepareRetainedStagedTargetParent(
	ctx context.Context,
	path string,
) (*retainedStagedTargetParent, error) {
	if ctx == nil || !validProviderFilesystemPath(path) ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("SQLite staged target parent input is invalid")
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, cause
	}
	info, err := os.Lstat(path)
	if err == nil {
		if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("SQLite staged target parent is unsafe")
		}
		if secureErr := EnsurePrivateDirectory(path); secureErr != nil {
			return nil, secureErr
		}
		return nil, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	platform, err := createRetainedStagedTargetParentPlatform(ctx, path)
	if err != nil {
		return nil, err
	}
	return &retainedStagedTargetParent{path: path, platform: platform}, nil
}

func (parent *retainedStagedTargetParent) Check(
	ctx context.Context,
	path string,
	observed os.FileInfo,
) (fileidentity.Identity, error) {
	if parent == nil || ctx == nil || filepath.Clean(path) != path {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent check is unavailable",
		)
	}
	parent.Lock()
	defer parent.Unlock()
	if parent.closed || parent.platform == nil || parent.installed || path != parent.path {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent check is closed or mismatched",
		)
	}
	if cause := context.Cause(ctx); cause != nil {
		return fileidentity.Identity{}, cause
	}
	return checkRetainedStagedTargetParentPlatform(
		ctx,
		path,
		observed,
		parent.platform,
	)
}

func (parent *retainedStagedTargetParent) CheckSoleStage(
	ctx context.Context,
	path string,
	stage string,
	stageInfo os.FileInfo,
) (fileidentity.Identity, error) {
	return parent.checkSoleStage(
		ctx,
		path,
		stage,
		stageInfo,
		fileidentity.Identity{},
		false,
	)
}

func (parent *retainedStagedTargetParent) SealSoleStage(
	ctx context.Context,
	path string,
	stage string,
	stageInfo os.FileInfo,
	stageIdentity fileidentity.Identity,
) (fileidentity.Identity, error) {
	return parent.checkSoleStage(ctx, path, stage, stageInfo, stageIdentity, true)
}

func (parent *retainedStagedTargetParent) checkSoleStage(
	ctx context.Context,
	path string,
	stage string,
	stageInfo os.FileInfo,
	stageIdentity fileidentity.Identity,
	seal bool,
) (fileidentity.Identity, error) {
	return parent.checkSoleStageWithLstat(
		ctx, path, stage, stageInfo, stageIdentity, seal, os.Lstat,
	)
}

func (parent *retainedStagedTargetParent) checkSoleStageWithLstat(
	ctx context.Context,
	path string,
	stage string,
	stageInfo os.FileInfo,
	stageIdentity fileidentity.Identity,
	seal bool,
	lstat func(string) (os.FileInfo, error),
) (fileidentity.Identity, error) {
	if parent == nil || ctx == nil || filepath.Clean(path) != path ||
		filepath.Dir(stage) != path || filepath.Clean(stage) != stage || stageInfo == nil {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent inventory check is unavailable",
		)
	}
	parent.Lock()
	defer parent.Unlock()
	if parent.closed || parent.platform == nil || parent.installed || path != parent.path {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent inventory check is closed or mismatched",
		)
	}
	if cause := context.Cause(ctx); cause != nil {
		return fileidentity.Identity{}, cause
	}
	if seal == (parent.sealed != nil) {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent inventory seal state is invalid",
		)
	}
	if seal {
		if !stageIdentity.Valid() {
			return fileidentity.Identity{}, errors.New(
				"SQLite staged target parent stage identity is unavailable",
			)
		}
	} else {
		stageIdentity = parent.stageIdentity
		if !stageIdentity.Valid() || stage != parent.stagePath ||
			!sameValidatedReplacementMetadata(parent.stageInfo, stageInfo) {
			return fileidentity.Identity{}, errors.New(
				"SQLite staged target parent inventory seal changed",
			)
		}
	}
	before, err := lstat(path)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	if parent.sealed != nil && !sameStagedTargetParentMetadata(parent.sealed, before) {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent metadata changed after sealing",
		)
	}
	identity, err := checkRetainedStagedTargetParentSoleStagePlatform(
		ctx,
		path,
		stage,
		stageInfo,
		stageIdentity,
		parent.platform,
	)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	after, err := lstat(path)
	if err != nil || !sameStagedTargetParentMetadata(before, after) {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent metadata changed during inventory"),
			err,
		)
	}
	if seal {
		parent.sealed = after
		parent.stagePath = stage
		parent.stageInfo = stageInfo
		parent.stageIdentity = stageIdentity
	}
	return identity, context.Cause(ctx)
}

func (parent *retainedStagedTargetParent) Close() error {
	if parent == nil {
		return nil
	}
	parent.Lock()
	defer parent.Unlock()
	if parent.closed {
		return nil
	}
	parent.closed = true
	platform := parent.platform
	rollback := !parent.installed
	parent.platform = nil
	parent.sealed = nil
	parent.stagePath = ""
	parent.stageInfo = nil
	parent.stageIdentity = fileidentity.Identity{}
	parent.installedPath = ""
	parent.installedInfo = nil
	parent.installedID = fileidentity.Identity{}
	if platform == nil {
		return nil
	}
	return closeRetainedStagedTargetParentPlatform(platform, rollback)
}

func (parent *retainedStagedTargetParent) ReplaceStage(
	ctx context.Context,
	stage string,
	target string,
	stageInfo os.FileInfo,
	stageFile *os.File,
) (bool, error) {
	if parent == nil || ctx == nil || stageInfo == nil || stageFile == nil {
		return false, errors.New("SQLite staged target parent replacement is unavailable")
	}
	parent.Lock()
	defer parent.Unlock()
	if parent.closed || parent.platform == nil || parent.sealed == nil ||
		!parent.stageIdentity.Valid() ||
		stage != parent.stagePath ||
		!sameValidatedReplacementMetadata(parent.stageInfo, stageInfo) ||
		filepath.Dir(stage) != parent.path || filepath.Dir(target) != parent.path ||
		filepath.Clean(stage) != stage || filepath.Clean(target) != target {
		return false, errors.New("SQLite staged target parent replacement is invalid")
	}
	if cause := context.Cause(ctx); cause != nil {
		return false, cause
	}
	current, err := os.Lstat(parent.path)
	if err != nil || !sameStagedTargetParentMetadata(parent.sealed, current) {
		return false, errors.Join(
			errors.New("SQLite staged target parent changed before replacement"),
			err,
		)
	}
	if _, err := checkRetainedStagedTargetParentSoleStagePlatform(
		ctx,
		parent.path,
		stage,
		stageInfo,
		parent.stageIdentity,
		parent.platform,
	); err != nil {
		return false, err
	}
	complete, replaceErr := replaceRetainedStagedTargetParentStagePlatform(
		ctx,
		parent.path,
		stage,
		target,
		stageInfo,
		stageFile,
		parent.stageIdentity,
		parent.platform,
	)
	if complete {
		parent.sealed = nil
		parent.installedPath = target
		parent.installedInfo = parent.stageInfo
		parent.installedID = parent.stageIdentity
		parent.stagePath = ""
		parent.stageInfo = nil
		parent.stageIdentity = fileidentity.Identity{}
		parent.installed = true
	}
	return complete, replaceErr
}

func (parent *retainedStagedTargetParent) CheckSoleInstalledTarget(
	ctx context.Context,
	target string,
) (fileidentity.Identity, error) {
	if parent == nil || ctx == nil || filepath.Clean(target) != target {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent installed-target check is unavailable",
		)
	}
	parent.Lock()
	defer parent.Unlock()
	if parent.closed || parent.platform == nil || !parent.installed ||
		target != parent.installedPath || filepath.Dir(target) != parent.path ||
		parent.installedInfo == nil || !parent.installedID.Valid() {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent installed-target proof is unavailable",
		)
	}
	if cause := context.Cause(ctx); cause != nil {
		return fileidentity.Identity{}, cause
	}
	return checkRetainedStagedTargetParentSoleInstalledPlatform(
		ctx,
		parent.path,
		parent.installedPath,
		parent.installedInfo,
		parent.installedID,
		parent.platform,
	)
}

func sameStagedTargetParentMetadata(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.IsDir() && right.IsDir() &&
		left.Mode()&os.ModeSymlink == 0 && right.Mode()&os.ModeSymlink == 0 &&
		os.SameFile(left, right) && left.Mode() == right.Mode() &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}
