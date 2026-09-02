package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func swapTestHook[T any](t *testing.T, target *T, replacement T) {
	t.Helper()
	original := *target
	*target = replacement
	t.Cleanup(func() { *target = original })
}

type faultSQLiteFile struct {
	sqliteFile
	statErr  error
	chmodErr error
	syncErr  error
	closeErr error
}

func (file faultSQLiteFile) Stat() (os.FileInfo, error) {
	if file.statErr != nil {
		return nil, file.statErr
	}
	return file.sqliteFile.Stat()
}

func (file faultSQLiteFile) Chmod(mode os.FileMode) error {
	if file.chmodErr != nil {
		return file.chmodErr
	}
	return file.sqliteFile.Chmod(mode)
}

func (file faultSQLiteFile) Sync() error {
	if file.syncErr != nil {
		return file.syncErr
	}
	return file.sqliteFile.Sync()
}

func (file faultSQLiteFile) Close() error {
	closeErr := file.sqliteFile.Close()
	if file.closeErr != nil {
		return file.closeErr
	}
	return closeErr
}

func TestOpenReportsEveryPipelineStageFailure(t *testing.T) {
	canary := errors.New("pipeline canary")
	tests := []struct {
		name   string
		mutate func(*testing.T, string, *Options)
	}{
		{
			name: "resolve DSN",
			mutate: func(t *testing.T, _ string, _ *Options) {
				swapTestHook(t, &absoluteSQLitePath, func(string) (string, error) {
					return "", canary
				})
			},
		},
		{
			name: "open database",
			mutate: func(t *testing.T, _ string, _ *Options) {
				swapTestHook(t, &openSQLiteDatabase, func(string, time.Duration) (*sql.DB, error) {
					return nil, canary
				})
			},
		},
		{
			name: "configure database",
			mutate: func(t *testing.T, _ string, _ *Options) {
				swapTestHook(
					t,
					&configureOpenedSQLiteDatabase,
					func(context.Context, *sql.DB, time.Duration, bool, string) error {
						return canary
					},
				)
			},
		},
		{
			name: "second sidecar fence",
			mutate: func(t *testing.T, _ string, _ *Options) {
				original := secureOpenedSQLiteFiles
				calls := 0
				swapTestHook(t, &secureOpenedSQLiteFiles, func(path string) error {
					calls++
					if calls == 1 {
						return canary
					}
					return original(path)
				})
			},
		},
		{
			name: "migrate database",
			mutate: func(t *testing.T, _ string, _ *Options) {
				swapTestHook(
					t,
					&migrateOpenedSQLiteDatabase,
					func(context.Context, *sql.DB, Options) error { return canary },
				)
			},
		},
		{
			name: "post-migration integrity",
			mutate: func(t *testing.T, _ string, _ *Options) {
				swapTestHook(
					t,
					&checkOpenedSQLiteIntegrity,
					func(context.Context, *sql.DB, string) error { return canary },
				)
			},
		},
		{
			name: "final sidecar fence",
			mutate: func(t *testing.T, _ string, _ *Options) {
				original := secureOpenedSQLiteFiles
				calls := 0
				swapTestHook(t, &secureOpenedSQLiteFiles, func(path string) error {
					calls++
					if calls == 2 {
						return canary
					}
					return original(path)
				})
			},
		},
		{
			name: "archive legacy sources",
			mutate: func(t *testing.T, root string, options *Options) {
				options.Legacy = legacyTestOptions(root, nil, nil)
				swapTestHook(
					t,
					&archiveOpenedSQLiteLegacyFiles,
					func(context.Context, *sql.DB, string, LegacyOptions) error { return canary },
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			options := testOptions()
			test.mutate(t, root, &options)
			db, openErr := Open(t.Context(), filepath.Join(root, "store.db"), options)
			if db != nil {
				db.Close()
			}
			if !errors.Is(openErr, canary) {
				t.Fatalf("Open() error = %v, want canary", openErr)
			}
		})
	}
}

func TestDatabaseFilePreparationFailureInjection(t *testing.T) {
	canary := errors.New("file canary")
	type failureTest struct {
		name   string
		mutate func(*testing.T, string)
	}
	tests := make([]failureTest, 0, 6)
	tests = append(tests,
		failureTest{
			name: "lstat",
			mutate: func(t *testing.T, path string) {
				original := lstatSQLitePath
				swapTestHook(t, &lstatSQLitePath, func(candidate string) (os.FileInfo, error) {
					if candidate == path {
						return nil, canary
					}
					return original(candidate)
				})
			},
		},
		failureTest{
			name: "open",
			mutate: func(t *testing.T, path string) {
				original := openSQLiteFile
				swapTestHook(
					t,
					&openSQLiteFile,
					func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
						if candidate == path {
							return nil, canary
						}
						return original(candidate, flag, mode)
					},
				)
			},
		},
	)
	for _, method := range []string{"stat", "chmod", "sync", "close"} {
		tests = append(tests, failureTest{
			name: method,
			mutate: func(t *testing.T, path string) {
				original := openSQLiteFile
				swapTestHook(
					t,
					&openSQLiteFile,
					func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
						file, openErr := original(candidate, flag, mode)
						if openErr != nil || candidate != path {
							return file, openErr
						}
						fault := faultSQLiteFile{sqliteFile: file}
						switch method {
						case "stat":
							fault.statErr = canary
						case "chmod":
							fault.chmodErr = canary
						case "sync":
							fault.syncErr = canary
						case "close":
							fault.closeErr = canary
						}
						return fault, nil
					},
				)
			},
		})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			test.mutate(t, path)
			if err := prepareDatabaseFile(path); !errors.Is(err, canary) {
				t.Fatalf("prepareDatabaseFile() error = %v, want canary", err)
			}
		})
	}
}

