package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	catalogDatabaseFilename   = "model-catalogs.db"
	legacyCatalogFilename     = "model_catalogs.json"
	catalogDatabaseComponent  = "model-catalogs"
	legacyCatalogSourceID     = "model-catalogs-json-v1"
	legacyCatalogArchiveLabel = "model-catalogs-v1"
	legacyCatalogMaxBytes     = int64(64 << 20)

	maximumCatalogs          = 100_000
	maximumCatalogModels     = 1_000_000
	maximumCatalogIDBytes    = 16 << 10
	maximumProviderBytes     = 256
	maximumAPIBaseBytes      = 8 << 10
	maximumAPIKeyMaskBytes   = 1 << 10
	maximumModelIDBytes      = 8 << 10
	maximumModelOwnerBytes   = 1 << 10
	maximumModelExtraBytes   = 1 << 20
	maximumCatalogExtraBytes = int64(16 << 20)
	maximumCatalogAuditItems = 512
)

const modelCatalogsSchema = `CREATE TABLE model_catalogs (
    catalog_id       TEXT PRIMARY KEY,
    provider         TEXT NOT NULL,
    api_base         TEXT NOT NULL,
    api_key_mask     TEXT NOT NULL,
    fetched_at_unix_seconds INTEGER NOT NULL
        CHECK(fetched_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    fetched_at_nanosecond INTEGER NOT NULL
        CHECK(fetched_at_nanosecond BETWEEN 0 AND 999999999),
    models_is_null   INTEGER NOT NULL CHECK(models_is_null IN (0, 1)),
    version          INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(catalog_id AS BLOB)) BETWEEN 1 AND 16384),
    CHECK(catalog_id = trim(catalog_id)),
    CHECK(instr(catalog_id, char(0)) = 0),
    CHECK(length(CAST(provider AS BLOB)) BETWEEN 1 AND 256),
    CHECK(provider = lower(trim(provider))),
    CHECK(instr(provider, char(0)) = 0),
    CHECK(length(CAST(api_base AS BLOB)) <= 8192),
    CHECK(api_base = rtrim(trim(api_base), '/')),
    CHECK(instr(api_base, char(0)) = 0),
    CHECK(length(CAST(api_key_mask AS BLOB)) <= 1024),
    CHECK(instr(api_key_mask, char(0)) = 0)
) STRICT`

const catalogModelsSchema = `CREATE TABLE model_catalog_models (
    catalog_id  TEXT NOT NULL,
    position    INTEGER NOT NULL CHECK(position >= 0),
    model_id    TEXT NOT NULL,
    owned_by    TEXT NOT NULL,
    extra_json  BLOB,
    PRIMARY KEY(catalog_id, position),
    FOREIGN KEY(catalog_id) REFERENCES model_catalogs(catalog_id) ON DELETE CASCADE,
    CHECK(length(CAST(model_id AS BLOB)) BETWEEN 1 AND 8192),
    CHECK(model_id = trim(model_id)),
    CHECK(instr(model_id, char(0)) = 0),
    CHECK(length(CAST(owned_by AS BLOB)) <= 1024),
    CHECK(instr(owned_by, char(0)) = 0),
    CHECK(extra_json IS NULL OR (typeof(extra_json) = 'blob' AND length(extra_json) <= 1048576))
) STRICT`

const modelCatalogsProviderIndexSchema = `CREATE INDEX model_catalogs_provider_idx
    ON model_catalogs(provider, catalog_id)`

const catalogModelsIdentityIndexSchema = `CREATE INDEX model_catalog_models_identity_idx
    ON model_catalog_models(model_id, catalog_id, position)`

var errLegacyCatalogLimit = errors.New("legacy model catalog count exceeds its limit")

// CatalogModel represents a single model entry in a saved catalog.
type CatalogModel struct {
	ID      string         `json:"id"`
	OwnedBy string         `json:"owned_by,omitempty"`
	Extra   map[string]any `json:"extra,omitempty"`
}

