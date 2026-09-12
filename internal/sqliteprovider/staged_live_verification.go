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

	"github.com/sipeed/picoclaw/internal/fileidentity"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

var errValidatedReplacementContract = errors.New(
	"validated SQLite replacement callback contract was violated",
)

const stagedReplacementRandomHexLength = 32

// StagedLiveVerification revalidates live migration inputs while the provider
// retains the exact validated replacement pin. ValidatedReplacement exposes
// that provider-selected stage only through one synchronous use.
type StagedLiveVerification func(context.Context, ValidatedReplacement) error

// ValidatedReplacementCheck proves that one exact directory-entry observation
// still names the provider-retained replacement. Successful checks return the
// physical identity derived from that retained handle for alias accounting.
type ValidatedReplacementCheck func(
	context.Context,
	os.FileInfo,
) (fileidentity.Identity, error)

// ValidatedTargetParentCheck proves that an observed directory is the exact
// target parent atomically created and retained by this provider invocation.
// It also requires the validated replacement stage to be that directory's
// sole entry.
type ValidatedTargetParentCheck func(
	context.Context,
	os.FileInfo,
) (fileidentity.Identity, error)

// ValidatedReplacement is an opaque, callback-scoped capability for one exact
// provider-selected and claims-pinned replacement generation. Its zero value
// and copies retained beyond StagedLiveVerification are unusable.
type ValidatedReplacement struct {
	state *validatedReplacementState
}

type validatedReplacementState struct {
	sync.Mutex
	wait      sync.WaitGroup
	checkWait sync.WaitGroup

	active         bool
	used           int
	inFlight       bool
	callbackActive bool
	checking       bool
	useErr         error
	storeID        dblayer.StoreID
	target         string
	stage          string
	stageIdentity  os.FileInfo
	stageHandleID  fileidentity.Identity
	stageDigest    [sha256.Size]byte
	stageFile      *os.File
	stageFileMu    sync.Mutex
	targetParent   *retainedStagedTargetParent
	scope          context.Context
}

// Use exposes the exact stage main only while the originating provider
// callback is active. expectedID and expectedTarget must match the immutable
// provider-lease binding; callers cannot substitute an exclusion path.
func (replacement ValidatedReplacement) Use(
	ctx context.Context,
	expectedID dblayer.StoreID,
	expectedTarget string,
	use func(context.Context, string, ValidatedReplacementCheck) error,
) (returnErr error) {
	if use == nil {
		return errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement use is unavailable"),
		)
	}
	return replacement.use(ctx, expectedID, expectedTarget, func(
		useCtx context.Context,
		stage string,
		check ValidatedReplacementCheck,
		_ ValidatedTargetParentCheck,
	) error {
		return use(useCtx, stage, check)
	})
}

// UseWithTargetParent is Use with a separate checker for the exact parent that
// this provider invocation created. The checker is unavailable for a parent
// that pre-existed or whose creation ownership was ambiguous.
func (replacement ValidatedReplacement) UseWithTargetParent(
	ctx context.Context,
	expectedID dblayer.StoreID,
	expectedTarget string,
	use func(
		context.Context,
		string,
		ValidatedReplacementCheck,
		ValidatedTargetParentCheck,
	) error,
) error {
	return replacement.use(ctx, expectedID, expectedTarget, use)
}

