package sqliteprovider

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	maximumImmutableGenerationMemberBytes = int64(8 << 30)
	maximumImmutableGenerationBytes       = int64(32 << 30)
)

var errImmutableGenerationSourceContract = errors.New(
	"immutable SQLite generation source contract was violated",
)

type immutableGenerationCopyOps struct {
	afterSourceOpen       func(string) error
	beforeDestinationOpen func(int, string) error
	retain                func(context.Context, string) (*retainedStagedGeneration, error)
	remove                func(string) error
	syncDirectory         func(string) error
}

type immutableStageWriteOps struct {
	open     func(*os.Root, string) (*os.File, error)
	stat     func(*os.File) (os.FileInfo, error)
	chmod    func(*os.File, os.FileMode) error
	secure   func(string) error
	lstat    func(string) (os.FileInfo, error)
	seek     func(*os.File, int64, int) (int64, error)
	copy     func(context.Context, context.Context, io.Writer, io.Reader, int64) ([sha256.Size]byte, int64, error)
	sync     func(*os.File) error
	hash     func(context.Context, context.Context, *os.File, int64) ([sha256.Size]byte, int64, error)
	validate func(string, os.FileInfo) error
	retain   func(context.Context, string) (*retainedStagedGeneration, error)
}

func defaultImmutableGenerationCopyOps() immutableGenerationCopyOps {
	return immutableGenerationCopyOps{
		afterSourceOpen:       func(string) error { return nil },
		beforeDestinationOpen: func(int, string) error { return nil },
		remove:                os.Remove,
		syncDirectory:         syncStagedMigrationDirectory,
	}
}

func defaultImmutableStageWriteOps() immutableStageWriteOps {
	return immutableStageWriteOps{
		open: func(root *os.Root, name string) (*os.File, error) {
			return root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		},
		stat:     func(file *os.File) (os.FileInfo, error) { return file.Stat() },
		chmod:    func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		secure:   secureProviderFile,
		lstat:    os.Lstat,
		seek:     func(file *os.File, offset int64, whence int) (int64, error) { return file.Seek(offset, whence) },
		copy:     copyImmutableGenerationBytes,
		sync:     func(file *os.File) error { return file.Sync() },
		hash:     hashImmutableGenerationFile,
		validate: validateImmutableGenerationMember,
	}
}

type immutableGenerationSnapshot struct {
	path  string
	infos [4]os.FileInfo
}

type immutableGenerationOpenMember struct {
	index  int
	path   string
	name   string
	info   os.FileInfo
	file   *os.File
	digest [sha256.Size]byte
}

type immutableStageMember struct {
	index              int
	path               string
	info               os.FileInfo
	retained           *retainedStagedGeneration
	retentionAttempted bool
}

// copyImmutableGenerationToStage invokes source exactly once and copies the
// complete raw generation into stage without asking SQLite to open source.
// stage must be an unused provider-owned name in a private directory.
func copyImmutableGenerationToStage(
	ctx context.Context,
	source ImmutableGenerationSource,
	stage string,
) (sourceExists bool, returnErr error) {
	sourceExists, retainedMain, returnErr := copyImmutableGenerationToRetainedStage(
		ctx, source, stage,
	)
	if retainedMain != nil {
		returnErr = errors.Join(returnErr, retainedMain.Close())
	}
	return sourceExists, returnErr
}

func copyImmutableGenerationToRetainedStage(
	ctx context.Context,
	source ImmutableGenerationSource,
	stage string,
) (sourceExists bool, retainedMain *retainedStagedGeneration, returnErr error) {
	ops := defaultImmutableGenerationCopyOps()
	ops.retain = retainStagedGeneration
	return copyImmutableGenerationToRetainedStageWithOps(
		ctx, source, stage, ops,
	)
}

func copyImmutableGenerationToStageWithOps(
	ctx context.Context,
	source ImmutableGenerationSource,
	stage string,
	ops immutableGenerationCopyOps,
) (sourceExists bool, returnErr error) {
	sourceExists, retainedMain, returnErr := copyImmutableGenerationToRetainedStageWithOps(
		ctx, source, stage, ops,
	)
	if retainedMain != nil {
		returnErr = errors.Join(returnErr, retainedMain.Close())
	}
	return sourceExists, returnErr
}