// CatalogEntry is a saved list of upstream models fetched for a specific provider+key combination.
type CatalogEntry struct {
	ID         string         `json:"id"`
	Provider   string         `json:"provider"`
	APIBase    string         `json:"api_base"`
	APIKeyMask string         `json:"api_key_mask"`
	Models     []CatalogModel `json:"models"`
	FetchedAt  string         `json:"fetched_at"`
}

// CatalogStore holds all saved model catalogs.
type CatalogStore struct {
	Entries map[string]*CatalogEntry `json:"entries"`
}

func catalogDatabasePath() string {
	return filepath.Join(config.GetHome(), catalogDatabaseFilename)
}

func resolvedCatalogDatabasePath() (string, error) {
	return filepath.Abs(catalogDatabasePath())
}

// generateCatalogKey creates a deterministic key for a provider+base+key combination.
func generateCatalogKey(provider, apiBase, apiKey string) string {
	provider = providers.NormalizeProvider(provider)
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	hash := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("%s|%s|%x", provider, apiBase, hash[:6])
}

// maskAPIKeyValue masks an API key for display.
// Keys longer than 12 chars show prefix + last 4 chars: "sk-****abcd".
// Keys 9-12 chars show prefix + last 2 chars: "sk-****cd".
// Shorter keys are fully masked as "****".
// Empty keys return empty string.
// Ensure at least 40% of the key will not be displayed.
func maskAPIKeyValue(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	if len(key) <= 12 {
		return key[:3] + "****" + key[len(key)-2:]
	}
	return key[:3] + "****" + key[len(key)-4:]
}

func openCatalogDatabase(ctx context.Context) (*sql.DB, error) {
	path, err := resolvedCatalogDatabasePath()
	if err != nil {
		return nil, fmt.Errorf("resolve model catalog database path: %w", err)
	}
	root := filepath.Dir(path)
	return sqlitestore.Open(ctx, path, catalogStoreOptions(root))
}

func catalogStoreOptions(root string) sqlitestore.Options {
	return sqlitestore.Options{
		Component: catalogDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				modelCatalogsSchema,
				catalogModelsSchema,
				modelCatalogsProviderIndexSchema,
				catalogModelsIdentityIndexSchema,
			},
		}},
		Validate: validateCatalogSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  root,
			ArchiveRoot: filepath.Join(root, "legacy-json", legacyCatalogArchiveLabel),
			Sources: func() ([]sqlitestore.LegacySource, error) {
				return []sqlitestore.LegacySource{{
					ID:       legacyCatalogSourceID,
					Relative: legacyCatalogFilename,
					MaxBytes: legacyCatalogMaxBytes,
				}}, nil
			},
			Import:        importLegacyCatalogStore,
			MaxBytes:      legacyCatalogMaxBytes,
			MaxSources:    1,
			MaxTotalBytes: legacyCatalogMaxBytes,
		},
	}
}

func validateCatalogSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct {
		objectType string
		name       string
		schema     string
	}{
		{objectType: "table", name: "model_catalogs", schema: modelCatalogsSchema},
		{objectType: "table", name: "model_catalog_models", schema: catalogModelsSchema},
		{objectType: "index", name: "model_catalogs_provider_idx", schema: modelCatalogsProviderIndexSchema},
		{objectType: "index", name: "model_catalog_models_identity_idx", schema: catalogModelsIdentityIndexSchema},
	} {
		if err := sqlitestore.ValidateSchemaObject(
			ctx,
			conn,
			object.objectType,
			object.name,
			object.schema,
		); err != nil {
			return err
		}
	}
	for _, table := range []string{"model_catalogs", "model_catalog_models"} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE tbl_name IN ('model_catalogs', 'model_catalog_models')
          AND type IN ('index', 'trigger')
          AND name NOT LIKE 'sqlite_autoindex_%'
          AND name NOT IN ('model_catalogs_provider_idx', 'model_catalog_models_identity_idx')`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("model catalog schema has unexpected indexes or triggers")
	}
	var catalogCount, modelCount int
	var extraBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM model_catalogs),
		(SELECT COUNT(*) FROM model_catalog_models),
		(SELECT COALESCE(SUM(length(extra_json)), 0) FROM model_catalog_models)`).Scan(
		&catalogCount,
		&modelCount,
		&extraBytes,
	); err != nil {
		return err
	}
	if catalogCount > maximumCatalogs || modelCount > maximumCatalogModels ||
		extraBytes > maximumCatalogExtraBytes {
		return errors.New("model catalog database exceeds its record limit")
	}
	var noncontiguous, invalidNullState int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
        SELECT position,
               row_number() OVER (PARTITION BY catalog_id ORDER BY position) - 1 AS expected
          FROM model_catalog_models
    ) WHERE position <> expected`).Scan(&noncontiguous); err != nil {
		return err
	}
	if noncontiguous != 0 {
		return errors.New("model catalog model positions are not contiguous")
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_catalogs AS c
        WHERE c.models_is_null = 1
          AND EXISTS (SELECT 1 FROM model_catalog_models AS m WHERE m.catalog_id = c.catalog_id)`).Scan(
		&invalidNullState,
	); err != nil {
		return err
	}
	if invalidNullState != 0 {
		return errors.New("model catalog null-model state is inconsistent")
	}
	rows, err := conn.QueryContext(ctx, `SELECT extra_json
        FROM model_catalog_models WHERE extra_json IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		_, canonical, err := canonicalCatalogExtra(raw)
		if err != nil || !bytes.Equal(raw, canonical) {
			return errors.New("model catalog metadata is not canonical JSON")
		}
	}
	return rows.Err()
}

func loadCatalogs() (*CatalogStore, error) {
	return loadCatalogsContext(context.Background())
}

func loadCatalogsContext(ctx context.Context) (*CatalogStore, error) {
	db, err := openCatalogDatabase(ctx)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT
		c.catalog_id, c.provider, c.api_base, c.api_key_mask,
		c.fetched_at_unix_seconds, c.fetched_at_nanosecond, c.models_is_null,
        m.position, m.model_id, m.owned_by, m.extra_json
      FROM model_catalogs AS c
      LEFT JOIN model_catalog_models AS m ON m.catalog_id = c.catalog_id
     ORDER BY c.catalog_id, m.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	store := &CatalogStore{Entries: make(map[string]*CatalogEntry)}
	for rows.Next() {
		var (
			catalogID, provider, apiBase, apiKeyMask string
			fetchedAtSeconds, fetchedAtNanosecond    int64
			modelsIsNull                             int
			position                                 sql.NullInt64
			modelID, ownedBy                         sql.NullString
			extra                                    []byte
		)
		if err := rows.Scan(
			&catalogID,
			&provider,
			&apiBase,
			&apiKeyMask,
			&fetchedAtSeconds,
			&fetchedAtNanosecond,
			&modelsIsNull,
			&position,
			&modelID,
			&ownedBy,
			&extra,
		); err != nil {
			return nil, err
		}
		entry := store.Entries[catalogID]
		if entry == nil {
			models := make([]CatalogModel, 0)
			if modelsIsNull == 1 {
				models = nil
			}
			entry = &CatalogEntry{
				ID:         catalogID,
				Provider:   provider,
				APIBase:    apiBase,
				APIKeyMask: apiKeyMask,
				Models:     models,
				FetchedAt: time.Unix(
					fetchedAtSeconds,
					fetchedAtNanosecond,
				).UTC().Format(time.RFC3339Nano),
			}
			store.Entries[catalogID] = entry
		}
		if !position.Valid {
			continue
		}
		if position.Int64 != int64(len(entry.Models)) || !modelID.Valid || !ownedBy.Valid {
			return nil, errors.New("model catalog model ordering is invalid")
		}
		metadata, canonical, err := canonicalCatalogExtra(extra)
		if err != nil || (extra != nil && !bytes.Equal(extra, canonical)) {
			return nil, errors.New("model catalog metadata is invalid")
		}
		entry.Models = append(entry.Models, CatalogModel{
			ID:      modelID.String,
			OwnedBy: ownedBy.String,
			Extra:   metadata,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return store, nil
}

func saveCatalogs(store *CatalogStore) error {
	if store == nil || len(store.Entries) > maximumCatalogs {
		return errors.New("model catalog store is invalid")
	}
	keys := make([]string, 0, len(store.Entries))
	for key := range store.Entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	type catalogWrite struct {
		entry  *CatalogEntry
		extras [][]byte
	}
	writes := make([]catalogWrite, 0, len(keys))
	totalModels := 0
	var totalExtraBytes int64
	for _, key := range keys {
		entry := store.Entries[key]
		if entry == nil || key != entry.ID {
			return errors.New("model catalog identity is invalid")
		}
		if len(entry.Models) > maximumCatalogModels-totalModels {
			return errors.New("model catalog store exceeds its model limit")
		}
		totalModels += len(entry.Models)
		normalized, extras, err := normalizeCatalogEntry(entry)
		if err != nil {
			return err
		}
		extraBytes := catalogExtraPayloadBytes(extras)
		if extraBytes > maximumCatalogExtraBytes-totalExtraBytes {
			return errors.New("model catalog store exceeds its metadata limit")
		}
		totalExtraBytes += extraBytes
		writes = append(writes, catalogWrite{entry: normalized, extras: extras})
	}
	db, err := openCatalogDatabase(context.Background())
	if err != nil {
		return err
	}
	defer db.Close()
	return sqlitestore.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(context.Background(), `DELETE FROM model_catalogs`); err != nil {
			return err
		}
		for _, write := range writes {
			if err := insertCatalogEntry(
				context.Background(),
				conn,
				write.entry,
				write.extras,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func catalogExtraPayloadBytes(extras [][]byte) int64 {
	var total int64
	for _, extra := range extras {
		total += int64(len(extra))
	}
	return total
}

func boolIntCatalog(value bool) int {
	if value {
		return 1
	}
	return 0
}

// SaveCatalog persists a fetched model list for a given provider+key combination.
// If a catalog with the same key already exists, it is updated atomically.
func SaveCatalog(provider, apiBase, apiKey string, models []CatalogModel) error {
	provider = providers.NormalizeProvider(provider)
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	entry := &CatalogEntry{
		ID:         generateCatalogKey(provider, apiBase, apiKey),
		Provider:   provider,
		APIBase:    apiBase,
		APIKeyMask: maskAPIKeyValue(apiKey),
		Models:     models,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	normalized, extras, err := normalizeCatalogEntry(entry)
	if err != nil {
		return err
	}
	incomingExtraBytes := catalogExtraPayloadBytes(extras)
	if incomingExtraBytes > maximumCatalogExtraBytes {
		return errors.New("model catalog entry exceeds its metadata limit")
	}
	db, err := openCatalogDatabase(context.Background())
	if err != nil {
		return err
	}
	defer db.Close()
	return sqlitestore.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		var catalogExists, catalogCount, modelCount, replacedModelCount int
		var extraBytes, replacedExtraBytes int64
		if err := conn.QueryRowContext(context.Background(), `SELECT
            EXISTS(SELECT 1 FROM model_catalogs WHERE catalog_id = ?),
            (SELECT COUNT(*) FROM model_catalogs),
            (SELECT COUNT(*) FROM model_catalog_models),
			(SELECT COUNT(*) FROM model_catalog_models WHERE catalog_id = ?),
			(SELECT COALESCE(SUM(length(extra_json)), 0) FROM model_catalog_models),
			(SELECT COALESCE(SUM(length(extra_json)), 0)
			   FROM model_catalog_models WHERE catalog_id = ?)`,
			normalized.ID,
			normalized.ID,
			normalized.ID,
		).Scan(
			&catalogExists,
			&catalogCount,
			&modelCount,
			&replacedModelCount,
			&extraBytes,
			&replacedExtraBytes,
		); err != nil {
			return err
		}
		if (catalogExists == 0 && catalogCount >= maximumCatalogs) ||
			len(normalized.Models) > maximumCatalogModels-(modelCount-replacedModelCount) ||
			incomingExtraBytes > maximumCatalogExtraBytes-(extraBytes-replacedExtraBytes) {
			return errors.New("model catalog database exceeds its record limit")
		}
		fetchedAtSeconds, fetchedAtNanosecond, err := parseCatalogTimestamp(normalized.FetchedAt)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO model_catalogs (
			catalog_id, provider, api_base, api_key_mask, fetched_at_unix_seconds,
			fetched_at_nanosecond, models_is_null, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1)
        ON CONFLICT(catalog_id) DO UPDATE SET
            provider = excluded.provider,
            api_base = excluded.api_base,
            api_key_mask = excluded.api_key_mask,
			fetched_at_unix_seconds = excluded.fetched_at_unix_seconds,
			fetched_at_nanosecond = excluded.fetched_at_nanosecond,
			models_is_null = excluded.models_is_null,
            version = model_catalogs.version + 1`,
			normalized.ID,
			normalized.Provider,
			normalized.APIBase,
			normalized.APIKeyMask,
			fetchedAtSeconds,
			fetchedAtNanosecond,
			boolIntCatalog(normalized.Models == nil),
		); err != nil {
			return err
		}
		if _, err := conn.ExecContext(
			context.Background(),
			`DELETE FROM model_catalog_models WHERE catalog_id = ?`,
			normalized.ID,
		); err != nil {
			return err
		}
		return insertCatalogModels(context.Background(), conn, normalized.ID, normalized.Models, extras)
	})
}

