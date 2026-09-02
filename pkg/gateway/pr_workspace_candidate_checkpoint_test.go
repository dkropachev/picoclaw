package gateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

func TestPRWorkspaceCandidateCheckpointStoreRoundTripAndRemove(t *testing.T) {
	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	revision, err := store.Save(checkpoint, absent)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Lstat(store.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if !privatePRWorkspaceCheckpointFile(store.databasePath(), info) {
		t.Fatalf("checkpoint mode = %v", info.Mode())
	}
	loaded, loadedRevision, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found || loaded != checkpoint || loadedRevision != revision {
		t.Fatalf("Load() = %#v, %v, %v", loaded, found, err)
	}
	if err = store.Remove(checkpoint.WorkspaceID, loadedRevision); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	_, absent, found, err = store.Load(checkpoint.WorkspaceID)
	if err != nil || found {
		t.Fatalf("Load(after remove) found = %v, error = %v", found, err)
	}
	if absent.sequence <= loadedRevision.sequence {
		t.Fatalf(
			"deletion did not advance absence horizon: before=%d after=%d",
			loadedRevision.sequence,
			absent.sequence,
		)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var deletedVersion, deletionSequence int64
	var deletedDigest []byte
	if queryErr := database.QueryRow(`SELECT deleted_row_version, deleted_state_digest, deletion_sequence
	    FROM checkpoint_deletions WHERE workspace_id = ?`, checkpoint.WorkspaceID).Scan(
		&deletedVersion, &deletedDigest, &deletionSequence,
	); queryErr != nil {
		_ = database.Close()
		t.Fatal(queryErr)
	}
	if closeErr := database.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if deletedVersion != loadedRevision.sequence ||
		!bytes.Equal(deletedDigest, loadedRevision.stateDigest[:]) || deletionSequence != absent.sequence {
		t.Fatalf(
			"deletion tombstone = version:%d digest:%x sequence:%d",
			deletedVersion,
			deletedDigest,
			deletionSequence,
		)
	}
	if err = store.Remove(checkpoint.WorkspaceID, absent); err != nil {
		t.Fatalf("idempotent Remove() error = %v", err)
	}
}

func TestPRWorkspaceCandidateCheckpointStoreRejectsUnsafeOrMalformedFiles(t *testing.T) {
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	t.Run("unknown field", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "active")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded[:len(encoded)-1], []byte(`,"unexpected":true}`)...)
		legacyPath := filepath.Join(root, legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID))
		if err = os.WriteFile(legacyPath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		store, err := newPRWorkspaceCandidateCheckpointStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, found, loadErr := store.Load(checkpoint.WorkspaceID); loadErr != nil || found {
			t.Fatalf("malformed legacy checkpoint = found %v, error %v", found, loadErr)
		}
		archive := filepath.Join(root, "legacy-json", prWorkspaceCheckpointArchiveLabel, filepath.Base(legacyPath))
		if _, err := os.Stat(archive); err != nil {
			t.Fatalf("malformed examined source was not archived: %v", err)
		}
	})

	t.Run("public mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows FileMode permission bits do not represent ACL privacy")
		}
		root := filepath.Join(t.TempDir(), "active")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(checkpoint)
		path := filepath.Join(root, legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID))
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := newPRWorkspaceCandidateCheckpointStore(root); err == nil {
			t.Fatal("constructor accepted a group/world-readable checkpoint")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "active")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.json")
		encoded, _ := json.Marshal(checkpoint)
		if err := os.WriteFile(target, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(
			target,
			filepath.Join(root, legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID)),
		); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := newPRWorkspaceCandidateCheckpointStore(root); err == nil {
			t.Fatal("constructor accepted a symlink checkpoint")
		}
	})

	t.Run("archive parent symlink without sources", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "active")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "legacy-json")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := newPRWorkspaceCandidateCheckpointStore(root); err == nil ||
			!strings.Contains(err.Error(), "archive ancestor") {
			t.Fatalf("constructor with symlinked empty archive error = %v", err)
		}
	})
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestPRWorkspaceCheckpointDuplicateJSONNameIsAuditedAndArchived(t *testing.T) {
	root := filepath.Join(t.TempDir(), "active")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded[:len(encoded)-1], []byte(`,"VERSION":2}`)...)
	if _, err := decodeLegacyPRWorkspaceCheckpoint(encoded); err == nil ||
		!strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate checkpoint decoder error = %v", err)
	}
	legacy := filepath.Join(root, legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID))
	if err := os.WriteFile(legacy, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newPRWorkspaceCandidateCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "legacy-json", prWorkspaceCheckpointArchiveLabel, filepath.Base(legacy))
	if _, err := os.Lstat(archive); err != nil {
		t.Fatalf("duplicate checkpoint was not archived: %v", err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var skipped, issues int
	if err := database.QueryRow(`SELECT
	    (SELECT skipped_count FROM storage_imports WHERE component = ?),
	    (SELECT COUNT(*) FROM storage_import_issues
	      WHERE component = ? AND issue_code = 'malformed-checkpoint')`,
		prWorkspaceCheckpointComponent,
		prWorkspaceCheckpointComponent,
	).Scan(&skipped, &issues); err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || issues != 1 {
		t.Fatalf("duplicate checkpoint audit = skipped:%d issues:%d", skipped, issues)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestPRWorkspaceCandidateCheckpointSQLiteMigratesArchivesAndReopens(t *testing.T) {
	root := filepath.Join(t.TempDir(), "active")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID))
	if writeErr := os.WriteFile(legacy, encoded, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	store, err := newPRWorkspaceCandidateCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found || !equivalentPRWorkspaceCheckpoint(loaded, checkpoint) {
		t.Fatalf("migrated checkpoint = %#v, found=%v err=%v", loaded, found, err)
	}
	archive := filepath.Join(root, "legacy-json", prWorkspaceCheckpointArchiveLabel, filepath.Base(legacy))
	if _, statErr := os.Stat(legacy); !os.IsNotExist(statErr) {
		t.Fatalf("legacy checkpoint remains: %v", statErr)
	}
	if info, statErr := os.Stat(archive); statErr != nil || !privatePRWorkspaceCheckpointFile(archive, info) {
		t.Fatalf("legacy checkpoint archive = %#v, %v", info, statErr)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var imported, skipped int
	var status string
	if err := database.QueryRow(`SELECT imported_count, skipped_count, archive_status
        FROM storage_imports WHERE component = ?`, prWorkspaceCheckpointComponent).Scan(
		&imported, &skipped, &status,
	); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()
	if imported != 1 || skipped != 0 || status != "complete" {
		t.Fatalf("checkpoint import ledger = %d/%d/%q", imported, skipped, status)
	}
	if _, err := newPRWorkspaceCandidateCheckpointStore(root); err != nil {
		t.Fatalf("idempotent checkpoint reopen: %v", err)
	}
	if err := os.Rename(archive, legacy); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", store.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE storage_imports
	    SET archive_status = 'pending', archived_at = NULL
	    WHERE component = ?`, prWorkspaceCheckpointComponent); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := newPRWorkspaceCandidateCheckpointStore(root); err != nil {
		t.Fatalf("checkpoint archive retry with closed import horizon: %v", err)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("checkpoint archive retry retained source: %v", err)
	}
	if _, err := os.Lstat(archive); err != nil {
		t.Fatalf("checkpoint archive retry lost destination: %v", err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestPRWorkspaceCheckpointEmptyFirstOpenRejectsLateLegacySource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "active")
	store, err := newPRWorkspaceCandidateCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID))
	if err := os.WriteFile(legacy, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := newPRWorkspaceCandidateCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := reopened.Load(checkpoint.WorkspaceID); err != nil || found {
		t.Fatalf("late legacy checkpoint = found:%v error:%v", found, err)
	}
	archive := filepath.Join(
		root, "legacy-json", prWorkspaceCheckpointArchiveLabel, filepath.Base(legacy),
	)
	if _, err := os.Lstat(archive); err != nil {
		t.Fatalf("late legacy checkpoint was not archived: %v", err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var skipped, issues int
	if err := database.QueryRow(`SELECT
	    (SELECT skipped_count FROM storage_imports WHERE component = ?),
	    (SELECT COUNT(*) FROM storage_import_issues
	      WHERE component = ? AND issue_code = 'late-source')`,
		prWorkspaceCheckpointComponent,
		prWorkspaceCheckpointComponent,
	).Scan(&skipped, &issues); err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || issues != 1 {
		t.Fatalf("late checkpoint audit = skipped:%d issues:%d", skipped, issues)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestPRWorkspaceCheckpointSchemaV1UpgradeClosesImportAndSeedsRevision(t *testing.T) {
	root := filepath.Join(t.TempDir(), "active")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, prWorkspaceCheckpointDatabaseFilename)
	v1, err := sqlitestore.Open(t.Context(), path, sqlitestore.Options{
		Component: prWorkspaceCheckpointComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				prWorkspaceCheckpointsSchema,
				prWorkspaceCheckpointsStateIndexSchema,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	arguments := append(prWorkspaceCheckpointArguments(checkpoint), int64(41))
	if _, err := v1.Exec(prWorkspaceCheckpointInsertSQL, arguments...); err != nil {
		t.Fatal(err)
	}
	if err := v1.Close(); err != nil {
		t.Fatal(err)
	}
	late := checkpoint
	late.WorkspaceID = "devw-late-v1-checkpoint"
	encoded, err := json.Marshal(late)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, legacyPRWorkspaceCheckpointFilename(late.WorkspaceID))
	if err := os.WriteFile(legacy, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newPRWorkspaceCandidateCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, revision, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found || loaded != checkpoint || revision.sequence != 41 {
		t.Fatalf("upgraded v1 checkpoint = %#v, revision:%#v found:%v error:%v", loaded, revision, found, err)
	}
	if _, _, found, err := store.Load(late.WorkspaceID); err != nil || found {
		t.Fatalf("late v1 checkpoint = found:%v error:%v", found, err)
	}
	loaded.Candidate.CandidateDigest = strings.Repeat("8", 64)
	next, err := store.Save(loaded, revision)
	if err != nil || next.sequence != 42 {
		t.Fatalf("first upgraded revision = %#v, %v", next, err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var userVersion int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		t.Fatal(err)
	}
	if userVersion != 3 {
		t.Fatalf("upgraded checkpoint user_version = %d", userVersion)
	}
}

func TestPRWorkspaceCheckpointMissingImportHorizonFailsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "active")
	store, err := newPRWorkspaceCandidateCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", store.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM checkpoint_legacy_import_state`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := newPRWorkspaceCandidateCheckpointStore(root); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
		t.Fatalf("missing checkpoint import horizon error = %v", err)
	}
}

func TestPRWorkspaceCandidateCheckpointSQLiteCASAndSchemaFences(t *testing.T) {
	root := filepath.Join(t.TempDir(), "active")
	store, err := newPRWorkspaceCandidateCheckpointStore(root)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	revision := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	revision, saveErr := store.Save(checkpoint, revision)
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	parked := checkpoint
	parked.State = prWorkspaceCandidateCheckpointParked
	parked.Fence = &prworkspace.ImplementationPublicationFence{
		GitWorkspaceID: checkpoint.GitWorkspaceID, LineID: checkpoint.LineID,
		LineVersion: checkpoint.Lease.Version + 1, MutationEpoch: checkpoint.Lease.MutationEpoch,
		ParkIntentID: "park-checkpoint", BaseCommit: checkpoint.HeadSHA,
		Tip: "5555555555555555555555555555555555555555", Tree: checkpoint.Candidate.Tree,
	}
	revision, saveErr = store.Save(parked, revision)
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	conflict := parked
	conflict.Candidate.CandidateDigest = strings.Repeat("6", 64)
	if _, saveErr := store.Save(conflict, revision); saveErr == nil || !strings.Contains(saveErr.Error(), "conflict") {
		t.Fatalf("parked checkpoint overwrite = %v", saveErr)
	}

	raw, err := sql.Open("sqlite", store.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	if _, err := newPRWorkspaceCandidateCheckpointStore(root); !errors.Is(err, sqlitestore.ErrTooNew) {
		t.Fatalf("too-new checkpoint schema = %v", err)
	}
}

//nolint:govet // Parent and subprocess assertions intentionally use independent error scopes.
func TestPRWorkspaceCheckpointStaleRevisionIsFencedAcrossProcesses(t *testing.T) {
	if os.Getenv("PICOCLAW_CHECKPOINT_CAS_HELPER") == "1" {
		root := os.Getenv("PICOCLAW_CHECKPOINT_CAS_ROOT")
		ready := os.Getenv("PICOCLAW_CHECKPOINT_CAS_READY")
		release := os.Getenv("PICOCLAW_CHECKPOINT_CAS_RELEASE")
		operation := os.Getenv("PICOCLAW_CHECKPOINT_CAS_OPERATION")
		store, err := newPRWorkspaceCandidateCheckpointStore(root)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := prWorkspaceCandidateCheckpointFixture()
		loaded, revision, found, err := store.Load(checkpoint.WorkspaceID)
		if err != nil || found != (operation != "resurrect") {
			t.Fatalf("helper checkpoint load = found:%v error:%v", found, err)
		}
		if found {
			checkpoint = loaded
		}
		if err := os.WriteFile(ready, []byte("ready\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForPRWorkspaceCheckpointProcessFile(t, release)
		switch operation {
		case "overwrite":
			checkpoint.Candidate.CandidateDigest = strings.Repeat("7", 64)
			_, err = store.Save(checkpoint, revision)
		case "delete":
			err = store.Remove(checkpoint.WorkspaceID, revision)
		case "resurrect":
			_, err = store.Save(checkpoint, revision)
		default:
			t.Fatalf("unknown helper operation %q", operation)
		}
		if !errors.Is(err, errPRWorkspaceCandidateCheckpointConflict) {
			t.Fatalf("stale helper %s error = %v", operation, err)
		}
		return
	}

	for _, operation := range []string{"overwrite", "delete", "resurrect"} {
		t.Run(operation, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "active")
			store, err := newPRWorkspaceCandidateCheckpointStore(root)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := prWorkspaceCandidateCheckpointFixture()
			if operation != "resurrect" {
				absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
				if _, err := store.Save(checkpoint, absent); err != nil {
					t.Fatal(err)
				}
			}

			ready := filepath.Join(t.TempDir(), "ready")
			release := filepath.Join(t.TempDir(), "release")
			var output bytes.Buffer
			command := exec.Command(
				os.Args[0],
				"-test.run=^TestPRWorkspaceCheckpointStaleRevisionIsFencedAcrossProcesses$",
			)
			command.Env = append(
				os.Environ(),
				"PICOCLAW_CHECKPOINT_CAS_HELPER=1",
				"PICOCLAW_CHECKPOINT_CAS_ROOT="+root,
				"PICOCLAW_CHECKPOINT_CAS_READY="+ready,
				"PICOCLAW_CHECKPOINT_CAS_RELEASE="+release,
				"PICOCLAW_CHECKPOINT_CAS_OPERATION="+operation,
			)
			command.Stdout = &output
			command.Stderr = &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if command.Process != nil {
					_ = command.Process.Kill()
				}
			})
			waitForPRWorkspaceCheckpointProcessFile(t, ready)

			if operation == "resurrect" {
				absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
				createdRevision, saveErr := store.Save(checkpoint, absent)
				if saveErr != nil {
					t.Fatal(saveErr)
				}
				if err := store.Remove(checkpoint.WorkspaceID, createdRevision); err != nil {
					t.Fatal(err)
				}
			} else {
				current, revision, found, loadErr := store.Load(checkpoint.WorkspaceID)
				if loadErr != nil || !found {
					t.Fatalf("parent checkpoint load = found:%v error:%v", found, loadErr)
				}
				current.Candidate.CandidateDigest = strings.Repeat("6", 64)
				if operation == "delete" {
					if err := store.Remove(current.WorkspaceID, revision); err != nil {
						t.Fatal(err)
					}
					revision = requireAbsentPRWorkspaceCheckpointRevision(t, store, current.WorkspaceID)
				}
				if _, err := store.Save(current, revision); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("stale %s helper failed: %v\n%s", operation, err, output.String())
			}
			retained, _, found, err := store.Load(checkpoint.WorkspaceID)
			if operation == "resurrect" {
				if err != nil || found {
					t.Fatalf("checkpoint resurrected after stale absence: %#v, found:%v error:%v", retained, found, err)
				}
			} else if err != nil || !found || retained.Candidate.CandidateDigest != strings.Repeat("6", 64) {
				t.Fatalf("retained checkpoint after stale %s = %#v, found:%v error:%v", operation, retained, found, err)
			}
		})
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestPRWorkspaceCheckpointCapacityGuardDoesNotWedgeStore(t *testing.T) {
	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	if _, err := store.Save(checkpoint, absent); err != nil {
		t.Fatal(err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(t.Context(), `WITH RECURSIVE sequence(value) AS (
		    SELECT 2 UNION ALL SELECT value + 1 FROM sequence WHERE value < 10000
		) INSERT INTO candidate_checkpoints
		SELECT printf('devw-capacity-%05d', value), checkpoint_version, state,
		    repository, source_ref, head_sha, charter_id, charter_head_sha, git_workspace_id, line_id,
		    lease_workspace_id, lease_version, lease_mutation_epoch, lease_tip, lease_tree,
		    lease_already_owned, candidate_workspace_id, candidate_parent_commit, candidate_tree,
		    candidate_digest, candidate_changed_files, fence_git_workspace_id, fence_line_id,
		    fence_line_version, fence_mutation_epoch, fence_park_intent_id, fence_base_commit,
		    fence_tip, fence_tree, value
		FROM candidate_checkpoints CROSS JOIN sequence
		WHERE workspace_id = ?`, checkpoint.WorkspaceID)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(t.Context(), `UPDATE checkpoint_metadata
		    SET next_revision = 10001 WHERE singleton = 1`)
		return err
	})
	if closeErr := database.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	newCheckpoint := checkpoint
	newCheckpoint.WorkspaceID = "devw-capacity-new"
	newAbsent := requireAbsentPRWorkspaceCheckpointRevision(t, store, newCheckpoint.WorkspaceID)
	if _, err := store.Save(newCheckpoint, newAbsent); !errors.Is(err, errPRWorkspaceCheckpointCapacity) {
		t.Fatalf("10,001st checkpoint save error = %v", err)
	}
	current, revision, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found {
		t.Fatalf("full-store checkpoint load = found:%v error:%v", found, err)
	}
	current.Candidate.CandidateDigest = strings.Repeat("6", 64)
	if _, err := store.Save(current, revision); err != nil {
		t.Fatalf("full-store checkpoint update: %v", err)
	}
	removedID := "devw-capacity-00002"
	_, removedRevision, found, err := store.Load(removedID)
	if err != nil || !found {
		t.Fatalf("capacity checkpoint load = found:%v error:%v", found, err)
	}
	if err := store.Remove(removedID, removedRevision); err != nil {
		t.Fatal(err)
	}
	newAbsent = requireAbsentPRWorkspaceCheckpointRevision(t, store, newCheckpoint.WorkspaceID)
	if _, err := store.Save(newCheckpoint, newAbsent); err != nil {
		t.Fatalf("checkpoint save after freeing capacity: %v", err)
	}
}

func TestPRWorkspaceCheckpointAbsenceRevisionsAreWorkspaceScoped(t *testing.T) {
	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	checkpointA := prWorkspaceCandidateCheckpointFixture()
	checkpointA.WorkspaceID = "devw-absence-a"
	checkpointB := checkpointA
	checkpointB.WorkspaceID = "devw-absence-b"
	absentA := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpointA.WorkspaceID)
	absentB := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpointB.WorkspaceID)
	revisionB, err := store.Save(checkpointB, absentB)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(checkpointB.WorkspaceID, revisionB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(checkpointA, absentA); err != nil {
		t.Fatalf("unrelated checkpoint invalidated absence revision: %v", err)
	}
}

func TestPRWorkspaceCheckpointDeletionHistoryCapacityIsBounded(t *testing.T) {
	t.Run("new identity removal fails without deleting", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		store.maximumRecords = 1
		checkpointA := prWorkspaceCandidateCheckpointFixture()
		checkpointA.WorkspaceID = "devw-deletion-cap-a"
		revisionA, err := store.Save(
			checkpointA,
			requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpointA.WorkspaceID),
		)
		if err != nil {
			t.Fatal(err)
		}
		if removeErr := store.Remove(checkpointA.WorkspaceID, revisionA); removeErr != nil {
			t.Fatal(removeErr)
		}
		checkpointB := checkpointA
		checkpointB.WorkspaceID = "devw-deletion-cap-b"
		revisionB, err := store.Save(
			checkpointB,
			requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpointB.WorkspaceID),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remove(checkpointB.WorkspaceID, revisionB); !errors.Is(
			err,
			errPRWorkspaceCheckpointCapacity,
		) {
			t.Fatalf("new deletion at history capacity error = %v", err)
		}
		if _, _, found, err := store.Load(checkpointB.WorkspaceID); err != nil || !found {
			t.Fatalf("capacity-rejected deletion removed checkpoint: found:%v error:%v", found, err)
		}
	})

	t.Run("existing identity tombstone updates", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		store.maximumRecords = 1
		checkpoint := prWorkspaceCandidateCheckpointFixture()
		revision, err := store.Save(
			checkpoint,
			requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID),
		)
		if err != nil {
			t.Fatal(err)
		}
		if removeErr := store.Remove(checkpoint.WorkspaceID, revision); removeErr != nil {
			t.Fatal(removeErr)
		}
		revision, err = store.Save(
			checkpoint,
			requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Remove(checkpoint.WorkspaceID, revision); err != nil {
			t.Fatalf("existing deletion tombstone update at capacity: %v", err)
		}
	})
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestPRWorkspaceRuntimeCarriesCheckpointRevisionAcrossSaves(t *testing.T) {
	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	candidate := prWorkspaceCandidate{
		pin: gitworkspace.PinnedAcquireRequest{
			Repository: checkpoint.Repository, SourceRef: checkpoint.SourceRef,
			ExpectedCommit: checkpoint.HeadSHA, ReservationKey: "pr-workspace:" + checkpoint.WorkspaceID,
		},
		candidate: checkpoint.Candidate,
		charter: prworkspace.Charter{
			ID: checkpoint.CharterID, HeadSHA: checkpoint.CharterHeadSHA,
		},
		lineID: checkpoint.LineID,
		lease:  checkpoint.Lease,
		checkpointRevision: requireAbsentPRWorkspaceCheckpointRevision(
			t, store, checkpoint.WorkspaceID,
		),
	}
	runtime := &prWorkspaceImplementationRuntime{checkpoints: store}
	if err := runtime.saveCandidateCheckpoint(checkpoint.WorkspaceID, &candidate); err != nil {
		t.Fatal(err)
	}
	if !candidate.checkpointRevision.exists {
		t.Fatal("runtime did not retain the committed checkpoint revision")
	}
	stale := candidate
	current, revision, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found {
		t.Fatalf("runtime checkpoint load = found:%v error:%v", found, err)
	}
	current.Candidate.CandidateDigest = strings.Repeat("6", 64)
	if _, err := store.Save(current, revision); err != nil {
		t.Fatal(err)
	}
	if err := runtime.saveCandidateCheckpoint(checkpoint.WorkspaceID, &stale); !errors.Is(
		err,
		errPRWorkspaceCandidateCheckpointConflict,
	) {
		t.Fatalf("runtime stale checkpoint save error = %v", err)
	}
	current, currentRevision, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found {
		t.Fatalf("updated runtime checkpoint load = found:%v error:%v", found, err)
	}
	if err := store.Remove(checkpoint.WorkspaceID, currentRevision); err != nil {
		t.Fatal(err)
	}
	absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	original := prWorkspaceCandidateCheckpointFixture()
	if _, err := store.Save(original, absent); err != nil {
		t.Fatal(err)
	}
	if current.Candidate.CandidateDigest == original.Candidate.CandidateDigest {
		t.Fatal("runtime ABA fixture did not change state before replacement")
	}
	if err := runtime.saveCandidateCheckpoint(checkpoint.WorkspaceID, &stale); !errors.Is(
		err,
		errPRWorkspaceCandidateCheckpointConflict,
	) {
		t.Fatalf("runtime accepted exact-payload ABA replacement: %v", err)
	}
}

