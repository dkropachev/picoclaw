package gateway

import (
	"bytes"
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
	"sort"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	prWorkspaceCandidateCheckpointVersion = 2
	prWorkspaceCandidateCheckpointMaxSize = 64 << 10
	prWorkspaceCandidateCheckpointActive  = "active"
	prWorkspaceCandidateCheckpointParked  = "parked"
	prWorkspaceCheckpointDatabaseFilename = "checkpoints.db"
	prWorkspaceCheckpointComponent        = "pr-workspace-checkpoints"
	prWorkspaceCheckpointArchiveLabel     = "pr-workspace-checkpoints-v1"
	prWorkspaceCheckpointMaximumRecords   = 10_000
	prWorkspaceCheckpointLegacyReadBatch  = 128
)

type prWorkspaceCandidateCheckpoint struct {
	Version        int                                         `json:"version"`
	State          string                                      `json:"state"`
	WorkspaceID    string                                      `json:"workspace_id"`
	Repository     string                                      `json:"repository"`
	SourceRef      string                                      `json:"source_ref"`
	HeadSHA        string                                      `json:"head_sha"`
	CharterID      string                                      `json:"charter_id"`
	CharterHeadSHA string                                      `json:"charter_head_sha"`
	GitWorkspaceID string                                      `json:"git_workspace_id"`
	LineID         string                                      `json:"line_id"`
	Lease          gitworkspace.PinnedLineLease                `json:"lease"`
	Candidate      gitworkspace.PinnedCandidate                `json:"candidate"`
	Fence          *prworkspace.ImplementationPublicationFence `json:"fence,omitempty"`
}

// prWorkspaceCandidateCheckpointRevision is an opaque, workspace-bound
// compare-and-swap token. The globally allocated sequence prevents ABA after
// a checkpoint is deleted and recreated; the digest also fences the complete
// expected state.
type prWorkspaceCandidateCheckpointRevision struct {
	workspaceID string
	sequence    int64
	stateDigest [sha256.Size]byte
	exists      bool
}

type prWorkspaceCandidateCheckpointStore struct {
	root           string
	maximumRecords int
	mu             sync.Mutex
}

const prWorkspaceCheckpointsSchema = `CREATE TABLE candidate_checkpoints (
    workspace_id              TEXT PRIMARY KEY CHECK(length(CAST(workspace_id AS BLOB)) BETWEEN 1 AND 256),
    checkpoint_version        INTEGER NOT NULL CHECK(checkpoint_version = 2),
    state                     TEXT NOT NULL CHECK(state IN ('active', 'parked')),
    repository                TEXT NOT NULL CHECK(length(CAST(repository AS BLOB)) BETWEEN 1 AND 4096),
    source_ref                TEXT NOT NULL CHECK(length(CAST(source_ref AS BLOB)) BETWEEN 1 AND 4096),
    head_sha                  TEXT NOT NULL CHECK(length(CAST(head_sha AS BLOB)) BETWEEN 1 AND 256),
    charter_id                TEXT NOT NULL CHECK(length(CAST(charter_id AS BLOB)) BETWEEN 1 AND 256),
    charter_head_sha          TEXT NOT NULL CHECK(length(CAST(charter_head_sha AS BLOB)) BETWEEN 1 AND 256),
    git_workspace_id          TEXT NOT NULL CHECK(length(CAST(git_workspace_id AS BLOB)) BETWEEN 1 AND 256),
    line_id                   TEXT NOT NULL CHECK(length(CAST(line_id AS BLOB)) BETWEEN 1 AND 256),
    lease_workspace_id        TEXT NOT NULL CHECK(length(CAST(lease_workspace_id AS BLOB)) BETWEEN 1 AND 256),
    lease_version             INTEGER NOT NULL CHECK(lease_version BETWEEN 0 AND 8192),
    lease_mutation_epoch      INTEGER NOT NULL CHECK(lease_mutation_epoch BETWEEN 1 AND 8193),
    lease_tip                 TEXT NOT NULL CHECK(length(CAST(lease_tip AS BLOB)) BETWEEN 1 AND 256),
    lease_tree                TEXT NOT NULL CHECK(length(CAST(lease_tree AS BLOB)) BETWEEN 1 AND 256),
    lease_already_owned       INTEGER NOT NULL CHECK(lease_already_owned IN (0, 1)),
    candidate_workspace_id    TEXT NOT NULL CHECK(length(CAST(candidate_workspace_id AS BLOB)) BETWEEN 1 AND 256),
    candidate_parent_commit   TEXT NOT NULL CHECK(length(CAST(candidate_parent_commit AS BLOB)) BETWEEN 1 AND 256),
    candidate_tree            TEXT NOT NULL CHECK(length(CAST(candidate_tree AS BLOB)) BETWEEN 1 AND 256),
    candidate_digest          TEXT NOT NULL CHECK(length(CAST(candidate_digest AS BLOB)) = 64),
    candidate_changed_files   INTEGER NOT NULL CHECK(candidate_changed_files BETWEEN 0 AND 1000000),
    fence_git_workspace_id    TEXT,
    fence_line_id             TEXT,
    fence_line_version        INTEGER,
    fence_mutation_epoch      INTEGER,
    fence_park_intent_id      TEXT,
    fence_base_commit         TEXT,
    fence_tip                 TEXT,
    fence_tree                TEXT,
    row_version               INTEGER NOT NULL CHECK(row_version > 0),
    CHECK(charter_head_sha = head_sha),
    CHECK(lease_workspace_id = git_workspace_id),
    CHECK(candidate_workspace_id = git_workspace_id),
    CHECK((state = 'active' AND fence_git_workspace_id IS NULL AND fence_line_id IS NULL AND
           fence_line_version IS NULL AND fence_mutation_epoch IS NULL AND fence_park_intent_id IS NULL AND
           fence_base_commit IS NULL AND fence_tip IS NULL AND fence_tree IS NULL) OR
          (state = 'parked' AND fence_git_workspace_id IS NOT NULL AND fence_line_id IS NOT NULL AND
           fence_line_version IS NOT NULL AND fence_mutation_epoch IS NOT NULL AND
           fence_park_intent_id IS NOT NULL AND fence_base_commit IS NOT NULL AND
           fence_tip IS NOT NULL AND fence_tree IS NOT NULL AND
           length(CAST(fence_git_workspace_id AS BLOB)) BETWEEN 1 AND 256 AND
           length(CAST(fence_line_id AS BLOB)) BETWEEN 1 AND 256 AND fence_line_version BETWEEN 1 AND 8193 AND
           fence_mutation_epoch BETWEEN 1 AND 8193 AND length(CAST(fence_park_intent_id AS BLOB)) BETWEEN 1 AND 256 AND
           length(CAST(fence_base_commit AS BLOB)) BETWEEN 1 AND 256 AND
           length(CAST(fence_tip AS BLOB)) BETWEEN 1 AND 256 AND length(CAST(fence_tree AS BLOB)) BETWEEN 1 AND 256))
) STRICT`

