//go:build !unix && !windows

package tools

import (
	"fmt"
	"os"
)

func openApplyPatchSource(string) (*os.File, error) {
	return nil, fmt.Errorf("safe apply-patch source reads are unsupported on this platform")
}