func TestPrivateDirectoryAndSidecarFailureInjection(t *testing.T) {
	canary := errors.New("filesystem canary")
	t.Run("mkdir", func(t *testing.T) {
		swapTestHook(t, &mkdirAllSQLiteDirectories, func(string, os.FileMode) error { return canary })
		if err := ensurePrivateDir(filepath.Join(t.TempDir(), "private")); !errors.Is(err, canary) {
			t.Fatalf("ensurePrivateDir() error = %v", err)
		}
	})
	t.Run("directory lstat", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "private")
		original := lstatSQLitePath
		swapTestHook(t, &lstatSQLitePath, func(candidate string) (os.FileInfo, error) {
			if candidate == path {
				return nil, canary
			}
			return original(candidate)
		})
		if err := ensurePrivateDir(path); !errors.Is(err, canary) {
			t.Fatalf("ensurePrivateDir() error = %v", err)
		}
	})
	t.Run("directory chmod", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "private")
		original := chmodSQLitePath
		swapTestHook(t, &chmodSQLitePath, func(candidate string, mode os.FileMode) error {
			if candidate == path {
				return canary
			}
			return original(candidate, mode)
		})
		if err := ensurePrivateDir(path); !errors.Is(err, canary) {
			t.Fatalf("ensurePrivateDir() error = %v", err)
		}
	})

	for _, method := range []string{"lstat", "open", "stat", "chmod", "close"} {
		t.Run("sidecar "+method, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			switch method {
			case "lstat":
				original := lstatSQLitePath
				swapTestHook(t, &lstatSQLitePath, func(candidate string) (os.FileInfo, error) {
					if candidate == path {
						return nil, canary
					}
					return original(candidate)
				})
			case "open":
				original := openSQLiteFile
				swapTestHook(
					t,
					&openSQLiteFile,
					func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
						if candidate == path {
							return nil, canary
						}
						return original(candidate, flag, mode)
					},
				)
			default:
				original := openSQLiteFile
				swapTestHook(
					t,
					&openSQLiteFile,
					func(candidate string, flag int, mode os.FileMode) (sqliteFile, error) {
						file, openErr := original(candidate, flag, mode)
						if openErr != nil || candidate != path {
							return file, openErr
						}
						fault := faultSQLiteFile{sqliteFile: file}
						switch method {
						case "stat":
							fault.statErr = canary
						case "chmod":
							fault.chmodErr = canary
						case "close":
							fault.closeErr = canary
						}
						return fault, nil
					},
				)
			}
			if err := secureSQLiteFiles(path); !errors.Is(err, canary) {
				t.Fatalf("secureSQLiteFiles() error = %v, want canary", err)
			}
		})
	}
}
