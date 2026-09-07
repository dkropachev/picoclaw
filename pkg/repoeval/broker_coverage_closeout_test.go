package repoeval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

//nolint:govet // Contract assertions intentionally keep each operation error local.
func TestEvaluationBrokerMutationBulkDeleteAndErrorContracts(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newEvaluationBrokerHandlerForTest(t, home, workspace)
	server := startEvaluationBroker(t, home, handler)
	defer closeEvaluationBroker(t, server)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setEvaluationBrokerClientForTest(t, client)
	store := NewSQLiteStore(workspace)

	created := make([]Evaluation, 0, 4)
	for range 4 {
		value, err := store.Create(t.Context(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, value)
	}
	items, err := store.List(t.Context())
	if err != nil || len(items) != len(created) {
		t.Fatalf("paginated List() = %d, %v", len(items), err)
	}
	if _, found, err := store.Get(t.Context(), "missing"); err != nil || found {
		t.Fatalf("missing Get() = %v, %v", found, err)
	}
	if _, err := store.Update(
		t.Context(),
		created[0].ID,
		created[0].Version,
		nil,
	); !errors.Is(
		err,
		ErrInvalidEvaluation,
	) {
		t.Fatalf("nil Update() = %v", err)
	}
	if _, err := store.Update(
		t.Context(),
		"missing",
		1,
		func(*Evaluation) error { return nil },
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing Update() = %v", err)
	}
	if _, err := store.Update(
		t.Context(),
		created[0].ID,
		created[0].Version+1,
		func(*Evaluation) error { return nil },
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale Update() = %v", err)
	}
	mutationErr := errors.New("mutation stopped")
	if _, err := store.Update(
		t.Context(),
		created[0].ID,
		created[0].Version,
		func(*Evaluation) error { return mutationErr },
	); !errors.Is(
		err,
		mutationErr,
	) {
		t.Fatalf("callback Update() = %v", err)
	}
	updated, err := store.Update(
		t.Context(), created[0].ID, created[0].Version,
		func(value *Evaluation) error { value.Focus.FreeText = "broker coverage"; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(t.Context(), updated.ID, updated.Version+1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Delete() = %v", err)
	}
	if err := store.Delete(t.Context(), "missing", 1); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing Delete() = %v", err)
	}
	result, err := store.BulkDelete(t.Context(), []BulkDeleteItem{
		{ID: updated.ID, Version: updated.Version},
		{ID: created[1].ID, Version: created[1].Version},
	})
	if err != nil || len(result.DeletedIDs) != 2 {
		t.Fatalf("BulkDelete() = %#v, %v", result, err)
	}
	if err := store.Delete(t.Context(), created[2].ID, created[2].Version); err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct {
		err  error
		code database.ErrorCode
	}{
		{context.Canceled, database.CodeDeadline},
		{ErrConflict, database.CodeConflict},
		{ErrInvalidTransition, database.CodeUnsupported},
		{ErrInvalidEvaluation, database.CodeInvalid},
		{os.ErrNotExist, database.CodeNotFound},
		{sqlitestore.ErrTooNew, database.CodeUnsupported},
		{sqlitestore.ErrInvalidSchema, database.CodeIntegrity},
		{errors.New("opaque"), database.CodeInternal},
	} {
		if got := database.CodeOf(mapEvaluationBrokerError(item.err)); got != item.code {
			t.Errorf("mapEvaluationBrokerError(%v) = %s, want %s", item.err, got, item.code)
		}
	}
	for _, item := range []struct {
		code database.ErrorCode
		want error
	}{
		{database.CodeConflict, ErrConflict},
		{database.CodeInvalid, ErrInvalidEvaluation},
		{database.CodeNotFound, os.ErrNotExist},
	} {
		got := mapEvaluationClientError(database.NewError(item.code, "mapped"))
		if !errors.Is(got, item.want) {
			t.Errorf("mapEvaluationClientError(%s) = %v", item.code, got)
		}
	}
	if got := mapEvaluationMutationClientError(
		database.NewError(database.CodeUnsupported, "transition"),
	); !errors.Is(
		got,
		ErrInvalidTransition,
	) {
		t.Fatalf("mutation client mapping = %v", got)
	}
}

//nolint:govet // Invalid-operation table assertions intentionally bind local errors.
func TestEvaluationBrokerHandlerValidationPaginationAndLeaseLifecycle(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newEvaluationBrokerHandlerForTest(t, home, workspace)
	child := handler.workspaces[EvaluationStoreID]
	t.Cleanup(func() { _ = handler.Close() })

	createdAny, err := child.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationCreate,
		evaluationCreateRequest{StoreID: EvaluationStoreID, Input: validCreateRequest()},
	))
	if err != nil {
		t.Fatal(err)
	}
	created := createdAny.(evaluationResponse).Evaluation
	pageAny, err := child.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationList,
		evaluationListRequest{StoreID: EvaluationStoreID, Offset: 99, Limit: 1},
	))
	if err != nil || len(pageAny.(evaluationListResponse).Items) != 0 || !pageAny.(evaluationListResponse).Done {
		t.Fatalf("empty list page = %#v, %v", pageAny, err)
	}

	invalid := []database.Request{
		{
			Domain:    "wrong",
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationGet,
			Payload:   json.RawMessage(`{}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationPreflight,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationCreate,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationGet,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationList,
			Payload: mustEvaluationBrokerPayload(
				t,
				evaluationListRequest{StoreID: EvaluationStoreID, Offset: -1, Limit: 1},
			),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationList,
			Payload: mustEvaluationBrokerPayload(
				t,
				evaluationListRequest{StoreID: EvaluationStoreID, Limit: evaluationBrokerPageSize + 1},
			),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationUpdate,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationDelete,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationBulkDelete,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationLock,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationUnlock,
			Payload:   json.RawMessage(`{"store_id":"workspace/repository-evaluations","lease_id":""}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: evaluationOperationRenewLease,
			Payload:   json.RawMessage(`{"store_id":"workspace/repository-evaluations","lease_id":""}`),
		},
		{
			Domain:    evaluationBrokerDomain,
			Version:   evaluationBrokerVersion,
			Operation: "unknown",
			Payload:   json.RawMessage(`{}`),
		},
	}
	for _, request := range invalid {
		if _, err := child.Handle(t.Context(), request); err == nil {
			t.Errorf("operation %q accepted invalid request", request.Operation)
		}
	}

	leaseAny, err := child.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationLock, evaluationTarget{StoreID: EvaluationStoreID},
	))
	if err != nil {
		t.Fatal(err)
	}
	lease := leaseAny.(evaluationLeaseResponse)
	renewed, err := child.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationRenewLease,
		evaluationLeaseRequest{StoreID: EvaluationStoreID, LeaseID: lease.LeaseID},
	))
	if err != nil || renewed.(evaluationLeaseResponse).LeaseID != lease.LeaseID {
		t.Fatalf("renew lease = %#v, %v", renewed, err)
	}
	if _, err := child.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationUnlock,
		evaluationLeaseRequest{StoreID: EvaluationStoreID, LeaseID: lease.LeaseID},
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := child.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationUnlock,
		evaluationLeaseRequest{StoreID: EvaluationStoreID, LeaseID: lease.LeaseID},
	)); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("repeat unlock = %v", err)
	}
	if _, err := child.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationRenewLease,
		evaluationLeaseRequest{StoreID: EvaluationStoreID, LeaseID: "missing"},
	)); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("missing renew = %v", err)
	}

	released := 0
	registered := &evaluationLease{release: func() { released++ }}
	if err := child.registerLease("collision", registered); err != nil {
		t.Fatal(err)
	}
	if err := child.registerLease("collision", &evaluationLease{}); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("duplicate lease registration = %v", err)
	}
	child.mu.Lock()
	generation := registered.generation
	child.mu.Unlock()
	child.expireLease("collision", registered, generation+1)
	if released != 0 {
		t.Fatal("stale generation released active lease")
	}
	child.expireLease("collision", registered, generation)
	registered.releaseNow()
	if released != 1 {
		t.Fatalf("lease release count = %d", released)
	}
	if child.effectiveLeaseTTL() <= 0 || (*evaluationStoreHandler)(nil).effectiveLeaseTTL() <= 0 {
		t.Fatal("lease TTL fallback is not positive")
	}

	if _, err := child.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationDelete,
		evaluationDeleteRequest{StoreID: EvaluationStoreID, ID: created.ID, ExpectedVersion: created.Version},
	)); err != nil {
		t.Fatal(err)
	}
	closed := newEvaluationStoreHandler(workspace, EvaluationStoreID)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closed.registerLease("closed", &evaluationLease{}); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed lease registration = %v", err)
	}
	if _, err := closed.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationPreflight, evaluationTarget{StoreID: EvaluationStoreID},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed handler request = %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	<-child.requestGate
	if _, err := child.Handle(
		canceled,
		database.Request{Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion},
	); database.CodeOf(
		err,
	) != database.CodeDeadline {
		t.Fatalf("canceled handler request = %v", err)
	}
	child.requestGate <- struct{}{}

	if _, err := handler.Handle(
		t.Context(),
		database.Request{Domain: "wrong"},
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("outer unsupported request = %v", err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion,
		Operation: evaluationOperationResolveStore, Payload: json.RawMessage(`{"workspace_selector":""}`),
	}); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid selector = %v", err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion,
		Operation: evaluationOperationResolveStore, Payload: json.RawMessage(`{"workspace_selector":"missing"}`),
	}); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown selector = %v", err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion,
		Operation: evaluationOperationGet, Payload: json.RawMessage(`{"store_id":"workspace/unknown"}`),
	}); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown store = %v", err)
	}

	child.mu.Lock()
	child.leaseTTL = time.Millisecond
	child.mu.Unlock()
}

