//go:build !unix

package pid

import "os"

func openPidFileForPeek(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY, 0)
}
