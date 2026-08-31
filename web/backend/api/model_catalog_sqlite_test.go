package api

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

func TestModelCatalogSQLiteSchemaPragmasPermissionsAndReopen(t *testing.T) {
	home := useCatalogTestHome(t)
	models := []CatalogModel{
		{ID: "model-b", OwnedBy: "vendor-b", Extra: map[string]any{
			"z": []any{3, 2, 1},
			"a": json.Number("9007199254740993"),
		}},
		{ID: "model-a", OwnedBy: "vendor-a"},
	}
	if err := SaveCatalog(" OpenAI ", "https://example.test/v1///", "sk-1234567890abcd", models); err != nil {
		t.Fatalf("SaveCatalog() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, legacyCatalogFilename)); !os.IsNotExist(err) {
		t.Fatalf("legacy JSON created or retained unexpectedly: %v", err)
	}

	db, openErr := openCatalogDatabase(t.Context())
	if openErr != nil {
		t.Fatalf("openCatalogDatabase() error = %v", openErr)
	}
	defer db.Close()

	var version, foreignKeys, busyTimeout, synchronous int
	var journal string
	for query, destination := range map[string]any{
		"PRAGMA user_version": &version,
		"PRAGMA foreign_keys": &foreignKeys,
		"PRAGMA busy_timeout": &busyTimeout,
		"PRAGMA synchronous":  &synchronous,
		"PRAGMA journal_mode": &journal,
	} {
		if err := db.QueryRowContext(t.Context(), query).Scan(destination); err != nil {
			t.Fatalf("%s error = %v", query, err)
		}
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journal, "wal") {
		t.Fatalf(
			"PRAGMAs = version:%d fk:%d busy:%d sync:%d journal:%q",
			version,
			foreignKeys,
			busyTimeout,
			synchronous,
			journal,
		)
	}

	expectedSchema := map[string]string{
		"model_catalogs":                    modelCatalogsSchema,
		"model_catalog_models":              catalogModelsSchema,
		"model_catalogs_provider_idx":       modelCatalogsProviderIndexSchema,
		"model_catalog_models_identity_idx": catalogModelsIdentityIndexSchema,
	}
	for name, expected := range expectedSchema {
		var actual string
		if err := db.QueryRowContext(
			t.Context(),
			`SELECT sql FROM sqlite_schema WHERE name = ?`,
			name,
		).Scan(&actual); err != nil {
			t.Fatalf("read schema %s: %v", name, err)
		}
		if compactCatalogSQL(actual) != compactCatalogSQL(expected) {
			t.Fatalf("schema %s = %q, want %q", name, actual, expected)
		}
	}

	key := generateCatalogKey("openai", "https://example.test/v1", "sk-1234567890abcd")
	var provider, apiBase, mask string
	var storedVersion, fetchedAtSeconds, fetchedAtNanosecond int64
	var modelsIsNull int
	if err := db.QueryRowContext(t.Context(), `SELECT
		provider, api_base, api_key_mask, fetched_at_unix_seconds,
		fetched_at_nanosecond, models_is_null, version
        FROM model_catalogs WHERE catalog_id = ?`, key).Scan(
		&provider,
		&apiBase,
		&mask,
		&fetchedAtSeconds,
		&fetchedAtNanosecond,
		&modelsIsNull,
		&storedVersion,
	); err != nil {
		t.Fatalf("read typed catalog row: %v", err)
	}
	if provider != "openai" || apiBase != "https://example.test/v1" ||
		mask != "sk-****abcd" || storedVersion != 1 || fetchedAtSeconds <= 0 ||
		fetchedAtNanosecond != 0 || modelsIsNull != 0 {
		t.Fatalf(
			"typed row = provider:%q base:%q mask:%q fetched:%d.%09d null:%d version:%d",
			provider,
			apiBase,
			mask,
			fetchedAtSeconds,
			fetchedAtNanosecond,
			modelsIsNull,
			storedVersion,
		)
	}
	var extra []byte
	if err := db.QueryRowContext(t.Context(), `SELECT extra_json
        FROM model_catalog_models WHERE catalog_id = ? AND position = 0`, key).Scan(&extra); err != nil {
		t.Fatalf("read canonical metadata: %v", err)
	}
	if got, want := string(extra), `{"a":9007199254740993,"z":[3,2,1]}`; got != want {
		t.Fatalf("extra_json = %s, want %s", got, want)
	}
	var extraType string
	if err := db.QueryRowContext(t.Context(), `SELECT typeof(extra_json)
        FROM model_catalog_models WHERE catalog_id = ? AND position = 0`, key).Scan(&extraType); err != nil {
		t.Fatalf("read metadata type: %v", err)
	}
	if extraType != "blob" {
		t.Fatalf("typeof(extra_json) = %q, want blob", extraType)
	}

	if _, err := db.ExecContext(
		t.Context(),
		`UPDATE model_catalogs SET version = version WHERE catalog_id = ?`,
		key,
	); err != nil {
		t.Fatalf("create WAL evidence: %v", err)
	}
	for _, path := range []string{
		catalogDatabasePath(),
		catalogDatabasePath() + "-wal",
		catalogDatabasePath() + "-shm",
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", filepath.Base(path), err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode %s = %o, want 600", filepath.Base(path), got)
		}
	}
	if info, err := os.Stat(home); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("home mode = %v, %v; want 700", info, err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, loadErr := loadCatalogs()
	if loadErr != nil {
		t.Fatalf("loadCatalogs() after reopen error = %v", loadErr)
	}
	entry := store.Entries[key]
	if entry == nil || len(entry.Models) != 2 || entry.Models[0].ID != "model-b" ||
		entry.Models[1].ID != "model-a" {
		t.Fatalf("reopened entry = %#v", entry)
	}
}

