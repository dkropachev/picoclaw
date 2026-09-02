package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	reviewOperationPurgeSnapshot   = "purge-snapshot"
	reviewOperationPurgeAutomation = "purge-automation"
	reviewOperationReconcilePurges = "reconcile-purges"
)

type reviewPurgeProjection string

const (
	reviewPurgeProjectionDetail      reviewPurgeProjection = "detail"
	reviewPurgeProjectionEligibility reviewPurgeProjection = "eligibility"
)

type reviewPurgeOutcome string

const (
	reviewPurgeOutcomeReady         reviewPurgeOutcome = "ready"
	reviewPurgeOutcomeApplied       reviewPurgeOutcome = "applied"
	reviewPurgeOutcomeBlocked       reviewPurgeOutcome = "blocked"
	reviewPurgeOutcomeHistoryAbsent reviewPurgeOutcome = "history_absent"
	reviewPurgeOutcomeInProgress    reviewPurgeOutcome = "in_progress"
)

type reviewPurgeSnapshotRequest struct {
	StoreID            database.StoreID      `json:"store_id"`
	AutomationID       string                `json:"automation_id"`
	Projection         reviewPurgeProjection `json:"projection"`
	ExpectedVersion    int64                 `json:"expected_version,omitempty"`
	ExpectedRepository string                `json:"expected_repository,omitempty"`
}

type reviewPurgeSnapshotResponse struct {
	Outcome        reviewPurgeOutcome               `json:"outcome"`
	Automation     *RepositoryReviewAutomation      `json:"automation,omitempty"`
	State          *RepositoryState                 `json:"state,omitempty"`
	HistoryFound   bool                             `json:"history_found"`
	Eligibility    RepositoryReviewPurgeEligibility `json:"eligibility"`
	InventoryError *database.Error                  `json:"inventory_error,omitempty"`
}

type reviewPurgeAutomationRequest struct {
	StoreID                   database.StoreID          `json:"store_id"`
	Mode                      repositoryReviewPurgeMode `json:"mode"`
	AutomationID              string                    `json:"automation_id"`
	ExpectedAutomationVersion int64                     `json:"expected_automation_version"`
	ExpectedRepositoryVersion int64                     `json:"expected_repository_version"`
	ExpectedLedgerFence       string                    `json:"expected_ledger_fence"`
	ConfirmRepository         string                    `json:"confirm_repository"`
}

type reviewPurgeAutomationResponse struct {
	Outcome     reviewPurgeOutcome               `json:"outcome"`
	Automation  *RepositoryReviewAutomation      `json:"automation,omitempty"`
	Eligibility RepositoryReviewPurgeEligibility `json:"eligibility"`
}

type reviewReconcilePurgesResponse struct {
	Completed int `json:"completed"`
}

func (s Store) brokerRepositoryReviewAutomationSnapshot(
	ctx context.Context,
	id string,
) (RepositoryReviewAutomationSnapshot, error) {
	response, err := s.brokerPurgeSnapshot(ctx, reviewPurgeSnapshotRequest{
		StoreID: s.StoreID(), AutomationID: id, Projection: reviewPurgeProjectionDetail,
	})
	if err != nil {
		return RepositoryReviewAutomationSnapshot{}, err
	}
	if err := validateReviewPurgeSnapshotResponse(response, reviewPurgeProjectionDetail, id); err != nil {
		return RepositoryReviewAutomationSnapshot{}, err
	}
	if response.Outcome == reviewPurgeOutcomeInProgress {
		return RepositoryReviewAutomationSnapshot{}, ErrRepositoryReviewPurgeInProgress
	}
	snapshot := RepositoryReviewAutomationSnapshot{
		Automation:       *response.Automation,
		HistoryFound:     response.HistoryFound,
		PurgeEligibility: response.Eligibility,
	}
	if response.State != nil {
		snapshot.State = *response.State
	}
	if response.InventoryError != nil {
		snapshot.PurgeInventoryError = database.NewError(
			response.InventoryError.Code,
			response.InventoryError.Message,
		)
	}
	return snapshot, nil
}

