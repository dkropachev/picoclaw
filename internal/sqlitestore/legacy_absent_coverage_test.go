//go:build (unix && !aix) || windows

package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealedAbsentLegacyRootCommonFaultCoverage(t *testing.T) {
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	path := filepath.Join(ancestor, "missing")
	canary := errors.New("sealed absent common canary")

	t.Run("captured layout", func(t *testing.T) {
		swapTestHook(t, &sealedAbsentLegacyCapturePlatform, func(
			context.Context,
			string,
		) (*sealedAbsentLegacyRootPlatform, string, []string, error) {
			return &sealedAbsentLegacyRootPlatform{}, ancestor, []string{"different"}, nil
		})
		if proof, err := captureSealedAbsentLegacyRoot(t.Context(), path); proof != nil || err == nil {
			t.Fatalf("inconsistent captured layout = %#v, %v", proof, err)
		}
	})

	t.Run("canceled after platform capture", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		swapTestHook(t, &sealedAbsentLegacyCapturePlatform, func(
			context.Context,
			string,
		) (*sealedAbsentLegacyRootPlatform, string, []string, error) {
			cancel()
			return &sealedAbsentLegacyRootPlatform{}, ancestor, []string{"missing"}, nil
		})
		if proof, err := captureSealedAbsentLegacyRoot(ctx, path); proof != nil ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("post-capture cancellation = %#v, %v", proof, err)
		}
	})

	t.Run("captured revalidation", func(t *testing.T) {
		swapTestHook(t, &sealedAbsentLegacyRevalidatePlatform, func(
			context.Context,
			string,
			string,
			[]string,
			*sealedAbsentLegacyRootPlatform,
		) error {
			return canary
		})
		if proof, err := captureSealedAbsentLegacyRoot(t.Context(), path); proof != nil ||
			!errors.Is(err, canary) {
			t.Fatalf("captured revalidation fault = %#v, %v", proof, err)
		}
	})

	t.Run("canceled after captured revalidation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		swapTestHook(t, &sealedAbsentLegacyRevalidatePlatform, func(
			context.Context,
			string,
			string,
			[]string,
			*sealedAbsentLegacyRootPlatform,
		) error {
			cancel()
			return nil
		})
		if proof, err := captureSealedAbsentLegacyRoot(ctx, path); proof != nil ||
			!errors.Is(err, context.Canceled) {
			t.Fatalf("post-revalidation cancellation = %#v, %v", proof, err)
		}
	})

	for name, invoke := range map[string]func() error{
		"nil enumeration": func() error {
			return markSealedAbsentLegacyRootEnumerated(t.Context(), nil)
		},
		"nil precommit": func() error {
			return checkSealedAbsentLegacyRootBeforeCommit(t.Context(), nil)
		},
		"nil consumption": func() error { return requireSealedAbsentLegacyRootConsumed(nil) },
		"invalid enumeration": func() error {
			return markSealedAbsentLegacyRootEnumerated(t.Context(), &sealedAbsentLegacyRoot{})
		},
		"invalid precommit": func() error {
			return checkSealedAbsentLegacyRootBeforeCommit(t.Context(), &sealedAbsentLegacyRoot{})
		},
		"invalid consumption": func() error {
			return requireSealedAbsentLegacyRootConsumed(&sealedAbsentLegacyRoot{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := invoke(); err == nil {
				t.Fatal("invalid proof operation succeeded")
			}
		})
	}

	proofWithoutPlatform := &sealedAbsentLegacyRoot{}
	if err := proofWithoutPlatform.closeLocked(); err != nil || !proofWithoutPlatform.closed {
		t.Fatalf("close without platform = closed:%t error:%v", proofWithoutPlatform.closed, err)
	}
}

func TestSealedAbsentLegacyLayoutCoverage(t *testing.T) {
	ancestor := filepath.Clean(t.TempDir())
	path := filepath.Join(ancestor, "one", "two")
	for name, test := range map[string]struct {
		path     string
		ancestor string
		suffix   []string
	}{
		"invalid path":      {path: "relative", ancestor: ancestor, suffix: []string{"one"}},
		"invalid suffix":    {path: path, ancestor: ancestor, suffix: []string{"one", ".."}},
		"escaping ancestor": {path: path, ancestor: filepath.Join(ancestor, "other"), suffix: []string{"one"}},
		"incomplete suffix": {path: path, ancestor: ancestor, suffix: []string{"one"}},
		"changed suffix":    {path: path, ancestor: ancestor, suffix: []string{"one", "different"}},
		"empty ancestor":    {path: path, suffix: []string{"one", "two"}},
		"relative ancestor": {path: path, ancestor: "relative", suffix: []string{"one", "two"}},
		"unclean ancestor": {
			path:     path,
			ancestor: ancestor + string(os.PathSeparator) + ".",
			suffix:   []string{"one", "two"},
		},
		"empty suffix":     {path: path, ancestor: ancestor},
		"excessive suffix": {path: path, ancestor: ancestor, suffix: make([]string, maximumSealedAbsentLegacyComponents+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSealedAbsentLegacyLayout(test.path, test.ancestor, test.suffix); err == nil {
				t.Fatal("invalid layout succeeded")
			}
		})
	}

	separator := string(os.PathSeparator)
	if _, err := sealedAbsentLegacyRelativeComponents(separator, separator); err == nil {
		t.Fatal("filesystem root produced relative components")
	}
	tooMany := strings.Repeat("component"+separator, maximumSealedAbsentLegacyComponents+1)
	if _, err := sealedAbsentLegacyRelativeComponents(tooMany, ""); err == nil {
		t.Fatal("excessive component path succeeded")
	}
	if _, err := sealedAbsentLegacyRelativeComponents("ordinary"+separator+"..", ""); err == nil {
		t.Fatal("invalid relative component succeeded")
	}
}

