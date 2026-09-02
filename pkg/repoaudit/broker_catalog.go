package repoaudit

import (
	"context"
	"os"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	reviewOperationGetByID           = "get-by-id"
	reviewOperationListStates        = "list-states"
	reviewOperationListSummaries     = "list-summaries"
	reviewOperationListProfiles      = "list-profiles"
	reviewOperationGetProfile        = "get-profile"
	reviewOperationCreateProfile     = "create-profile"
	reviewOperationUpdateProfile     = "update-profile"
	reviewOperationProfileAssigned   = "profile-assigned"
	reviewOperationDeleteProfile     = "delete-profile"
	reviewOperationListAutomations   = "list-automations"
	reviewOperationGetAutomation     = "get-automation"
	reviewOperationCreateAutomation  = "create-automation"
	reviewOperationUpdateAutomation  = "update-automation"
	reviewOperationDeleteAutomation  = "delete-automation"
	reviewOperationRewriteState      = "rewrite-state"
	reviewOperationRewriteProfile    = "rewrite-profile"
	reviewOperationRewriteAutomation = "rewrite-automation"
	reviewStatePageSize              = 1
	reviewSummaryPageSize            = 128
	reviewProfilePageSize            = 32
	reviewAutomationPageSize         = 8
)

type reviewIDRequest struct {
	StoreID database.StoreID `json:"store_id"`
	ID      string           `json:"id"`
}

type reviewVersionRequest struct {
	StoreID         database.StoreID `json:"store_id"`
	ID              string           `json:"id"`
	ExpectedVersion int64            `json:"expected_version"`
}

type reviewPageRequest struct {
	StoreID database.StoreID `json:"store_id"`
	Offset  int              `json:"offset"`
	Limit   int              `json:"limit"`
}

type reviewProfileRequest struct {
	StoreID database.StoreID        `json:"store_id"`
	Profile RepositoryReviewProfile `json:"profile"`
}

type reviewProfileUpdateRequest struct {
	StoreID         database.StoreID        `json:"store_id"`
	ID              string                  `json:"id"`
	ExpectedVersion int64                   `json:"expected_version"`
	Candidate       RepositoryReviewProfile `json:"candidate"`
}

type reviewAutomationRequest struct {
	StoreID    database.StoreID           `json:"store_id"`
	Automation RepositoryReviewAutomation `json:"automation"`
}

type reviewAutomationUpdateRequest struct {
	StoreID         database.StoreID           `json:"store_id"`
	ID              string                     `json:"id"`
	ExpectedVersion int64                      `json:"expected_version"`
	Candidate       RepositoryReviewAutomation `json:"candidate"`
}

type reviewGetStateResponse struct {
	Found bool            `json:"found"`
	State RepositoryState `json:"state"`
}

type reviewStatesResponse struct {
	Items []RepositoryState `json:"items"`
	Done  bool              `json:"done"`
}

type reviewSummariesResponse struct {
	Items []RepositorySummary `json:"items"`
	Done  bool                `json:"done"`
}

type reviewGetProfileResponse struct {
	Found   bool                    `json:"found"`
	Profile RepositoryReviewProfile `json:"profile"`
}

type reviewProfilesResponse struct {
	Items []RepositoryReviewProfile `json:"items"`
	Done  bool                      `json:"done"`
}

type reviewProfileResponse struct {
	Profile RepositoryReviewProfile `json:"profile"`
}

type reviewBoolResponse struct {
	Value bool `json:"value"`
}

type reviewGetAutomationResponse struct {
	Found      bool                       `json:"found"`
	Automation RepositoryReviewAutomation `json:"automation"`
}

type reviewAutomationsResponse struct {
	Items []RepositoryReviewAutomation `json:"items"`
	Done  bool                         `json:"done"`
}

type reviewAutomationResponse struct {
	Automation RepositoryReviewAutomation `json:"automation"`
}

type reviewRewriteStateRequest struct {
	StoreID database.StoreID `json:"store_id"`
	State   RepositoryState  `json:"state"`
}

