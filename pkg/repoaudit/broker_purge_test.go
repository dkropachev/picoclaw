package repoaudit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestReviewBrokerPurgeWholeOperationRuntime(t *testing.T) {
	store, handler := newReviewPurgeBrokerStore(t)

	automation := createReviewBrokerPurgeAutomation(
		t, store, "rra_broker_purge_reset", "owner/broker-purge-reset",
	)
	state := createReviewBrokerPurgeLedger(t, store, automation.Repository)
	snapshot, err := store.RepositoryReviewAutomationSnapshot(t.Context(), automation.ID)
	if err != nil || !snapshot.HistoryFound || snapshot.State.ID != state.ID ||
		!snapshot.PurgeEligibility.CanPurge {
		t.Fatalf("broker snapshot = %#v, %v", snapshot, err)
	}
	eligibility, err := store.RepositoryReviewPurgeEligibilityForAutomation(automation)
	if err != nil || eligibility.Summary.LedgerFence != snapshot.PurgeEligibility.Summary.LedgerFence {
		t.Fatalf("broker eligibility = %#v, %v", eligibility, err)
	}
	stale := automation
	stale.Version++
	if _, staleErr := store.RepositoryReviewPurgeEligibilityForAutomation(stale); !errors.Is(staleErr, ErrConflict) {
		t.Fatalf("stale broker eligibility = %v", staleErr)
	}

	child := handler.workspaces[store.StoreID()]
	<-child.requestGate
	provider := &child.store
	provider.openForTest = func(context.Context) (*sql.DB, error) {
		return nil, errors.New("purge opened a second database pool")
	}
	child.requestGate <- struct{}{}
	updated, applied, err := store.PurgeAutomationHistory(
		t.Context(), "  "+automation.ID+"  ", automation.Version, state.Version,
		eligibility.Summary.LedgerFence, automation.Repository,
	)
	if err != nil || !applied.CanPurge || updated.Version != automation.Version+1 ||
		!repositoryReviewAutomationHistoryReset(updated) {
		t.Fatalf("broker reset purge = %#v, %#v, %v", updated, applied, err)
	}
	if _, found, getErr := store.Get(state.Repository); getErr != nil || found {
		t.Fatalf("broker reset retained ledger found=%v err=%v", found, getErr)
	}

	removed := createReviewBrokerPurgeAutomation(
		t, store, "rra_broker_purge_remove", "owner/broker-purge-remove",
	)
	removedState := createReviewBrokerPurgeLedger(t, store, removed.Repository)
	removeEligibility, err := store.RepositoryReviewPurgeEligibilityForAutomation(removed)
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.DeleteAutomationAndHistory(
		t.Context(), removed.ID, removed.Version, removedState.Version,
		removeEligibility.Summary.LedgerFence, removed.Repository,
	)
	if err != nil || !result.CanRemove {
		t.Fatalf("broker remove purge = %#v, %v", result, err)
	}
	if _, found, getErr := store.GetAutomation(t.Context(), removed.ID); getErr != nil || found {
		t.Fatalf("removed broker automation found=%v err=%v", found, getErr)
	}

	empty := createReviewBrokerPurgeAutomation(
		t, store, "rra_broker_purge_empty", "owner/broker-purge-empty",
	)
	emptyEligibility, err := store.RepositoryReviewPurgeEligibilityForAutomation(empty)
	if err != nil || emptyEligibility.HistoryFound || !emptyEligibility.CanRemove {
		t.Fatalf("empty broker eligibility = %#v, %v", emptyEligibility, err)
	}
	if _, _, purgeErr := store.PurgeAutomationHistory(
		t.Context(), empty.ID, empty.Version, 0,
		emptyEligibility.Summary.LedgerFence, empty.Repository,
	); !errors.Is(purgeErr, ErrRepositoryReviewHistoryAbsent) {
		t.Fatalf("empty broker purge = %v", purgeErr)
	}
	missing := validAutomationForTest("rra_broker_purge_missing", "missing")
	missing.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	missing.Version = 1
	missing.CreatedAt = time.Now().UTC()
	missing.UpdatedAt = missing.CreatedAt
	if _, missingErr := store.RepositoryReviewPurgeEligibilityForAutomation(
		missing,
	); !errors.Is(
		missingErr,
		ErrConflict,
	) {
		t.Fatalf("missing broker eligibility = %v", missingErr)
	}

	blockedInput := validAutomationForTest("rra_broker_purge_blocked", "blocked")
	blockedInput.Repository = "owner/broker-purge-blocked"
	blockedInput.Status = RepositoryReviewAutomationRunning
	blockedInput.ActiveRunID = "wr_broker_purge_active"
	blockedInput.RunIDs = []string{blockedInput.ActiveRunID}
	blocked, err := store.CreateAutomation(t.Context(), blockedInput)
	if err != nil {
		t.Fatal(err)
	}
	blockedEligibility, err := store.RepositoryReviewPurgeEligibilityForAutomation(blocked)
	if err != nil || len(blockedEligibility.Blockers) != 1 {
		t.Fatalf("blocked broker eligibility = %#v, %v", blockedEligibility, err)
	}
	returned, err := store.DeleteAutomationAndHistory(
		t.Context(), blocked.ID, blocked.Version, 0,
		blockedEligibility.Summary.LedgerFence, blocked.Repository,
	)
	if !errors.Is(err, ErrRepositoryReviewPurgeBlocked) || len(returned.Blockers) != 1 ||
		returned.Blockers[0].Code != RepositoryReviewPurgeBlockerReviewActive {
		t.Fatalf("blocked broker purge = %#v, %v", returned, err)
	}
}

