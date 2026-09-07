package sqliteadapter

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/sipeed/picoclaw/internal/channelstore/matrixstore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestBrokerHandlerValidationAndClosePaths(t *testing.T) {
	ctx := t.Context()
	if result, err := (*BrokerHandler)(nil).Handle(ctx, database.Request{}); result != nil ||
		database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("nil Handle() = %#v, %v", result, err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatal(err)
	}
	if err := backendError(nil); err != nil {
		t.Fatal(err)
	}
	original := database.NewError(database.CodeInvalid, "invalid")
	if backendError(original) != original {
		t.Fatal("backendError did not preserve structured error")
	}
	if database.CodeOf(backendError(errors.New("backend"))) != database.CodeUnavailable {
		t.Fatal("backendError did not sanitize backend failure")
	}
	if _, err := mutationResult(errors.New("backend")); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("mutationResult() error = %v", err)
	}
	if database.CodeOf(invalidCodec(nil)) != database.CodeInvalid ||
		database.CodeOf(invalidCodec(errors.New("bad pickle"))) != database.CodeIntegrity {
		t.Fatal("invalidCodec returned wrong error code")
	}

	harness := newMatrixHarness(t)
	handler := harness.handler
	badDomain := brokerRequest("other", matrixstore.Request{})
	badDomain.Domain = "other"
	if _, err := handler.Handle(ctx, badDomain); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("wrong-domain Handle() error = %v", err)
	}
	if _, err := handler.Handle(
		ctx, brokerRequest(matrixstore.ResolveOperation, matrixstore.ResolveRequest{}),
	); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid resolve error = %v", err)
	}
	if _, err := handler.Handle(ctx, brokerRequest(
		matrixstore.ResolveOperation, matrixstore.ResolveRequest{ChannelName: "unknown"},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown resolve error = %v", err)
	}
	if _, err := handler.Handle(ctx, brokerRequest(
		matrixstore.PreflightOperation, matrixstore.StoreTarget{StoreID: "bad store"},
	)); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid preflight error = %v", err)
	}
	if _, err := handler.Handle(
		ctx, brokerRequest("crypto.get-account", matrixstore.Request{}),
	); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid request error = %v", err)
	}
	if _, err := handler.Handle(ctx, brokerRequest("crypto.get-account", matrixstore.Request{
		StoreID: "channel/matrix/unknown", DeviceID: testDeviceID,
	})); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown store error = %v", err)
	}

	bundle, err := handler.bundle(ctx, harness.storeID, testDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handler.bundle(ctx, harness.storeID, "OTHER"); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("device change error = %v", err)
	}
	restore := database.SuspendProviderTestAuthority()
	if _, err = handler.bundle(ctx, harness.storeID, testDeviceID); database.CodeOf(err) != database.CodeUnauthorized {
		restore()
		t.Fatalf("unprivileged bundle error = %v", err)
	}
	restore()

	invalidRequests := []struct {
		operation string
		request   matrixstore.Request
	}{
		{"crypto.put-account", matrixstore.Request{}},
		{"crypto.add-session", matrixstore.Request{}},
		{"crypto.update-session", matrixstore.Request{}},
		{"crypto.delete-session", matrixstore.Request{}},
		{"crypto.put-olm-hash", matrixstore.Request{MessageHash: []byte("short")}},
		{"crypto.put-group-session", matrixstore.Request{}},
		{"crypto.put-withheld-group-session", matrixstore.Request{}},
		{"crypto.get-all-group-sessions", matrixstore.Request{Limit: 0}},
		{"crypto.add-outbound-group-session", matrixstore.Request{}},
		{"crypto.update-outbound-group-session", matrixstore.Request{}},
		{"crypto.put-device", matrixstore.Request{}},
		{"state.set-member", matrixstore.Request{}},
		{"state.set-power-levels", matrixstore.Request{}},
		{"state.set-create", matrixstore.Request{}},
		{"state.set-join-rules", matrixstore.Request{}},
		{"state.set-encryption-event", matrixstore.Request{}},
		{"not-an-operation", matrixstore.Request{}},
	}
	for _, test := range invalidRequests {
		if _, dispatchErr := dispatch(ctx, test.operation, test.request, bundle); dispatchErr == nil {
			t.Errorf("dispatch(%q) unexpectedly succeeded", test.operation)
		}
	}

	if err = handler.Close(); err != nil {
		t.Fatal(err)
	}
	if err = handler.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = handler.Handle(ctx, brokerRequest(
		matrixstore.ResolveOperation, matrixstore.ResolveRequest{ChannelName: "secure"},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed resolve error = %v", err)
	}
	if _, err = handler.bundle(ctx, harness.storeID, testDeviceID); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed bundle error = %v", err)
	}
}

