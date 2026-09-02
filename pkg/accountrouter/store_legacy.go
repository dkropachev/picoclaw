package accountrouter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	credentialAuthInvalidationVersion = 1
	maximumLegacyAccountRouterSources = maximumAccountRouterInvalidations + 1
)

type legacyAccountRouterEntry struct {
	name string
	raw  json.RawMessage
}

func (s *Store) legacyOptions() *sqlitestore.LegacyOptions {
	return &sqlitestore.LegacyOptions{
		SourceRoot:    s.sourceRoot,
		ArchiveRoot:   s.archiveRoot,
		Sources:       s.legacySources,
		Import:        importLegacyAccountRouterSource,
		MaxBytes:      accountRouterLegacyMaxBytes,
		MaxSources:    maximumLegacyAccountRouterSources,
		MaxTotalBytes: accountRouterLegacyMaxBytes * 2,
	}
}

func (s *Store) hasLegacyState() bool {
	if s == nil {
		return false
	}
	if _, err := os.Lstat(filepath.Join(
		s.sourceRoot,
		filepath.FromSlash(s.sourceRelative),
	)); err == nil || !os.IsNotExist(err) {
		return true
	}
	if _, err := os.Lstat(s.archiveRoot); err == nil || !os.IsNotExist(err) {
		return true
	}
	entries, err := os.ReadDir(s.sourceRoot)
	if err != nil {
		return !os.IsNotExist(err)
	}
	prefix := filepath.Base(s.sourceRelative) + ".auth-invalidation."
	for _, entry := range entries {
		if validLegacyAccountRouterSidecarName(entry.Name(), prefix) {
			return true
		}
	}
	return false
}