func (s Store) brokerGetByID(id string) (RepositoryState, bool, error) {
	var response reviewGetStateResponse
	err := s.broker.Call(
		context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationGetByID,
		reviewIDRequest{StoreID: s.StoreID(), ID: id}, &response,
	)
	return response.State, response.Found, mapReviewClientError(err)
}

func (s Store) brokerListStates() ([]RepositoryState, error) {
	items := make([]RepositoryState, 0)
	for offset := 0; ; offset += reviewStatePageSize {
		var response reviewStatesResponse
		err := s.broker.Call(
			context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationListStates,
			reviewPageRequest{StoreID: s.StoreID(), Offset: offset, Limit: reviewStatePageSize}, &response,
		)
		if err != nil {
			return nil, mapReviewClientError(err)
		}
		if len(response.Items) > reviewStatePageSize || len(items)+len(response.Items) > 10_000 {
			return nil, database.NewError(database.CodeIntegrity, "repository review list response is invalid")
		}
		items = append(items, response.Items...)
		if response.Done {
			return items, nil
		}
		if len(response.Items) != reviewStatePageSize {
			return nil, database.NewError(database.CodeIntegrity, "repository review list response is invalid")
		}
	}
}

func (s Store) brokerListSummaries() ([]RepositorySummary, error) {
	items := make([]RepositorySummary, 0)
	for offset := 0; ; offset += reviewSummaryPageSize {
		var response reviewSummariesResponse
		err := s.broker.Call(
			context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationListSummaries,
			reviewPageRequest{StoreID: s.StoreID(), Offset: offset, Limit: reviewSummaryPageSize}, &response,
		)
		if err != nil {
			return nil, mapReviewClientError(err)
		}
		items = append(items, response.Items...)
		if response.Done {
			return items, nil
		}
	}
}

func (s Store) brokerListProfiles(ctx context.Context) ([]RepositoryReviewProfile, error) {
	items := make([]RepositoryReviewProfile, 0)
	for offset := 0; ; offset += reviewProfilePageSize {
		var response reviewProfilesResponse
		err := s.broker.Call(
			ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationListProfiles,
			reviewPageRequest{StoreID: s.StoreID(), Offset: offset, Limit: reviewProfilePageSize}, &response,
		)
		if err != nil {
			return nil, mapReviewProfileClientError(err)
		}
		items = append(items, response.Items...)
		if response.Done {
			return items, nil
		}
	}
}

func (s Store) brokerGetProfile(ctx context.Context, id string) (RepositoryReviewProfile, bool, error) {
	var response reviewGetProfileResponse
	err := s.broker.Call(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationGetProfile,
		reviewIDRequest{StoreID: s.StoreID(), ID: id}, &response,
	)
	return response.Profile, response.Found, mapReviewProfileClientError(err)
}

func (s Store) brokerCreateProfile(
	ctx context.Context,
	profile RepositoryReviewProfile,
) (RepositoryReviewProfile, error) {
	var response reviewProfileResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationCreateProfile,
		reviewProfileRequest{StoreID: s.StoreID(), Profile: profile}, &response,
		database.CallOptions{Mutation: true},
	)
	return response.Profile, mapReviewProfileClientError(err)
}

func (s Store) brokerUpdateProfile(
	ctx context.Context,
	id string,
	expectedVersion int64,
	mutate func(*RepositoryReviewProfile) error,
) (RepositoryReviewProfile, error) {
	if mutate == nil {
		return RepositoryReviewProfile{}, ErrInvalidProfile
	}
	current, found, err := s.brokerGetProfile(ctx, id)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	if !found {
		return RepositoryReviewProfile{}, os.ErrNotExist
	}
	candidate := cloneProfile(current)
	if mutateErr := mutate(&candidate); mutateErr != nil {
		return RepositoryReviewProfile{}, mutateErr
	}
	var response reviewProfileResponse
	err = s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationUpdateProfile,
		reviewProfileUpdateRequest{
			StoreID: s.StoreID(), ID: id, ExpectedVersion: expectedVersion, Candidate: candidate,
		},
		&response, database.CallOptions{Mutation: true},
	)
	return response.Profile, mapReviewProfileClientError(err)
}

