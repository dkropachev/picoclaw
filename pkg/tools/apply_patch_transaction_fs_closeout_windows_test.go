//go:build windows

package tools

import "errors"

func createApplyPatchTxnCloseoutFIFO(string) error {
	return errors.ErrUnsupported
}
