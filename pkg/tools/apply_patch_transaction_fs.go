package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var errApplyPatchTransactionUnsupported = errors.New(
	"apply-patch transaction unsupported on this platform",
)

func requireApplyPatchTxnRuntimeSupport() error {
	return applyPatchTxnPlatformRuntimeSupport()
}

// applyPatchTxnIdentity is a serializable identity for a pinned filesystem
// object. It is revalidation evidence only: durable ownership is proved by a
// retained hard-link witness, never by an inode/file ID alone.
type applyPatchTxnIdentity struct {
	Device uint64 `json:"device"`
	File   uint64 `json:"file"`
	Kind   string `json:"kind"`
}

type applyPatchTxnObjectState struct {
	Identity applyPatchTxnIdentity
	Mode     os.FileMode
	Links    uint64
	Size     int64
}

func (identity applyPatchTxnIdentity) valid(expectedKind string) bool {
	return identity.Device != 0 && identity.File != 0 && identity.Kind == expectedKind
}

func (identity applyPatchTxnIdentity) equal(other applyPatchTxnIdentity) bool {
	return identity.Device == other.Device && identity.File == other.File &&
		identity.Kind == other.Kind
}

func applyPatchTxnWorkspaceIdentityDigest(identity applyPatchTxnIdentity) (string, error) {
	if !identity.valid("directory") {
		return "", errors.New("apply-patch transaction workspace identity is invalid")
	}
	data := make([]byte, 16+len(identity.Kind))
	binary.BigEndian.PutUint64(data[:8], identity.Device)
	binary.BigEndian.PutUint64(data[8:16], identity.File)
	copy(data[16:], identity.Kind)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func applyPatchTxnIdentityFromFileInfo(
	info os.FileInfo,
	expectedKind string,
) (applyPatchTxnIdentity, error) {
	if info == nil {
		return applyPatchTxnIdentity{}, errors.New("apply-patch transaction file identity is unavailable")
	}
	return applyPatchTxnPlatformIdentityFromFileInfo(info, expectedKind)
}

type applyPatchTxnAnchor struct {
	canonical string
	identity  applyPatchTxnIdentity
	platform  applyPatchTxnPlatformAnchor
	closed    bool
}

func openApplyPatchTxnAnchor(path string) (*applyPatchTxnAnchor, error) {
	if path == "" || path != strings.TrimSpace(path) ||
		!filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return nil, errors.New("apply-patch transaction anchor is invalid")
	}
	canonical := filepath.Clean(path)
	platform, identity, err := openApplyPatchTxnPlatformAnchor(canonical)
	if err != nil {
		return nil, err
	}
	return &applyPatchTxnAnchor{
		canonical: canonical,
		identity:  identity,
		platform:  platform,
	}, nil
}

func (anchor *applyPatchTxnAnchor) Close() error {
	if anchor == nil || anchor.closed {
		return nil
	}
	anchor.closed = true
	return closeApplyPatchTxnPlatformAnchor(anchor.platform)
}

func (anchor *applyPatchTxnAnchor) revalidate() error {
	if anchor == nil || anchor.closed || !anchor.identity.valid("directory") {
		return errors.New("apply-patch transaction anchor is unavailable")
	}
	current, err := applyPatchTxnPlatformAnchorIdentity(anchor.platform)
	if err != nil {
		return err
	}
	if !current.equal(anchor.identity) {
		return errors.New("apply-patch transaction anchor changed")
	}
	namedPlatform, namedIdentity, err := openApplyPatchTxnPlatformAnchor(anchor.canonical)
	if err != nil {
		return errors.New("apply-patch transaction named anchor changed")
	}
	if closeErr := closeApplyPatchTxnPlatformAnchor(namedPlatform); closeErr != nil {
		return closeErr
	}
	if !namedIdentity.equal(anchor.identity) {
		return errors.New("apply-patch transaction named anchor changed")
	}
	return nil
}

func validateApplyPatchTxnBasename(name string) error {
	if name == "" || name == "." || name == ".." ||
		name != filepath.Base(name) || strings.ContainsRune(name, '\x00') ||
		strings.ContainsRune(name, '/') ||
		filepath.Separator != '/' && strings.ContainsRune(name, filepath.Separator) {
		return errors.New("apply-patch transaction basename is invalid")
	}
	return nil
}

