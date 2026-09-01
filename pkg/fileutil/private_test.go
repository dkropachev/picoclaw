package fileutil

import (
	"os"
	"path/filepath"
	"runtime"
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

func TestPrivatePathValidationRejectsMissingWrongTypeModeAndIdentity(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if _, err := SecurePrivateDirectory(missing); err == nil {
		t.Fatal("missing private directory was accepted")
	}
	if err := ValidatePrivateDirectory(missing, nil); err == nil {
		t.Fatal("missing private directory validation succeeded")
	}
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	regularInfo, err := os.Lstat(regular)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SecurePrivateDirectory(regular); err == nil {
		t.Fatal("regular file was accepted as a private directory")
	}
	if err := ValidatePrivateDirectory(root, regularInfo); err == nil {
		t.Fatal("changed private directory identity was accepted")
	}
	if _, err := SecurePrivateFile(missing); err == nil {
		t.Fatal("missing private file was accepted")
	}
	if _, err := SecurePrivateFile(root); err == nil {
		t.Fatal("directory was accepted as a private file")
	}
	if err := ValidatePrivateFile(missing, nil); err == nil {
		t.Fatal("missing private file validation succeeded")
	}
	if err := ValidatePrivateFile(regular, nil); err == nil {
		t.Fatal("nil private file identity was accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(root); err != nil || ValidatePrivateDirectory(root, info) == nil {
			t.Fatalf("public directory validation = info:%#v error:%v", info, err)
		}
		if err := os.Chmod(regular, 0o644); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Lstat(regular); err != nil || ValidatePrivateFile(regular, info) == nil {
			t.Fatalf("public file validation = info:%#v error:%v", info, err)
		}
	}
}
