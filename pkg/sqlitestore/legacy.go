package sqlitestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	defaultLegacyMaxBytes      = int64(64 << 20)
	defaultLegacyMaxSources    = 100_000
	maximumLegacyMaxSources    = 1_000_000
	defaultLegacyMaxTotalBytes = int64(1 << 30)
	maximumLegacyMaxTotalBytes = int64(1 << 40)
	maxImportIssues            = 512
	maximumLegacyRecordCount   = 1_000_000_000
	maximumLegacyRelativeBytes = 16 << 10
)

// LegacyOptions describes the legacy sources owned by one database.
type LegacyOptions struct {
	SourceRoot  string
	ArchiveRoot string
	Sources     func() ([]LegacySource, error)
	Import      LegacyImporter
	// Finalize resolves relationships among sources imported by this exact
	// transaction. It is invoked at most once, only when at least one source is
	// newly imported, before subsystem schema validation and commit.
	Finalize LegacyFinalizer
	// FinalizeResults is the aggregate variant used when dependency resolution
	// determines whether parsed records were actually imported. It replaces
	// the provisional per-source accounting atomically before commit.
	FinalizeResults LegacyResultFinalizer
	// Seal closes the subsystem's legacy import horizon after deterministic
	// enumeration and import. It runs on every successful open, including when
	// no source exists or no source is newly imported, inside the same
	// BEGIN IMMEDIATE transaction and before schema validation. Implementations
	// must be idempotent.
	Seal          LegacySealer
	MaxBytes      int64
	MaxSources    int
	MaxTotalBytes int64
	Now           func() time.Time
}

// LegacySource is one deterministic, relative source below SourceRoot.
type LegacySource struct {
	ID       string
	Relative string
	MaxBytes int64
	// Order is an optional stable primary import key. Zero preserves the
	// historical Relative/ID ordering for existing callers. Subsystems use
	// nonzero phases only when referential dependencies require it.
	Order int
}

// LegacyInput is the safely read, bounded source passed to an importer.
type LegacyInput struct {
	ID       string
	Relative string
	Data     []byte
	Digest   [sha256.Size]byte
	Limit    int64
	Mode     os.FileMode
	ModTime  time.Time
}

// ImportIssue is a secret-safe description of a skipped legacy record.
type ImportIssue struct {
	Code         string
	RecordDigest [sha256.Size]byte
}

// ImportResult records migration accounting without retaining source payloads.
type ImportResult struct {
	Imported int
	Skipped  int
	Issues   []ImportIssue
}

// LegacyImporter writes valid records to the new schema using conn.
type LegacyImporter func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error)

// LegacyFinalizeInput contains bounded, secret-free accounting for only the
// sources newly imported in the current transaction. SourceIDs follow the
// deterministic source ordering and are detached from importer-owned slices.
type LegacyFinalizeInput struct {
	SourceIDs []string
	Imported  int
	Skipped   int
}

// LegacyFinalizer commits aggregate records and relationships after all new
// legacy sources have been parsed inside the same BEGIN IMMEDIATE transaction.
type LegacyFinalizer func(context.Context, *sql.Conn, LegacyFinalizeInput) error

// LegacyResultFinalizer returns final accounting for every SourceID in its
// input. Missing, extra, or invalid source results fail the migration.
type LegacyResultFinalizer func(
	context.Context,
	*sql.Conn,
	LegacyFinalizeInput,
) (map[string]ImportResult, error)

// LegacySealer durably marks a subsystem's SQLite database authoritative once
// its first complete legacy enumeration has succeeded.
type LegacySealer func(context.Context, *sql.Conn) error

const storageImportsSchema = `CREATE TABLE IF NOT EXISTS storage_imports (
    component       TEXT NOT NULL,
    source_id       TEXT NOT NULL,
    source_relative TEXT NOT NULL,
    source_digest   BLOB NOT NULL CHECK(length(source_digest) = 32),
    source_size     INTEGER NOT NULL CHECK(source_size >= 0),
    source_limit    INTEGER NOT NULL CHECK(source_limit >= source_size),
    source_mode     INTEGER NOT NULL CHECK(source_mode BETWEEN 0 AND 511),
    imported_count  INTEGER NOT NULL CHECK(imported_count >= 0),
    skipped_count   INTEGER NOT NULL CHECK(skipped_count >= 0),
    archive_status  TEXT NOT NULL CHECK(archive_status IN ('pending', 'complete')),
    imported_at     INTEGER NOT NULL,
    archived_at     INTEGER,
    PRIMARY KEY(component, source_id),
    UNIQUE(component, source_relative)
) STRICT;`

