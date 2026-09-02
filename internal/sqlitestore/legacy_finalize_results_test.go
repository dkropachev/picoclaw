package sqlitestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyFinalizeResultsAtomicallyReplacesProvisionalAccounting(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(source, []byte(`{"secret":"not-a-diagnostic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	options.Legacy = &LegacyOptions{
		SourceRoot: root, ArchiveRoot: filepath.Join(root, "archive"),
		Sources: func() ([]LegacySource, error) {
			return []LegacySource{{ID: "legacy-source", Relative: "legacy.json"}}, nil
		},
		Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			return ImportResult{Imported: 7}, nil
		},
		FinalizeResults: func(
			_ context.Context,
			_ *sql.Conn,
			input LegacyFinalizeInput,
		) (map[string]ImportResult, error) {
			if len(input.SourceIDs) != 1 || input.Imported != 7 || input.Skipped != 0 {
				t.Fatalf("finalize input = %#v", input)
			}
			return map[string]ImportResult{"legacy-source": {
				Skipped: 1,
				Issues: []ImportIssue{{
					Code: "dependency-conflict", RecordDigest: sha256.Sum256([]byte("record")),
				}},
			}}, nil
		},
	}
	db, err := Open(t.Context(), filepath.Join(root, "store.db"), options)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var imported, skipped int
	if err := db.QueryRow(`SELECT imported_count, skipped_count FROM storage_imports
        WHERE component = 'test-store' AND source_id = 'legacy-source'`).Scan(
		&imported, &skipped,
	); err != nil || imported != 0 || skipped != 1 {
		t.Fatalf("final accounting = %d/%d, %v", imported, skipped, err)
	}
	var code string
	if err := db.QueryRow(`SELECT issue_code FROM storage_import_issues
        WHERE component = 'test-store' AND source_id = 'legacy-source'`).Scan(&code); err != nil ||
		code != "dependency-conflict" {
		t.Fatalf("final issue = %q, %v", code, err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source was not archived: %v", err)
	}
}

func TestLegacyFinalizeResultsRejectsIncompleteAndCompetingFinalizers(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*LegacyOptions)
		want      string
	}{
		{
			name: "missing result",
			configure: func(options *LegacyOptions) {
				options.FinalizeResults = func(
					context.Context, *sql.Conn, LegacyFinalizeInput,
				) (map[string]ImportResult, error) {
					return map[string]ImportResult{}, nil
				}
			},
			want: "does not cover",
		},
		{
			name: "competing finalizers",
			configure: func(options *LegacyOptions) {
				options.Finalize = func(context.Context, *sql.Conn, LegacyFinalizeInput) error { return nil }
				options.FinalizeResults = func(
					context.Context, *sql.Conn, LegacyFinalizeInput,
				) (map[string]ImportResult, error) {
					return nil, nil
				}
			},
			want: "multiple finalizers",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "legacy.json"), []byte("record"), 0o600); err != nil {
				t.Fatal(err)
			}
			options := testOptions()
			legacy := &LegacyOptions{
				SourceRoot: root, ArchiveRoot: filepath.Join(root, "archive"),
				Sources: func() ([]LegacySource, error) {
					return []LegacySource{{ID: "source", Relative: "legacy.json"}}, nil
				},
				Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
					return ImportResult{}, nil
				},
			}
			test.configure(legacy)
			options.Legacy = legacy
			db, err := Open(t.Context(), filepath.Join(root, "store.db"), options)
			if db != nil {
				_ = db.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Open() error = %v, want %q", err, test.want)
			}
			if _, err := os.Stat(filepath.Join(root, "legacy.json")); err != nil {
				t.Fatalf("failed finalization changed source: %v", err)
			}
		})
	}
}

//nolint:govet // Independent storage assertions intentionally use narrow error scopes.
func TestReplaceFinalizedImportResultsRejectsUnknownInvalidMissingAndAggregateOverflow(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := createImportSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	insert := func(id string) {
		t.Helper()
		_, err := conn.ExecContext(ctx, `INSERT INTO storage_imports (
            component,source_id,source_relative,source_digest,source_size,source_limit,
            source_mode,imported_count,skipped_count,archive_status,imported_at
        ) VALUES ('component',?,?,?,1,1,384,0,0,'pending',1)`,
			id, id+".json", make([]byte, sha256.Size))
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("source")

	for name, test := range map[string]struct {
		sourceIDs []string
		results   map[string]ImportResult
		want      string
	}{
		"unknown": {
			[]string{"source"}, map[string]ImportResult{"other": {}}, "unknown source",
		},
		"invalid": {
			[]string{"source"}, map[string]ImportResult{"source": {
				Skipped: 1, Issues: []ImportIssue{{Code: "bad code"}},
			}}, "issue code is invalid",
		},
		"missing row": {
			[]string{"absent"}, map[string]ImportResult{"absent": {}}, "record is missing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			summary := legacyImportSummary{}
			err := replaceFinalizedImportResults(
				ctx, conn, "component", test.sourceIDs, test.results, &summary,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("replaceFinalizedImportResults() error = %v, want %q", err, test.want)
			}
		})
	}

	insert("second")
	summary := legacyImportSummary{}
	err = replaceFinalizedImportResults(ctx, conn, "component", []string{"source", "second"},
		map[string]ImportResult{
			"source": {Imported: maximumLegacyRecordCount},
			"second": {Imported: maximumLegacyRecordCount},
		}, &summary)
	if err == nil || !strings.Contains(err.Error(), "aggregate record count") {
		t.Fatalf("aggregate overflow error = %v", err)
	}
}
