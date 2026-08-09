//go:build !unix

package localci

import (
	"fmt"
	"os"
)

func openDiscoveryFile(rootPath, relative string) (*os.File, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("discovery input is not a regular file")
	}
	return root.Open(relative)
}
