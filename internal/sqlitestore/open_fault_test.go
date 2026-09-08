package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
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
					func(context.Context, *sql.DB, time.Duration, bool, bool, string) error {
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
