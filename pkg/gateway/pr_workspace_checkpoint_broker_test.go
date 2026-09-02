package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
)

func TestPRWorkspaceCheckpointBrokerTypedLifecycleAndRetainedPool(t *testing.T) {
	home := t.TempDir()
	gitRoot := filepath.Join(home, "git-workspaces")
	cfg := &config.Config{GitWorkspaces: config.GitWorkspacesConfig{RootDir: gitRoot}}
	handler, err := NewPRWorkspaceCheckpointBrokerHandler(home, cfg)
	if err != nil {
		t.Fatalf("NewPRWorkspaceCheckpointBrokerHandler() error = %v", err)
	}
	retained := handler.store.db
	if retained == nil {
		t.Fatal("broker handler did not retain its checkpoint pool")
	}
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		_ = handler.Close()
		t.Fatalf("database.StartServer() error = %v", err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := server.Close(closeCtx); closeErr != nil {
			t.Errorf("server.Close() error = %v", closeErr)
		}
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatalf("database.Connect() error = %v", err)
	}
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(client)
	t.Cleanup(func() { database.InstallProcessClient(previousClient) })

	// This path must remain entirely opaque to a runtime-side constructor.
	runtimePhysicalTrap := filepath.Join(home, "must-not-be-derived")
	store, err := newPRWorkspaceCandidateCheckpointStore(runtimePhysicalTrap)
	if err != nil {
		t.Fatalf("runtime checkpoint constructor error = %v", err)
	}
	if store.root != "" || store.databasePath() != "" || store.broker != client ||
		store.storeID != PRWorkspaceCheckpointStoreID {
		t.Fatalf("runtime checkpoint store exposes provider state: %#v", store)
	}

	checkpoint := prWorkspaceCandidateCheckpointFixture()
	_, absent, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || found || absent.exists {
		t.Fatalf("initial Load() = found %v, revision %#v, error %v", found, absent, err)
	}
	revision, err := store.Save(checkpoint, absent)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, loadedRevision, found, err := store.Load(checkpoint.WorkspaceID)
	if err != nil || !found || loaded != checkpoint || loadedRevision != revision {
		t.Fatalf("Load() = %#v, %#v, %v, %v", loaded, loadedRevision, found, err)
	}
	if _, saveErr := store.Save(checkpoint, absent); !errors.Is(
		saveErr,
		errPRWorkspaceCandidateCheckpointConflict,
	) || database.CodeOf(saveErr) != database.CodeConflict {
		t.Fatalf("stale Save() error = %v", saveErr)
	}

	parked := checkpoint
	parked.State = prWorkspaceCandidateCheckpointParked
	parked.Fence = &prworkspace.ImplementationPublicationFence{
		GitWorkspaceID: parked.GitWorkspaceID,
		LineID:         parked.LineID,
		LineVersion:    parked.Lease.Version + 1,
		MutationEpoch:  parked.Lease.MutationEpoch,
		ParkIntentID:   "park-intent",
		BaseCommit:     parked.HeadSHA,
		Tip:            "parked-tip",
		Tree:           parked.Candidate.Tree,
	}
	parkedRevision, err := store.Save(parked, revision)
	if err != nil {
		t.Fatalf("Save(parked) error = %v", err)
	}
	reconciled, matched, err := store.reconcileFinalized(parked, revision)
	if err != nil || !matched || reconciled != parkedRevision {
		t.Fatalf("reconcileFinalized() = %#v, %v, %v", reconciled, matched, err)
	}
	if err = store.Remove(checkpoint.WorkspaceID, parkedRevision); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	matched, err = store.removalMatches(checkpoint.WorkspaceID, parkedRevision)
	if err != nil || !matched {
		t.Fatalf("removalMatches() = %v, %v", matched, err)
	}
	if handler.store.db != retained {
		t.Fatal("broker replaced its retained checkpoint pool")
	}
	if _, statErr := os.Lstat(runtimePhysicalTrap); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("runtime constructor touched its physical trap: %v", statErr)
	}
}

