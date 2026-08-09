//go:build !linux

package localci

import "fmt"

func newPlatformSandbox(SandboxConfig, [32]byte) (Sandbox, error) {
	return nil, fmt.Errorf("%w: mandatory local CI isolation is implemented only on Linux", ErrSandboxUnavailable)
}