func TestReviewBrokerRenewLeaseBypassesPurgeRequestGate(t *testing.T) {
	handler := newReviewStoreHandler(t.TempDir(), ReviewStoreID)
	acquired, err := handler.Handle(t.Context(), reviewRequest(
		t,
		reviewOperationLock,
		reviewLockRequest{StoreID: ReviewStoreID, Key: "purge-renewal"},
	))
	if err != nil {
		t.Fatal(err)
	}
	lease := acquired.(reviewLeaseResponse)
	canceled, cancelCanceled := context.WithCancel(t.Context())
	cancelCanceled()
	if _, canceledErr := handler.Handle(canceled, reviewRequest(
		t,
		reviewOperationRenewLease,
		reviewLeaseRequest{StoreID: ReviewStoreID, LeaseID: lease.LeaseID},
	)); database.CodeOf(canceledErr) != database.CodeDeadline {
		t.Fatalf("canceled purge lease renewal = %v", canceledErr)
	}
	<-handler.requestGate
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	renewed, renewErr := handler.Handle(ctx, reviewRequest(
		t,
		reviewOperationRenewLease,
		reviewLeaseRequest{StoreID: ReviewStoreID, LeaseID: lease.LeaseID},
	))
	cancel()
	handler.requestGate <- struct{}{}
	if renewErr != nil || renewed.(reviewLeaseResponse).LeaseID != lease.LeaseID {
		t.Fatalf("purge-gated lease renewal = %#v, %v", renewed, renewErr)
	}
	if _, err := handler.Handle(t.Context(), reviewRequest(
		t,
		reviewOperationUnlock,
		reviewLeaseRequest{StoreID: ReviewStoreID, LeaseID: lease.LeaseID},
	)); err != nil {
		t.Fatal(err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReviewBrokerReconcilesProviderPurgeIntent(t *testing.T) {
	store, handler := newReviewPurgeBrokerStore(t)
	automation := createReviewBrokerPurgeAutomation(
		t, store, "rra_broker_purge_reconcile", "owner/broker-purge-reconcile",
	)
	state := createReviewBrokerPurgeLedger(t, store, automation.Repository)
	provider := handler.workspaces[store.StoreID()].store
	intent := purgeTestIntent(automation, state)
	intent.CreatedAt = time.Now().UTC()
	if err := provider.savePurgeIntent(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RepositoryReviewAutomationSnapshot(
		t.Context(), automation.ID,
	); !errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		t.Fatalf("broker snapshot ignored provider purge intent: %v", err)
	}
	if _, err := store.RepositoryReviewPurgeEligibilityForAutomation(
		automation,
	); !errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		t.Fatalf("broker eligibility ignored provider purge intent: %v", err)
	}
	if _, _, err := store.GetAutomation(
		t.Context(),
		automation.ID,
	); !errors.Is(
		err,
		ErrRepositoryReviewPurgeInProgress,
	) {
		t.Fatalf("broker automation read ignored provider purge intent: %v", err)
	}
	if _, _, err := store.Get(automation.Repository); !errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		t.Fatalf("broker ledger read ignored provider purge intent: %v", err)
	}
	if _, _, err := store.GetByID(state.ID); !errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		t.Fatalf("broker ledger ID read ignored provider purge intent: %v", err)
	}
	if _, err := store.List(); !errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		t.Fatalf("broker ledger list ignored provider purge intent: %v", err)
	}
	if _, err := store.ListSummaries(); !errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		t.Fatalf("broker summary list ignored provider purge intent: %v", err)
	}
	if _, err := store.ListAutomations(t.Context()); !errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		t.Fatalf("broker automation list ignored provider purge intent: %v", err)
	}
	replacement := validAutomationForTest("rra_broker_purge_replacement", "replacement")
	replacement.Repository = automation.Repository
	if _, err := store.CreateAutomation(t.Context(), replacement); !errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		t.Fatalf("broker reassignment ignored provider purge intent: %v", err)
	}
	completed, err := store.ReconcilePurgeIntents(t.Context())
	if err != nil || completed != 1 {
		t.Fatalf("broker purge reconciliation = %d, %v", completed, err)
	}
	if completed, err = store.ReconcilePurgeIntents(t.Context()); err != nil || completed != 0 {
		t.Fatalf("idempotent broker purge reconciliation = %d, %v", completed, err)
	}
	snapshot, err := store.RepositoryReviewAutomationSnapshot(t.Context(), automation.ID)
	if err != nil || snapshot.HistoryFound || snapshot.Automation.Version != automation.Version+1 ||
		!repositoryReviewAutomationHistoryReset(snapshot.Automation) {
		t.Fatalf("reconciled broker snapshot = %#v, %v", snapshot, err)
	}
	if _, err := os.Stat(provider.purgeAutomationIntentPath(automation.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider purge intent survived reconciliation: %v", err)
	}
}