//nolint:govet // Narrow replay assertions intentionally use independent error scopes.
func TestPRWorkspaceRuntimeReconcilesExactFinalizationAndConcurrentAcknowledgement(t *testing.T) {
	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	candidate := prWorkspaceCandidate{
		pin: gitworkspace.PinnedAcquireRequest{
			Repository: checkpoint.Repository, SourceRef: checkpoint.SourceRef,
			ExpectedCommit: checkpoint.HeadSHA, ReservationKey: "pr-workspace:" + checkpoint.WorkspaceID,
		},
		candidate: checkpoint.Candidate,
		charter: prworkspace.Charter{
			ID: checkpoint.CharterID, HeadSHA: checkpoint.CharterHeadSHA,
		},
		lineID: checkpoint.LineID,
		lease:  checkpoint.Lease,
		parked: &gitworkspace.PinnedLineParkResult{},
		checkpointRevision: requireAbsentPRWorkspaceCheckpointRevision(
			t, store, checkpoint.WorkspaceID,
		),
	}
	runtime := &prWorkspaceImplementationRuntime{checkpoints: store}
	if err := runtime.saveCandidateCheckpoint(checkpoint.WorkspaceID, &candidate); err != nil {
		t.Fatal(err)
	}
	stale := candidate
	winner := candidate
	fence := &prworkspace.ImplementationPublicationFence{
		GitWorkspaceID: checkpoint.GitWorkspaceID, LineID: checkpoint.LineID,
		LineVersion: checkpoint.Lease.Version + 1, MutationEpoch: checkpoint.Lease.MutationEpoch,
		ParkIntentID: "park-checkpoint", BaseCommit: checkpoint.HeadSHA,
		Tip: "5555555555555555555555555555555555555555", Tree: checkpoint.Candidate.Tree,
	}
	if err := runtime.saveFinalizedCandidateCheckpoint(
		checkpoint.WorkspaceID, &winner, fence,
	); err != nil {
		t.Fatal(err)
	}
	if err := runtime.saveFinalizedCandidateCheckpoint(
		checkpoint.WorkspaceID, &stale, fence,
	); err != nil {
		t.Fatalf("exact concurrent finalization did not reconcile: %v", err)
	}
	if stale.checkpointRevision != winner.checkpointRevision {
		t.Fatalf(
			"reconciled finalization revision = %#v, want %#v",
			stale.checkpointRevision,
			winner.checkpointRevision,
		)
	}

	loaded, revision, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found || loaded.State != prWorkspaceCandidateCheckpointParked {
		t.Fatalf("finalized checkpoint = %#v, found:%v error:%v", loaded, found, err)
	}
	if err := store.Remove(checkpoint.WorkspaceID, revision); err != nil {
		t.Fatal(err)
	}
	if err := runtime.removeAcknowledgedCandidateCheckpoint(
		checkpoint.WorkspaceID, revision,
	); err != nil {
		t.Fatalf("concurrent acknowledgement did not reconcile absence: %v", err)
	}

	absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	replacementRevision, err := store.Save(loaded, absent)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.removeAcknowledgedCandidateCheckpoint(
		checkpoint.WorkspaceID, revision,
	); !errors.Is(err, errPRWorkspaceCandidateCheckpointConflict) {
		t.Fatalf("stale acknowledgement accepted replacement revision %#v: %v", replacementRevision, err)
	}
}