func normalizeCatalogEntry(entry *CatalogEntry) (*CatalogEntry, [][]byte, error) {
	if entry == nil || len(entry.Models) > maximumCatalogModels {
		return nil, nil, errors.New("model catalog entry is invalid")
	}
	var models []CatalogModel
	if entry.Models != nil {
		models = make([]CatalogModel, len(entry.Models))
	}
	normalized := &CatalogEntry{
		ID:         entry.ID,
		Provider:   providers.NormalizeProvider(entry.Provider),
		APIBase:    strings.TrimRight(strings.TrimSpace(entry.APIBase), "/"),
		APIKeyMask: entry.APIKeyMask,
		Models:     models,
		FetchedAt:  entry.FetchedAt,
	}
	if !validCatalogText(normalized.ID, 1, maximumCatalogIDBytes, true) ||
		!validCatalogText(normalized.Provider, 1, maximumProviderBytes, true) ||
		!validCatalogText(normalized.APIBase, 0, maximumAPIBaseBytes, true) ||
		!validCatalogText(normalized.APIKeyMask, 0, maximumAPIKeyMaskBytes, false) {
		return nil, nil, errors.New("model catalog fields are invalid")
	}
	if _, _, err := parseCatalogTimestamp(normalized.FetchedAt); err != nil {
		return nil, nil, err
	}
	extras := make([][]byte, len(entry.Models))
	for position, model := range entry.Models {
		if !validCatalogText(model.ID, 1, maximumModelIDBytes, true) ||
			!validCatalogText(model.OwnedBy, 0, maximumModelOwnerBytes, false) {
			return nil, nil, errors.New("model catalog model fields are invalid")
		}
		metadata, canonical, err := canonicalCatalogExtraValue(model.Extra)
		if err != nil {
			return nil, nil, err
		}
		normalized.Models[position] = CatalogModel{
			ID:      model.ID,
			OwnedBy: model.OwnedBy,
			Extra:   metadata,
		}
		extras[position] = canonical
	}
	return normalized, extras, nil
}