func TestPRWorkspaceCheckpointBrokerHasNoRuntimeFallback(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{GitWorkspaces: config.GitWorkspacesConfig{
		RootDir: filepath.Join(home, "git-workspaces"),
	}}
	handler, err := NewPRWorkspaceCheckpointBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home: home, Handler: handler, CloseHandler: handler.Close,
	})
	if err != nil {
		_ = handler.Close()
		t.Fatal(err)
	}
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	trap := filepath.Join(home, "runtime-fallback-trap")
	previousClient := database.RuntimeClient()
	database.InstallProcessClient(client)
	store, err := newPRWorkspaceCandidateCheckpointStore(trap)
	database.InstallProcessClient(previousClient)
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = server.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = store.Load("workspace-one"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("Load() after broker shutdown error = %v", err)
	}
	if _, statErr := os.Lstat(trap); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("runtime fallback touched the physical trap: %v", statErr)
	}
}

func TestPRWorkspaceCheckpointProviderAndMigrationAuthority(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "checkpoint-root")
	restore := database.SuspendProviderTestAuthority()
	if _, err := newPRWorkspaceCandidateCheckpointStore(root); database.CodeOf(err) != database.CodeUnauthorized {
		restore()
		t.Fatalf("unfenced provider constructor error = %v", err)
	}
	if _, err := NewPRWorkspaceCheckpointBrokerHandler(home, &config.Config{}); database.CodeOf(err) !=
		database.CodeUnauthorized {
		restore()
		t.Fatalf("unfenced broker handler error = %v", err)
	}
	restore()

	if err := RunOfflinePRWorkspaceCheckpointMigration(t.Context(), root); database.CodeOf(err) !=
		database.CodeConflict {
		t.Fatalf("unfenced offline migration error = %v", err)
	}
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatalf("database.AcquireMigrationFence() error = %v", err)
	}
	if err = RunOfflinePRWorkspaceCheckpointMigration(t.Context(), root); err != nil {
		_ = fence.Close()
		t.Fatalf("RunOfflinePRWorkspaceCheckpointMigration() error = %v", err)
	}
	if err = fence.Close(); err != nil {
		t.Fatalf("migration fence Close() error = %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, prWorkspaceCheckpointDatabaseFilename)); err != nil {
		t.Fatalf("offline checkpoint store missing: %v", err)
	}
}

func TestPRWorkspaceCheckpointBrokerRejectsUnknownStoreAndMalformedRevision(t *testing.T) {
	home := t.TempDir()
	handler, err := NewPRWorkspaceCheckpointBrokerHandler(home, &config.Config{
		GitWorkspaces: config.GitWorkspacesConfig{RootDir: filepath.Join(home, "git-workspaces")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	checkpoint := prWorkspaceCandidateCheckpointFixture()
	request := database.Request{
		Domain: PRWorkspaceCheckpointBrokerDomain, Version: prWorkspaceCheckpointBrokerVersion,
		Operation: prWorkspaceCheckpointOperationSave,
	}
	request.Payload, err = database.MarshalCanonical(prWorkspaceCheckpointSaveRequest{
		StoreID: "other.store", Checkpoint: checkpoint,
		Expected: prWorkspaceCheckpointRevisionWire{WorkspaceID: checkpoint.WorkspaceID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handler.Handle(t.Context(), request); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("unknown StoreID error = %v", err)
	}
	request.Payload, err = database.MarshalCanonical(prWorkspaceCheckpointSaveRequest{
		StoreID: PRWorkspaceCheckpointStoreID, Checkpoint: checkpoint,
		Expected: prWorkspaceCheckpointRevisionWire{
			WorkspaceID: checkpoint.WorkspaceID, Sequence: 1, Exists: true, StateDigest: "not-a-digest",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handler.Handle(t.Context(), request); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("malformed revision error = %v", err)
	}
}