func TestEvaluationBrokerClientAndWorkspaceFailClosedBoundaries(t *testing.T) {
	sentinel := database.NewError(database.CodeUnavailable, "broker unavailable")
	failed := Store{brokerErr: sentinel, brokerState: &evaluationBrokerClientState{}}
	if _, err := failed.brokerCreate(t.Context(), validCreateRequest()); !errors.Is(err, sentinel) {
		t.Errorf("brokerCreate() = %v", err)
	}
	if _, _, err := failed.brokerGet(t.Context(), "id"); !errors.Is(err, sentinel) {
		t.Errorf("brokerGet() = %v", err)
	}
	if _, err := failed.brokerList(t.Context()); !errors.Is(err, sentinel) {
		t.Errorf("brokerList() = %v", err)
	}
	if _, err := failed.brokerUpdate(
		t.Context(),
		"id",
		1,
		func(*Evaluation) error { return nil },
	); !errors.Is(
		err,
		sentinel,
	) {
		t.Errorf("brokerUpdate() = %v", err)
	}
	if err := failed.brokerDelete(t.Context(), "id", 1); !errors.Is(err, sentinel) {
		t.Errorf("brokerDelete() = %v", err)
	}
	if _, err := failed.brokerBulkDelete(
		t.Context(),
		[]BulkDeleteItem{{ID: "id", Version: 1}},
	); !errors.Is(
		err,
		sentinel,
	) {
		t.Errorf("brokerBulkDelete() = %v", err)
	}
	if _, err := failed.brokerLockController(); !errors.Is(err, sentinel) {
		t.Errorf("brokerLockController() = %v", err)
	}
	if err := failed.Preflight(t.Context()); !errors.Is(err, sentinel) {
		t.Errorf("Preflight() = %v", err)
	}
	if failed.StoreID() != "" {
		t.Fatal("failed broker store exposed a StoreID")
	}

	var withoutState Store
	withoutState.recordBrokerLeaseError(sentinel)
	if err := withoutState.consumeBrokerLeaseError(); err != nil {
		t.Fatalf("nil broker state error = %v", err)
	}
	failed.recordBrokerLeaseError(nil)
	failed.recordBrokerLeaseError(sentinel)
	failed.recordBrokerLeaseError(database.NewError(database.CodeInternal, "later"))
	if err := failed.consumeBrokerLeaseError(); !errors.Is(err, sentinel) {
		t.Fatalf("recorded broker error = %v", err)
	}
	if err := failed.consumeBrokerLeaseError(); err != nil {
		t.Fatalf("consumed broker error persisted = %v", err)
	}
	if mapEvaluationBrokerError(nil) != nil || mapEvaluationClientError(nil) != nil {
		t.Fatal("nil error mapping changed nil")
	}

	if _, err := resolveEvaluationBrokerStoreID(
		t.Context(),
		nil,
		"workspace",
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("nil broker resolution = %v", err)
	}
	if _, err := evaluationWorkspaceSelector(""); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("empty workspace selector = %v", err)
	}
	if _, err := evaluationWorkspaceSelector("bad\x00workspace"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("NUL workspace selector = %v", err)
	}
	if _, err := evaluationRequestStoreID(
		database.Request{Payload: json.RawMessage(`{}`)},
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("missing request StoreID = %v", err)
	}
	home := t.TempDir()
	for _, configured := range []string{"", "relative", home, "~"} {
		if resolved, err := resolveEvaluationWorkspace(home, configured); err != nil || !filepath.IsAbs(resolved) {
			t.Errorf("resolveEvaluationWorkspace(%q) = %q, %v", configured, resolved, err)
		}
	}
	configured, err := configuredEvaluationWorkspaces(home, nil)
	if err != nil || len(configured) != 1 {
		t.Fatalf("configuredEvaluationWorkspaces(nil) = %#v, %v", configured, err)
	}

	handler := newEvaluationBrokerHandlerForTest(t, home, filepath.Join(home, "workspace"))
	selector := ""
	for value := range handler.selectors {
		selector = value
	}
	resolved, err := handler.Handle(nil, database.Request{
		Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion,
		Operation: evaluationOperationResolveStore,
		Payload:   mustEvaluationBrokerPayload(t, evaluationResolveStoreRequest{WorkspaceSelector: selector}),
	})
	if err != nil || !resolved.(evaluationResolveStoreResponse).StoreID.Valid() {
		t.Fatalf("nil-context resolve = %#v, %v", resolved, err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion,
		Operation: evaluationOperationResolveStore,
		Payload:   mustEvaluationBrokerPayload(t, evaluationResolveStoreRequest{WorkspaceSelector: selector}),
	}); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed resolver = %v", err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil handler Close() = %v", err)
	}
	if err := (*evaluationStoreHandler)(nil).Close(); err != nil {
		t.Fatalf("nil store handler Close() = %v", err)
	}
	if _, err := NewBrokerHandler("bad\x00home", nil); err == nil {
		t.Fatal("broker accepted invalid home")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := handler.Handle(
		canceled,
		database.Request{Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion},
	); database.CodeOf(
		err,
	) != database.CodeDeadline {
		t.Fatalf("outer canceled request = %v", err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion,
		Operation: evaluationOperationGet, Payload: json.RawMessage(`{}`),
	}); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid outer store request = %v", err)
	}
	if _, err := handler.Handle(t.Context(), evaluationRequest(
		t, evaluationOperationGet,
		evaluationIDRequest{StoreID: EvaluationStoreID, ID: "missing"},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed outer store request = %v", err)
	}
	if _, err := resolveEvaluationBrokerStoreID(
		t.Context(),
		&database.Client{},
		"",
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("invalid workspace resolution = %v", err)
	}
	if _, err := resolveEvaluationWorkspace(home, "~/child"); err != nil {
		t.Fatalf("home-relative workspace = %v", err)
	}
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: filepath.Join(home, "workspace")},
		List: []config.AgentConfig{
			{ID: "blank", Workspace: " "},
			{ID: "duplicate", Workspace: filepath.Join(home, "workspace")},
			{ID: "second", Workspace: filepath.Join(home, "second")},
		},
	}}
	if workspaces, err := configuredEvaluationWorkspaces(home, cfg); err != nil || len(workspaces) != 2 {
		t.Fatalf("configured workspace filtering = %#v, %v", workspaces, err)
	}
}