func (replacement ValidatedReplacement) use(
	ctx context.Context,
	expectedID dblayer.StoreID,
	expectedTarget string,
	use func(
		context.Context,
		string,
		ValidatedReplacementCheck,
		ValidatedTargetParentCheck,
	) error,
) (returnErr error) {
	state := replacement.state
	if state == nil || ctx == nil || !expectedID.Valid() ||
		!validProviderFilesystemPath(expectedTarget) || use == nil {
		return errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement use is unavailable"),
		)
	}
	state.Lock()
	state.used++
	call := state.used
	if !state.active || call != 1 || state.inFlight {
		state.Unlock()
		return errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement is not active and single-use"),
		)
	}
	state.inFlight = true
	state.wait.Add(1)
	storeID := state.storeID
	target := state.target
	stage := state.stage
	identity := state.stageIdentity
	handleIdentity := state.stageHandleID
	digest := state.stageDigest
	targetParent := state.targetParent
	scope := state.scope
	state.Unlock()

	defer func() {
		state.Lock()
		state.useErr = returnErr
		state.inFlight = false
		state.Unlock()
		state.wait.Done()
	}()
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if cause := context.Cause(scope); cause != nil {
		return cause
	}
	if expectedID != storeID || expectedTarget != target ||
		filepath.Clean(expectedTarget) != expectedTarget {
		return errors.Join(
			errValidatedReplacementContract,
			dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement does not match its target",
			),
		)
	}
	checkObserved := func(
		checkCtx context.Context,
		observed os.FileInfo,
		requireCallback bool,
	) (retainedIdentity fileidentity.Identity, returnErr error) {
		if checkCtx == nil {
			return retainedIdentity, errors.Join(
				errValidatedReplacementContract,
				errors.New("validated SQLite replacement check context is unavailable"),
			)
		}
		state.Lock()
		active := state.active && state.inFlight && state.scope == scope &&
			(!requireCallback || state.callbackActive) && !state.checking
		if active {
			state.checking = true
			state.checkWait.Add(1)
		}
		state.Unlock()
		if !active {
			return retainedIdentity, errors.Join(
				errValidatedReplacementContract,
				errors.New("validated SQLite replacement check is unavailable or concurrent"),
			)
		}
		defer func() {
			state.Lock()
			state.checking = false
			state.Unlock()
			state.checkWait.Done()
		}()
		if cause := context.Cause(checkCtx); cause != nil {
			return retainedIdentity, cause
		}
		if cause := context.Cause(scope); cause != nil {
			return retainedIdentity, cause
		}
		if !sameValidatedReplacementMetadata(identity, observed) {
			return retainedIdentity, dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement observation does not match its pin",
			)
		}
		if err := verifyValidatedReplacementStage(stage, identity); err != nil {
			return retainedIdentity, err
		}
		state.stageFileMu.Lock()
		defer state.stageFileMu.Unlock()
		if cause := context.Cause(scope); cause != nil {
			return retainedIdentity, cause
		}
		if err := verifyOpenedValidatedReplacementStage(stage, identity, state.stageFile); err != nil {
			return retainedIdentity, err
		}
		retainedIdentity, objectType, err := fileidentity.Opened(state.stageFile)
		namedIdentity, namedType, exists, namedErr := fileidentity.ExistingWithType(stage)
		if err != nil || namedErr != nil || objectType != fileidentity.ObjectTypeRegular ||
			!retainedIdentity.Valid() || retainedIdentity != handleIdentity || !exists ||
			namedType != fileidentity.ObjectTypeRegular || namedIdentity != handleIdentity {
			return fileidentity.Identity{}, errors.Join(
				dblayer.NewError(
					dblayer.CodeIntegrity,
					"validated SQLite replacement retained identity is unavailable",
				),
				err,
				namedErr,
			)
		}
		return retainedIdentity, nil
	}
	checkExact := func(
		checkCtx context.Context,
		observed os.FileInfo,
	) (fileidentity.Identity, error) {
		return checkObserved(checkCtx, observed, true)
	}
	checkTargetParent := func(
		checkCtx context.Context,
		observed os.FileInfo,
	) (retainedIdentity fileidentity.Identity, returnErr error) {
		if checkCtx == nil {
			return retainedIdentity, errors.Join(
				errValidatedReplacementContract,
				errors.New("validated SQLite target-parent check input is invalid"),
			)
		}
		state.Lock()
		active := state.active && state.inFlight && state.scope == scope &&
			state.callbackActive && !state.checking
		if active {
			state.checking = true
			state.checkWait.Add(1)
		}
		state.Unlock()
		if !active {
			return retainedIdentity, errors.Join(
				errValidatedReplacementContract,
				errors.New("validated SQLite target-parent check is unavailable or concurrent"),
			)
		}
		defer func() {
			state.Lock()
			state.checking = false
			state.Unlock()
			state.checkWait.Done()
		}()
		if targetParent == nil {
			return retainedIdentity, errors.Join(
				errValidatedReplacementContract,
				errors.New("validated SQLite target parent was not provider-created"),
			)
		}
		if cause := context.Cause(checkCtx); cause != nil {
			return retainedIdentity, cause
		}
		if cause := context.Cause(scope); cause != nil {
			return retainedIdentity, cause
		}
		expectedParent := filepath.Dir(target)
		observedIdentity, err := targetParent.Check(checkCtx, expectedParent, observed)
		if err != nil {
			return retainedIdentity, err
		}
		retainedIdentity, err = targetParent.CheckSoleStage(
			checkCtx,
			expectedParent,
			stage,
			identity,
		)
		if err != nil || retainedIdentity != observedIdentity {
			return fileidentity.Identity{}, errors.Join(
				errors.New("validated SQLite target parent changed during its exact check"),
				err,
			)
		}
		return retainedIdentity, nil
	}
	if _, err := checkObserved(ctx, identity, false); err != nil {
		return err
	}
	operationCtx, cancelOperation := context.WithCancelCause(ctx)
	stopScope := context.AfterFunc(scope, func() {
		cancelOperation(context.Cause(scope))
	})
	if cause := context.Cause(scope); cause != nil {
		cancelOperation(cause)
	}
	state.Lock()
	state.callbackActive = true
	state.Unlock()
	var (
		useErr              error
		returnedDuringCheck bool
	)
	func() {
		defer func() {
			state.Lock()
			state.callbackActive = false
			returnedDuringCheck = state.checking
			state.Unlock()
			state.checkWait.Wait()
		}()
		useErr = use(operationCtx, stage, checkExact, checkTargetParent)
	}()
	stopScope()
	cancelOperation(errValidatedReplacementContract)
	if returnedDuringCheck {
		useErr = errors.Join(
			useErr,
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement callback returned during exact check"),
		)
	}
	if cause := context.Cause(ctx); cause != nil && !errors.Is(useErr, cause) {
		useErr = errors.Join(useErr, cause)
	}
	if cause := context.Cause(scope); cause != nil && !errors.Is(useErr, cause) {
		useErr = errors.Join(useErr, cause)
	}
	_, guardErr := checkObserved(ctx, identity, false)
	var targetParentErr error
	if targetParent != nil {
		_, targetParentErr = targetParent.CheckSoleStage(
			ctx,
			filepath.Dir(target),
			stage,
			identity,
		)
	}
	state.stageFileMu.Lock()
	finalDigest, digestErr := fingerprintOpenedValidatedReplacementStage(
		ctx, stage, identity, state.stageFile,
	)
	state.stageFileMu.Unlock()
	if digestErr == nil && finalDigest != digest {
		digestErr = dblayer.NewError(
			dblayer.CodeIntegrity,
			"validated SQLite replacement contents changed",
		)
	}
	return errors.Join(useErr, guardErr, targetParentErr, digestErr)
}

