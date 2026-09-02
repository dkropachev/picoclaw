package dashboardauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

type legacyConfigSnapshot struct {
	data   []byte
	digest [sha256.Size]byte
	mode   os.FileMode
	info   os.FileInfo
}

type legacyConfigFile interface {
	io.Reader
	Stat() (os.FileInfo, error)
	Close() error
}

var (
	legacyConfigLstat       = os.Lstat
	legacyConfigOpen        = func(path string) (legacyConfigFile, error) { return os.Open(path) }
	legacyConfigReadAll     = io.ReadAll
	legacyArchiveMkdir      = os.Mkdir
	legacyArchiveChmod      = os.Chmod
	legacyArchiveSync       = fileutil.SyncDirectory
	legacyArchiveLink       = os.Link
	legacyArchiveRemove     = fileutil.RemoveDurable
	legacyConfigWriteAtomic = fileutil.WriteFileAtomic
)

func storeOptions(launcherPath string) sqlitestore.Options {
	return sqlitestore.Options{
		Component: databaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				sqlCreateLegacyImports,
				sqlCreateLegacyImportsIndex,
			},
			Apply: func(ctx context.Context, conn *sql.Conn) error {
				if err := migrateCredentialsTable(ctx, conn); err != nil {
					return err
				}
				return importLegacyLauncherConfig(ctx, conn, launcherPath)
			},
		}},
		Validate: validateSchema,
	}
}