func TestSealedAbsentLegacyPolicyFaultCoverage(t *testing.T) {
	ancestor := privateSealedAbsentLegacyTestDirectory(t)
	path := filepath.Join(ancestor, "missing")
	canary := errors.New("sealed absent policy canary")
	if _, err := canonicalLegacySourceRoot("relative"); err == nil {
		t.Fatal("relative canonical legacy root succeeded")
	}
	t.Run("absolute path failure", func(t *testing.T) {
		swapTestHook(t, &legacyAbsolutePath, func(string) (string, error) { return "", canary })
		if _, err := canonicalLegacySourceRoot(path); !errors.Is(err, canary) {
			t.Fatalf("canonical root absolute-path error = %v", err)
		}
	})
	t.Run("absolute path mismatch", func(t *testing.T) {
		swapTestHook(t, &legacyAbsolutePath, func(string) (string, error) { return "relative", nil })
		if _, err := canonicalLegacySourceRoot(path); err == nil {
			t.Fatal("canonical root accepted altered absolute path")
		}
	})

	options := LegacyOptions{Closeout: LegacyCloseoutDeferred, SourceRootPolicy: LegacySourceRootSealedAbsent}
	if err := revalidateDeferredLegacyProof(nil, "test", options, nil); err == nil {
		t.Fatal("nil deferred proof succeeded")
	}
	if err := revalidateDeferredLegacyProof(t.Context(), "test", options, &deferredLegacyProof{}); err == nil {
		t.Fatal("malformed sealed-absent deferred proof succeeded")
	}
	importOptions := LegacyOptions{
		SourceRoot:       path,
		SourceRootPolicy: LegacySourceRootSealedAbsent,
		Closeout:         LegacyCloseoutDeferred,
		Sources:          func() ([]LegacySource, error) { return nil, nil },
		Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			return ImportResult{}, nil
		},
	}
	if _, err := importLegacySources(t.Context(), nil, "test", importOptions); err == nil {
		t.Fatal("sealed-absent import without a proof succeeded")
	}
	importOptions.sealedAbsentRoot = &sealedAbsentLegacyRoot{}
	if _, err := importLegacySources(t.Context(), nil, "test", importOptions); err == nil {
		t.Fatal("sealed-absent import with an invalid proof succeeded")
	}
	options.SourceRootPolicy = LegacySourceRootExistingDirectory
	if err := revalidateDeferredLegacyProof(t.Context(), "test", options, &deferredLegacyProof{
		sealedAbsent: &sealedAbsentLegacyRoot{},
	}); err == nil {
		t.Fatal("mixed deferred proof policy succeeded")
	}
}

func TestOpenSealedAbsentLegacyRootProofCloseoutFaults(t *testing.T) {
	newOptions := func(t *testing.T) (context.Context, string, string, Options) {
		t.Helper()
		ancestor := privateSealedAbsentLegacyTestDirectory(t)
		root := filepath.Join(ancestor, "missing", "legacy")
		databaseHome := t.TempDir()
		databasePath := filepath.Join(databaseHome, "store.db")
		options := sealedAbsentOpenOptions(
			root,
			func() ([]LegacySource, error) { return nil, nil },
			func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
				t.Fatal("sealed absent fault importer was invoked")
				return ImportResult{}, nil
			},
		)
		return deferredMigrationContext(t, databaseHome, databasePath), root, databasePath, options
	}

	t.Run("unused proof", func(t *testing.T) {
		ctx, _, databasePath, options := newOptions(t)
		swapTestHook(t, &migrateOpenedSQLiteDatabase, func(context.Context, *sql.DB, Options) error {
			return nil
		})
		database, err := Open(ctx, databasePath, options)
		if database != nil {
			_ = database.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "proof consumption") {
			t.Fatalf("unused sealed absent proof error = %v", err)
		}
	})

	t.Run("proof close", func(t *testing.T) {
		ctx, _, databasePath, options := newOptions(t)
		canary := errors.New("sealed absent close canary")
		original := sealedAbsentLegacyClosePlatform
		swapTestHook(t, &sealedAbsentLegacyClosePlatform, func(platform *sealedAbsentLegacyRootPlatform) error {
			return errors.Join(canary, original(platform))
		})
		database, err := Open(ctx, databasePath, options)
		if database != nil {
			_ = database.Close()
		}
		if !errors.Is(err, canary) {
			t.Fatalf("sealed absent proof close error = %v", err)
		}
	})
}
