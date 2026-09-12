package sqlitestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maximumSealedAbsentLegacyPathBytes  = 16 << 10
	maximumSealedAbsentLegacyComponents = 1024
)

var (
	sealedAbsentLegacyCapturePlatform    = captureSealedAbsentLegacyRootPlatform
	sealedAbsentLegacyRevalidatePlatform = revalidateSealedAbsentLegacyRootPlatform
	sealedAbsentLegacyClosePlatform      = closeSealedAbsentLegacyRootPlatform
)

// sealedAbsentLegacyRoot retains the nearest existing private directory and
// binds it to the complete missing suffix of one exact legacy root. It is an
// in-memory, single-open capability and carries no mutation authority.
type sealedAbsentLegacyRoot struct {
	sync.Mutex
	path         string
	ancestorPath string
	suffix       []string
	platform     *sealedAbsentLegacyRootPlatform
	state        sealedAbsentLegacyRootState
	closed       bool
}

type sealedAbsentLegacyRootState uint8

const (
	sealedAbsentLegacyRootCaptured sealedAbsentLegacyRootState = iota + 1
	sealedAbsentLegacyRootEnumerated
	sealedAbsentLegacyRootCheckedBeforeCommit
)

// captureSealedAbsentLegacyRoot proves that path is absent below one retained
// private directory without following a symlink or reparse point. Callers must
// close the returned proof after the transaction that consumes it.
func captureSealedAbsentLegacyRoot(
	ctx context.Context,
	path string,
) (*sealedAbsentLegacyRoot, error) {
	if ctx == nil {
		return nil, errors.New("sealed absent legacy root context is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if !validSealedAbsentLegacyPath(path) {
		return nil, errors.New("sealed absent legacy root path is invalid")
	}

	platform, ancestorPath, suffix, err := sealedAbsentLegacyCapturePlatform(ctx, path)
	if err != nil {
		return nil, err
	}
	proof := &sealedAbsentLegacyRoot{
		path:         path,
		ancestorPath: ancestorPath,
		suffix:       append([]string(nil), suffix...),
		platform:     platform,
		state:        sealedAbsentLegacyRootCaptured,
	}
	fail := func(cause error) (*sealedAbsentLegacyRoot, error) {
		return nil, errors.Join(cause, proof.closeLocked())
	}
	if err := validateSealedAbsentLegacyLayout(path, ancestorPath, suffix); err != nil {
		return fail(err)
	}
	if err := context.Cause(ctx); err != nil {
		return fail(err)
	}
	if err := sealedAbsentLegacyRevalidatePlatform(
		ctx,
		proof.path,
		proof.ancestorPath,
		proof.suffix,
		proof.platform,
	); err != nil {
		return fail(fmt.Errorf("revalidate captured sealed absent legacy root: %w", err))
	}
	if err := context.Cause(ctx); err != nil {
		return fail(err)
	}
	return proof, nil
}

// revalidateSealedAbsentLegacyRoot proves that the named ancestor still binds
// to the retained private directory and that the first missing component (and
// therefore the complete descendant suffix) is still absent.
func revalidateSealedAbsentLegacyRoot(
	ctx context.Context,
	proof *sealedAbsentLegacyRoot,
) error {
	if ctx == nil || proof == nil {
		return errors.New("sealed absent legacy root proof is unavailable")
	}
	proof.Lock()
	defer proof.Unlock()
	if err := validateSealedAbsentLegacyRootLocked(proof); err != nil {
		return err
	}
	if proof.state != sealedAbsentLegacyRootCaptured &&
		proof.state != sealedAbsentLegacyRootEnumerated {
		return errors.New("sealed absent legacy root proof cannot be revalidated in its current state")
	}
	return revalidateSealedAbsentLegacyRootLocked(ctx, proof)
}

// markSealedAbsentLegacyRootEnumerated records one deterministic empty
// enumeration after revalidating the retained filesystem proof.
func markSealedAbsentLegacyRootEnumerated(
	ctx context.Context,
	proof *sealedAbsentLegacyRoot,
) error {
	if ctx == nil || proof == nil {
		return errors.New("sealed absent legacy root enumeration proof is unavailable")
	}
	proof.Lock()
	defer proof.Unlock()
	if err := validateSealedAbsentLegacyRootLocked(proof); err != nil {
		return err
	}
	if proof.state != sealedAbsentLegacyRootCaptured {
		return errors.New("sealed absent legacy root enumeration was already consumed")
	}
	if err := revalidateSealedAbsentLegacyRootLocked(ctx, proof); err != nil {
		return err
	}
	proof.state = sealedAbsentLegacyRootEnumerated
	return nil
}

// checkSealedAbsentLegacyRootBeforeCommit performs the proof's one final
// filesystem check after all callbacks and immediately before COMMIT.
func checkSealedAbsentLegacyRootBeforeCommit(
	ctx context.Context,
	proof *sealedAbsentLegacyRoot,
) error {
	if ctx == nil || proof == nil {
		return errors.New("sealed absent legacy root precommit proof is unavailable")
	}
	proof.Lock()
	defer proof.Unlock()
	if err := validateSealedAbsentLegacyRootLocked(proof); err != nil {
		return err
	}
	if proof.state != sealedAbsentLegacyRootEnumerated {
		return errors.New("sealed absent legacy root was not enumerated exactly once")
	}
	if err := revalidateSealedAbsentLegacyRootLocked(ctx, proof); err != nil {
		return err
	}
	proof.state = sealedAbsentLegacyRootCheckedBeforeCommit
	return nil
}

// requireSealedAbsentLegacyRootConsumed proves that empty enumeration and the
// precommit check both ran exactly once before Open reports success.
func requireSealedAbsentLegacyRootConsumed(proof *sealedAbsentLegacyRoot) error {
	if proof == nil {
		return errors.New("sealed absent legacy root consumption proof is unavailable")
	}
	proof.Lock()
	defer proof.Unlock()
	if err := validateSealedAbsentLegacyRootLocked(proof); err != nil {
		return err
	}
	if proof.state != sealedAbsentLegacyRootCheckedBeforeCommit {
		return errors.New("sealed absent legacy root proof was not consumed")
	}
	return nil
}

func validateSealedAbsentLegacyRootLocked(proof *sealedAbsentLegacyRoot) error {
	if proof == nil || proof.closed || proof.platform == nil ||
		proof.state < sealedAbsentLegacyRootCaptured ||
		proof.state > sealedAbsentLegacyRootCheckedBeforeCommit ||
		validateSealedAbsentLegacyLayout(proof.path, proof.ancestorPath, proof.suffix) != nil {
		return errors.New("sealed absent legacy root proof is invalid")
	}
	return nil
}

func revalidateSealedAbsentLegacyRootLocked(
	ctx context.Context,
	proof *sealedAbsentLegacyRoot,
) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := sealedAbsentLegacyRevalidatePlatform(
		ctx,
		proof.path,
		proof.ancestorPath,
		proof.suffix,
		proof.platform,
	); err != nil {
		return err
	}
	return context.Cause(ctx)
}