//nolint:govet // Parent and subprocess assertions intentionally use independent error scopes.
func TestPRWorkspaceCheckpointIdempotentTerminalReplayAcrossProcesses(t *testing.T) {
	if os.Getenv("PICOCLAW_CHECKPOINT_REPLAY_HELPER") == "1" {
		store, err := newPRWorkspaceCandidateCheckpointStore(
			os.Getenv("PICOCLAW_CHECKPOINT_REPLAY_ROOT"),
		)
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, revision, found, err := store.Load(
			prWorkspaceCandidateCheckpointFixture().WorkspaceID,
		)
		if err != nil || !found {
			t.Fatalf("terminal replay helper load = found:%v error:%v", found, err)
		}
		ready := os.Getenv("PICOCLAW_CHECKPOINT_REPLAY_READY")
		release := os.Getenv("PICOCLAW_CHECKPOINT_REPLAY_RELEASE")
		if err := os.WriteFile(ready, []byte("ready\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitForPRWorkspaceCheckpointProcessFile(t, release)
		runtime := &prWorkspaceImplementationRuntime{checkpoints: store}
		operation := os.Getenv("PICOCLAW_CHECKPOINT_REPLAY_OPERATION")
		switch operation {
		case "finalize", "finalize_aba":
			candidate := prWorkspaceCandidateFromCheckpoint(checkpoint, revision)
			candidate.parked = &gitworkspace.PinnedLineParkResult{}
			_, fence := parkedPRWorkspaceCheckpoint(checkpoint)
			err = runtime.saveFinalizedCandidateCheckpoint(
				checkpoint.WorkspaceID, &candidate, fence,
			)
		case "acknowledge":
			err = runtime.removeAcknowledgedCandidateCheckpoint(checkpoint.WorkspaceID, revision)
		default:
			t.Fatal("terminal replay helper operation is invalid")
		}
		if operation == "finalize_aba" {
			if !errors.Is(err, errPRWorkspaceCandidateCheckpointConflict) {
				t.Fatalf("terminal ABA replay helper error = %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("terminal replay helper error = %v", err)
		}
		return
	}

	for _, operation := range []string{"finalize", "finalize_aba", "acknowledge"} {
		t.Run(operation, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "active")
			store, err := newPRWorkspaceCandidateCheckpointStore(root)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := prWorkspaceCandidateCheckpointFixture()
			absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
			revision, err := store.Save(checkpoint, absent)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "acknowledge" {
				checkpoint, _ = parkedPRWorkspaceCheckpoint(checkpoint)
				revision, err = store.Save(checkpoint, revision)
				if err != nil {
					t.Fatal(err)
				}
			}

			ready := filepath.Join(t.TempDir(), "ready")
			release := filepath.Join(t.TempDir(), "release")
			var output bytes.Buffer
			command := exec.Command(
				os.Args[0],
				"-test.run=^TestPRWorkspaceCheckpointIdempotentTerminalReplayAcrossProcesses$",
			)
			command.Env = append(
				os.Environ(),
				"PICOCLAW_CHECKPOINT_REPLAY_HELPER=1",
				"PICOCLAW_CHECKPOINT_REPLAY_ROOT="+root,
				"PICOCLAW_CHECKPOINT_REPLAY_READY="+ready,
				"PICOCLAW_CHECKPOINT_REPLAY_RELEASE="+release,
				"PICOCLAW_CHECKPOINT_REPLAY_OPERATION="+operation,
			)
			command.Stdout = &output
			command.Stderr = &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if command.Process != nil {
					_ = command.Process.Kill()
				}
			})
			waitForPRWorkspaceCheckpointProcessFile(t, ready)
			if operation == "finalize" {
				checkpoint, _ = parkedPRWorkspaceCheckpoint(checkpoint)
				if _, err := store.Save(checkpoint, revision); err != nil {
					t.Fatal(err)
				}
			} else if operation == "finalize_aba" {
				parked, _ := parkedPRWorkspaceCheckpoint(checkpoint)
				parkedRevision, saveErr := store.Save(parked, revision)
				if saveErr != nil {
					t.Fatal(saveErr)
				}
				if err := store.Remove(checkpoint.WorkspaceID, parkedRevision); err != nil {
					t.Fatal(err)
				}
				absent = requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
				recreatedRevision, saveErr := store.Save(checkpoint, absent)
				if saveErr != nil {
					t.Fatal(saveErr)
				}
				checkpoint, _ = parkedPRWorkspaceCheckpoint(checkpoint)
				if _, saveErr := store.Save(checkpoint, recreatedRevision); saveErr != nil {
					t.Fatal(saveErr)
				}
			} else if err := store.Remove(checkpoint.WorkspaceID, revision); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := command.Wait(); err != nil {
				t.Fatalf("terminal replay helper failed: %v\n%s", err, output.String())
			}
			loaded, _, found, err := store.Load(checkpoint.WorkspaceID)
			if operation == "finalize" || operation == "finalize_aba" {
				if err != nil || !found || !equivalentPRWorkspaceCheckpoint(loaded, checkpoint) {
					t.Fatalf("terminal finalization replay = %#v, found:%v error:%v", loaded, found, err)
				}
			} else if err != nil || found {
				t.Fatalf("terminal acknowledgement replay = found:%v error:%v", found, err)
			}
		})
	}
}

