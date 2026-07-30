//go:build !unix && !windows

package api

import (
	"os"
)

func openAgentDefinitionNoFollow(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errAgentDefinitionNotRegular
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, errAgentDefinitionNotRegular
	}
	return file, nil
}

func openAgentCapabilityCatalogDirectory(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, errAgentDefinitionNotRegular
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !after.IsDir() || !os.SameFile(before, after) {
		file.Close()
		return nil, errAgentDefinitionNotRegular
	}
	return file, nil
}