func TestEvaluationBrokerMalformedResponsesAndProviderFailures(t *testing.T) {
	t.Run("responses", func(t *testing.T) {
		client, closeBroker := startEvaluationScriptedBroker(t, func(request database.Request) (any, error) {
			switch request.Operation {
			case evaluationOperationList:
				return evaluationListResponse{Items: []Evaluation{{}}, Done: false}, nil
			case evaluationOperationDelete:
				return evaluationMutationResponse{Updated: false}, nil
			case evaluationOperationLock:
				return evaluationLeaseResponse{}, nil
			case evaluationOperationUnlock:
				return evaluationMutationResponse{Updated: false}, nil
			case evaluationOperationRenewLease:
				return evaluationLeaseResponse{LeaseID: "wrong", TTLNanoSeconds: 1}, nil
			default:
				return nil, database.NewError(database.CodeUnauthorized, "rejected")
			}
		})
		defer closeBroker()
		store := Store{
			broker: client, storeID: EvaluationStoreID,
			brokerState: &evaluationBrokerClientState{},
		}
		if err := store.Preflight(t.Context()); database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("rejected broker preflight = %v", err)
		}
		store.brokerState.err = database.NewError(database.CodeConflict, "lost lease")
		if _, err := store.brokerLockController(); database.CodeOf(err) != database.CodeConflict {
			t.Fatalf("remembered lease error = %v", err)
		}
		if _, err := store.brokerList(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("short unfinished list = %v", err)
		}
		if err := store.brokerDelete(t.Context(), "id", 1); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("false delete response = %v", err)
		}
		if _, err := store.brokerLockController(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("invalid lock response = %v", err)
		}
		if _, err := store.brokerUpdate(
			t.Context(),
			"id",
			1,
			func(*Evaluation) error { return nil },
		); database.CodeOf(
			err,
		) != database.CodeUnauthorized {
			t.Fatalf("failed update lookup = %v", err)
		}
		release := store.newBrokerLeaseRelease("lease", 0)
		release()
		release()
		if err := store.BrokerLeaseError(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("invalid unlock response = %v", err)
		}
		done := make(chan struct{})
		store.renewBrokerLease(make(chan struct{}), done, "lease", time.Millisecond)
		<-done
		if err := store.BrokerLeaseError(); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("invalid renewal response = %v", err)
		}
	})

	t.Run("errors", func(t *testing.T) {
		client, closeBroker := startEvaluationScriptedBroker(t, func(database.Request) (any, error) {
			return nil, database.NewError(database.CodeUnauthorized, "rejected")
		})
		defer closeBroker()
		store := Store{
			broker: client, storeID: EvaluationStoreID,
			brokerState: &evaluationBrokerClientState{},
		}
		if _, err := store.brokerList(t.Context()); database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("failed list = %v", err)
		}
		if _, err := store.brokerLockController(); database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("failed controller lock = %v", err)
		}
	})

	t.Run("oversized list", func(t *testing.T) {
		client, closeBroker := startEvaluationScriptedBroker(t, func(request database.Request) (any, error) {
			return evaluationListResponse{
				Items: make([]Evaluation, evaluationBrokerPageSize+1), Done: true,
			}, nil
		})
		defer closeBroker()
		store := Store{broker: client, storeID: EvaluationStoreID}
		if _, err := store.brokerList(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
			t.Fatalf("oversized list = %v", err)
		}
	})

	t.Run("provider open", func(t *testing.T) {
		child := newEvaluationStoreHandler(t.TempDir(), EvaluationStoreID)
		child.once.Do(func() { child.err = errors.New("provider failed") })
		for _, request := range []database.Request{
			evaluationRequest(t, evaluationOperationPreflight, evaluationTarget{StoreID: EvaluationStoreID}),
			evaluationRequest(t, evaluationOperationCreate, evaluationCreateRequest{StoreID: EvaluationStoreID, Input: validCreateRequest()}),
			evaluationRequest(t, evaluationOperationGet, evaluationIDRequest{StoreID: EvaluationStoreID, ID: "id"}),
			evaluationRequest(t, evaluationOperationList, evaluationListRequest{StoreID: EvaluationStoreID, Limit: 1}),
			evaluationRequest(t, evaluationOperationUpdate, evaluationUpdateRequest{StoreID: EvaluationStoreID}),
			evaluationRequest(t, evaluationOperationDelete, evaluationDeleteRequest{StoreID: EvaluationStoreID}),
			evaluationRequest(t, evaluationOperationBulkDelete, evaluationBulkDeleteRequest{StoreID: EvaluationStoreID}),
			evaluationRequest(t, evaluationOperationLock, evaluationTarget{StoreID: EvaluationStoreID}),
		} {
			if _, err := child.Handle(t.Context(), request); database.CodeOf(err) != database.CodeInternal {
				t.Errorf("%s provider failure = %v", request.Operation, err)
			}
		}
		if err := child.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("domain errors", func(t *testing.T) {
		child := newEvaluationStoreHandler(t.TempDir(), EvaluationStoreID)
		t.Cleanup(func() { _ = child.Close() })
		if _, err := child.Handle(nil, evaluationRequest(
			t, evaluationOperationPreflight, evaluationTarget{StoreID: EvaluationStoreID},
		)); err != nil {
			t.Fatalf("nil-context preflight = %v", err)
		}
		for _, request := range []database.Request{
			evaluationRequest(t, evaluationOperationCreate, evaluationCreateRequest{StoreID: EvaluationStoreID}),
			evaluationRequest(t, evaluationOperationUpdate, evaluationUpdateRequest{StoreID: EvaluationStoreID}),
			evaluationRequest(t, evaluationOperationBulkDelete, evaluationBulkDeleteRequest{StoreID: EvaluationStoreID}),
		} {
			if _, err := child.Handle(t.Context(), request); err == nil {
				t.Errorf("%s accepted invalid domain value", request.Operation)
			}
		}
		child.store.retained.mu.Lock()
		if err := child.store.retained.db.Close(); err != nil {
			child.store.retained.mu.Unlock()
			t.Fatal(err)
		}
		child.store.retained.mu.Unlock()
		missingID, err := randomEvaluationID()
		if err != nil {
			t.Fatal(err)
		}
		for _, request := range []database.Request{
			evaluationRequest(t, evaluationOperationGet, evaluationIDRequest{StoreID: EvaluationStoreID, ID: missingID}),
			evaluationRequest(t, evaluationOperationList, evaluationListRequest{StoreID: EvaluationStoreID, Limit: 1}),
		} {
			if _, err := child.Handle(t.Context(), request); err == nil {
				t.Errorf("%s accepted a closed provider pool", request.Operation)
			}
		}
	})

	t.Run("handler close releases leases", func(t *testing.T) {
		released := 0
		child := newEvaluationStoreHandler(t.TempDir(), EvaluationStoreID)
		for _, id := range []string{"first", "second"} {
			lease := &evaluationLease{
				release: func() { released++ }, timer: time.NewTimer(time.Hour),
			}
			child.leases[id] = lease
		}
		if err := child.Close(); err != nil {
			t.Fatal(err)
		}
		if released != 2 {
			t.Fatalf("released leases = %d", released)
		}
	})

	t.Run("local provider lifecycle", func(t *testing.T) {
		workspace := t.TempDir()
		local := newSQLiteStoreLocal(workspace)
		if err := local.Preflight(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := local.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := (Store{broker: &database.Client{}}).open(
			t.Context(),
		); database.CodeOf(
			err,
		) != database.CodeUnsupported {
			t.Fatalf("broker-routed local open = %v", err)
		}
		if _, _, err := (Store{brokerErr: database.NewError(database.CodeUnavailable, "failed")}).acquire(
			t.Context(),
		); database.CodeOf(
			err,
		) != database.CodeUnavailable {
			t.Fatalf("failed acquire = %v", err)
		}
		closedRetained := Store{retained: &retainedEvaluationDatabase{closed: true}}
		if _, _, err := closedRetained.acquire(t.Context()); err == nil {
			t.Fatal("closed retained store was acquired")
		}
		nilPool := Store{retained: &retainedEvaluationDatabase{}}
		if err := nilPool.Close(); err != nil {
			t.Fatal(err)
		}
		if err := nilPool.Close(); err != nil {
			t.Fatal(err)
		}
		if err := (Store{}).Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := (Store{brokerErr: database.NewError(database.CodeUnavailable, "failed")}).lock(); database.CodeOf(
			err,
		) != database.CodeUnavailable {
			t.Fatalf("failed lock = %v", err)
		}
		first := newSQLiteStoreLocal(workspace)
		first.brokerOwned = true
		second := newSQLiteStoreLocal(workspace)
		second.brokerOwned = true
		unlock, err := first.lock()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := second.lock(); !errors.Is(err, ErrConflict) {
			t.Fatalf("broker-owned lock conflict = %v", err)
		}
		unlock()
		if got := (Store{storeID: "workspace/custom"}).StoreID(); got != "workspace/custom" {
			t.Fatalf("local StoreID = %q", got)
		}
		if got := (Store{}).StoreID(); got != EvaluationStoreID {
			t.Fatalf("default StoreID = %q", got)
		}
	})

	t.Run("offline migration", func(t *testing.T) {
		if err := RunOfflineDatabaseMigration(t.Context(), t.TempDir()); database.CodeOf(err) != database.CodeConflict {
			t.Fatalf("unfenced migration = %v", err)
		}
		home := t.TempDir()
		fence, err := database.AcquireMigrationFence(home)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := fence.Close(); err != nil {
				t.Error(err)
			}
		}()
		if err := RunOfflineDatabaseMigration(t.Context(), filepath.Join(home, "workspace")); err != nil {
			t.Fatal(err)
		}
		fileWorkspace := filepath.Join(home, "not-a-directory")
		if err := os.WriteFile(fileWorkspace, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RunOfflineDatabaseMigration(t.Context(), fileWorkspace); err == nil {
			t.Fatal("migration accepted a file workspace")
		}
	})

	t.Run("provider authority and open failure", func(t *testing.T) {
		restoreAuthority := database.SuspendProviderTestAuthority()
		allowUnfencedEvaluationProviderForTests.Store(false)
		authorityErr := evaluationProviderAuthorityError()
		_, err := newRetainedEvaluationStore(t.TempDir())
		allowUnfencedEvaluationProviderForTests.Store(true)
		restoreAuthority()
		if database.CodeOf(authorityErr) != database.CodeUnauthorized {
			t.Fatalf("unfenced provider authority = %v", authorityErr)
		}
		if database.CodeOf(err) != database.CodeUnauthorized {
			t.Fatalf("unfenced retained provider = %v", err)
		}
		workspaceFile := filepath.Join(t.TempDir(), "workspace-file")
		if err := os.WriteFile(workspaceFile, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newRetainedEvaluationStore(workspaceFile); err == nil {
			t.Fatal("retained provider accepted a file workspace")
		}
	})

	t.Run("workspace resolution failures", func(t *testing.T) {
		client, closeBroker := startEvaluationScriptedBroker(t, func(request database.Request) (any, error) {
			return evaluationResolveStoreResponse{}, nil
		})
		defer closeBroker()
		if _, err := resolveEvaluationBrokerStoreID(
			t.Context(),
			client,
			t.TempDir(),
		); database.CodeOf(
			err,
		) != database.CodeIntegrity {
			t.Fatalf("invalid resolved StoreID = %v", err)
		}
		t.Setenv("HOME", "")
		if _, err := resolveEvaluationWorkspace(t.TempDir(), "~"); err == nil {
			t.Fatal("home-less tilde workspace resolved")
		}
		primaryCfg := &config.Config{Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: "~"},
		}}
		if _, err := configuredEvaluationWorkspaces(t.TempDir(), primaryCfg); err == nil {
			t.Fatal("home-less primary workspace configured")
		}
		agentCfg := &config.Config{Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: t.TempDir()},
			List:     []config.AgentConfig{{ID: "agent", Workspace: "~"}},
		}}
		if _, err := configuredEvaluationWorkspaces(t.TempDir(), agentCfg); err == nil {
			t.Fatal("home-less agent workspace configured")
		}
		originalWorkingDirectory, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		doomed := t.TempDir()
		if err := os.Chdir(doomed); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(doomed); err != nil {
			_ = os.Chdir(originalWorkingDirectory)
			t.Fatal(err)
		}
		_, workspaceErr := resolveEvaluationWorkspace("relative-home", "relative")
		_, selectorErr := evaluationWorkspaceSelector("relative")
		if err := os.Chdir(originalWorkingDirectory); err != nil {
			t.Fatal(err)
		}
		if workspaceErr == nil || selectorErr == nil {
			t.Fatalf("deleted cwd errors = %v, %v", workspaceErr, selectorErr)
		}
	})
}

func startEvaluationScriptedBroker(
	t *testing.T,
	handle func(database.Request) (any, error),
) (*database.Client, func()) {
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
	return client, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Error(err)
		}
	}
}

func evaluationRequest(t *testing.T, operation string, value any) database.Request {
	t.Helper()
	return database.Request{
		Domain: evaluationBrokerDomain, Version: evaluationBrokerVersion,
		Operation: operation, Payload: mustEvaluationBrokerPayload(t, value),
	}
}

func mustEvaluationBrokerPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := database.MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