func applyPatchTxnCreateRegular(
	anchor *applyPatchTxnAnchor,
	name string,
	mode os.FileMode,
) (*os.File, applyPatchTxnIdentity, error) {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return nil, applyPatchTxnIdentity{}, err
	}
	if err := validateApplyPatchTxnBasename(name); err != nil {
		return nil, applyPatchTxnIdentity{}, err
	}
	return applyPatchTxnPlatformCreateRegular(anchor.platform, name, mode.Perm())
}

func applyPatchTxnWriteRegular(
	file *os.File,
	data []byte,
	mode os.FileMode,
	preserveMode bool,
) error {
	return applyPatchTxnWriteRegularContext(
		context.Background(),
		file,
		data,
		mode,
		preserveMode,
	)
}

func applyPatchTxnWriteRegularContext(
	ctx context.Context,
	file *os.File,
	data []byte,
	mode os.FileMode,
	preserveMode bool,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if file == nil {
		return errors.New("apply-patch transaction stage is unavailable")
	}
	const chunkSize = 64 << 10
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+chunkSize, len(data))
		written, err := file.Write(data[offset:end])
		if err != nil {
			return fmt.Errorf("write apply-patch transaction stage: %w", err)
		}
		if written != end-offset {
			return io.ErrShortWrite
		}
		offset = end
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if preserveMode {
		if err := file.Chmod(mode.Perm()); err != nil {
			return fmt.Errorf("set apply-patch transaction stage permissions: %w", err)
		}
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync apply-patch transaction stage: %w", err)
	}
	return nil
}

func applyPatchTxnIdentityAt(
	anchor *applyPatchTxnAnchor,
	name string,
) (applyPatchTxnIdentity, os.FileMode, error) {
	state, err := applyPatchTxnInspectAt(anchor, name)
	return state.Identity, state.Mode, err
}

func applyPatchTxnInspectAt(
	anchor *applyPatchTxnAnchor,
	name string,
) (applyPatchTxnObjectState, error) {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return applyPatchTxnObjectState{}, err
	}
	if err := validateApplyPatchTxnBasename(name); err != nil {
		return applyPatchTxnObjectState{}, err
	}
	return applyPatchTxnPlatformInspectAt(anchor.platform, name)
}