func invokeStagedLiveVerification(
	ctx context.Context,
	storeID dblayer.StoreID,
	target string,
	seal *validatedReplacementSeal,
	verify StagedLiveVerification,
) (returnErr error) {
	return invokeStagedLiveVerificationWithTargetParent(
		ctx,
		storeID,
		target,
		seal,
		nil,
		verify,
	)
}

func invokeStagedLiveVerificationWithTargetParent(
	ctx context.Context,
	storeID dblayer.StoreID,
	target string,
	seal *validatedReplacementSeal,
	targetParent *retainedStagedTargetParent,
	verify StagedLiveVerification,
) (returnErr error) {
	if verify == nil {
		return nil
	}
	if ctx == nil || !storeID.Valid() || !validProviderFilesystemPath(target) || seal == nil ||
		!validProviderFilesystemPath(seal.path) || seal.identity == nil || seal.file == nil ||
		!seal.handleIdentity.Valid() {
		return errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement callback input is invalid"),
		)
	}
	if err := validateTargetDerivedReplacementPath(target, seal.path); err != nil {
		return err
	}
	if err := seal.verify(ctx); err != nil {
		return err
	}
	if targetParent != nil {
		if _, err := targetParent.CheckSoleStage(
			ctx,
			filepath.Dir(target),
			seal.path,
			seal.identity,
		); err != nil {
			return err
		}
	}
	scope, cancelScope := context.WithCancelCause(ctx)
	state := &validatedReplacementState{
		active: true, storeID: storeID, target: target,
		stage: seal.path, stageIdentity: seal.identity, stageHandleID: seal.handleIdentity,
		stageDigest: seal.digest,
		stageFile:   seal.file, targetParent: targetParent, scope: scope,
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = errors.Join(
				returnErr,
				errValidatedReplacementContract,
				errors.New("validated SQLite replacement callback panicked"),
			)
		}
		state.Lock()
		state.active = false
		uses := state.used
		returnedDuringUse := state.inFlight
		state.Unlock()
		cancelScope(errValidatedReplacementContract)
		state.wait.Wait()
		state.Lock()
		useErr := state.useErr
		state.Unlock()
		if uses != 1 {
			returnErr = errors.Join(
				returnErr,
				errValidatedReplacementContract,
				fmt.Errorf("validated SQLite replacement use count is %d, want 1", uses),
			)
		}
		if returnedDuringUse {
			returnErr = errors.Join(
				returnErr,
				errValidatedReplacementContract,
				errors.New("validated SQLite replacement callback returned during use"),
			)
		}
		if useErr != nil && !errors.Is(returnErr, useErr) {
			returnErr = errors.Join(returnErr, useErr)
		}
	}()
	return verify(scope, ValidatedReplacement{state: state})
}