func (s Store) brokerRepositoryReviewPurgeEligibility(
	ctx context.Context,
	automation RepositoryReviewAutomation,
) (RepositoryReviewPurgeEligibility, error) {
	response, err := s.brokerPurgeSnapshot(ctx, reviewPurgeSnapshotRequest{
		StoreID: s.StoreID(), AutomationID: automation.ID,
		Projection:      reviewPurgeProjectionEligibility,
		ExpectedVersion: automation.Version, ExpectedRepository: automation.Repository,
	})
	if err != nil {
		return RepositoryReviewPurgeEligibility{}, err
	}
	if err := validateReviewPurgeSnapshotResponse(
		response,
		reviewPurgeProjectionEligibility,
		automation.ID,
	); err != nil {
		return RepositoryReviewPurgeEligibility{}, err
	}
	if response.Outcome == reviewPurgeOutcomeInProgress {
		return RepositoryReviewPurgeEligibility{}, ErrRepositoryReviewPurgeInProgress
	}
	return response.Eligibility, nil
}

func (s Store) brokerPurgeSnapshot(
	ctx context.Context,
	input reviewPurgeSnapshotRequest,
) (reviewPurgeSnapshotResponse, error) {
	if s.brokerErr != nil {
		return reviewPurgeSnapshotResponse{}, s.brokerErr
	}
	if s.broker == nil {
		return reviewPurgeSnapshotResponse{}, database.NewError(
			database.CodeUnavailable,
			"repository review broker is unavailable",
		)
	}
	var response reviewPurgeSnapshotResponse
	err := s.broker.Call(
		ctx,
		reviewBrokerDomain,
		reviewBrokerVersion,
		reviewOperationPurgeSnapshot,
		input,
		&response,
	)
	if err != nil {
		return reviewPurgeSnapshotResponse{}, mapReviewPurgeClientError(err)
	}
	return response, nil
}

func (s Store) brokerPurgeAutomation(
	ctx context.Context,
	id string,
	expectedAutomationVersion int64,
	expectedRepositoryVersion int64,
	expectedLedgerFence string,
	confirmRepository string,
	mode repositoryReviewPurgeMode,
) (RepositoryReviewAutomation, RepositoryReviewPurgeEligibility, error) {
	if s.brokerErr != nil {
		return RepositoryReviewAutomation{}, RepositoryReviewPurgeEligibility{}, s.brokerErr
	}
	if s.broker == nil {
		return RepositoryReviewAutomation{}, RepositoryReviewPurgeEligibility{}, database.NewError(
			database.CodeUnavailable,
			"repository review broker is unavailable",
		)
	}
	input := reviewPurgeAutomationRequest{
		StoreID: s.StoreID(), Mode: mode, AutomationID: id,
		ExpectedAutomationVersion: expectedAutomationVersion,
		ExpectedRepositoryVersion: expectedRepositoryVersion,
		ExpectedLedgerFence:       expectedLedgerFence,
		ConfirmRepository:         confirmRepository,
	}
	var response reviewPurgeAutomationResponse
	err := s.broker.CallWithOptions(
		ctx,
		reviewBrokerDomain,
		reviewBrokerVersion,
		reviewOperationPurgeAutomation,
		input,
		&response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		return RepositoryReviewAutomation{}, RepositoryReviewPurgeEligibility{},
			mapReviewPurgeClientError(err)
	}
	if err := validateReviewPurgeAutomationResponse(response, input); err != nil {
		return RepositoryReviewAutomation{}, RepositoryReviewPurgeEligibility{}, err
	}
	var automation RepositoryReviewAutomation
	if response.Automation != nil {
		automation = *response.Automation
	}
	switch response.Outcome {
	case reviewPurgeOutcomeApplied:
		return automation, response.Eligibility, nil
	case reviewPurgeOutcomeBlocked:
		return RepositoryReviewAutomation{}, response.Eligibility, ErrRepositoryReviewPurgeBlocked
	case reviewPurgeOutcomeHistoryAbsent:
		return RepositoryReviewAutomation{}, response.Eligibility, ErrRepositoryReviewHistoryAbsent
	default:
		return RepositoryReviewAutomation{}, RepositoryReviewPurgeEligibility{},
			ErrRepositoryReviewPurgeInProgress
	}
}

