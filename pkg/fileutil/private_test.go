package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecureAndValidatePrivatePaths(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := SecurePrivateDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if validateErr := ValidatePrivateDirectory(directory, directoryInfo); validateErr != nil {
		t.Fatal(validateErr)
	}
	file := filepath.Join(directory, "state")
	if writeErr := os.WriteFile(file, []byte("state"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	fileInfo, err := SecurePrivateFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePrivateFile(file, fileInfo); err != nil {
		t.Fatal(err)
	}
}
