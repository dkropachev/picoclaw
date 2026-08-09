//go:build unix

package localci

import (
	"os"

	"golang.org/x/sys/unix"
)

func openDiscoveryFile(rootPath, relative string) (*os.File, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile(relative, os.O_RDONLY|unix.O_NONBLOCK, 0)
	closeErr := root.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		_ = file.Close()
		return nil, closeErr
	}
	return file, nil
}