func copyImmutableGenerationToRetainedStageWithOps(
	ctx context.Context,
	source ImmutableGenerationSource,
	stage string,
	ops immutableGenerationCopyOps,
) (sourceExists bool, retainedMain *retainedStagedGeneration, returnErr error) {
	if ctx == nil || !source.storeID.Valid() || source.use == nil ||
		!validImmutableGenerationPath(stage) ||
		ops.afterSourceOpen == nil || ops.beforeDestinationOpen == nil ||
		ops.remove == nil || ops.syncDirectory == nil {
		return false, nil, errors.New("immutable SQLite generation copy input is invalid")
	}
	if cause := context.Cause(ctx); cause != nil {
		return false, nil, cause
	}
	if err := EnsurePrivateDirectory(filepath.Dir(stage)); err != nil {
		return false, nil, fmt.Errorf("prepare immutable SQLite generation stage: %w", err)
	}
	if err := requireUnusedImmutableStage(stage); err != nil {
		return false, nil, err
	}

	created := make([]immutableStageMember, 0, 4)
	completed := false
	defer func() {
		if completed {
			return
		}
		returnErr = errors.Join(
			returnErr,
			cleanupImmutableStage(created, filepath.Dir(stage), ops),
		)
	}()

	invokeErr := invokeImmutableGenerationSource(
		ctx,
		source,
		func(sourceCtx context.Context, sourcePath string) error {
			var copyErr error
			sourceExists, copyErr = copyImmutableGeneration(
				ctx, sourceCtx, sourcePath, stage, &created, ops,
			)
			return copyErr
		},
	)
	if invokeErr != nil {
		return false, nil, invokeErr
	}
	if cause := context.Cause(ctx); cause != nil {
		return false, nil, cause
	}
	retainedMain, closeErr := detachImmutableStageRetentions(
		ctx, created, sourceExists, ops.retain != nil,
	)
	if closeErr != nil {
		return false, nil, closeErr
	}
	completed = true
	return sourceExists, retainedMain, nil
}

func invokeImmutableGenerationSource(
	ctx context.Context,
	source ImmutableGenerationSource,
	operation func(context.Context, string) error,
) error {
	if ctx == nil || !source.storeID.Valid() || source.use == nil || operation == nil {
		return errors.Join(
			errImmutableGenerationSourceContract,
			errors.New("immutable SQLite generation source is unavailable"),
		)
	}
	type invocationState struct {
		sync.Mutex
		wait        sync.WaitGroup
		active      bool
		calls       int
		inFlight    bool
		callbackErr error
	}
	state := &invocationState{active: true}
	callback := func(callbackCtx context.Context, path string) (returnErr error) {
		state.Lock()
		state.calls++
		call := state.calls
		if !state.active || call != 1 {
			state.Unlock()
			return errors.Join(
				errImmutableGenerationSourceContract,
				errors.New("immutable SQLite generation source callback is not single-use"),
			)
		}
		state.inFlight = true
		state.wait.Add(1)
		state.Unlock()

		defer func() {
			if recovered := recover(); recovered != nil {
				returnErr = errors.Join(
					errImmutableGenerationSourceContract,
					errors.New("immutable SQLite generation source callback panicked"),
				)
			}
			state.Lock()
			state.callbackErr = returnErr
			state.inFlight = false
			state.Unlock()
			state.wait.Done()
		}()
		return operation(callbackCtx, path)
	}

	sourceErr := callImmutableGenerationSource(ctx, source, callback)
	state.Lock()
	state.active = false
	calls := state.calls
	returnedDuringCallback := state.inFlight
	state.Unlock()
	state.wait.Wait()
	state.Lock()
	callbackErr := state.callbackErr
	state.Unlock()

	var contractErr error
	if calls != 1 {
		contractErr = errors.Join(
			errImmutableGenerationSourceContract,
			fmt.Errorf("immutable SQLite generation source callback count is %d, want 1", calls),
		)
	}
	if returnedDuringCallback {
		contractErr = errors.Join(
			contractErr,
			errImmutableGenerationSourceContract,
			errors.New("immutable SQLite generation source returned before its callback"),
		)
	}
	return errors.Join(sourceErr, callbackErr, contractErr)
}

func callImmutableGenerationSource(
	ctx context.Context,
	source ImmutableGenerationSource,
	callback func(context.Context, string) error,
) (returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = errors.Join(
				errImmutableGenerationSourceContract,
				errors.New("immutable SQLite generation source panicked"),
			)
		}
	}()
	return source.use(ctx, callback)
}