func (s Store) brokerReconcilePurgeIntents(ctx context.Context) (int, error) {
	if s.brokerErr != nil {
		return 0, s.brokerErr
	}
	if s.broker == nil {
		return 0, database.NewError(database.CodeUnavailable, "repository review broker is unavailable")
	}
	var response reviewReconcilePurgesResponse
	err := s.broker.CallWithOptions(
		ctx,
		reviewBrokerDomain,
		reviewBrokerVersion,
		reviewOperationReconcilePurges,
		reviewTarget{StoreID: s.StoreID()},
		&response,
		database.CallOptions{Mutation: true},
	)
	if err != nil {
		return 0, mapReviewPurgeClientError(err)
	}
	if response.Completed < 0 || response.Completed > repositoryReviewPurgeIntentLimit {
		return 0, database.NewError(
			database.CodeIntegrity,
			"repository review purge reconciliation response is invalid",
		)
	}
	return response.Completed, nil
}

func (handler *reviewStoreHandler) handlePurgeOperation(
	ctx context.Context,
	request database.Request,
) (any, error) {
	switch request.Operation {
	case reviewOperationPurgeSnapshot:
		return handler.handlePurgeSnapshot(ctx, request)
	case reviewOperationPurgeAutomation:
		return handler.handlePurgeAutomation(ctx, request)
	default:
		var input reviewTarget
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review purge request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		completed, reconcileErr := store.ReconcilePurgeIntents(ctx)
		if reconcileErr != nil {
			return nil, mapReviewBrokerError(reconcileErr)
		}
		return reviewReconcilePurgesResponse{Completed: completed}, nil
	}
}

func (handler *reviewStoreHandler) handlePurgeSnapshot(
	ctx context.Context,
	request database.Request,
) (any, error) {
	var input reviewPurgeSnapshotRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
		input.AutomationID != strings.TrimSpace(input.AutomationID) ||
		!validAutomationID(input.AutomationID) ||
		!validReviewPurgeSnapshotRequest(input) {
		return nil, database.NewError(database.CodeInvalid, "repository review purge request is invalid")
	}
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	snapshot, err := store.RepositoryReviewAutomationSnapshot(ctx, input.AutomationID)
	if errors.Is(err, ErrRepositoryReviewPurgeInProgress) {
		return reviewPurgeSnapshotResponse{Outcome: reviewPurgeOutcomeInProgress}, nil
	}
	if err != nil {
		if input.Projection == reviewPurgeProjectionEligibility && errors.Is(err, os.ErrNotExist) {
			return nil, mapReviewBrokerError(ErrConflict)
		}
		return nil, mapReviewBrokerError(err)
	}
	if input.Projection == reviewPurgeProjectionEligibility {
		if snapshot.Automation.Version != input.ExpectedVersion ||
			snapshot.Automation.Repository != input.ExpectedRepository {
			return nil, mapReviewBrokerError(ErrConflict)
		}
		if snapshot.PurgeInventoryError != nil {
			return nil, mapReviewBrokerError(snapshot.PurgeInventoryError)
		}
		return reviewPurgeSnapshotResponse{
			Outcome: reviewPurgeOutcomeReady, Eligibility: snapshot.PurgeEligibility,
		}, nil
	}
	response := reviewPurgeSnapshotResponse{
		Outcome:      reviewPurgeOutcomeReady,
		Automation:   &snapshot.Automation,
		HistoryFound: snapshot.HistoryFound,
		Eligibility:  snapshot.PurgeEligibility,
	}
	if snapshot.HistoryFound {
		response.State = &snapshot.State
	}
	if snapshot.PurgeInventoryError != nil {
		mapped := mapReviewBrokerError(snapshot.PurgeInventoryError)
		var structured *database.Error
		if !errors.As(mapped, &structured) || structured == nil {
			return nil, database.NewError(database.CodeInternal, "repository review purge inventory failed")
		}
		response.InventoryError = database.NewError(structured.Code, structured.Message)
	}
	return response, nil
}

