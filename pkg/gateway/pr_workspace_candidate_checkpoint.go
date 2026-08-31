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

type prWorkspaceCandidateCheckpointStore struct {
	root string
	mu   sync.Mutex
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

func (store *prWorkspaceCandidateCheckpointStore) open(ctx context.Context) (*sql.DB, error) {
	if store == nil {
		return nil, errors.New("PR workspace candidate checkpoint store is unavailable")
	}
	return sqlitestore.Open(ctx, store.databasePath(), sqlitestore.Options{
		Component: prWorkspaceCheckpointComponent,
		Migrations: []sqlitestore.Migration{{Version: 1, Statements: []string{
			prWorkspaceCheckpointsSchema,
			prWorkspaceCheckpointsStateIndexSchema,
		}}},
		Validate: validatePRWorkspaceCheckpointSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:    store.root,
			ArchiveRoot:   filepath.Join(store.root, "legacy-json", prWorkspaceCheckpointArchiveLabel),
			Sources:       store.legacySources,
			Import:        importLegacyPRWorkspaceCheckpoint,
			MaxBytes:      prWorkspaceCandidateCheckpointMaxSize,
			MaxSources:    prWorkspaceCheckpointMaximumRecords,
			MaxTotalBytes: prWorkspaceCheckpointMaximumRecords * prWorkspaceCandidateCheckpointMaxSize,
		},
	})
}

func validatePRWorkspaceCheckpointSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct{ typ, name, ddl string }{
		{"table", "candidate_checkpoints", prWorkspaceCheckpointsSchema},
		{"index", "candidate_checkpoints_state_idx", prWorkspaceCheckpointsStateIndexSchema},
	} {
		if err := sqlitestore.ValidateSchemaObject(ctx, conn, object.typ, object.name, object.ddl); err != nil {
			return err
		}
	}
	if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, "candidate_checkpoints"); err != nil {
		return err
	}
	var unexpected, count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%' AND name NOT IN (
            'candidate_checkpoints', 'candidate_checkpoints_state_idx',
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
	rows, err := conn.QueryContext(ctx, prWorkspaceCheckpointSelectSQL+` ORDER BY workspace_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		checkpoint, _, err := scanPRWorkspaceCheckpoint(rows)
		if err != nil || !validPRWorkspaceCandidateCheckpointShape(checkpoint) {
			return errors.New("PR workspace checkpoint row is invalid")
		}
	}
	return rows.Err()
}

func (store *prWorkspaceCandidateCheckpointStore) legacySources() ([]sqlitestore.LegacySource, error) {
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return nil, err
	}
	if len(entries) > prWorkspaceCheckpointMaximumRecords+4 {
		return nil, errors.New("PR workspace checkpoint directory exceeds its entry limit")
	}
	sources := make([]sqlitestore.LegacySource, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := os.Lstat(filepath.Join(store.root, name))
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("PR workspace checkpoint legacy source is unsafe")
		}
		digest := sha256.Sum256([]byte(name))
		sources = append(sources, sqlitestore.LegacySource{
			ID:       "checkpoint-" + hex.EncodeToString(digest[:16]),
			Relative: name,
			MaxBytes: prWorkspaceCandidateCheckpointMaxSize,
		})
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
	arguments := prWorkspaceCheckpointArguments(checkpoint)
	arguments = append(arguments, 1)
	if _, err := conn.ExecContext(ctx, prWorkspaceCheckpointInsertSQL, arguments...); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	return sqlitestore.ImportResult{Imported: 1}, nil
}

func decodeLegacyPRWorkspaceCheckpoint(data []byte) (prWorkspaceCandidateCheckpoint, error) {
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

func (store *prWorkspaceCandidateCheckpointStore) Save(checkpoint prWorkspaceCandidateCheckpoint) error {
	if store == nil || !validPRWorkspaceCandidateCheckpointShape(checkpoint) {
		return errors.New("PR workspace candidate checkpoint is invalid")
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	if len(encoded) > prWorkspaceCandidateCheckpointMaxSize {
		return errors.New("PR workspace candidate checkpoint is too large")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	database, err := store.open(context.Background())
	if err != nil {
		return err
	}
	defer database.Close()
	return sqlitestore.Immediate(context.Background(), database, func(conn *sql.Conn) error {
		current, version, found, err := loadPRWorkspaceCheckpoint(context.Background(), conn, checkpoint.WorkspaceID)
		if err != nil {
			return err
		}
		arguments := prWorkspaceCheckpointArguments(checkpoint)
		if !found {
			arguments = append(arguments, 1)
			if _, execErr := conn.ExecContext(
				context.Background(),
				prWorkspaceCheckpointInsertSQL,
				arguments...); execErr != nil {
				return fmt.Errorf("write PR workspace candidate checkpoint: %w", execErr)
			}
			return nil
		}
		if equivalentPRWorkspaceCheckpoint(current, checkpoint) {
			return nil
		}
		if current.State == prWorkspaceCandidateCheckpointParked ||
			current.WorkspaceID != checkpoint.WorkspaceID || current.Repository != checkpoint.Repository ||
			current.SourceRef != checkpoint.SourceRef || current.HeadSHA != checkpoint.HeadSHA ||
			current.CharterID != checkpoint.CharterID || current.CharterHeadSHA != checkpoint.CharterHeadSHA ||
			current.GitWorkspaceID != checkpoint.GitWorkspaceID || current.LineID != checkpoint.LineID {
			return errors.New("PR workspace candidate checkpoint conflict")
		}
		arguments = append(arguments[1:], version+1, checkpoint.WorkspaceID, version)
		result, err := conn.ExecContext(context.Background(), prWorkspaceCheckpointUpdateSQL, arguments...)
		if err != nil {
			return fmt.Errorf("write PR workspace candidate checkpoint: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("PR workspace candidate checkpoint conflict")
		}
		return nil
	})
}

func (store *prWorkspaceCandidateCheckpointStore) Load(
	workspaceID string,
) (prWorkspaceCandidateCheckpoint, bool, error) {
	if store == nil || stringsTrimmed(workspaceID) == "" {
		return prWorkspaceCandidateCheckpoint{}, false, errors.New(
			"PR workspace candidate checkpoint lookup is invalid",
		)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	database, err := store.open(context.Background())
	if err != nil {
		return prWorkspaceCandidateCheckpoint{}, false, err
	}
	defer database.Close()
	checkpoint, _, found, err := loadPRWorkspaceCheckpoint(context.Background(), database, workspaceID)
	if err != nil {
		return prWorkspaceCandidateCheckpoint{}, false, err
	}
	if found && (checkpoint.WorkspaceID != workspaceID || !validPRWorkspaceCandidateCheckpointShape(checkpoint)) {
		return prWorkspaceCandidateCheckpoint{}, false, errors.New(
			"PR workspace candidate checkpoint identity is invalid",
		)
	}
	return checkpoint, found, nil
}

func (store *prWorkspaceCandidateCheckpointStore) Remove(workspaceID string) error {
	if store == nil || stringsTrimmed(workspaceID) == "" {
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
		_, version, found, err := loadPRWorkspaceCheckpoint(context.Background(), conn, workspaceID)
		if err != nil || !found {
			return err
		}
		result, err := conn.ExecContext(context.Background(),
			`DELETE FROM candidate_checkpoints WHERE workspace_id = ? AND row_version = ?`, workspaceID, version)
		if err != nil {
			return fmt.Errorf("remove PR workspace candidate checkpoint: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("PR workspace candidate checkpoint conflict")
		}
		return nil
	})
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