const prWorkspaceCheckpointMetaSchema = `CREATE TABLE checkpoint_metadata (
    singleton      INTEGER PRIMARY KEY CHECK(singleton = 1),
    next_revision  INTEGER NOT NULL CHECK(next_revision > 0)
) STRICT`

const prWorkspaceCheckpointImportStateSchema = `CREATE TABLE checkpoint_legacy_import_state (
    singleton      INTEGER PRIMARY KEY CHECK(singleton = 1),
    import_closed  INTEGER NOT NULL CHECK(import_closed = 1)
) STRICT`

const prWorkspaceCheckpointDeletionsSchema = `CREATE TABLE checkpoint_deletions (
    workspace_id         TEXT PRIMARY KEY CHECK(length(CAST(workspace_id AS BLOB)) BETWEEN 1 AND 256),
    deleted_row_version  INTEGER NOT NULL CHECK(deleted_row_version > 0),
    deleted_state_digest BLOB NOT NULL CHECK(length(deleted_state_digest) = 32),
    deletion_sequence    INTEGER NOT NULL CHECK(deletion_sequence > deleted_row_version)
) STRICT`

const prWorkspaceCheckpointDeletionsSequenceIndexSchema = `CREATE INDEX checkpoint_deletions_sequence_idx
    ON checkpoint_deletions(deletion_sequence, workspace_id)`

const prWorkspaceCheckpointsStateIndexSchema = `CREATE INDEX candidate_checkpoints_state_idx
    ON candidate_checkpoints(state, workspace_id)`

func newPRWorkspaceCandidateCheckpointStore(root string) (*prWorkspaceCandidateCheckpointStore, error) {
	if stringsTrimmed(root) == "" || !filepath.IsAbs(root) {
		return nil, errors.New("PR workspace candidate checkpoint root must be absolute")
	}
	root = filepath.Clean(root)
	if err := ensurePrivatePRWorkspaceDirectory(root); err != nil {
		return nil, fmt.Errorf("prepare PR workspace candidate checkpoints: %w", err)
	}
	store := &prWorkspaceCandidateCheckpointStore{root: root}
	database, err := store.open(context.Background())
	if err != nil {
		return nil, err
	}
	if err = database.Close(); err != nil {
		return nil, fmt.Errorf("close PR workspace candidate checkpoints: %w", err)
	}
	return store, nil
}

func (store *prWorkspaceCandidateCheckpointStore) databasePath() string {
	return filepath.Join(store.root, prWorkspaceCheckpointDatabaseFilename)
}

func (store *prWorkspaceCandidateCheckpointStore) recordLimit() int {
	if store != nil && store.maximumRecords > 0 &&
		store.maximumRecords <= prWorkspaceCheckpointMaximumRecords {
		return store.maximumRecords
	}
	return prWorkspaceCheckpointMaximumRecords
}

func (store *prWorkspaceCandidateCheckpointStore) open(ctx context.Context) (*sql.DB, error) {
	if store == nil {
		return nil, errors.New("PR workspace candidate checkpoint store is unavailable")
	}
	freshDatabase := false
	return sqlitestore.Open(ctx, store.databasePath(), sqlitestore.Options{
		Component: prWorkspaceCheckpointComponent,
		Migrations: []sqlitestore.Migration{
			{
				Version: 1,
				Statements: []string{
					prWorkspaceCheckpointsSchema,
					prWorkspaceCheckpointsStateIndexSchema,
				},
				Apply: func(context.Context, *sql.Conn) error {
					freshDatabase = true
					return nil
				},
			},
			{
				Version: 2,
				Statements: []string{
					prWorkspaceCheckpointMetaSchema,
					prWorkspaceCheckpointImportStateSchema,
				},
				Apply: func(ctx context.Context, conn *sql.Conn) error {
					return initializePRWorkspaceCheckpointMeta(ctx, conn, freshDatabase)
				},
			},
			{
				Version: 3,
				Statements: []string{
					prWorkspaceCheckpointDeletionsSchema,
					prWorkspaceCheckpointDeletionsSequenceIndexSchema,
				},
			},
		},
		Validate: validatePRWorkspaceCheckpointSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  store.root,
			ArchiveRoot: filepath.Join(store.root, "legacy-json", prWorkspaceCheckpointArchiveLabel),
			Sources:     store.legacySources,
			Import:      importLegacyPRWorkspaceCheckpoint,
			Seal: func(ctx context.Context, conn *sql.Conn) error {
				return sealPRWorkspaceCheckpointImport(ctx, conn, freshDatabase)
			},
			MaxBytes:      prWorkspaceCandidateCheckpointMaxSize,
			MaxSources:    prWorkspaceCheckpointMaximumRecords,
			MaxTotalBytes: prWorkspaceCheckpointMaximumRecords * prWorkspaceCandidateCheckpointMaxSize,
		},
	})
}

func validatePRWorkspaceCheckpointSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct{ typ, name, ddl string }{
		{"table", "checkpoint_metadata", prWorkspaceCheckpointMetaSchema},
		{"table", "checkpoint_legacy_import_state", prWorkspaceCheckpointImportStateSchema},
		{"table", "checkpoint_deletions", prWorkspaceCheckpointDeletionsSchema},
		{"table", "candidate_checkpoints", prWorkspaceCheckpointsSchema},
		{"index", "candidate_checkpoints_state_idx", prWorkspaceCheckpointsStateIndexSchema},
		{"index", "checkpoint_deletions_sequence_idx", prWorkspaceCheckpointDeletionsSequenceIndexSchema},
	} {
		if err := sqlitestore.ValidateSchemaObject(ctx, conn, object.typ, object.name, object.ddl); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"checkpoint_metadata", "checkpoint_legacy_import_state", "checkpoint_deletions",
		"candidate_checkpoints",
	} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	var unexpected, count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%' AND name NOT IN (
		            'checkpoint_metadata', 'checkpoint_legacy_import_state', 'checkpoint_deletions',
		            'candidate_checkpoints', 'candidate_checkpoints_state_idx',
		            'checkpoint_deletions_sequence_idx',
            'storage_imports', 'storage_import_issues', 'storage_imports_archive_status_idx'
        )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("PR workspace checkpoint schema has unexpected objects")
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM candidate_checkpoints`).Scan(&count); err != nil {
		return err
	}
	if count > prWorkspaceCheckpointMaximumRecords {
		return errors.New("PR workspace checkpoint count exceeds its limit")
	}
	var deletionCount int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoint_deletions`).Scan(
		&deletionCount,
	); err != nil {
		return err
	}
	if deletionCount > prWorkspaceCheckpointMaximumRecords {
		return errors.New("PR workspace checkpoint deletion history exceeds its limit")
	}
	var nextRevision, maximumRevision int64
	if err := conn.QueryRowContext(ctx, `SELECT next_revision
	    FROM checkpoint_metadata WHERE singleton = 1`).Scan(&nextRevision); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `SELECT MAX(revision) FROM (
	        SELECT COALESCE(MAX(row_version), 0) AS revision FROM candidate_checkpoints
	        UNION ALL
	        SELECT COALESCE(MAX(deletion_sequence), 0) AS revision FROM checkpoint_deletions
	    )`).Scan(&maximumRevision); err != nil {
		return err
	}
	var importStateRows int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
	    FROM checkpoint_legacy_import_state
	    WHERE singleton = 1 AND import_closed = 1`).Scan(&importStateRows); err != nil {
		return err
	}
	if nextRevision <= maximumRevision || importStateRows != 1 {
		return errors.New("PR workspace checkpoint metadata is invalid")
	}
	rows, err := conn.QueryContext(ctx, prWorkspaceCheckpointSelectSQL+` ORDER BY workspace_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		checkpoint, revision, err := scanPRWorkspaceCheckpoint(rows)
		encoded, encodeErr := encodePRWorkspaceCheckpoint(checkpoint)
		if err != nil || encodeErr != nil || revision <= 0 ||
			!validPRWorkspaceCandidateCheckpointShape(checkpoint) ||
			len(encoded) > prWorkspaceCandidateCheckpointMaxSize {
			return errors.New("PR workspace checkpoint row is invalid")
		}
	}
	return rows.Err()
}

func initializePRWorkspaceCheckpointMeta(ctx context.Context, conn *sql.Conn, fresh bool) error {
	var maximumRevision int64
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(row_version), 0)
	    FROM candidate_checkpoints`).Scan(&maximumRevision); err != nil {
		return err
	}
	if maximumRevision == int64(^uint64(0)>>1) {
		return errors.New("PR workspace checkpoint revision is exhausted")
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO checkpoint_metadata(
	    singleton, next_revision
	) VALUES (1, ?)`, maximumRevision+1); err != nil {
		return err
	}
	if fresh {
		return nil
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO checkpoint_legacy_import_state(
	    singleton, import_closed
	) VALUES (1, 1)`)
	return err
}

func sealPRWorkspaceCheckpointImport(ctx context.Context, conn *sql.Conn, fresh bool) error {
	if fresh {
		_, err := conn.ExecContext(ctx, `INSERT INTO checkpoint_legacy_import_state(
		    singleton, import_closed
		) VALUES (1, 1)`)
		return err
	}
	var rows int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
	    FROM checkpoint_legacy_import_state
	    WHERE singleton = 1 AND import_closed = 1`).Scan(&rows); err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf(
			"%w: PR workspace checkpoint import horizon is missing",
			sqlitestore.ErrInvalidSchema,
		)
	}
	return nil
}

func (store *prWorkspaceCandidateCheckpointStore) legacySources() ([]sqlitestore.LegacySource, error) {
	return store.legacySourcesBounded(
		prWorkspaceCheckpointMaximumRecords+4,
		prWorkspaceCheckpointLegacyReadBatch,
	)
}

