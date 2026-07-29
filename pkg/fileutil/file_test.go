package fileutil

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestWriteFileAtomic_Basic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	data := []byte("hello picoclaw")

	err := WriteFileAtomic(path, data, 0o644)
	if err != nil {
		t.Fatalf("WriteFileAtomic failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q, want %q", got, data)
	}
}

func TestWriteFileAtomic_Permissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.txt")

	err := WriteFileAtomic(path, []byte("secret"), 0o600)
	if err != nil {
		t.Fatalf("WriteFileAtomic failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	// On Unix, check file mode (ignoring directory bits)
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("permissions = %o, want %o", got, 0o600)
	}
}

func TestWriteFileAtomic_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "overwrite.txt")

	// Write initial content
	if err := WriteFileAtomic(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("first write failed: %v", err)
	}

	// Overwrite
	if err := WriteFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("got %q after overwrite, want %q", got, "new")
	}
}

func TestWriteFileAtomic_EmptyData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	err := WriteFileAtomic(path, []byte{}, 0o644)
	if err != nil {
		t.Fatalf("WriteFileAtomic with empty data failed: %v", err)
	}

	got, _ := os.ReadFile(path)
	if len(got) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(got))
	}
}

func TestWriteFileAtomic_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "deep.txt")

	err := WriteFileAtomic(path, []byte("deep"), 0o644)
	if err != nil {
		t.Fatalf("WriteFileAtomic with nested dirs failed: %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "deep" {
		t.Errorf("got %q, want %q", got, "deep")
	}
}

func TestMkdirAllDurable_FirstOperationCreatesEveryParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "first", "second", "third")
	if err := MkdirAllDurable(dir, 0o750); err != nil {
		t.Fatalf("MkdirAllDurable() error = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("created path mode = %v, want directory", info.Mode())
	}
	if err := MkdirAllDurable(dir, 0o700); err != nil {
		t.Fatalf("MkdirAllDurable(existing) error = %v", err)
	}
}

func TestRemoveDurable_FileAndEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state.json")
	if err := os.WriteFile(file, []byte("state"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := RemoveDurable(file); err != nil {
		t.Fatalf("RemoveDurable(file) error = %v", err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatalf("Lstat(removed file) error = %v, want not exist", err)
	}

	dir := filepath.Join(root, "empty")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := RemoveDurable(dir); err != nil {
		t.Fatalf("RemoveDurable(directory) error = %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("Lstat(removed directory) error = %v, want not exist", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("durable remove left entries = %v", entries)
	}
}

func TestRemoveDurable_PreservesRemoveErrors(t *testing.T) {
	root := t.TempDir()
	if err := RemoveDurable(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatalf("RemoveDurable(missing) error = %v, want not exist", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := RemoveDurable(file + string(filepath.Separator)); err == nil {
		t.Fatal("RemoveDurable(file with trailing separator) error = nil, want error")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("Stat(file after rejected remove) error = %v", err)
	}
	nonEmpty := filepath.Join(root, "non-empty")
	if err := os.Mkdir(nonEmpty, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmpty, "child"), nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := RemoveDurable(nonEmpty); err == nil {
		t.Fatal("RemoveDurable(non-empty directory) error = nil, want error")
	}
	if _, err := os.Stat(nonEmpty); err != nil {
		t.Fatalf("Stat(non-empty directory) error = %v", err)
	}
}

func TestDurablePathOperationsRejectEmptyPath(t *testing.T) {
	if err := MkdirAllDurable("", 0o700); err == nil {
		t.Fatal("MkdirAllDurable(empty) error = nil, want error")
	}
	if err := RemoveDurable(""); err == nil {
		t.Fatal("RemoveDurable(empty) error = nil, want error")
	}
}

func TestWriteFileAtomic_NoTempFileOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clean.txt")

	if err := WriteFileAtomic(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic failed: %v", err)
	}

	// Verify no temp files remain
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "clean.txt" {
			t.Errorf("unexpected file remaining: %s", e.Name())
		}
	}
}

func TestWriteFileAtomic_DoesNotRemoveReusedTempNameAfterReplace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	injected := errors.New("injected directory sync failure")
	var replacedSource string
	const sentinel = "new owner"

	err := writeFileAtomicWithHooks(
		target,
		[]byte("target data"),
		0o600,
		func(source, target string) error {
			replacedSource = source
			return replaceFile(source, target)
		},
		func(string) error {
			if writeErr := os.WriteFile(replacedSource, []byte(sentinel), 0o600); writeErr != nil {
				t.Fatalf("create reused temp name: %v", writeErr)
			}
			return injected
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("WriteFileAtomic() error = %v, want injected sync failure", err)
	}
	if data, readErr := os.ReadFile(replacedSource); readErr != nil {
		t.Fatalf("reused temp name was removed: %v", readErr)
	} else if string(data) != sentinel {
		t.Fatalf("reused temp data = %q, want %q", data, sentinel)
	}
	if data, readErr := os.ReadFile(target); readErr != nil {
		t.Fatalf("read replaced target: %v", readErr)
	} else if string(data) != "target data" {
		t.Fatalf("target data = %q, want target data", data)
	}
}

func TestWriteFileAtomic_LargeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "large.bin")

	// 1MB of data
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 256)
	}

	if err := WriteFileAtomic(path, data, 0o644); err != nil {
		t.Fatalf("WriteFileAtomic with large file failed: %v", err)
	}

	got, _ := os.ReadFile(path)
	if len(got) != len(data) {
		t.Errorf("file size = %d, want %d", len(got), len(data))
	}
}

func TestWriteFileAtomic_Concurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.txt")

	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			data := []byte(string(rune('A' + n)))
			if err := WriteFileAtomic(path, data, 0o644); err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent write error: %v", err)
	}

	// File should exist and contain exactly 1 byte (last writer wins)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after concurrent writes failed: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 byte after concurrent writes, got %d", len(got))
	}
}

func TestWriteFileAtomic_InvalidPath(t *testing.T) {
	// /dev/null/impossible is not a valid path on any OS
	err := WriteFileAtomic("/dev/null/impossible/file.txt", []byte("data"), 0o644)
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

func TestCopyFileCopiesContentAndPermissions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.txt")
	dst := filepath.Join(dir, "nested", "dest.txt")

	if err := os.WriteFile(src, []byte("copied"), 0o600); err != nil {
		t.Fatalf("WriteFile source failed: %v", err)
	}
	if err := CopyFile(src, dst, 0o640); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile destination failed: %v", err)
	}
	if string(got) != "copied" {
		t.Fatalf("destination content = %q, want copied", got)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat destination failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("destination permissions = %o, want %o", got, 0o640)
	}
}