// migrateCredentialsTable accepts the exact two-column table created by older
// PicoClaw versions, rebuilds it as STRICT, and copies its singleton row.
func migrateCredentialsTable(ctx context.Context, conn *sql.Conn) error {
	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
        WHERE type = 'table' AND name = 'dashboard_credentials'`).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		_, err := conn.ExecContext(ctx, sqlCreateCredentials)
		return err
	}
	legacySchemaErr := sqlitestore.ValidateSchemaObject(
		ctx, conn, "table", "dashboard_credentials", sqlLegacyCreateCredentials,
	)
	currentSchemaErr := sqlitestore.ValidateSchemaObject(
		ctx, conn, "table", "dashboard_credentials", sqlCreateCredentials,
	)
	if legacySchemaErr != nil && currentSchemaErr != nil {
		return errors.New("legacy dashboard credentials DDL is invalid")
	}
	var unexpectedObjects int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
        WHERE tbl_name = 'dashboard_credentials' AND type IN ('index', 'trigger')
          AND name NOT LIKE 'sqlite_autoindex_%'`).Scan(&unexpectedObjects); err != nil {
		return err
	}
	if unexpectedObjects != 0 {
		return errors.New("legacy dashboard credentials has unexpected schema objects")
	}
	if _, err := conn.ExecContext(ctx, `ALTER TABLE dashboard_credentials
        RENAME TO dashboard_credentials_unversioned`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, sqlCreateCredentials); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO dashboard_credentials(id, bcrypt_hash)
        SELECT id, bcrypt_hash FROM dashboard_credentials_unversioned`); err != nil {
		return fmt.Errorf("copy legacy dashboard credentials: %w", err)
	}
	_, dropErr := conn.ExecContext(ctx, `DROP TABLE dashboard_credentials_unversioned`)
	return dropErr
}

func importLegacyLauncherConfig(ctx context.Context, conn *sql.Conn, path string) error {
	snapshot, found, readErr := readLegacyConfig(path)
	if readErr != nil || !found {
		return readErr
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.data, &fields); err != nil {
		return fmt.Errorf("decode legacy launcher config fields: %w", err)
	}
	rawHash, hasHash := fields["dashboard_password_hash"]
	rawToken, hasToken := fields["launcher_token"]
	if !hasHash && !hasToken {
		return nil
	}
	var legacyHash, legacyToken string
	if hasHash && string(rawHash) != "null" {
		if err := json.Unmarshal(rawHash, &legacyHash); err != nil {
			return errors.New("legacy dashboard password hash is not a string")
		}
	}
	if hasToken && string(rawToken) != "null" {
		if err := json.Unmarshal(rawToken, &legacyToken); err != nil {
			return errors.New("legacy launcher token is not a string")
		}
	}

	source := "none"
	imported, skipped := 0, 0
	var issue any
	var existing int
	if err := conn.QueryRowContext(ctx, sqlCountCredentials).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		source = "existing-database"
	} else if hash := strings.TrimSpace(legacyHash); hash != "" {
		if _, err := bcrypt.Cost([]byte(hash)); err != nil {
			skipped = 1
			issue = "invalid-bcrypt-hash"
			if token := strings.TrimSpace(legacyToken); token != "" {
				generated, hashErr := bcrypt.GenerateFromPassword([]byte(token), bcryptCost)
				if hashErr != nil {
					return hashErr
				}
				if _, insertErr := conn.ExecContext(
					ctx,
					`INSERT INTO dashboard_credentials(id, bcrypt_hash) VALUES (1, ?)`,
					string(generated),
				); insertErr != nil {
					return insertErr
				}
				source, imported = "launcher-token", 1
			}
		} else if _, err := conn.ExecContext(ctx,
			`INSERT INTO dashboard_credentials(id, bcrypt_hash) VALUES (1, ?)`, hash); err != nil {
			return err
		} else {
			source, imported = "dashboard-password-hash", 1
		}
	} else if token := strings.TrimSpace(legacyToken); token != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(token), bcryptCost)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO dashboard_credentials(id, bcrypt_hash) VALUES (1, ?)`, string(hash)); err != nil {
			return err
		}
		source, imported = "launcher-token", 1
	}

	_, insertLedgerErr := conn.ExecContext(ctx, `INSERT INTO launcher_auth_legacy_imports (
        source_id, source_relative, source_digest, source_size, source_limit, source_mode,
        credential_source, imported_count, skipped_count, issue_code, archive_status, imported_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
		legacySourceID, launcherconfig.FileName, snapshot.digest[:], len(snapshot.data),
		legacyConfigMaxBytes, int64(snapshot.mode.Perm()), source, imported, skipped, issue,
		time.Now().UTC().Format(time.RFC3339Nano))
	if insertLedgerErr != nil {
		return insertLedgerErr
	}
	if skipped > 0 {
		logger.WarnCF("sqlite-migration", "Skipped invalid legacy launcher auth field", map[string]any{
			"component":     databaseComponent,
			"source_id":     legacySourceID,
			"source_digest": hex.EncodeToString(snapshot.digest[:8]),
			"issue_code":    issue,
			"imported":      imported,
			"skipped":       skipped,
		})
	}
	return nil
}

func validateSchema(ctx context.Context, conn *sql.Conn) error {
	expectedSQL := map[string]struct {
		objectType string
		sql        string
	}{
		"dashboard_credentials": {
			objectType: "table", sql: sqlCreateCredentials,
		},
		"launcher_auth_legacy_imports": {
			objectType: "table", sql: sqlCreateLegacyImports,
		},
		"launcher_auth_legacy_imports_status_idx": {
			objectType: "index", sql: sqlCreateLegacyImportsIndex,
		},
	}
	for name, want := range expectedSQL {
		if err := sqlitestore.ValidateSchemaObject(ctx, conn, want.objectType, name, want.sql); err != nil {
			return err
		}
	}
	for _, table := range []string{"dashboard_credentials", "launcher_auth_legacy_imports"} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name NOT LIKE 'sqlite_%'
		  AND name NOT IN (
		      'dashboard_credentials',
		      'launcher_auth_legacy_imports',
		      'launcher_auth_legacy_imports_status_idx',
		      'storage_imports',
		      'storage_import_issues',
		      'storage_import_horizons',
		      'storage_imports_archive_status_idx'
		  )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("launcher auth schema has unexpected objects")
	}
	return nil
}

type legacyImportRecord struct {
	relative string
	digest   [sha256.Size]byte
	size     int64
	limit    int64
	mode     os.FileMode
	status   string
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func finishLegacyLauncherConfigMigration(ctx context.Context, db *sql.DB, path string) error {
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		return finishLegacyLauncherConfigMigrationConn(ctx, conn, path)
	})
}

func finishLegacyLauncherConfigMigrationConn(ctx context.Context, conn *sql.Conn, path string) error {
	record, found, readErr := readLegacyImportRecord(ctx, conn)
	if readErr != nil || !found {
		return readErr
	}
	if err := validateLegacyImportRecord(record); err != nil {
		return err
	}
	archiveDir, err := prepareArchiveDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	archivePath := filepath.Join(archiveDir, record.relative)
	snapshot, sourceFound, err := readLegacyConfig(path)
	if err != nil {
		return err
	}
	if !sourceFound {
		if record.status != "complete" {
			return errors.New("legacy launcher config is missing before archival")
		}
		return verifyArchiveCopy(archivePath, record.digest, record.size, record.mode)
	}
	if snapshot.digest != record.digest {
		hasAuth, parseErr := hasLegacyAuthFields(snapshot.data)
		if parseErr != nil {
			return fmt.Errorf("decode changed launcher config: %w", parseErr)
		}
		if hasAuth {
			return errors.New("legacy launcher config changed after import")
		}
		if err := verifyArchiveCopy(archivePath, record.digest, record.size, record.mode); err != nil {
			return err
		}
		return markArchiveCompleteConn(ctx, conn, record)
	}
	if err := publishArchiveCopy(path, archiveDir, record.relative, snapshot); err != nil {
		return err
	}
	if err := stripLegacyAuthFields(path, record.digest, record.mode); err != nil {
		return err
	}
	return markArchiveCompleteConn(ctx, conn, record)
}

func readLegacyImportRecord(ctx context.Context, query queryRower) (legacyImportRecord, bool, error) {
	var record legacyImportRecord
	var digest []byte
	var mode int64
	err := query.QueryRowContext(ctx, `SELECT source_relative, source_digest, source_size, source_limit,
        source_mode, archive_status FROM launcher_auth_legacy_imports WHERE source_id = ?`, legacySourceID).
		Scan(&record.relative, &digest, &record.size, &record.limit, &mode, &record.status)
	if errors.Is(err, sql.ErrNoRows) {
		return legacyImportRecord{}, false, nil
	}
	if err != nil {
		return legacyImportRecord{}, false, err
	}
	if len(digest) == sha256.Size {
		copy(record.digest[:], digest)
	} else {
		record.digest = [sha256.Size]byte{}
		record.size = -1
	}
	record.mode = os.FileMode(mode)
	return record, true, nil
}

func validateLegacyImportRecord(record legacyImportRecord) error {
	if record.relative != launcherconfig.FileName || record.limit != legacyConfigMaxBytes ||
		record.size < 0 || record.size > record.limit || record.mode.Perm() != record.mode ||
		record.mode > 0o777 || (record.status != "pending" && record.status != "complete") {
		return errors.New("invalid launcher auth legacy import record")
	}
	return nil
}

func markArchiveComplete(ctx context.Context, db *sql.DB, expected legacyImportRecord) error {
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		return markArchiveCompleteConn(ctx, conn, expected)
	})
}

func markArchiveCompleteConn(
	ctx context.Context,
	conn *sql.Conn,
	expected legacyImportRecord,
) error {
	current, found, err := readLegacyImportRecord(ctx, conn)
	if err != nil {
		return err
	}
	if !found || current.relative != expected.relative || current.digest != expected.digest ||
		current.size != expected.size || current.limit != expected.limit || current.mode != expected.mode {
		return errors.New("launcher auth legacy import record changed")
	}
	if current.status == "complete" {
		return nil
	}
	if current.status != "pending" {
		return errors.New("launcher auth legacy import status is invalid")
	}
	result, err := conn.ExecContext(ctx, `UPDATE launcher_auth_legacy_imports
        SET archive_status = 'complete', archived_at = ?
      WHERE source_id = ? AND archive_status = 'pending'`,
		time.Now().UTC().Format(time.RFC3339Nano), legacySourceID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("launcher auth legacy import record changed")
	}
	return nil
}

func readLegacyConfig(path string) (legacyConfigSnapshot, bool, error) {
	var zero legacyConfigSnapshot
	parentInfo, err := realDirectoryInfo(filepath.Dir(path))
	if err != nil {
		return zero, false, fmt.Errorf("inspect launcher config directory: %w", err)
	}
	info, err := legacyConfigLstat(path)
	if os.IsNotExist(err) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || info.Mode().Perm()&0o022 != 0 {
		return zero, false, errors.New("legacy launcher config has an unsafe type or mode")
	}
	if info.Size() > legacyConfigMaxBytes {
		return zero, false, errors.New("legacy launcher config exceeds the size limit")
	}
	file, err := legacyConfigOpen(path)
	if err != nil {
		return zero, false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return zero, false, err
	}
	if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return zero, false, errors.New("legacy launcher config changed while opening")
	}
	data, err := legacyConfigReadAll(io.LimitReader(file, legacyConfigMaxBytes+1))
	if err != nil {
		return zero, false, err
	}
	if int64(len(data)) > legacyConfigMaxBytes {
		return zero, false, errors.New("legacy launcher config exceeds the size limit")
	}
	current, err := legacyConfigLstat(path)
	if err != nil || !os.SameFile(opened, current) {
		return zero, false, errors.New("legacy launcher config changed while reading")
	}
	if err := directoryUnchanged(filepath.Dir(path), parentInfo); err != nil {
		return zero, false, err
	}
	return legacyConfigSnapshot{
		data: data, digest: sha256.Sum256(data), mode: opened.Mode(), info: opened,
	}, true, nil
}

func prepareArchiveDirectory(configDir string) (string, error) {
	if _, err := realDirectoryInfo(configDir); err != nil {
		return "", fmt.Errorf("inspect launcher config directory: %w", err)
	}
	root, err := ensurePrivateChildDirectory(configDir, "legacy-json")
	if err != nil {
		return "", err
	}
	return ensurePrivateChildDirectory(root, legacyArchiveVersion)
}

func ensurePrivateChildDirectory(parent, name string) (string, error) {
	if name == "" || name != filepath.Base(name) {
		return "", errors.New("archive directory name is invalid")
	}
	parentInfo, parentErr := realDirectoryInfo(parent)
	if parentErr != nil {
		return "", parentErr
	}
	child := filepath.Join(parent, name)
	info, err := legacyConfigLstat(child)
	if os.IsNotExist(err) {
		if err = legacyArchiveMkdir(child, 0o700); err != nil && !os.IsExist(err) {
			return "", err
		}
		if err == nil {
			if syncErr := legacyArchiveSync(parent); syncErr != nil {
				return "", syncErr
			}
		}
		info, err = legacyConfigLstat(child)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("archive directory must be a real directory")
	}
	if err := legacyArchiveChmod(child, 0o700); err != nil {
		return "", err
	}
	if err := legacyArchiveSync(child); err != nil {
		return "", err
	}
	if err := directoryUnchanged(parent, parentInfo); err != nil {
		return "", err
	}
	return child, nil
}

func publishArchiveCopy(sourcePath, dir, name string, source legacyConfigSnapshot) error {
	path := filepath.Join(dir, name)
	found, err := archiveCopyMatches(path, source.digest, int64(len(source.data)), source.mode)
	if err != nil {
		return err
	}
	if found {
		return legacyArchiveSync(dir)
	}
	dirInfo, err := realDirectoryInfo(dir)
	if err != nil {
		return err
	}
	current, currentFound, err := readLegacyConfig(sourcePath)
	if err != nil || !currentFound {
		return errors.Join(err, errors.New("legacy launcher config disappeared before archival"))
	}
	if current.digest != source.digest || !os.SameFile(current.info, source.info) {
		return errors.New("legacy launcher config changed before archival")
	}
	if err := directoryUnchanged(dir, dirInfo); err != nil {
		return err
	}
	created := false
	if err := legacyArchiveLink(sourcePath, path); err != nil {
		if !os.IsExist(err) {
			return err
		}
		if found, verifyErr := archiveCopyMatches(
			path, source.digest, int64(len(source.data)), source.mode,
		); verifyErr != nil || !found {
			if verifyErr != nil {
				return verifyErr
			}
			return errors.New("launcher auth archive destination already exists")
		}
	} else {
		created = true
	}
	if created {
		archiveInfo, statErr := legacyConfigLstat(path)
		sourceInfo, sourceErr := legacyConfigLstat(sourcePath)
		if statErr != nil || sourceErr != nil || !os.SameFile(source.info, archiveInfo) ||
			!os.SameFile(archiveInfo, sourceInfo) {
			cleanupErr := legacyArchiveRemove(path)
			return errors.Join(
				statErr, sourceErr, cleanupErr,
				errors.New("legacy launcher config changed while publishing archive"),
			)
		}
	}
	if err := legacyArchiveSync(dir); err != nil {
		return err
	}
	if err := directoryUnchanged(dir, dirInfo); err != nil {
		return err
	}
	if err := verifyArchiveCopy(path, source.digest, int64(len(source.data)), source.mode); err != nil {
		if created {
			return errors.Join(err, legacyArchiveRemove(path))
		}
		return err
	}
	return nil
}

func verifyArchiveCopy(path string, digest [sha256.Size]byte, size int64, mode os.FileMode) error {
	found, err := archiveCopyMatches(path, digest, size, mode)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("launcher auth archive is missing")
	}
	return nil
}

func archiveCopyMatches(
	path string,
	digest [sha256.Size]byte,
	size int64,
	mode os.FileMode,
) (bool, error) {
	parentInfo, err := realDirectoryInfo(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	info, err := legacyConfigLstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode.Perm() ||
		info.Size() != size || size < 0 || size > legacyConfigMaxBytes {
		return false, errors.New("launcher auth archive has unexpected type, mode, or size")
	}
	file, err := legacyConfigOpen(path)
	if err != nil {
		return false, err
	}
	written, readErr := legacyConfigReadAll(io.LimitReader(file, legacyConfigMaxBytes+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	current, currentErr := legacyConfigLstat(path)
	if err := errors.Join(readErr, statErr, closeErr, currentErr); err != nil {
		return false, err
	}
	if !os.SameFile(info, opened) || !os.SameFile(opened, current) || int64(len(written)) != size ||
		sha256.Sum256(written) != digest {
		return false, errors.New("launcher auth archive digest verification failed")
	}
	if err := directoryUnchanged(filepath.Dir(path), parentInfo); err != nil {
		return false, err
	}
	return true, nil
}

func stripLegacyAuthFields(path string, expected [sha256.Size]byte, mode os.FileMode) error {
	snapshot, found, readErr := readLegacyConfig(path)
	if readErr != nil {
		return readErr
	}
	if !found {
		return errors.New("launcher config disappeared during credential cleanup")
	}
	if snapshot.digest != expected {
		hasAuth, parseErr := hasLegacyAuthFields(snapshot.data)
		if parseErr != nil {
			return parseErr
		}
		if !hasAuth {
			return nil
		}
		return errors.New("legacy launcher config changed before credential cleanup")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.data, &raw); err != nil {
		return err
	}
	if _, hash := raw["dashboard_password_hash"]; !hash {
		if _, token := raw["launcher_token"]; !token {
			return nil
		}
	}
	delete(raw, "dashboard_password_hash")
	delete(raw, "launcher_token")
	// raw came from a successful JSON decode, so its retained RawMessages are
	// necessarily serializable.
	clean, _ := json.MarshalIndent(raw, "", "  ")
	clean = append(clean, '\n')
	current, found, currentErr := readLegacyConfig(path)
	if currentErr != nil || !found {
		return errors.Join(currentErr, errors.New("launcher config disappeared during credential cleanup"))
	}
	if current.digest != expected {
		hasAuth, parseErr := hasLegacyAuthFields(current.data)
		if parseErr != nil {
			return parseErr
		}
		if !hasAuth {
			return nil
		}
		return errors.New("legacy launcher config changed before credential cleanup")
	}
	parentInfo, parentErr := realDirectoryInfo(filepath.Dir(path))
	if parentErr != nil {
		return parentErr
	}
	if err := legacyConfigWriteAtomic(path, clean, mode.Perm()); err != nil {
		return err
	}
	if err := directoryUnchanged(filepath.Dir(path), parentInfo); err != nil {
		return err
	}
	cleaned, found, verifyErr := readLegacyConfig(path)
	cleanedHasAuth := false
	if verifyErr == nil && found {
		cleanedHasAuth, verifyErr = hasLegacyAuthFields(cleaned.data)
	}
	if verifyErr != nil || !found || cleanedHasAuth || cleaned.mode.Perm() != mode.Perm() {
		if verifyErr != nil {
			return verifyErr
		}
		return errors.New("launcher config credential cleanup verification failed")
	}
	return nil
}

func hasLegacyAuthFields(data []byte) (bool, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, err
	}
	_, hash := raw["dashboard_password_hash"]
	_, token := raw["launcher_token"]
	return hash || token, nil
}

func realDirectoryInfo(path string) (os.FileInfo, error) {
	info, err := legacyConfigLstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("directory must be a safe real directory")
	}
	return info, nil
}

func directoryUnchanged(path string, expected os.FileInfo) error {
	current, err := realDirectoryInfo(path)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return errors.New("directory changed during launcher auth migration")
	}
	return nil
}