func prWorkspaceCandidateFromCheckpoint(
	checkpoint prWorkspaceCandidateCheckpoint,
	revision prWorkspaceCandidateCheckpointRevision,
) prWorkspaceCandidate {
	return prWorkspaceCandidate{
		pin: gitworkspace.PinnedAcquireRequest{
			Repository: checkpoint.Repository, SourceRef: checkpoint.SourceRef,
			ExpectedCommit: checkpoint.HeadSHA, ReservationKey: "pr-workspace:" + checkpoint.WorkspaceID,
		},
		candidate: checkpoint.Candidate,
		charter: prworkspace.Charter{
			ID: checkpoint.CharterID, HeadSHA: checkpoint.CharterHeadSHA,
		},
		lineID: checkpoint.LineID, lease: checkpoint.Lease, checkpointRevision: revision,
	}
}

func parkedPRWorkspaceCheckpoint(
	checkpoint prWorkspaceCandidateCheckpoint,
) (prWorkspaceCandidateCheckpoint, *prworkspace.ImplementationPublicationFence) {
	fence := &prworkspace.ImplementationPublicationFence{
		GitWorkspaceID: checkpoint.GitWorkspaceID, LineID: checkpoint.LineID,
		LineVersion: checkpoint.Lease.Version + 1, MutationEpoch: checkpoint.Lease.MutationEpoch,
		ParkIntentID: "park-checkpoint", BaseCommit: checkpoint.HeadSHA,
		Tip: "5555555555555555555555555555555555555555", Tree: checkpoint.Candidate.Tree,
	}
	checkpoint.State = prWorkspaceCandidateCheckpointParked
	checkpoint.Fence = fence
	return checkpoint, fence
}

func waitForPRWorkspaceCheckpointProcessFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for checkpoint process file %s", filepath.Base(path))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPRWorkspaceCandidateCheckpointSQLiteInputAndNilBoundaries(t *testing.T) {
	for _, root := range []string{"", "relative/checkpoints"} {
		if store, err := newPRWorkspaceCandidateCheckpointStore(root); err == nil || store != nil {
			t.Fatalf("constructor root %q = %#v, %v", root, store, err)
		}
	}
	fileRoot := filepath.Join(t.TempDir(), "checkpoints-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := newPRWorkspaceCandidateCheckpointStore(fileRoot); err == nil || store != nil {
		t.Fatalf("constructor file root = %#v, %v", store, err)
	}

	checkpoint := prWorkspaceCandidateCheckpointFixture()
	var nilStore *prWorkspaceCandidateCheckpointStore
	if database, err := nilStore.open(t.Context()); err == nil || database != nil {
		t.Fatalf("nil store open = %#v, %v", database, err)
	}
	if _, err := nilStore.Save(checkpoint, prWorkspaceCandidateCheckpointRevision{}); err == nil {
		t.Fatal("nil store Save succeeded")
	}
	if _, _, found, err := nilStore.Load(checkpoint.WorkspaceID); err == nil || found {
		t.Fatalf("nil store Load = found %v, error %v", found, err)
	}
	if err := nilStore.Remove(checkpoint.WorkspaceID, prWorkspaceCandidateCheckpointRevision{}); err == nil {
		t.Fatal("nil store Remove succeeded")
	}

	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	for _, workspaceID := range []string{"", " \t\r\n "} {
		if _, _, found, loadErr := store.Load(workspaceID); loadErr == nil || found {
			t.Fatalf("Load(%q) = found %v, error %v", workspaceID, found, loadErr)
		}
		if removeErr := store.Remove(workspaceID, prWorkspaceCandidateCheckpointRevision{}); removeErr == nil {
			t.Fatalf("Remove(%q) succeeded", workspaceID)
		}
	}
	absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	oversized := checkpoint
	oversized.Repository = strings.Repeat("r", prWorkspaceCandidateCheckpointMaxSize)
	if _, saveErr := store.Save(oversized, absent); saveErr == nil || !strings.Contains(saveErr.Error(), "too large") {
		t.Fatalf("oversized Save error = %v", saveErr)
	}
	invalid := checkpoint
	invalid.Fence = &prworkspace.ImplementationPublicationFence{}
	if _, saveErr := store.Save(invalid, absent); saveErr == nil {
		t.Fatal("active checkpoint with fence was accepted")
	}
	invalid = checkpoint
	invalid.State = prWorkspaceCandidateCheckpointParked
	if _, saveErr := store.Save(invalid, absent); saveErr == nil {
		t.Fatal("parked checkpoint without fence was accepted")
	}

	alreadyOwned := checkpoint
	alreadyOwned.Lease.AlreadyOwned = true
	if _, saveErr := store.Save(alreadyOwned, absent); saveErr != nil {
		t.Fatal(saveErr)
	}
	loaded, _, found, err := store.Load(alreadyOwned.WorkspaceID)
	if err != nil || !found || !loaded.Lease.AlreadyOwned {
		t.Fatalf("already-owned round trip = %#v, found=%v err=%v", loaded, found, err)
	}
	if got := stringsTrimmed(" \t\r\n checkpoint \n\t "); got != "checkpoint" {
		t.Fatalf("stringsTrimmed() = %q", got)
	}
}