func TestBrokerHandlerEmptyConfigurationPaths(t *testing.T) {
	handler, err := NewBrokerHandler(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := handler.Close(); closeErr != nil {
			t.Errorf("close empty handler: %v", closeErr)
		}
	})

	missingStore := database.StoreID("channel/matrix/missing")
	if _, err = handler.Handle(t.Context(), brokerRequest(
		matrixstore.PreflightOperation,
		matrixstore.StoreTarget{StoreID: missingStore},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("missing-store preflight error = %v", err)
	}

	unconfigured := &BrokerHandler{
		targets: map[database.StoreID]targetConfig{
			missingStore: {path: filepath.Join(t.TempDir(), "store.db")},
		},
		stores: make(map[database.StoreID]*storeBundle),
		pages:  newGroupSessionPages(time.Minute),
	}
	if _, err = unconfigured.bundle(
		t.Context(),
		missingStore,
		testDeviceID,
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("unconfigured bundle error = %v", err)
	}
	if err = unconfigured.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDispatchBackendFailurePaths(t *testing.T) {
	harness := newMatrixHarness(t)
	bundle, err := harness.handler.bundle(t.Context(), harness.storeID, testDeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if err = bundle.database.Close(); err != nil {
		t.Fatal(err)
	}
	validHash := make([]byte, 32)
	requests := []struct {
		operation string
		request   matrixstore.Request
	}{
		{"crypto.get-account", matrixstore.Request{}},
		{"crypto.get-sessions", matrixstore.Request{SenderKey: "sender"}},
		{"crypto.get-latest-session", matrixstore.Request{SenderKey: "sender"}},
		{"crypto.get-newest-session-creation-ts", matrixstore.Request{SenderKey: "sender"}},
		{"crypto.get-olm-hash", matrixstore.Request{MessageHash: validHash}},
		{"crypto.delete-old-olm-hashes", matrixstore.Request{}},
		{"crypto.get-group-session", matrixstore.Request{RoomID: "!room:test", SessionID: "session"}},
		{"crypto.redact-group-session", matrixstore.Request{SessionID: "session"}},
		{"crypto.redact-group-sessions", matrixstore.Request{RoomID: "!room:test"}},
		{"crypto.redact-expired-group-sessions", matrixstore.Request{}},
		{"crypto.redact-outdated-group-sessions", matrixstore.Request{}},
		{"crypto.get-withheld-group-session", matrixstore.Request{RoomID: "!room:test", SessionID: "session"}},
		{"crypto.get-all-group-sessions", matrixstore.Request{Limit: 1}},
		{"crypto.get-outbound-group-session", matrixstore.Request{RoomID: "!room:test"}},
		{"crypto.remove-outbound-group-session", matrixstore.Request{RoomID: "!room:test"}},
		{"crypto.is-outbound-group-session-shared", matrixstore.Request{}},
		{"crypto.validate-message-index", matrixstore.Request{}},
		{"crypto.get-devices", matrixstore.Request{UserID: "@user:test"}},
		{"crypto.get-device", matrixstore.Request{UserID: "@user:test", TargetDeviceID: "D"}},
		{"crypto.find-device-by-key", matrixstore.Request{UserID: "@user:test"}},
		{"crypto.filter-tracked-users", matrixstore.Request{Users: []id.UserID{"@user:test"}}},
		{"crypto.get-outdated-tracked-users", matrixstore.Request{}},
		{"crypto.get-cross-signing-keys", matrixstore.Request{UserID: "@user:test"}},
		{"crypto.is-key-signed-by", matrixstore.Request{}},
		{"crypto.drop-signatures-by-key", matrixstore.Request{}},
		{"crypto.get-signatures-for-key-by", matrixstore.Request{}},
		{"crypto.get-secret", matrixstore.Request{SecretName: "secret"}},
		{"sync.load-next-batch", matrixstore.Request{}},
		{"state.get-member", matrixstore.Request{RoomID: "!room:test", UserID: "@user:test"}},
		{"state.try-get-member", matrixstore.Request{RoomID: "!room:test", UserID: "@user:test"}},
		{"state.set-membership", matrixstore.Request{}},
		{"state.set-member", matrixstore.Request{Member: &event.MemberEventContent{}}},
		{"state.is-confusable-name", matrixstore.Request{}},
		{"state.clear-cached-members", matrixstore.Request{}},
		{"state.replace-cached-members", matrixstore.Request{}},
		{"state.set-power-levels", matrixstore.Request{PowerLevels: &event.PowerLevelsEventContent{}}},
		{"state.get-power-levels", matrixstore.Request{}},
		{"state.set-create", matrixstore.Request{Create: &event.Event{Type: event.StateCreate}}},
		{"state.get-create", matrixstore.Request{}},
		{"state.get-join-rules", matrixstore.Request{}},
		{"state.set-join-rules", matrixstore.Request{JoinRules: &event.JoinRulesEventContent{}}},
		{"state.has-fetched-members", matrixstore.Request{}},
		{"state.mark-members-fetched", matrixstore.Request{}},
		{"state.get-all-members", matrixstore.Request{}},
		{"state.set-encryption-event", matrixstore.Request{Encryption: &event.EncryptionEventContent{}}},
		{"state.get-encryption-event", matrixstore.Request{}},
		{"state.is-encrypted", matrixstore.Request{}},
		{"state.find-shared-rooms", matrixstore.Request{}},
		{"state.get-room-joined-or-invited-members", matrixstore.Request{}},
	}
	for _, test := range requests {
		if _, dispatchErr := dispatch(t.Context(), test.operation, test.request, bundle); dispatchErr == nil {
			t.Errorf("dispatch(%q) on closed backend unexpectedly succeeded", test.operation)
		}
	}
}

func TestMigrationAdapterFaultPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix", "store.db")
	if err := MigrateDatabase(t.Context(), path); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unfenced migration error = %v", err)
	}
	home := t.TempDir()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	injected := errors.New("injected")
	err = MigrateDatabaseWithCheckpoint(
		t.Context(), filepath.Join(home, "matrix", "store.db"),
		func(phase string) error {
			if phase == MigrationAfterCrypto {
				return injected
			}
			return nil
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("checkpoint migration error = %v", err)
	}
}

func brokerRequest(operation string, payload any) database.Request {
	raw, err := database.MarshalCanonical(payload)
	if err != nil {
		panic(err)
	}
	return database.Request{
		Domain: matrixstore.Domain, Version: matrixstore.Version,
		Operation: operation, Payload: raw,
	}
}