func (store *prWorkspaceCandidateCheckpointStore) legacySourcesBounded(
	maximumEntries,
	readBatch int,
) ([]sqlitestore.LegacySource, error) {
	if maximumEntries < 1 || readBatch < 1 {
		return nil, errors.New("PR workspace checkpoint legacy enumeration bounds are invalid")
	}
	for _, directory := range []string{
		filepath.Join(store.root, "legacy-json"),
		filepath.Join(store.root, "legacy-json", prWorkspaceCheckpointArchiveLabel),
	} {
		info, statErr := os.Lstat(directory)
		if os.IsNotExist(statErr) {
			break
		}
		if statErr != nil {
			return nil, statErr
		}
		if !privatePRWorkspaceCheckpointDirectory(directory, info) || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("PR workspace checkpoint legacy archive ancestor is unsafe")
		}
	}
	rootInfo, err := os.Lstat(store.root)
	if err != nil {
		return nil, err
	}
	if !privatePRWorkspaceCheckpointDirectory(store.root, rootInfo) || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("PR workspace checkpoint directory is unsafe")
	}
	directory, err := os.Open(store.root)
	if err != nil {
		return nil, err
	}
	fail := func(cause error) ([]sqlitestore.LegacySource, error) {
		return nil, errors.Join(cause, directory.Close())
	}
	openedInfo, err := directory.Stat()
	if err != nil || !os.SameFile(rootInfo, openedInfo) {
		return fail(errors.Join(errors.New("PR workspace checkpoint directory changed"), err))
	}
	sources := make([]sqlitestore.LegacySource, 0)
	entryCount := 0
	for {
		entries, readErr := directory.ReadDir(readBatch)
		for _, entry := range entries {
			entryCount++
			if entryCount > maximumEntries {
				return fail(errors.New("PR workspace checkpoint directory exceeds its entry limit"))
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			info, statErr := os.Lstat(filepath.Join(store.root, name))
			if statErr != nil {
				return fail(statErr)
			}
			path := filepath.Join(store.root, name)
			if !privatePRWorkspaceCheckpointFile(path, info) || info.Mode()&os.ModeSymlink != 0 {
				return fail(errors.New("PR workspace checkpoint legacy source is unsafe"))
			}
			digest := sha256.Sum256([]byte(name))
			sources = append(sources, sqlitestore.LegacySource{
				ID:       "checkpoint-" + hex.EncodeToString(digest[:16]),
				Relative: name,
				MaxBytes: prWorkspaceCandidateCheckpointMaxSize,
			})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fail(readErr)
		}
	}
	if closeErr := directory.Close(); closeErr != nil {
		return nil, closeErr
	}
	currentRoot, err := os.Lstat(store.root)
	if err != nil || !os.SameFile(rootInfo, currentRoot) ||
		!privatePRWorkspaceCheckpointDirectory(store.root, currentRoot) {
		return nil, errors.Join(errors.New("PR workspace checkpoint directory changed"), err)
	}
	sort.Slice(sources, func(left, right int) bool { return sources[left].Relative < sources[right].Relative })
	return sources, nil
}

