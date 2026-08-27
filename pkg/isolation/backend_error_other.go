//go:build !linux

package isolation

import "fmt"

func linuxBackendUnavailableError(err error) error {
	return fmt.Errorf("linux isolation backend is unavailable: %w", err)
}