type validatedReplacementSeal struct {
	path           string
	identity       os.FileInfo
	handleIdentity fileidentity.Identity
	file           *os.File
	digest         [sha256.Size]byte
}

func sealValidatedReplacementStage(
	ctx context.Context,
	path string,
) (*validatedReplacementSeal, error) {
	if ctx == nil || !validProviderFilesystemPath(path) {
		return nil, errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement seal input is invalid"),
		)
	}
	identity, err := os.Lstat(path)
	if err != nil || identity == nil || !identity.Mode().IsRegular() ||
		identity.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(
			dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement identity is unavailable for sealing",
			),
			err,
		)
	}
	file, digest, err := openValidatedReplacementStage(ctx, path, identity)
	if err != nil {
		return nil, err
	}
	handleIdentity, objectType, identityErr := fileidentity.Opened(file)
	if identityErr != nil || !handleIdentity.Valid() ||
		objectType != fileidentity.ObjectTypeRegular {
		return nil, errors.Join(
			dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement handle identity is unavailable for sealing",
			),
			identityErr,
			file.Close(),
		)
	}
	return &validatedReplacementSeal{
		path: path, identity: identity, handleIdentity: handleIdentity,
		file: file, digest: digest,
	}, nil
}

func (seal *validatedReplacementSeal) verify(ctx context.Context) error {
	if seal == nil || seal.file == nil || !seal.handleIdentity.Valid() {
		return errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement seal is unavailable"),
		)
	}
	if err := verifyValidatedReplacementStage(seal.path, seal.identity); err != nil {
		return err
	}
	digest, err := fingerprintOpenedValidatedReplacementStage(
		ctx, seal.path, seal.identity, seal.file,
	)
	if err != nil {
		return err
	}
	if digest != seal.digest {
		return dblayer.NewError(
			dblayer.CodeIntegrity,
			"validated SQLite replacement contents changed after validation",
		)
	}
	if err := seal.verifyHandleIdentity(); err != nil {
		return err
	}
	return verifyValidatedReplacementStage(seal.path, seal.identity)
}

func (seal *validatedReplacementSeal) verifyHandleIdentity() error {
	identity, objectType, err := fileidentity.Opened(seal.file)
	namedIdentity, namedType, exists, namedErr := fileidentity.ExistingWithType(seal.path)
	if err != nil || namedErr != nil || identity != seal.handleIdentity ||
		objectType != fileidentity.ObjectTypeRegular || !exists ||
		namedIdentity != seal.handleIdentity || namedType != fileidentity.ObjectTypeRegular {
		return errors.Join(
			dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement retained handle identity changed",
			),
			err,
			namedErr,
		)
	}
	return nil
}

func (seal *validatedReplacementSeal) close() error {
	if seal == nil || seal.file == nil {
		return nil
	}
	file := seal.file
	seal.file = nil
	return file.Close()
}

func validateTargetDerivedReplacementPath(target, stage string) error {
	if !filepath.IsAbs(target) || !filepath.IsAbs(stage) ||
		filepath.Clean(target) != target || filepath.Clean(stage) != stage ||
		filepath.Dir(stage) != filepath.Dir(target) {
		return dblayer.NewError(
			dblayer.CodeIntegrity,
			"validated SQLite replacement path is not target-derived",
		)
	}
	prefix := "." + filepath.Base(target) + ".migration-stage-"
	base := filepath.Base(stage)
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".db") {
		return dblayer.NewError(
			dblayer.CodeIntegrity,
			"validated SQLite replacement path is not target-derived",
		)
	}
	random := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".db")
	if len(random) != stagedReplacementRandomHexLength {
		return dblayer.NewError(
			dblayer.CodeIntegrity,
			"validated SQLite replacement path has an invalid nonce",
		)
	}
	for _, value := range random {
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return dblayer.NewError(
					dblayer.CodeIntegrity,
					"validated SQLite replacement path has an invalid nonce",
				)
			}
		}
	}
	return nil
}

