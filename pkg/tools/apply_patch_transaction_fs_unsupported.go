//go:build !linux

package tools

import (
	"os"
)

type applyPatchTxnPlatformAnchor struct{}

func applyPatchTxnPlatformRuntimeSupport() error {
	return errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformIdentityFromFileInfo(
	os.FileInfo,
	string,
) (applyPatchTxnIdentity, error) {
	return applyPatchTxnIdentity{}, errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformSameFileSnapshot(expected, current os.FileInfo) bool {
	return expected != nil && current != nil && os.SameFile(expected, current) &&
		expected.Mode() == current.Mode() && expected.Size() == current.Size() &&
		expected.ModTime().Equal(current.ModTime())
}

func openApplyPatchTxnPlatformAnchor(string) (
	applyPatchTxnPlatformAnchor,
	applyPatchTxnIdentity,
	error,
) {
	return applyPatchTxnPlatformAnchor{}, applyPatchTxnIdentity{},
		errApplyPatchTransactionUnsupported
}

func closeApplyPatchTxnPlatformAnchor(applyPatchTxnPlatformAnchor) error { return nil }

func applyPatchTxnPlatformAnchorIdentity(applyPatchTxnPlatformAnchor) (
	applyPatchTxnIdentity,
	error,
) {
	return applyPatchTxnIdentity{}, errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformCreateRegular(
	applyPatchTxnPlatformAnchor,
	string,
	os.FileMode,
) (*os.File, applyPatchTxnIdentity, error) {
	return nil, applyPatchTxnIdentity{}, errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformInspectAt(
	applyPatchTxnPlatformAnchor,
	string,
) (applyPatchTxnObjectState, error) {
	return applyPatchTxnObjectState{}, errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformOpenRegular(
	applyPatchTxnPlatformAnchor,
	string,
) (*os.File, os.FileMode, applyPatchTxnIdentity, error) {
	return nil, 0, applyPatchTxnIdentity{}, errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformOpenRegularWrite(
	applyPatchTxnPlatformAnchor,
	string,
	applyPatchTxnIdentity,
) (*os.File, error) {
	return nil, errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformLinkNoReplace(
	applyPatchTxnPlatformAnchor,
	string,
	applyPatchTxnPlatformAnchor,
	string,
) error {
	return errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformRenameNoReplace(
	applyPatchTxnPlatformAnchor,
	string,
	applyPatchTxnPlatformAnchor,
	string,
) error {
	return errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformRemoveExact(
	applyPatchTxnPlatformAnchor,
	string,
	string,
	applyPatchTxnIdentity,
	bool,
	func() error,
) error {
	return errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformMkdir(
	applyPatchTxnPlatformAnchor,
	string,
	os.FileMode,
) (applyPatchTxnIdentity, error) {
	return applyPatchTxnIdentity{}, errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformOpenChildDirectory(
	applyPatchTxnPlatformAnchor,
	string,
) (applyPatchTxnPlatformAnchor, applyPatchTxnIdentity, error) {
	return applyPatchTxnPlatformAnchor{}, applyPatchTxnIdentity{},
		errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformSyncDirectory(applyPatchTxnPlatformAnchor) error {
	return errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformReadDirectoryNames(
	applyPatchTxnPlatformAnchor,
	int,
) ([]string, error) {
	return nil, errApplyPatchTransactionUnsupported
}

func applyPatchTxnPlatformProbeNoReplace(
	applyPatchTxnPlatformAnchor,
	string,
) error {
	return errApplyPatchTransactionUnsupported
}
