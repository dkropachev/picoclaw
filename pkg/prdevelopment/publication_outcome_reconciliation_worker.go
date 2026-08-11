package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	defaultPublicationOutcomeReconciliationBatchLimit = 1
	maximumPublicationOutcomeReconciliationBatchLimit = 64
	publicationOutcomeReconciliationRetryCapacity     = 256
)

// publicationOutcomeReconciliationStore is deliberately narrower than the
// production event store. In particular, it grants no publication claim,
// push-journal, workflow, provider-write, or Git authority.
type publicationOutcomeReconciliationStore interface {
	GetPRDevelopmentPublication(
		ctx context.Context,
		publicationID string,
	) (eventing.PRDevelopmentPublication, error)
	ExpirePRDevelopmentPublicationPushes(
		ctx context.Context,
		limit int,
	) ([]eventing.PRDevelopmentPublication, error)
	ListPRDevelopmentPublicationUnknownOutcomes(
		ctx context.Context,
		filter eventing.PRDevelopmentPublicationUnknownOutcomeFilter,
	) (eventing.PRDevelopmentPublicationUnknownOutcomePage, error)
	ReconcilePRDevelopmentPublicationOutcome(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationOutcomeReconciliation,
	) (publication eventing.PRDevelopmentPublication, newlyReconciled bool, err error)
	GetPRDevelopmentCase(
		ctx context.Context,
		id string,
	) (eventing.PRDevelopmentCase, error)
	GetPRDevelopmentThreadForCase(
		ctx context.Context,
		caseID string,
	) (eventing.PRDevelopmentThread, error)
}

var _ publicationOutcomeReconciliationStore = (*eventing.Store)(nil)

// PublicationOutcomeReconciliationWorkerConfig contains only trusted,
// process-local read/reconciliation dependencies. Every field is private to
// JSON projections even when a caller embeds this configuration elsewhere.
type PublicationOutcomeReconciliationWorkerConfig struct {
	Store      publicationOutcomeReconciliationStore `json:"-"`
	Observer   PublicationRemoteHeadObserver         `json:"-"`
	BatchLimit int                                   `json:"-"`
	Now        func() time.Time                      `json:"-"`
}

type publicationOutcomeRetry struct {
	attempt int
	dueAt   time.Time
	touched uint64
}

// PublicationOutcomeReconciliationWorker performs at most one head-only
// observation per ProcessOne call. Its cursor and retry state are only bounded
// process-local read optimizations; restart safely begins another durable scan.
type PublicationOutcomeReconciliationWorker struct {
	store      publicationOutcomeReconciliationStore
	observer   PublicationRemoteHeadObserver
	batchLimit int
	now        func() time.Time

	mu            sync.Mutex
	cursor        *eventing.PRDevelopmentPublicationUnknownOutcomeCursor
	retries       map[string]publicationOutcomeRetry
	retrySequence uint64
}