func TestPRWorkspaceCandidateCheckpointSQLiteDecoderRejectsTrailers(t *testing.T) {
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for name, suffix := range map[string]string{
		"trailing value":    `{}`,
		"malformed trailer": `{`,
	} {
		t.Run(name, func(t *testing.T) {
			if decoded, decodeErr := decodeLegacyPRWorkspaceCheckpoint(
				append(append([]byte(nil), encoded...), suffix...),
			); decodeErr == nil || decoded != (prWorkspaceCandidateCheckpoint{}) {
				t.Fatalf("decode with %s = %#v, %v", name, decoded, decodeErr)
			}
		})
	}
}

func TestPRWorkspaceCandidateCheckpointSQLiteAuditsConflictingLegacyIdentity(t *testing.T) {
	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	if _, saveErr := store.Save(checkpoint, absent); saveErr != nil {
		t.Fatal(saveErr)
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
		if _, deleteErr := conn.ExecContext(
			t.Context(),
			`DELETE FROM checkpoint_legacy_import_state`,
		); deleteErr != nil {
			return deleteErr
		}
		for _, test := range []struct {
			name      string
			relative  string
			data      []byte
			issueCode string
		}{
			{"malformed", "malformed.json", []byte(`{`), "malformed-checkpoint"},
			{"identity", "wrong-name.json", encoded, "invalid-checkpoint"},
			{"conflict", legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID), encoded, "identity-conflict"},
		} {
			result, importErr := importLegacyPRWorkspaceCheckpoint(t.Context(), conn, sqlitestore.LegacyInput{
				Relative: test.relative,
				Data:     test.data,
			})
			if importErr != nil || result.Skipped != 1 || result.Imported != 0 ||
				len(result.Issues) != 1 || result.Issues[0].Code != test.issueCode {
				return fmt.Errorf("%s import = %#v, %v", test.name, result, importErr)
			}
		}
		_, insertErr := conn.ExecContext(t.Context(), `INSERT INTO checkpoint_legacy_import_state(
		    singleton, import_closed
		) VALUES (1, 1)`)
		return insertErr
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPRWorkspaceCandidateCheckpointSQLiteRejectsSchemaAndRowTampering(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(*testing.T, *sql.DB)
	}{
		{
			name: "missing index",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`DROP INDEX candidate_checkpoints_state_idx`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing deletion index",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`DROP INDEX checkpoint_deletions_sequence_idx`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unexpected object",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`CREATE TABLE checkpoint_injected(value TEXT) STRICT`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra unique index",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`CREATE UNIQUE INDEX checkpoint_unique_tamper
                    ON candidate_checkpoints(charter_id)`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid row",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`UPDATE candidate_checkpoints
                    SET candidate_parent_commit = 'different-parent'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "excess deletion history",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`WITH RECURSIVE sequence(value) AS (
				    SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value <= 10000
				) INSERT INTO checkpoint_deletions(
				    workspace_id, deleted_row_version, deleted_state_digest, deletion_sequence
				) SELECT printf('devw-deleted-%05d', value), 1, zeroblob(32), value + 1
				FROM sequence`); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`UPDATE checkpoint_metadata
				    SET next_revision = 20000 WHERE singleton = 1`); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "active")
			store, err := newPRWorkspaceCandidateCheckpointStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "invalid row" {
				checkpoint := prWorkspaceCandidateCheckpointFixture()
				absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
				if _, saveErr := store.Save(checkpoint, absent); saveErr != nil {
					t.Fatal(saveErr)
				}
			}
			database, err := sql.Open("sqlite", store.databasePath())
			if err != nil {
				t.Fatal(err)
			}
			database.SetMaxOpenConns(1)
			test.tamper(t, database)
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := newPRWorkspaceCandidateCheckpointStore(root); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
				t.Fatalf("tampered checkpoint schema error = %v", err)
			}
		})
	}
}