func (handler *reviewStoreHandler) handlePurgeAutomation(
	ctx context.Context,
	request database.Request,
) (any, error) {
	var input reviewPurgeAutomationRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID ||
		(input.Mode != repositoryReviewPurgeReset && input.Mode != repositoryReviewPurgeRemove) ||
		input.AutomationID != strings.TrimSpace(input.AutomationID) ||
		!validAutomationID(input.AutomationID) || input.ExpectedAutomationVersion < 1 ||
		input.ExpectedRepositoryVersion < 0 ||
		!validRepositoryReviewPurgeLedgerFence(input.ExpectedLedgerFence) ||
		input.ConfirmRepository != strings.TrimSpace(input.ConfirmRepository) ||
		!validBoundedText(input.ConfirmRepository, maxRepositoryIdentityBytes) ||
		!validAutomationRepository(input.ConfirmRepository) {
		return nil, database.NewError(database.CodeInvalid, "repository review purge request is invalid")
	}
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	var automation RepositoryReviewAutomation
	var eligibility RepositoryReviewPurgeEligibility
	if input.Mode == repositoryReviewPurgeReset {
		automation, eligibility, err = store.PurgeAutomationHistory(
			ctx,
			input.AutomationID,
			input.ExpectedAutomationVersion,
			input.ExpectedRepositoryVersion,
			input.ExpectedLedgerFence,
			input.ConfirmRepository,
		)
	} else {
		eligibility, err = store.DeleteAutomationAndHistory(
			ctx,
			input.AutomationID,
			input.ExpectedAutomationVersion,
			input.ExpectedRepositoryVersion,
			input.ExpectedLedgerFence,
			input.ConfirmRepository,
		)
	}
	switch {
	case err == nil:
		response := reviewPurgeAutomationResponse{
			Outcome: reviewPurgeOutcomeApplied, Eligibility: eligibility,
		}
		if input.Mode == repositoryReviewPurgeReset {
			response.Automation = &automation
		}
		return response, nil
	case errors.Is(err, ErrRepositoryReviewPurgeBlocked):
		return reviewPurgeAutomationResponse{
			Outcome: reviewPurgeOutcomeBlocked, Eligibility: eligibility,
		}, nil
	case errors.Is(err, ErrRepositoryReviewHistoryAbsent):
		return reviewPurgeAutomationResponse{
			Outcome: reviewPurgeOutcomeHistoryAbsent, Eligibility: eligibility,
		}, nil
	case errors.Is(err, ErrRepositoryReviewPurgeInProgress):
		return reviewPurgeAutomationResponse{Outcome: reviewPurgeOutcomeInProgress}, nil
	default:
		return nil, mapReviewBrokerError(err)
	}
}

func validReviewPurgeSnapshotRequest(input reviewPurgeSnapshotRequest) bool {
	switch input.Projection {
	case reviewPurgeProjectionDetail:
		return input.ExpectedVersion == 0 && input.ExpectedRepository == ""
	case reviewPurgeProjectionEligibility:
		return input.ExpectedVersion > 0 &&
			validBoundedText(input.ExpectedRepository, maxRepositoryIdentityBytes) &&
			validAutomationRepository(input.ExpectedRepository)
	default:
		return false
	}
}