// closeSealedAbsentLegacyRoot releases proof handles without touching any
// pathname. It is idempotent, including for a nil proof.
func closeSealedAbsentLegacyRoot(proof *sealedAbsentLegacyRoot) error {
	if proof == nil {
		return nil
	}
	proof.Lock()
	defer proof.Unlock()
	return proof.closeLocked()
}

func (proof *sealedAbsentLegacyRoot) closeLocked() error {
	if proof == nil || proof.closed {
		return nil
	}
	proof.closed = true
	platform := proof.platform
	proof.platform = nil
	if platform == nil {
		return nil
	}
	return sealedAbsentLegacyClosePlatform(platform)
}

func validSealedAbsentLegacyPath(path string) bool {
	if path == "" || path != strings.TrimSpace(path) || !utf8.ValidString(path) ||
		len(path) > maximumSealedAbsentLegacyPathBytes || strings.ContainsRune(path, 0) ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	components, err := sealedAbsentLegacyRelativeComponents(path, filepath.VolumeName(path))
	return err == nil && len(components) > 0 && len(components) <= maximumSealedAbsentLegacyComponents
}

func validateSealedAbsentLegacyLayout(path, ancestorPath string, suffix []string) error {
	if !validSealedAbsentLegacyPath(path) || ancestorPath == "" ||
		!filepath.IsAbs(ancestorPath) || filepath.Clean(ancestorPath) != ancestorPath ||
		len(suffix) == 0 || len(suffix) > maximumSealedAbsentLegacyComponents {
		return errors.New("sealed absent legacy root layout is invalid")
	}
	for _, component := range suffix {
		if !validSealedAbsentLegacyComponent(component) {
			return errors.New("sealed absent legacy root suffix is invalid")
		}
	}
	relative, err := filepath.Rel(ancestorPath, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errors.Join(errors.New("sealed absent legacy root suffix escapes its ancestor"), err)
	}
	parts := strings.Split(relative, string(os.PathSeparator))
	if len(parts) != len(suffix) {
		return errors.New("sealed absent legacy root suffix is incomplete")
	}
	for index := range parts {
		if parts[index] != suffix[index] {
			return errors.New("sealed absent legacy root suffix changed")
		}
	}
	return nil
}

func validSealedAbsentLegacyComponent(component string) bool {
	return component != "" && component != "." && component != ".." &&
		component == filepath.Base(component) && utf8.ValidString(component) &&
		!strings.ContainsRune(component, 0) &&
		!strings.ContainsRune(component, '/') &&
		(filepath.Separator == '/' || !strings.ContainsRune(component, filepath.Separator))
}

// sealedAbsentLegacyRelativeComponents returns the path portion below volume.
// Platform code performs any stronger component policy required by its native
// namespace before opening a handle.
func sealedAbsentLegacyRelativeComponents(path, volume string) ([]string, error) {
	remainder := strings.TrimPrefix(path, volume)
	remainder = strings.TrimLeft(remainder, `/\`)
	if remainder == "" {
		return nil, errors.New("sealed absent legacy root names a filesystem root")
	}
	parts := strings.FieldsFunc(remainder, func(character rune) bool {
		return character == rune(filepath.Separator)
	})
	if len(parts) == 0 || len(parts) > maximumSealedAbsentLegacyComponents {
		return nil, errors.New("sealed absent legacy root component count is invalid")
	}
	for _, component := range parts {
		if !validSealedAbsentLegacyComponent(component) {
			return nil, errors.New("sealed absent legacy root component is invalid")
		}
	}
	return parts, nil
}