func importLegacyPRWorkspaceCheckpoint(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	skip := func(code string) (sqlitestore.ImportResult, error) {
		return sqlitestore.ImportResult{
			Skipped: 1,
			Issues:  []sqlitestore.ImportIssue{{Code: code, RecordDigest: input.Digest}},
		}, nil
	}
	var importClosed int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
	    FROM checkpoint_legacy_import_state
	    WHERE singleton = 1 AND import_closed = 1`).Scan(&importClosed); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if importClosed == 1 {
		return skip("sqlite-authoritative")
	}
	if importClosed != 0 {
		return sqlitestore.ImportResult{}, errors.New(
			"PR workspace checkpoint import horizon is invalid",
		)
	}
	checkpoint, err := decodeLegacyPRWorkspaceCheckpoint(input.Data)
	if err != nil {
		return skip("malformed-checkpoint")
	}
	expectedName := legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID)
	if input.Relative != expectedName || !validPRWorkspaceCandidateCheckpointShape(checkpoint) {
		return skip("invalid-checkpoint")
	}
	var existing int
	err = conn.QueryRowContext(ctx, `SELECT 1 FROM candidate_checkpoints WHERE workspace_id = ?`,
		checkpoint.WorkspaceID).Scan(&existing)
	if err == nil {
		return skip("identity-conflict")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return sqlitestore.ImportResult{}, err
	}
	revision, err := allocatePRWorkspaceCheckpointRevision(ctx, conn)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	arguments := prWorkspaceCheckpointArguments(checkpoint)
	arguments = append(arguments, revision)
	if _, err := conn.ExecContext(ctx, prWorkspaceCheckpointInsertSQL, arguments...); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

func decodeLegacyPRWorkspaceCheckpoint(data []byte) (prWorkspaceCandidateCheckpoint, error) {
	if err := rejectDuplicatePRWorkspaceCheckpointJSONNames(data); err != nil {
		return prWorkspaceCandidateCheckpoint{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var checkpoint prWorkspaceCandidateCheckpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return prWorkspaceCandidateCheckpoint{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return prWorkspaceCandidateCheckpoint{}, err
	}
	return checkpoint, nil
}

func rejectDuplicatePRWorkspaceCheckpointJSONNames(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeUniquePRWorkspaceCheckpointJSONValue(decoder, 0); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func consumeUniquePRWorkspaceCheckpointJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("PR workspace checkpoint JSON nesting exceeds its limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("PR workspace checkpoint JSON name is invalid")
			}
			identity := strings.ToLower(name)
			if _, duplicate := seen[identity]; duplicate {
				return errors.New("PR workspace checkpoint JSON name is duplicated")
			}
			seen[identity] = struct{}{}
			if err := consumeUniquePRWorkspaceCheckpointJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			if err != nil {
				return err
			}
			return errors.New("PR workspace checkpoint JSON object is unterminated")
		}
		return nil
	case '[':
		for decoder.More() {
			if err := consumeUniquePRWorkspaceCheckpointJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			if err != nil {
				return err
			}
			return errors.New("PR workspace checkpoint JSON array is unterminated")
		}
		return nil
	default:
		return errors.New("PR workspace checkpoint JSON delimiter is invalid")
	}
}

var (
	errPRWorkspaceCandidateCheckpointConflict = errors.New("PR workspace candidate checkpoint conflict")
	errPRWorkspaceCheckpointCapacity          = errors.New("PR workspace checkpoint capacity is exhausted")
)

func (store *prWorkspaceCandidateCheckpointStore) Save(
	checkpoint prWorkspaceCandidateCheckpoint,
	expected prWorkspaceCandidateCheckpointRevision,
) (prWorkspaceCandidateCheckpointRevision, error) {
	if store == nil || !validPRWorkspaceCandidateCheckpointShape(checkpoint) ||
		!validPRWorkspaceCheckpointRevision(expected, checkpoint.WorkspaceID) {
		return prWorkspaceCandidateCheckpointRevision{}, errors.New(
			"PR workspace candidate checkpoint is invalid",
		)
	}
	encoded, err := encodePRWorkspaceCheckpoint(checkpoint)
	if err != nil {
		return prWorkspaceCandidateCheckpointRevision{}, err
	}
	if len(encoded) > prWorkspaceCandidateCheckpointMaxSize {
		return prWorkspaceCandidateCheckpointRevision{}, errors.New(
			"PR workspace candidate checkpoint is too large",
		)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	database, err := store.open(context.Background())
	if err != nil {
		return prWorkspaceCandidateCheckpointRevision{}, err
	}
	defer database.Close()
	var committed prWorkspaceCandidateCheckpointRevision
	err = sqlitestore.Immediate(context.Background(), database, func(conn *sql.Conn) error {
		current, version, found, loadErr := loadPRWorkspaceCheckpoint(
			context.Background(), conn, checkpoint.WorkspaceID,
		)
		if loadErr != nil {
			return loadErr
		}
		currentRevision, revisionErr := revisionForPRWorkspaceCheckpoint(
			context.Background(), conn, current, version, found, checkpoint.WorkspaceID,
		)
		if revisionErr != nil {
			return revisionErr
		}
		if currentRevision != expected {
			return errPRWorkspaceCandidateCheckpointConflict
		}
		arguments := prWorkspaceCheckpointArguments(checkpoint)
		if !found {
			var count int
			if queryErr := conn.QueryRowContext(context.Background(), `SELECT COUNT(*)
			    FROM candidate_checkpoints`).Scan(&count); queryErr != nil {
				return queryErr
			}
			if count >= store.recordLimit() {
				return errPRWorkspaceCheckpointCapacity
			}
			allocated, allocateErr := allocatePRWorkspaceCheckpointRevision(context.Background(), conn)
			if allocateErr != nil {
				return allocateErr
			}
			version = allocated
			arguments = append(arguments, version)
			if _, execErr := conn.ExecContext(
				context.Background(),
				prWorkspaceCheckpointInsertSQL,
				arguments...,
			); execErr != nil {
				return fmt.Errorf("write PR workspace candidate checkpoint: %w", execErr)
			}
			committed = newPRWorkspaceCheckpointRevision(checkpoint, version, encoded)
			return nil
		}
		if equivalentPRWorkspaceCheckpoint(current, checkpoint) {
			committed = currentRevision
			return nil
		}
		if current.State == prWorkspaceCandidateCheckpointParked ||
			current.WorkspaceID != checkpoint.WorkspaceID || current.Repository != checkpoint.Repository ||
			current.SourceRef != checkpoint.SourceRef || current.HeadSHA != checkpoint.HeadSHA ||
			current.CharterID != checkpoint.CharterID || current.CharterHeadSHA != checkpoint.CharterHeadSHA ||
			current.GitWorkspaceID != checkpoint.GitWorkspaceID || current.LineID != checkpoint.LineID {
			return errPRWorkspaceCandidateCheckpointConflict
		}
		newVersion, allocateErr := allocatePRWorkspaceCheckpointRevision(context.Background(), conn)
		if allocateErr != nil {
			return allocateErr
		}
		arguments = append(arguments[1:], newVersion, checkpoint.WorkspaceID, version)
		result, execErr := conn.ExecContext(context.Background(), prWorkspaceCheckpointUpdateSQL, arguments...)
		if execErr != nil {
			return fmt.Errorf("write PR workspace candidate checkpoint: %w", execErr)
		}
		changed, rowsErr := result.RowsAffected()
		if rowsErr != nil || changed != 1 {
			return errPRWorkspaceCandidateCheckpointConflict
		}
		committed = newPRWorkspaceCheckpointRevision(checkpoint, newVersion, encoded)
		return nil
	})
	if err != nil {
		return prWorkspaceCandidateCheckpointRevision{}, err
	}
	return committed, nil
}

func (store *prWorkspaceCandidateCheckpointStore) Load(
	workspaceID string,
) (prWorkspaceCandidateCheckpoint, prWorkspaceCandidateCheckpointRevision, bool, error) {
	if store == nil || stringsTrimmed(workspaceID) == "" {
		return prWorkspaceCandidateCheckpoint{}, prWorkspaceCandidateCheckpointRevision{}, false, errors.New(
			"PR workspace candidate checkpoint lookup is invalid",
		)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	database, err := store.open(context.Background())
	if err != nil {
		return prWorkspaceCandidateCheckpoint{}, prWorkspaceCandidateCheckpointRevision{}, false, err
	}
	defer database.Close()
	checkpoint, sequence, found, err := loadPRWorkspaceCheckpoint(
		context.Background(), database, workspaceID,
	)
	if err != nil {
		return prWorkspaceCandidateCheckpoint{}, prWorkspaceCandidateCheckpointRevision{}, false, err
	}
	if found && (checkpoint.WorkspaceID != workspaceID || !validPRWorkspaceCandidateCheckpointShape(checkpoint)) {
		return prWorkspaceCandidateCheckpoint{}, prWorkspaceCandidateCheckpointRevision{}, false, errors.New(
			"PR workspace candidate checkpoint identity is invalid",
		)
	}
	revision, err := revisionForPRWorkspaceCheckpoint(
		context.Background(), database, checkpoint, sequence, found, workspaceID,
	)
	if err != nil {
		return prWorkspaceCandidateCheckpoint{}, prWorkspaceCandidateCheckpointRevision{}, false, err
	}
	return checkpoint, revision, found, nil
}

func (store *prWorkspaceCandidateCheckpointStore) Remove(
	workspaceID string,
	expected prWorkspaceCandidateCheckpointRevision,
) error {
	if store == nil || stringsTrimmed(workspaceID) == "" ||
		!validPRWorkspaceCheckpointRevision(expected, workspaceID) {
		return errors.New("PR workspace candidate checkpoint removal is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	database, err := store.open(context.Background())
	if err != nil {
		return err
	}
	defer database.Close()
	return sqlitestore.Immediate(context.Background(), database, func(conn *sql.Conn) error {
		checkpoint, sequence, found, err := loadPRWorkspaceCheckpoint(
			context.Background(), conn, workspaceID,
		)
		if err != nil {
			return err
		}
		currentRevision, err := revisionForPRWorkspaceCheckpoint(
			context.Background(), conn, checkpoint, sequence, found, workspaceID,
		)
		if err != nil {
			return err
		}
		if currentRevision != expected {
			return errPRWorkspaceCandidateCheckpointConflict
		}
		if !found {
			return nil
		}
		var existingDeletion, deletionCount int
		if queryErr := conn.QueryRowContext(context.Background(), `SELECT
		        EXISTS(SELECT 1 FROM checkpoint_deletions WHERE workspace_id = ?),
		        (SELECT COUNT(*) FROM checkpoint_deletions)`, workspaceID).Scan(
			&existingDeletion, &deletionCount,
		); queryErr != nil {
			return queryErr
		}
		if existingDeletion == 0 && deletionCount >= store.recordLimit() {
			return errPRWorkspaceCheckpointCapacity
		}
		result, err := conn.ExecContext(context.Background(),
			`DELETE FROM candidate_checkpoints WHERE workspace_id = ? AND row_version = ?`,
			workspaceID, sequence,
		)
		if err != nil {
			return fmt.Errorf("remove PR workspace candidate checkpoint: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errPRWorkspaceCandidateCheckpointConflict
		}
		deletionSequence, allocateErr := allocatePRWorkspaceCheckpointRevision(
			context.Background(), conn,
		)
		if allocateErr != nil {
			return allocateErr
		}
		if _, execErr := conn.ExecContext(context.Background(), `INSERT INTO checkpoint_deletions(
		    workspace_id, deleted_row_version, deleted_state_digest, deletion_sequence
		) VALUES (?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
		    deleted_row_version = excluded.deleted_row_version,
		    deleted_state_digest = excluded.deleted_state_digest,
		    deletion_sequence = excluded.deletion_sequence`,
			workspaceID, expected.sequence, expected.stateDigest[:], deletionSequence,
		); execErr != nil {
			return fmt.Errorf("record PR workspace candidate checkpoint deletion: %w", execErr)
		}
		return nil
	})
}

func (store *prWorkspaceCandidateCheckpointStore) removalMatches(
	workspaceID string,
	expected prWorkspaceCandidateCheckpointRevision,
) (bool, error) {
	if store == nil || !expected.exists ||
		!validPRWorkspaceCheckpointRevision(expected, workspaceID) {
		return false, errors.New("PR workspace candidate checkpoint removal match is invalid")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	database, err := store.open(context.Background())
	if err != nil {
		return false, err
	}
	defer database.Close()
	var matches int
	err = database.QueryRowContext(context.Background(), `SELECT COUNT(*)
	    FROM checkpoint_deletions AS deletion
	    WHERE deletion.workspace_id = ?
	      AND deletion.deleted_row_version = ?
	      AND deletion.deleted_state_digest = ?
	      AND NOT EXISTS (
	          SELECT 1 FROM candidate_checkpoints AS checkpoint
	          WHERE checkpoint.workspace_id = deletion.workspace_id
	      )`, workspaceID, expected.sequence, expected.stateDigest[:]).Scan(&matches)
	if err != nil {
		return false, err
	}
	return matches == 1, nil
}

func (store *prWorkspaceCandidateCheckpointStore) reconcileFinalized(
	checkpoint prWorkspaceCandidateCheckpoint,
	expected prWorkspaceCandidateCheckpointRevision,
) (prWorkspaceCandidateCheckpointRevision, bool, error) {
	if store == nil || checkpoint.State != prWorkspaceCandidateCheckpointParked || checkpoint.Fence == nil ||
		!expected.exists || !validPRWorkspaceCheckpointRevision(expected, checkpoint.WorkspaceID) {
		return prWorkspaceCandidateCheckpointRevision{}, false, errors.New(
			"PR workspace finalized checkpoint reconciliation is invalid",
		)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	database, err := store.open(context.Background())
	if err != nil {
		return prWorkspaceCandidateCheckpointRevision{}, false, err
	}
	defer database.Close()
	var revision prWorkspaceCandidateCheckpointRevision
	matched := false
	err = sqlitestore.Immediate(context.Background(), database, func(conn *sql.Conn) error {
		current, sequence, found, loadErr := loadPRWorkspaceCheckpoint(
			context.Background(), conn, checkpoint.WorkspaceID,
		)
		if loadErr != nil || !found || !equivalentPRWorkspaceCheckpoint(current, checkpoint) {
			return loadErr
		}
		var interveningDeletion int
		if queryErr := conn.QueryRowContext(context.Background(), `SELECT COUNT(*)
		    FROM checkpoint_deletions
		    WHERE workspace_id = ? AND deletion_sequence > ?`,
			checkpoint.WorkspaceID, expected.sequence,
		).Scan(&interveningDeletion); queryErr != nil {
			return queryErr
		}
		if interveningDeletion != 0 {
			return nil
		}
		var revisionErr error
		revision, revisionErr = revisionForPRWorkspaceCheckpoint(
			context.Background(), conn, current, sequence, true, checkpoint.WorkspaceID,
		)
		matched = revisionErr == nil
		return revisionErr
	})
	if err != nil {
		return prWorkspaceCandidateCheckpointRevision{}, false, err
	}
	return revision, matched, nil
}

func allocatePRWorkspaceCheckpointRevision(ctx context.Context, conn *sql.Conn) (int64, error) {
	var revision int64
	err := conn.QueryRowContext(ctx, `UPDATE checkpoint_metadata
	    SET next_revision = next_revision + 1
	    WHERE singleton = 1 AND next_revision < 9223372036854775807
	    RETURNING next_revision - 1`).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("PR workspace checkpoint revision is exhausted")
	}
	if err != nil {
		return 0, err
	}
	if revision <= 0 {
		return 0, errors.New("PR workspace checkpoint revision is invalid")
	}
	return revision, nil
}

func revisionForPRWorkspaceCheckpoint(
	ctx context.Context,
	queryer interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
	checkpoint prWorkspaceCandidateCheckpoint,
	sequence int64,
	found bool,
	workspaceID string,
) (prWorkspaceCandidateCheckpointRevision, error) {
	if !found {
		err := queryer.QueryRowContext(ctx, `SELECT deletion_sequence
		    FROM checkpoint_deletions WHERE workspace_id = ?`, workspaceID).Scan(&sequence)
		if errors.Is(err, sql.ErrNoRows) {
			sequence = 0
		} else if err != nil {
			return prWorkspaceCandidateCheckpointRevision{}, err
		}
		return prWorkspaceCandidateCheckpointRevision{
			workspaceID: workspaceID,
			sequence:    sequence,
		}, nil
	}
	encoded, err := encodePRWorkspaceCheckpoint(checkpoint)
	if err != nil {
		return prWorkspaceCandidateCheckpointRevision{}, err
	}
	return newPRWorkspaceCheckpointRevision(checkpoint, sequence, encoded), nil
}

func newPRWorkspaceCheckpointRevision(
	checkpoint prWorkspaceCandidateCheckpoint,
	sequence int64,
	encoded []byte,
) prWorkspaceCandidateCheckpointRevision {
	digestInput := make([]byte, 0, len(encoded)+48)
	digestInput = append(digestInput, "picoclaw-pr-workspace-checkpoint-revision-v1\x00"...)
	digestInput = append(digestInput, encoded...)
	return prWorkspaceCandidateCheckpointRevision{
		workspaceID: checkpoint.WorkspaceID,
		sequence:    sequence,
		stateDigest: sha256.Sum256(digestInput),
		exists:      true,
	}
}

func validPRWorkspaceCheckpointRevision(
	revision prWorkspaceCandidateCheckpointRevision,
	workspaceID string,
) bool {
	if revision.workspaceID != workspaceID || stringsTrimmed(workspaceID) == "" {
		return false
	}
	if revision.exists {
		return revision.sequence > 0 && revision.stateDigest != [sha256.Size]byte{}
	}
	return revision.sequence >= 0 && revision.stateDigest == [sha256.Size]byte{}
}

func encodePRWorkspaceCheckpoint(checkpoint prWorkspaceCandidateCheckpoint) ([]byte, error) {
	return json.Marshal(checkpoint)
}

func legacyPRWorkspaceCheckpointFilename(workspaceID string) string {
	digest := sha256.Sum256([]byte(workspaceID))
	return hex.EncodeToString(digest[:]) + ".json"
}

const prWorkspaceCheckpointSelectSQL = `SELECT workspace_id, checkpoint_version, state,
    repository, source_ref, head_sha, charter_id, charter_head_sha, git_workspace_id, line_id,
    lease_workspace_id, lease_version, lease_mutation_epoch, lease_tip, lease_tree, lease_already_owned,
    candidate_workspace_id, candidate_parent_commit, candidate_tree, candidate_digest,
    candidate_changed_files, fence_git_workspace_id, fence_line_id, fence_line_version,
    fence_mutation_epoch, fence_park_intent_id, fence_base_commit, fence_tip, fence_tree, row_version
    FROM candidate_checkpoints`

const prWorkspaceCheckpointInsertSQL = `INSERT INTO candidate_checkpoints VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const prWorkspaceCheckpointUpdateSQL = `UPDATE candidate_checkpoints SET
    checkpoint_version = ?, state = ?, repository = ?, source_ref = ?, head_sha = ?, charter_id = ?,
    charter_head_sha = ?, git_workspace_id = ?, line_id = ?, lease_workspace_id = ?, lease_version = ?,
    lease_mutation_epoch = ?, lease_tip = ?, lease_tree = ?, lease_already_owned = ?,
    candidate_workspace_id = ?, candidate_parent_commit = ?, candidate_tree = ?, candidate_digest = ?,
    candidate_changed_files = ?, fence_git_workspace_id = ?, fence_line_id = ?, fence_line_version = ?,
    fence_mutation_epoch = ?, fence_park_intent_id = ?, fence_base_commit = ?, fence_tip = ?, fence_tree = ?,
    row_version = ? WHERE workspace_id = ? AND row_version = ?`

type prWorkspaceCheckpointScanner interface {
	Scan(destinations ...any) error
}

func loadPRWorkspaceCheckpoint(
	ctx context.Context,
	queryer interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
	workspaceID string,
) (prWorkspaceCandidateCheckpoint, int64, bool, error) {
	checkpoint, version, err := scanPRWorkspaceCheckpoint(queryer.QueryRowContext(
		ctx, prWorkspaceCheckpointSelectSQL+` WHERE workspace_id = ?`, workspaceID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return prWorkspaceCandidateCheckpoint{}, 0, false, nil
	}
	return checkpoint, version, err == nil, err
}

func scanPRWorkspaceCheckpoint(scanner prWorkspaceCheckpointScanner) (prWorkspaceCandidateCheckpoint, int64, error) {
	var checkpoint prWorkspaceCandidateCheckpoint
	var alreadyOwned int
	var fenceWorkspace, fenceLine, fenceIntent, fenceBase, fenceTip, fenceTree sql.NullString
	var fenceVersion, fenceEpoch sql.NullInt64
	var rowVersion int64
	err := scanner.Scan(
		&checkpoint.WorkspaceID, &checkpoint.Version, &checkpoint.State, &checkpoint.Repository,
		&checkpoint.SourceRef, &checkpoint.HeadSHA, &checkpoint.CharterID, &checkpoint.CharterHeadSHA,
		&checkpoint.GitWorkspaceID, &checkpoint.LineID, &checkpoint.Lease.WorkspaceID,
		&checkpoint.Lease.Version, &checkpoint.Lease.MutationEpoch, &checkpoint.Lease.Tip,
		&checkpoint.Lease.Tree, &alreadyOwned, &checkpoint.Candidate.WorkspaceID,
		&checkpoint.Candidate.ParentCommit, &checkpoint.Candidate.Tree,
		&checkpoint.Candidate.CandidateDigest, &checkpoint.Candidate.ChangedFiles,
		&fenceWorkspace, &fenceLine, &fenceVersion, &fenceEpoch, &fenceIntent, &fenceBase,
		&fenceTip, &fenceTree, &rowVersion,
	)
	if err != nil {
		return prWorkspaceCandidateCheckpoint{}, 0, err
	}
	checkpoint.Lease.AlreadyOwned = alreadyOwned == 1
	if fenceWorkspace.Valid {
		checkpoint.Fence = &prworkspace.ImplementationPublicationFence{
			GitWorkspaceID: fenceWorkspace.String, LineID: fenceLine.String,
			LineVersion: fenceVersion.Int64, MutationEpoch: fenceEpoch.Int64,
			ParkIntentID: fenceIntent.String, BaseCommit: fenceBase.String,
			Tip: fenceTip.String, Tree: fenceTree.String,
		}
	}
	return checkpoint, rowVersion, nil
}

func prWorkspaceCheckpointArguments(checkpoint prWorkspaceCandidateCheckpoint) []any {
	arguments := []any{
		checkpoint.WorkspaceID, checkpoint.Version, checkpoint.State, checkpoint.Repository,
		checkpoint.SourceRef, checkpoint.HeadSHA, checkpoint.CharterID, checkpoint.CharterHeadSHA,
		checkpoint.GitWorkspaceID, checkpoint.LineID, checkpoint.Lease.WorkspaceID,
		checkpoint.Lease.Version, checkpoint.Lease.MutationEpoch, checkpoint.Lease.Tip,
		checkpoint.Lease.Tree, boolInteger(checkpoint.Lease.AlreadyOwned),
		checkpoint.Candidate.WorkspaceID, checkpoint.Candidate.ParentCommit,
		checkpoint.Candidate.Tree, checkpoint.Candidate.CandidateDigest,
		checkpoint.Candidate.ChangedFiles,
	}
	if checkpoint.Fence == nil {
		return append(arguments, nil, nil, nil, nil, nil, nil, nil, nil)
	}
	return append(arguments,
		checkpoint.Fence.GitWorkspaceID, checkpoint.Fence.LineID, checkpoint.Fence.LineVersion,
		checkpoint.Fence.MutationEpoch, checkpoint.Fence.ParkIntentID, checkpoint.Fence.BaseCommit,
		checkpoint.Fence.Tip, checkpoint.Fence.Tree,
	)
}

func equivalentPRWorkspaceCheckpoint(left, right prWorkspaceCandidateCheckpoint) bool {
	if left.Version != right.Version || left.State != right.State || left.WorkspaceID != right.WorkspaceID ||
		left.Repository != right.Repository || left.SourceRef != right.SourceRef || left.HeadSHA != right.HeadSHA ||
		left.CharterID != right.CharterID || left.CharterHeadSHA != right.CharterHeadSHA ||
		left.GitWorkspaceID != right.GitWorkspaceID || left.LineID != right.LineID ||
		left.Lease != right.Lease || left.Candidate != right.Candidate || (left.Fence == nil) != (right.Fence == nil) {
		return false
	}
	return left.Fence == nil || *left.Fence == *right.Fence
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validPRWorkspaceCandidateCheckpointShape(checkpoint prWorkspaceCandidateCheckpoint) bool {
	if checkpoint.Version != prWorkspaceCandidateCheckpointVersion ||
		(checkpoint.State != prWorkspaceCandidateCheckpointActive && checkpoint.State != prWorkspaceCandidateCheckpointParked) ||
		stringsTrimmed(checkpoint.WorkspaceID) == "" || stringsTrimmed(checkpoint.Repository) == "" ||
		stringsTrimmed(checkpoint.SourceRef) == "" || stringsTrimmed(checkpoint.HeadSHA) == "" ||
		stringsTrimmed(checkpoint.CharterID) == "" || checkpoint.CharterHeadSHA != checkpoint.HeadSHA ||
		stringsTrimmed(checkpoint.GitWorkspaceID) == "" || stringsTrimmed(checkpoint.LineID) == "" ||
		checkpoint.Lease.WorkspaceID != checkpoint.GitWorkspaceID || checkpoint.Lease.MutationEpoch <= 0 ||
		stringsTrimmed(checkpoint.Lease.Tip) == "" || stringsTrimmed(checkpoint.Lease.Tree) == "" ||
		checkpoint.Candidate.WorkspaceID != checkpoint.GitWorkspaceID ||
		checkpoint.Candidate.ParentCommit != checkpoint.Lease.Tip ||
		stringsTrimmed(checkpoint.Candidate.Tree) == "" ||
		stringsTrimmed(checkpoint.Candidate.CandidateDigest) == "" || checkpoint.Candidate.ChangedFiles < 0 {
		return false
	}
	if checkpoint.State == prWorkspaceCandidateCheckpointActive {
		return checkpoint.Fence == nil
	}
	if checkpoint.Fence == nil {
		return false
	}
	fence := checkpoint.Fence
	return fence.GitWorkspaceID == checkpoint.GitWorkspaceID && fence.LineID == checkpoint.LineID &&
		fence.LineVersion == checkpoint.Lease.Version+1 &&
		fence.MutationEpoch == checkpoint.Lease.MutationEpoch && stringsTrimmed(fence.ParkIntentID) != "" &&
		fence.BaseCommit == checkpoint.HeadSHA && stringsTrimmed(fence.Tip) != "" &&
		fence.Tree == checkpoint.Candidate.Tree
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("PR workspace candidate checkpoint contains trailing JSON")
	}
	return fmt.Errorf("decode PR workspace candidate checkpoint trailer: %w", err)
}

func stringsTrimmed(value string) string {
	for len(value) > 0 && (value[0] == ' ' || value[0] == '\t' || value[0] == '\r' || value[0] == '\n') {
		value = value[1:]
	}
	for len(value) > 0 {
		last := value[len(value)-1]
		if last != ' ' && last != '\t' && last != '\r' && last != '\n' {
			break
		}
		value = value[:len(value)-1]
	}
	return value
}
