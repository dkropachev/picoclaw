package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

const validCatalogSnapshotRevision = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func catalogSnapshotTestOptions(t *testing.T) Options {
	t.Helper()

	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(home, "workspace")
	return Options{
		Home:       home,
		Config:     cfg,
		ConfigPath: filepath.Join(home, "config.json"),
	}
}

func TestNewSnapshotReturnsMatchingLogicalCatalogAndFingerprint(t *testing.T) {
	for _, revision := range []string{"missing", validCatalogSnapshotRevision} {
		t.Run(revision, func(t *testing.T) {
			options := catalogSnapshotTestOptions(t)
			logical, fingerprint, err := NewSnapshot(options, revision)
			if err != nil {
				t.Fatal(err)
			}
			if logical == nil {
				t.Fatal("NewSnapshot returned nil logical catalog")
			}
			if len(fingerprint) != len("sha256:")+64 || !strings.HasPrefix(fingerprint, "sha256:") ||
				fingerprint != strings.ToLower(fingerprint) {
				t.Fatalf("snapshot fingerprint = %q", fingerprint)
			}
			for _, character := range strings.TrimPrefix(fingerprint, "sha256:") {
				if character < '0' || character > '9' && character < 'a' || character > 'f' {
					t.Fatalf("snapshot fingerprint contains non-lowercase-hex character %q", character)
				}
			}

			separate, newErr := New(options)
			if newErr != nil {
				t.Fatal(newErr)
			}
			if !reflect.DeepEqual(logical.Entries(), separate.Entries()) {
				t.Fatalf("snapshot entries differ from New: %#v != %#v", logical.Entries(), separate.Entries())
			}
		})
	}
}

func TestNewSnapshotIsDeterministicAndRevisionBound(t *testing.T) {
	options := catalogSnapshotTestOptions(t)
	firstCatalog, firstFingerprint, err := NewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	secondCatalog, secondFingerprint, err := NewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint ||
		!reflect.DeepEqual(firstCatalog.Entries(), secondCatalog.Entries()) {
		t.Fatalf("repeat snapshot differs: %q/%#v != %q/%#v",
			firstFingerprint, firstCatalog.Entries(), secondFingerprint, secondCatalog.Entries())
	}

	revisionCatalog, revisionFingerprint, err := NewSnapshot(options, validCatalogSnapshotRevision)
	if err != nil {
		t.Fatal(err)
	}
	if revisionFingerprint == firstFingerprint {
		t.Fatalf("configuration revision did not change fingerprint %q", revisionFingerprint)
	}
	if !reflect.DeepEqual(revisionCatalog.Entries(), firstCatalog.Entries()) {
		t.Fatalf("revision alone changed logical entries: %#v", revisionCatalog.Entries())
	}
}

