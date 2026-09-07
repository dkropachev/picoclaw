package repoaudit

import (
	"context"
	"database/sql"
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

//nolint:govet // Contract scenarios intentionally reuse short result names in assertion scopes.
func TestReviewBrokerCatalogRewriteAndNamedLeaseContracts(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newReviewBrokerHandlerForTest(t, home, workspace)
	server := startReviewBroker(t, home, handler)
	defer closeReviewBroker(t, server)
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setReviewBrokerClientForTest(t, client)
	store := NewSQLiteStore(workspace)

	plan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(), "owner/catalog", "commit", "inventory", "profile", nil,
		false, maxReviewFiles, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeNoopPlan(plan); err != nil {
		t.Fatal(err)
	}
	states, err := store.List()
	if err != nil || len(states) != 1 {
		t.Fatalf("List() = %#v, %v", states, err)
	}
	state, found, err := store.brokerGetByID(states[0].ID)
	if err != nil || !found || state.Repository != "owner/catalog" {
		t.Fatalf("brokerGetByID() = %#v, %v, %v", state, found, err)
	}
	if _, found, err := store.brokerGetByID("missing"); err != nil || found {
		t.Fatalf("missing brokerGetByID() = %v, %v", found, err)
	}
	state.LastExcludedFiles = 3
	state, err = store.brokerRewriteState(t.Context(), state)
	if err != nil || state.LastExcludedFiles != 3 {
		t.Fatalf("brokerRewriteState() = %#v, %v", state, err)
	}

	profile, err := store.CreateProfile(
		t.Context(), validProfileForTest("rrpf_broker_contract", "Broker contract"),
	)
	if err != nil {
		t.Fatal(err)
	}
	loadedProfile, found, err := store.GetProfile(t.Context(), profile.ID)
	if err != nil || !found || loadedProfile.ID != profile.ID {
		t.Fatalf("GetProfile() = %#v, %v, %v", loadedProfile, found, err)
	}
	if assigned, err := store.IsProfileAssigned(t.Context(), profile.ID); err != nil || assigned {
		t.Fatalf("unassigned profile = %v, %v", assigned, err)
	}
	mutationErr := errors.New("profile mutation stopped")
	if _, err := store.UpdateProfile(
		t.Context(),
		profile.ID,
		profile.Version,
		nil,
	); !errors.Is(
		err,
		ErrInvalidProfile,
	) {
		t.Fatalf("nil profile mutation = %v", err)
	}
	if _, err := store.UpdateProfile(
		t.Context(),
		"rrpf_missing_broker",
		1,
		func(*RepositoryReviewProfile) error { return nil },
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing profile mutation = %v", err)
	}
	if _, err := store.UpdateProfile(
		t.Context(),
		profile.ID,
		profile.Version,
		func(*RepositoryReviewProfile) error { return mutationErr },
	); !errors.Is(
		err,
		mutationErr,
	) {
		t.Fatalf("profile callback error = %v", err)
	}
	if _, err := store.UpdateProfile(
		t.Context(),
		profile.ID,
		profile.Version+1,
		func(value *RepositoryReviewProfile) error { value.Name = "stale"; return nil },
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale profile mutation = %v", err)
	}
	profile, err = store.UpdateProfile(
		t.Context(), profile.ID, profile.Version,
		func(value *RepositoryReviewProfile) error { value.Name = "Updated broker contract"; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	profile.Name = "Rewritten broker contract"
	profile, err = store.brokerRewriteProfile(t.Context(), profile)
	if err != nil || profile.Name != "Rewritten broker contract" {
		t.Fatalf("brokerRewriteProfile() = %#v, %v", profile, err)
	}

	automationInput := validAutomationForTest("rra_broker_contract", "Broker contract")
	automationInput, err = MaterializeRepositoryReviewAutomation(profile, automationInput)
	if err != nil {
		t.Fatal(err)
	}
	automation, err := store.CreateAutomation(t.Context(), automationInput)
	if err != nil {
		t.Fatal(err)
	}
	loadedAutomation, found, err := store.GetAutomation(t.Context(), automation.ID)
	if err != nil || !found || loadedAutomation.ID != automation.ID {
		t.Fatalf("GetAutomation() = %#v, %v, %v", loadedAutomation, found, err)
	}
	if assigned, err := store.IsProfileAssigned(t.Context(), profile.ID); err != nil || !assigned {
		t.Fatalf("assigned profile = %v, %v", assigned, err)
	}
	if err := store.DeleteProfile(t.Context(), profile.ID, profile.Version); !errors.Is(err, ErrProfileAssigned) {
		t.Fatalf("assigned profile deletion = %v", err)
	}
	if _, err := store.UpdateAutomation(
		t.Context(),
		automation.ID,
		automation.Version,
		nil,
	); !errors.Is(
		err,
		ErrInvalidAutomation,
	) {
		t.Fatalf("nil automation mutation = %v", err)
	}
	if _, err := store.UpdateAutomation(
		t.Context(),
		"rra_missing_broker",
		1,
		func(*RepositoryReviewAutomation) error { return nil },
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing automation mutation = %v", err)
	}
	mutationErr = errors.New("automation mutation stopped")
	if _, err := store.UpdateAutomation(
		t.Context(),
		automation.ID,
		automation.Version,
		func(*RepositoryReviewAutomation) error { return mutationErr },
	); !errors.Is(
		err,
		mutationErr,
	) {
		t.Fatalf("automation callback error = %v", err)
	}
	if _, err := store.UpdateAutomation(
		t.Context(),
		automation.ID,
		automation.Version+1,
		func(value *RepositoryReviewAutomation) error { value.Name = "stale"; return nil },
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale automation mutation = %v", err)
	}
	automation, err = store.UpdateAutomation(
		t.Context(), automation.ID, automation.Version,
		func(value *RepositoryReviewAutomation) error { value.Name = "Updated broker automation"; return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	automation.Name = "Rewritten broker automation"
	automation, err = store.brokerRewriteAutomation(t.Context(), automation)
	if err != nil || automation.Name != "Rewritten broker automation" {
		t.Fatalf("brokerRewriteAutomation() = %#v, %v", automation, err)
	}
	if err := store.DeleteAutomation(t.Context(), automation.ID, automation.Version+1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale automation deletion = %v", err)
	}
	if err := store.DeleteAutomation(t.Context(), automation.ID, automation.Version); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetAutomation(t.Context(), automation.ID); err != nil || found {
		t.Fatalf("deleted automation = %v, %v", found, err)
	}
	if err := store.DeleteProfile(t.Context(), profile.ID, profile.Version); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetProfile(t.Context(), profile.ID); err != nil || found {
		t.Fatalf("deleted profile = %v, %v", found, err)
	}

	releaseController, err := store.LockAutomationController()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.LockAutomationController(); !errors.Is(err, ErrAutomationControllerLocked) {
		t.Fatalf("duplicate controller lease = %v", err)
	}
	releaseController()
	releaseAttempt, acquired, err := store.TryLockIssueGenerationAttempt(
		"owner/catalog", "draft", "generation",
	)
	if err != nil || !acquired {
		t.Fatalf("issue attempt lease = %v, %v", acquired, err)
	}
	if duplicateRelease, duplicate, err := store.TryLockIssueGenerationAttempt(
		"owner/catalog", "draft", "generation",
	); err != nil || duplicate || duplicateRelease != nil {
		t.Fatalf("duplicate issue attempt = %v, %v", duplicate, err)
	}
	releaseAttempt()
	for name, acquire := range map[string]func() (func(), error){
		"issue":         func() (func(), error) { return store.AcquireIssueGenerationSlot(t.Context(), 1) },
		"deduplication": func() (func(), error) { return store.AcquireDeduplicationSlot(t.Context()) },
		"validation":    func() (func(), error) { return store.AcquireValidationSlot(t.Context()) },
	} {
		t.Run(name, func(t *testing.T) {
			release, err := acquire()
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}

func TestReviewBrokerHandlerRejectsInvalidCatalogAndLeaseRequests(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newReviewBrokerHandlerForTest(t, home, workspace)
	child := handler.workspaces[ReviewStoreID]
	t.Cleanup(func() { _ = handler.Close() })

	tests := []database.Request{
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationGetByID,
			Payload:   json.RawMessage(`{"store_id":"wrong","id":"x"}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationListStates,
			Payload:   mustReviewBrokerPayload(t, reviewPageRequest{StoreID: ReviewStoreID, Offset: -1, Limit: 1}),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationListStates,
			Payload: mustReviewBrokerPayload(
				t,
				reviewPageRequest{StoreID: ReviewStoreID, Limit: reviewStatePageSize + 1},
			),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationListSummaries,
			Payload: mustReviewBrokerPayload(
				t,
				reviewPageRequest{StoreID: ReviewStoreID, Limit: reviewSummaryPageSize + 1},
			),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationListProfiles,
			Payload: mustReviewBrokerPayload(
				t,
				reviewPageRequest{StoreID: ReviewStoreID, Limit: reviewProfilePageSize + 1},
			),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationListAutomations,
			Payload: mustReviewBrokerPayload(
				t,
				reviewPageRequest{StoreID: ReviewStoreID, Limit: reviewAutomationPageSize + 1},
			),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationGetProfile,
			Payload:   json.RawMessage(`{"store_id":"wrong","id":"x"}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationCreateProfile,
			Payload:   json.RawMessage(`{"store_id":"wrong","profile":{}}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationUpdateProfile,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationDeleteProfile,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationCreateAutomation,
			Payload:   json.RawMessage(`{"store_id":"wrong","automation":{}}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationUpdateAutomation,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationDeleteAutomation,
			Payload:   json.RawMessage(`{"store_id":"wrong"}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationRewriteState,
			Payload:   json.RawMessage(`{"store_id":"wrong","state":{}}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationRewriteProfile,
			Payload:   json.RawMessage(`{"store_id":"wrong","profile":{}}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationRewriteAutomation,
			Payload:   json.RawMessage(`{"store_id":"wrong","automation":{}}`),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: reviewOperationAcquireNamedLease,
			Payload:   mustReviewBrokerPayload(t, reviewNamedLeaseRequest{StoreID: ReviewStoreID, Kind: "unknown"}),
		},
		{
			Domain:    reviewBrokerDomain,
			Version:   reviewBrokerVersion,
			Operation: "unknown",
			Payload:   json.RawMessage(`{}`),
		},
	}
	for _, request := range tests {
		if _, err := child.Handle(t.Context(), request); err == nil {
			t.Errorf("operation %q accepted invalid request", request.Operation)
		}
	}

	empty, err := child.Handle(t.Context(), database.Request{
		Domain: reviewBrokerDomain, Version: reviewBrokerVersion, Operation: reviewOperationListProfiles,
		Payload: mustReviewBrokerPayload(t, reviewPageRequest{StoreID: ReviewStoreID, Offset: 50, Limit: 1}),
	})
	if err != nil || len(empty.(reviewProfilesResponse).Items) != 0 || !empty.(reviewProfilesResponse).Done {
		t.Fatalf("empty page = %#v, %v", empty, err)
	}

	for _, item := range []struct {
		err  error
		code database.ErrorCode
	}{
		{context.Canceled, database.CodeDeadline},
		{ErrProfileAssigned, database.CodeAlreadyExists},
		{ErrProfileActive, database.CodeUnsupported},
		{ErrConflict, database.CodeConflict},
		{os.ErrNotExist, database.CodeNotFound},
		{ErrInvalidProfile, database.CodeInvalid},
		{errors.New("opaque"), database.CodeInternal},
	} {
		if got := database.CodeOf(mapReviewBrokerError(item.err)); got != item.code {
			t.Errorf("mapReviewBrokerError(%v) = %s, want %s", item.err, got, item.code)
		}
	}
	for _, item := range []struct {
		err  error
		want error
	}{
		{database.NewError(database.CodeConflict, "conflict"), ErrConflict},
		{database.NewError(database.CodeAlreadyExists, "assigned"), ErrProfileAssigned},
		{database.NewError(database.CodeUnsupported, "active"), ErrProfileActive},
		{database.NewError(database.CodeNotFound, "missing"), os.ErrNotExist},
		{database.NewError(database.CodeInvalid, "invalid"), ErrInvalidProfile},
	} {
		if got := mapReviewProfileClientError(item.err); !errors.Is(got, item.want) {
			t.Errorf("profile client map = %v, want %v", got, item.want)
		}
	}
	for _, item := range []struct {
		err  error
		want error
	}{
		{database.NewError(database.CodeConflict, "conflict"), ErrConflict},
		{database.NewError(database.CodeNotFound, "missing"), os.ErrNotExist},
		{database.NewError(database.CodeUnsupported, "active"), ErrAutomationActive},
		{database.NewError(database.CodeInvalid, "invalid"), ErrInvalidAutomation},
	} {
		if got := mapReviewAutomationClientError(item.err); !errors.Is(got, item.want) {
			t.Errorf("automation client map = %v, want %v", got, item.want)
		}
	}

	if child.effectiveLeaseTTL() <= 0 || (*reviewStoreHandler)(nil).effectiveLeaseTTL() <= 0 {
		t.Fatal("lease TTL fallback is not positive")
	}
	released := 0
	lease := &reviewBrokerLease{release: func() { released++ }}
	if err := child.registerLease("collision", lease); err != nil {
		t.Fatal(err)
	}
	if err := child.registerLease("collision", &reviewBrokerLease{}); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("duplicate lease registration = %v", err)
	}
	child.mu.Lock()
	generation := lease.generation
	child.mu.Unlock()
	child.expireLease("collision", lease, generation+1)
	if released != 0 {
		t.Fatal("stale lease generation released the active lease")
	}
	child.expireLease("collision", lease, generation)
	if released != 1 {
		t.Fatalf("lease release count = %d", released)
	}
	lease.releaseNow()
	if released != 1 {
		t.Fatal("lease release was not idempotent")
	}

	closed := newReviewStoreHandler(workspace, ReviewStoreID)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closed.registerLease("closed", &reviewBrokerLease{}); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed lease registration = %v", err)
	}
	deadline, cancel := context.WithCancel(t.Context())
	cancel()
	<-child.requestGate
	if _, err := child.Handle(
		deadline,
		database.Request{Domain: reviewBrokerDomain, Version: reviewBrokerVersion},
	); database.CodeOf(
		err,
	) != database.CodeDeadline {
		t.Fatalf("canceled handler request = %v", err)
	}
	child.requestGate <- struct{}{}

	child.mu.Lock()
	child.leaseTTL = time.Millisecond
	child.mu.Unlock()
}

func TestReviewBrokerClientFailClosedAndMalformedResponseContracts(t *testing.T) {
	sentinel := database.NewError(database.CodeUnavailable, "broker unavailable")
	failed := Store{brokerErr: sentinel, brokerState: &auditBrokerClientState{}}
	if _, err := failed.brokerLock("key"); !errors.Is(err, sentinel) {
		t.Errorf("brokerLock() = %v", err)
	}
	if _, err := failed.currentBrokerLease(); !errors.Is(err, sentinel) {
		t.Errorf("currentBrokerLease() = %v", err)
	}
	if !failed.brokerClock().IsZero() {
		t.Fatal("failed broker clock was nonzero")
	}
	if err := failed.Preflight(t.Context()); !errors.Is(err, sentinel) {
		t.Errorf("Preflight() = %v", err)
	}
	failed.broker = &database.Client{}
	if _, err := failed.brokerLock("key"); !errors.Is(err, sentinel) {
		t.Errorf("broker-backed failed lock = %v", err)
	}
	if _, err := failed.brokerAcquireNamedLease(
		t.Context(),
		reviewLeaseValidationSlot,
		reviewNamedLeaseRequest{},
	); !errors.Is(
		err,
		sentinel,
	) {
		t.Errorf("failed named lease = %v", err)
	}
	if _, _, err := failed.brokerTryIssueAttempt("owner/repo", "draft", "generation"); !errors.Is(err, sentinel) {
		t.Errorf("failed issue attempt = %v", err)
	}
	if _, err := (Store{}).brokerLock("key"); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("missing broker lock = %v", err)
	}
	if _, err := (Store{broker: &database.Client{}, brokerState: &auditBrokerClientState{}}).brokerLock(
		" ",
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("empty broker lock = %v", err)
	}
	withoutLease := Store{brokerState: &auditBrokerClientState{}}
	if _, err := withoutLease.currentBrokerLease(); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("missing current lease = %v", err)
	}
	if _, err := withoutLease.brokerLoadState("owner/repo"); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unleased load = %v", err)
	}
	if err := withoutLease.brokerSaveState(nil); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("nil state save = %v", err)
	}
	if err := withoutLease.brokerSaveState(
		&RepositoryState{Repository: "owner/repo"},
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("unleased state save = %v", err)
	}
	withoutLease.recordBrokerLeaseError(nil)
	withoutLease.recordBrokerLeaseError(sentinel)
	withoutLease.recordBrokerLeaseError(database.NewError(database.CodeInternal, "later"))
	if err := withoutLease.consumeBrokerLeaseError(); !errors.Is(err, sentinel) {
		t.Fatalf("recorded lease error = %v", err)
	}
	if err := withoutLease.consumeBrokerLeaseError(); err != nil {
		t.Fatalf("consumed lease error persisted = %v", err)
	}
	var nilState Store
	nilState.recordBrokerLeaseError(sentinel)
	if err := nilState.consumeBrokerLeaseError(); err != nil {
		t.Fatalf("nil lease state = %v", err)
	}

	client, closeBroker := startReviewScriptedBroker(t, func(request database.Request) (any, error) {
		switch request.Operation {
		case reviewOperationLock:
			return reviewLeaseResponse{}, nil
		case reviewOperationSaveState:
			return reviewMutationResponse{Updated: false}, nil
		case reviewOperationClock:
			return reviewClockResponse{}, nil
		case reviewOperationPreflight:
			return reviewReadyResponse{Ready: false}, nil
		case reviewOperationUnlock:
			return reviewMutationResponse{Updated: false}, nil
		case reviewOperationRenewLease:
			return reviewLeaseResponse{LeaseID: "wrong", TTLNanoSeconds: 1}, nil
		case reviewOperationAcquireNamedLease:
			return reviewNamedLeaseResponse{Acquired: true}, nil
		case reviewOperationListStates:
			return reviewStatesResponse{Items: []RepositoryState{}, Done: false}, nil
		default:
			return nil, database.NewError(database.CodeUnauthorized, "rejected")
		}
	})
	defer closeBroker()
	store := Store{
		broker: client, storeID: ReviewStoreID,
		brokerState: &auditBrokerClientState{leaseID: "lease", lockKey: "owner/repo"},
	}
	store.brokerState.releaseErr = sentinel
	if _, err := store.brokerLock("key"); !errors.Is(err, sentinel) {
		t.Fatalf("remembered lease lock = %v", err)
	}
	store.brokerState.releaseErr = sentinel
	if _, err := store.brokerAcquireNamedLease(
		t.Context(),
		reviewLeaseValidationSlot,
		reviewNamedLeaseRequest{},
	); !errors.Is(
		err,
		sentinel,
	) {
		t.Fatalf("remembered named lease = %v", err)
	}
	store.brokerState.releaseErr = sentinel
	if _, _, err := store.brokerTryIssueAttempt("owner/repo", "draft", "generation"); !errors.Is(err, sentinel) {
		t.Fatalf("remembered issue lease = %v", err)
	}
	if _, err := store.brokerLock("key"); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid lock response = %v", err)
	}
	if err := store.brokerSaveState(
		&RepositoryState{Repository: "owner/repo"},
	); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("false save response = %v", err)
	}
	if !store.brokerClock().IsZero() {
		t.Fatal("invalid broker clock was accepted")
	}
	if err := store.Preflight(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("false readiness response = %v", err)
	}
	if _, err := store.brokerListStates(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("short unfinished state page = %v", err)
	}
	if _, err := store.brokerListSummaries(); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("failed summary list = %v", err)
	}
	if _, err := store.brokerListProfiles(t.Context()); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("failed profile list = %v", err)
	}
	if _, err := store.brokerListAutomations(t.Context()); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("failed automation list = %v", err)
	}
	if _, err := store.brokerUpdateProfile(
		t.Context(),
		"id",
		1,
		func(*RepositoryReviewProfile) error { return nil },
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("failed profile lookup = %v", err)
	}
	if _, err := store.brokerUpdateAutomation(
		t.Context(),
		"id",
		1,
		func(*RepositoryReviewAutomation) error { return nil },
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("failed automation lookup = %v", err)
	}
	if _, err := store.brokerAcquireNamedLease(
		t.Context(),
		reviewLeaseValidationSlot,
		reviewNamedLeaseRequest{},
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("invalid named lease response = %v", err)
	}
	if _, acquired, err := store.brokerTryIssueAttempt(
		"owner/repo",
		"draft",
		"generation",
	); database.CodeOf(err) != database.CodeIntegrity ||
		acquired {
		t.Fatalf("invalid issue attempt response = %v, %v", acquired, err)
	}
	release := store.newBrokerLeaseRelease("lease", 0, nil)
	release()
	release()
	if err := store.BrokerLeaseError(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid unlock response = %v", err)
	}

	errorClient, closeErrorBroker := startReviewScriptedBroker(t, func(database.Request) (any, error) {
		return nil, database.NewError(database.CodeUnauthorized, "rejected")
	})
	defer closeErrorBroker()
	errorStore := Store{
		broker: errorClient, storeID: ReviewStoreID,
		brokerState: &auditBrokerClientState{},
	}
	if _, err := errorStore.brokerListStates(); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("failed state list = %v", err)
	}
	if _, err := errorStore.brokerAcquireNamedLease(
		t.Context(),
		reviewLeaseValidationSlot,
		reviewNamedLeaseRequest{},
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("failed named lease call = %v", err)
	}
	if _, _, err := errorStore.brokerTryIssueAttempt(
		"owner/repo",
		"draft",
		"generation",
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("failed issue attempt call = %v", err)
	}
	if _, _, err := errorStore.loadProfile("rrpf_error"); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("broker profile load = %v", err)
	}
	if err := errorStore.saveProfile(RepositoryReviewProfile{}); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("broker profile save = %v", err)
	}
	if _, err := errorStore.profileAssignedUnlocked("rrpf_error"); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("broker profile assignment = %v", err)
	}
	if _, err := errorStore.profileActiveUnlocked("rrpf_error"); database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("broker profile activity = %v", err)
	}
	if _, _, err := errorStore.loadAutomation("rra_error"); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("broker automation load = %v", err)
	}
	if err := errorStore.saveAutomation(
		RepositoryReviewAutomation{},
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("broker automation save = %v", err)
	}
	if _, _, err := errorStore.GetByID(
		"rrp_" + strings.Repeat("a", 64),
	); database.CodeOf(
		err,
	) != database.CodeUnauthorized {
		t.Fatalf("broker GetByID = %v", err)
	}
	done := make(chan struct{})
	store.renewBrokerLease(make(chan struct{}), done, "lease", time.Millisecond)
	<-done
	if err := store.BrokerLeaseError(); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid renewal response = %v", err)
	}

	oversizedClient, closeOversized := startReviewScriptedBroker(t, func(database.Request) (any, error) {
		return reviewStatesResponse{
			Items: make([]RepositoryState, reviewStatePageSize+1), Done: true,
		}, nil
	})
	defer closeOversized()
	if _, err := (Store{broker: oversizedClient, storeID: ReviewStoreID}).brokerListStates(); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("oversized state page = %v", err)
	}

	if mapReviewBrokerError(nil) != nil || mapReviewClientError(nil) != nil {
		t.Fatal("nil review error mapping changed nil")
	}
	for _, item := range []struct {
		err  error
		code database.ErrorCode
	}{
		{sqlitestore.ErrTooNew, database.CodeUnsupported},
		{sqlitestore.ErrInvalidSchema, database.CodeIntegrity},
		{sqlitestore.ErrIntegrity, database.CodeIntegrity},
	} {
		if got := database.CodeOf(mapReviewBrokerError(item.err)); got != item.code {
			t.Errorf("mapReviewBrokerError(%v) = %s", item.err, got)
		}
	}
	for _, item := range []struct {
		code database.ErrorCode
		want error
	}{
		{database.CodeConflict, ErrConflict},
		{database.CodeNotFound, os.ErrNotExist},
		{database.CodeInvalid, ErrInvalidPlan},
	} {
		if got := mapReviewClientError(database.NewError(item.code, "mapped")); !errors.Is(got, item.want) {
			t.Errorf("mapReviewClientError(%s) = %v", item.code, got)
		}
	}
}

//nolint:govet // Independent failure assertions intentionally use local error bindings.
func TestReviewBrokerProviderFailuresAndCoreHandlerErrors(t *testing.T) {
	workspace := t.TempDir()
	poisoned := newReviewStoreHandler(workspace, ReviewStoreID)
	poisoned.once.Do(func() { poisoned.err = errors.New("provider failed") })
	poisoned.leases["lease"] = &reviewBrokerLease{key: "owner/repo"}
	t.Cleanup(func() { _ = poisoned.Close() })
	requests := []database.Request{
		reviewRequest(t, reviewOperationPreflight, reviewTarget{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationLock, reviewLockRequest{StoreID: ReviewStoreID, Key: "key"}),
		reviewRequest(
			t,
			reviewOperationLoadState,
			reviewLoadRequest{StoreID: ReviewStoreID, LeaseID: "lease", Repository: "owner/repo"},
		),
		reviewRequest(
			t,
			reviewOperationSaveState,
			reviewSaveRequest{
				StoreID: ReviewStoreID,
				LeaseID: "lease",
				State:   RepositoryState{Repository: "owner/repo"},
			},
		),
		reviewRequest(t, reviewOperationClock, reviewTarget{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationGetByID, reviewIDRequest{StoreID: ReviewStoreID, ID: "id"}),
		reviewRequest(t, reviewOperationListStates, reviewPageRequest{StoreID: ReviewStoreID, Limit: 1}),
		reviewRequest(t, reviewOperationGetProfile, reviewIDRequest{StoreID: ReviewStoreID, ID: "id"}),
		reviewRequest(t, reviewOperationCreateProfile, reviewProfileRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationCreateAutomation, reviewAutomationRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationRewriteState, reviewRewriteStateRequest{StoreID: ReviewStoreID}),
		reviewRequest(
			t,
			reviewOperationAcquireNamedLease,
			reviewNamedLeaseRequest{StoreID: ReviewStoreID, Kind: reviewLeaseValidationSlot},
		),
	}
	for _, request := range requests {
		if _, err := poisoned.Handle(t.Context(), request); database.CodeOf(err) != database.CodeInternal {
			t.Errorf("%s provider failure = %v", request.Operation, err)
		}
	}

	child := newReviewStoreHandler(t.TempDir(), ReviewStoreID)
	t.Cleanup(func() { _ = child.Close() })
	invalid := []database.Request{
		reviewRequest(t, reviewOperationPreflight, reviewTarget{StoreID: "wrong"}),
		reviewRequest(t, reviewOperationLoadState, reviewLoadRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationSaveState, reviewSaveRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationClock, reviewTarget{StoreID: "wrong"}),
		reviewRequest(t, reviewOperationLock, reviewLockRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationUnlock, reviewLeaseRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationRenewLease, reviewLeaseRequest{StoreID: ReviewStoreID}),
	}
	for _, request := range invalid {
		if _, err := child.Handle(t.Context(), request); err == nil {
			t.Errorf("%s accepted invalid request", request.Operation)
		}
	}
	if _, err := child.authorizeLease("missing", "key"); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("missing lease authorization = %v", err)
	}
	if _, err := child.Handle(
		t.Context(),
		database.Request{Domain: "wrong"},
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("unsupported inner request = %v", err)
	}
	closedChild := newReviewStoreHandler(t.TempDir(), ReviewStoreID)
	if err := closedChild.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := closedChild.Handle(t.Context(), reviewRequest(
		t, reviewOperationPreflight, reviewTarget{StoreID: ReviewStoreID},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed inner request = %v", err)
	}
	if _, err := child.Handle(t.Context(), reviewRequest(
		t, reviewOperationLoadState,
		reviewLoadRequest{StoreID: ReviewStoreID, LeaseID: "missing", Repository: "owner/repo"},
	)); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unauthorized state load = %v", err)
	}
	if _, err := child.Handle(t.Context(), reviewRequest(
		t, reviewOperationSaveState,
		reviewSaveRequest{StoreID: ReviewStoreID, LeaseID: "missing", State: RepositoryState{Repository: "owner/repo"}},
	)); database.CodeOf(err) != database.CodeConflict {
		t.Fatalf("unauthorized state save = %v", err)
	}
	if _, err := child.Handle(t.Context(), reviewRequest(
		t, reviewOperationAcquireNamedLease,
		reviewNamedLeaseRequest{StoreID: "wrong", Kind: reviewLeaseValidationSlot},
	)); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid named lease target = %v", err)
	}
	if _, err := child.releaseLease(
		reviewRequest(t, reviewOperationUnlock, reviewLeaseRequest{StoreID: ReviewStoreID, LeaseID: "missing"}),
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("missing lease release = %v", err)
	}
	if _, err := child.renewLease(
		reviewRequest(t, reviewOperationRenewLease, reviewLeaseRequest{StoreID: ReviewStoreID, LeaseID: "missing"}),
	); database.CodeOf(
		err,
	) != database.CodeConflict {
		t.Fatalf("missing lease renewal = %v", err)
	}

	firstAny, err := child.acquireLease(t.Context(), reviewRequest(
		t, reviewOperationLock, reviewLockRequest{StoreID: ReviewStoreID, Key: "contended"},
	))
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(reviewLeaseResponse)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := child.acquireLease(canceled, reviewRequest(
		t, reviewOperationLock, reviewLockRequest{StoreID: ReviewStoreID, Key: "contended"},
	)); database.CodeOf(err) != database.CodeDeadline {
		t.Fatalf("contended canceled lease = %v", err)
	}
	if _, err := child.releaseLease(reviewRequest(
		t, reviewOperationUnlock, reviewLeaseRequest{StoreID: ReviewStoreID, LeaseID: first.LeaseID},
	)); err != nil {
		t.Fatal(err)
	}

	released := 0
	closing := newReviewStoreHandler(t.TempDir(), ReviewStoreID)
	for _, id := range []string{"first", "second"} {
		closing.leases[id] = &reviewBrokerLease{
			release: func() { released++ }, timer: time.NewTimer(time.Hour),
		}
	}
	if err := closing.Close(); err != nil {
		t.Fatal(err)
	}
	if released != 2 {
		t.Fatalf("closing released %d leases", released)
	}
	if err := (*reviewStoreHandler)(nil).Close(); err != nil {
		t.Fatalf("nil review handler Close() = %v", err)
	}

	domain := newReviewStoreHandler(t.TempDir(), ReviewStoreID)
	t.Cleanup(func() { _ = domain.Close() })
	if _, err := domain.Handle(t.Context(), reviewRequest(
		t, reviewOperationPreflight, reviewTarget{StoreID: ReviewStoreID},
	)); err != nil {
		t.Fatal(err)
	}
	for _, request := range []database.Request{
		reviewRequest(t, reviewOperationCreateProfile, reviewProfileRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationCreateAutomation, reviewAutomationRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationRewriteState, reviewRewriteStateRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationRewriteProfile, reviewProfileRequest{StoreID: ReviewStoreID}),
		reviewRequest(t, reviewOperationRewriteAutomation, reviewAutomationRequest{StoreID: ReviewStoreID}),
	} {
		if _, err := domain.Handle(t.Context(), request); err == nil {
			t.Errorf("%s accepted invalid domain value", request.Operation)
		}
	}
	plan, err := domain.store.PlanWithProfileLimitAuthoritative(
		t.Context(), "owner/closed", "commit", "inventory", "profile", nil,
		false, maxReviewFiles, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.store.FinalizeNoopPlan(plan); err != nil {
		t.Fatal(err)
	}
	states, err := domain.store.List()
	if err != nil || len(states) != 1 {
		t.Fatalf("closed-pool seed states = %#v, %v", states, err)
	}
	closedStateID := states[0].ID
	domain.store.retained.mu.Lock()
	if err := domain.store.retained.db.Close(); err != nil {
		domain.store.retained.mu.Unlock()
		t.Fatal(err)
	}
	domain.store.retained.mu.Unlock()
	for _, request := range []database.Request{
		reviewRequest(t, reviewOperationLoadState, reviewLoadRequest{StoreID: ReviewStoreID, LeaseID: "lease", Repository: "owner/repo"}),
		reviewRequest(t, reviewOperationSaveState, reviewSaveRequest{StoreID: ReviewStoreID, LeaseID: "lease", State: states[0]}),
		reviewRequest(t, reviewOperationGetByID, reviewIDRequest{StoreID: ReviewStoreID, ID: closedStateID}),
		reviewRequest(t, reviewOperationListStates, reviewPageRequest{StoreID: ReviewStoreID, Limit: 1}),
		reviewRequest(t, reviewOperationListSummaries, reviewPageRequest{StoreID: ReviewStoreID, Limit: 1}),
		reviewRequest(t, reviewOperationListProfiles, reviewPageRequest{StoreID: ReviewStoreID, Limit: 1}),
		reviewRequest(t, reviewOperationListAutomations, reviewPageRequest{StoreID: ReviewStoreID, Limit: 1}),
		reviewRequest(t, reviewOperationGetProfile, reviewIDRequest{StoreID: ReviewStoreID, ID: "rrpf_closed"}),
		reviewRequest(t, reviewOperationProfileAssigned, reviewIDRequest{StoreID: ReviewStoreID, ID: "rrpf_closed"}),
		reviewRequest(t, reviewOperationGetAutomation, reviewIDRequest{StoreID: ReviewStoreID, ID: "rra_closed"}),
	} {
		if request.Operation == reviewOperationLoadState {
			domain.leases["lease"] = &reviewBrokerLease{key: "owner/repo"}
		}
		if request.Operation == reviewOperationSaveState {
			domain.leases["lease"] = &reviewBrokerLease{key: states[0].Repository}
		}
		if _, err := domain.Handle(t.Context(), request); err == nil {
			t.Errorf("%s accepted a closed provider pool", request.Operation)
		}
	}
	closedPreflight := Store{retained: &retainedReviewDatabase{closed: true}}
	if err := closedPreflight.Preflight(t.Context()); err == nil {
		t.Fatal("local preflight accepted a closed pool")
	}
}

//nolint:govet // Boundary assertions intentionally keep errors local to each operation.
func TestReviewBrokerWorkspaceAndLocalProviderBoundaries(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	handler := newReviewBrokerHandlerForTest(t, home, workspace)
	selector := ""
	for value := range handler.selectors {
		selector = value
	}
	resolved, err := handler.Handle(nil, database.Request{
		Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
		Operation: reviewOperationResolveStore,
		Payload:   mustReviewBrokerPayload(t, reviewResolveStoreRequest{WorkspaceSelector: selector}),
	})
	if err != nil || !resolved.(reviewResolveStoreResponse).StoreID.Valid() {
		t.Fatalf("nil-context resolve = %#v, %v", resolved, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := handler.Handle(
		canceled,
		database.Request{Domain: reviewBrokerDomain, Version: reviewBrokerVersion},
	); database.CodeOf(
		err,
	) != database.CodeDeadline {
		t.Fatalf("outer canceled request = %v", err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
		Operation: reviewOperationResolveStore, Payload: json.RawMessage(`{}`),
	}); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid selector request = %v", err)
	}
	if _, err := handler.Handle(
		t.Context(),
		database.Request{Domain: "wrong"},
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("outer unsupported request = %v", err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
		Operation: reviewOperationResolveStore,
		Payload:   mustReviewBrokerPayload(t, reviewResolveStoreRequest{WorkspaceSelector: "missing"}),
	}); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown selector = %v", err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
		Operation: reviewOperationGetByID, Payload: json.RawMessage(`{}`),
	}); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid outer store request = %v", err)
	}
	if _, err := handler.Handle(t.Context(), reviewRequest(
		t, reviewOperationGetByID, reviewIDRequest{StoreID: "workspace/unknown", ID: "id"},
	)); database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unknown store = %v", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.Handle(t.Context(), database.Request{
		Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
		Operation: reviewOperationResolveStore,
		Payload:   mustReviewBrokerPayload(t, reviewResolveStoreRequest{WorkspaceSelector: selector}),
	}); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed resolver = %v", err)
	}
	if _, err := handler.Handle(t.Context(), reviewRequest(
		t, reviewOperationGetByID, reviewIDRequest{StoreID: ReviewStoreID, ID: "id"},
	)); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("closed outer store = %v", err)
	}
	if err := (*BrokerHandler)(nil).Close(); err != nil {
		t.Fatalf("nil broker handler Close() = %v", err)
	}

	if _, err := resolveReviewBrokerStoreID(
		t.Context(),
		nil,
		workspace,
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("nil broker resolution = %v", err)
	}
	if _, err := resolveReviewBrokerStoreID(
		t.Context(),
		&database.Client{},
		"",
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("invalid workspace resolution = %v", err)
	}
	if _, err := reviewWorkspaceSelector(""); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("empty selector = %v", err)
	}
	if _, err := reviewWorkspaceSelector("bad\x00workspace"); database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("NUL selector = %v", err)
	}
	if _, err := reviewRequestStoreID(
		database.Request{Payload: json.RawMessage(`{}`)},
	); database.CodeOf(
		err,
	) != database.CodeInvalid {
		t.Fatalf("invalid request StoreID = %v", err)
	}
	for _, configured := range []string{"", "relative", home, "~", "~/child"} {
		if resolved, err := resolveReviewWorkspace(home, configured); err != nil || !filepath.IsAbs(resolved) {
			t.Errorf("resolveReviewWorkspace(%q) = %q, %v", configured, resolved, err)
		}
	}
	cfg := &config.Config{Agents: config.AgentsConfig{
		Defaults: config.AgentDefaults{Workspace: workspace},
		List: []config.AgentConfig{
			{ID: "blank", Workspace: " "},
			{ID: "duplicate", Workspace: workspace},
			{ID: "second", Workspace: filepath.Join(home, "second")},
		},
	}}
	if workspaces, err := configuredReviewWorkspaces(home, cfg); err != nil || len(workspaces) != 2 {
		t.Fatalf("configured workspace filtering = %#v, %v", workspaces, err)
	}
	if _, err := NewBrokerHandler("bad\x00home", nil); err == nil {
		t.Fatal("broker accepted invalid home")
	}
	invalidClient, closeInvalid := startReviewScriptedBroker(t, func(database.Request) (any, error) {
		return reviewResolveStoreResponse{}, nil
	})
	if _, err := resolveReviewBrokerStoreID(
		t.Context(),
		invalidClient,
		workspace,
	); database.CodeOf(
		err,
	) != database.CodeIntegrity {
		t.Fatalf("invalid resolved StoreID = %v", err)
	}
	brokerStore := Store{broker: invalidClient, storeID: ReviewStoreID, brokerState: &auditBrokerClientState{}}
	if _, err := brokerStore.RewriteStateForMigration(t.Context(), RepositoryState{}); err == nil {
		t.Fatal("broker state rewrite accepted invalid broker response")
	}
	if _, err := brokerStore.RewriteProfileForMigration(t.Context(), RepositoryReviewProfile{}); err == nil {
		t.Fatal("broker profile rewrite accepted invalid broker response")
	}
	if _, err := brokerStore.RewriteAutomationForMigration(t.Context(), RepositoryReviewAutomation{}); err == nil {
		t.Fatal("broker automation rewrite accepted invalid broker response")
	}
	closeInvalid()

	t.Run("workspace errors", func(t *testing.T) {
		t.Setenv("HOME", "")
		if _, err := resolveReviewWorkspace(home, "~"); err == nil {
			t.Fatal("home-less tilde workspace resolved")
		}
		primaryCfg := &config.Config{Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: "~"},
		}}
		if _, err := configuredReviewWorkspaces(home, primaryCfg); err == nil {
			t.Fatal("home-less primary workspace configured")
		}
		agentCfg := &config.Config{Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{Workspace: workspace},
			List:     []config.AgentConfig{{ID: "agent", Workspace: "~"}},
		}}
		if _, err := configuredReviewWorkspaces(home, agentCfg); err == nil {
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
		_, workspaceErr := resolveReviewWorkspace("relative-home", "relative")
		_, selectorErr := reviewWorkspaceSelector("relative")
		if err := os.Chdir(originalWorkingDirectory); err != nil {
			t.Fatal(err)
		}
		if workspaceErr == nil || selectorErr == nil {
			t.Fatalf("deleted cwd errors = %v, %v", workspaceErr, selectorErr)
		}
	})

	local := newSQLiteStoreLocal(t.TempDir())
	if err := local.Preflight(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{broker: &database.Client{}}).openDatabase(
		t.Context(),
	); database.CodeOf(
		err,
	) != database.CodeUnsupported {
		t.Fatalf("broker-routed local open = %v", err)
	}
	closedRetained := Store{retained: &retainedReviewDatabase{closed: true}}
	if _, _, err := closedRetained.acquireDatabase(t.Context()); err == nil {
		t.Fatal("closed retained store was acquired")
	}
	nilPool := Store{retained: &retainedReviewDatabase{}}
	if err := nilPool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := nilPool.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (Store{}).Close(); err != nil {
		t.Fatal(err)
	}
	if got := (Store{storeID: "workspace/custom"}).StoreID(); got != "workspace/custom" {
		t.Fatalf("local StoreID = %q", got)
	}
	if got := (Store{}).StoreID(); got != ReviewStoreID {
		t.Fatalf("default StoreID = %q", got)
	}
	providerFailed := Store{brokerErr: database.NewError(database.CodeUnavailable, "provider failed")}
	if _, err := providerFailed.AcquireDeduplicationSlot(
		t.Context(),
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("failed deduplication slot = %v", err)
	}
	if _, err := providerFailed.AcquireValidationSlot(t.Context()); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("failed validation slot = %v", err)
	}
	if _, err := providerFailed.AcquireIssueGenerationSlot(
		t.Context(),
		1,
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("failed issue slot = %v", err)
	}
	if _, _, err := providerFailed.TryLockIssueGenerationAttempt(
		"owner/repo",
		"draft",
		"generation",
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("failed issue attempt = %v", err)
	}
	if _, err := providerFailed.LockAutomationController(); database.CodeOf(err) != database.CodeUnavailable {
		t.Fatalf("failed controller lock = %v", err)
	}
	if _, _, err := (Store{brokerErr: database.NewError(database.CodeUnavailable, "failed")}).acquireDatabase(
		t.Context(),
	); database.CodeOf(
		err,
	) != database.CodeUnavailable {
		t.Fatalf("failed acquire = %v", err)
	}
	lockWorkspace := t.TempDir()
	first := newSQLiteStoreLocal(lockWorkspace)
	first.brokerOwned = true
	second := newSQLiteStoreLocal(lockWorkspace)
	second.brokerOwned = true
	unlock, err := first.lock("repository")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.lock("repository"); !errors.Is(err, ErrConflict) {
		t.Fatalf("broker-owned lock conflict = %v", err)
	}
	unlock()

	t.Run("offline migration", func(t *testing.T) {
		if err := RunOfflineDatabaseMigration(t.Context(), t.TempDir()); database.CodeOf(err) != database.CodeConflict {
			t.Fatalf("unfenced migration = %v", err)
		}
		migrationHome := t.TempDir()
		fence, err := database.AcquireMigrationFence(migrationHome)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := fence.Close(); err != nil {
				t.Error(err)
			}
		}()
		if err := RunOfflineDatabaseMigration(t.Context(), filepath.Join(migrationHome, "workspace")); err != nil {
			t.Fatal(err)
		}
		fileWorkspace := filepath.Join(migrationHome, "not-a-directory")
		if err := os.WriteFile(fileWorkspace, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := RunOfflineDatabaseMigration(t.Context(), fileWorkspace); err == nil {
			t.Fatal("migration accepted a file workspace")
		}
	})

	t.Run("provider authority and open failure", func(t *testing.T) {
		restoreAuthority := database.SuspendProviderTestAuthority()
		allowUnfencedReviewProviderForTests.Store(false)
		authorityErr := reviewProviderAuthorityError()
		_, retainedErr := newRetainedReviewStore(t.TempDir())
		allowUnfencedReviewProviderForTests.Store(true)
		restoreAuthority()
		if database.CodeOf(authorityErr) != database.CodeUnauthorized ||
			database.CodeOf(retainedErr) != database.CodeUnauthorized {
			t.Fatalf("unfenced provider errors = %v, %v", authorityErr, retainedErr)
		}
		workspaceFile := filepath.Join(t.TempDir(), "workspace-file")
		if err := os.WriteFile(workspaceFile, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newRetainedReviewStore(workspaceFile); err == nil {
			t.Fatal("retained provider accepted a file workspace")
		}
	})

	closedRetained.root = t.TempDir()
	if err := closedRetained.rewriteMigrationRow(
		t.Context(), "key", "SELECT 1", "id", 1,
		func(context.Context, *sql.Conn, int64) (bool, error) { return true, nil },
	); err == nil {
		t.Fatal("migration rewrite acquired a closed pool")
	}
}

func startReviewScriptedBroker(
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

func reviewRequest(t *testing.T, operation string, value any) database.Request {
	t.Helper()
	return database.Request{
		Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
		Operation: operation, Payload: mustReviewBrokerPayload(t, value),
	}
}

func mustReviewBrokerPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := database.MarshalCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