func validCatalogText(value string, minimum, maximum int, trimmed bool) bool {
	if !utf8.ValidString(value) || len(value) < minimum || len(value) > maximum ||
		strings.ContainsRune(value, '\x00') || (trimmed && value != strings.TrimSpace(value)) {
		return false
	}
	for _, character := range value {
		if character < ' ' || character == '\u007f' {
			return false
		}
	}
	return true
}

func parseCatalogTimestamp(value string) (int64, int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, 0, errors.New("model catalog timestamp is invalid")
	}
	if parsed.Year() < 0 || parsed.Year() > 9999 {
		return 0, 0, errors.New("model catalog timestamp is invalid")
	}
	return parsed.Unix(), int64(parsed.Nanosecond()), nil
}

func canonicalCatalogExtraValue(value map[string]any) (map[string]any, []byte, error) {
	if value == nil {
		return nil, nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, nil, errors.New("model catalog metadata is invalid")
	}
	return canonicalCatalogExtra(raw)
}

func canonicalCatalogExtra(raw []byte) (map[string]any, []byte, error) {
	if raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil, nil
	}
	if len(raw) > maximumModelExtraBytes {
		return nil, nil, errors.New("model catalog metadata exceeds its size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, nil, errors.New("model catalog metadata is invalid")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, nil, errors.New("model catalog metadata is invalid")
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maximumModelExtraBytes {
		return nil, nil, errors.New("model catalog metadata is invalid")
	}
	return value, canonical, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON")
	}
	return err
}