func (s Store) brokerProfileAssigned(ctx context.Context, id string) (bool, error) {
	var response reviewBoolResponse
	err := s.broker.Call(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationProfileAssigned,
		reviewIDRequest{StoreID: s.StoreID(), ID: id}, &response,
	)
	return response.Value, mapReviewProfileClientError(err)
}

func (s Store) brokerDeleteProfile(ctx context.Context, id string, expectedVersion int64) error {
	var response reviewMutationResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationDeleteProfile,
		reviewVersionRequest{StoreID: s.StoreID(), ID: id, ExpectedVersion: expectedVersion}, &response,
		database.CallOptions{Mutation: true},
	)
	return mapReviewProfileClientError(err)
}

func (s Store) brokerListAutomations(ctx context.Context) ([]RepositoryReviewAutomation, error) {
	items := make([]RepositoryReviewAutomation, 0)
	for offset := 0; ; offset += reviewAutomationPageSize {
		var response reviewAutomationsResponse
		err := s.broker.Call(
			ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationListAutomations,
			reviewPageRequest{StoreID: s.StoreID(), Offset: offset, Limit: reviewAutomationPageSize}, &response,
		)
		if err != nil {
			return nil, mapReviewAutomationClientError(err)
		}
		items = append(items, response.Items...)
		if response.Done {
			return items, nil
		}
	}
}

func (s Store) brokerGetAutomation(ctx context.Context, id string) (RepositoryReviewAutomation, bool, error) {
	var response reviewGetAutomationResponse
	err := s.broker.Call(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationGetAutomation,
		reviewIDRequest{StoreID: s.StoreID(), ID: id}, &response,
	)
	return response.Automation, response.Found, mapReviewAutomationClientError(err)
}

func (s Store) brokerCreateAutomation(
	ctx context.Context,
	automation RepositoryReviewAutomation,
) (RepositoryReviewAutomation, error) {
	var response reviewAutomationResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationCreateAutomation,
		reviewAutomationRequest{StoreID: s.StoreID(), Automation: automation}, &response,
		database.CallOptions{Mutation: true},
	)
	return response.Automation, mapReviewAutomationClientError(err)
}

func (s Store) brokerUpdateAutomation(
	ctx context.Context,
	id string,
	expectedVersion int64,
	mutate func(*RepositoryReviewAutomation) error,
) (RepositoryReviewAutomation, error) {
	if mutate == nil {
		return RepositoryReviewAutomation{}, ErrInvalidAutomation
	}
	current, found, err := s.brokerGetAutomation(ctx, id)
	if err != nil {
		return RepositoryReviewAutomation{}, err
	}
	if !found {
		return RepositoryReviewAutomation{}, os.ErrNotExist
	}
	candidate := cloneAutomation(current)
	if mutateErr := mutate(&candidate); mutateErr != nil {
		return RepositoryReviewAutomation{}, mutateErr
	}
	var response reviewAutomationResponse
	err = s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationUpdateAutomation,
		reviewAutomationUpdateRequest{
			StoreID: s.StoreID(), ID: id, ExpectedVersion: expectedVersion, Candidate: candidate,
		},
		&response, database.CallOptions{Mutation: true},
	)
	return response.Automation, mapReviewAutomationClientError(err)
}

func (s Store) brokerDeleteAutomation(ctx context.Context, id string, expectedVersion int64) error {
	var response reviewMutationResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationDeleteAutomation,
		reviewVersionRequest{StoreID: s.StoreID(), ID: id, ExpectedVersion: expectedVersion}, &response,
		database.CallOptions{Mutation: true},
	)
	return mapReviewAutomationClientError(err)
}

func (s Store) brokerRewriteState(ctx context.Context, state RepositoryState) (RepositoryState, error) {
	var response reviewStateResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationRewriteState,
		reviewRewriteStateRequest{StoreID: s.StoreID(), State: state}, &response,
		database.CallOptions{Mutation: true},
	)
	return response.State, mapReviewClientError(err)
}

