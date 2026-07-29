//go:build !unix && !windows

package config

import (
	"os"
	"path/filepath"
)

func lockConfigMutationFile(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return func() {}, nil
}
