package sqlitestore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type replacePathOnCloseSQLiteFile struct {
	sqliteFile
	path     string
	replaced *bool
}

func (file replacePathOnCloseSQLiteFile) Close() error {
	closeErr := file.sqliteFile.Close()
	removeErr := os.Remove(file.path)
	mkdirErr := os.Mkdir(file.path, 0o700)
	if removeErr == nil && mkdirErr == nil {
		*file.replaced = true
	}
	return errors.Join(closeErr, removeErr, mkdirErr)
}

func TestSecureSQLiteFilesRoutesFinalIdentityThroughPrivateFileValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	replaced := false
	original := openSQLiteFile
	swapTestHook(t, &openSQLiteFile, func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
		file, err := original(candidate, flag, mode)
		if err != nil || candidate != path {
			return file, err
		}
		return replacePathOnCloseSQLiteFile{sqliteFile: file, path: path, replaced: &replaced}, nil
	})
	if err := secureSQLiteFiles(path); err == nil {
		t.Fatal("SQLite path replacement after close was accepted")
	}
	if !replaced {
		t.Fatal("test did not replace the SQLite path after closing its verified handle")
	}
}

func TestSecureSQLiteFilesAllowsOnlyVanishedCompanionsAtFinalValidation(t *testing.T) {
	t.Run("WAL companion", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		wal := path + "-wal"
		for _, candidate := range []string{path, wal} {
			if err := os.WriteFile(candidate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		original := securePrivateSQLiteFile
		swapTestHook(t, &securePrivateSQLiteFile, func(candidate string) (os.FileInfo, error) {
			if candidate != wal {
				return original(candidate)
			}
			if err := os.Remove(candidate); err != nil {
				return nil, err
			}
			return nil, os.ErrNotExist
		})
		if err := secureSQLiteFiles(path); err != nil {
			t.Fatalf("vanished WAL companion: %v", err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("primary database disappeared: %v", err)
		}
		if _, err := os.Lstat(wal); !os.IsNotExist(err) {
			t.Fatalf("WAL companion still exists: %v", err)
		}
	})

	t.Run("primary database", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		original := securePrivateSQLiteFile
		swapTestHook(t, &securePrivateSQLiteFile, func(candidate string) (os.FileInfo, error) {
			if candidate != path {
				return original(candidate)
			}
			if err := os.Remove(candidate); err != nil {
				return nil, err
			}
			return nil, os.ErrNotExist
		})
		if err := secureSQLiteFiles(path); !os.IsNotExist(err) {
			t.Fatalf("vanished primary database error = %v", err)
		}
	})
}

func TestLegacyArchiveDirectoryCreationRoutesThroughPrivateValidation(t *testing.T) {
	tests := []struct {
		name string
		call func(*testing.T, string) error
	}{
		{
			name: "archive root",
			call: func(t *testing.T, root string) error {
				return ensureLegacyArchiveRoot(root, filepath.Join(root, "legacy-json", "component-v1"))
			},
		},
		{
			name: "archive parent",
			call: func(t *testing.T, root string) error {
				return ensureArchiveParent(root, filepath.Join(root, "nested", "archive", "source.json"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			original := legacyArchiveChmod
			replaced := false
			swapTestHook(t, &legacyArchiveChmod, func(path string, mode os.FileMode) error {
				if err := original(path, mode); err != nil {
					return err
				}
				if replaced {
					return nil
				}
				if err := os.Remove(path); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
					return err
				}
				replaced = true
				return nil
			})
			callErr := test.call(t, root)
			if callErr == nil {
				t.Fatal("archive directory replacement after chmod was accepted")
			}
			if !replaced {
				t.Fatalf("test did not replace an archive directory after chmod: %v", callErr)
			}
		})
	}
}