func TestModelCatalogSQLiteSaveUpdateOrderMetadataAndDeleteHTTP(t *testing.T) {
	useCatalogTestHome(t)
	keyOne := generateCatalogKey("anthropic", "https://one.test", "secret-one")
	keyTwo := generateCatalogKey("openai", "https://two.test", "secret-two")
	if err := SaveCatalog("openai", "https://two.test/", "secret-two", []CatalogModel{{ID: "z"}}); err != nil {
		t.Fatalf("SaveCatalog(two) error = %v", err)
	}
	if err := SaveCatalog("anthropic", "https://one.test", "secret-one", []CatalogModel{
		{ID: "third", Extra: map[string]any{"object": map[string]any{"b": 2, "a": 1}}},
		{ID: "first"},
		{ID: "second"},
	}); err != nil {
		t.Fatalf("SaveCatalog(one) error = %v", err)
	}
	if err := SaveCatalog("anthropic", "https://one.test", "secret-one", []CatalogModel{
		{ID: "replacement-2"},
		{ID: "replacement-1", Extra: map[string]any{"exact": json.Number("1.2300")}},
	}); err != nil {
		t.Fatalf("SaveCatalog(update) error = %v", err)
	}

	db, openErr := openCatalogDatabase(t.Context())
	if openErr != nil {
		t.Fatalf("openCatalogDatabase() error = %v", openErr)
	}
	var version int
	if err := db.QueryRowContext(
		t.Context(),
		`SELECT version FROM model_catalogs WHERE catalog_id = ?`,
		keyOne,
	).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 2 {
		t.Fatalf("version = %d, want 2", version)
	}
	var canonical []byte
	if err := db.QueryRowContext(t.Context(), `SELECT extra_json FROM model_catalog_models
        WHERE catalog_id = ? AND position = 1`, keyOne).Scan(&canonical); err != nil {
		t.Fatalf("read replacement metadata: %v", err)
	}
	if got, want := string(canonical), `{"exact":1.2300}`; got != want {
		t.Fatalf("canonical metadata = %s, want %s", got, want)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	handler := &Handler{}
	list := httptest.NewRecorder()
	handler.handleListCatalogs(list, httptest.NewRequest(http.MethodGet, "/api/accounts/models/catalog", nil))
	if list.Code != http.StatusOK || list.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("list response = %d %q %s", list.Code, list.Header().Get("Content-Type"), list.Body.String())
	}
	var response struct {
		Entries []*CatalogEntry `json:"entries"`
		Total   int             `json:"total"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	wantKeys := []string{keyOne, keyTwo}
	sort.Strings(wantKeys)
	if response.Total != 2 || len(response.Entries) != 2 ||
		response.Entries[0].ID != wantKeys[0] || response.Entries[1].ID != wantKeys[1] {
		t.Fatalf("ordered list response = %#v", response)
	}
	entry := response.Entries[0]
	if entry.ID != keyOne {
		entry = response.Entries[1]
	}
	if len(entry.Models) != 2 || entry.Models[0].ID != "replacement-2" ||
		entry.Models[1].ID != "replacement-1" {
		t.Fatalf("model order = %#v", entry.Models)
	}

	missingID := httptest.NewRecorder()
	handler.handleDeleteCatalog(missingID, httptest.NewRequest(http.MethodDelete, "/", nil))
	if missingID.Code != http.StatusBadRequest || missingID.Body.String() != "id is required\n" {
		t.Fatalf("missing id response = %d %q", missingID.Code, missingID.Body.String())
	}
	notFoundRequest := httptest.NewRequest(http.MethodDelete, "/", nil)
	notFoundRequest.SetPathValue("id", "missing")
	notFound := httptest.NewRecorder()
	handler.handleDeleteCatalog(notFound, notFoundRequest)
	if notFound.Code != http.StatusNotFound || notFound.Body.String() != "catalog not found\n" {
		t.Fatalf("not found response = %d %q", notFound.Code, notFound.Body.String())
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/", nil)
	deleteRequest.SetPathValue("id", keyOne)
	deleted := httptest.NewRecorder()
	handler.handleDeleteCatalog(deleted, deleteRequest)
	if deleted.Code != http.StatusOK || deleted.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("delete response = %d %q", deleted.Code, deleted.Body.String())
	}
	store, err := loadCatalogs()
	if err != nil || len(store.Entries) != 1 || store.Entries[keyTwo] == nil {
		t.Fatalf("store after delete = %#v, %v", store, err)
	}
}

func TestModelCatalogSQLitePreservesNilAndEmptyModelSlices(t *testing.T) {
	t.Run("runtime saves", func(t *testing.T) {
		useCatalogTestHome(t)
		if err := SaveCatalog("openai", "https://nil.test", "nil-key", nil); err != nil {
			t.Fatalf("SaveCatalog(nil) error = %v", err)
		}
		if err := SaveCatalog(
			"openai",
			"https://empty.test",
			"empty-key",
			[]CatalogModel{},
		); err != nil {
			t.Fatalf("SaveCatalog(empty) error = %v", err)
		}
		store, err := loadCatalogs()
		if err != nil {
			t.Fatalf("loadCatalogs() error = %v", err)
		}
		nilKey := generateCatalogKey("openai", "https://nil.test", "nil-key")
		emptyKey := generateCatalogKey("openai", "https://empty.test", "empty-key")
		if store.Entries[nilKey] == nil || store.Entries[nilKey].Models != nil {
			t.Fatalf("nil models = %#v", store.Entries[nilKey])
		}
		if store.Entries[emptyKey] == nil || store.Entries[emptyKey].Models == nil ||
			len(store.Entries[emptyKey].Models) != 0 {
			t.Fatalf("empty models = %#v", store.Entries[emptyKey])
		}
		nilJSON, err := json.Marshal(store.Entries[nilKey])
		if err != nil || !bytes.Contains(nilJSON, []byte(`"models":null`)) {
			t.Fatalf("nil model JSON = %s, %v", nilJSON, err)
		}
		emptyJSON, err := json.Marshal(store.Entries[emptyKey])
		if err != nil || !bytes.Contains(emptyJSON, []byte(`"models":[]`)) {
			t.Fatalf("empty model JSON = %s, %v", emptyJSON, err)
		}
	})

	t.Run("legacy null", func(t *testing.T) {
		home := useCatalogTestHome(t)
		legacy := `{"entries":{"legacy":{"id":"legacy","provider":"openai","api_base":"","api_key_mask":"****","models":null,"fetched_at":"2025-01-02T03:04:05Z"}}}`
		legacyPath := filepath.Join(home, legacyCatalogFilename)
		if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
			t.Fatalf("write legacy null models: %v", err)
		}
		if err := os.Chmod(legacyPath, 0o600); err != nil {
			t.Fatalf("chmod legacy null models: %v", err)
		}
		store, err := loadCatalogs()
		if err != nil {
			t.Fatalf("loadCatalogs() error = %v", err)
		}
		if store.Entries["legacy"] == nil || store.Entries["legacy"].Models != nil {
			t.Fatalf("legacy null models = %#v", store.Entries["legacy"])
		}
	})
}

func TestModelCatalogSQLiteLegacyImportAuditArchiveAndIdempotence(t *testing.T) {
	home := useCatalogTestHome(t)
	legacy := `{
  "entries": {
    "alpha": {
      "id": "alpha",
      "provider": "openai",
      "api_base": "https://alpha.test/v1",
      "api_key_mask": "sk-****lpha",
      "models": [
        {"id":"model-2","owned_by":"owner","extra":{"z":[3,2,1],"a":9007199254740993}},
        {"id":"model-1"}
      ],
      "fetched_at": "2025-03-04T05:06:07.123456789Z"
    },
    "broken": {
      "id": "broken",
      "provider": "openai",
      "api_base": "",
      "api_key_mask": "****",
      "models": [{"id":""}],
      "fetched_at": "2025-03-04T05:06:07Z"
    },
    "zeta": {
      "id": "zeta",
      "provider": "anthropic",
      "api_base": "https://zeta.test",
      "api_key_mask": "****",
      "models": [],
      "fetched_at": "2024-01-02T03:04:05Z"
    }
  }
}`
	legacyPath := filepath.Join(home, legacyCatalogFilename)
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o640); err != nil {
		t.Fatalf("write legacy catalog: %v", err)
	}
	if err := os.Chmod(legacyPath, 0o640); err != nil {
		t.Fatalf("chmod legacy catalog: %v", err)
	}

	store, loadErr := loadCatalogs()
	if loadErr != nil {
		t.Fatalf("loadCatalogs() legacy import error = %v", loadErr)
	}
	if len(store.Entries) != 2 || store.Entries["alpha"] == nil || store.Entries["zeta"] == nil {
		t.Fatalf("imported entries = %#v", store.Entries)
	}
	alpha := store.Entries["alpha"]
	if alpha.FetchedAt != "2025-03-04T05:06:07.123456789Z" || len(alpha.Models) != 2 ||
		alpha.Models[0].ID != "model-2" || alpha.Models[1].ID != "model-1" {
		t.Fatalf("alpha entry = %#v", alpha)
	}
	metadata, marshalErr := json.Marshal(alpha.Models[0].Extra)
	if marshalErr != nil || string(metadata) != `{"a":9007199254740993,"z":[3,2,1]}` {
		t.Fatalf("metadata = %s, %v", metadata, marshalErr)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy source still exists: %v", err)
	}
	archivePath := filepath.Join(home, "legacy-json", legacyCatalogArchiveLabel, legacyCatalogFilename)
	archive, archiveErr := os.ReadFile(archivePath)
	if archiveErr != nil || string(archive) != legacy {
		t.Fatalf("archive = %q, %v", archive, archiveErr)
	}
	if info, err := os.Stat(archivePath); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("archive mode = %v, %v; want 640", info, err)
	}

	db, openErr := openCatalogDatabase(t.Context())
	if openErr != nil {
		t.Fatalf("openCatalogDatabase() error = %v", openErr)
	}
	defer db.Close()
	var imported, skipped int
	var archiveStatus, sourceRelative string
	var digest []byte
	if err := db.QueryRowContext(t.Context(), `SELECT
        source_relative, source_digest, imported_count, skipped_count, archive_status
        FROM storage_imports
        WHERE component = ? AND source_id = ?`, catalogDatabaseComponent, legacyCatalogSourceID).Scan(
		&sourceRelative,
		&digest,
		&imported,
		&skipped,
		&archiveStatus,
	); err != nil {
		t.Fatalf("read import audit: %v", err)
	}
	if sourceRelative != legacyCatalogFilename || len(digest) != sha256.Size ||
		imported != 2 || skipped != 1 || archiveStatus != "complete" {
		t.Fatalf("audit = source:%q digest:%d imported:%d skipped:%d status:%q",
			sourceRelative, len(digest), imported, skipped, archiveStatus)
	}
	var issueCode string
	var issueDigest []byte
	if err := db.QueryRowContext(t.Context(), `SELECT issue_code, record_digest
        FROM storage_import_issues
        WHERE component = ? AND source_id = ?`, catalogDatabaseComponent, legacyCatalogSourceID).Scan(
		&issueCode,
		&issueDigest,
	); err != nil {
		t.Fatalf("read import issue: %v", err)
	}
	if issueCode != "invalid-catalog" || len(issueDigest) != sha256.Size ||
		strings.Contains(issueCode, "broken") || strings.Contains(issueCode, "model") {
		t.Fatalf("unsafe or unexpected issue = %q digest=%d", issueCode, len(issueDigest))
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, secondErr := loadCatalogs()
	if secondErr != nil || len(second.Entries) != 2 {
		t.Fatalf("second load = %#v, %v", second, secondErr)
	}
	db, reopenErr := openCatalogDatabase(t.Context())
	if reopenErr != nil {
		t.Fatalf("reopen database: %v", reopenErr)
	}
	defer db.Close()
	var imports, catalogs, models int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM storage_imports WHERE component = 'model-catalogs'`: &imports,
		`SELECT COUNT(*) FROM model_catalogs`:                                     &catalogs,
		`SELECT COUNT(*) FROM model_catalog_models`:                               &models,
	} {
		if err := db.QueryRowContext(t.Context(), query).Scan(destination); err != nil {
			t.Fatalf("%s error = %v", query, err)
		}
	}
	if imports != 1 || catalogs != 2 || models != 2 {
		t.Fatalf("idempotent counts = imports:%d catalogs:%d models:%d", imports, catalogs, models)
	}
}