func (s Store) brokerRewriteProfile(
	ctx context.Context,
	profile RepositoryReviewProfile,
) (RepositoryReviewProfile, error) {
	var response reviewProfileResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationRewriteProfile,
		reviewProfileRequest{StoreID: s.StoreID(), Profile: profile}, &response,
		database.CallOptions{Mutation: true},
	)
	return response.Profile, mapReviewProfileClientError(err)
}

func (s Store) brokerRewriteAutomation(
	ctx context.Context,
	automation RepositoryReviewAutomation,
) (RepositoryReviewAutomation, error) {
	var response reviewAutomationResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationRewriteAutomation,
		reviewAutomationRequest{StoreID: s.StoreID(), Automation: automation}, &response,
		database.CallOptions{Mutation: true},
	)
	return response.Automation, mapReviewAutomationClientError(err)
}

func (handler *reviewStoreHandler) handleExtended(ctx context.Context, request database.Request) (any, error) {
	switch request.Operation {
	case reviewOperationGetByID:
		var input reviewIDRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		store, err := handler.open()
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		state, found, err := store.GetByID(input.ID)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewGetStateResponse{Found: found, State: state}, nil
	case reviewOperationListStates, reviewOperationListSummaries,
		reviewOperationListProfiles, reviewOperationListAutomations:
		return handler.handleList(ctx, request)
	case reviewOperationGetProfile, reviewOperationProfileAssigned,
		reviewOperationGetAutomation:
		return handler.handleGetCatalog(ctx, request)
	case reviewOperationCreateProfile, reviewOperationUpdateProfile, reviewOperationDeleteProfile:
		return handler.handleProfileMutation(ctx, request)
	case reviewOperationCreateAutomation, reviewOperationUpdateAutomation, reviewOperationDeleteAutomation:
		return handler.handleAutomationMutation(ctx, request)
	case reviewOperationRewriteState, reviewOperationRewriteProfile, reviewOperationRewriteAutomation:
		return handler.handleRewrite(ctx, request)
	default:
		return nil, database.NewError(database.CodeUnsupported, "repository review operation is unsupported")
	}
}

func (handler *reviewStoreHandler) handleRewrite(ctx context.Context, request database.Request) (any, error) {
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	switch request.Operation {
	case reviewOperationRewriteState:
		var input reviewRewriteStateRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		value, err := store.RewriteStateForMigration(ctx, input.State)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewStateResponse{State: value}, nil
	case reviewOperationRewriteProfile:
		var input reviewProfileRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		value, err := store.RewriteProfileForMigration(ctx, input.Profile)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewProfileResponse{Profile: value}, nil
	default:
		var input reviewAutomationRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		value, err := store.RewriteAutomationForMigration(ctx, input.Automation)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewAutomationResponse{Automation: value}, nil
	}
}

func (handler *reviewStoreHandler) handleList(ctx context.Context, request database.Request) (any, error) {
	var input reviewPageRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID || input.Offset < 0 || input.Limit < 1 {
		return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
	}
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	slice := func(length int) (int, int) {
		if input.Offset >= length {
			return length, length
		}
		end := input.Offset + input.Limit
		if end > length {
			end = length
		}
		return input.Offset, end
	}
	switch request.Operation {
	case reviewOperationListStates:
		if input.Limit > reviewStatePageSize {
			return nil, database.NewError(database.CodeInvalid, "repository review page is invalid")
		}
		items, err := store.List()
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		start, end := slice(len(items))
		return reviewStatesResponse{Items: items[start:end], Done: end == len(items)}, nil
	case reviewOperationListSummaries:
		if input.Limit > reviewSummaryPageSize {
			return nil, database.NewError(database.CodeInvalid, "repository review page is invalid")
		}
		items, err := store.ListSummaries()
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		start, end := slice(len(items))
		return reviewSummariesResponse{Items: items[start:end], Done: end == len(items)}, nil
	case reviewOperationListProfiles:
		if input.Limit > reviewProfilePageSize {
			return nil, database.NewError(database.CodeInvalid, "repository review page is invalid")
		}
		items, err := store.ListProfiles(ctx)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		start, end := slice(len(items))
		return reviewProfilesResponse{Items: items[start:end], Done: end == len(items)}, nil
	default:
		if input.Limit > reviewAutomationPageSize {
			return nil, database.NewError(database.CodeInvalid, "repository review page is invalid")
		}
		items, err := store.ListAutomations(ctx)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		start, end := slice(len(items))
		return reviewAutomationsResponse{Items: items[start:end], Done: end == len(items)}, nil
	}
}

