package repoaudit

import (
	"context"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	reviewOperationAcquireNamedLease = "acquire-named-lease"
	reviewLeaseAutomationController  = "automation-controller"
	reviewLeaseIssueAttempt          = "issue-attempt"
	reviewLeaseIssueSlot             = "issue-slot"
	reviewLeaseDeduplicationSlot     = "deduplication-slot"
	reviewLeaseValidationSlot        = "validation-slot"
)

type reviewNamedLeaseRequest struct {
	StoreID      database.StoreID `json:"store_id"`
	Kind         string           `json:"kind"`
	Repository   string           `json:"repository,omitempty"`
	DraftID      string           `json:"draft_id,omitempty"`
	GenerationID string           `json:"generation_id,omitempty"`
	Maximum      int              `json:"maximum,omitempty"`
}

type reviewNamedLeaseResponse struct {
	LeaseID        string `json:"lease_id,omitempty"`
	Acquired       bool   `json:"acquired"`
	TTLNanoSeconds int64  `json:"ttl_nanoseconds,omitempty"`
}

func (s Store) brokerAcquireNamedLease(
	ctx context.Context,
	kind string,
	input reviewNamedLeaseRequest,
) (func(), error) {
	if s.brokerErr != nil {
		return nil, s.brokerErr
	}
	if err := s.consumeBrokerLeaseError(); err != nil {
		return nil, err
	}
	input.StoreID, input.Kind = s.StoreID(), kind
	var response reviewNamedLeaseResponse
	err := s.broker.CallWithOptions(
		ctx, reviewBrokerDomain, reviewBrokerVersion, reviewOperationAcquireNamedLease,
		input, &response, database.CallOptions{Mutation: true},
	)
	if err != nil {
		if database.CodeOf(err) == database.CodeConflict && kind == reviewLeaseAutomationController {
			return nil, ErrAutomationControllerLocked
		}
		return nil, mapReviewClientError(err)
	}
	if !response.Acquired || response.LeaseID == "" || response.TTLNanoSeconds <= 0 {
		return nil, database.NewError(database.CodeConflict, "repository review lease was not acquired")
	}
	return s.newBrokerLeaseRelease(
		response.LeaseID, time.Duration(response.TTLNanoSeconds), nil,
	), nil
}

func (s Store) brokerTryIssueAttempt(
	repository, draftID, generationID string,
) (func(), bool, error) {
	if s.brokerErr != nil {
		return nil, false, s.brokerErr
	}
	if err := s.consumeBrokerLeaseError(); err != nil {
		return nil, false, err
	}
	var response reviewNamedLeaseResponse
	err := s.broker.CallWithOptions(
		context.Background(), reviewBrokerDomain, reviewBrokerVersion, reviewOperationAcquireNamedLease,
		reviewNamedLeaseRequest{
			StoreID: s.StoreID(), Kind: reviewLeaseIssueAttempt,
			Repository: repository, DraftID: draftID, GenerationID: generationID,
		},
		&response, database.CallOptions{Mutation: true},
	)
	if err != nil {
		return nil, false, mapReviewClientError(err)
	}
	if !response.Acquired {
		return nil, false, nil
	}
	if response.LeaseID == "" || response.TTLNanoSeconds <= 0 {
		return nil, false, database.NewError(database.CodeIntegrity, "repository review lease response is invalid")
	}
	return s.newBrokerLeaseRelease(
		response.LeaseID, time.Duration(response.TTLNanoSeconds), nil,
	), true, nil
}

func (handler *reviewStoreHandler) acquireNamedLease(
	ctx context.Context,
	request database.Request,
) (any, error) {
	var input reviewNamedLeaseRequest
	if request.DecodePayload(&input) != nil || input.StoreID != handler.storeID {
		return nil, database.NewError(database.CodeInvalid, "repository review lease request is invalid")
	}
	store, err := handler.open()
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	var release func()
	acquired := true
	switch input.Kind {
	case reviewLeaseAutomationController:
		release, err = store.LockAutomationController()
	case reviewLeaseIssueAttempt:
		release, acquired, err = store.TryLockIssueGenerationAttempt(
			input.Repository, input.DraftID, input.GenerationID,
		)
	case reviewLeaseIssueSlot:
		release, err = store.AcquireIssueGenerationSlot(ctx, input.Maximum)
	case reviewLeaseDeduplicationSlot:
		release, err = store.AcquireDeduplicationSlot(ctx)
	case reviewLeaseValidationSlot:
		release, err = store.AcquireValidationSlot(ctx)
	default:
		return nil, database.NewError(database.CodeInvalid, "repository review lease kind is invalid")
	}
	if err != nil {
		return nil, mapReviewBrokerError(err)
	}
	if !acquired {
		return reviewNamedLeaseResponse{Acquired: false}, nil
	}
	if release == nil {
		return nil, database.NewError(database.CodeInternal, "repository review lease is unavailable")
	}
	id, err := newReviewLeaseID()
	if err != nil {
		release()
		return nil, database.NewError(database.CodeInternal, "repository review lease identity failed")
	}
	lease := &reviewBrokerLease{key: "named:" + input.Kind, release: release}
	if err := handler.registerLease(id, lease); err != nil {
		lease.releaseNow()
		return nil, err
	}
	return reviewNamedLeaseResponse{
		LeaseID: id, Acquired: true, TTLNanoSeconds: int64(handler.effectiveLeaseTTL()),
	}, nil
}