func TestModelCatalogSQLiteMalformedLegacyIsAuditedAndArchived(t *testing.T) {
	home := useCatalogTestHome(t)
	legacy := []byte(`{"entries":{"secret-token":{"id":`) // intentionally malformed
	legacyPath := filepath.Join(home, legacyCatalogFilename)
	if err := os.WriteFile(legacyPath, legacy, 0o600); err != nil {
		t.Fatalf("write malformed legacy store: %v", err)
	}
	if err := os.Chmod(legacyPath, 0o600); err != nil {
		t.Fatalf("chmod malformed legacy store: %v", err)
	}
	store, err := loadCatalogs()
	if err != nil {
		t.Fatalf("loadCatalogs() error = %v", err)
	}
	if len(store.Entries) != 0 {
		t.Fatalf("entries = %#v, want empty", store.Entries)
	}
	archivePath := filepath.Join(home, "legacy-json", legacyCatalogArchiveLabel, legacyCatalogFilename)
	archived, err := os.ReadFile(archivePath)
	if err != nil || !bytes.Equal(archived, legacy) {
		t.Fatalf("archived malformed source = %q, %v", archived, err)
	}
	db, err := openCatalogDatabase(t.Context())
	if err != nil {
		t.Fatalf("openCatalogDatabase() error = %v", err)
	}
	defer db.Close()
	var imported, skipped int
	var code string
	if err := db.QueryRowContext(t.Context(), `SELECT imported_count, skipped_count
        FROM storage_imports WHERE component = ?`, catalogDatabaseComponent).Scan(&imported, &skipped); err != nil {
		t.Fatalf("read malformed import record: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT issue_code FROM storage_import_issues
        WHERE component = ?`, catalogDatabaseComponent).Scan(&code); err != nil {
		t.Fatalf("read malformed issue: %v", err)
	}
	if imported != 0 || skipped != 1 || code != "malformed-json" || strings.Contains(code, "secret") {
		t.Fatalf("malformed audit = imported:%d skipped:%d code:%q", imported, skipped, code)
	}
}

func TestModelCatalogSQLiteLegacyDuplicateIdentityFirstValidWins(t *testing.T) {
	home := useCatalogTestHome(t)
	legacy := `{"entries":{
        "same":{"id":"same","provider":"openai","api_base":"https://first.test","api_key_mask":"****","models":[{"id":"first"}],"fetched_at":"2025-01-01T00:00:00Z"},
        "same":{"id":"same","provider":"openai","api_base":"https://second.test","api_key_mask":"****","models":[{"id":"second"}],"fetched_at":"2025-01-02T00:00:00Z"}
    }}`
	legacyPath := filepath.Join(home, legacyCatalogFilename)
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write duplicate legacy store: %v", err)
	}
	if err := os.Chmod(legacyPath, 0o600); err != nil {
		t.Fatalf("chmod duplicate legacy store: %v", err)
	}
	store, err := loadCatalogs()
	if err != nil {
		t.Fatalf("loadCatalogs() error = %v", err)
	}
	entry := store.Entries["same"]
	if entry == nil || entry.APIBase != "https://first.test" || len(entry.Models) != 1 ||
		entry.Models[0].ID != "first" {
		t.Fatalf("winning duplicate = %#v", entry)
	}
	db, err := openCatalogDatabase(t.Context())
	if err != nil {
		t.Fatalf("openCatalogDatabase() error = %v", err)
	}
	defer db.Close()
	var imported, skipped int
	var code string
	if err := db.QueryRowContext(t.Context(), `SELECT imported_count, skipped_count
        FROM storage_imports WHERE component = ?`, catalogDatabaseComponent).Scan(&imported, &skipped); err != nil {
		t.Fatalf("read duplicate import record: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), `SELECT issue_code FROM storage_import_issues
        WHERE component = ?`, catalogDatabaseComponent).Scan(&code); err != nil {
		t.Fatalf("read duplicate import issue: %v", err)
	}
	if imported != 1 || skipped != 1 || code != "identity-conflict" {
		t.Fatalf("duplicate audit = imported:%d skipped:%d code:%q", imported, skipped, code)
	}
}

func TestModelCatalogSQLiteRejectsTooNewAndInvalidSchema(t *testing.T) {
	useCatalogTestHome(t)
	db, err := openCatalogDatabase(t.Context())
	if err != nil {
		t.Fatalf("openCatalogDatabase() error = %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `PRAGMA user_version = 2`); err != nil {
		t.Fatalf("set too-new user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := loadCatalogs(); !errors.Is(err, sqlitestore.ErrTooNew) {
		t.Fatalf("loadCatalogs() error = %v, want ErrTooNew", err)
	}
	if err := SaveCatalog("openai", "", "key", []CatalogModel{{ID: "model"}}); !errors.Is(err, sqlitestore.ErrTooNew) {
		t.Fatalf("SaveCatalog() error = %v, want ErrTooNew", err)
	}
	handler := &Handler{}
	list := httptest.NewRecorder()
	handler.handleListCatalogs(list, httptest.NewRequest(http.MethodGet, "/", nil))
	if list.Code != http.StatusInternalServerError || !strings.Contains(list.Body.String(), "Failed to load catalogs") {
		t.Fatalf("too-new list response = %d %q", list.Code, list.Body.String())
	}
	deleteRequest := httptest.NewRequest(http.MethodDelete, "/", nil)
	deleteRequest.SetPathValue("id", "catalog")
	deleted := httptest.NewRecorder()
	handler.handleDeleteCatalog(deleted, deleteRequest)
	if deleted.Code != http.StatusInternalServerError ||
		!strings.Contains(deleted.Body.String(), "Failed to load catalogs") {
		t.Fatalf("too-new delete response = %d %q", deleted.Code, deleted.Body.String())
	}

	if raw, err := sql.Open("sqlite", catalogDatabasePath()); err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	} else {
		if _, err := raw.Exec(`PRAGMA user_version = 1`); err != nil {
			raw.Close()
			t.Fatalf("restore user_version: %v", err)
		}
		if _, err := raw.Exec(`CREATE INDEX unexpected_catalog_index ON model_catalogs(api_base)`); err != nil {
			raw.Close()
			t.Fatalf("create unexpected index: %v", err)
		}
		if err := raw.Close(); err != nil {
			t.Fatalf("close raw database: %v", err)
		}
	}
	if _, err := loadCatalogs(); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
		t.Fatalf("loadCatalogs() schema error = %v, want ErrInvalidSchema", err)
	}
}

func TestModelCatalogSQLiteRejectsInvalidRelationalDataOnReopen(t *testing.T) {
	t.Run("noncanonical metadata", func(t *testing.T) {
		useCatalogTestHome(t)
		db, err := openCatalogDatabase(t.Context())
		if err != nil {
			t.Fatalf("openCatalogDatabase() error = %v", err)
		}
		insertRawCatalogForTest(t, db, "catalog")
		if _, err := db.ExecContext(t.Context(), `INSERT INTO model_catalog_models
            (catalog_id, position, model_id, owned_by, extra_json)
            VALUES ('catalog', 0, 'model', '', ?)`, []byte(`{"z":1,"a":2}`)); err != nil {
			db.Close()
			t.Fatalf("insert noncanonical metadata: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if _, err := loadCatalogs(); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("loadCatalogs() error = %v, want ErrInvalidSchema", err)
		}
	})

	t.Run("noncontiguous positions", func(t *testing.T) {
		useCatalogTestHome(t)
		db, err := openCatalogDatabase(t.Context())
		if err != nil {
			t.Fatalf("openCatalogDatabase() error = %v", err)
		}
		insertRawCatalogForTest(t, db, "catalog")
		if _, err := db.ExecContext(t.Context(), `INSERT INTO model_catalog_models
            (catalog_id, position, model_id, owned_by)
            VALUES ('catalog', 1, 'model', '')`); err != nil {
			db.Close()
			t.Fatalf("insert noncontiguous model: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if _, err := loadCatalogs(); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("loadCatalogs() error = %v, want ErrInvalidSchema", err)
		}
	})
}

func TestModelCatalogSQLiteConstraintsAndInvalidMetadata(t *testing.T) {
	useCatalogTestHome(t)
	if err := SaveCatalog("", "", "key", []CatalogModel{{ID: "model"}}); err == nil {
		t.Fatal("SaveCatalog() accepted empty provider")
	}
	if err := SaveCatalog("openai", "", "key", []CatalogModel{{ID: ""}}); err == nil {
		t.Fatal("SaveCatalog() accepted empty model identity")
	}
	if err := SaveCatalog("openai", "", "key", []CatalogModel{{
		ID:    "model",
		Extra: map[string]any{"bad": func() {}},
	}}); err == nil {
		t.Fatal("SaveCatalog() accepted non-JSON metadata")
	}
	db, err := openCatalogDatabase(t.Context())
	if err != nil {
		t.Fatalf("openCatalogDatabase() error = %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO model_catalog_models
        (catalog_id, position, model_id, owned_by) VALUES ('missing', 0, 'model', '')`); err == nil {
		t.Fatal("foreign key constraint accepted missing catalog")
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO model_catalogs
		(catalog_id, provider, api_base, api_key_mask, fetched_at_unix_seconds,
		 fetched_at_nanosecond, models_is_null)
		VALUES ('invalid', 'OpenAI', '', '', 1, 0, 0)`); err == nil {
		t.Fatal("provider normalization constraint accepted mixed case")
	}
}

func TestModelCatalogSQLiteAggregateMetadataBounds(t *testing.T) {
	t.Run("transactional saves and replacement", func(t *testing.T) {
		useCatalogTestHome(t)
		fullExtra := maximumCatalogExtraFixture(t)
		fullModelCount := int(maximumCatalogExtraBytes / int64(maximumModelExtraBytes))
		models := make([]CatalogModel, fullModelCount)
		for index := range models {
			models[index] = CatalogModel{
				ID:    fmt.Sprintf("model-%02d", index),
				Extra: fullExtra,
			}
		}
		if err := SaveCatalog("openai", "https://full.test", "full-key", models); err != nil {
			t.Fatalf("SaveCatalog(full metadata budget) error = %v", err)
		}
		if err := SaveCatalog("openai", "https://overflow.test", "overflow-key", []CatalogModel{{
			ID:    "overflow",
			Extra: map[string]any{"extra": true},
		}}); err == nil || !strings.Contains(err.Error(), "record limit") {
			t.Fatalf("SaveCatalog(overflow) error = %v", err)
		}
		store, err := loadCatalogs()
		if err != nil || len(store.Entries) != 1 {
			t.Fatalf("store after rejected overflow = %#v, %v", store, err)
		}
		if err := SaveCatalog("openai", "https://full.test", "full-key", nil); err != nil {
			t.Fatalf("SaveCatalog(release metadata budget) error = %v", err)
		}
		if err := SaveCatalog("openai", "https://overflow.test", "overflow-key", []CatalogModel{{
			ID:    "now-fits",
			Extra: map[string]any{"extra": true},
		}}); err != nil {
			t.Fatalf("SaveCatalog(after replacement) error = %v", err)
		}
	})

	t.Run("whole-store replacement", func(t *testing.T) {
		useCatalogTestHome(t)
		baseline := &CatalogEntry{
			ID:         "baseline",
			Provider:   "openai",
			APIKeyMask: "****",
			FetchedAt:  "2025-01-01T00:00:00Z",
		}
		if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
			"baseline": baseline,
		}}); err != nil {
			t.Fatalf("saveCatalogs(baseline) error = %v", err)
		}
		fullExtra := maximumCatalogExtraFixture(t)
		entries := make(map[string]*CatalogEntry)
		for index := 0; index <= int(maximumCatalogExtraBytes/int64(maximumModelExtraBytes)); index++ {
			id := fmt.Sprintf("catalog-%02d", index)
			entries[id] = &CatalogEntry{
				ID:         id,
				Provider:   "openai",
				APIKeyMask: "****",
				Models:     []CatalogModel{{ID: "model", Extra: fullExtra}},
				FetchedAt:  "2025-01-01T00:00:00Z",
			}
		}
		if err := saveCatalogs(&CatalogStore{Entries: entries}); err == nil ||
			!strings.Contains(err.Error(), "metadata limit") {
			t.Fatalf("saveCatalogs(aggregate overflow) error = %v", err)
		}
		store, err := loadCatalogs()
		if err != nil || len(store.Entries) != 1 || store.Entries["baseline"] == nil {
			t.Fatalf("transactional whole-store rollback = %#v, %v", store, err)
		}
	})

	t.Run("reopen validation", func(t *testing.T) {
		useCatalogTestHome(t)
		fullExtra := maximumCatalogExtraFixture(t)
		models := make([]CatalogModel, int(maximumCatalogExtraBytes/int64(maximumModelExtraBytes)))
		for index := range models {
			models[index] = CatalogModel{ID: fmt.Sprintf("model-%02d", index), Extra: fullExtra}
		}
		if err := SaveCatalog("openai", "https://full.test", "full-key", models); err != nil {
			t.Fatalf("SaveCatalog(full metadata budget) error = %v", err)
		}
		db, err := openCatalogDatabase(t.Context())
		if err != nil {
			t.Fatalf("openCatalogDatabase() error = %v", err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO model_catalogs (
			catalog_id, provider, api_base, api_key_mask, fetched_at_unix_seconds,
			fetched_at_nanosecond, models_is_null
		) VALUES ('overflow', 'openai', '', '****', 1, 0, 0)`); err != nil {
			db.Close()
			t.Fatalf("insert overflow parent: %v", err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO model_catalog_models (
			catalog_id, position, model_id, owned_by, extra_json
		) VALUES ('overflow', 0, 'model', '', ?)`, []byte(`{"extra":true}`)); err != nil {
			db.Close()
			t.Fatalf("insert overflow metadata: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		if _, err := loadCatalogs(); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("loadCatalogs() aggregate error = %v, want ErrInvalidSchema", err)
		}
	})
}

func TestModelCatalogSQLiteRejectsCorruptAndUnsafeFirstOpen(t *testing.T) {
	t.Run("corrupt database", func(t *testing.T) {
		home := useCatalogTestHome(t)
		databasePath := filepath.Join(home, catalogDatabaseFilename)
		if err := os.WriteFile(databasePath, []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatalf("write corrupt database: %v", err)
		}
		if err := os.Chmod(databasePath, 0o600); err != nil {
			t.Fatalf("chmod corrupt database: %v", err)
		}
		if _, err := loadCatalogs(); err == nil {
			t.Fatal("loadCatalogs() accepted a corrupt database")
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("unsafe legacy mode", func(t *testing.T) {
			home := useCatalogTestHome(t)
			legacyPath := filepath.Join(home, legacyCatalogFilename)
			if err := os.WriteFile(legacyPath, []byte(`{"entries":{}}`), 0o600); err != nil {
				t.Fatalf("write unsafe legacy source: %v", err)
			}
			if err := os.Chmod(legacyPath, 0o666); err != nil {
				t.Fatalf("chmod unsafe legacy source: %v", err)
			}
			if _, err := loadCatalogs(); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("loadCatalogs() unsafe-mode error = %v", err)
			}
			assertCatalogLegacyNotArchived(t, home, legacyPath)
		})
	}

	t.Run("legacy source symlink", func(t *testing.T) {
		home := useCatalogTestHome(t)
		target := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(target, []byte(`{"entries":{}}`), 0o600); err != nil {
			t.Fatalf("write legacy symlink target: %v", err)
		}
		if err := os.Chmod(target, 0o600); err != nil {
			t.Fatalf("chmod legacy symlink target: %v", err)
		}
		legacyPath := filepath.Join(home, legacyCatalogFilename)
		if err := os.Symlink(target, legacyPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := loadCatalogs(); err == nil ||
			(!strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "unsafe")) {
			t.Fatalf("loadCatalogs() legacy symlink error = %v", err)
		}
		if data, err := os.ReadFile(target); err != nil || string(data) != `{"entries":{}}` {
			t.Fatalf("legacy symlink target changed = %q, %v", data, err)
		}
		assertCatalogLegacyNotArchived(t, home, legacyPath)
	})

	t.Run("database symlink", func(t *testing.T) {
		home := useCatalogTestHome(t)
		target := filepath.Join(t.TempDir(), "outside.db")
		if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
			t.Fatalf("write database symlink target: %v", err)
		}
		if err := os.Chmod(target, 0o600); err != nil {
			t.Fatalf("chmod database symlink target: %v", err)
		}
		if err := os.Symlink(target, filepath.Join(home, catalogDatabaseFilename)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := loadCatalogs(); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("loadCatalogs() database symlink error = %v", err)
		}
		if data, err := os.ReadFile(target); err != nil || string(data) != "keep" {
			t.Fatalf("database symlink target changed = %q, %v", data, err)
		}
	})

	t.Run("oversized legacy source", func(t *testing.T) {
		home := useCatalogTestHome(t)
		legacyPath := filepath.Join(home, legacyCatalogFilename)
		file, err := os.OpenFile(legacyPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("open oversized legacy source: %v", err)
		}
		if err := file.Truncate(legacyCatalogMaxBytes + 1); err != nil {
			file.Close()
			t.Fatalf("truncate oversized legacy source: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close oversized legacy source: %v", err)
		}
		if err := os.Chmod(legacyPath, 0o600); err != nil {
			t.Fatalf("chmod oversized legacy source: %v", err)
		}
		if _, err := loadCatalogs(); err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("loadCatalogs() oversized error = %v", err)
		}
		assertCatalogLegacyNotArchived(t, home, legacyPath)
	})

	t.Run("legacy aggregate metadata", func(t *testing.T) {
		home := useCatalogTestHome(t)
		fullExtra := maximumCatalogExtraFixture(t)
		models := make([]CatalogModel, int(maximumCatalogExtraBytes/int64(maximumModelExtraBytes))+1)
		for index := range models {
			models[index] = CatalogModel{ID: fmt.Sprintf("model-%02d", index), Extra: fullExtra}
		}
		legacyStore := CatalogStore{Entries: map[string]*CatalogEntry{
			"aggregate": {
				ID:         "aggregate",
				Provider:   "openai",
				APIKeyMask: "****",
				Models:     models,
				FetchedAt:  "2025-01-01T00:00:00Z",
			},
		}}
		data, err := json.Marshal(legacyStore)
		if err != nil {
			t.Fatalf("marshal aggregate legacy source: %v", err)
		}
		legacyPath := filepath.Join(home, legacyCatalogFilename)
		if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
			t.Fatalf("write aggregate legacy source: %v", err)
		}
		if err := os.Chmod(legacyPath, 0o600); err != nil {
			t.Fatalf("chmod aggregate legacy source: %v", err)
		}
		if _, err := loadCatalogs(); err == nil || !strings.Contains(err.Error(), "aggregate limit") {
			t.Fatalf("loadCatalogs() aggregate legacy error = %v", err)
		}
		assertCatalogLegacyNotArchived(t, home, legacyPath)
	})
}

func TestModelCatalogSQLiteConcurrentSaveAndRead(t *testing.T) {
	useCatalogTestHome(t)
	const writers = 20
	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := SaveCatalog(
				"openai",
				fmt.Sprintf("https://catalog-%02d.test/v1", writer),
				fmt.Sprintf("secret-%02d", writer),
				[]CatalogModel{
					{ID: fmt.Sprintf("model-%02d-a", writer), Extra: map[string]any{"writer": writer}},
					{ID: fmt.Sprintf("model-%02d-b", writer)},
				},
			)
			errorsByWriter <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("concurrent SaveCatalog() error = %v", err)
		}
	}
	store, err := loadCatalogs()
	if err != nil {
		t.Fatalf("loadCatalogs() error = %v", err)
	}
	if len(store.Entries) != writers {
		t.Fatalf("catalog count = %d, want %d", len(store.Entries), writers)
	}
	for writer := 0; writer < writers; writer++ {
		base := fmt.Sprintf("https://catalog-%02d.test/v1", writer)
		key := generateCatalogKey("openai", base, fmt.Sprintf("secret-%02d", writer))
		entry := store.Entries[key]
		if entry == nil || len(entry.Models) != 2 ||
			entry.Models[0].ID != fmt.Sprintf("model-%02d-a", writer) ||
			entry.Models[1].ID != fmt.Sprintf("model-%02d-b", writer) {
			t.Fatalf("catalog %d = %#v", writer, entry)
		}
	}

	const sameKeyWriters = 12
	start = make(chan struct{})
	errorsByWriter = make(chan error, sameKeyWriters)
	wait = sync.WaitGroup{}
	for writer := 0; writer < sameKeyWriters; writer++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsByWriter <- SaveCatalog("anthropic", "https://shared.test", "shared-key", []CatalogModel{
				{ID: fmt.Sprintf("snapshot-%02d-a", writer)},
				{ID: fmt.Sprintf("snapshot-%02d-b", writer)},
			})
		}()
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatalf("same-key SaveCatalog() error = %v", err)
		}
	}
	sharedKey := generateCatalogKey("anthropic", "https://shared.test", "shared-key")
	store, err = loadCatalogs()
	if err != nil {
		t.Fatalf("loadCatalogs() after same-key writes error = %v", err)
	}
	shared := store.Entries[sharedKey]
	if shared == nil || len(shared.Models) != 2 {
		t.Fatalf("shared catalog = %#v", shared)
	}
	prefix := strings.TrimSuffix(shared.Models[0].ID, "-a")
	if shared.Models[1].ID != prefix+"-b" {
		t.Fatalf("torn shared snapshot = %#v", shared.Models)
	}
	db, err := openCatalogDatabase(t.Context())
	if err != nil {
		t.Fatalf("openCatalogDatabase() error = %v", err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(t.Context(), `SELECT version FROM model_catalogs
        WHERE catalog_id = ?`, sharedKey).Scan(&version); err != nil {
		t.Fatalf("read shared version: %v", err)
	}
	if version != sameKeyWriters {
		t.Fatalf("shared version = %d, want %d", version, sameKeyWriters)
	}
}

func TestModelCatalogRemainingCoverageBoundaries(t *testing.T) {
	t.Run("aggregate and insert helpers", func(t *testing.T) {
		useCatalogTestHome(t)
		fullExtra := maximumCatalogExtraFixture(t)
		models := make(
			[]CatalogModel,
			int(maximumCatalogExtraBytes/int64(maximumModelExtraBytes))+1,
		)
		for index := range models {
			models[index] = CatalogModel{
				ID:    fmt.Sprintf("oversized-%02d", index),
				Extra: fullExtra,
			}
		}
		if err := SaveCatalog("openai", "https://oversized.test", "key", models); err == nil ||
			!strings.Contains(err.Error(), "metadata limit") {
			t.Fatalf("SaveCatalog(per-entry aggregate) error = %v", err)
		}

		db, err := openCatalogDatabase(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		invalidTime := &CatalogEntry{
			ID: "invalid-time", Provider: "openai", FetchedAt: "invalid",
		}
		if err := insertCatalogEntry(t.Context(), conn, invalidTime, nil); err == nil {
			t.Fatal("insertCatalogEntry accepted an invalid timestamp")
		}
		valid := &CatalogEntry{
			ID: "duplicate", Provider: "openai", FetchedAt: "2025-01-01T00:00:00Z",
		}
		if err := insertCatalogEntry(t.Context(), conn, valid, nil); err != nil {
			t.Fatal(err)
		}
		if err := insertCatalogEntry(t.Context(), conn, valid, nil); err == nil {
			t.Fatal("insertCatalogEntry accepted a duplicate parent")
		}
		if err := insertCatalogModels(
			t.Context(),
			conn,
			"missing-parent",
			[]CatalogModel{{ID: "model"}},
			[][]byte{nil},
		); err == nil {
			t.Fatal("insertCatalogModels accepted a missing parent")
		}
	})

	t.Run("legacy decoder failures", func(t *testing.T) {
		for _, raw := range []string{
			``,
			`null trailing`,
			`{`,
			`{"`,
			`{"ignored":`,
			`{"entries":`,
			`{"entries":{`,
			`{"entries":{"`,
			`{"entries":{"catalog":`,
			`{"entries":{"catalog":null`,
			`{"entries":{}`,
		} {
			if _, err := decodeLegacyCatalogRecords([]byte(raw)); err == nil {
				t.Fatalf("decodeLegacyCatalogRecords(%q) returned nil", raw)
			}
		}
	})

	t.Run("legacy importer selected skips", func(t *testing.T) {
		useCatalogTestHome(t)
		db, err := openCatalogDatabase(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		data := []byte(`{"entries":{
			"null-entry":null,
			"bad-extra":{"id":"bad-extra","provider":"openai","models":[{"id":"m","extra":[]}],"fetched_at":"2025-01-01T00:00:00Z"},
			"mismatched-key":{"id":"different-id","provider":"openai","models":[],"fetched_at":"2025-01-01T00:00:00Z"}
		}}`)
		result, err := importLegacyCatalogStore(t.Context(), conn, sqlitestore.LegacyInput{
			ID: legacyCatalogSourceID, Data: data, Digest: sha256.Sum256(data),
		})
		if err != nil || result.Imported != 0 || result.Skipped != 3 {
			t.Fatalf("selected-skip import = %#v, %v", result, err)
		}
	})

	t.Run("legacy importer query failure", func(t *testing.T) {
		useCatalogTestHome(t)
		db, err := openCatalogDatabase(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			db.Close()
			t.Fatal(err)
		}
		data := []byte(`{"entries":{}}`)
		if _, err := importLegacyCatalogStore(t.Context(), conn, sqlitestore.LegacyInput{
			ID: legacyCatalogSourceID, Data: data, Digest: sha256.Sum256(data),
		}); err == nil {
			t.Fatal("importLegacyCatalogStore accepted a closed connection")
		}
		_ = db.Close()
	})
}

func TestImportLegacyCatalogStoreRejectsUnboundedCatalogCount(t *testing.T) {
	useCatalogTestHome(t)
	db, err := openCatalogDatabase(t.Context())
	if err != nil {
		t.Fatalf("openCatalogDatabase() error = %v", err)
	}
	defer db.Close()
	entries := make(map[string]json.RawMessage, maximumCatalogs+1)
	for index := 0; index <= maximumCatalogs; index++ {
		entries[fmt.Sprintf("catalog-%06d", index)] = json.RawMessage("null")
	}
	data, err := json.Marshal(map[string]any{"entries": entries})
	if err != nil {
		t.Fatalf("marshal oversized fixture: %v", err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("db.Conn() error = %v", err)
	}
	defer conn.Close()
	_, err = importLegacyCatalogStore(t.Context(), conn, sqlitestore.LegacyInput{
		ID:     legacyCatalogSourceID,
		Data:   data,
		Digest: sha256.Sum256(data),
	})
	if err == nil || !strings.Contains(err.Error(), "count exceeds") {
		t.Fatalf("importLegacyCatalogStore() error = %v, want bounded count error", err)
	}
}

func TestModelCatalogLegacySQLiteAuthoritativeAndHelperValidation(t *testing.T) {
	home := useCatalogTestHome(t)
	existing := &CatalogEntry{
		ID:         "same",
		Provider:   "openai",
		APIBase:    "https://sqlite.test",
		APIKeyMask: "****",
		Models:     []CatalogModel{{ID: "sqlite-model"}},
		FetchedAt:  "2025-01-01T00:00:00Z",
	}
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{"same": existing}}); err != nil {
		t.Fatalf("saveCatalogs() error = %v", err)
	}
	legacy := `{"entries":{"same":{"id":"same","provider":"openai","api_base":"https://legacy.test","api_key_mask":"****","models":[{"id":"legacy-model"}],"fetched_at":"2025-01-02T00:00:00Z"}}}`
	legacyPath := filepath.Join(home, legacyCatalogFilename)
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy conflict: %v", err)
	}
	if err := os.Chmod(legacyPath, 0o600); err != nil {
		t.Fatalf("chmod legacy conflict: %v", err)
	}
	store, err := loadCatalogs()
	if err != nil {
		t.Fatalf("loadCatalogs() error = %v", err)
	}
	if got := store.Entries["same"]; got == nil || got.APIBase != "https://sqlite.test" ||
		len(got.Models) != 1 || got.Models[0].ID != "sqlite-model" {
		t.Fatalf("SQLite authoritative catalog = %#v", got)
	}
	db, err := openCatalogDatabase(t.Context())
	if err != nil {
		t.Fatalf("openCatalogDatabase() error = %v", err)
	}
	defer db.Close()
	var code string
	if err := db.QueryRowContext(t.Context(), `SELECT issue_code FROM storage_import_issues
        WHERE component = ?`, catalogDatabaseComponent).Scan(&code); err != nil {
		t.Fatalf("read authoritative issue: %v", err)
	}
	if code != "sqlite-authoritative" {
		t.Fatalf("issue code = %q, want sqlite-authoritative", code)
	}

	if err := saveCatalogs(nil); err == nil {
		t.Fatal("saveCatalogs(nil) returned nil")
	}
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{
		"wrong-key": existing,
	}}); err == nil {
		t.Fatal("saveCatalogs() accepted mismatched map identity")
	}
	if _, _, err := normalizeCatalogEntry(nil); err == nil {
		t.Fatal("normalizeCatalogEntry(nil) returned nil error")
	}
	invalidEntries := []*CatalogEntry{
		{ID: "catalog", Provider: "", FetchedAt: "2025-01-01T00:00:00Z"},
		{ID: " catalog", Provider: "openai", FetchedAt: "2025-01-01T00:00:00Z"},
		{ID: "catalog", Provider: "openai", APIKeyMask: "bad\nmask", FetchedAt: "2025-01-01T00:00:00Z"},
		{ID: "catalog", Provider: "openai", FetchedAt: "not-a-time"},
		{ID: "catalog", Provider: "openai", FetchedAt: "2025-01-01T00:00:00Z", Models: []CatalogModel{{ID: " model"}}},
	}
	for _, entry := range invalidEntries {
		if _, _, err := normalizeCatalogEntry(entry); err == nil {
			t.Fatalf("normalizeCatalogEntry(%#v) returned nil error", entry)
		}
	}
	for _, value := range []string{" bad", "bad\x00", "bad\n", string([]byte{0xff})} {
		if validCatalogText(value, 1, 100, true) {
			t.Fatalf("validCatalogText(%q) = true", value)
		}
	}
	if validCatalogText("", 1, 100, true) || validCatalogText(strings.Repeat("a", 101), 1, 100, true) {
		t.Fatal("validCatalogText accepted an invalid byte length")
	}
	for _, timestamp := range []string{"invalid", "10000-01-01T00:00:00Z"} {
		if _, _, err := parseCatalogTimestamp(timestamp); err == nil {
			t.Fatalf("parseCatalogTimestamp(%q) returned nil error", timestamp)
		}
	}
	for _, timestamp := range []string{
		"0000-01-01T00:00:00Z",
		"1969-12-31T23:59:59.123456789Z",
		"2300-01-01T00:00:00Z",
		"9999-12-31T23:59:59.999999999Z",
	} {
		seconds, nanoseconds, err := parseCatalogTimestamp(timestamp)
		if err != nil || time.Unix(seconds, nanoseconds).UTC().Format(time.RFC3339Nano) != timestamp {
			t.Fatalf("parseCatalogTimestamp(%q) = (%d, %d, %v)", timestamp, seconds, nanoseconds, err)
		}
	}
	for _, raw := range [][]byte{
		[]byte(`[1,2,3]`),
		[]byte(`{"a":1} trailing`),
		bytes.Repeat([]byte(" "), maximumModelExtraBytes+1),
	} {
		if _, _, err := canonicalCatalogExtra(raw); err == nil {
			t.Fatalf("canonicalCatalogExtra(%q...) returned nil error", raw[:min(len(raw), 16)])
		}
	}
	metadata, canonical, metadataErr := canonicalCatalogExtra([]byte("null"))
	if metadataErr != nil || metadata != nil || canonical != nil {
		t.Fatalf("canonicalCatalogExtra(null) = %#v %q %v", metadata, canonical, metadataErr)
	}
	if err := insertCatalogModels(t.Context(), nil, "catalog", []CatalogModel{{ID: "model"}}, nil); err == nil {
		t.Fatal("insertCatalogModels() accepted mismatched metadata count")
	}
}

