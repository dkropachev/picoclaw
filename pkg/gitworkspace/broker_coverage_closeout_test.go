//nolint:govet // Independent broker boundary assertions intentionally use narrow errors.
package gitworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

//nolint:govet // Boundary assertions intentionally keep operation errors local.
func TestInventoryBrokerHandlerProtocolAndLeaseBoundaries(t *testing.T) {
	fixture := newInventoryBrokerFixture(t)
	handler := fixture.handler
	if _, err := (*BrokerHandler)(
		nil,
	).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler = %v", err)
	}
	if _, err := handler.Handle(
		t.Context(),
		database.Request{Domain: "wrong"},
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("wrong domain = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := handler.Handle(
		canceled,
		inventoryRequest(t, inventoryOperationPreflight, inventoryBrokerTarget{StoreID: InventoryStoreID}),
	); database.CodeOf(
		err,
	) != database.CodeDeadline {
		t.Fatalf("canceled request = %v", err)
	}
	for _, operation := range []string{
		inventoryOperationPreflight, inventoryOperationAcquireLease, inventoryOperationRenewLease,
		inventoryOperationReleaseLease, inventoryOperationLoadChunk, inventoryOperationSaveBegin,
		inventoryOperationSaveChunk, inventoryOperationSaveCommit,
	} {
		if _, err := handler.Handle(t.Context(), database.Request{
			Domain: BrokerDomain, Version: BrokerVersion, Operation: operation,
			Payload: json.RawMessage(`{`),
		}); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("%s malformed request = %v", operation, err)
		}
	}
	if _, err := handler.Handle(
		t.Context(),
		inventoryRequest(t, "unknown", inventoryBrokerTarget{}),
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unknown operation = %v", err)
	}
	if handler.validLeaseRequest(inventoryBrokerLeaseRequest{StoreID: "wrong", LeaseID: "lease"}) ||
		handler.validLeaseRequest(inventoryBrokerLeaseRequest{StoreID: InventoryStoreID}) {
		t.Fatal("invalid lease request was accepted")
	}
	if (*BrokerHandler)(nil).effectiveLeaseTTL() <= 0 {
		t.Fatal("nil handler lease TTL is not positive")
	}
	if _, err := handler.renewLease("missing"); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("missing renewal = %v", err)
	}
	if _, err := handler.releaseLease("missing"); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("missing release = %v", err)
	}
	if _, err := handler.beginLeaseOperation("missing"); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("missing operation lease = %v", err)
	}
	if _, err := handler.beginSave(
		inventoryBrokerSaveBeginRequest{LeaseID: "missing"},
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("missing upload lease = %v", err)
	}
	if _, err := handler.saveChunk(
		inventoryBrokerSaveChunkRequest{LeaseID: "missing", Data: []byte("x")},
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("missing upload chunk = %v", err)
	}
	if _, err := handler.commitSave(
		t.Context(),
		inventoryBrokerSaveCommitRequest{LeaseID: "missing"},
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("missing upload commit = %v", err)
	}
	if _, err := handler.loadChunk(
		t.Context(),
		inventoryBrokerLoadRequest{LeaseID: "missing", Offset: 1},
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("missing snapshot = %v", err)
	}
	if _, err := handler.loadChunk(
		t.Context(), inventoryBrokerLoadRequest{LeaseID: "missing"},
	); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("missing initial snapshot = %v", err)
	}

	leaseAny, err := handler.acquireLease(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	leaseID := leaseAny.(inventoryBrokerLeaseResponse).LeaseID
	deadline, cancelDeadline := context.WithCancel(t.Context())
	cancelDeadline()
	if _, err := handler.acquireLease(deadline); database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("contended canceled lease = %v", err)
	}
	if _, err := handler.renewLease(leaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.loadChunk(
		t.Context(),
		inventoryBrokerLoadRequest{LeaseID: leaseID, Offset: 1},
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("out-of-order snapshot = %v", err)
	}

	for name, data := range map[string][]byte{
		"digest":  []byte(`{"version":"4","repositories":{},"workspaces":{}}`),
		"payload": []byte(`{`),
	} {
		t.Run(name, func(t *testing.T) {
			digest := sha256.Sum256(data)
			digestText := hex.EncodeToString(digest[:])
			beginDigest := digestText
			if name == "digest" {
				beginDigest = strings.Repeat("0", sha256.Size*2)
			}
			if _, err := handler.beginSave(inventoryBrokerSaveBeginRequest{
				LeaseID: leaseID, ExpectedGeneration: 0, TotalBytes: len(data), Digest: beginDigest,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := handler.saveChunk(inventoryBrokerSaveChunkRequest{
				LeaseID: leaseID, Data: data,
			}); err != nil {
				t.Fatal(err)
			}
			_, err := handler.commitSave(t.Context(), inventoryBrokerSaveCommitRequest{
				LeaseID: leaseID, ExpectedGeneration: 0, TotalBytes: len(data), Digest: beginDigest,
			})
			want := database.CodeIntegrity
			if name == "payload" {
				want = database.CodeInvalid
			}
			if database.CodeOf(err) != want {
				t.Fatalf("commit error = %v, want %s", err, want)
			}
		})
	}
	providerData, err := json.Marshal(storeState{
		Version: stateVersion, Repositories: map[string]*RepositoryRecord{},
		Workspaces: map[string]*WorkspaceRecord{},
	})
	if err != nil {
		t.Fatal(err)
	}
	providerDigest := sha256.Sum256(providerData)
	providerDigestText := hex.EncodeToString(providerDigest[:])
	if _, err := handler.beginSave(inventoryBrokerSaveBeginRequest{
		LeaseID: leaseID, TotalBytes: len(providerData), Digest: providerDigestText,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.saveChunk(inventoryBrokerSaveChunkRequest{
		LeaseID: leaseID, Data: providerData,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.commitSave(t.Context(), inventoryBrokerSaveCommitRequest{
		LeaseID: leaseID, TotalBytes: len(providerData), Digest: providerDigestText,
	}); err != nil {
		t.Fatalf("upload commit = %v", err)
	}
	if _, err := handler.releaseLease(leaseID); err != nil {
		t.Fatal(err)
	}

	wrongLease := &inventoryBrokerLease{id: "wrong"}
	handler.endLeaseOperation(wrongLease)
	handler.finishSave(wrongLease, true)
	handler.expireLease(wrongLease, 1)
	handler.mu.Lock()
	handler.lease = &inventoryBrokerLease{id: "inflight", inflight: 1}
	handler.armLeaseLocked(handler.lease)
	inflight := handler.lease
	epoch := inflight.timerEpoch
	handler.mu.Unlock()
	handler.expireLease(inflight, epoch)
	handler.mu.Lock()
	handler.clearLeaseLocked()
	handler.mu.Unlock()
	if validInventoryDigest("BAD") {
		t.Fatal("invalid digest was accepted")
	}
}

func TestInventoryBrokerCloseMappingAndClientFailures(t *testing.T) {
	closed := &BrokerHandler{storeID: InventoryStoreID, changed: make(chan struct{}), closed: true}
	if err := closed.available(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed availability = %v", err)
	}
	if _, err := closed.acquireLease(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed acquisition = %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil close = %v", err)
	}
	for _, item := range []struct {
		err  error
		code database.ErrorCode
	}{
		{nil, ""},
		{database.NewError(database.CodeUnauthorized, "denied"), database.CodeUnauthorized},
		{context.Canceled, database.CodeDeadline},
		{errInventoryGenerationConflict, database.CodeConflict},
		{sqlitestore.ErrTooNew, database.CodeUnsupported},
		{sqlitestore.ErrInvalidSchema, database.CodeIntegrity},
		{errors.New("opaque"), database.CodeInternal},
	} {
		mapped := mapInventoryBrokerError(item.err)
		if item.err == nil {
			if mapped != nil {
				t.Errorf("nil mapping = %v", mapped)
			}
			continue
		}
		if database.CodeOf(mapped) != item.code {
			t.Errorf("map(%v) = %v", item.err, mapped)
		}
	}

	if err := (*Manager)(nil).brokerPreflight(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil manager preflight = %v", err)
	}
	if _, err := (*Manager)(nil).lockBrokerInventory(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil manager lock = %v", err)
	}
	manager := &Manager{brokerLeaseErr: database.NewError(database.CodeConflict, "lost")}
	if _, err := manager.lockBrokerInventory(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("brokerless manager lock = %v", err)
	}
	manager = &Manager{broker: &database.Client{}, storeID: InventoryStoreID}
	manager.recordBrokerInventoryLeaseError(nil)
	manager.recordBrokerInventoryLeaseError(database.NewError(database.CodeConflict, "lost"))
	manager.recordBrokerInventoryLeaseError(database.NewError(database.CodeInternal, "later"))
	if _, err := manager.brokerInventoryLeaseID(); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("remembered lease error = %v", err)
	}
	manager.brokerLeaseErr = nil
	if _, err := manager.brokerInventoryLeaseID(); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("missing lease ID = %v", err)
	}
	manager.brokerLease = &inventoryBrokerClientLease{id: "lease"}
	if id, err := manager.brokerInventoryLeaseID(); err != nil || id != "lease" {
		t.Fatalf("lease ID = %q, %v", id, err)
	}
	manager.brokerLease = nil
	if _, err := manager.loadBrokerInventory(t.Context()); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unleased inventory load = %v", err)
	}
	if err := manager.saveBrokerInventory(t.Context(), &storeState{}); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unleased inventory save = %v", err)
	}
	emptyState := storeState{}
	normalizeBrokerInventoryState(&emptyState)
	if emptyState.Repositories == nil || emptyState.Workspaces == nil ||
		emptyState.DevelopmentLines == nil || emptyState.PinnedReservationRotations == nil {
		t.Fatal("broker inventory normalization left nil maps")
	}
}

func TestInventoryBrokerClientRejectsMalformedTransportResponses(t *testing.T) {
	validState := storeState{
		Version: stateVersion, Repositories: map[string]*RepositoryRecord{},
		Workspaces: map[string]*WorkspaceRecord{},
	}
	encoded, err := json.Marshal(validState)
	if err != nil {
		t.Fatal(err)
	}
	digestValue := sha256.Sum256(encoded)
	digest := hex.EncodeToString(digestValue[:])
	validLoad := inventoryBrokerLoadResponse{
		Offset: 0, NextOffset: len(encoded), TotalBytes: len(encoded), Generation: 0,
		Digest: digest, Data: encoded, Done: true,
	}
	for _, test := range []struct {
		name    string
		respond func(database.Request) (any, error)
	}{
		{name: "rpc", respond: func(database.Request) (any, error) {
			return nil, database.NewError(database.CodeUnavailable, "offline")
		}},
		{name: "shape", respond: func(database.Request) (any, error) {
			return inventoryBrokerLoadResponse{}, nil
		}},
		{name: "generation", respond: func(database.Request) (any, error) {
			response := validLoad
			response.Generation = -1
			return response, nil
		}},
		{name: "incomplete", respond: func(database.Request) (any, error) {
			response := validLoad
			response.TotalBytes++
			return response, nil
		}},
		{name: "unfinished", respond: func(database.Request) (any, error) {
			response := validLoad
			response.Done = false
			return response, nil
		}},
		{name: "digest", respond: func(database.Request) (any, error) {
			response := validLoad
			response.Digest = strings.Repeat("0", sha256.Size*2)
			return response, nil
		}},
		{name: "json", respond: func(database.Request) (any, error) {
			data := []byte(`{`)
			sum := sha256.Sum256(data)
			return inventoryBrokerLoadResponse{
				NextOffset: 1, TotalBytes: 1, Digest: hex.EncodeToString(sum[:]), Data: data, Done: true,
			}, nil
		}},
	} {
		t.Run("load "+test.name, func(t *testing.T) {
			manager, closeBroker := scriptedInventoryManager(t, test.respond)
			defer closeBroker()
			if _, err := manager.loadBrokerInventory(t.Context()); err == nil {
				t.Fatal("malformed load response was accepted")
			}
		})
	}
	for name, state := range map[string]storeState{
		"relational": {
			Version: stateVersion, Repositories: map[string]*RepositoryRecord{},
			Workspaces: map[string]*WorkspaceRecord{"orphan": {ID: "orphan"}},
		},
		"development": {
			Version:      stateVersion,
			Repositories: map[string]*RepositoryRecord{"bad": {ID: "wrong"}},
			Workspaces:   map[string]*WorkspaceRecord{},
		},
	} {
		t.Run("load "+name, func(t *testing.T) {
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			manager, closeBroker := scriptedInventoryManager(t, func(database.Request) (any, error) {
				return inventoryBrokerLoadResponse{
					NextOffset: len(data), TotalBytes: len(data), Digest: hex.EncodeToString(sum[:]),
					Data: data, Done: true,
				}, nil
			})
			defer closeBroker()
			if _, err := manager.loadBrokerInventory(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
				t.Fatalf("invalid state = %v", err)
			}
		})
	}

	t.Run("changed page", func(t *testing.T) {
		calls := 0
		manager, closeBroker := scriptedInventoryManager(t, func(database.Request) (any, error) {
			calls++
			if calls == 1 {
				return inventoryBrokerLoadResponse{
					NextOffset: 1, TotalBytes: 2, Digest: digest, Data: []byte("x"), Done: false,
				}, nil
			}
			return inventoryBrokerLoadResponse{
				Offset: 1, NextOffset: 2, TotalBytes: 3, Generation: 1,
				Digest: digest, Data: []byte("y"), Done: true,
			}, nil
		})
		defer closeBroker()
		if _, err := manager.loadBrokerInventory(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("changed page = %v", err)
		}
	})

	for _, test := range []struct {
		name    string
		respond func(database.Request) (any, error)
	}{
		{name: "begin rpc", respond: inventoryOperationError(inventoryOperationSaveBegin)},
		{name: "begin shape", respond: func(request database.Request) (any, error) {
			if request.Operation == inventoryOperationSaveBegin {
				return inventoryBrokerSaveResponse{NextOffset: 1}, nil
			}
			return inventoryBrokerSaveResponse{}, nil
		}},
		{name: "chunk rpc", respond: inventoryOperationError(inventoryOperationSaveChunk)},
		{name: "chunk shape", respond: func(request database.Request) (any, error) {
			if request.Operation == inventoryOperationSaveChunk {
				return inventoryBrokerSaveResponse{}, nil
			}
			return inventoryBrokerSaveResponse{}, nil
		}},
		{name: "commit rpc", respond: inventoryOperationError(inventoryOperationSaveCommit)},
		{name: "commit shape", respond: func(request database.Request) (any, error) {
			switch request.Operation {
			case inventoryOperationSaveBegin:
				return inventoryBrokerSaveResponse{}, nil
			case inventoryOperationSaveChunk:
				var input inventoryBrokerSaveChunkRequest
				_ = request.DecodePayload(&input)
				return inventoryBrokerSaveResponse{NextOffset: input.Offset + len(input.Data)}, nil
			default:
				return inventoryBrokerSaveResponse{}, nil
			}
		}},
	} {
		t.Run("save "+test.name, func(t *testing.T) {
			manager, closeBroker := scriptedInventoryManager(t, test.respond)
			defer closeBroker()
			state := validState
			if err := manager.saveBrokerInventory(t.Context(), &state); err == nil {
				t.Fatal("malformed save response was accepted")
			}
		})
	}
}

func TestInventoryBrokerClientPreflightLeaseRenewAndReleaseValidation(t *testing.T) {
	manager, closeBroker := scriptedInventoryManager(t, func(request database.Request) (any, error) {
		switch request.Operation {
		case inventoryOperationPreflight:
			return inventoryBrokerMutationResponse{}, nil
		case inventoryOperationAcquireLease:
			return inventoryBrokerLeaseResponse{}, nil
		case inventoryOperationRenewLease:
			return inventoryBrokerLeaseResponse{LeaseID: "wrong", TTLNanoSeconds: 1}, nil
		case inventoryOperationReleaseLease:
			return inventoryBrokerMutationResponse{}, nil
		default:
			return inventoryBrokerMutationResponse{}, nil
		}
	})
	defer closeBroker()
	if err := manager.brokerPreflight(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("false preflight = %v", err)
	}
	manager.brokerLease = nil
	if _, err := manager.lockBrokerInventory(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid acquisition = %v", err)
	}
	remembered := database.NewError(database.CodeConflict, "lost")
	manager.brokerLeaseErr = remembered
	if _, err := manager.lockBrokerInventory(t.Context()); !errors.Is(err, remembered) {
		t.Fatalf("remembered lock error = %v", err)
	}
	manager.brokerLease = &inventoryBrokerClientLease{id: "held"}
	if _, err := manager.lockBrokerInventory(t.Context()); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("duplicate client lease = %v", err)
	}
	manager.brokerLease = nil
	renewLease := &inventoryBrokerClientLease{
		id: "lease", ttl: time.Millisecond, stop: make(chan struct{}), done: make(chan struct{}),
	}
	manager.renewBrokerInventoryLease(renewLease)
	if database.CodeOf(manager.brokerLeaseErr) != database.CodeIntegrity {
		t.Fatalf("invalid renewal = %v", manager.brokerLeaseErr)
	}
	manager.brokerLeaseErr = nil
	releaseLease := &inventoryBrokerClientLease{
		id: "lease", ttl: time.Second, stop: make(chan struct{}), done: make(chan struct{}),
	}
	close(releaseLease.done)
	manager.brokerLease = releaseLease
	manager.releaseBrokerInventoryLease(releaseLease)
	manager.releaseBrokerInventoryLease(releaseLease)
	if database.CodeOf(manager.brokerLeaseErr) != database.CodeIntegrity || manager.brokerLease != nil {
		t.Fatalf("invalid release = %v/%#v", manager.brokerLeaseErr, manager.brokerLease)
	}
	errorManager, closeErrorBroker := scriptedInventoryManager(t, func(database.Request) (any, error) {
		return nil, database.NewError(database.CodeUnavailable, "offline")
	})
	defer closeErrorBroker()
	errorManager.brokerLease = nil
	if _, err := errorManager.lockBrokerInventory(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("acquire RPC error = %v", err)
	}
	stopped := &inventoryBrokerClientLease{
		id: "stopped", ttl: time.Minute, stop: make(chan struct{}), done: make(chan struct{}),
	}
	close(stopped.stop)
	manager.renewBrokerInventoryLease(stopped)
	<-stopped.done
}

func TestInventoryBrokerConfigurationAndOfflineMigrationBoundaries(t *testing.T) {
	home := t.TempDir()
	restoreAuthority := database.SuspendProviderTestAuthority()
	_, unauthorizedErr := NewBrokerHandler(home, config.DefaultConfig())
	restoreAuthority()
	if database.CodeOf(unauthorizedErr) != database.CodeUnauthorized {
		t.Fatalf("unfenced broker = %v", unauthorizedErr)
	}
	if _, err := NewBrokerHandler("bad\x00home", config.DefaultConfig()); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid broker config = %v", err)
	}
	if _, err := configuredInventoryRoot("bad\x00home", nil); err == nil {
		t.Fatal("invalid home was accepted")
	}
	if root, err := configuredInventoryRoot(home, nil); err != nil || !filepath.IsAbs(root) {
		t.Fatalf("default inventory root = %q, %v", root, err)
	}
	badCfg := config.DefaultConfig()
	badCfg.Agents.Defaults.Workspace = "bad\x00workspace"
	if _, err := configuredInventoryRoot(home, badCfg); err == nil {
		t.Fatal("invalid configured workspace was accepted")
	}
	if _, err := expandInventoryConfiguredPath(home, ""); err == nil {
		t.Fatal("empty configured path was accepted")
	}
	if relative, err := expandInventoryConfiguredPath(home, "relative"); err != nil || !filepath.IsAbs(relative) {
		t.Fatalf("relative configured path = %q, %v", relative, err)
	}
	if expanded, err := expandInventoryConfiguredPath(home, "~/workspace"); err != nil || !filepath.IsAbs(expanded) {
		t.Fatalf("home configured path = %q, %v", expanded, err)
	}
	if expanded, err := expandInventoryConfiguredPath(home, "~"); err != nil || !filepath.IsAbs(expanded) {
		t.Fatalf("exact home configured path = %q, %v", expanded, err)
	}
	t.Setenv("HOME", "")
	if _, err := expandInventoryConfiguredPath(home, "~"); err == nil {
		t.Fatal("home-less configured path was accepted")
	}
	if err := RunOfflineDatabaseMigration(t.Context(), t.TempDir()); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unfenced migration = %v", err)
	}
	migrationHome := t.TempDir()
	fence, err := database.AcquireMigrationFence(migrationHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunOfflineDatabaseMigration(t.Context(), filepath.Join(migrationHome, "inventory")); err != nil {
		t.Fatal(err)
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}

	rootFile := filepath.Join(home, "root-file")
	if err := os.WriteFile(rootFile, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileCfg := config.DefaultConfig()
	fileCfg.GitWorkspaces.RootDir = rootFile
	if _, err := NewBrokerHandler(home, fileCfg); err == nil {
		t.Fatal("file inventory root was accepted")
	}
	databaseRoot := filepath.Join(home, "database-root")
	if err := os.MkdirAll(filepath.Join(databaseRoot, inventoryDatabaseFilename), 0o700); err != nil {
		t.Fatal(err)
	}
	databaseCfg := config.DefaultConfig()
	databaseCfg.GitWorkspaces.RootDir = databaseRoot
	if _, err := NewBrokerHandler(home, databaseCfg); err == nil {
		t.Fatal("directory inventory database was accepted")
	}
	secondFence, err := database.AcquireMigrationFence(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := RunOfflineDatabaseMigration(t.Context(), databaseRoot); err == nil {
		t.Fatal("directory inventory database migration succeeded")
	}
	if err := secondFence.Close(); err != nil {
		t.Fatal(err)
	}

	fixture := newInventoryBrokerFixture(t)
	if _, err := fixture.handler.Handle(nil, inventoryRequest(
		t, inventoryOperationPreflight, inventoryBrokerTarget{StoreID: InventoryStoreID},
	)); err != nil {
		t.Fatalf("nil-context preflight = %v", err)
	}
	closed := &BrokerHandler{storeID: InventoryStoreID, changed: make(chan struct{}), closed: true}
	if _, err := closed.Handle(t.Context(), inventoryRequest(
		t, inventoryOperationPreflight, inventoryBrokerTarget{StoreID: InventoryStoreID},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed preflight = %v", err)
	}
	withLease := &BrokerHandler{
		storeID: InventoryStoreID, changed: make(chan struct{}),
		lease: &inventoryBrokerLease{id: "lease"},
	}
	if err := withLease.Close(); err != nil {
		t.Fatal(err)
	}
}

func inventoryRequest(t *testing.T, operation string, payload any) database.Request {
	t.Helper()
	raw, err := database.MarshalCanonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	return database.Request{
		Domain: BrokerDomain, Version: BrokerVersion, Operation: operation,
		Payload: json.RawMessage(raw),
	}
}

func scriptedInventoryManager(
	t *testing.T,
	handle func(database.Request) (any, error),
) (*Manager, func()) {
	t.Helper()
	home := t.TempDir()
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home: home,
		Handler: database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
			return handle(request)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := database.Connect(home)
	if err != nil {
		_ = server.Close(t.Context())
		t.Fatal(err)
	}
	manager := &Manager{broker: client, storeID: InventoryStoreID}
	manager.brokerLease = &inventoryBrokerClientLease{id: "lease"}
	return manager, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Error(err)
		}
	}
}

func inventoryOperationError(operation string) func(database.Request) (any, error) {
	return func(request database.Request) (any, error) {
		if request.Operation == operation {
			return nil, database.NewError(database.CodeUnavailable, "scripted failure")
		}
		switch request.Operation {
		case inventoryOperationSaveBegin:
			return inventoryBrokerSaveResponse{}, nil
		case inventoryOperationSaveChunk:
			var input inventoryBrokerSaveChunkRequest
			_ = request.DecodePayload(&input)
			return inventoryBrokerSaveResponse{NextOffset: input.Offset + len(input.Data)}, nil
		default:
			return inventoryBrokerSaveResponse{Updated: true, Generation: 1}, nil
		}
	}
}