const storageImportIssuesSchema = `CREATE TABLE IF NOT EXISTS storage_import_issues (
    component       TEXT NOT NULL,
    source_id       TEXT NOT NULL,
    sequence        INTEGER NOT NULL CHECK(sequence >= 0),
    issue_code      TEXT NOT NULL,
    record_digest   BLOB NOT NULL CHECK(length(record_digest) = 32),
    PRIMARY KEY(component, source_id, sequence),
    FOREIGN KEY(component, source_id)
        REFERENCES storage_imports(component, source_id) ON DELETE CASCADE
) STRICT;`

const storageImportsArchiveIndexSchema = `CREATE INDEX IF NOT EXISTS storage_imports_archive_status_idx
    ON storage_imports(component, archive_status, source_id);`

const importSchema = storageImportsSchema + "\n" + storageImportIssuesSchema + "\n" +
	storageImportsArchiveIndexSchema

func createImportSchema(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, importSchema)
	return err
}

func validateImportSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct {
		typeName string
		name     string
		schema   string
	}{
		{typeName: "table", name: "storage_imports", schema: storageImportsSchema},
		{typeName: "table", name: "storage_import_issues", schema: storageImportIssuesSchema},
		{typeName: "index", name: "storage_imports_archive_status_idx", schema: storageImportsArchiveIndexSchema},
	} {
		if err := ValidateSchemaObject(
			ctx,
			conn,
			object.typeName,
			object.name,
			object.schema,
		); err != nil {
			return err
		}
	}
	for _, table := range []string{"storage_imports", "storage_import_issues"} {
		if err := ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	return nil
}

type legacyImportSummary struct {
	SourcesWithSkips int
	Imported         int64
	Skipped          int64
	NewSourceIDs     []string
}

func logLegacyImportSummary(component string, summary legacyImportSummary) {
	if summary.Skipped == 0 {
		return
	}
	logger.WarnCF("sqlite-migration", "Skipped invalid legacy records", map[string]any{
		"component":          component,
		"sources_with_skips": summary.SourcesWithSkips,
		"imported":           summary.Imported,
		"skipped":            summary.Skipped,
	})
}