func TestDecodeLegacyCatalogRecordsEnvelopeVariants(t *testing.T) {
	valid, err := decodeLegacyCatalogRecords([]byte(`{"ignored":{"nested":true},"entries":null}`))
	if err != nil || len(valid) != 0 {
		t.Fatalf("decode valid empty envelope = %#v, %v", valid, err)
	}
	valid, err = decodeLegacyCatalogRecords([]byte(`null`))
	if err != nil || len(valid) != 0 {
		t.Fatalf("decode null envelope = %#v, %v", valid, err)
	}
	for _, raw := range []string{
		`[]`,
		`{"entries":[],"other":1}`,
		`{"entries":{},"entries":{}}`,
		`{"entries":{}} trailing`,
		`{"entries":`,
	} {
		if _, err := decodeLegacyCatalogRecords([]byte(raw)); err == nil {
			t.Fatalf("decodeLegacyCatalogRecords(%q) returned nil error", raw)
		}
	}
	decoder := json.NewDecoder(strings.NewReader(`1 2`))
	var first int
	if err := decoder.Decode(&first); err != nil {
		t.Fatalf("decode first JSON value: %v", err)
	}
	if err := requireJSONEOF(decoder); err == nil {
		t.Fatal("requireJSONEOF() accepted a second JSON value")
	}
	decoder = json.NewDecoder(strings.NewReader(`1 {`))
	if err := decoder.Decode(&first); err != nil {
		t.Fatalf("decode first malformed-tail value: %v", err)
	}
	if err := requireJSONEOF(decoder); err == nil {
		t.Fatal("requireJSONEOF() accepted malformed trailing JSON")
	}
}