func TestPRWorkspaceCandidateCheckpointSQLiteLegacyEnumerationBoundaries(t *testing.T) {
	missing := &prWorkspaceCandidateCheckpointStore{root: filepath.Join(t.TempDir(), "missing")}
	if sources, err := missing.legacySources(); err == nil || sources != nil {
		t.Fatalf("missing legacy root = %#v, %v", sources, err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z.json", "a.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &prWorkspaceCandidateCheckpointStore{root: root}
	sources, err := store.legacySources()
	if err != nil || len(sources) != 2 || sources[0].Relative != "a.json" || sources[1].Relative != "z.json" {
		t.Fatalf("sorted legacy sources = %#v, %v", sources, err)
	}
	sources, err = store.legacySourcesBounded(3, 1)
	if err != nil || len(sources) != 2 || sources[0].Relative != "a.json" || sources[1].Relative != "z.json" {
		t.Fatalf("batched exact-bound legacy sources = %#v, %v", sources, err)
	}
	if sources, err = store.legacySourcesBounded(2, 1); err == nil || sources != nil ||
		!strings.Contains(err.Error(), "entry limit") {
		t.Fatalf("one-over legacy enumeration = %#v, %v", sources, err)
	}
	if sources, err = store.legacySourcesBounded(0, 1); err == nil || sources != nil ||
		!strings.Contains(err.Error(), "bounds") {
		t.Fatalf("invalid legacy enumeration bounds = %#v, %v", sources, err)
	}
}

func TestPRWorkspaceCandidateCheckpointSQLiteConstraintFailuresRollback(t *testing.T) {
	t.Run("insert", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := prWorkspaceCandidateCheckpointFixture()
		absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
		checkpoint.Repository = strings.Repeat("r", 4097)
		if _, err := store.Save(checkpoint, absent); err == nil || !strings.Contains(err.Error(), "write") {
			t.Fatalf("constraint insert error = %v", err)
		}
		if _, _, found, err := store.Load(checkpoint.WorkspaceID); err != nil || found {
			t.Fatalf("failed insert remained: found=%v err=%v", found, err)
		}
	})

	t.Run("update", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := prWorkspaceCandidateCheckpointFixture()
		absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
		revision, saveErr := store.Save(checkpoint, absent)
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		invalidUpdate := checkpoint
		invalidUpdate.Candidate.CandidateDigest = "short"
		if _, saveErr := store.Save(
			invalidUpdate,
			revision,
		); saveErr == nil || !strings.Contains(saveErr.Error(), "write") {
			t.Fatalf("constraint update error = %v", saveErr)
		}
		loaded, _, found, err := store.Load(checkpoint.WorkspaceID)
		if err != nil || !found || !equivalentPRWorkspaceCheckpoint(loaded, checkpoint) {
			t.Fatalf("failed update state = %#v, found=%v err=%v", loaded, found, err)
		}
	})
}

func TestPRWorkspaceCandidateCheckpointSQLiteImportDatabaseFailuresRollback(t *testing.T) {
	t.Run("identity query", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := prWorkspaceCandidateCheckpointFixture()
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		database, err := store.open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
			if _, deleteErr := conn.ExecContext(
				t.Context(),
				`DELETE FROM checkpoint_legacy_import_state`,
			); deleteErr != nil {
				return deleteErr
			}
			if _, dropErr := conn.ExecContext(t.Context(), `DROP TABLE candidate_checkpoints`); dropErr != nil {
				return dropErr
			}
			_, importErr := importLegacyPRWorkspaceCheckpoint(t.Context(), conn, sqlitestore.LegacyInput{
				Relative: legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID), Data: encoded,
			})
			return importErr
		})
		if err == nil {
			t.Fatal("legacy import identity query failure committed")
		}
	})

	t.Run("row insert", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := prWorkspaceCandidateCheckpointFixture()
		checkpoint.Repository = strings.Repeat("r", 4097)
		encoded, err := json.Marshal(checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		database, err := store.open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
			if _, deleteErr := conn.ExecContext(
				t.Context(),
				`DELETE FROM checkpoint_legacy_import_state`,
			); deleteErr != nil {
				return deleteErr
			}
			_, importErr := importLegacyPRWorkspaceCheckpoint(t.Context(), conn, sqlitestore.LegacyInput{
				Relative: legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID), Data: encoded,
			})
			return importErr
		})
		if err == nil {
			t.Fatal("legacy import constraint failure committed")
		}
		if _, _, found, err := store.Load(checkpoint.WorkspaceID); err != nil || found {
			t.Fatalf("failed legacy insert remained: found=%v err=%v", found, err)
		}
	})
}

