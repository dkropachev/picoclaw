package sqlitestore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

type replacePathWithRegularOnCloseSQLiteFile struct {
	sqliteFile
	path            string
	replacementPath string
	replaced        *bool
}

func (file replacePathWithRegularOnCloseSQLiteFile) Close() error {
	closeErr := file.sqliteFile.Close()
	removeErr := os.Remove(file.path)
	renameErr := os.Rename(file.replacementPath, file.path)
	if removeErr == nil && renameErr == nil {
		*file.replaced = true
	}
	return errors.Join(closeErr, removeErr, renameErr)
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

func TestSecureSQLiteFilesRejectsRegularPathReplacementAfterClose(t *testing.T) {
	for _, companion := range []bool{false, true} {
		t.Run(fmt.Sprintf("companion=%t", companion), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			target := path
			if companion {
				target = path + "-shm"
			}
			candidates := []string{path}
			if target != path {
				candidates = append(candidates, target)
			}
			for _, candidate := range candidates {
				if err := os.WriteFile(candidate, []byte("initial"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			replacementPath := target + "-replacement"
			if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			replaced := false
			original := openSQLiteFile
			swapTestHook(
				t,
				&openSQLiteFile,
				func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
					opened, err := original(candidate, flag, mode)
					if err != nil || candidate != target {
						return opened, err
					}
					return replacePathWithRegularOnCloseSQLiteFile{
						sqliteFile:      opened,
						path:            target,
						replacementPath: replacementPath,
						replaced:        &replaced,
					}, nil
				},
			)
			if err := secureSQLiteFiles(path); err == nil {
				t.Fatal("regular SQLite path replacement after close was accepted")
			}
			if !replaced {
				t.Fatal("test did not replace the SQLite path with another regular file")
			}
		})
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
			return nil, errors.Join(errors.New("private file changed while securing"), fs.ErrNotExist)
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

func TestSecureSQLiteFilesAllowsCompanionDisappearanceWhileOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	companion := path + "-shm"
	for _, candidate := range []string{path, companion} {
		if err := os.WriteFile(candidate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original := openSQLiteFile
	swapTestHook(t, &openSQLiteFile, func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
		if candidate == companion {
			if err := os.Remove(candidate); err != nil {
				return nil, err
			}
		}
		return original(candidate, flag, mode)
	})
	if err := secureSQLiteFiles(path); err != nil {
		t.Fatalf("vanished SQLite companion: %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("primary database disappeared: %v", err)
	}
	if _, err := os.Lstat(companion); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SQLite companion still exists: %v", err)
	}
}

func TestSecureSQLiteFilesRejectsDanglingCompanionReplacementWhileOpening(t *testing.T) {
	root := t.TempDir()
	probe := filepath.Join(root, "symlink-probe")
	if err := os.Symlink(filepath.Join(root, "missing-probe-target"), probe); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "store.db")
	companion := path + "-shm"
	for _, candidate := range []string{path, companion} {
		if err := os.WriteFile(candidate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	original := openSQLiteFile
	swapTestHook(t, &openSQLiteFile, func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
		if candidate == companion {
			if err := os.Remove(candidate); err != nil {
				return nil, err
			}
			if err := os.Symlink(filepath.Join(root, "missing-target"), candidate); err != nil {
				return nil, err
			}
		}
		return original(candidate, flag, mode)
	})
	if err := secureSQLiteFiles(path); err == nil {
		t.Fatal("dangling SQLite companion replacement was accepted")
	}
	info, err := os.Lstat(companion)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("replacement companion = %v, %v", info, err)
	}
}

func TestSecureSQLiteFilesAllowsCompanionDisappearanceAfterOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go SQLite file handles on Windows do not share delete access")
	}
	path := filepath.Join(t.TempDir(), "store.db")
	companion := path + "-shm"
	for _, candidate := range []string{path, companion} {
		if err := os.WriteFile(candidate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(companion, 0o644); err != nil {
		t.Fatal(err)
	}
	alias := companion + "-alias"
	if err := os.Link(companion, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	original := lstatSQLitePath
	companionInspections := 0
	swapTestHook(t, &lstatSQLitePath, func(candidate string) (os.FileInfo, error) {
		if candidate == companion {
			companionInspections++
			if companionInspections == 2 {
				if err := os.Remove(candidate); err != nil {
					return nil, err
				}
			}
		}
		return original(candidate)
	})
	if err := secureSQLiteFiles(path); err != nil {
		t.Fatalf("companion vanished after open: %v", err)
	}
	if companionInspections != 2 {
		t.Fatalf("companion inspections = %d, want 2", companionInspections)
	}
	info, err := os.Stat(alias)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("vanished companion alias = %v, %v; want mode 0600", info, err)
	}
}

func TestSecureSQLiteFilesFailsClosedWhenVanishedCompanionHandleCannotHarden(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go SQLite file handles on Windows do not share delete access")
	}
	for _, stage := range []string{"chmod", "close"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			companion := path + "-shm"
			for _, candidate := range []string{path, companion} {
				if err := os.WriteFile(candidate, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			canary := errors.New("opened companion hardening failure")
			originalOpen := openSQLiteFile
			swapTestHook(
				t,
				&openSQLiteFile,
				func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
					opened, err := originalOpen(candidate, flag, mode)
					if err != nil || candidate != companion {
						return opened, err
					}
					fault := faultSQLiteFile{sqliteFile: opened}
					if stage == "chmod" {
						fault.chmodErr = canary
					} else {
						fault.closeErr = canary
					}
					return fault, nil
				},
			)
			originalLstat := lstatSQLitePath
			companionInspections := 0
			swapTestHook(t, &lstatSQLitePath, func(candidate string) (os.FileInfo, error) {
				if candidate == companion {
					companionInspections++
					if companionInspections == 2 {
						if err := os.Remove(candidate); err != nil {
							return nil, err
						}
					}
				}
				return originalLstat(candidate)
			})
			if err := secureSQLiteFiles(path); !errors.Is(err, canary) {
				t.Fatalf("vanished companion %s error = %v", stage, err)
			}
		})
	}
}

func TestSecureSQLiteFilesFailsClosedOnCompanionReinspectionErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	companion := path + "-shm"
	for _, candidate := range []string{path, companion} {
		if err := os.WriteFile(candidate, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	canary := errors.New("companion reinspection failure")
	originalOpen := openSQLiteFile
	swapTestHook(t, &openSQLiteFile, func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
		if candidate == companion {
			return nil, fs.ErrNotExist
		}
		return originalOpen(candidate, flag, mode)
	})
	originalLstat := lstatSQLitePath
	companionInspections := 0
	swapTestHook(t, &lstatSQLitePath, func(candidate string) (os.FileInfo, error) {
		if candidate == companion {
			companionInspections++
			if companionInspections == 2 {
				return nil, canary
			}
		}
		return originalLstat(candidate)
	})
	if err := secureSQLiteFiles(path); !errors.Is(err, canary) {
		t.Fatalf("companion open reinspection error = %v", err)
	}
}

func TestSecureSQLiteFilesRejectsCompanionPresentAfterFinalNotExist(t *testing.T) {
	for _, reinspectionError := range []bool{false, true} {
		t.Run(fmt.Sprintf("reinspection-error=%t", reinspectionError), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			companion := path + "-shm"
			for _, candidate := range []string{path, companion} {
				if err := os.WriteFile(candidate, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			originalSecure := securePrivateSQLiteFile
			swapTestHook(t, &securePrivateSQLiteFile, func(candidate string) (os.FileInfo, error) {
				if candidate == companion {
					return nil, errors.Join(errors.New("private file changed while securing"), fs.ErrNotExist)
				}
				return originalSecure(candidate)
			})
			canary := errors.New("final companion reinspection failure")
			if reinspectionError {
				originalLstat := lstatSQLitePath
				companionInspections := 0
				swapTestHook(t, &lstatSQLitePath, func(candidate string) (os.FileInfo, error) {
					if candidate == companion {
						companionInspections++
						if companionInspections == 3 {
							return nil, canary
						}
					}
					return originalLstat(candidate)
				})
			}
			err := secureSQLiteFiles(path)
			if reinspectionError {
				if !errors.Is(err, canary) {
					t.Fatalf("final companion reinspection error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "changed while securing") {
				t.Fatalf("present companion after not-exist error = %v", err)
			}
		})
	}
}

func TestSecureSQLiteFilesRejectsCompanionReplacementAfterInitialInspection(t *testing.T) {
	for _, removeReplacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("vanished=%t", removeReplacement), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			companion := path + "-shm"
			for _, candidate := range []string{path, companion} {
				if err := os.WriteFile(candidate, []byte("initial"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			replacementPath := companion + "-replacement"
			if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			originalOpen := openSQLiteFile
			swapTestHook(
				t,
				&openSQLiteFile,
				func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
					if candidate == companion {
						if err := os.Remove(candidate); err != nil {
							return nil, err
						}
						if err := os.Rename(replacementPath, candidate); err != nil {
							return nil, err
						}
					}
					return originalOpen(candidate, flag, mode)
				},
			)
			if removeReplacement {
				originalLstat := lstatSQLitePath
				companionInspections := 0
				swapTestHook(t, &lstatSQLitePath, func(candidate string) (os.FileInfo, error) {
					if candidate == companion {
						companionInspections++
						if companionInspections == 2 {
							if err := os.Remove(candidate); err != nil {
								return nil, err
							}
						}
					}
					return originalLstat(candidate)
				})
			}
			if err := secureSQLiteFiles(path); err == nil {
				t.Fatal("replacement SQLite companion identity was accepted")
			}
		})
	}
}

func TestSecureSQLiteFilesRequiresPrimaryDatabaseDuringHardening(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing.db")
	if err := secureSQLiteFiles(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing primary database error = %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	original := openSQLiteFile
	swapTestHook(t, &openSQLiteFile, func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
		if candidate == path {
			if err := os.Remove(candidate); err != nil {
				return nil, err
			}
		}
		return original(candidate, flag, mode)
	})
	if err := secureSQLiteFiles(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("primary disappearance while opening error = %v", err)
	}
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