func validateReviewPurgeSnapshotResponse(
	response reviewPurgeSnapshotResponse,
	projection reviewPurgeProjection,
	id string,
) error {
	invalid := func() error {
		return database.NewError(database.CodeIntegrity, "repository review purge snapshot response is invalid")
	}
	if response.Outcome == reviewPurgeOutcomeInProgress {
		if response.Automation != nil || response.State != nil || response.HistoryFound ||
			response.InventoryError != nil ||
			!reflect.DeepEqual(response.Eligibility, RepositoryReviewPurgeEligibility{}) {
			return invalid()
		}
		return nil
	}
	if response.Outcome != reviewPurgeOutcomeReady {
		return invalid()
	}
	if projection == reviewPurgeProjectionEligibility {
		if response.Automation != nil || response.State != nil || response.HistoryFound ||
			response.InventoryError != nil || !validRepositoryReviewPurgeEligibility(response.Eligibility) {
			return invalid()
		}
		return nil
	}
	if response.Automation == nil || !validReviewBrokerAutomation(*response.Automation, id) {
		return invalid()
	}
	if response.HistoryFound != (response.State != nil) {
		return invalid()
	}
	if response.State != nil {
		encoded, err := json.Marshal(response.State)
		if err != nil || int64(len(encoded)) > maxStateFileBytes || validateState(*response.State) != nil ||
			!reviewPurgeSnapshotStateMatchesAutomation(*response.Automation, *response.State) {
			return invalid()
		}
	}
	if response.InventoryError != nil {
		if !response.InventoryError.Code.Valid() || strings.TrimSpace(response.InventoryError.Message) == "" ||
			!reflect.DeepEqual(response.Eligibility, RepositoryReviewPurgeEligibility{}) {
			return invalid()
		}
		return nil
	}
	if !validRepositoryReviewPurgeEligibility(response.Eligibility) ||
		response.Eligibility.HistoryFound != response.HistoryFound ||
		response.HistoryFound && response.Eligibility.Summary.RepositoryVersion != response.State.Version {
		return invalid()
	}
	return nil
}