func TestSaveCatalogsReplacesWholeStoreTransactionally(t *testing.T) {
	useCatalogTestHome(t)
	first := &CatalogEntry{
		ID:         "first",
		Provider:   "openai",
		APIBase:    "https://first.test",
		APIKeyMask: "****",
		Models:     []CatalogModel{{ID: "one"}},
		FetchedAt:  "2025-01-02T03:04:05Z",
	}
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{"first": first}}); err != nil {
		t.Fatalf("saveCatalogs(first) error = %v", err)
	}
	invalid := &CatalogEntry{
		ID:         "second",
		Provider:   "openai",
		APIBase:    "https://second.test",
		APIKeyMask: "****",
		Models:     []CatalogModel{{ID: ""}},
		FetchedAt:  "2025-01-02T03:04:05Z",
	}
	if err := saveCatalogs(&CatalogStore{Entries: map[string]*CatalogEntry{"second": invalid}}); err == nil {
		t.Fatal("saveCatalogs(invalid) returned nil")
	}
	store, err := loadCatalogs()
	if err != nil || len(store.Entries) != 1 || store.Entries["first"] == nil {
		t.Fatalf("transaction rollback store = %#v, %v", store, err)
	}
}

func useCatalogTestHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".picoclaw")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", home, err)
	}
	t.Setenv("PICOCLAW_HOME", home)
	return home
}