func TestReviewBrokerPurgeResponseValidationAndIntentBound(t *testing.T) {
	if err := validateReviewPurgeSnapshotResponse(
		reviewPurgeSnapshotResponse{
			Outcome:     reviewPurgeOutcomeInProgress,
			Eligibility: RepositoryReviewPurgeEligibility{CanRemove: true},
		},
		reviewPurgeProjectionDetail,
		"rra_invalid_response",
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid in-progress snapshot response = %v", err)
	}
	if err := validateReviewPurgeAutomationResponse(
		reviewPurgeAutomationResponse{Outcome: "unknown"},
		reviewPurgeAutomationRequest{Mode: repositoryReviewPurgeReset},
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid purge mutation response = %v", err)
	}
	if got := mapReviewPurgeClientError(
		database.NewError(database.CodeInvalid, "invalid"),
	); !errors.Is(got, ErrInvalidAutomation) {
		t.Fatalf("purge client error = %v", got)
	}

	store := newAutomationTestStore(t)
	padding := strings.Repeat("x", maxRepositoryIdentityBytes-16)
	targets := make([]repositoryReviewPurgeLedgerTarget, 1_100)
	for index := range targets {
		targets[index] = repositoryReviewPurgeLedgerTarget{
			Repository: fmt.Sprintf("%04d-%s", index, padding), Version: 1,
		}
	}
	intent := repositoryReviewPurgeIntent{
		SchemaVersion:             repositoryReviewPurgeIntentSchemaVersion,
		Mode:                      repositoryReviewPurgeReset,
		Phase:                     repositoryReviewPurgePrepared,
		AutomationID:              "rra_oversized_purge_intent",
		ConfiguredRepository:      "owner/repo",
		Repository:                targets[0].Repository,
		LedgerTargets:             targets,
		ExpectedAutomationVersion: 1,
		ExpectedRepositoryVersion: 1,
		CreatedAt:                 time.Now().UTC(),
	}
	if err := store.savePurgeIntent(intent); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("oversized purge intent = %v", err)
	}
	if _, err := os.Stat(store.purgeAutomationIntentPath(intent.AutomationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized primary purge intent was published: %v", err)
	}
}

