package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace/localci"
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
	store, storeErr := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if storeErr != nil {
		t.Fatal(storeErr)
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

	store, storeErr := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if storeErr != nil {
		t.Fatal(storeErr)
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

func TestCheckpointSchemaMigrationAndImportFaultBoundaries(t *testing.T) {
	openMemoryConnection := func(t *testing.T, statements ...string) (*sql.DB, *sql.Conn) {
		t.Helper()
		database, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		for _, statement := range statements {
			if _, execErr := database.Exec(statement); execErr != nil {
				t.Fatal(execErr)
			}
		}
		conn, err := database.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return database, conn
	}
	openStoreConnection := func(t *testing.T) (*prWorkspaceCandidateCheckpointStore, *sql.DB, *sql.Conn) {
		t.Helper()
		store, err := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
		if err != nil {
			t.Fatal(err)
		}
		database, err := store.open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		conn, err := database.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return store, database, conn
	}

	t.Run("v1 query", func(t *testing.T) {
		_, conn := openMemoryConnection(t)
		if err := initializePRWorkspaceCheckpointMeta(t.Context(), conn, false); err == nil {
			t.Fatal("metadata initialization without the v1 table succeeded")
		}
	})
	t.Run("metadata insert", func(t *testing.T) {
		_, conn := openMemoryConnection(t, prWorkspaceCheckpointsSchema)
		if err := initializePRWorkspaceCheckpointMeta(t.Context(), conn, false); err == nil {
			t.Fatal("metadata initialization without its target table succeeded")
		}
	})
	t.Run("seal query and missing horizon", func(t *testing.T) {
		_, missingConn := openMemoryConnection(t)
		if err := sealPRWorkspaceCheckpointImport(t.Context(), missingConn, false); err == nil {
			t.Fatal("seal without its import table succeeded")
		}
		_, emptyConn := openMemoryConnection(t, prWorkspaceCheckpointImportStateSchema)
		if err := sealPRWorkspaceCheckpointImport(t.Context(), emptyConn, false); err == nil {
			t.Fatal("seal without a durable import horizon succeeded")
		}
	})
	t.Run("invalid metadata", func(t *testing.T) {
		_, _, conn := openStoreConnection(t)
		if _, err := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(), `UPDATE checkpoint_metadata
		    SET next_revision = 0 WHERE singleton = 1`); err != nil {
			t.Fatal(err)
		}
		if err := validatePRWorkspaceCheckpointSchema(t.Context(), conn); err == nil {
			t.Fatal("invalid checkpoint revision metadata passed schema validation")
		}
	})

	checkpoint := prWorkspaceCandidateCheckpointFixture()
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	input := sqlitestore.LegacyInput{
		Relative: legacyPRWorkspaceCheckpointFilename(checkpoint.WorkspaceID),
		Data:     encoded,
	}
	t.Run("import horizon query", func(t *testing.T) {
		_, _, conn := openStoreConnection(t)
		if _, err := conn.ExecContext(t.Context(), `DROP TABLE checkpoint_legacy_import_state`); err != nil {
			t.Fatal(err)
		}
		if _, err := importLegacyPRWorkspaceCheckpoint(t.Context(), conn, input); err == nil {
			t.Fatal("legacy import without its horizon table succeeded")
		}
	})
	t.Run("invalid import horizon", func(t *testing.T) {
		_, _, conn := openStoreConnection(t)
		for _, statement := range []string{
			`DROP TABLE checkpoint_legacy_import_state`,
			`CREATE TABLE checkpoint_legacy_import_state(singleton INTEGER, import_closed INTEGER)`,
			`INSERT INTO checkpoint_legacy_import_state VALUES (1, 1), (1, 1)`,
		} {
			if _, err := conn.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := importLegacyPRWorkspaceCheckpoint(t.Context(), conn, input); err == nil ||
			!strings.Contains(err.Error(), "horizon") {
			t.Fatalf("invalid import horizon error = %v", err)
		}
	})
	t.Run("revision allocation", func(t *testing.T) {
		_, _, conn := openStoreConnection(t)
		if _, err := conn.ExecContext(t.Context(), `DELETE FROM checkpoint_legacy_import_state`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(), `DROP TABLE checkpoint_metadata`); err != nil {
			t.Fatal(err)
		}
		if _, err := importLegacyPRWorkspaceCheckpoint(t.Context(), conn, input); err == nil {
			t.Fatal("legacy import without revision metadata succeeded")
		}
	})
	t.Run("row insert", func(t *testing.T) {
		_, _, conn := openStoreConnection(t)
		for _, statement := range []string{
			`DELETE FROM checkpoint_legacy_import_state`,
			`CREATE TRIGGER reject_checkpoint_insert BEFORE INSERT ON candidate_checkpoints
			 BEGIN SELECT RAISE(ABORT, 'injected insert failure'); END`,
		} {
			if _, err := conn.ExecContext(t.Context(), statement); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := importLegacyPRWorkspaceCheckpoint(t.Context(), conn, input); err == nil {
			t.Fatal("legacy import ignored its row insertion failure")
		}
	})
}

func TestCheckpointAggregateParserReportsUnterminatedValues(t *testing.T) {
	for name, input := range map[string]string{
		"object": `{"value":1`,
		"array":  `[1`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := rejectDuplicatePRWorkspaceCheckpointJSONNames([]byte(input)); err == nil {
				t.Fatal("unterminated aggregate was accepted")
			}
		})
	}
}

func TestCheckpointLegacyEnumerationRejectsUnreadableAndNonDirectoryRoots(t *testing.T) {
	invalid := &prWorkspaceCandidateCheckpointStore{root: "invalid\x00root"}
	if sources, err := invalid.legacySourcesBounded(1, 1); err == nil || sources != nil {
		t.Fatalf("invalid legacy root = %#v, %v", sources, err)
	}
	regularRoot := filepath.Join(t.TempDir(), "checkpoint-root")
	if err := os.WriteFile(regularRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	regular := &prWorkspaceCandidateCheckpointStore{root: regularRoot}
	if sources, err := regular.legacySourcesBounded(1, 1); err == nil || sources != nil {
		t.Fatalf("regular-file legacy root = %#v, %v", sources, err)
	}
}

func TestCheckpointRevisionAndInternalOperationFaultBoundaries(t *testing.T) {
	t.Run("invalid allocated revision", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		if _, execErr := database.Exec(`CREATE TABLE checkpoint_metadata(
		    singleton INTEGER PRIMARY KEY, next_revision INTEGER NOT NULL
		); INSERT INTO checkpoint_metadata VALUES (1, 0)`); execErr != nil {
			t.Fatal(execErr)
		}
		conn, err := database.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if revision, err := allocatePRWorkspaceCheckpointRevision(t.Context(), conn); err == nil || revision != 0 {
			t.Fatalf("invalid allocated revision = %d, %v", revision, err)
		}
	})

	for _, operation := range []string{"removal match", "finalized reconciliation"} {
		t.Run(operation, func(t *testing.T) {
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
			if err := os.Remove(store.databasePath()); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(store.databasePath(), 0o700); err != nil {
				t.Fatal(err)
			}
			if operation == "removal match" {
				if matched, err := store.removalMatches(checkpoint.WorkspaceID, revision); err == nil || matched {
					t.Fatalf("unsafe removal match = %v, %v", matched, err)
				}
				return
			}
			parked, _ := parkedPRWorkspaceCheckpoint(checkpoint)
			if _, matched, err := store.reconcileFinalized(parked, revision); err == nil || matched {
				t.Fatalf("unsafe finalized reconciliation = %v, %v", matched, err)
			}
		})
	}

	t.Run("remove rolls back exhausted deletion sequence", func(t *testing.T) {
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
		database, err := store.open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, execErr := database.Exec(`UPDATE checkpoint_metadata
		    SET next_revision = 9223372036854775807 WHERE singleton = 1`); execErr != nil {
			_ = database.Close()
			t.Fatal(execErr)
		}
		if closeErr := database.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if removeErr := store.Remove(checkpoint.WorkspaceID, revision); removeErr == nil ||
			!strings.Contains(removeErr.Error(), "exhausted") {
			t.Fatalf("remove at exhausted revision = %v", removeErr)
		}
		loaded, retainedRevision, found, err := store.Load(checkpoint.WorkspaceID)
		if err != nil || !found || loaded != checkpoint || retainedRevision != revision {
			t.Fatalf("rolled-back removal = %#v, %#v, %v, %v", loaded, retainedRevision, found, err)
		}
	})

	if validPRWorkspaceCheckpointRevision(
		prWorkspaceCandidateCheckpointRevision{workspaceID: "one"}, "two",
	) {
		t.Fatal("cross-workspace checkpoint revision was accepted")
	}
}

func TestImplementationValidateProjectsUnavailableLocalCI(t *testing.T) {
	workspaceID := "devw_11111111111111111111111111111111"
	tree := strings.Repeat("a", 40)
	candidate := prWorkspaceCandidate{
		pin: gitworkspace.PinnedAcquireRequest{
			Repository: "https://example.invalid/repository.git",
			SourceRef:  "main", ExpectedCommit: strings.Repeat("b", 40),
			ReservationKey: "validation-reservation", AgentID: "validation-agent",
		},
		candidate: gitworkspace.PinnedCandidate{
			WorkspaceID: "gw-validation", ParentCommit: strings.Repeat("b", 40), Tree: tree,
			CandidateDigest: strings.Repeat("c", 64), ChangedFiles: 1,
		},
	}
	runtime := &prWorkspaceImplementationRuntime{
		manager: &gitworkspace.Manager{},
		ci:      &localci.Runner{},
		candidates: map[prWorkspaceCandidateKey]prWorkspaceCandidate{
			{workspaceID: workspaceID, tree: tree}: candidate,
		},
	}
	run, err := runtime.Validate(t.Context(), prworkspace.ValidationRequest{
		ID: "validation-unavailable", WorkspaceID: workspaceID, CandidateSHA: tree,
	})
	if err == nil || run.State != prworkspace.ExecutionFailed || run.StartedAt.IsZero() ||
		run.FinishedAt == nil || len(run.Checks) != 1 || run.Checks[0].ID != "local-ci" ||
		run.Checks[0].Status != "failed" {
		t.Fatalf("unavailable local CI projection = %#v, %v", run, err)
	}
}

func TestImplementationValidationAndSummaryDefensiveBranches(t *testing.T) {
	var nilRuntime *prWorkspaceImplementationRuntime
	if run, err := nilRuntime.Validate(t.Context(), prworkspace.ValidationRequest{}); err == nil ||
		run.ID != "" || run.State != "" || len(run.Checks) != 0 {
		t.Fatalf("nil validation runtime = %#v, %v", run, err)
	}

	value := strings.Repeat("x", (4<<10)-1) + "é" + strings.Repeat("y", 16)
	summary := publicLocalCISummary(value, localci.StatusInfrastructureError)
	if !strings.HasSuffix(summary, "… output truncated …") || !strings.Contains(summary, "x") {
		t.Fatalf("UTF-8 bounded infrastructure summary = %q", summary)
	}
	if index := publicLocalCIStackStart("ordinary output without a stack marker"); index != -1 {
		t.Fatalf("ordinary output stack index = %d", index)
	}
}

func TestImplementationRuntimeDefensiveLifecycleBoundaries(t *testing.T) {
	if created, err := newPRWorkspaceImplementationRuntime(nil, nil, "", nil); err == nil || created != nil {
		t.Fatalf("incomplete implementation runtime = %#v, %v", created, err)
	}
	if created, err := newPRWorkspaceImplementationRuntime(
		&agent.AgentLoop{}, &localci.Runner{}, "controller-agent", nil,
	); err == nil || created != nil {
		t.Fatalf("runtime without a controller Git manager = %#v, %v", created, err)
	}

	checkpoint := prWorkspaceCandidateCheckpointFixture()
	_, fence := parkedPRWorkspaceCheckpoint(checkpoint)
	result := prworkspace.RepairResult{
		WorkspaceID: checkpoint.GitWorkspaceID, CandidateSHA: fence.Tip, PublicationFence: fence,
	}
	var nilRuntime *prWorkspaceImplementationRuntime
	if err := nilRuntime.AcknowledgeFinalizedRepair(
		t.Context(), checkpoint.WorkspaceID, result,
	); err == nil {
		t.Fatal("nil finalized-repair acknowledgement succeeded")
	}
	if err := nilRuntime.saveCandidateCheckpoint(checkpoint.WorkspaceID, nil); err == nil {
		t.Fatal("nil runtime saved an active candidate checkpoint")
	}
	if _, found, err := nilRuntime.restoreCheckpointedCandidate(
		t.Context(), gitworkspace.PinnedAcquireRequest{}, "", "", prworkspace.Charter{},
	); err == nil || found {
		t.Fatalf("nil checkpoint restoration = found:%v error:%v", found, err)
	}
	if evidence, err := nilRuntime.LoadCandidateEvidence(t.Context(), prworkspace.RepairAttempt{}); err == nil ||
		evidence.CandidateSHA != "" || evidence.CandidateDiff != "" ||
		evidence.EvidenceDigest != "" || len(evidence.Metrics.ChangedFiles) != 0 {
		t.Fatalf("nil candidate evidence = %#v, %v", evidence, err)
	}

	store, storeErr := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	runtime := &prWorkspaceImplementationRuntime{checkpoints: store}
	changed := result
	changed.CandidateSHA = "changed-tip"
	if err := runtime.AcknowledgeFinalizedRepair(
		t.Context(), checkpoint.WorkspaceID, changed,
	); err == nil {
		t.Fatal("changed finalized-repair acknowledgement succeeded")
	}
	if err := runtime.AcknowledgeFinalizedRepair(
		t.Context(), checkpoint.WorkspaceID, result,
	); err != nil {
		t.Fatalf("absent finalized checkpoint acknowledgement = %v", err)
	}

	revision, saveErr := store.Save(
		checkpoint,
		requireAbsentPRWorkspaceCheckpointRevision(t, store, checkpoint.WorkspaceID),
	)
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	if err := runtime.AcknowledgeFinalizedRepair(
		t.Context(), checkpoint.WorkspaceID, result,
	); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("active checkpoint acknowledgement = %v", err)
	}

	candidate := prWorkspaceCandidate{checkpointRevision: revision}
	if err := runtime.saveFinalizedCandidateCheckpoint(
		checkpoint.WorkspaceID, &candidate, fence,
	); err == nil {
		t.Fatal("candidate without parked evidence was finalized")
	}
	if err := runtime.saveCandidateCheckpointRevision(checkpoint, &candidate); err == nil {
		t.Fatal("active checkpoint was accepted as finalized")
	}

	unsafeStore, unsafeStoreErr := newPRWorkspaceCandidateCheckpointStore(filepath.Join(t.TempDir(), "active"))
	if unsafeStoreErr != nil {
		t.Fatal(unsafeStoreErr)
	}
	if err := os.Remove(unsafeStore.databasePath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(unsafeStore.databasePath(), 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeRuntime := &prWorkspaceImplementationRuntime{checkpoints: unsafeStore}
	if err := unsafeRuntime.AcknowledgeFinalizedRepair(
		t.Context(), checkpoint.WorkspaceID, result,
	); err == nil {
		t.Fatal("acknowledgement ignored an unsafe checkpoint database")
	}

	if _, err := snapshotPRWorkspaceExpectedCandidate(
		t.Context(), &gitworkspace.Manager{}, gitworkspace.PinnedCandidateRequest{}, 0,
	); err == nil {
		t.Fatal("validation snapshot unexpectedly succeeded without a manager root")
	}
	ctx, release, err := (&prWorkspaceImplementationRuntime{}).acquire(t.Context())
	if err != nil || ctx == nil || release == nil {
		t.Fatalf("default runtime acquisition = %#v, %v, %v", ctx, release != nil, err)
	}
	release()
	if got := stablePRWorkspaceLineID("malformed-workspace"); got != "pdln_00000000000000000000000000000000" {
		t.Fatalf("fallback development line ID = %q", got)
	}
}