func verifyValidatedReplacementStage(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil || current == nil || expected == nil ||
		!current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(expected, current) || current.Size() != expected.Size() ||
		current.Mode() != expected.Mode() || !current.ModTime().Equal(expected.ModTime()) {
		return errors.Join(
			dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement identity changed",
			),
			err,
		)
	}
	if err := requireNoGenerationSidecars(path); err != nil {
		return errors.Join(
			dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement sidecars changed",
			),
			err,
		)
	}
	return nil
}

func openValidatedReplacementStage(
	ctx context.Context,
	path string,
	expected os.FileInfo,
) (_ *os.File, digest [sha256.Size]byte, returnErr error) {
	if ctx == nil || !validProviderFilesystemPath(path) || expected == nil ||
		expected.Size() < 0 || expected.Size() > maximumImmutableGenerationMemberBytes {
		return nil, digest, errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement fingerprint input is invalid"),
		)
	}
	if err := verifyValidatedReplacementStage(path, expected); err != nil {
		return nil, digest, err
	}
	opened, err := providerOpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return nil, digest, fmt.Errorf("open validated SQLite replacement: %w", err)
	}
	file, ok := opened.(*os.File)
	if !ok {
		_ = opened.Close()
		return nil, digest, errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement file handle is unavailable"),
		)
	}
	digest, err = fingerprintOpenedValidatedReplacementStage(ctx, path, expected, file)
	if err != nil {
		return nil, digest, errors.Join(err, file.Close())
	}
	return file, digest, nil
}

func fingerprintOpenedValidatedReplacementStage(
	ctx context.Context,
	path string,
	expected os.FileInfo,
	file *os.File,
) (digest [sha256.Size]byte, returnErr error) {
	if ctx == nil || file == nil || expected == nil || expected.Size() < 0 ||
		expected.Size() > maximumImmutableGenerationMemberBytes {
		return digest, errors.Join(
			errValidatedReplacementContract,
			errors.New("opened validated SQLite replacement fingerprint input is invalid"),
		)
	}
	if err := verifyOpenedValidatedReplacementStage(path, expected, file); err != nil {
		return digest, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return digest, err
	}
	digest, size, hashErr := hashImmutableGenerationFile(ctx, ctx, file, expected.Size())
	after, finalStatErr := file.Stat()
	finalPath, finalPathErr := os.Lstat(path)
	if hashErr != nil || size != expected.Size() || finalStatErr != nil || finalPathErr != nil ||
		!sameValidatedReplacementMetadata(expected, after) ||
		!sameValidatedReplacementMetadata(expected, finalPath) {
		return digest, errors.Join(
			dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement changed while fingerprinting",
			),
			hashErr,
			finalStatErr,
			finalPathErr,
		)
	}
	return digest, nil
}

func verifyOpenedValidatedReplacementStage(
	path string,
	expected os.FileInfo,
	file *os.File,
) error {
	if file == nil {
		return errors.Join(
			errValidatedReplacementContract,
			errors.New("validated SQLite replacement file handle is unavailable"),
		)
	}
	opened, statErr := file.Stat()
	pathInfo, pathErr := os.Lstat(path)
	if statErr != nil || pathErr != nil ||
		!sameValidatedReplacementMetadata(expected, opened) ||
		!sameValidatedReplacementMetadata(expected, pathInfo) {
		return errors.Join(
			dblayer.NewError(
				dblayer.CodeIntegrity,
				"validated SQLite replacement changed while retained",
			),
			statErr,
			pathErr,
		)
	}
	return nil
}

func sameValidatedReplacementMetadata(expected, current os.FileInfo) bool {
	return expected != nil && current != nil && current.Mode().IsRegular() &&
		current.Mode()&os.ModeSymlink == 0 && os.SameFile(expected, current) &&
		expected.Size() == current.Size() && expected.Mode() == current.Mode() &&
		expected.ModTime().Equal(current.ModTime())
}