func (s *Store) legacySources() ([]sqlitestore.LegacySource, error) {
	if s == nil || strings.TrimSpace(s.sourceRelative) == "" {
		return nil, errors.New("account-router legacy source identity is unavailable")
	}
	sources := make([]sqlitestore.LegacySource, 0, 1)
	primaryPath := filepath.Join(s.sourceRoot, filepath.FromSlash(s.sourceRelative))
	if _, err := os.Lstat(primaryPath); err == nil {
		sources = append(sources, sqlitestore.LegacySource{
			ID:       accountRouterLegacySourceID,
			Relative: filepath.ToSlash(s.sourceRelative),
			MaxBytes: accountRouterLegacyMaxBytes,
		})
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	entries, err := os.ReadDir(s.sourceRoot)
	if os.IsNotExist(err) {
		return sources, nil
	}
	if err != nil {
		return nil, err
	}
	prefix := filepath.Base(s.sourceRelative) + ".auth-invalidation."
	for _, entry := range entries {
		name := entry.Name()
		if !validLegacyAccountRouterSidecarName(name, prefix) {
			continue
		}
		if len(sources) >= maximumLegacyAccountRouterSources {
			return nil, errors.New("account-router legacy source count exceeds its limit")
		}
		relative := filepath.ToSlash(name)
		digest := sha256.Sum256([]byte(relative))
		sources = append(sources, sqlitestore.LegacySource{
			ID:       accountRouterLegacySidecarPrefix + hex.EncodeToString(digest[:8]),
			Relative: relative,
			MaxBytes: 1 << 20,
			Order:    1,
		})
	}
	sort.Slice(sources, func(left, right int) bool {
		if sources[left].Order != sources[right].Order {
			return sources[left].Order < sources[right].Order
		}
		return sources[left].Relative < sources[right].Relative
	})
	return sources, nil
}

func validLegacyAccountRouterSidecarName(name, prefix string) bool {
	suffix, ok := strings.CutPrefix(name, prefix)
	if !ok || len(suffix) != 32 || suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

func importLegacyAccountRouterSource(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	if input.ID == accountRouterLegacySourceID {
		return importLegacyAccountRouterState(ctx, conn, input)
	}
	if strings.HasPrefix(input.ID, accountRouterLegacySidecarPrefix) {
		return importLegacyAccountRouterInvalidation(ctx, conn, input)
	}
	return sqlitestore.ImportResult{}, errors.New("account-router legacy source identity is invalid")
}

func importLegacyAccountRouterState(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	version, entries, valid := decodeLegacyAccountRouterEntriesForImport(input.Data)
	if !valid {
		return skippedAccountRouterImport("malformed-json", input.Digest), nil
	}
	if version != 0 && version != stateVersion {
		return skippedAccountRouterImport("unsupported-version", input.Digest), nil
	}
	if len(entries) > maximumAccountRouterRouters {
		return sqlitestore.ImportResult{}, errors.New("legacy account-router count exceeds its limit")
	}
	current, err := loadAccountRouterState(ctx, conn)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	sort.SliceStable(entries, func(left, right int) bool { return entries[left].name < entries[right].name })
	seen := make(map[string]struct{}, len(entries))
	result := sqlitestore.ImportResult{}
	for _, entry := range entries {
		digest := sha256.Sum256(entry.raw)
		if _, duplicate := seen[entry.name]; duplicate {
			appendAccountRouterImportIssue(&result, "identity-conflict", digest)
			continue
		}
		var router RouterState
		if err := json.Unmarshal(entry.raw, &router); err != nil {
			appendAccountRouterImportIssue(&result, "invalid-router", digest)
			continue
		}
		seen[entry.name] = struct{}{}
		normalizeLegacyRouterState(&router)
		candidate := State{Version: stateVersion, Routers: map[string]*RouterState{entry.name: &router}}
		if err := validateAccountRouterState(&candidate); err != nil {
			appendAccountRouterImportIssue(&result, "invalid-router", digest)
			continue
		}
		if _, exists := current.Routers[entry.name]; exists {
			appendAccountRouterImportIssue(&result, "sqlite-authoritative", digest)
			continue
		}
		current.Routers[entry.name] = &router
		result.Imported++
	}
	if err := writeAccountRouterState(ctx, conn, &current); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	return result, nil
}

func decodeLegacyAccountRouterEntriesForImport(data []byte) (int, []legacyAccountRouterEntry, bool) {
	version, entries, err := decodeLegacyAccountRouterEntries(data)
	return version, entries, err == nil
}

func decodeLegacyAccountRouterEntries(data []byte) (int, []legacyAccountRouterEntry, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return 0, nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return 0, nil, errors.New("legacy account-router state is not an object")
	}
	version := 0
	versionSeen := false
	var entries []legacyAccountRouterEntry
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return 0, nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return 0, nil, errors.New("legacy account-router field is invalid")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return 0, nil, err
		}
		switch name {
		case "version":
			if !versionSeen {
				if err := json.Unmarshal(raw, &version); err != nil {
					return 0, nil, err
				}
				versionSeen = true
			}
		case "routers":
			decoded, err := decodeLegacyRouterObject(raw)
			if err != nil {
				return 0, nil, err
			}
			if len(decoded) > maximumAccountRouterRouters-len(entries) {
				return 0, nil, errors.New("legacy account-router count exceeds its limit")
			}
			entries = append(entries, decoded...)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return 0, nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return 0, nil, errors.New("legacy account-router state has trailing JSON")
		}
		return 0, nil, err
	}
	return version, entries, nil
}