func TestReviewBrokerPurgeWireBoundaryValidation(t *testing.T) {
	local := newAutomationTestStore(t)
	automation := createAutomationForTest(t, local, "rra_purge_wire_fixture", "wire")
	state := createPurgeTestLedger(t, local, automation.Repository)
	eligibility, err := local.RepositoryReviewPurgeEligibilityForAutomation(automation)
	if err != nil {
		t.Fatal(err)
	}
	detail := reviewPurgeSnapshotResponse{
		Outcome: reviewPurgeOutcomeReady, Automation: &automation, State: &state,
		HistoryFound: true, Eligibility: eligibility,
	}
	if err := validateReviewPurgeSnapshotResponse(
		detail,
		reviewPurgeProjectionDetail,
		automation.ID,
	); err != nil {
		t.Fatal(err)
	}
	eligibilityOnly := reviewPurgeSnapshotResponse{
		Outcome: reviewPurgeOutcomeReady, Eligibility: eligibility,
	}
	if err := validateReviewPurgeSnapshotResponse(
		eligibilityOnly,
		reviewPurgeProjectionEligibility,
		automation.ID,
	); err != nil {
		t.Fatal(err)
	}
	inventoryUnavailable := detail
	inventoryUnavailable.Eligibility = RepositoryReviewPurgeEligibility{}
	inventoryUnavailable.InventoryError = database.NewError(database.CodeInternal, "inventory unavailable")
	if err := validateReviewPurgeSnapshotResponse(
		inventoryUnavailable,
		reviewPurgeProjectionDetail,
		automation.ID,
	); err != nil {
		t.Fatal(err)
	}

	invalidSnapshots := []reviewPurgeSnapshotResponse{
		{Outcome: "unknown"},
		{Outcome: reviewPurgeOutcomeReady},
		func() reviewPurgeSnapshotResponse {
			value := detail
			value.HistoryFound = false
			return value
		}(),
		func() reviewPurgeSnapshotResponse {
			value := detail
			changed := state
			changed.Repository = "owner/unrelated"
			value.State = &changed
			return value
		}(),
		func() reviewPurgeSnapshotResponse {
			value := inventoryUnavailable
			value.InventoryError = &database.Error{}
			return value
		}(),
		func() reviewPurgeSnapshotResponse {
			value := detail
			value.Eligibility.Summary.RepositoryVersion++
			return value
		}(),
		func() reviewPurgeSnapshotResponse {
			value := detail
			malformed := automation
			malformed.Name = ""
			value.Automation = &malformed
			return value
		}(),
	}
	for index, response := range invalidSnapshots {
		if err := validateReviewPurgeSnapshotResponse(
			response,
			reviewPurgeProjectionDetail,
			automation.ID,
		); database.CodeOf(err) != database.CodeIntegrity {
			t.Errorf("invalid snapshot %d = %v", index, err)
		}
	}
	extraEligibility := eligibilityOnly
	extraEligibility.Automation = &automation
	if err := validateReviewPurgeSnapshotResponse(
		extraEligibility,
		reviewPurgeProjectionEligibility,
		automation.ID,
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid eligibility projection = %v", err)
	}

	request := reviewPurgeAutomationRequest{
		Mode: repositoryReviewPurgeReset, AutomationID: automation.ID,
		ExpectedAutomationVersion: automation.Version,
		ExpectedRepositoryVersion: state.Version,
		ExpectedLedgerFence:       eligibility.Summary.LedgerFence,
		ConfirmRepository:         automation.Repository,
	}
	reset := cloneAutomation(automation)
	resetRepositoryReviewAutomationHistory(&reset)
	reset.Version++
	reset.UpdatedAt = reset.UpdatedAt.Add(time.Second)
	validReset := reviewPurgeAutomationResponse{
		Outcome: reviewPurgeOutcomeApplied, Automation: &reset, Eligibility: eligibility,
	}
	if err := validateReviewPurgeAutomationResponse(validReset, request); err != nil {
		t.Fatal(err)
	}
	invalidApplied := validReset
	invalidApplied.Eligibility.Summary.LedgerFence = repositoryReviewPurgeLedgerFence(nil)
	if err := validateReviewPurgeAutomationResponse(
		invalidApplied,
		request,
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid applied eligibility = %v", err)
	}
	invalidReset := validReset
	invalidReset.Automation = nil
	if err := validateReviewPurgeAutomationResponse(
		invalidReset,
		request,
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid reset response = %v", err)
	}
	removeRequest := request
	removeRequest.Mode = repositoryReviewPurgeRemove
	invalidRemove := reviewPurgeAutomationResponse{
		Outcome: reviewPurgeOutcomeApplied, Automation: &automation, Eligibility: eligibility,
	}
	if err := validateReviewPurgeAutomationResponse(
		invalidRemove,
		removeRequest,
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid remove response = %v", err)
	}
	blocked := eligibility
	blocked.CanPurge, blocked.CanRemove = false, false
	blocked.Blockers = []RepositoryReviewPurgeBlocker{{
		Code: RepositoryReviewPurgeBlockerReviewActive, Count: 1,
		Message: repositoryReviewPurgeBlockerMessage(RepositoryReviewPurgeBlockerReviewActive),
	}}
	invalidBlocked := reviewPurgeAutomationResponse{
		Outcome: reviewPurgeOutcomeBlocked, Automation: &automation, Eligibility: blocked,
	}
	if err := validateReviewPurgeAutomationResponse(
		invalidBlocked,
		request,
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid blocked response = %v", err)
	}
	absent := EvaluateRepositoryReviewPurge(automation, RepositoryState{}, false)
	absentRequest := request
	absentRequest.ExpectedRepositoryVersion = 0
	absentRequest.ExpectedLedgerFence = absent.Summary.LedgerFence
	invalidAbsent := reviewPurgeAutomationResponse{
		Outcome: reviewPurgeOutcomeHistoryAbsent, Automation: &automation, Eligibility: absent,
	}
	if err := validateReviewPurgeAutomationResponse(
		invalidAbsent,
		absentRequest,
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid history-absent response = %v", err)
	}
	if err := validateReviewPurgeAutomationResponse(
		reviewPurgeAutomationResponse{
			Outcome: reviewPurgeOutcomeInProgress, Automation: &automation,
		},
		request,
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("invalid in-progress response = %v", err)
	}

	fallbackAutomation := automation
	fallbackAutomation.Repository = "owner/configured"
	fallbackAutomation.RunIDs = []string{"", "wr_fallback"}
	fallbackState := state
	fallbackState.Repository = "legacy/fallback"
	fallbackState.Runs = []ReviewRun{{ID: "wr_fallback"}}
	if !reviewPurgeSnapshotStateMatchesAutomation(fallbackAutomation, fallbackState) {
		t.Fatal("run-bound fallback state was rejected")
	}
	fallbackState.Runs[0].ID = "wr_other"
	if reviewPurgeSnapshotStateMatchesAutomation(fallbackAutomation, fallbackState) {
		t.Fatal("unrelated fallback state was accepted")
	}
	if validReviewPurgeSnapshotRequest(reviewPurgeSnapshotRequest{Projection: "unknown"}) ||
		validReviewBrokerAutomation(RepositoryReviewAutomation{}, "") ||
		validRepositoryReviewPurgeEligibility(RepositoryReviewPurgeEligibility{}) {
		t.Fatal("invalid purge wire value was accepted")
	}
	badBlocker := blocked
	badBlocker.Blockers[0].Count = 0
	if validRepositoryReviewPurgeEligibility(badBlocker) {
		t.Fatal("invalid purge blocker was accepted")
	}
	for brokerCode, want := range map[database.ErrorCode]error{
		database.CodeConflict: ErrConflict,
		database.CodeNotFound: os.ErrNotExist,
		database.CodeInvalid:  ErrInvalidAutomation,
	} {
		if got := mapReviewPurgeClientError(database.NewError(brokerCode, "mapped")); !errors.Is(got, want) {
			t.Errorf("mapReviewPurgeClientError(%s) = %v", brokerCode, got)
		}
	}
	if mapReviewPurgeClientError(nil) != nil ||
		database.CodeOf(mapReviewPurgeClientError(database.NewError(database.CodeInternal, "kept"))) !=
			database.CodeInternal {
		t.Fatal("purge client error passthrough failed")
	}
}