func TestNewSnapshotBindsResolvedCatalogContext(t *testing.T) {
	base := catalogSnapshotTestOptions(t)
	baseCatalog, baseFingerprint, err := NewSnapshot(base, "missing")
	if err != nil {
		t.Fatal(err)
	}

	otherHome := t.TempDir()
	if chmodErr := os.Chmod(otherHome, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	contexts := []struct {
		name    string
		options Options
	}{
		{
			name: "home",
			options: Options{
				Home: otherHome, Config: base.Config,
				ConfigPath: filepath.Join(otherHome, "config.json"),
			},
		},
		{
			name: "config path",
			options: Options{
				Home: base.Home, Config: base.Config,
				ConfigPath: filepath.Join(base.Home, "alternate", "config.json"),
			},
		},
	}
	for _, test := range contexts {
		t.Run(test.name, func(t *testing.T) {
			logical, fingerprint, snapshotErr := NewSnapshot(test.options, "missing")
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			if fingerprint == baseFingerprint {
				t.Fatalf("%s context retained fingerprint %q", test.name, fingerprint)
			}
			if !reflect.DeepEqual(logical.Entries(), baseCatalog.Entries()) {
				t.Fatalf("%s physical context changed logical entries", test.name)
			}
		})
	}

	firstUserHome := t.TempDir()
	secondUserHome := t.TempDir()
	tildeConfig := config.DefaultConfig()
	tildeConfig.Agents.Defaults.Workspace = "~/workspace"
	firstOptions := Options{
		Home: base.Home, Config: tildeConfig, ConfigPath: base.ConfigPath, UserHome: firstUserHome,
	}
	secondOptions := firstOptions
	secondOptions.UserHome = secondUserHome
	firstCatalog, firstFingerprint, err := NewSnapshot(firstOptions, "missing")
	if err != nil {
		t.Fatal(err)
	}
	secondCatalog, secondFingerprint, err := NewSnapshot(secondOptions, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint == secondFingerprint {
		t.Fatalf("resolved user-home context retained fingerprint %q", firstFingerprint)
	}
	if !reflect.DeepEqual(firstCatalog.Entries(), secondCatalog.Entries()) {
		t.Fatalf("physical user-home context changed primary logical entries")
	}
}

func TestNewSnapshotDoesNotInspectGenerationLeaves(t *testing.T) {
	options := catalogSnapshotTestOptions(t)
	unsafeMain := filepath.Join(options.Home, "auth.db")
	if err := os.Mkdir(unsafeMain, 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeSidecar := filepath.Join(options.Home, "launcher-auth.db-wal")
	if err := os.Mkdir(unsafeSidecar, 0o700); err != nil {
		t.Fatal(err)
	}

	logical, fingerprint, err := NewSnapshot(options, "missing")
	if err != nil {
		t.Fatalf("snapshot inspected generation leaves: %v", err)
	}
	if logical == nil || fingerprint == "" || !logical.Contains("global/auth") ||
		!logical.Contains("launcher/auth") {
		t.Fatalf("snapshot result = %#v, %q", logical, fingerprint)
	}
}

func TestNewSnapshotDetachesReturnedStateAndCallerMutation(t *testing.T) {
	options := catalogSnapshotTestOptions(t)
	logical, fingerprint, err := NewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	original := logical.Entries()
	if len(original) == 0 {
		t.Fatal("snapshot catalog is empty")
	}

	mutated := logical.Entries()
	mutated[0] = Entry{ID: "forged/id", Domain: "forged", Required: !mutated[0].Required}
	if slices.Equal(mutated, logical.Entries()) {
		t.Fatal("snapshot returned retained entry storage")
	}
	options.Config.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "replacement")
	options.Config.Workflows.Enabled = !options.Config.Workflows.Enabled
	if !reflect.DeepEqual(logical.Entries(), original) {
		t.Fatalf("caller config mutation changed snapshot: %#v", logical.Entries())
	}
	if fingerprint == "" {
		t.Fatal("fingerprint was not retained by caller value")
	}

	changed, changedFingerprint, err := NewSnapshot(options, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if changedFingerprint == fingerprint {
		t.Fatalf("mutated catalog context retained fingerprint %q", fingerprint)
	}
	if changed == logical {
		t.Fatal("repeat snapshot reused catalog pointer")
	}
}

func TestNewSnapshotFailuresAreAtomicStructuredAndSanitized(t *testing.T) {
	valid := catalogSnapshotTestOptions(t)
	secret := filepath.Join(t.TempDir(), "secret-provider-store.db")
	tests := []struct {
		name     string
		options  Options
		revision string
		code     database.ErrorCode
	}{
		{name: "nil config", options: Options{Home: valid.Home}, revision: "missing", code: database.CodeInvalid},
		{
			name: "invalid revision", options: valid,
			revision: "sha256:" + strings.Repeat("a", 63) + "g", code: database.CodeInvalid,
		},
		{name: "empty revision", options: valid, code: database.CodeInvalid},
		{name: "padded revision", options: valid, revision: " missing", code: database.CodeInvalid},
		{
			name: "uppercase revision", options: valid,
			revision: "sha256:" + strings.Repeat("A", 64), code: database.CodeInvalid,
		},
		{
			name: "short revision", options: valid,
			revision: "sha256:" + strings.Repeat("a", 63), code: database.CodeInvalid,
		},
		{name: "invalid home", options: Options{
			Home: secret + "\x00", Config: config.DefaultConfig(),
		}, revision: "missing", code: database.CodeInvalid},
		{name: "invalid config path", options: Options{
			Home: valid.Home, Config: config.DefaultConfig(), ConfigPath: secret + "\x00",
		}, revision: "missing", code: database.CodeInvalid},
		{name: "invalid user home", options: Options{
			Home: valid.Home, Config: config.DefaultConfig(), UserHome: secret + "\x00",
		}, revision: "missing", code: database.CodeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logical, fingerprint, err := NewSnapshot(test.options, test.revision)
			if logical != nil || fingerprint != "" || database.CodeOf(err) != test.code {
				t.Fatalf("NewSnapshot failure = %#v, %q, %v", logical, fingerprint, err)
			}
			if err == nil || err.Error() != string(test.code)+": "+catalogSnapshotFailureMessage {
				t.Fatalf("snapshot error = %v", err)
			}
			lowerError := strings.ToLower(err.Error())
			if strings.Contains(err.Error(), secret) || strings.Contains(lowerError, ".db") ||
				strings.Contains(lowerError, "sqlite") || strings.Contains(lowerError, "provider") {
				t.Fatalf("snapshot error leaked path/provider detail: %v", err)
			}
		})
	}
}

func TestSanitizeSnapshotErrorPreservesOnlyStructuredCode(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code database.ErrorCode
	}{
		{name: "ordinary", err: os.ErrPermission, code: database.CodeInternal},
		{name: "unavailable", err: database.NewError(database.CodeUnavailable, "secret path"), code: database.CodeUnavailable},
		{name: "unauthorized", err: database.NewError(database.CodeUnauthorized, "secret path"), code: database.CodeUnauthorized},
		{name: "integrity", err: database.NewError(database.CodeIntegrity, "secret path"), code: database.CodeIntegrity},
		{name: "deadline", err: database.NewError(database.CodeDeadline, "secret path"), code: database.CodeDeadline},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := sanitizeSnapshotError(test.err)
			if database.CodeOf(err) != test.code || err.Message != catalogSnapshotFailureMessage ||
				strings.Contains(err.Error(), "secret") {
				t.Fatalf("sanitizeSnapshotError() = %v", err)
			}
		})
	}
}

func TestNewCatalogSnapshotDropsPartialInvalidProjection(t *testing.T) {
	logical, fingerprint, err := newCatalogSnapshot([]storecatalog.Spec{{
		ID: "global/auth", Domain: "invalid_domain",
	}}, "partial-fingerprint-must-not-escape")
	if logical != nil || fingerprint != "" || database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("newCatalogSnapshot invalid projection = %#v, %q, %v", logical, fingerprint, err)
	}
	if err.Error() != string(database.CodeIntegrity)+": "+catalogSnapshotFailureMessage {
		t.Fatalf("newCatalogSnapshot error = %v", err)
	}
}