func compactCatalogSQL(value string) string {
	return strings.Join(strings.Fields(strings.TrimSuffix(strings.TrimSpace(value), ";")), " ")
}

func maximumCatalogExtraFixture(t *testing.T) map[string]any {
	t.Helper()
	const overhead = len(`{"payload":""}`)
	metadata := map[string]any{
		"payload": strings.Repeat("x", maximumModelExtraBytes-overhead),
	}
	_, canonical, err := canonicalCatalogExtraValue(metadata)
	if err != nil {
		t.Fatalf("canonicalCatalogExtraValue(maximum fixture): %v", err)
	}
	if len(canonical) != maximumModelExtraBytes {
		t.Fatalf("maximum metadata fixture bytes = %d, want %d", len(canonical), maximumModelExtraBytes)
	}
	return metadata
}

func assertCatalogLegacyNotArchived(t *testing.T, home, sourcePath string) {
	t.Helper()
	if _, err := os.Lstat(sourcePath); err != nil {
		t.Fatalf("legacy source was removed: %v", err)
	}
	archivePath := filepath.Join(
		home,
		"legacy-json",
		legacyCatalogArchiveLabel,
		legacyCatalogFilename,
	)
	if _, err := os.Lstat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("legacy source was unexpectedly archived: %v", err)
	}
}

func insertRawCatalogForTest(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO model_catalogs
		(catalog_id, provider, api_base, api_key_mask, fetched_at_unix_seconds,
		 fetched_at_nanosecond, models_is_null)
		VALUES (?, 'openai', '', '****', 1, 0, 0)`, id); err != nil {
		t.Fatalf("insert raw catalog: %v", err)
	}
}