func insertCatalogEntry(
	ctx context.Context,
	conn *sql.Conn,
	entry *CatalogEntry,
	extras [][]byte,
) error {
	fetchedAtSeconds, fetchedAtNanosecond, err := parseCatalogTimestamp(entry.FetchedAt)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO model_catalogs (
		catalog_id, provider, api_base, api_key_mask, fetched_at_unix_seconds,
		fetched_at_nanosecond, models_is_null, version
	) VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
		entry.ID,
		entry.Provider,
		entry.APIBase,
		entry.APIKeyMask,
		fetchedAtSeconds,
		fetchedAtNanosecond,
		boolIntCatalog(entry.Models == nil),
	); err != nil {
		return err
	}
	return insertCatalogModels(ctx, conn, entry.ID, entry.Models, extras)
}

func insertCatalogModels(
	ctx context.Context,
	conn *sql.Conn,
	catalogID string,
	models []CatalogModel,
	extras [][]byte,
) error {
	if len(models) != len(extras) {
		return errors.New("model catalog metadata count is invalid")
	}
	for position, model := range models {
		if _, err := conn.ExecContext(ctx, `INSERT INTO model_catalog_models (
            catalog_id, position, model_id, owned_by, extra_json
        ) VALUES (?, ?, ?, ?, ?)`,
			catalogID,
			position,
			model.ID,
			model.OwnedBy,
			extras[position],
		); err != nil {
			return err
		}
	}
	return nil
}

type legacyCatalogModel struct {
	ID      string          `json:"id"`
	OwnedBy string          `json:"owned_by,omitempty"`
	Extra   json.RawMessage `json:"extra,omitempty"`
}

type legacyCatalogEntry struct {
	ID         string               `json:"id"`
	Provider   string               `json:"provider"`
	APIBase    string               `json:"api_base"`
	APIKeyMask string               `json:"api_key_mask"`
	Models     []legacyCatalogModel `json:"models"`
	FetchedAt  string               `json:"fetched_at"`
}

type legacyCatalogRecord struct {
	key string
	raw json.RawMessage
}