func importLegacySources(
	ctx context.Context,
	conn *sql.Conn,
	component string,
	options LegacyOptions,
) (legacyImportSummary, error) {
	if options.Sources == nil || options.Import == nil {
		return legacyImportSummary{}, fmt.Errorf("%s legacy migration is incomplete", component)
	}
	maximumSources, maximumTotalBytes, err := legacyEnumerationBounds(options)
	if err != nil {
		return legacyImportSummary{}, fmt.Errorf("%s legacy migration: %w", component, err)
	}
	sources, err := options.Sources()
	if err != nil {
		return legacyImportSummary{}, fmt.Errorf("enumerate %s legacy sources: %w", component, err)
	}
	if len(sources) > maximumSources {
		return legacyImportSummary{}, fmt.Errorf(
			"enumerate %s legacy sources: source count exceeds its limit",
			component,
		)
	}
	if len(sources) > 0 {
		if err := validateLegacyRoots(options); err != nil {
			return legacyImportSummary{}, fmt.Errorf("%s legacy migration: %w", component, err)
		}
	}
	sources = append([]LegacySource(nil), sources...)
	sort.Slice(sources, func(left, right int) bool {
		if sources[left].Order != sources[right].Order {
			return sources[left].Order < sources[right].Order
		}
		if sources[left].Relative != sources[right].Relative {
			return sources[left].Relative < sources[right].Relative
		}
		return sources[left].ID < sources[right].ID
	})
	seenID := make(map[string]struct{}, len(sources))
	seenRelative := make(map[string]struct{}, len(sources))
	var totalBytes int64
	var summary legacyImportSummary
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return legacyImportSummary{}, err
		}
		if !validIdentifier(source.ID) || !validRelativePath(source.Relative) {
			return legacyImportSummary{}, fmt.Errorf(
				"enumerate %s legacy sources: invalid source identity",
				component,
			)
		}
		if _, duplicate := seenID[source.ID]; duplicate {
			return legacyImportSummary{}, fmt.Errorf(
				"enumerate %s legacy sources: duplicate source identity",
				component,
			)
		}
		relativeKey := legacyRelativePathKey(source.Relative)
		if _, duplicate := seenRelative[relativeKey]; duplicate {
			return legacyImportSummary{}, fmt.Errorf(
				"enumerate %s legacy sources: duplicate source path",
				component,
			)
		}
		seenID[source.ID] = struct{}{}
		seenRelative[relativeKey] = struct{}{}
		if err := validateLegacySourceOutsideArchive(options, source.Relative); err != nil {
			return legacyImportSummary{}, fmt.Errorf(
				"enumerate %s legacy source %s: %w",
				component,
				source.ID,
				err,
			)
		}

		input, found, err := readLegacySource(options.SourceRoot, source, options.MaxBytes)
		if err != nil {
			return legacyImportSummary{}, fmt.Errorf(
				"read %s legacy source %s: %w",
				component,
				source.ID,
				err,
			)
		}
		if !found {
			continue
		}
		inputBytes := int64(len(input.Data))
		if inputBytes > maximumTotalBytes-totalBytes {
			return legacyImportSummary{}, fmt.Errorf(
				"read %s legacy sources: aggregate size exceeds its limit",
				component,
			)
		}
		totalBytes += inputBytes

		var existingDigest []byte
		var existingRelative string
		err = conn.QueryRowContext(
			ctx,
			`SELECT source_digest, source_relative
			   FROM storage_imports
			  WHERE component = ? AND source_id = ?`,
			component,
			source.ID,
		).Scan(&existingDigest, &existingRelative)
		switch {
		case err == nil:
			if !equalDigest(existingDigest, input.Digest[:]) {
				return legacyImportSummary{}, fmt.Errorf(
					"%s legacy source %s changed after import",
					component,
					source.ID,
				)
			}
			if existingRelative != source.Relative {
				return legacyImportSummary{}, fmt.Errorf(
					"%s legacy source %s path changed after import",
					component,
					source.ID,
				)
			}
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return legacyImportSummary{}, fmt.Errorf("read %s import record: %w", component, err)
		}

		result, err := options.Import(ctx, conn, input)
		if err != nil {
			return legacyImportSummary{}, fmt.Errorf(
				"import %s legacy source %s: %w",
				component,
				source.ID,
				err,
			)
		}
		if err := validateImportResult(result); err != nil {
			return legacyImportSummary{}, fmt.Errorf(
				"import %s legacy source %s: %w",
				component,
				source.ID,
				err,
			)
		}
		now := legacyNow(options).UnixNano()
		if _, err := conn.ExecContext(
			ctx,
			`INSERT INTO storage_imports (
                component, source_id, source_relative, source_digest, source_size, source_limit,
				source_mode, imported_count, skipped_count, archive_status, imported_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?)`,
			component,
			source.ID,
			source.Relative,
			input.Digest[:],
			len(input.Data),
			input.Limit,
			int64(input.Mode.Perm()),
			result.Imported,
			result.Skipped,
			now,
		); err != nil {
			return legacyImportSummary{}, fmt.Errorf("record %s legacy import: %w", component, err)
		}
		for sequence, issue := range result.Issues {
			if _, err := conn.ExecContext(
				ctx,
				`INSERT INTO storage_import_issues (
                    component, source_id, sequence, issue_code, record_digest
                ) VALUES (?, ?, ?, ?, ?)`,
				component,
				source.ID,
				sequence,
				issue.Code,
				issue.RecordDigest[:],
			); err != nil {
				return legacyImportSummary{}, fmt.Errorf(
					"record %s legacy import issue: %w",
					component,
					err,
				)
			}
		}
		summary.Imported += int64(result.Imported)
		summary.Skipped += int64(result.Skipped)
		summary.NewSourceIDs = append(summary.NewSourceIDs, source.ID)
		if result.Skipped > 0 {
			summary.SourcesWithSkips++
		}
	}
	if options.Finalize != nil && options.FinalizeResults != nil {
		return legacyImportSummary{}, fmt.Errorf(
			"finalize %s legacy import: multiple finalizers are configured",
			component,
		)
	}
	if len(summary.NewSourceIDs) > 0 && (options.Finalize != nil || options.FinalizeResults != nil) {
		if summary.Imported > int64(maximumLegacyRecordCount) ||
			summary.Skipped > int64(maximumLegacyRecordCount) {
			return legacyImportSummary{}, fmt.Errorf(
				"finalize %s legacy import: aggregate record count exceeds its limit",
				component,
			)
		}
		input := LegacyFinalizeInput{
			SourceIDs: append([]string(nil), summary.NewSourceIDs...),
			Imported:  int(summary.Imported),
			Skipped:   int(summary.Skipped),
		}
		if options.Finalize != nil {
			if err := options.Finalize(ctx, conn, input); err != nil {
				return legacyImportSummary{}, fmt.Errorf(
					"finalize %s legacy import: %w",
					component,
					err,
				)
			}
		} else {
			results, err := options.FinalizeResults(ctx, conn, input)
			if err != nil {
				return legacyImportSummary{}, fmt.Errorf(
					"finalize %s legacy import: %w", component, err,
				)
			}
			if err := replaceFinalizedImportResults(
				ctx, conn, component, input.SourceIDs, results, &summary,
			); err != nil {
				return legacyImportSummary{}, fmt.Errorf(
					"finalize %s legacy import: %w", component, err,
				)
			}
		}
	}
	if options.Seal != nil {
		if err := options.Seal(ctx, conn); err != nil {
			return legacyImportSummary{}, fmt.Errorf("seal %s legacy import: %w", component, err)
		}
	}
	return summary, nil
}