func copyImmutableGeneration(
	parentCtx context.Context,
	sourceCtx context.Context,
	sourcePath string,
	stage string,
	created *[]immutableStageMember,
	ops immutableGenerationCopyOps,
) (exists bool, returnErr error) {
	if sourceCtx == nil || created == nil || !validImmutableGenerationPath(sourcePath) {
		return false, errors.New("immutable SQLite generation source path is invalid")
	}
	if err := immutableGenerationContextError(parentCtx, sourceCtx); err != nil {
		return false, err
	}
	sourceKey, sourceKeyErr := inspectedPoolKey(sourcePath)
	stageKey, stageKeyErr := inspectedPoolKey(stage)
	if sourceKeyErr != nil || stageKeyErr != nil || sourceKey == stageKey {
		return false, errors.Join(
			errors.New("immutable SQLite generation source must differ from its stage"),
			sourceKeyErr,
			stageKeyErr,
		)
	}
	if err := validateProviderAncestors(sourcePath); err != nil {
		return false, fmt.Errorf("validate immutable SQLite generation ancestors: %w", err)
	}

	snapshot, err := captureImmutableGeneration(sourcePath)
	if err != nil {
		return false, err
	}
	if snapshot.infos[0] == nil {
		return false, revalidateImmutableGeneration(sourceCtx, snapshot, nil)
	}

	sourceRoot, openRootErr := os.OpenRoot(filepath.Dir(sourcePath))
	if openRootErr != nil {
		return false, fmt.Errorf("open immutable SQLite generation root: %w", openRootErr)
	}
	defer func() { returnErr = errors.Join(returnErr, sourceRoot.Close()) }()

	opened, err := openImmutableGenerationMembers(
		parentCtx, sourceCtx, sourceRoot, snapshot,
	)
	if err != nil {
		return false, err
	}
	defer func() {
		for index := len(opened) - 1; index >= 0; index-- {
			returnErr = errors.Join(returnErr, opened[index].file.Close())
		}
	}()
	if hookErr := ops.afterSourceOpen(sourcePath); hookErr != nil {
		return false, hookErr
	}
	if revalidateErr := revalidateImmutableGeneration(sourceCtx, snapshot, opened); revalidateErr != nil {
		return false, revalidateErr
	}

	destinationRoot, err := os.OpenRoot(filepath.Dir(stage))
	if err != nil {
		return false, fmt.Errorf("open immutable SQLite stage root: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, destinationRoot.Close()) }()

	for index := range opened {
		if err := immutableGenerationContextError(parentCtx, sourceCtx); err != nil {
			return false, err
		}
		destinationPath := stage + immutableGenerationSuffix(opened[index].index)
		if err := ops.beforeDestinationOpen(opened[index].index, destinationPath); err != nil {
			return false, err
		}
		writeOps := defaultImmutableStageWriteOps()
		writeOps.retain = ops.retain
		member, copyErr := writeImmutableStageMemberWithOps(
			parentCtx,
			sourceCtx,
			destinationRoot,
			filepath.Base(destinationPath),
			destinationPath,
			&opened[index],
			writeOps,
		)
		if member.info != nil {
			*created = append(*created, member)
		}
		if copyErr != nil {
			return false, copyErr
		}
	}
	if err := revalidateImmutableGeneration(sourceCtx, snapshot, opened); err != nil {
		return false, err
	}
	if err := validateCopiedImmutableStage(parentCtx, sourceCtx, stage, opened); err != nil {
		return false, err
	}
	if err := ops.syncDirectory(filepath.Dir(stage)); err != nil {
		return false, fmt.Errorf("sync immutable SQLite stage directory: %w", err)
	}
	return true, nil
}

func captureImmutableGeneration(path string) (immutableGenerationSnapshot, error) {
	result := immutableGenerationSnapshot{path: path}
	total := int64(0)
	for index := range result.infos {
		member := path + immutableGenerationSuffix(index)
		info, err := os.Lstat(member)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return immutableGenerationSnapshot{}, fmt.Errorf(
				"inspect immutable SQLite generation member: %w", err,
			)
		}
		if err := validateImmutableGenerationMember(member, info); err != nil {
			return immutableGenerationSnapshot{}, err
		}
		if info.Size() < 0 || info.Size() > maximumImmutableGenerationMemberBytes ||
			info.Size() > maximumImmutableGenerationBytes-total {
			return immutableGenerationSnapshot{}, errors.New(
				"immutable SQLite generation size limit is exceeded",
			)
		}
		total += info.Size()
		result.infos[index] = info
	}
	if result.infos[0] == nil {
		for _, info := range result.infos[1:] {
			if info != nil {
				return immutableGenerationSnapshot{}, errors.New(
					"immutable SQLite generation has a sidecar without its database",
				)
			}
		}
		return result, nil
	}
	if result.infos[2] != nil && result.infos[1] == nil ||
		result.infos[1] != nil && result.infos[3] != nil {
		return immutableGenerationSnapshot{}, errors.New(
			"immutable SQLite generation has incoherent sidecars",
		)
	}
	return result, nil
}

func openImmutableGenerationMembers(
	parentCtx context.Context,
	sourceCtx context.Context,
	root *os.Root,
	snapshot immutableGenerationSnapshot,
) ([]immutableGenerationOpenMember, error) {
	opened := make([]immutableGenerationOpenMember, 0, 4)
	fail := func(cause error) ([]immutableGenerationOpenMember, error) {
		for index := len(opened) - 1; index >= 0; index-- {
			cause = errors.Join(cause, opened[index].file.Close())
		}
		return nil, cause
	}
	for index, expected := range snapshot.infos {
		if expected == nil {
			continue
		}
		if err := immutableGenerationContextError(parentCtx, sourceCtx); err != nil {
			return fail(err)
		}
		path := snapshot.path + immutableGenerationSuffix(index)
		name := filepath.Base(path)
		file, err := root.Open(name)
		if err != nil {
			return fail(fmt.Errorf("open immutable SQLite generation member: %w", err))
		}
		openedInfo, statErr := file.Stat()
		current, lstatErr := os.Lstat(path)
		if statErr != nil || lstatErr != nil ||
			!sameImmutableGenerationMetadata(expected, openedInfo) ||
			!sameImmutableGenerationMetadata(expected, current) {
			_ = file.Close()
			return fail(errors.Join(
				errors.New("immutable SQLite generation member changed while opening"),
				statErr,
				lstatErr,
			))
		}
		if err := validateImmutableGenerationMember(path, openedInfo); err != nil {
			_ = file.Close()
			return fail(err)
		}
		opened = append(opened, immutableGenerationOpenMember{
			index: index, path: path, name: name, info: expected, file: file,
		})
	}
	return opened, nil
}

func writeImmutableStageMember(
	parentCtx context.Context,
	sourceCtx context.Context,
	root *os.Root,
	name string,
	path string,
	source *immutableGenerationOpenMember,
) (immutableStageMember, error) {
	return writeImmutableStageMemberWithOps(
		parentCtx, sourceCtx, root, name, path, source, defaultImmutableStageWriteOps(),
	)
}

func writeImmutableStageMemberWithOps(
	parentCtx context.Context,
	sourceCtx context.Context,
	root *os.Root,
	name string,
	path string,
	source *immutableGenerationOpenMember,
	ops immutableStageWriteOps,
) (member immutableStageMember, returnErr error) {
	if root == nil || source == nil || source.file == nil {
		return member, errors.New("immutable SQLite stage copy is unavailable")
	}
	file, createErr := ops.open(root, name)
	if createErr != nil {
		return member, fmt.Errorf("create immutable SQLite stage member: %w", createErr)
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	info, err := ops.stat(file)
	if err != nil {
		return member, err
	}
	member = immutableStageMember{index: source.index, path: path, info: info}
	if chmodErr := ops.chmod(file, 0o600); chmodErr != nil {
		return member, fmt.Errorf("secure immutable SQLite stage member: %w", chmodErr)
	}
	// The stage is provider-owned and has never been opened by SQLite, so
	// handle-based Windows DACL hardening is safe here. On Unix this proves the
	// exact 0600 pathname identity without opening another descriptor.
	if secureErr := ops.secure(path); secureErr != nil {
		return member, fmt.Errorf("secure immutable SQLite stage member ACL: %w", secureErr)
	}
	securedHandle, handleStatErr := ops.stat(file)
	securedPath, secureStatErr := ops.lstat(path)
	if handleStatErr != nil || secureStatErr != nil || securedHandle == nil || securedPath == nil ||
		!os.SameFile(info, securedHandle) || !os.SameFile(securedHandle, securedPath) {
		return member, errors.Join(
			errors.New("immutable SQLite stage member changed while securing"),
			handleStatErr,
			secureStatErr,
		)
	}
	member.info = securedHandle
	if ops.retain != nil {
		member.retentionAttempted = true
		member.retained, err = ops.retain(parentCtx, path)
		if err != nil {
			return member, fmt.Errorf("retain immutable SQLite stage member: %w", err)
		}
	}
	if _, seekErr := ops.seek(source.file, 0, io.SeekStart); seekErr != nil {
		return member, fmt.Errorf("rewind immutable SQLite source member: %w", seekErr)
	}
	digest, size, err := ops.copy(
		parentCtx, sourceCtx, file, source.file, source.info.Size(),
	)
	if err != nil {
		return member, err
	}
	if size != source.info.Size() {
		return member, errors.New("immutable SQLite generation member size changed while copying")
	}
	source.digest = digest
	if syncErr := ops.sync(file); syncErr != nil {
		return member, fmt.Errorf("sync immutable SQLite stage member: %w", syncErr)
	}
	if _, seekErr := ops.seek(file, 0, io.SeekStart); seekErr != nil {
		return member, seekErr
	}
	destinationDigest, destinationSize, err := ops.hash(
		parentCtx, sourceCtx, file, source.info.Size(),
	)
	if err != nil || destinationSize != source.info.Size() || destinationDigest != digest {
		return member, errors.Join(
			errors.New("immutable SQLite stage member differs from its source"), err,
		)
	}
	current, statErr := ops.stat(file)
	pathInfo, lstatErr := ops.lstat(path)
	if statErr != nil || lstatErr != nil || current == nil || pathInfo == nil ||
		!os.SameFile(info, current) || !os.SameFile(current, pathInfo) ||
		current.Size() != source.info.Size() {
		return member, errors.Join(
			errors.New("immutable SQLite stage member changed while copying"),
			statErr,
			lstatErr,
		)
	}
	if err := ops.validate(path, current); err != nil {
		return member, err
	}
	member.info = current
	return member, nil
}

func copyImmutableGenerationBytes(
	parentCtx context.Context,
	sourceCtx context.Context,
	destination io.Writer,
	source io.Reader,
	limit int64,
) ([sha256.Size]byte, int64, error) {
	var result [sha256.Size]byte
	if destination == nil || source == nil || limit < 0 ||
		limit > maximumImmutableGenerationMemberBytes {
		return result, 0, errors.New("immutable SQLite generation byte copy is invalid")
	}
	digest := sha256.New()
	buffer := make([]byte, 128<<10)
	written := int64(0)
	for {
		if err := immutableGenerationContextError(parentCtx, sourceCtx); err != nil {
			return result, written, err
		}
		remaining := limit - written
		if remaining == 0 {
			var extra [1]byte
			n, readErr := source.Read(extra[:])
			if n != 0 || readErr == nil {
				return result, written, errors.New(
					"immutable SQLite generation member grew while copying",
				)
			}
			if !errors.Is(readErr, io.EOF) {
				return result, written, readErr
			}
			copy(result[:], digest.Sum(nil))
			return result, written, nil
		}
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, readErr := source.Read(chunk)
		if n > 0 {
			_, _ = digest.Write(chunk[:n])
			for offset := 0; offset < n; {
				count, writeErr := destination.Write(chunk[offset:n])
				if count > 0 {
					offset += count
					written += int64(count)
				}
				if writeErr != nil {
					return result, written, writeErr
				}
				if count == 0 {
					return result, written, io.ErrShortWrite
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return result, written, io.ErrUnexpectedEOF
		}
		if readErr != nil {
			return result, written, readErr
		}
		if n == 0 {
			return result, written, io.ErrNoProgress
		}
	}
}

func hashImmutableGenerationFile(
	parentCtx context.Context,
	sourceCtx context.Context,
	file *os.File,
	limit int64,
) ([sha256.Size]byte, int64, error) {
	return copyImmutableGenerationBytes(parentCtx, sourceCtx, io.Discard, file, limit)
}

func revalidateImmutableGeneration(
	ctx context.Context,
	snapshot immutableGenerationSnapshot,
	opened []immutableGenerationOpenMember,
) error {
	if ctx == nil {
		return errors.New("immutable SQLite generation revalidation context is unavailable")
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	current, err := captureImmutableGeneration(snapshot.path)
	if err != nil {
		return err
	}
	for index := range snapshot.infos {
		if !sameImmutableGenerationMetadata(snapshot.infos[index], current.infos[index]) {
			return errors.New("immutable SQLite generation changed while copying")
		}
	}
	for index := range opened {
		member := &opened[index]
		openedInfo, statErr := member.file.Stat()
		pathInfo, lstatErr := os.Lstat(member.path)
		if statErr != nil || lstatErr != nil ||
			!sameImmutableGenerationMetadata(member.info, openedInfo) ||
			!sameImmutableGenerationMetadata(member.info, pathInfo) {
			return errors.Join(
				errors.New("immutable SQLite generation member changed while copying"),
				statErr,
				lstatErr,
			)
		}
		if member.digest == ([sha256.Size]byte{}) {
			continue
		}
		if _, seekErr := member.file.Seek(0, io.SeekStart); seekErr != nil {
			return seekErr
		}
		digest, size, hashErr := hashImmutableGenerationFile(
			ctx, ctx, member.file, member.info.Size(),
		)
		if hashErr != nil || size != member.info.Size() || digest != member.digest {
			return errors.Join(
				errors.New("immutable SQLite generation member bytes changed while copying"),
				hashErr,
			)
		}
	}
	return nil
}

func validateCopiedImmutableStage(
	parentCtx context.Context,
	sourceCtx context.Context,
	stage string,
	opened []immutableGenerationOpenMember,
) (returnErr error) {
	snapshot, err := captureImmutableGeneration(stage)
	if err != nil {
		return err
	}
	byIndex := make(map[int]*immutableGenerationOpenMember, len(opened))
	for index := range opened {
		member := &opened[index]
		if member.index < 0 || member.index >= len(snapshot.infos) || byIndex[member.index] != nil {
			return errors.New("immutable SQLite source generation inventory is invalid")
		}
		byIndex[member.index] = member
	}
	root, err := os.OpenRoot(filepath.Dir(stage))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	for index := range snapshot.infos {
		source := byIndex[index]
		expected := source != nil
		if (snapshot.infos[index] != nil) != expected {
			return errors.New("immutable SQLite stage inventory differs from its source")
		}
		if !expected {
			continue
		}
		path := stage + immutableGenerationSuffix(index)
		file, err := root.Open(filepath.Base(path))
		if err != nil {
			return err
		}
		openedInfo, statErr := file.Stat()
		pathInfo, lstatErr := os.Lstat(path)
		if statErr != nil || lstatErr != nil ||
			!sameImmutableGenerationMetadata(snapshot.infos[index], openedInfo) ||
			!sameImmutableGenerationMetadata(snapshot.infos[index], pathInfo) {
			_ = file.Close()
			return errors.Join(
				errors.New("immutable SQLite stage member changed before verification"),
				statErr,
				lstatErr,
			)
		}
		digest, size, hashErr := hashImmutableGenerationFile(
			parentCtx, sourceCtx, file, source.info.Size(),
		)
		finalInfo, finalStatErr := file.Stat()
		finalPath, finalLstatErr := os.Lstat(path)
		closeErr := file.Close()
		if hashErr != nil || finalStatErr != nil || finalLstatErr != nil || closeErr != nil ||
			size != source.info.Size() || digest != source.digest ||
			!sameImmutableGenerationMetadata(snapshot.infos[index], finalInfo) ||
			!sameImmutableGenerationMetadata(snapshot.infos[index], finalPath) {
			return errors.Join(
				errors.New("immutable SQLite stage member verification failed"),
				hashErr,
				finalStatErr,
				finalLstatErr,
				closeErr,
			)
		}
	}
	return nil
}

func validateImmutableGenerationMember(path string, info os.FileInfo) error {
	return validateImmutableGenerationMemberWithFilesystem(
		path, info, systemProviderFilesystem(),
	)
}

func validateImmutableGenerationMemberWithFilesystem(
	path string,
	info os.FileInfo,
	filesystem providerFilesystem,
) error {
	if err := filesystem.validateLiveInfo(info); err != nil {
		return fmt.Errorf("validate immutable SQLite generation member: %w", err)
	}
	switch providerGenerationLinkCount(filesystem, path, info) {
	case generationLinkSingle:
	case generationLinkZero:
		return errors.New("immutable SQLite generation member is transitioning")
	case generationLinkMultiple, generationLinkUnsafe:
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("immutable SQLite generation member has an unsafe link boundary"),
		)
	default:
		return errors.New("immutable SQLite generation member link count is unavailable")
	}
	switch providerGenerationOwner(filesystem, path, info) {
	case generationOwnerCurrent:
		return nil
	case generationOwnerForeign:
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("immutable SQLite generation member is owned by another user"),
		)
	default:
		return errors.New("immutable SQLite generation member owner is unavailable")
	}
}

func requireUnusedImmutableStage(stage string) error {
	for index := 0; index < 4; index++ {
		member := stage + immutableGenerationSuffix(index)
		if _, err := os.Lstat(member); err == nil {
			return errors.New("immutable SQLite generation stage already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect immutable SQLite generation stage: %w", err)
		}
	}
	return nil
}

func detachImmutableStageRetentions(
	ctx context.Context,
	created []immutableStageMember,
	sourceExists bool,
	requireRetainedMain bool,
) (*retainedStagedGeneration, error) {
	var (
		main      *retainedStagedGeneration
		closeErrs error
	)
	for index := range created {
		retained := created[index].retained
		if retained == nil {
			continue
		}
		if err := retained.Check(ctx, created[index].path); err != nil {
			return nil, fmt.Errorf("recheck copied immutable SQLite stage member: %w", err)
		}
		if created[index].index == 0 {
			if main != nil {
				return nil, errors.New("immutable SQLite stage retained multiple mains")
			}
			main = retained
			continue
		}
		closeErrs = errors.Join(closeErrs, retained.Close())
		created[index].retained = nil
	}
	if closeErrs != nil {
		return nil, closeErrs
	}
	if !sourceExists && main != nil {
		return nil, errors.New("missing immutable SQLite source created a retained stage main")
	}
	if sourceExists && requireRetainedMain && main == nil {
		return nil, errors.New("immutable SQLite stage main retention is unavailable")
	}
	if main != nil {
		for index := range created {
			if created[index].retained == main {
				created[index].retained = nil
				break
			}
		}
	}
	return main, nil
}

func cleanupImmutableStage(
	created []immutableStageMember,
	parent string,
	ops immutableGenerationCopyOps,
) error {
	var result error
	removed := false
	cleanupCtx, cancel := context.WithTimeout(context.Background(), maximumStagedCleanupDuration)
	defer cancel()
	for index := len(created) - 1; index >= 0; index-- {
		member := created[index]
		if member.retentionAttempted {
			if member.retained == nil {
				result = errors.Join(
					result,
					errors.New("immutable SQLite stage retention was not established before cleanup"),
				)
				continue
			}
			retireErr := member.retained.Retire(cleanupCtx)
			closeErr := member.retained.Close()
			result = errors.Join(result, retireErr, closeErr)
			if retireErr == nil {
				removed = true
			}
			continue
		}
		current, err := os.Lstat(member.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if member.info == nil || current == nil || !os.SameFile(member.info, current) {
			result = errors.Join(
				result,
				errors.New("immutable SQLite stage member changed before cleanup"),
			)
			continue
		}
		if err := ops.remove(member.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
			continue
		}
		removed = true
	}
	if removed {
		result = errors.Join(result, ops.syncDirectory(parent))
	}
	return result
}

func sameImmutableGenerationMetadata(left, right os.FileInfo) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return os.SameFile(left, right) && left.Size() == right.Size() &&
		left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func validImmutableGenerationPath(path string) bool {
	return validProviderFilesystemPath(path) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path && !strings.HasPrefix(strings.ToLower(path), "file:")
}

func immutableGenerationSuffix(index int) string {
	return [...]string{"", "-wal", "-shm", "-journal"}[index]
}

func immutableGenerationContextError(parentCtx, sourceCtx context.Context) error {
	if parentCtx == nil || sourceCtx == nil {
		return errors.New("immutable SQLite generation context is unavailable")
	}
	if err := context.Cause(parentCtx); err != nil {
		return err
	}
	return context.Cause(sourceCtx)
}