// NewPublicationOutcomeReconciliationWorker constructs the least-authority
// outcome worker. A non-positive batch uses one; oversized batches are capped
// at the same bound as the durable store scan.
func NewPublicationOutcomeReconciliationWorker(
	config PublicationOutcomeReconciliationWorkerConfig,
) (*PublicationOutcomeReconciliationWorker, error) {
	if config.Store == nil || isNilServiceValue(config.Store) {
		return nil, fmt.Errorf(
			"%w: publication outcome reconciliation store is required",
			ErrUnavailable,
		)
	}
	if config.Observer == nil || isNilServiceValue(config.Observer) {
		return nil, fmt.Errorf(
			"%w: publication remote-head observer is required",
			ErrUnavailable,
		)
	}
	limit := config.BatchLimit
	if limit <= 0 {
		limit = defaultPublicationOutcomeReconciliationBatchLimit
	}
	if limit > maximumPublicationOutcomeReconciliationBatchLimit {
		limit = maximumPublicationOutcomeReconciliationBatchLimit
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &PublicationOutcomeReconciliationWorker{
		store:      config.Store,
		observer:   config.Observer,
		batchLimit: limit,
		now:        now,
		retries:    make(map[string]publicationOutcomeRetry),
	}, nil
}

// ProcessOne expires abandoned started effects before scanning durable unknown
// outcomes, then performs at most one independently observed reconciliation.
// It never invokes or schedules the original Git effect.
func (worker *PublicationOutcomeReconciliationWorker) ProcessOne(
	ctx context.Context,
) (bool, error) {
	if worker == nil || worker.store == nil || isNilServiceValue(worker.store) ||
		worker.observer == nil || isNilServiceValue(worker.observer) ||
		worker.batchLimit < 1 ||
		worker.batchLimit > maximumPublicationOutcomeReconciliationBatchLimit ||
		worker.now == nil {
		return false, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}

	worker.mu.Lock()
	defer worker.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := worker.now().UTC()
	if now.IsZero() {
		return false, fmt.Errorf(
			"%w: publication outcome reconciliation clock is invalid",
			ErrUnavailable,
		)
	}

	expired, err := worker.store.ExpirePRDevelopmentPublicationPushes(
		ctx,
		worker.batchLimit,
	)
	if err != nil {
		return false, fmt.Errorf("expire publication pushes before reconciliation: %w", err)
	}
	if len(expired) > worker.batchLimit {
		return true, errors.New("publication push expiry exceeded its requested bound")
	}
	for _, publication := range expired {
		if publication.Status != eventing.PRDevelopmentPublicationOutcomeUnknown {
			return true, errors.New("publication push expiry returned a non-unknown outcome")
		}
	}
	// A full page may mean more expired effects remain. Drain them before any
	// provider observation so expiry ordering is deterministic and bounded.
	if len(expired) == worker.batchLimit {
		return true, nil
	}

	publication, found, err := worker.nextDuePublication(ctx, now)
	if err != nil {
		return len(expired) != 0, err
	}
	if !found {
		return len(expired) != 0, nil
	}
	if err = worker.reconcilePublication(ctx, now, publication); err != nil {
		return true, err
	}
	return true, nil
}

func (worker *PublicationOutcomeReconciliationWorker) nextDuePublication(
	ctx context.Context,
	now time.Time,
) (eventing.PRDevelopmentPublication, bool, error) {
	// At most one wrap is needed when the prior cursor points beyond the last
	// still-unknown row (for example after another worker reconciled it).
	for pass := 0; pass < 2; pass++ {
		page, err := worker.store.ListPRDevelopmentPublicationUnknownOutcomes(
			ctx,
			eventing.PRDevelopmentPublicationUnknownOutcomeFilter{
				After: clonePublicationUnknownOutcomeCursor(worker.cursor),
				Limit: worker.batchLimit,
			},
		)
		if err != nil {
			return eventing.PRDevelopmentPublication{}, false, fmt.Errorf(
				"scan publication unknown outcomes: %w",
				err,
			)
		}
		if len(page.Publications) > worker.batchLimit ||
			(len(page.Publications) == 0 && page.Next != nil) {
			return eventing.PRDevelopmentPublication{}, false, errors.New(
				"publication unknown-outcome scan exceeded its requested bound",
			)
		}
		if len(page.Publications) == 0 {
			if worker.cursor != nil && pass == 0 {
				worker.cursor = nil
				continue
			}
			worker.cursor = nil
			return eventing.PRDevelopmentPublication{}, false, nil
		}

		for _, publication := range page.Publications {
			worker.cursor = publicationUnknownOutcomeCursor(publication)
			if retry, exists := worker.retries[publication.ID]; exists &&
				retry.dueAt.After(now) {
				continue
			}
			return publication, true, nil
		}

		if page.Next != nil {
			// The next call continues after this backed-off page, so an old
			// prefix cannot starve later durable rows.
			worker.cursor = clonePublicationUnknownOutcomeCursor(page.Next)
			return eventing.PRDevelopmentPublication{}, false, nil
		}
		worker.cursor = nil
		return eventing.PRDevelopmentPublication{}, false, nil
	}
	return eventing.PRDevelopmentPublication{}, false, nil
}

func (worker *PublicationOutcomeReconciliationWorker) reconcilePublication(
	ctx context.Context,
	now time.Time,
	publication eventing.PRDevelopmentPublication,
) error {
	if err := validatePublicationOutcomeUnknown(publication); err != nil {
		worker.deferPublication(publication.ID, now)
		return err
	}
	storedCase, err := worker.store.GetPRDevelopmentCase(ctx, publication.CaseID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		worker.deferPublication(publication.ID, now)
		return fmt.Errorf("load publication outcome case: %w", err)
	}
	thread, err := worker.store.GetPRDevelopmentThreadForCase(ctx, publication.CaseID)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		worker.deferPublication(publication.ID, now)
		return fmt.Errorf("load publication outcome provider thread: %w", err)
	}
	if err = validatePublicationOutcomeSubject(publication, storedCase, thread); err != nil {
		worker.deferPublication(publication.ID, now)
		return err
	}

	observed, err := worker.observer.ObservePublicationRemoteHead(
		ctx,
		storedCase,
		thread.Identity,
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		worker.deferPublication(publication.ID, now)
		return fmt.Errorf("observe publication remote head: %w", err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if !publicationOutcomeObservationProvesDesiredTip(publication, observed) {
		worker.deferPublication(publication.ID, now)
		return nil
	}

	result := publicationOutcomeReconciledResult(publication)
	input := eventing.PRDevelopmentPublicationOutcomeReconciliation{
		PublicationID: publication.ID,
		RequestHash:   publication.PushRequestHash,
		Observation:   observed.Observation,
		ObservedAt:    observed.ObservedAt,
		Result:        result,
	}
	reconciled, _, reconcileErr := worker.store.ReconcilePRDevelopmentPublicationOutcome(
		ctx,
		input,
	)
	if reconcileErr == nil && publicationOutcomeConverged(
		publication,
		observed.Observation,
		result,
		reconciled,
	) {
		worker.clearPublicationRetry(publication.ID)
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	// Exact store replay includes ObservedAt. Independent racing readers can
	// therefore receive a changed-replay conflict even though one already
	// committed the same semantic proof. Reread and accept only that narrow
	// convergence; no arbitrary terminal state is swallowed.
	current, readErr := worker.store.GetPRDevelopmentPublication(ctx, publication.ID)
	if readErr == nil && publicationOutcomeConverged(
		publication,
		observed.Observation,
		result,
		current,
	) {
		worker.clearPublicationRetry(publication.ID)
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	worker.deferPublication(publication.ID, now)
	if reconcileErr != nil {
		if readErr != nil {
			return errors.Join(
				fmt.Errorf("reconcile publication outcome: %w", reconcileErr),
				fmt.Errorf("read publication outcome after reconciliation: %w", readErr),
			)
		}
		return fmt.Errorf("reconcile publication outcome: %w", reconcileErr)
	}
	if readErr != nil {
		return fmt.Errorf("read publication outcome after invalid reconciliation: %w", readErr)
	}
	return errors.New("publication outcome reconciliation returned non-converged state")
}

func validatePublicationOutcomeUnknown(
	publication eventing.PRDevelopmentPublication,
) error {
	if strings.TrimSpace(publication.ID) == "" ||
		strings.TrimSpace(publication.CaseID) == "" ||
		strings.TrimSpace(publication.ThreadID) == "" ||
		publication.Status != eventing.PRDevelopmentPublicationOutcomeUnknown ||
		publication.ClaimFrom != "" || publication.ClaimOwner != "" ||
		publication.ClaimToken != "" || publication.ClaimUntil != nil ||
		publication.LastErrorCode != eventing.PRDevelopmentPublicationErrorOutcomeUnknown ||
		publication.EffectStartedAt == nil || publication.CompletedAt == nil ||
		publication.ProviderObservedAt == nil ||
		strings.TrimSpace(publication.PushRequestHash) == "" ||
		strings.TrimSpace(publication.PushRequest.ExpectedTip) == "" ||
		strings.TrimSpace(publication.PushRequest.ExpectedRemoteTip) == "" ||
		publication.PushRequest.Repository == "" || publication.PushRequest.SourceRef == "" ||
		publication.ReconciliationObservedAt != nil ||
		publication.ReconciliationObservationHash != "" ||
		publication.PushDisposition != "" || publication.PushResultHash != "" {
		return fmt.Errorf(
			"%w: publication is not a complete unknown outcome",
			eventing.ErrInvalidPRDevelopmentPublication,
		)
	}
	return nil
}

func validatePublicationOutcomeSubject(
	publication eventing.PRDevelopmentPublication,
	storedCase eventing.PRDevelopmentCase,
	thread eventing.PRDevelopmentThread,
) error {
	provider := publication.ProviderObservation
	identity := thread.Identity
	if storedCase.ID != publication.CaseID || thread.ID != publication.ThreadID ||
		thread.Kind != eventing.PRDevelopmentThreadProvider ||
		thread.CaseCount != len(thread.Cases) || thread.CaseCount < 1 ||
		strings.TrimSpace(identity.Provider) == "" ||
		strings.TrimSpace(identity.ProviderOrigin) == "" ||
		strings.TrimSpace(identity.PullAuthorID) == "" ||
		strings.TrimSpace(identity.RepositoryID) == "" ||
		strings.TrimSpace(identity.PullRequestID) == "" ||
		identity.PullNumber != storedCase.PullNumber ||
		storedCase.Repository != provider.Repository ||
		storedCase.PullNumber != provider.PullNumber ||
		storedCase.HeadRepository != provider.HeadRepository ||
		storedCase.HeadRef != provider.HeadRef ||
		!publicationThreadContainsCase(thread, publication.CaseID) {
		return fmt.Errorf(
			"%w: publication outcome case or provider thread is inconsistent",
			eventing.ErrInvalidPRDevelopmentPublication,
		)
	}
	return nil
}

func publicationThreadContainsCase(
	thread eventing.PRDevelopmentThread,
	caseID string,
) bool {
	found := false
	for ordinal, link := range thread.Cases {
		if link.Ordinal != ordinal {
			return false
		}
		if link.CaseID == caseID {
			found = true
		}
	}
	return found
}

func publicationOutcomeObservationProvesDesiredTip(
	publication eventing.PRDevelopmentPublication,
	observed TimedPublicationRemoteObservation,
) bool {
	provider := publication.ProviderObservation
	observation := observed.Observation
	return !observed.ObservedAt.IsZero() &&
		observation.Repository == provider.Repository &&
		observation.PullNumber == provider.PullNumber &&
		observation.HeadRepository == provider.HeadRepository &&
		observation.HeadRef == provider.HeadRef &&
		observation.HeadSHA == publication.PushRequest.ExpectedTip
}

func publicationOutcomeReconciledResult(
	publication eventing.PRDevelopmentPublication,
) eventing.PRDevelopmentPublicationPushResult {
	request := publication.PushRequest
	return eventing.PRDevelopmentPublicationPushResult{
		WorkspaceID:       request.WorkspaceID,
		Version:           request.ExpectedVersion,
		MutationEpoch:     request.ExpectedMutationEpoch,
		ParkIntentID:      request.ExpectedParkIntentID,
		BaseCommit:        request.ExpectedBase,
		Tip:               request.ExpectedTip,
		Tree:              request.ExpectedTree,
		RemoteRef:         "refs/heads/" + request.SourceRef,
		ExpectedRemoteTip: request.ExpectedRemoteTip,
		RemoteTip:         request.ExpectedTip,
		Disposition:       eventing.PRDevelopmentPublicationPushReconciled,
		WorkspaceClean:    false,
	}
}

func publicationOutcomeConverged(
	original eventing.PRDevelopmentPublication,
	observation eventing.PRDevelopmentPublicationRemoteObservation,
	expectedResult eventing.PRDevelopmentPublicationPushResult,
	current eventing.PRDevelopmentPublication,
) bool {
	if current.ID != original.ID ||
		current.Status != eventing.PRDevelopmentPublicationPublished ||
		current.PushRequestHash != original.PushRequestHash ||
		current.PushRequest != original.PushRequest ||
		current.ProviderObservationHash != original.ProviderObservationHash ||
		current.PushDisposition != eventing.PRDevelopmentPublicationPushReconciled ||
		current.PushResult != expectedResult ||
		current.PushResultHash == "" ||
		current.ReconciliationObservation != observation ||
		current.ReconciliationObservationHash == "" ||
		current.ReconciliationObservedAt == nil ||
		current.WorkspaceClean || current.LocalDrift ||
		current.LastErrorCode != "" || current.LastErrorDetail != "" {
		return false
	}
	if original.CompletedAt == nil || current.CompletedAt == nil ||
		!current.CompletedAt.Equal(*original.CompletedAt) {
		return false
	}
	return true
}

func publicationUnknownOutcomeCursor(
	publication eventing.PRDevelopmentPublication,
) *eventing.PRDevelopmentPublicationUnknownOutcomeCursor {
	return &eventing.PRDevelopmentPublicationUnknownOutcomeCursor{
		AvailableAt: publication.AvailableAt,
		CreatedAt:   publication.CreatedAt,
		ID:          publication.ID,
	}
}

func clonePublicationUnknownOutcomeCursor(
	cursor *eventing.PRDevelopmentPublicationUnknownOutcomeCursor,
) *eventing.PRDevelopmentPublicationUnknownOutcomeCursor {
	if cursor == nil {
		return nil
	}
	cloned := *cursor
	return &cloned
}

func (worker *PublicationOutcomeReconciliationWorker) deferPublication(
	publicationID string,
	notBefore time.Time,
) {
	publicationID = strings.TrimSpace(publicationID)
	if publicationID == "" {
		return
	}
	now := worker.now().UTC()
	if now.IsZero() || now.Before(notBefore) {
		now = notBefore
	}
	worker.retrySequence++
	retry := worker.retries[publicationID]
	retry.attempt++
	retry.dueAt = now.Add(PublicationRetryDelay(retry.attempt))
	retry.touched = worker.retrySequence
	if _, exists := worker.retries[publicationID]; !exists &&
		len(worker.retries) >= publicationOutcomeReconciliationRetryCapacity {
		worker.evictOldestPublicationRetry()
	}
	worker.retries[publicationID] = retry
}

func (worker *PublicationOutcomeReconciliationWorker) clearPublicationRetry(
	publicationID string,
) {
	delete(worker.retries, publicationID)
}

func (worker *PublicationOutcomeReconciliationWorker) evictOldestPublicationRetry() {
	var (
		oldestID string
		oldest   publicationOutcomeRetry
		found    bool
	)
	for publicationID, retry := range worker.retries {
		if !found || retry.touched < oldest.touched ||
			(retry.touched == oldest.touched && publicationID < oldestID) {
			oldestID = publicationID
			oldest = retry
			found = true
		}
	}
	if found {
		delete(worker.retries, oldestID)
	}
}
