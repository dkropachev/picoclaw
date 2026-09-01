package gateway

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

func TestCheckpointJSONParserRejectsRecursiveMalformedAndDuplicateShapes(t *testing.T) {
	for _, test := range []struct {
		name, input string
		wantError   bool
	}{
		{name: "scalar", input: `1`},
		{name: "nested", input: `{"a":[1,{"b":true}]}`},
		{name: "truncated object", input: `{"a":`, wantError: true},
		{name: "truncated array", input: `[`, wantError: true},
		{name: "nested duplicate", input: `{"a":{"B":1,"b":2}}`, wantError: true},
		{
			name:      "depth",
			input:     strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66),
			wantError: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := rejectDuplicatePRWorkspaceCheckpointJSONNames([]byte(test.input))
			if (err != nil) != test.wantError {
				t.Fatalf("parser error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestCheckpointSaveIdempotenceAndRevisionExhaustion(t *testing.T) {
	t.Run("idempotent save retains revision", func(t *testing.T) {
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		checkpoint := prWorkspaceCandidateCheckpointFixture()
		revision, err := store.Save(
			checkpoint,
			requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID),
		)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := store.Save(checkpoint, revision)
		if err != nil || replayed != revision {
			t.Fatalf("idempotent save revision = %#v, %v", replayed, err)
		}
	})

	for _, operation := range []string{"insert", "update"} {
		t.Run(operation, func(t *testing.T) {
			store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := prWorkspaceCandidateCheckpointFixture()
			revision := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
			if operation == "update" {
				revision, err = store.Save(checkpoint, revision)
				if err != nil {
					t.Fatal(err)
				}
				checkpoint.Candidate.CandidateDigest = strings.Repeat("9", 64)
			}
			database, err := store.open(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`UPDATE checkpoint_metadata
			    SET next_revision = 9223372036854775807 WHERE singleton = 1`); err != nil {
				_ = database.Close()
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Save(checkpoint, revision); err == nil ||
				!strings.Contains(err.Error(), "exhausted") {
				t.Fatalf("%s at exhausted revision = %v", operation, err)
			}
		})
	}
}

func TestCheckpointTerminalReconciliationRejectsInProcessParkedABA(t *testing.T) {
	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	candidate := prWorkspaceCandidateFromCheckpoint(
		checkpoint,
		requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID),
	)
	candidate.parked = &gitworkspace.PinnedLineParkResult{}
	runtime := &prWorkspaceImplementationRuntime{checkpoints: store}
	if saveErr := runtime.saveCandidateCheckpoint(checkpoint.WorkspaceID, &candidate); saveErr != nil {
		t.Fatal(saveErr)
	}
	stale := candidate
	winner := candidate
	parked, fence := parkedPRWorkspaceCheckpoint(checkpoint)
	if finalizeErr := runtime.saveFinalizedCandidateCheckpoint(
		checkpoint.WorkspaceID, &winner, fence,
	); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	if removeErr := store.Remove(checkpoint.WorkspaceID, winner.checkpointRevision); removeErr != nil {
		t.Fatal(removeErr)
	}
	absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	recreated, err := store.Save(checkpoint, absent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(parked, recreated); err != nil {
		t.Fatal(err)
	}
	if err := runtime.saveFinalizedCandidateCheckpoint(
		checkpoint.WorkspaceID, &stale, fence,
	); !errors.Is(err, errPRWorkspaceCandidateCheckpointConflict) {
		t.Fatalf("parked ABA reconciliation error = %v", err)
	}
}

func TestCheckpointDefensiveHelperBoundaries(t *testing.T) {
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	var nilStore *prWorkspaceCandidateCheckpointStore
	if ok, err := nilStore.removalMatches(
		checkpoint.WorkspaceID, prWorkspaceCandidateCheckpointRevision{},
	); err == nil || ok {
		t.Fatalf("nil removal match = %v, %v", ok, err)
	}
	if _, ok, err := nilStore.reconcileFinalized(
		checkpoint, prWorkspaceCandidateCheckpointRevision{},
	); err == nil || ok {
		t.Fatalf("nil finalized reconciliation = %v, %v", ok, err)
	}

	store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if err != nil {
		t.Fatal(err)
	}
	absent := requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID)
	if _, ok, reconcileErr := store.reconcileFinalized(checkpoint, absent); reconcileErr == nil || ok {
		t.Fatalf("active finalized reconciliation = %v, %v", ok, reconcileErr)
	}
	parked, _ := parkedPRWorkspaceCheckpoint(checkpoint)
	if _, ok, reconcileErr := store.reconcileFinalized(
		parked, prWorkspaceCandidateCheckpointRevision{},
	); reconcileErr == nil || ok {
		t.Fatalf("invalid revision reconciliation = %v, %v", ok, reconcileErr)
	}

	database, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
		if _, dropErr := conn.ExecContext(t.Context(), `DROP TABLE checkpoint_deletions`); dropErr != nil {
			return dropErr
		}
		_, revisionErr := revisionForPRWorkspaceCheckpoint(
			t.Context(), conn, prWorkspaceCandidateCheckpoint{}, 0, false, checkpoint.WorkspaceID,
		)
		return revisionErr
	})
	_ = database.Close()
	if err == nil {
		t.Fatal("missing deletion table did not fail absent revision lookup")
	}
}

func TestCheckpointMetadataInitializationRejectsExhaustedV1Revision(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, execErr := database.Exec(prWorkspaceCheckpointsSchema); execErr != nil {
		t.Fatal(execErr)
	}
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	arguments := append(prWorkspaceCheckpointArguments(checkpoint), int64(^uint64(0)>>1))
	if _, execErr := database.Exec(prWorkspaceCheckpointInsertSQL, arguments...); execErr != nil {
		t.Fatal(execErr)
	}
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := initializePRWorkspaceCheckpointMeta(t.Context(), conn, false); err == nil ||
		!strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("exhausted v1 metadata initialization = %v", err)
	}
}
