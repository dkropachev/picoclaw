package tools

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const applyPatchTransactionRandomBytes = 16

type applyPatchTxnTargetLayout struct {
	anchorPath string
	components []string
}

type applyPatchTxnEndpoint struct {
	anchor   *applyPatchTxnAnchor
	basename string
	state    applyPatchTxnObjectState
}

func (endpoint *applyPatchTxnEndpoint) Close() error {
	if endpoint == nil || endpoint.anchor == nil {
		return nil
	}
	return endpoint.anchor.Close()
}

func openApplyPatchTxnExistingRegular(
	path string,
	expected os.FileInfo,
) (*applyPatchTxnEndpoint, error) {
	if path == "" || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return nil, errors.New("apply-patch transaction endpoint is invalid")
	}
	basename := filepath.Base(path)
	if err := validateApplyPatchTxnBasename(basename); err != nil {
		return nil, err
	}
	expectedIdentity, err := applyPatchTxnIdentityFromFileInfo(expected, "regular")
	if err != nil {
		return nil, err
	}
	currentInfo, err := os.Lstat(path)
	if err != nil || !applyPatchTxnPlatformSameFileSnapshot(expected, currentInfo) {
		return nil, errors.New("apply-patch transaction source changed")
	}
	anchor, err := openApplyPatchTxnAnchor(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	state, err := applyPatchTxnInspectAt(anchor, basename)
	if err != nil || !state.Identity.equal(expectedIdentity) ||
		state.Identity.Kind != "regular" {
		_ = anchor.Close()
		return nil, errors.Join(
			errors.New("apply-patch transaction source changed"),
			err,
		)
	}
	afterInfo, err := os.Lstat(path)
	if err != nil || !applyPatchTxnPlatformSameFileSnapshot(expected, afterInfo) {
		_ = anchor.Close()
		return nil, errors.New("apply-patch transaction source changed")
	}
	return &applyPatchTxnEndpoint{
		anchor: anchor, basename: basename, state: state,
	}, nil
}

func newApplyPatchTxnPrivateName(prefix string) (string, error) {
	if prefix == "" || strings.ContainsAny(prefix, "/\\\x00") {
		return "", errors.New("apply-patch transaction private-name prefix is invalid")
	}
	var random [applyPatchTransactionRandomBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create apply-patch transaction private name: %w", err)
	}
	return ".picoclaw-apply-patch-" + prefix + "-" + hex.EncodeToString(random[:]), nil
}

func newApplyPatchTxnID() (string, error) {
	var random [applyPatchTransactionRandomBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create apply-patch transaction ID: %w", err)
	}
	return hex.EncodeToString(random[:]), nil
}

// resolveApplyPatchTxnTargetLayout returns the nearest existing directory and
// the absent path components to construct below it. The plan already carries
// a canonical target; this second no-follow walk is the transaction layer's
// rooted staging boundary.
func resolveApplyPatchTxnTargetLayout(target string) (applyPatchTxnTargetLayout, error) {
	if target == "" || !filepath.IsAbs(target) || target != filepath.Clean(target) ||
		strings.ContainsRune(target, '\x00') {
		return applyPatchTxnTargetLayout{}, errors.New("apply-patch transaction target is invalid")
	}
	components := []string{filepath.Base(target)}
	current := filepath.Dir(target)
	for {
		info, err := os.Lstat(current)
		switch {
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			return applyPatchTxnTargetLayout{}, errors.New("apply-patch transaction target ancestor is a symlink")
		case err == nil && !info.IsDir():
			return applyPatchTxnTargetLayout{}, errors.New("apply-patch transaction target ancestor is not a directory")
		case err == nil:
			for _, component := range components {
				validationErr := validateApplyPatchTxnBasename(component)
				if validationErr != nil {
					return applyPatchTxnTargetLayout{}, validationErr
				}
			}
			return applyPatchTxnTargetLayout{
				anchorPath: current,
				components: append([]string(nil), components...),
			}, nil
		case !os.IsNotExist(err):
			return applyPatchTxnTargetLayout{}, fmt.Errorf("inspect apply-patch transaction target ancestor: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return applyPatchTxnTargetLayout{}, errors.New(
				"apply-patch transaction target has no existing directory ancestor",
			)
		}
		components = append([]string{filepath.Base(current)}, components...)
		current = parent
	}
}

func applyPatchTxnLayoutGroupKey(layout applyPatchTxnTargetLayout) (string, error) {
	if layout.anchorPath == "" || len(layout.components) == 0 {
		return "", errors.New("apply-patch transaction target layout is incomplete")
	}
	if err := validateApplyPatchTxnBasename(layout.components[0]); err != nil {
		return "", err
	}
	return filepath.Clean(layout.anchorPath) + "\x00" + layout.components[0], nil
}