func decodeLegacyCatalogRecords(data []byte) ([]legacyCatalogRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, tokenErr := decoder.Token()
	if tokenErr != nil {
		return nil, tokenErr
	}
	if token == nil {
		if err := requireJSONEOF(decoder); err != nil {
			return nil, err
		}
		return nil, nil
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("legacy model catalog envelope is not an object")
	}
	var records []legacyCatalogRecord
	entriesSeen := false
	for decoder.More() {
		nameToken, nameErr := decoder.Token()
		if nameErr != nil {
			return nil, nameErr
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("legacy model catalog field name is invalid")
		}
		if name != "entries" {
			var ignored json.RawMessage
			if err := decoder.Decode(&ignored); err != nil {
				return nil, err
			}
			continue
		}
		if entriesSeen {
			return nil, errors.New("legacy model catalog entries field is duplicated")
		}
		entriesSeen = true
		entriesToken, entriesErr := decoder.Token()
		if entriesErr != nil {
			return nil, entriesErr
		}
		if entriesToken == nil {
			continue
		}
		entriesDelimiter, ok := entriesToken.(json.Delim)
		if !ok || entriesDelimiter != '{' {
			return nil, errors.New("legacy model catalog entries are not an object")
		}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return nil, keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("legacy model catalog identity is invalid")
			}
			var raw json.RawMessage
			if err := decoder.Decode(&raw); err != nil {
				return nil, err
			}
			records = append(records, legacyCatalogRecord{key: key, raw: raw})
			if len(records) > maximumCatalogs {
				return nil, errLegacyCatalogLimit
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errors.New("legacy model catalog entries are malformed")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return nil, errors.New("legacy model catalog envelope is malformed")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	sort.SliceStable(records, func(left, right int) bool {
		return records[left].key < records[right].key
	})
	return records, nil
}

func importLegacyCatalogStore(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	records, err := decodeLegacyCatalogRecords(input.Data)
	if err != nil {
		if errors.Is(err, errLegacyCatalogLimit) {
			return sqlitestore.ImportResult{}, err
		}
		return sqlitestore.ImportResult{
			Skipped: 1,
			Issues: []sqlitestore.ImportIssue{{
				Code:         "malformed-json",
				RecordDigest: input.Digest,
			}},
		}, nil
	}

	result := sqlitestore.ImportResult{}
	addIssue := func(code string, digest [sha256.Size]byte) {
		result.Skipped++
		if len(result.Issues) < maximumCatalogAuditItems {
			result.Issues = append(result.Issues, sqlitestore.ImportIssue{
				Code:         code,
				RecordDigest: digest,
			})
		}
	}
	var storedExtraBytes int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(extra_json)), 0)
		FROM model_catalog_models`).Scan(&storedExtraBytes); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	totalModels := 0
	legacyWinners := make(map[string]struct{}, len(records))
	for _, record := range records {
		key := record.key
		raw := record.raw
		digest := sha256.Sum256(raw)
		var legacy legacyCatalogEntry
		entryDecoder := json.NewDecoder(bytes.NewReader(raw))
		if err := entryDecoder.Decode(&legacy); err != nil || requireJSONEOF(entryDecoder) != nil ||
			bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			addIssue("invalid-catalog", digest)
			continue
		}
		if len(legacy.Models) > maximumCatalogModels-totalModels {
			return sqlitestore.ImportResult{}, errors.New("legacy model catalog model count exceeds its limit")
		}
		totalModels += len(legacy.Models)
		var models []CatalogModel
		if legacy.Models != nil {
			models = make([]CatalogModel, len(legacy.Models))
		}
		entry := &CatalogEntry{
			ID:         legacy.ID,
			Provider:   legacy.Provider,
			APIBase:    legacy.APIBase,
			APIKeyMask: legacy.APIKeyMask,
			Models:     models,
			FetchedAt:  legacy.FetchedAt,
		}
		extras := make([][]byte, len(legacy.Models))
		valid := key == legacy.ID
		for position, model := range legacy.Models {
			metadata, canonical, err := canonicalCatalogExtra(model.Extra)
			if err != nil {
				valid = false
				break
			}
			entry.Models[position] = CatalogModel{
				ID:      model.ID,
				OwnedBy: model.OwnedBy,
				Extra:   metadata,
			}
			extras[position] = canonical
		}
		if !valid {
			addIssue("invalid-catalog", digest)
			continue
		}
		normalized, normalizedExtras, err := normalizeCatalogEntry(entry)
		if err != nil {
			addIssue("invalid-catalog", digest)
			continue
		}
		if _, duplicate := legacyWinners[normalized.ID]; duplicate {
			addIssue("identity-conflict", digest)
			continue
		}
		// The raw metadata path above preserves JSON number tokens; use its
		// canonical blobs instead of remarshal-derived equivalents.
		if len(extras) == len(normalizedExtras) {
			normalizedExtras = extras
		}
		incomingExtraBytes := catalogExtraPayloadBytes(normalizedExtras)
		fetchedAtSeconds, fetchedAtNanosecond, err := parseCatalogTimestamp(normalized.FetchedAt)
		if err != nil {
			addIssue("invalid-catalog", digest)
			continue
		}
		execution, err := conn.ExecContext(ctx, `INSERT INTO model_catalogs (
			catalog_id, provider, api_base, api_key_mask, fetched_at_unix_seconds,
			fetched_at_nanosecond, models_is_null, version
		) VALUES (?, ?, ?, ?, ?, ?, ?, 1) ON CONFLICT(catalog_id) DO NOTHING`,
			normalized.ID,
			normalized.Provider,
			normalized.APIBase,
			normalized.APIKeyMask,
			fetchedAtSeconds,
			fetchedAtNanosecond,
			boolIntCatalog(normalized.Models == nil),
		)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		inserted, err := execution.RowsAffected()
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if inserted != 1 {
			addIssue("sqlite-authoritative", digest)
			continue
		}
		if incomingExtraBytes > maximumCatalogExtraBytes-storedExtraBytes {
			return sqlitestore.ImportResult{}, errors.New(
				"legacy model catalog metadata exceeds its aggregate limit",
			)
		}
		if err := insertCatalogModels(ctx, conn, normalized.ID, normalized.Models, normalizedExtras); err != nil {
			return sqlitestore.ImportResult{}, err
		}
		storedExtraBytes += incomingExtraBytes
		legacyWinners[normalized.ID] = struct{}{}
		result.Imported++
	}
	return result, nil
}

// handleListCatalogs returns all saved model catalogs.
//
//	GET /api/accounts/models/catalog
func (h *Handler) handleListCatalogs(w http.ResponseWriter, r *http.Request) {
	store, err := loadCatalogsContext(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load catalogs: %v", err), http.StatusInternalServerError)
		return
	}

	keys := make([]string, 0, len(store.Entries))
	for key := range store.Entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]*CatalogEntry, 0, len(store.Entries))
	for _, key := range keys {
		entries = append(entries, store.Entries[key])
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"entries": entries,
		"total":   len(entries),
	})
}

// handleDeleteCatalog deletes a saved model catalog by ID.
//
//	DELETE /api/accounts/models/catalog/{id}
func (h *Handler) handleDeleteCatalog(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	db, openErr := openCatalogDatabase(r.Context())
	if openErr != nil {
		http.Error(w, fmt.Sprintf("Failed to load catalogs: %v", openErr), http.StatusInternalServerError)
		return
	}
	defer db.Close()
	var deleted bool
	transactionErr := sqlitestore.Immediate(r.Context(), db, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(r.Context(), `DELETE FROM model_catalogs WHERE catalog_id = ?`, id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		deleted = count == 1
		return nil
	})
	if transactionErr != nil {
		http.Error(w, fmt.Sprintf("Failed to save catalogs: %v", transactionErr), http.StatusInternalServerError)
		return
	}
	if !deleted {
		http.Error(w, "catalog not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