func replaceFinalizedImportResults(
	ctx context.Context,
	conn *sql.Conn,
	component string,
	sourceIDs []string,
	results map[string]ImportResult,
	summary *legacyImportSummary,
) error {
	if len(results) != len(sourceIDs) {
		return errors.New("final accounting does not cover the imported source set")
	}
	allowed := make(map[string]struct{}, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		allowed[sourceID] = struct{}{}
	}
	for sourceID := range results {
		if _, ok := allowed[sourceID]; !ok {
			return errors.New("final accounting contains an unknown source")
		}
	}
	var imported, skipped int64
	sourcesWithSkips := 0
	for _, sourceID := range sourceIDs {
		result, ok := results[sourceID]
		if !ok {
			return errors.New("final accounting is missing a source")
		}
		if err := validateImportResult(result); err != nil {
			return err
		}
		imported += int64(result.Imported)
		skipped += int64(result.Skipped)
		if imported > maximumLegacyRecordCount || skipped > maximumLegacyRecordCount {
			return errors.New("final aggregate record count exceeds its limit")
		}
		if result.Skipped > 0 {
			sourcesWithSkips++
		}
		update, err := conn.ExecContext(ctx, `UPDATE storage_imports
            SET imported_count = ?, skipped_count = ?
            WHERE component = ? AND source_id = ?`,
			result.Imported, result.Skipped, component, sourceID)
		if err != nil {
			return err
		}
		if changed, err := update.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return err
			}
			return errors.New("final accounting source record is missing")
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM storage_import_issues
            WHERE component = ? AND source_id = ?`, component, sourceID); err != nil {
			return err
		}
		for sequence, issue := range result.Issues {
			if _, err := conn.ExecContext(ctx, `INSERT INTO storage_import_issues (
                component, source_id, sequence, issue_code, record_digest
            ) VALUES (?, ?, ?, ?, ?)`, component, sourceID, sequence,
				issue.Code, issue.RecordDigest[:]); err != nil {
				return err
			}
		}
	}
	summary.Imported = imported
	summary.Skipped = skipped
	summary.SourcesWithSkips = sourcesWithSkips
	return nil
}

func archiveImportedSources(
	ctx context.Context,
	db *sql.DB,
	component string,
	options LegacyOptions,
) error {
	type pending struct {
		id, relative string
		digest       []byte
		limit, mode  int64
	}
	return Immediate(ctx, db, func(conn *sql.Conn) error {
		rows, err := conn.QueryContext(
			ctx,
			`SELECT source_id, source_relative, source_digest, source_limit, source_mode
			   FROM storage_imports
			  WHERE component = ? AND archive_status = 'pending'
			  ORDER BY source_id`,
			component,
		)
		if err != nil {
			return fmt.Errorf("list %s pending legacy archives: %w", component, err)
		}
		defer rows.Close()
		var pendingSources []pending
		for rows.Next() {
			var source pending
			if err := rows.Scan(
				&source.id,
				&source.relative,
				&source.digest,
				&source.limit,
				&source.mode,
			); err != nil {
				return fmt.Errorf("scan %s pending legacy archive: %w", component, err)
			}
			pendingSources = append(pendingSources, source)
		}
		rowsErr := rows.Err()
		if rowsErr == nil {
			rowsErr = legacyArchiveRowsErr(rows)
		}
		if rowsErr != nil {
			return fmt.Errorf("list %s pending legacy archives: %w", component, rowsErr)
		}
		if len(pendingSources) == 0 {
			return nil
		}
		if err := validateLegacyRoots(options); err != nil {
			return fmt.Errorf("archive %s legacy sources: %w", component, err)
		}
		if err := ensureLegacyArchiveRoot(options.SourceRoot, options.ArchiveRoot); err != nil {
			return fmt.Errorf("archive %s legacy sources: %w", component, err)
		}

		for _, source := range pendingSources {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !validIdentifier(source.id) || !validRelativePath(source.relative) ||
				source.limit < 1 || source.mode < 0 || source.mode > 0o777 ||
				len(source.digest) != sha256.Size {
				return fmt.Errorf("archive %s legacy source: invalid recorded identity", component)
			}
			if err := validateLegacySourceOutsideArchive(options, source.relative); err != nil {
				return fmt.Errorf("archive %s legacy source %s: %w", component, source.id, err)
			}
			if err := rejectSymlinkPath(options.SourceRoot, source.relative); err != nil {
				return fmt.Errorf("archive %s legacy source %s: %w", component, source.id, err)
			}
			sourcePath := filepath.Join(options.SourceRoot, filepath.FromSlash(source.relative))
			archivePath := filepath.Join(options.ArchiveRoot, filepath.FromSlash(source.relative))
			if err := archiveLegacySource(
				sourcePath,
				archivePath,
				options.ArchiveRoot,
				source.digest,
				source.limit,
				os.FileMode(source.mode),
			); err != nil {
				return fmt.Errorf("archive %s legacy source %s: %w", component, source.id, err)
			}
			result, err := conn.ExecContext(
				ctx,
				`UPDATE storage_imports
				    SET archive_status = 'complete', archived_at = ?
				  WHERE component = ? AND source_id = ? AND archive_status = 'pending'`,
				legacyNow(options).UnixNano(),
				component,
				source.id,
			)
			if err != nil {
				return fmt.Errorf("record %s legacy archive %s: %w", component, source.id, err)
			}
			changed, err := result.RowsAffected()
			if err != nil || changed != 1 {
				return fmt.Errorf(
					"record %s legacy archive %s: import record changed",
					component,
					source.id,
				)
			}
		}
		return nil
	})
}

type legacyFileSnapshot struct {
	digest [sha256.Size]byte
	mode   os.FileMode
	info   os.FileInfo
}

var (
	legacyArchiveInspect      = inspectLegacyRegularFile
	legacyArchiveLink         = os.Link
	legacyArchiveSync         = fileutil.SyncDirectory
	legacyArchiveRemove       = fileutil.RemoveDurable
	legacySourceOpen          = os.Open
	legacySourceReadAll       = io.ReadAll
	legacyInspectOpen         = os.Open
	legacyInspectCopy         = io.Copy
	legacyArchiveMkdir        = fileutil.MkdirAllDurable
	legacyArchiveChmod        = os.Chmod
	legacyArchiveRowsErr      = func(rows *sql.Rows) error { return rows.Err() }
	legacyArchiveSyncSource   = syncLegacySourceFile
	legacyArchiveBeforeRemove = func() {}
	legacyAbsolutePath        = filepath.Abs
	legacyRelativePath        = filepath.Rel
	legacyPathLstat           = os.Lstat
	legacySyncOpen            = func(path string, flag int, mode os.FileMode) (legacySyncFile, error) {
		return os.OpenFile(path, flag, mode)
	}
	legacySyncCopy = io.Copy
)

type legacySyncFile interface {
	io.Reader
	Stat() (os.FileInfo, error)
	Sync() error
	Close() error
}

func archiveLegacySource(
	sourcePath,
	archivePath,
	archiveRoot string,
	expectedDigest []byte,
	maximumBytes int64,
	expectedMode os.FileMode,
) error {
	archive, archiveFound, err := legacyArchiveInspect(archivePath, maximumBytes)
	if err != nil {
		return fmt.Errorf("inspect archive destination: %w", err)
	}
	source, sourceFound, err := legacyArchiveInspect(sourcePath, maximumBytes)
	if err != nil {
		return fmt.Errorf("inspect committed source: %w", err)
	}
	if archiveFound {
		if !legacySnapshotMatches(archive, expectedDigest, expectedMode) {
			return errors.New("destination already exists with different content or permissions")
		}
		// Re-establish destination-name durability on every retry. In particular,
		// a prior link may be visible even though its directory sync reported an
		// error; source removal must not proceed until this sync succeeds.
		if syncErr := legacyArchiveSync(filepath.Dir(archivePath)); syncErr != nil {
			return fmt.Errorf("sync existing archive publication: %w", syncErr)
		}
		if sourceFound {
			if !legacySnapshotMatches(source, expectedDigest, expectedMode) {
				return errors.New("source changed after import")
			}
			if !os.SameFile(source.info, archive.info) {
				return errors.New("source and destination both exist independently")
			}
			if revalidateErr := revalidateLegacyRemoval(
				sourcePath,
				archive,
				expectedDigest,
				maximumBytes,
				expectedMode,
			); revalidateErr != nil {
				return revalidateErr
			}
			if removeErr := legacyArchiveRemove(sourcePath); removeErr != nil &&
				!os.IsNotExist(removeErr) {
				return fmt.Errorf("finish interrupted source removal: %w", removeErr)
			}
		}
		return nil
	}
	if !sourceFound {
		return errors.New("committed source and archive destination are missing")
	}
	if !legacySnapshotMatches(source, expectedDigest, expectedMode) {
		return errors.New("source changed after import")
	}
	if parentErr := ensureArchiveParent(archiveRoot, archivePath); parentErr != nil {
		return fmt.Errorf("prepare archive parent: %w", parentErr)
	}
	syncedSourceInfo, syncSourceErr := legacyArchiveSyncSource(
		sourcePath,
		source,
		expectedDigest,
		maximumBytes,
		expectedMode,
	)
	if syncSourceErr != nil {
		return fmt.Errorf("sync legacy source before archive: %w", syncSourceErr)
	}
	// Link publishes without replacement and preserves the source's exact mode.
	// A crash before durable source removal leaves two names for one inode; the
	// branch above recognizes and safely completes precisely that state.
	if linkErr := legacyArchiveLink(sourcePath, archivePath); linkErr != nil {
		return fmt.Errorf("publish archive without replacement: %w", linkErr)
	}
	if syncErr := legacyArchiveSync(filepath.Dir(archivePath)); syncErr != nil {
		return fmt.Errorf("sync archive publication: %w", syncErr)
	}
	published, found, err := legacyArchiveInspect(archivePath, maximumBytes)
	if err != nil || !found || !legacySnapshotMatches(published, expectedDigest, expectedMode) ||
		!os.SameFile(syncedSourceInfo, published.info) {
		if err == nil {
			err = errors.New("published archive changed before source removal")
		}
		return fmt.Errorf("verify archive publication: %w", err)
	}
	if revalidateErr := revalidateLegacyRemoval(
		sourcePath,
		published,
		expectedDigest,
		maximumBytes,
		expectedMode,
	); revalidateErr != nil {
		return revalidateErr
	}
	if err := legacyArchiveRemove(sourcePath); err != nil {
		return fmt.Errorf("remove archived source: %w", err)
	}
	return nil
}

func syncLegacySourceFile(
	path string,
	expected legacyFileSnapshot,
	expectedDigest []byte,
	maximumBytes int64,
	expectedMode os.FileMode,
) (os.FileInfo, error) {
	file, err := legacySyncOpen(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !safeLegacyRegularFile(openedInfo) || !safeLegacyRegularFile(pathInfo) ||
		!os.SameFile(expected.info, openedInfo) || !os.SameFile(openedInfo, pathInfo) ||
		openedInfo.Size() > maximumBytes || openedInfo.Mode().Perm() != expectedMode.Perm() {
		return nil, errors.New("legacy source changed before archive sync")
	}
	hash := sha256.New()
	written, err := legacySyncCopy(hash, io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, err
	}
	if written > maximumBytes || !equalDigest(hash.Sum(nil), expectedDigest) {
		return nil, errors.New("legacy source content changed before archive sync")
	}
	if syncErr := file.Sync(); syncErr != nil {
		return nil, syncErr
	}
	finalInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, finalInfo) ||
		finalInfo.Mode().Perm() != expectedMode.Perm() {
		return nil, errors.New("legacy source changed while syncing for archive")
	}
	return finalInfo, nil
}

func revalidateLegacyRemoval(
	path string,
	expected legacyFileSnapshot,
	expectedDigest []byte,
	maximumBytes int64,
	expectedMode os.FileMode,
) error {
	legacyArchiveBeforeRemove()
	current, found, err := legacyArchiveInspect(path, maximumBytes)
	if err != nil || !found || !legacySnapshotMatches(current, expectedDigest, expectedMode) ||
		!os.SameFile(expected.info, current.info) {
		return errors.New("legacy source changed immediately before removal")
	}
	return nil
}

func legacySnapshotMatches(
	snapshot legacyFileSnapshot,
	expectedDigest []byte,
	expectedMode os.FileMode,
) bool {
	return equalDigest(snapshot.digest[:], expectedDigest) &&
		snapshot.mode.Perm() == expectedMode.Perm()
}

func validateLegacyRoots(options LegacyOptions) error {
	if options.SourceRoot == "" || options.ArchiveRoot == "" ||
		options.SourceRoot != strings.TrimSpace(options.SourceRoot) ||
		options.ArchiveRoot != strings.TrimSpace(options.ArchiveRoot) ||
		strings.ContainsRune(options.SourceRoot, '\x00') ||
		strings.ContainsRune(options.ArchiveRoot, '\x00') {
		return errors.New("legacy source and archive roots are required")
	}
	sourceAbs, err := legacyAbsolutePath(options.SourceRoot)
	if err != nil {
		return err
	}
	archiveAbs, err := legacyAbsolutePath(options.ArchiveRoot)
	if err != nil {
		return err
	}
	if pathsEqual(sourceAbs, archiveAbs) || !pathWithin(sourceAbs, archiveAbs) {
		return errors.New("legacy archive root must be below the source root")
	}
	if sourceRootErr := requireSafeLegacyDirectory(sourceAbs, "legacy source root"); sourceRootErr != nil {
		return sourceRootErr
	}
	return validateLegacyArchiveAncestors(sourceAbs, archiveAbs)
}

func validateLegacyArchiveAncestors(sourceRoot, archiveRoot string) error {
	relative, err := legacyRelativePath(sourceRoot, archiveRoot)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("legacy archive root must be below the source root")
	}
	current := filepath.Clean(sourceRoot)
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := legacyPathLstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if err := requireSafeLegacyDirectoryInfo(info, "legacy archive ancestor"); err != nil {
			return err
		}
	}
	return nil
}

func ensureLegacyArchiveRoot(sourceRoot, archiveRoot string) error {
	sourceAbs, sourceAbsErr := legacyAbsolutePath(sourceRoot)
	if sourceAbsErr != nil {
		return sourceAbsErr
	}
	archiveAbs, archiveAbsErr := legacyAbsolutePath(archiveRoot)
	if archiveAbsErr != nil {
		return archiveAbsErr
	}
	if sourceRootErr := requireSafeLegacyDirectory(sourceAbs, "legacy source root"); sourceRootErr != nil {
		return sourceRootErr
	}
	relative, err := legacyRelativePath(sourceAbs, archiveAbs)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("legacy archive root must be below the source root")
	}
	current := sourceAbs
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := legacyPathLstat(current)
		if os.IsNotExist(statErr) {
			if mkdirErr := legacyArchiveMkdir(current, 0o700); mkdirErr != nil {
				return mkdirErr
			}
			info, statErr = legacyPathLstat(current)
		}
		if statErr != nil {
			return statErr
		}
		if err := requireSafeLegacyDirectoryInfo(info, "legacy archive ancestor"); err != nil {
			return err
		}
		if chmodErr := legacyArchiveChmod(current, 0o700); chmodErr != nil {
			return chmodErr
		}
	}
	return nil
}

func requireSafeLegacyDirectory(path, label string) error {
	info, err := legacyPathLstat(path)
	if err != nil {
		return err
	}
	return requireSafeLegacyDirectoryInfo(info, label)
}

func requireSafeLegacyDirectoryInfo(info os.FileInfo, label string) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		info.Mode().Perm()&0o022 != 0 {
		mode := os.FileMode(0)
		if info != nil {
			mode = info.Mode()
		}
		return fmt.Errorf("%s must be a non-writable real directory (mode %s)", label, mode)
	}
	return nil
}

func legacyEnumerationBounds(options LegacyOptions) (int, int64, error) {
	maximumSources := options.MaxSources
	if maximumSources == 0 {
		maximumSources = defaultLegacyMaxSources
	}
	if maximumSources < 1 || maximumSources > maximumLegacyMaxSources {
		return 0, 0, errors.New("legacy source count limit is invalid")
	}
	maximumTotalBytes := options.MaxTotalBytes
	if maximumTotalBytes == 0 {
		maximumTotalBytes = defaultLegacyMaxTotalBytes
	}
	if maximumTotalBytes < 1 || maximumTotalBytes > maximumLegacyMaxTotalBytes {
		return 0, 0, errors.New("legacy aggregate size limit is invalid")
	}
	return maximumSources, maximumTotalBytes, nil
}

func validateLegacySourceOutsideArchive(options LegacyOptions, relative string) error {
	sourcePath := filepath.Join(options.SourceRoot, filepath.FromSlash(relative))
	sourceAbs, err := filepath.Abs(sourcePath)
	if err != nil {
		return err
	}
	archiveAbs, err := filepath.Abs(options.ArchiveRoot)
	if err != nil {
		return err
	}
	if pathsEqual(sourceAbs, archiveAbs) || pathWithin(archiveAbs, sourceAbs) {
		return errors.New("legacy source is inside the archive root")
	}
	return nil
}

func legacyRelativePathKey(relative string) string {
	// Use a conservative cross-platform key even on case-sensitive hosts so an
	// archive produced on one supported platform cannot become ambiguous when
	// restored on another.
	return strings.ToLower(filepath.Clean(filepath.FromSlash(relative)))
}

func readLegacySource(
	root string,
	source LegacySource,
	defaultMax int64,
) (LegacyInput, bool, error) {
	path := filepath.Join(root, filepath.FromSlash(source.Relative))
	maxBytes := source.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultMax
	}
	if maxBytes == 0 {
		maxBytes = defaultLegacyMaxBytes
	}
	if maxBytes < 1 || maxBytes > 1<<30 {
		return LegacyInput{}, false, errors.New("legacy source size limit is invalid")
	}
	if err := rejectSymlinkPath(root, source.Relative); err != nil {
		return LegacyInput{}, false, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return LegacyInput{}, false, nil
	}
	if err != nil {
		return LegacyInput{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
		info.Mode().Perm()&0o022 != 0 {
		return LegacyInput{}, false, errors.New("legacy source has an unsafe type or mode")
	}
	if info.Size() > maxBytes {
		return LegacyInput{}, false, errors.New("legacy source exceeds the size limit")
	}
	file, err := legacySourceOpen(path)
	if err != nil {
		return LegacyInput{}, false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return LegacyInput{}, false, err
	}
	currentInfo, err := os.Lstat(path)
	if err != nil || !safeLegacyRegularFile(openedInfo) ||
		!safeLegacyRegularFile(currentInfo) || !os.SameFile(info, openedInfo) ||
		!os.SameFile(openedInfo, currentInfo) {
		return LegacyInput{}, false, errors.New("legacy source changed while opening")
	}
	data, err := legacySourceReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return LegacyInput{}, false, err
	}
	if int64(len(data)) > maxBytes {
		return LegacyInput{}, false, errors.New("legacy source exceeds the size limit")
	}
	return LegacyInput{
		ID:       source.ID,
		Relative: source.Relative,
		Data:     data,
		Digest:   sha256.Sum256(data),
		Limit:    maxBytes,
		Mode:     openedInfo.Mode().Perm(),
		ModTime:  openedInfo.ModTime(),
	}, true, nil
}

func inspectLegacyRegularFile(
	path string,
	maxBytes int64,
) (legacyFileSnapshot, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return legacyFileSnapshot{}, false, nil
	}
	if err != nil {
		return legacyFileSnapshot{}, false, err
	}
	if maxBytes < 1 || !safeLegacyRegularFile(info) || info.Size() > maxBytes {
		return legacyFileSnapshot{}, false, errors.New("path is not a safe bounded regular file")
	}
	file, err := legacyInspectOpen(path)
	if err != nil {
		return legacyFileSnapshot{}, false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return legacyFileSnapshot{}, false, err
	}
	currentInfo, err := os.Lstat(path)
	if err != nil || !safeLegacyRegularFile(openedInfo) ||
		!safeLegacyRegularFile(currentInfo) || !os.SameFile(info, openedInfo) ||
		!os.SameFile(openedInfo, currentInfo) {
		return legacyFileSnapshot{}, false, errors.New("path changed while opening")
	}
	hash := sha256.New()
	written, err := legacyInspectCopy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil {
		return legacyFileSnapshot{}, false, err
	}
	if written > maxBytes {
		return legacyFileSnapshot{}, false, errors.New("path exceeds the size limit")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return legacyFileSnapshot{
		digest: digest,
		mode:   openedInfo.Mode().Perm(),
		info:   openedInfo,
	}, true, nil
}

func ensureArchiveParent(root, destination string) error {
	if !pathWithin(root, destination) {
		return errors.New("archive destination escapes its root")
	}
	if err := ensurePrivateDir(root); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	// pathWithin above proves destination is strictly within root; its parent is
	// therefore root itself or another descendant, and Rel cannot escape.
	relative, _ := filepath.Rel(root, parent)
	current := filepath.Clean(root)
	if relative != "." {
		for _, part := range strings.Split(relative, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				if mkdirErr := legacyArchiveMkdir(current, 0o700); mkdirErr != nil {
					return mkdirErr
				}
				info, err = os.Lstat(current)
			}
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("archive path contains a symlink or non-directory")
			}
			if err := legacyArchiveChmod(current, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectSymlinkPath(root, relative string) error {
	rootInfo, err := legacyPathLstat(root)
	if err != nil {
		return err
	}
	if err := requireSafeLegacyDirectoryInfo(rootInfo, "legacy source root"); err != nil {
		return err
	}
	current := root
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := legacyPathLstat(current)
		if err != nil {
			return err
		}
		if err := requireSafeLegacyDirectoryInfo(info, "legacy source directory"); err != nil {
			return err
		}
	}
	return nil
}

func validateImportResult(result ImportResult) error {
	if result.Imported < 0 || result.Imported > maximumLegacyRecordCount ||
		result.Skipped < 0 || result.Skipped > maximumLegacyRecordCount ||
		len(result.Issues) > maxImportIssues ||
		len(result.Issues) > result.Skipped {
		return errors.New("legacy import accounting is invalid")
	}
	for _, issue := range result.Issues {
		if !validIdentifier(issue.Code) || zeroDigest(issue.RecordDigest) {
			return errors.New("legacy import issue code is invalid")
		}
	}
	return nil
}

func validRelativePath(value string) bool {
	if value == "" || len(value) > maximumLegacyRelativeBytes || !utf8.ValidString(value) ||
		value != filepath.ToSlash(filepath.Clean(value)) ||
		filepath.IsAbs(value) || filepath.VolumeName(filepath.FromSlash(value)) != "" ||
		strings.ContainsRune(value, '\x00') || value == "." ||
		value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	return true
}

func pathWithin(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && relative != "." && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathsEqual(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(leftAbs), filepath.Clean(rightAbs))
}

func safeLegacyRegularFile(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 &&
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 &&
		info.Mode().Perm()&0o022 == 0
}

func equalDigest(left, right []byte) bool {
	if len(left) != sha256.Size || len(right) != sha256.Size {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func zeroDigest(digest [sha256.Size]byte) bool {
	var combined byte
	for _, value := range digest {
		combined |= value
	}
	return combined == 0
}

func legacyNow(options LegacyOptions) time.Time {
	if options.Now != nil {
		return options.Now().UTC()
	}
	return time.Now().UTC()
}