func applyPatchTxnReadRegular(
	anchor *applyPatchTxnAnchor,
	name string,
	limit int64,
) ([]byte, os.FileMode, applyPatchTxnIdentity, error) {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return nil, 0, applyPatchTxnIdentity{}, err
	}
	if err := validateApplyPatchTxnBasename(name); err != nil {
		return nil, 0, applyPatchTxnIdentity{}, err
	}
	if limit < 0 {
		return nil, 0, applyPatchTxnIdentity{}, errors.New("apply-patch transaction read limit is invalid")
	}
	file, mode, identity, err := applyPatchTxnPlatformOpenRegular(anchor.platform, name)
	if err != nil {
		return nil, 0, applyPatchTxnIdentity{}, err
	}
	defer file.Close()
	reader := io.Reader(file)
	if limit < int64(^uint64(0)>>1) {
		reader = io.LimitReader(file, limit+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, 0, applyPatchTxnIdentity{}, fmt.Errorf("read apply-patch transaction file: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, 0, applyPatchTxnIdentity{}, errors.New("apply-patch transaction file exceeds limit")
	}
	return data, mode, identity, nil
}

func applyPatchTxnResumeRegularContext(
	ctx context.Context,
	anchor *applyPatchTxnAnchor,
	name string,
	expected applyPatchTxnIdentity,
	data []byte,
	mode os.FileMode,
) error {
	existing, observedMode, identity, err := applyPatchTxnReadRegular(
		anchor,
		name,
		int64(len(data)),
	)
	if err != nil || !identity.equal(expected) || len(existing) > len(data) ||
		!bytes.Equal(existing, data[:len(existing)]) {
		return errors.Join(
			errors.New("apply-patch transaction restore stage content conflict"),
			err,
		)
	}
	if observedMode.Perm() != 0o600 &&
		(len(existing) != len(data) || observedMode.Perm() != mode.Perm()) {
		return errors.New("apply-patch transaction restore stage mode conflict")
	}
	file, err := applyPatchTxnPlatformOpenRegularWrite(
		anchor.platform,
		name,
		expected,
	)
	if err != nil {
		return err
	}
	if _, err := file.Seek(int64(len(existing)), io.SeekStart); err != nil {
		_ = file.Close()
		return err
	}
	writeErr := applyPatchTxnWriteRegularContext(
		ctx,
		file,
		data[len(existing):],
		mode,
		true,
	)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func applyPatchTxnLinkNoReplace(
	from *applyPatchTxnAnchor,
	fromName string,
	to *applyPatchTxnAnchor,
	toName string,
) error {
	if err := requireApplyPatchTxnAnchors(from, to); err != nil {
		return err
	}
	if err := validateApplyPatchTxnBasename(fromName); err != nil {
		return err
	}
	if err := validateApplyPatchTxnBasename(toName); err != nil {
		return err
	}
	return applyPatchTxnPlatformLinkNoReplace(
		from.platform, fromName, to.platform, toName,
	)
}

// applyPatchTxnLinkWitness creates a no-replace witness and grants ownership
// authority only after the new link is proven to name the expected regular
// file. A raced link is removed only by its exact observed identity.
func applyPatchTxnLinkWitness(
	from *applyPatchTxnAnchor,
	fromName string,
	expected applyPatchTxnIdentity,
	expectedLinks uint64,
	to *applyPatchTxnAnchor,
	toName string,
	toRemovalName string,
) error {
	if !expected.valid("regular") {
		return errors.New("apply-patch transaction witness identity is invalid")
	}
	if err := applyPatchTxnLinkNoReplace(from, fromName, to, toName); err != nil {
		return err
	}
	source, sourceErr := applyPatchTxnInspectAt(from, fromName)
	witness, witnessErr := applyPatchTxnInspectAt(to, toName)
	if sourceErr == nil && witnessErr == nil && source.Identity.equal(expected) &&
		witness.Identity.equal(expected) && witness.Identity.Kind == "regular" &&
		source.Links == expectedLinks && witness.Links == expectedLinks {
		return nil
	}
	var cleanupErr error
	if sourceErr == nil && witnessErr == nil &&
		witness.Identity.equal(source.Identity) {
		cleanupErr = applyPatchTxnRemoveExact(
			to,
			toName,
			toRemovalName,
			witness.Identity,
			false,
		)
	}
	return errors.Join(
		errors.New("apply-patch transaction witness did not bind the expected file"),
		sourceErr,
		witnessErr,
		cleanupErr,
	)
}

func applyPatchTxnRenameNoReplace(
	from *applyPatchTxnAnchor,
	fromName string,
	to *applyPatchTxnAnchor,
	toName string,
) error {
	if err := requireApplyPatchTxnAnchors(from, to); err != nil {
		return err
	}
	if err := validateApplyPatchTxnBasename(fromName); err != nil {
		return err
	}
	if err := validateApplyPatchTxnBasename(toName); err != nil {
		return err
	}
	return applyPatchTxnPlatformRenameNoReplace(
		from.platform, fromName, to.platform, toName,
	)
}

func applyPatchTxnRemoveExact(
	anchor *applyPatchTxnAnchor,
	name string,
	removalName string,
	expected applyPatchTxnIdentity,
	directory bool,
	afterQuarantine ...func() error,
) error {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return err
	}
	if err := validateApplyPatchTxnBasename(name); err != nil {
		return err
	}
	if err := validateApplyPatchTransactionRandomPrivateName(
		removalName,
		"remove",
	); err != nil || removalName == name {
		return errors.New("apply-patch transaction removal name is invalid")
	}
	var hook func() error
	if len(afterQuarantine) > 0 {
		hook = afterQuarantine[0]
	}
	return applyPatchTxnPlatformRemoveExact(
		anchor.platform,
		name,
		removalName,
		expected,
		directory,
		hook,
	)
}

// applyPatchTxnQuarantineExact atomically displaces a public basename without
// replacement and verifies the displaced object before granting cleanup or
// rollback authority. If verification fails it attempts a no-replace restore;
// a raced replacement is never overwritten.
func applyPatchTxnQuarantineExact(
	anchor *applyPatchTxnAnchor,
	name string,
	quarantine string,
	expected applyPatchTxnIdentity,
) error {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return err
	}
	if err := validateApplyPatchTxnBasename(name); err != nil {
		return err
	}
	if err := validateApplyPatchTxnBasename(quarantine); err != nil {
		return err
	}
	if err := applyPatchTxnPlatformRenameNoReplace(
		anchor.platform,
		name,
		anchor.platform,
		quarantine,
	); err != nil {
		return err
	}
	current, inspectErr := applyPatchTxnPlatformInspectAt(
		anchor.platform,
		quarantine,
	)
	if inspectErr == nil && current.Identity.equal(expected) {
		return nil
	}
	restoreErr := applyPatchTxnPlatformRenameNoReplace(
		anchor.platform,
		quarantine,
		anchor.platform,
		name,
	)
	return errors.Join(
		errors.New("apply-patch transaction object changed before quarantine"),
		inspectErr,
		wrapApplyPatchTxnRestoreError(restoreErr),
	)
}

func wrapApplyPatchTxnRestoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("restore apply-patch transaction quarantine: %w", err)
}

func applyPatchTxnMkdir(
	anchor *applyPatchTxnAnchor,
	name string,
	mode os.FileMode,
) (applyPatchTxnIdentity, error) {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return applyPatchTxnIdentity{}, err
	}
	if err := validateApplyPatchTxnBasename(name); err != nil {
		return applyPatchTxnIdentity{}, err
	}
	return applyPatchTxnPlatformMkdir(anchor.platform, name, mode.Perm())
}

func applyPatchTxnOpenChildDirectory(
	anchor *applyPatchTxnAnchor,
	name string,
) (*applyPatchTxnAnchor, error) {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return nil, err
	}
	if err := validateApplyPatchTxnBasename(name); err != nil {
		return nil, err
	}
	platform, identity, err := applyPatchTxnPlatformOpenChildDirectory(anchor.platform, name)
	if err != nil {
		return nil, err
	}
	return &applyPatchTxnAnchor{
		canonical: filepath.Join(anchor.canonical, name),
		identity:  identity,
		platform:  platform,
	}, nil
}

func applyPatchTxnSyncDirectory(anchor *applyPatchTxnAnchor) error {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return err
	}
	return applyPatchTxnPlatformSyncDirectory(anchor.platform)
}

func applyPatchTxnReadDirectoryNames(
	anchor *applyPatchTxnAnchor,
	limit int,
) ([]string, error) {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return nil, err
	}
	if limit < 0 || limit > applyPatchTransactionMaxEntries {
		return nil, errors.New("apply-patch transaction directory listing limit is invalid")
	}
	names, err := applyPatchTxnPlatformReadDirectoryNames(anchor.platform, limit)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func applyPatchTxnProbeNoReplace(
	anchor *applyPatchTxnAnchor,
	name string,
	expected applyPatchTxnIdentity,
) error {
	if err := requireApplyPatchTxnAnchor(anchor); err != nil {
		return err
	}
	if err := validateApplyPatchTxnBasename(name); err != nil {
		return err
	}
	before, _, err := applyPatchTxnIdentityAt(anchor, name)
	if err != nil || !before.equal(expected) {
		return errors.Join(
			errors.New("apply-patch transaction probe object changed"),
			err,
		)
	}
	probeErr := applyPatchTxnPlatformProbeNoReplace(anchor.platform, name)
	if probeErr != nil {
		return probeErr
	}
	after, _, err := applyPatchTxnIdentityAt(anchor, name)
	if err != nil || !after.equal(expected) {
		return errors.Join(
			errors.New("apply-patch transaction probe changed its object"),
			err,
		)
	}
	return nil
}

func requireApplyPatchTxnAnchor(anchor *applyPatchTxnAnchor) error {
	if anchor == nil {
		return errors.New("apply-patch transaction anchor is unavailable")
	}
	return anchor.revalidate()
}

func requireApplyPatchTxnAnchors(anchors ...*applyPatchTxnAnchor) error {
	for _, anchor := range anchors {
		if err := requireApplyPatchTxnAnchor(anchor); err != nil {
			return err
		}
	}
	return nil
}
