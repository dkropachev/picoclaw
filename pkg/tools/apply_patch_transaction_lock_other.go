//go:build !unix && !windows

package tools

import (
	"context"
	"os"
)

func validateApplyPatchTransactionStatePath(string) error {
	return errApplyPatchTransactionUnsupported
}

func validateApplyPatchTransactionStatePathEntry(string, os.FileInfo) error {
	return errApplyPatchTransactionUnsupported
}

func validateApplyPatchTransactionPrivateObject(os.FileInfo, bool) error {
	return errApplyPatchTransactionUnsupported
}

func acquireApplyPatchTransactionFileLock(
	context.Context,
	string,
) (applyPatchTransactionHeldLock, error) {
	return nil, errApplyPatchTransactionUnsupported
}
