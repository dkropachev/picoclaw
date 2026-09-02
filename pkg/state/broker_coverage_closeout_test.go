//nolint:govet // Independent boundary assertions intentionally reuse narrow error names.
package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCoverageRuntimeStateBrokerClientAndHandlerBoundaries(t *testing.T) {
	if manager, err := newBrokerManager(" ", &database.Client{}); manager != nil ||
		database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("blank broker manager = %#v, %v", manager, err)
	}
	if manager, err := newBrokerManager("workspace", nil); manager != nil ||
		database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("nil-client broker manager = %#v, %v", manager, err)
	}
	for _, manager := range []*Manager{
		nil,
		{},
		{broker: &database.Client{}, brokerErr: database.NewError(database.CodeConflict, "blocked")},
		{broker: &database.Client{}, storeID: "bad id"},
	} {
		if _, err := manager.brokerSnapshot(t.Context()); err == nil {
			t.Fatalf("invalid manager snapshot succeeded: %#v", manager)
		}
		if err := manager.brokerUpdate(t.Context(), "last_channel", "value"); err == nil {
			t.Fatalf("invalid manager update succeeded: %#v", manager)
		}
	}
	manager := &Manager{broker: &database.Client{}, storeID: RuntimeStateStoreID}
	if err := manager.brokerUpdate(t.Context(), "unsupported", "value"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid broker field = %v", err)
	}

	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{Workspace: workspace}}}
	handler, err := NewBrokerHandler(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	if _, err := (*BrokerHandler)(
		nil,
	).Handle(t.Context(), database.Request{}); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("nil handler = %v", err)
	}
	if _, err := handler.Handle(
		t.Context(),
		database.Request{Domain: "other", Version: 1},
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("foreign domain = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handler.Handle(canceled, runtimeBrokerTestRequest(
		t, runtimeStateOperationPreflight, runtimeStateTarget{StoreID: RuntimeStateStoreID},
	)); database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("canceled handler = %v", err)
	}
	for _, payload := range [][]byte{[]byte(`{`), []byte(`{"workspace_selector":""}`)} {
		if _, err := handler.Handle(nil, database.Request{
			Domain: RuntimeStateDomain, Version: RuntimeStateVersion,
			Operation: runtimeStateOperationResolveStore, Payload: payload,
		}); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("invalid resolve payload %q = %v", payload, err)
		}
	}
	if _, err := handler.Handle(t.Context(), runtimeBrokerTestRequest(
		t, runtimeStateOperationResolveStore,
		runtimeStateResolveRequest{WorkspaceSelector: "unknown"},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown selector = %v", err)
	}
	selector, err := runtimeWorkspaceSelector(workspace)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := handler.Handle(t.Context(), runtimeBrokerTestRequest(
		t, runtimeStateOperationResolveStore,
		runtimeStateResolveRequest{WorkspaceSelector: selector},
	))
	if err != nil || resolved.(runtimeStateResolveResponse).StoreID != RuntimeStateStoreID {
		t.Fatalf("resolved store = %#v, %v", resolved, err)
	}
	for _, request := range []database.Request{
		{Domain: RuntimeStateDomain, Version: RuntimeStateVersion, Operation: "unknown", Payload: []byte(`{}`)},
		runtimeBrokerTestRequest(t, runtimeStateOperationPreflight, runtimeStateTarget{StoreID: "global/auth"}),
		runtimeBrokerTestRequest(t, runtimeStateOperationSnapshot, runtimeStateTarget{StoreID: "global/auth"}),
		runtimeBrokerTestRequest(t, runtimeStateOperationSetLastChannel,
			runtimeStateSetRequest{StoreID: RuntimeStateStoreID, Value: "bad\x00value"}),
	} {
		if _, err := handler.Handle(t.Context(), request); err == nil {
			t.Fatalf("invalid runtime request succeeded: %#v", request)
		}
	}
	if _, err := handler.Handle(t.Context(), runtimeBrokerTestRequest(
		t, "unknown", runtimeStateTarget{StoreID: RuntimeStateStoreID},
	)); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("unknown operation = %v", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Handle(t.Context(), runtimeBrokerTestRequest(
		t, runtimeStateOperationSnapshot, runtimeStateTarget{StoreID: RuntimeStateStoreID},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed handler = %v", err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCoverageRuntimeStateConfigurationAndErrorMapping(t *testing.T) {
	if _, err := configuredRuntimeWorkspaces("bad\x00home", nil); err == nil {
		t.Fatal("invalid runtime-state home accepted")
	}
	home := t.TempDir()
	configured, err := configuredRuntimeWorkspaces(home, nil)
	if err != nil || len(configured) != 1 || configured[0].storeID != RuntimeStateStoreID {
		t.Fatalf("default runtime workspaces = %#v, %v", configured, err)
	}
	defaultWorkspace, err := resolveRuntimeWorkspace(home, "")
	if err != nil || defaultWorkspace != filepath.Join(home, "workspace") {
		t.Fatalf("default workspace = %q, %v", defaultWorkspace, err)
	}
	relative, err := resolveRuntimeWorkspace(home, "relative")
	if err != nil || relative != filepath.Join(home, "relative") {
		t.Fatalf("relative workspace = %q, %v", relative, err)
	}
	userHome, err := os.UserHomeDir()
	if err == nil {
		resolved, resolveErr := resolveRuntimeWorkspace(home, "~/nested")
		if resolveErr != nil || resolved != filepath.Join(userHome, "nested") {
			t.Fatalf("home workspace = %q, %v", resolved, resolveErr)
		}
	}
	for _, value := range []string{"", "bad\x00workspace"} {
		if _, err := runtimeWorkspaceSelector(value); database.CodeOf(err) != database.CodeInvalid {
			t.Fatalf("selector %q = %v", value, err)
		}
	}
	if _, err := runtimeRequestStoreID(
		database.Request{Payload: []byte(`{}`)},
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("missing request StoreID = %v", err)
	}
	if id, err := runtimeRequestStoreID(runtimeBrokerTestRequest(
		t, "operation", runtimeStateTarget{StoreID: RuntimeStateStoreID},
	)); err != nil || id != RuntimeStateStoreID {
		t.Fatalf("request StoreID = %q, %v", id, err)
	}
	for _, test := range []struct {
		err  error
		code database.ErrorCode
	}{
		{err: nil, code: ""},
		{err: database.NewError(database.CodeMigrationRequired, "migrate"), code: database.CodeMigrationRequired},
		{err: context.Canceled, code: database.CodeDeadline},
		{err: errRuntimeStateVersionChanged, code: database.CodeConflict},
		{err: sqlitestore.ErrTooNew, code: database.CodeUnsupported},
		{err: sqlitestore.ErrInvalidSchema, code: database.CodeIntegrity},
		{err: sqlitestore.ErrIntegrity, code: database.CodeIntegrity},
		{err: os.ErrPermission, code: database.CodeUnavailable},
		{err: errors.New("plain"), code: database.CodeInternal},
	} {
		mapped := mapRuntimeStateBrokerError(test.err)
		if database.CodeOf(mapped) != test.code {
			t.Fatalf("map %v = %v, want %s", test.err, mapped, test.code)
		}
	}
}

func TestCoverageRuntimeStateHandlerRequiresAuthority(t *testing.T) {
	restore := database.SuspendProviderTestAuthority()
	allowUnfencedRuntimeStateProviderForTests.Store(false)
	t.Cleanup(func() {
		allowUnfencedRuntimeStateProviderForTests.Store(true)
		restore()
	})
	if handler, err := NewBrokerHandler(t.TempDir(), nil); handler != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unfenced handler = %#v, %v", handler, err)
	}
}