func decodeLegacyRouterObject(raw json.RawMessage) ([]legacyAccountRouterEntry, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("legacy account-router routers field is not an object")
	}
	entries := make([]legacyAccountRouterEntry, 0)
	for decoder.More() {
		if len(entries) >= maximumAccountRouterRouters {
			return nil, errors.New("legacy account-router count exceeds its limit")
		}
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("legacy account-router identity is invalid")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		entries = append(entries, legacyAccountRouterEntry{name: name, raw: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("legacy account-router routers field has trailing JSON")
	}
	return entries, nil
}

func normalizeLegacyRouterState(router *RouterState) {
	if router.Accounts == nil {
		router.Accounts = make(map[string]*AccountState)
	}
	if router.Sessions == nil {
		router.Sessions = make(map[string]*SessionState)
	}
	if router.Blocks == nil {
		router.Blocks = make(map[string]*BlockRunState)
	}
	for _, account := range router.Accounts {
		if account != nil && account.State == "" {
			account.State = "operational"
		}
	}
	for _, session := range router.Sessions {
		if session != nil && session.Blocks == nil {
			session.Blocks = make(map[string]BlockAffinity)
		}
	}
}

func importLegacyAccountRouterInvalidation(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	marker, valid := decodeLegacyAccountRouterInvalidationForImport(input.Data)
	if !valid {
		return skippedAccountRouterImport("invalid-invalidation", input.Digest), nil
	}
	normalized, validIdentity := normalizedLegacyCredentialID(marker.CredentialID)
	if !validIdentity || !validLegacyInvalidationGeneration(marker.Generation) {
		return skippedAccountRouterImport("invalid-invalidation", input.Digest), nil
	}
	var count int
	if countErr := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_router_auth_invalidations`).Scan(
		&count,
	); countErr != nil {
		return sqlitestore.ImportResult{}, countErr
	}
	var exists int
	if existsErr := conn.QueryRowContext(ctx, `SELECT EXISTS(
        SELECT 1 FROM account_router_auth_invalidations WHERE credential_id = ?
    )`, normalized).Scan(&exists); existsErr != nil {
		return sqlitestore.ImportResult{}, existsErr
	}
	if exists == 0 && count >= maximumAccountRouterInvalidations {
		return sqlitestore.ImportResult{}, errors.New("account-router invalidation count exceeds its limit")
	}
	now := timeFromImport(input)
	seconds, nanos, timeErr := accountRouterRequiredTimeValues(now)
	if timeErr != nil {
		return sqlitestore.ImportResult{}, timeErr
	}
	execution, execErr := conn.ExecContext(ctx, `INSERT INTO account_router_auth_invalidations (
        credential_id, generation, created_at_unix_seconds, created_at_nanosecond, version
    ) VALUES (?, ?, ?, ?, 1) ON CONFLICT(credential_id) DO NOTHING`,
		normalized, marker.Generation, seconds, nanos,
	)
	if execErr != nil {
		return sqlitestore.ImportResult{}, execErr
	}
	inserted, err := execution.RowsAffected()
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if inserted == 1 {
		return sqlitestore.ImportResult{Imported: 1}, nil
	}
	return skippedAccountRouterImport("sqlite-authoritative", input.Digest), nil
}

func validLegacyInvalidationGeneration(value string) bool {
	return validateAccountRouterText(value, 256, true) == nil
}

func normalizedLegacyCredentialID(value string) (string, bool) {
	normalized, err := normalizeCredentialID(value)
	return normalized, err == nil
}

func decodeLegacyAccountRouterInvalidationForImport(data []byte) (credentialAuthInvalidation, bool) {
	return decodeLegacyAccountRouterInvalidation(data)
}

func decodeLegacyAccountRouterInvalidation(data []byte) (credentialAuthInvalidation, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var marker credentialAuthInvalidation
	if err := decoder.Decode(&marker); err != nil {
		return credentialAuthInvalidation{}, false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return credentialAuthInvalidation{}, false
	}
	return marker, marker.Version == credentialAuthInvalidationVersion &&
		strings.TrimSpace(marker.CredentialID) != "" && strings.TrimSpace(marker.Generation) != ""
}

func timeFromImport(input sqlitestore.LegacyInput) time.Time {
	// Import ordering and generation identity do not depend on filesystem mtime;
	// use one deterministic full-range timestamp derived from the source digest.
	seconds := int64(input.Digest[0])
	return time.Unix(seconds, 0).UTC()
}

func skippedAccountRouterImport(code string, digest [sha256.Size]byte) sqlitestore.ImportResult {
	result := sqlitestore.ImportResult{}
	appendAccountRouterImportIssue(&result, code, digest)
	return result
}

func appendAccountRouterImportIssue(
	result *sqlitestore.ImportResult,
	code string,
	digest [sha256.Size]byte,
) {
	result.Skipped++
	if len(result.Issues) < 512 {
		result.Issues = append(result.Issues, sqlitestore.ImportIssue{
			Code:         code,
			RecordDigest: digest,
		})
	}
}