func validateReviewPurgeAutomationResponse(
	response reviewPurgeAutomationResponse,
	request reviewPurgeAutomationRequest,
) error {
	invalid := func() error {
		return database.NewError(database.CodeIntegrity, "repository review purge mutation response is invalid")
	}
	switch response.Outcome {
	case reviewPurgeOutcomeApplied:
		if !validRepositoryReviewPurgeEligibility(response.Eligibility) ||
			!reviewPurgeEligibilityMatchesRequest(response.Eligibility, request) {
			return invalid()
		}
		if request.Mode == repositoryReviewPurgeReset {
			if response.Automation == nil ||
				!validReviewBrokerAutomation(*response.Automation, request.AutomationID) ||
				response.Automation.Version != request.ExpectedAutomationVersion+1 ||
				response.Automation.Repository != request.ConfirmRepository ||
				!repositoryReviewAutomationHistoryReset(*response.Automation) ||
				!response.Eligibility.CanPurge {
				return invalid()
			}
		} else if response.Automation != nil || !response.Eligibility.CanRemove {
			return invalid()
		}
	case reviewPurgeOutcomeBlocked:
		if response.Automation != nil || !validRepositoryReviewPurgeEligibility(response.Eligibility) ||
			!reviewPurgeEligibilityMatchesRequest(response.Eligibility, request) ||
			response.Eligibility.CanRemove || len(response.Eligibility.Blockers) == 0 {
			return invalid()
		}
	case reviewPurgeOutcomeHistoryAbsent:
		if request.Mode != repositoryReviewPurgeReset || response.Automation != nil ||
			!validRepositoryReviewPurgeEligibility(response.Eligibility) ||
			!reviewPurgeEligibilityMatchesRequest(response.Eligibility, request) ||
			response.Eligibility.HistoryFound || response.Eligibility.CanPurge ||
			!response.Eligibility.CanRemove || len(response.Eligibility.Blockers) != 0 {
			return invalid()
		}
	case reviewPurgeOutcomeInProgress:
		if response.Automation != nil ||
			!reflect.DeepEqual(response.Eligibility, RepositoryReviewPurgeEligibility{}) {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

func reviewPurgeEligibilityMatchesRequest(
	eligibility RepositoryReviewPurgeEligibility,
	request reviewPurgeAutomationRequest,
) bool {
	return eligibility.Summary.RepositoryVersion == request.ExpectedRepositoryVersion &&
		eligibility.Summary.LedgerFence == request.ExpectedLedgerFence
}

func reviewPurgeSnapshotStateMatchesAutomation(
	automation RepositoryReviewAutomation,
	state RepositoryState,
) bool {
	for _, identity := range RepositoryLedgerIdentities(automation.Repository) {
		if state.Repository == identity {
			return true
		}
	}
	runIDs := make(map[string]struct{}, len(automation.RunIDs))
	for _, id := range automation.RunIDs {
		if id = strings.TrimSpace(id); id != "" {
			runIDs[id] = struct{}{}
		}
	}
	for _, run := range state.Runs {
		if _, found := runIDs[run.ID]; found {
			return true
		}
	}
	return false
}

func validReviewBrokerAutomation(automation RepositoryReviewAutomation, id string) bool {
	if automation.ID != id || !validAutomationID(automation.ID) || automation.Version < 1 ||
		automation.SchemaVersion != RepositoryReviewAutomationSchemaVersion ||
		!validAutomationRepository(automation.Repository) || automation.CreatedAt.IsZero() ||
		automation.UpdatedAt.IsZero() {
		return false
	}
	// These two provider-private fields are intentionally omitted from the wire
	// projection. Materialize their canonical values solely so the complete
	// domain validator can check every transmitted field without normalizing
	// attacker-controlled data.
	candidate := cloneAutomation(automation)
	candidate.DeduplicationSettingsSpecified = true
	candidate.EstimatedOutputTokens = defaultAutomationEstimatedOutputTokens
	if validateAutomation(candidate) != nil {
		return false
	}
	encoded, err := json.Marshal(automation)
	return err == nil && validateEncodedAutomationSize(encoded) == nil
}

func validRepositoryReviewPurgeEligibility(eligibility RepositoryReviewPurgeEligibility) bool {
	if eligibility.Summary.RepositoryVersion < 0 || eligibility.Summary.RawFindings < 0 ||
		eligibility.Summary.DeduplicatedFindings < 0 || eligibility.Summary.RepositoryFindings < 0 ||
		eligibility.Summary.IssuePreviews < 0 || eligibility.Summary.ExternalIssueAssociations < 0 ||
		!validRepositoryReviewPurgeLedgerFence(eligibility.Summary.LedgerFence) ||
		eligibility.HistoryFound != (eligibility.Summary.RepositoryVersion > 0) ||
		eligibility.CanRemove != (len(eligibility.Blockers) == 0) ||
		eligibility.CanPurge != (eligibility.HistoryFound && eligibility.CanRemove) ||
		len(eligibility.Blockers) > 6 {
		return false
	}
	order := map[RepositoryReviewPurgeBlockerCode]int{
		RepositoryReviewPurgeBlockerReviewActive:                  1,
		RepositoryReviewPurgeBlockerFindingProcessingActive:       2,
		RepositoryReviewPurgeBlockerResolutionCheckActive:         3,
		RepositoryReviewPurgeBlockerIssueGenerationActive:         4,
		RepositoryReviewPurgeBlockerPublicationActive:             5,
		RepositoryReviewPurgeBlockerHistoricalConsolidationActive: 6,
	}
	previous := 0
	for _, blocker := range eligibility.Blockers {
		position := order[blocker.Code]
		if position <= previous || blocker.Count < 1 ||
			blocker.Message != repositoryReviewPurgeBlockerMessage(blocker.Code) {
			return false
		}
		previous = position
	}
	return true
}

func validRepositoryReviewPurgeLedgerFence(value string) bool {
	suffix, found := strings.CutPrefix(value, "rplf_")
	return found && len(suffix) == 64 && validHexDigest(suffix)
}

func mapReviewPurgeClientError(err error) error {
	switch database.CodeOf(err) {
	case "":
		return nil
	case database.CodeConflict:
		return ErrConflict
	case database.CodeNotFound:
		return os.ErrNotExist
	case database.CodeInvalid:
		return ErrInvalidAutomation
	default:
		return err
	}
}