func TestReviewBrokerPurgeClientRejectsTransportAndMalformedResponses(t *testing.T) {
	sentinel := database.NewError(database.CodeUnavailable, "broker failed")
	failed := Store{broker: &database.Client{}, brokerErr: sentinel, storeID: ReviewStoreID}
	if _, err := failed.brokerRepositoryReviewAutomationSnapshot(
		t.Context(),
		"rra_failed_snapshot",
	); !errors.Is(err, sentinel) {
		t.Fatalf("failed snapshot broker = %v", err)
	}
	if _, err := failed.brokerRepositoryReviewPurgeEligibility(
		t.Context(),
		validAutomationForTest("rra_failed_eligibility", "failed"),
	); !errors.Is(err, sentinel) {
		t.Fatalf("failed eligibility broker = %v", err)
	}
	if _, _, err := failed.brokerPurgeAutomation(
		t.Context(), "rra_failed_purge", 1, 0, repositoryReviewPurgeLedgerFence(nil),
		"owner/repo", repositoryReviewPurgeReset,
	); !errors.Is(err, sentinel) {
		t.Fatalf("failed purge broker = %v", err)
	}
	if _, err := failed.brokerReconcilePurgeIntents(t.Context()); !errors.Is(err, sentinel) {
		t.Fatalf("failed reconcile broker = %v", err)
	}
	if _, err := (Store{}).brokerPurgeSnapshot(t.Context(), reviewPurgeSnapshotRequest{}); err == nil {
		t.Fatal("nil snapshot broker was accepted")
	}
	if _, _, err := (Store{}).brokerPurgeAutomation(
		t.Context(), "rra_nil_purge", 1, 0, repositoryReviewPurgeLedgerFence(nil),
		"owner/repo", repositoryReviewPurgeReset,
	); err == nil {
		t.Fatal("nil purge broker was accepted")
	}
	if _, err := (Store{}).brokerReconcilePurgeIntents(t.Context()); err == nil {
		t.Fatal("nil reconcile broker was accepted")
	}

	local := newAutomationTestStore(t)
	automation := createAutomationForTest(t, local, "rra_malformed_wire", "malformed")
	state := createPurgeTestLedger(t, local, automation.Repository)
	home := t.TempDir()
	server, err := database.StartServer(t.Context(), database.ServerOptions{
		Home: home,
		Handler: database.HandlerFunc(func(_ context.Context, request database.Request) (any, error) {
			switch request.Operation {
			case reviewOperationPurgeSnapshot:
				var input reviewPurgeSnapshotRequest
				if decodeErr := request.DecodePayload(&input); decodeErr != nil {
					return nil, decodeErr
				}
				if input.AutomationID == automation.ID {
					return reviewPurgeSnapshotResponse{
						Outcome: reviewPurgeOutcomeReady, Automation: &automation, State: &state,
						HistoryFound:   true,
						InventoryError: database.NewError(database.CodeInternal, "inventory failed"),
					}, nil
				}
				return reviewPurgeSnapshotResponse{Outcome: reviewPurgeOutcomeReady}, nil
			case reviewOperationPurgeAutomation:
				return reviewPurgeAutomationResponse{Outcome: "invalid"}, nil
			default:
				return reviewReconcilePurgesResponse{Completed: repositoryReviewPurgeIntentLimit + 1}, nil
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := server.Close(ctx); closeErr != nil {
			t.Error(closeErr)
		}
	})
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	store := Store{broker: client, storeID: ReviewStoreID, brokerState: &auditBrokerClientState{}}
	snapshot, err := store.brokerRepositoryReviewAutomationSnapshot(t.Context(), automation.ID)
	if err != nil || snapshot.PurgeInventoryError == nil || !snapshot.HistoryFound {
		t.Fatalf("inventory-error snapshot = %#v, %v", snapshot, err)
	}
	if _, err := store.brokerRepositoryReviewAutomationSnapshot(
		t.Context(),
		"rra_invalid_snapshot_response",
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed snapshot response = %v", err)
	}
	if _, err := store.brokerRepositoryReviewPurgeEligibility(
		t.Context(),
		validAutomationForTest("rra_invalid_eligibility_response", "invalid"),
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed eligibility response = %v", err)
	}
	if _, _, err := store.brokerPurgeAutomation(
		t.Context(), automation.ID, automation.Version, state.Version,
		purgeTestFence(state), automation.Repository, repositoryReviewPurgeReset,
	); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed purge response = %v", err)
	}
	if _, err := store.brokerReconcilePurgeIntents(t.Context()); database.CodeOf(err) != database.CodeIntegrity {
		t.Fatalf("malformed reconcile response = %v", err)
	}

	transportFailed := Store{
		broker: &database.Client{}, storeID: ReviewStoreID, brokerState: &auditBrokerClientState{},
	}
	if _, err := transportFailed.brokerPurgeSnapshot(
		t.Context(),
		reviewPurgeSnapshotRequest{StoreID: ReviewStoreID},
	); err == nil {
		t.Fatal("snapshot transport failure was ignored")
	}
	if _, _, err := transportFailed.brokerPurgeAutomation(
		t.Context(), automation.ID, automation.Version, state.Version,
		purgeTestFence(state), automation.Repository, repositoryReviewPurgeReset,
	); err == nil {
		t.Fatal("purge transport failure was ignored")
	}
	if _, err := transportFailed.brokerReconcilePurgeIntents(t.Context()); err == nil {
		t.Fatal("reconcile transport failure was ignored")
	}
}

func TestReviewBrokerPurgeHandlerBoundaries(t *testing.T) {
	handler := newReviewStoreHandler(t.TempDir(), ReviewStoreID)
	invalid := []database.Request{
		{
			Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
			Operation: reviewOperationReconcilePurges, Payload: []byte(`{}`),
		},
		{
			Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
			Operation: reviewOperationPurgeSnapshot, Payload: []byte(`{}`),
		},
		{
			Domain: reviewBrokerDomain, Version: reviewBrokerVersion,
			Operation: reviewOperationPurgeAutomation, Payload: []byte(`{}`),
		},
		reviewRequest(
			t,
			reviewOperationPurgeAutomation,
			reviewPurgeAutomationRequest{
				StoreID: ReviewStoreID, Mode: repositoryReviewPurgeReset,
				AutomationID: " rra_padded_purge ", ExpectedAutomationVersion: 1,
				ExpectedLedgerFence: repositoryReviewPurgeLedgerFence(nil),
				ConfirmRepository:   "owner/repo",
			},
		),
	}
	for _, request := range invalid {
		if _, err := handler.handlePurgeOperation(
			t.Context(),
			request,
		); database.CodeOf(err) != database.CodeInvalid {
			t.Errorf("invalid %s request = %v", request.Operation, err)
		}
	}
	missingSnapshot := reviewRequest(
		t,
		reviewOperationPurgeSnapshot,
		reviewPurgeSnapshotRequest{
			StoreID: ReviewStoreID, AutomationID: "rra_missing_purge_snapshot",
			Projection: reviewPurgeProjectionDetail,
		},
	)
	if _, err := handler.handlePurgeOperation(
		t.Context(),
		missingSnapshot,
	); database.CodeOf(err) != database.CodeNotFound {
		t.Fatalf("missing purge snapshot = %v", err)
	}
	missingPurge := reviewRequest(
		t,
		reviewOperationPurgeAutomation,
		reviewPurgeAutomationRequest{
			StoreID: ReviewStoreID, Mode: repositoryReviewPurgeReset,
			AutomationID: "rra_missing_purge_mutation", ExpectedAutomationVersion: 1,
			ExpectedLedgerFence: repositoryReviewPurgeLedgerFence(nil),
			ConfirmRepository:   "owner/repo",
		},
	)
	if _, err := handler.handlePurgeOperation(
		t.Context(),
		missingPurge,
	); database.CodeOf(err) != database.CodeNotFound {
		t.Fatalf("missing purge mutation = %v", err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}
}

func newReviewPurgeBrokerStore(t *testing.T) (Store, *BrokerHandler) {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	handler := newReviewBrokerHandlerForTest(t, home, workspace)
	server := startReviewBroker(t, home, handler)
	t.Cleanup(func() { closeReviewBroker(t, server) })
	client, err := database.Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	setReviewBrokerClientForTest(t, client)
	store := NewSQLiteStore(workspace)
	if store.root != "" || store.database != "" || store.StoreID() == "" {
		t.Fatalf("broker purge Store = %#v", store)
	}
	return store, handler
}

func createReviewBrokerPurgeAutomation(
	t *testing.T,
	store Store,
	id string,
	repository string,
) RepositoryReviewAutomation {
	t.Helper()
	input := validAutomationForTest(id, id)
	input.Repository = repository
	automation, err := store.CreateAutomation(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	return automation
}

func createReviewBrokerPurgeLedger(t *testing.T, store Store, repository string) RepositoryState {
	t.Helper()
	plan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(), repository, "commit", "inventory", "profile", nil,
		false, maxReviewFiles, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.FinalizeNoopPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	return state
}