func TestPRWorkspaceCandidateCheckpointSQLiteOperationsFailClosedWhenDatabasePathIsUnsafe(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(*prWorkspaceCandidateCheckpointStore) error
	}{
		{"save", func(store *prWorkspaceCandidateCheckpointStore) error {
			checkpoint := prWorkspaceCandidateCheckpointFixture()
			_, err := store.Save(checkpoint, prWorkspaceCandidateCheckpointRevision{
				workspaceID: checkpoint.WorkspaceID,
				sequence:    1,
			})
			return err
		}},
		{"load", func(store *prWorkspaceCandidateCheckpointStore) error {
			_, _, _, err := store.Load(prWorkspaceCandidateCheckpointFixture().WorkspaceID)
			return err
		}},
		{"remove", func(store *prWorkspaceCandidateCheckpointStore) error {
			checkpoint := prWorkspaceCandidateCheckpointFixture()
			return store.Remove(checkpoint.WorkspaceID, prWorkspaceCandidateCheckpointRevision{
				workspaceID: checkpoint.WorkspaceID,
				sequence:    1,
			})
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(store.databasePath()); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(store.databasePath(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := operation.run(store); err == nil {
				t.Fatal("operation accepted a directory at the database path")
			}
		})
	}
}

func TestPRWorkspaceCandidateCheckpointRestoresExactCandidateAfterManagerRestart(t *testing.T) {
	ctx := context.Background()
	source := initPRWorkspaceRepairTestRepository(t)
	head := runPRWorkspaceRepairGit(t, source, "rev-parse", "HEAD")
	root := t.TempDir()
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := "devw_22222222222222222222222222222222"
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: source, SourceRef: "main", ExpectedCommit: head,
		ReservationKey: "pr-workspace:" + workspaceID, AgentID: "repair-test",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := manager.SnapshotPinnedValidationCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	lineID := stablePRWorkspaceLineID(workspaceID)
	lease, err := manager.AdoptPinnedLine(ctx, gitworkspace.PinnedLineAdoptRequest{
		Pin: pin, WorkspaceID: workspace.ID, LineID: lineID, ExpectedTree: baseline.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	charter := prworkspace.Charter{
		ID: "pchar_22222222222222222222222222222222", HeadSHA: head,
		Confirmed: true, CreatedAt: time.Now().UTC(),
	}
	store, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(manager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	active := prWorkspaceCandidate{
		pin: pin, candidate: baseline, charter: charter, lineID: lineID, lease: lease,
		checkpointRevision: requireAbsentPRWorkspaceCheckpointRevision(t, store, workspaceID),
	}
	runtime := &prWorkspaceImplementationRuntime{
		manager: manager, checkpoints: store,
		candidates: make(map[prWorkspaceCandidateKey]prWorkspaceCandidate),
		active:     map[string]prWorkspaceCandidate{workspaceID: active},
	}
	if err = runtime.saveCandidateCheckpoint(workspaceID, &active); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(workspace.Path, "README.md"),
		[]byte("# retained repair\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err = runtime.capturePartialRepairCandidate(ctx, workspaceID, active); err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.SnapshotPinnedCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}

	restartedManager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	restartedWorkspace, err := restartedManager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	restartedStore, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(restartedManager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &prWorkspaceImplementationRuntime{
		manager:     restartedManager,
		checkpoints: restartedStore,
		candidates: make(
			map[prWorkspaceCandidateKey]prWorkspaceCandidate,
		),
		active: make(map[string]prWorkspaceCandidate),
	}
	restored, found, err := restarted.restoreCheckpointedCandidate(
		ctx, pin, restartedWorkspace.ID, lineID, charter,
	)
	if err != nil || !found || restored.candidate != candidate || restored.lease != lease {
		t.Fatalf("restoreCheckpointedCandidate() = %#v, %v, %v", restored, found, err)
	}
	if _, ok := restarted.candidates[prWorkspaceCandidateKey{workspaceID: workspaceID, tree: candidate.Tree}]; !ok {
		t.Fatal("restored candidate was not available to validation")
	}

	if err = os.WriteFile(
		filepath.Join(workspace.Path, "README.md"),
		[]byte("# out-of-band drift\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	drifted := &prWorkspaceImplementationRuntime{
		manager:     restartedManager,
		checkpoints: restartedStore,
		candidates: make(
			map[prWorkspaceCandidateKey]prWorkspaceCandidate,
		),
		active: make(map[string]prWorkspaceCandidate),
	}
	if _, _, err = drifted.restoreCheckpointedCandidate(ctx, pin, restartedWorkspace.ID, lineID, charter); err == nil {
		t.Fatal("restoreCheckpointedCandidate() accepted content drift")
	}
}

func TestPRWorkspaceFinalizationRetainsCheckpointUntilAggregateAcknowledgement(t *testing.T) {
	ctx := context.Background()
	source := initPRWorkspaceRepairTestRepository(t)
	head := runPRWorkspaceRepairGit(t, source, "rev-parse", "HEAD")
	root := t.TempDir()
	manager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := "devw_44444444444444444444444444444444"
	pin := gitworkspace.PinnedAcquireRequest{
		Repository: source, SourceRef: "main", ExpectedCommit: head,
		ReservationKey: "pr-workspace:" + workspaceID, AgentID: "repair-test",
	}
	workspace, err := manager.AcquirePinned(ctx, pin)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := manager.SnapshotPinnedValidationCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	lineID := stablePRWorkspaceLineID(workspaceID)
	lease, err := manager.AdoptPinnedLine(ctx, gitworkspace.PinnedLineAdoptRequest{
		Pin: pin, WorkspaceID: workspace.ID, LineID: lineID, ExpectedTree: baseline.Tree,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(
		filepath.Join(workspace.Path, "README.md"),
		[]byte("# finalized repair\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	candidate, err := manager.SnapshotPinnedCandidate(ctx, gitworkspace.PinnedCandidateRequest{
		Pin: pin, WorkspaceID: workspace.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	charter := prworkspace.Charter{
		ID: "pchar_44444444444444444444444444444444", HeadSHA: head,
		Confirmed: true, CreatedAt: time.Date(2026, 8, 14, 12, 0, 0, 987654321, time.UTC),
	}
	store, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(manager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	stored := prWorkspaceCandidate{
		pin: pin, candidate: candidate, charter: charter, lineID: lineID, lease: lease,
		checkpointRevision: requireAbsentPRWorkspaceCheckpointRevision(t, store, workspaceID),
	}
	runtime := &prWorkspaceImplementationRuntime{
		manager: manager, checkpoints: store,
		candidates: map[prWorkspaceCandidateKey]prWorkspaceCandidate{
			{workspaceID: workspaceID, tree: candidate.Tree}: stored,
		},
		active: map[string]prWorkspaceCandidate{workspaceID: stored},
	}
	if err = runtime.saveCandidateCheckpoint(workspaceID, &stored); err != nil {
		t.Fatal(err)
	}
	runtime.candidates[prWorkspaceCandidateKey{workspaceID: workspaceID, tree: candidate.Tree}] = stored
	runtime.active[workspaceID] = stored
	repair := prworkspace.RepairResult{
		WorkspaceID: workspace.ID, CandidateSHA: candidate.Tree, Summary: "finalized",
	}
	if _, err = runtime.FinalizeRepair(ctx, "devw_55555555555555555555555555555555", repair); err == nil {
		t.Fatal("FinalizeRepair() accepted another PR workspace identity")
	}
	finalized, err := runtime.FinalizeRepair(ctx, workspaceID, repair)
	if err != nil || finalized.PublicationFence == nil || finalized.CandidateSHA == candidate.Tree {
		t.Fatalf("FinalizeRepair() = %#v, %v", finalized, err)
	}
	checkpoint, _, found, loadErr := store.Load(workspaceID)
	if loadErr != nil || !found || checkpoint.State != prWorkspaceCandidateCheckpointParked || checkpoint.Fence == nil {
		t.Fatalf("checkpoint was removed before aggregate acknowledgement: found=%v err=%v", found, loadErr)
	}
	restartedManager, err := gitworkspace.NewManager(gitworkspace.Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	restartedStore, err := newPRWorkspaceCandidateCheckpointStore(
		filepath.Join(restartedManager.RootDir(), ".pr-workspace-implementation", "active"),
	)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &prWorkspaceImplementationRuntime{manager: restartedManager, checkpoints: restartedStore}
	if _, _, restoreErr := restarted.restoreCheckpointedCandidate(
		ctx, pin, workspace.ID, lineID, charter,
	); restoreErr == nil || !strings.Contains(restoreErr.Error(), "awaits aggregate reconciliation") {
		t.Fatalf("parked checkpoint restore error = %v", restoreErr)
	}
	if err = restarted.AcknowledgeFinalizedRepair(ctx, workspaceID, finalized); err != nil {
		t.Fatal(err)
	}
	if _, _, found, loadErr := restartedStore.Load(workspaceID); loadErr != nil || found {
		t.Fatalf("acknowledged checkpoint remains: found=%v err=%v", found, loadErr)
	}
	if err = restarted.AcknowledgeFinalizedRepair(ctx, workspaceID, finalized); err != nil {
		t.Fatalf("AcknowledgeFinalizedRepair(replay) error = %v", err)
	}
}

func prWorkspaceCandidateCheckpointFixture() prWorkspaceCandidateCheckpoint {
	return prWorkspaceCandidateCheckpoint{
		Version:        prWorkspaceCandidateCheckpointVersion,
		State:          prWorkspaceCandidateCheckpointActive,
		WorkspaceID:    "devw_11111111111111111111111111111111",
		Repository:     "git@example.invalid:owner/repository.git",
		SourceRef:      "feature/checkpoint",
		HeadSHA:        "1111111111111111111111111111111111111111",
		CharterID:      "pchar_11111111111111111111111111111111",
		CharterHeadSHA: "1111111111111111111111111111111111111111",
		GitWorkspaceID: "gw-111111111111",
		LineID:         "pdln_11111111111111111111111111111111",
		Lease: gitworkspace.PinnedLineLease{
			WorkspaceID: "gw-111111111111", Version: 0, MutationEpoch: 1,
			Tip: "1111111111111111111111111111111111111111", Tree: "2222222222222222222222222222222222222222",
		},
		Candidate: gitworkspace.PinnedCandidate{
			WorkspaceID:     "gw-111111111111",
			ParentCommit:    "1111111111111111111111111111111111111111",
			Tree:            "3333333333333333333333333333333333333333",
			CandidateDigest: "4444444444444444444444444444444444444444444444444444444444444444",
			ChangedFiles:    2,
		},
	}
}

func requireAbsentPRWorkspaceCheckpointRevision(
	t *testing.T,
	store *prWorkspaceCandidateCheckpointStore,
	workspaceID string,
) prWorkspaceCandidateCheckpointRevision {
	t.Helper()
	_, revision, found, err := store.Load(workspaceID)
	if err != nil || found || !validPRWorkspaceCheckpointRevision(revision, workspaceID) || revision.exists {
		t.Fatalf("observe absent checkpoint %q = %#v, found=%v err=%v", workspaceID, revision, found, err)
	}
	return revision
}