func (handler *reviewStoreHandler) handleGetCatalog(ctx context.Context, request database.Request) (any, error) {
	var input reviewIDRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
		return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
	}
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	switch request.Operation {
	case reviewOperationGetProfile:
		value, found, err := store.GetProfile(ctx, input.ID)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewGetProfileResponse{Found: found, Profile: value}, nil
	case reviewOperationProfileAssigned:
		value, err := store.IsProfileAssigned(ctx, input.ID)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewBoolResponse{Value: value}, nil
	default:
		value, found, err := store.GetAutomation(ctx, input.ID)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewGetAutomationResponse{Found: found, Automation: value}, nil
	}
}

//nolint:dupl // Profile and automation commands intentionally keep their typed wire contracts separate.
func (handler *reviewStoreHandler) handleProfileMutation(ctx context.Context, request database.Request) (any, error) {
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	switch request.Operation {
	case reviewOperationCreateProfile:
		var input reviewProfileRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		value, err := store.CreateProfile(ctx, input.Profile)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewProfileResponse{Profile: value}, nil
	case reviewOperationUpdateProfile:
		var input reviewProfileUpdateRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		value, err := store.UpdateProfile(
			ctx,
			input.ID,
			input.ExpectedVersion,
			func(value *RepositoryReviewProfile) error { *value = cloneProfile(input.Candidate); return nil },
		)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewProfileResponse{Profile: value}, nil
	default:
		var input reviewVersionRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		if err := store.DeleteProfile(ctx, input.ID, input.ExpectedVersion); err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewMutationResponse{Updated: true}, nil
	}
}

//nolint:dupl // Profile and automation commands intentionally keep their typed wire contracts separate.
func (handler *reviewStoreHandler) handleAutomationMutation(
	ctx context.Context,
	request database.Request,
) (any, error) {
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	switch request.Operation {
	case reviewOperationCreateAutomation:
		var input reviewAutomationRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		value, err := store.CreateAutomation(ctx, input.Automation)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewAutomationResponse{Automation: value}, nil
	case reviewOperationUpdateAutomation:
		var input reviewAutomationUpdateRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		value, err := store.UpdateAutomation(
			ctx,
			input.ID,
			input.ExpectedVersion,
			func(value *RepositoryReviewAutomation) error { *value = cloneAutomation(input.Candidate); return nil },
		)
		if err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewAutomationResponse{Automation: value}, nil
	default:
		var input reviewVersionRequest
		if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
			return nil, database.NewError(database.CodeInvalid, "repository review request is invalid")
		}
		if err := store.DeleteAutomation(ctx, input.ID, input.ExpectedVersion); err != nil {
			return nil, mapReviewBrokerError(err)
		}
		return reviewMutationResponse{Updated: true}, nil
	}
}

func mapReviewProfileClientError(err error) error {
	switch database.CodeOf(err) {
	case database.CodeConflict:
		return ErrConflict
	case database.CodeAlreadyExists:
		return ErrProfileAssigned
	case database.CodeUnsupported:
		return ErrProfileActive
	case database.CodeNotFound:
		return os.ErrNotExist
	case database.CodeInvalid:
		return ErrInvalidProfile
	default:
		return err
	}
}

func mapReviewAutomationClientError(err error) error {
	switch database.CodeOf(err) {
	case database.CodeConflict:
		return ErrConflict
	case database.CodeNotFound:
		return os.ErrNotExist
	case database.CodeUnsupported:
		return ErrAutomationActive
	case database.CodeInvalid:
		return ErrInvalidAutomation
	default:
		return err
	}
}
