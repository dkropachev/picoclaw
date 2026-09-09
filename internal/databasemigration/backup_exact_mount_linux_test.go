//go:build linux

package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseLinuxExactBackupMountID(t *testing.T) {
	if got, err := parseLinuxExactBackupMountID([]byte("pos:\t0\nflags:\t0100000\nmnt_id:\t4217\n")); err != nil || got != 4217 {
		t.Fatalf("parsed mount ID = %d, %v", got, err)
	}
	for _, payload := range [][]byte{
		nil,
		[]byte("pos:\t0\n"),
		[]byte("mnt_id:\t0\n"),
		[]byte("mnt_id:\tnot-a-number\n"),
	} {
		if got, err := parseLinuxExactBackupMountID(payload); got != 0 ||
			!errors.Is(err, errExactBackupMountUnknown) {
			t.Errorf("parse mount ID(%q) = %d, %v", payload, got, err)
		}
	}
}

func TestOpenExactBackupChildRejectsMountTransition(t *testing.T) {
	rootFile, err := os.Open(string(os.PathSeparator))
	if err != nil {
		t.Skipf("open filesystem root: %v", err)
	}
	defer rootFile.Close()
	procFile, err := os.Open(filepath.Join(string(os.PathSeparator), "proc"))
	if err != nil {
		t.Skipf("open proc mount: %v", err)
	}
	defer procFile.Close()
	rootMount, rootErr := linuxExactBackupMountIdentity(rootFile)
	procMount, procErr := linuxExactBackupMountIdentity(procFile)
	if rootErr != nil || procErr != nil || validateExactBackupMountIdentity(rootMount, procMount) == nil {
		t.Skipf("distinct proc mount unavailable: root=%#v/%v proc=%#v/%v", rootMount, rootErr, procMount, procErr)
	}

	root, err := os.OpenRoot(string(os.PathSeparator))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openExactBackupChild(root, filepath.Join("proc", "version"))
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, errExactBackupMountBoundary) {
		t.Fatalf("cross-mount open = %#v, %v", file, err)
	}
}

func TestValidateExactBackupRootMount(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	rootFile, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := validateExactBackupRootMount(directory, rootFile); err != nil {
		_ = rootFile.Close()
		_ = root.Close()
		t.Fatalf("same-mount root rejected: %v", err)
	}
	if err := rootFile.Close(); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	procRoot, err := os.OpenRoot(filepath.Join(string(os.PathSeparator), "proc"))
	if err != nil {
		t.Skipf("open proc mount: %v", err)
	}
	defer procRoot.Close()
	procFile, err := procRoot.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer procFile.Close()
	filesystemRoot, err := os.Open(string(os.PathSeparator))
	if err != nil {
		t.Skipf("open filesystem root: %v", err)
	}
	defer filesystemRoot.Close()
	rootMount, rootErr := linuxExactBackupMountIdentity(filesystemRoot)
	procMount, procErr := linuxExactBackupMountIdentity(procFile)
	if rootErr != nil || procErr != nil || validateExactBackupMountIdentity(rootMount, procMount) == nil {
		t.Skipf("distinct proc mount unavailable: root=%#v/%v proc=%#v/%v", rootMount, rootErr, procMount, procErr)
	}
	if err := validateExactBackupRootMount(
		filepath.Join(string(os.PathSeparator), "proc"), procFile,
	); !errors.Is(err, errExactBackupMountBoundary) {
		t.Fatalf("mounted archive root validation = %v", err)
	}
}

func TestOpenExactBackupChildAllowsSameMount(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "file"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openExactBackupChild(root, "file")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
