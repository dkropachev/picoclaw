package prdevelopment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	publicationGateSubjectFormat         = "pr-development-publication-gate-subject/v1"
	publicationGateSubjectModeZero       = "zero"
	publicationGateSubjectRevisionDomain = "picoclaw-pr-development-publication-gate-subject-v1"
)

var (
	errPublicationGateCorrupt         = errors.New("publication gate state is inconsistent")
	errPublicationGateLocalEvidence   = errors.New("publication local evidence changed")
	errPublicationGateProviderChanged = errors.New("publication provider evidence changed")
	errPublicationGateSuperseded      = errors.New("publication local candidate was superseded")
	errPublicationGateRetry           = errors.New("publication gate should be retried")
)

// PublicationGateStore is the least-authority storage boundary needed to
// process one already-claimed publication gate. It deliberately excludes
// claiming, renewal, decision-run admission, push, and reconciliation power.
type PublicationGateStore interface {
	eventing.PRDevelopmentPublicationGateClaimAuthenticator
	eventing.PRDevelopmentPublicationGateContextSnapshotReader
	GetPRDevelopmentPublication(
		ctx context.Context,
		publicationID string,
	) (eventing.PRDevelopmentPublication, error)
	PinPRDevelopmentPublicationPolicy(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationPolicyPin,
	) (eventing.PRDevelopmentPublication, bool, error)
	PinPRDevelopmentPublicationSubject(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationSubjectPin,
	) (eventing.PRDevelopmentPublication, bool, error)
	PinPRDevelopmentPublicationProvider(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationProviderPin,
	) (eventing.PRDevelopmentPublication, bool, error)
	MarkPRDevelopmentPublicationPushReady(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationMarkPushReady,
	) (eventing.PRDevelopmentPublication, bool, error)
	CompletePRDevelopmentPublicationPrestart(
		ctx context.Context,
		input eventing.PRDevelopmentPublicationPrestartCompletion,
	) (eventing.PRDevelopmentPublication, bool, error)
}

// PublicationGateProcessorConfig contains only policy, read-only provider,
// and narrow publication-store capabilities. This processor has no workflow
// executor, model, filesystem, Git, provider-write, claim, or push authority.
type PublicationGateProcessorConfig struct {
	Store    PublicationGateStore         `json:"-"`
	Policies sharedattention.PolicySource `json:"-"`
	Provider PublicationProviderObserver  `json:"-"`
}

// PublicationGateProcessDisposition is the bounded result of processing one
// exact initial publication claim.
type PublicationGateProcessDisposition string

const (
	// PublicationGateRequiresExecution means an active mixed-gate policy is
	// durably pinned and must be handled by the later private-run slice.
	PublicationGateRequiresExecution PublicationGateProcessDisposition = "requires_execution"
	// PublicationGatePushReady means an empty/all-zero policy completed its
	// provider preflight and the durable claim was released to push_ready.
	PublicationGatePushReady PublicationGateProcessDisposition = "push_ready"
	// PublicationGateTerminal means a proven non-retryable pre-effect outcome
	// was recorded without crossing the push boundary.
	PublicationGateTerminal PublicationGateProcessDisposition = "terminal"
)

// PublicationGateProcessResult is private process-local state. In particular,
// Publication never carries the scheduling claim token.
type PublicationGateProcessResult struct {
	Disposition PublicationGateProcessDisposition `json:"-"`
	Publication eventing.PRDevelopmentPublication `json:"-"`
}

// PublicationGateProcessor incrementally and replayably prepares one claimed
// publication policy. Only an empty/all-zero composition is completed here;
// active compositions stop after the immutable policy pin.
type PublicationGateProcessor struct {
	store    PublicationGateStore
	policies sharedattention.PolicySource
	provider PublicationProviderObserver
}

// NewPublicationGateProcessor constructs an uncomposed publication gate
// processor with no scheduling or effect runner.
func NewPublicationGateProcessor(
	config PublicationGateProcessorConfig,
) (*PublicationGateProcessor, error) {
	if config.Store == nil || isNilServiceValue(config.Store) {
		return nil, fmt.Errorf("%w: publication gate store is required", ErrUnavailable)
	}
	if config.Policies == nil || isNilServiceValue(config.Policies) {
		return nil, fmt.Errorf("%w: publication gate policy source is required", ErrUnavailable)
	}
	if config.Provider == nil || isNilServiceValue(config.Provider) {
		return nil, fmt.Errorf("%w: publication provider observer is required", ErrUnavailable)
	}
	return &PublicationGateProcessor{
		store: config.Store, policies: config.Policies, provider: config.Provider,
	}, nil
}

// ProcessClaim processes only an exact claimed-from-pending publication. It
// never claims or renews work and never invokes an active gate, model, Git, or
// push effect.
func (processor *PublicationGateProcessor) ProcessClaim(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
) (PublicationGateProcessResult, error) {
	if processor == nil || processor.store == nil || isNilServiceValue(processor.store) ||
		processor.policies == nil || isNilServiceValue(processor.policies) ||
		processor.provider == nil || isNilServiceValue(processor.provider) {
		return PublicationGateProcessResult{}, ErrUnavailable
	}
	ctx = ctxOrBackground(ctx)
	if err := validatePublicationGateClaimAuthority(claim); err != nil {
		return PublicationGateProcessResult{}, err
	}
	claim, repository, err := authenticatePublicationGateClaim(
		ctx,
		processor.store,
		claim,
	)
	if err != nil {
		return processor.finishPublicationGateFailure(ctx, claim, err)
	}

	publication, policy, err := processor.preparePublicationPolicy(ctx, claim, repository)
	if err != nil {
		return processor.finishPublicationGateFailure(ctx, claim, err)
	}
	if !policy.IsNoop() {
		// Active subject/provider pins belong to PublicationGateExecutor. Keep
		// this preparation seam replayable after either later pin without
		// interpreting or recreating those capabilities here.
		return PublicationGateProcessResult{
			Disposition: PublicationGateRequiresExecution,
			Publication: redactPublicationGateClaim(publication),
		}, nil
	}
	if err = verifyPublicationNoopPolicy(policy); err != nil {
		return processor.finishPublicationGateFailure(
			ctx,
			claim,
			fmt.Errorf("%w: %v", errPublicationGateCorrupt, err),
		)
	}

	publication, snapshot, err := processor.pinPublicationZeroSubject(
		ctx,
		claim,
		publication,
		policy,
		repository,
		nil,
	)
	if err != nil {
		return processor.finishPublicationGateFailure(ctx, claim, err)
	}
	publication, err = processor.pinPublicationProvider(
		ctx,
		claim,
		publication,
		repository,
		snapshot,
	)
	if err != nil {
		return processor.finishPublicationGateFailure(ctx, claim, err)
	}
	publication, err = processor.markPublicationPushReady(ctx, claim, publication)
	if err != nil {
		return processor.finishPublicationGateFailure(ctx, claim, err)
	}
	return PublicationGateProcessResult{
		Disposition: PublicationGatePushReady,
		Publication: publication,
	}, nil
}

func authenticatePublicationGateClaim(
	ctx context.Context,
	authenticator eventing.PRDevelopmentPublicationGateClaimAuthenticator,
	claim eventing.PRDevelopmentPublication,
) (eventing.PRDevelopmentPublication, string, error) {
	authentication, err := authenticator.AuthenticateClaimedPRDevelopmentPublicationGate(
		ctx,
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch,
	)
	if err != nil {
		return claim, "", publicationGateStoreFailure(err, errPublicationGateLocalEvidence)
	}
	authoritative := authentication.Publication
	if authoritative.ClaimToken != "" ||
		!validProviderRepositoryIdentity(authentication.Repository) ||
		!samePublicationGateIdentity(claim, authoritative) ||
		authoritative.Status != eventing.PRDevelopmentPublicationClaimed ||
		authoritative.ClaimFrom != eventing.PRDevelopmentPublicationPending ||
		authoritative.ClaimOwner != claim.ClaimOwner ||
		authoritative.ClaimEpoch != claim.ClaimEpoch ||
		authoritative.Claims != claim.Claims ||
		!timesEqual(authoritative.ClaimedAt, claim.ClaimedAt) ||
		authoritative.ClaimUntil == nil || claim.ClaimUntil == nil ||
		authoritative.ClaimUntil.Before(*claim.ClaimUntil) ||
		!publicationGatePinsArePrefix(claim, authoritative) {
		return claim, "", errPublicationGateCorrupt
	}
	authoritative.ClaimToken = claim.ClaimToken
	if err = validatePublicationGateClaim(authoritative); err != nil {
		return claim, "", errPublicationGateCorrupt
	}
	return authoritative, authentication.Repository, nil
}

func publicationGatePinsArePrefix(
	provided eventing.PRDevelopmentPublication,
	authoritative eventing.PRDevelopmentPublication,
) bool {
	providedPolicy := provided.PolicyRevision != "" || len(provided.PinnedPolicy) != 0 ||
		provided.PinnedPolicyHash != ""
	if providedPolicy && (provided.PolicyRevision != authoritative.PolicyRevision ||
		!bytes.Equal(provided.PinnedPolicy, authoritative.PinnedPolicy) ||
		provided.PinnedPolicyHash != authoritative.PinnedPolicyHash) {
		return false
	}
	providedSubject := provided.SubjectRevision != "" || len(provided.PinnedSubject) != 0 ||
		provided.PinnedSubjectHash != ""
	if providedSubject && (provided.SubjectRevision != authoritative.SubjectRevision ||
		!bytes.Equal(provided.PinnedSubject, authoritative.PinnedSubject) ||
		provided.PinnedSubjectHash != authoritative.PinnedSubjectHash) {
		return false
	}
	if hasPublicationProviderPin(provided) &&
		(!reflect.DeepEqual(provided.ProviderObservation, authoritative.ProviderObservation) ||
			provided.ProviderObservationHash != authoritative.ProviderObservationHash ||
			!bytes.Equal(provided.ProviderObservationJSON, authoritative.ProviderObservationJSON) ||
			!timesEqual(provided.ProviderPinnedAt, authoritative.ProviderPinnedAt) ||
			!timesEqual(provided.ProviderObservedAt, authoritative.ProviderObservedAt)) {
		return false
	}
	return true
}

func (processor *PublicationGateProcessor) preparePublicationPolicy(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	repository string,
) (
	eventing.PRDevelopmentPublication,
	sharedattention.PreparedPolicy,
	error,
) {
	if policy, found, err := decodePublicationGatePolicy(claim); found || err != nil {
		return claim, policy, err
	}
	prepared, err := sharedattention.PreparePolicy(
		ctx,
		processor.policies,
		sharedattention.PolicySelector{
			Repository:    repository,
			DecisionPoint: eventing.PRDevelopmentPublicationDecisionBeforePush,
		},
	)
	if err != nil {
		return claim, sharedattention.PreparedPolicy{}, err
	}
	canonical := prepared.Canonical()
	if len(canonical) == 0 || prepared.DecisionRevision() == "" {
		return claim, sharedattention.PreparedPolicy{}, errPublicationGateCorrupt
	}
	pinned, _, pinErr := processor.store.PinPRDevelopmentPublicationPolicy(
		ctx,
		eventing.PRDevelopmentPublicationPolicyPin{
			PublicationID:  claim.ID,
			ClaimToken:     claim.ClaimToken,
			ClaimEpoch:     claim.ClaimEpoch,
			PolicyRevision: prepared.DecisionRevision(),
			PinnedPolicy:   canonical,
		},
	)
	if pinErr != nil {
		pinned, prepared, pinErr = processor.recoverPublicationPolicyPin(
			ctx,
			claim,
			pinErr,
		)
		if pinErr != nil {
			return claim, sharedattention.PreparedPolicy{}, pinErr
		}
	}
	if err = validatePublicationPolicyStage(claim, pinned, prepared); err != nil {
		return claim, sharedattention.PreparedPolicy{}, err
	}
	return pinned, prepared, nil
}

func decodePublicationGatePolicy(
	publication eventing.PRDevelopmentPublication,
) (sharedattention.PreparedPolicy, bool, error) {
	hasRevision := publication.PolicyRevision != ""
	hasCanonical := len(publication.PinnedPolicy) != 0
	hasHash := publication.PinnedPolicyHash != ""
	if !hasRevision && !hasCanonical && !hasHash {
		return sharedattention.PreparedPolicy{}, false, nil
	}
	if !hasRevision || !hasCanonical || !hasHash ||
		!validControllerSHA256(publication.PinnedPolicyHash) {
		return sharedattention.PreparedPolicy{}, true, errPublicationGateCorrupt
	}
	prepared, err := sharedattention.DecodePreparedPolicy(publication.PinnedPolicy)
	if err != nil || prepared.DecisionRevision() != publication.PolicyRevision ||
		!bytes.Equal(prepared.Canonical(), publication.PinnedPolicy) {
		return sharedattention.PreparedPolicy{}, true, errPublicationGateCorrupt
	}
	return prepared, true, nil
}

func (processor *PublicationGateProcessor) recoverPublicationPolicyPin(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	original error,
) (eventing.PRDevelopmentPublication, sharedattention.PreparedPolicy, error) {
	current, err := processor.store.GetPRDevelopmentPublication(ctx, claim.ID)
	if err != nil {
		return eventing.PRDevelopmentPublication{}, sharedattention.PreparedPolicy{},
			publicationGateStoreFailure(original, errPublicationGateLocalEvidence)
	}
	decoded, found, decodeErr := decodePublicationGatePolicy(current)
	if decodeErr != nil {
		return eventing.PRDevelopmentPublication{}, sharedattention.PreparedPolicy{}, decodeErr
	}
	if !found {
		return eventing.PRDevelopmentPublication{}, sharedattention.PreparedPolicy{},
			publicationGateStoreFailure(original, errPublicationGateLocalEvidence)
	}
	return current, decoded, nil
}

func verifyPublicationNoopPolicy(policy sharedattention.PreparedPolicy) error {
	if !policy.IsNoop() {
		return errors.New("publication policy is not a no-op")
	}
	gates := policy.EffectiveGates()
	compilation, err := workflows.CompileGateWorkflow(
		sharedattention.WorkflowName,
		gates,
		nil,
	)
	if err != nil {
		return err
	}
	wantIDs := make([]string, len(gates))
	for index := range gates {
		wantIDs[index] = gates[index].ID
	}
	if compilation == nil || !compilation.Noop || compilation.Workflow != nil ||
		compilation.PrivateRoot != nil || compilation.RequiresSession ||
		compilation.RequiredSessionAgentID != "" || len(compilation.Inputs) != 0 ||
		!reflect.DeepEqual(compilation.GateIDs, wantIDs) {
		return errors.New("publication no-op policy compiled to an active workflow")
	}
	return nil
}

func (processor *PublicationGateProcessor) pinPublicationZeroSubject(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
	repository string,
	snapshot *eventing.PRDevelopmentPublicationGateContextSnapshot,
) (
	eventing.PRDevelopmentPublication,
	*eventing.PRDevelopmentPublicationGateContextSnapshot,
	error,
) {
	if subject, found, err := decodePublicationZeroSubject(publication, policy); found || err != nil {
		return publication, snapshot, err
	} else if subject.Format != "" {
		return publication, snapshot, errPublicationGateCorrupt
	}
	for attempt := 0; attempt < 2; attempt++ {
		if snapshot == nil {
			loaded, err := processor.loadPublicationGateContext(
				ctx,
				claim,
				publication,
				repository,
			)
			if err != nil {
				return publication, nil, err
			}
			snapshot = &loaded
		}
		subject, canonical, revision, err := buildPublicationZeroSubject(*snapshot, policy)
		if err != nil || subject.Format == "" {
			return publication, snapshot, fmt.Errorf("%w: build zero subject", errPublicationGateCorrupt)
		}
		pinned, _, pinErr := processor.store.PinPRDevelopmentPublicationSubject(
			ctx,
			eventing.PRDevelopmentPublicationSubjectPin{
				PublicationID:               claim.ID,
				ClaimToken:                  claim.ClaimToken,
				ClaimEpoch:                  claim.ClaimEpoch,
				PolicyRevision:              policy.DecisionRevision(),
				SubjectRevision:             revision,
				PinnedSubject:               canonical,
				ExpectedConversationVersion: snapshot.Conversation.Version,
				ExpectedTranscriptDigest:    snapshot.TranscriptDigest,
			},
		)
		if pinErr == nil {
			if err = validatePublicationSubjectStage(claim, pinned, policy, canonical, revision); err != nil {
				return publication, snapshot, err
			}
			return pinned, snapshot, nil
		}
		current, loadErr := processor.store.GetPRDevelopmentPublication(ctx, claim.ID)
		if loadErr == nil {
			_, found, decodeErr := decodePublicationZeroSubject(current, policy)
			switch {
			case decodeErr != nil:
				return publication, snapshot, decodeErr
			case found:
				if !validClaimedPublicationGateResponse(claim, current) {
					return publication, snapshot, errPublicationGateCorrupt
				}
				if err = validatePublicationGatePinProgression(current); err != nil {
					return publication, snapshot, err
				}
				return current, snapshot, nil
			}
		}
		if !errors.Is(pinErr, eventing.ErrPRDevelopmentPublicationConflict) {
			return publication, snapshot,
				publicationGateStoreFailure(pinErr, errPublicationGateLocalEvidence)
		}
		fresh, snapshotErr := processor.loadPublicationGateContext(
			ctx,
			claim,
			publication,
			repository,
		)
		if snapshotErr != nil {
			return publication, snapshot, snapshotErr
		}
		if attempt == 1 {
			return publication, &fresh, fmt.Errorf("%w: conversation changed repeatedly", errPublicationGateRetry)
		}
		snapshot = &fresh
	}
	return publication, snapshot, errPublicationGateRetry
}

type publicationZeroGateSubject struct {
	Format                   string `json:"format"`
	Mode                     string `json:"mode"`
	DecisionPoint            string `json:"decision_point"`
	PublicationID            string `json:"publication_id"`
	CaseID                   string `json:"case_id"`
	Repository               string `json:"repository"`
	ThreadID                 string `json:"thread_id"`
	SelectedOrdinal          int    `json:"selected_ordinal"`
	ThreadCasesDigest        string `json:"thread_cases_digest"`
	PolicyRevision           string `json:"policy_revision"`
	ConversationVersion      int64  `json:"conversation_version"`
	TranscriptDigest         string `json:"transcript_digest"`
	AttemptID                string `json:"attempt_id"`
	AttemptEntryID           string `json:"attempt_entry_id"`
	AttemptEntryHash         string `json:"attempt_entry_hash"`
	ReviewEntryID            string `json:"review_entry_id"`
	ReviewEntryHash          string `json:"review_entry_hash"`
	FenceOrdinal             int    `json:"fence_ordinal"`
	FenceHash                string `json:"fence_hash"`
	OrchestrationReceiptHash string `json:"orchestration_receipt_hash"`
	CIPlanDigest             string `json:"ci_plan_digest"`
	CIResultDigest           string `json:"ci_result_digest"`
	LedgerEntriesDigest      string `json:"ledger_entries_digest"`
	LedgerCheckpointsDigest  string `json:"ledger_checkpoints_digest"`
	TipCommit                string `json:"tip_commit"`
	Tree                     string `json:"tree"`
	NoChanges                bool   `json:"no_changes"`
}

func buildPublicationZeroSubject(
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
	policy sharedattention.PreparedPolicy,
) (publicationZeroGateSubject, []byte, string, error) {
	publication := snapshot.Publication
	subject := publicationZeroGateSubject{
		Format:                   publicationGateSubjectFormat,
		Mode:                     publicationGateSubjectModeZero,
		DecisionPoint:            eventing.PRDevelopmentPublicationDecisionBeforePush,
		PublicationID:            publication.ID,
		CaseID:                   publication.CaseID,
		Repository:               snapshot.Case.Repository,
		ThreadID:                 publication.ThreadID,
		SelectedOrdinal:          snapshot.SelectedOrdinal,
		ThreadCasesDigest:        snapshot.Thread.CasesDigest,
		PolicyRevision:           policy.DecisionRevision(),
		ConversationVersion:      snapshot.Conversation.Version,
		TranscriptDigest:         snapshot.TranscriptDigest,
		AttemptID:                publication.AttemptID,
		AttemptEntryID:           publication.AttemptLedgerEntryID,
		AttemptEntryHash:         publication.AttemptLedgerEntryHash,
		ReviewEntryID:            publication.ReviewLedgerEntryID,
		ReviewEntryHash:          publication.ReviewLedgerEntryHash,
		FenceOrdinal:             publication.FenceOrdinal,
		FenceHash:                publication.FenceHash,
		OrchestrationReceiptHash: publication.OrchestrationReceiptHash,
		CIPlanDigest:             publication.CIPlanDigest,
		CIResultDigest:           publication.CIResultDigest,
		LedgerEntriesDigest:      snapshot.Ledger.EntriesDigest,
		LedgerCheckpointsDigest:  snapshot.Ledger.CheckpointsDigest,
		TipCommit:                publication.TipCommit,
		Tree:                     publication.Tree,
		NoChanges:                publication.NoChanges,
	}
	if err := validatePublicationZeroSubject(subject, publication, policy); err != nil {
		return publicationZeroGateSubject{}, nil, "", err
	}
	canonical, err := json.Marshal(subject)
	if err != nil {
		return publicationZeroGateSubject{}, nil, "", err
	}
	revision := publicationZeroSubjectRevision(canonical)
	return subject, canonical, revision, nil
}

func decodePublicationZeroSubject(
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
) (publicationZeroGateSubject, bool, error) {
	hasRevision := publication.SubjectRevision != ""
	hasCanonical := len(publication.PinnedSubject) != 0
	hasHash := publication.PinnedSubjectHash != ""
	if !hasRevision && !hasCanonical && !hasHash {
		return publicationZeroGateSubject{}, false, nil
	}
	if !hasRevision || !hasCanonical || !hasHash ||
		!validControllerSHA256(publication.PinnedSubjectHash) ||
		len(publication.PinnedSubject) > eventing.MaxPRDevelopmentPublicationSubjectBytes {
		return publicationZeroGateSubject{}, true, errPublicationGateCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(publication.PinnedSubject))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var subject publicationZeroGateSubject
	if err := decoder.Decode(&subject); err != nil {
		return publicationZeroGateSubject{}, true, errPublicationGateCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return publicationZeroGateSubject{}, true, errPublicationGateCorrupt
	}
	canonical, err := json.Marshal(subject)
	if err != nil || !bytes.Equal(canonical, publication.PinnedSubject) ||
		publication.SubjectRevision != publicationZeroSubjectRevision(canonical) ||
		validatePublicationZeroSubject(subject, publication, policy) != nil {
		return publicationZeroGateSubject{}, true, errPublicationGateCorrupt
	}
	return subject, true, nil
}

func validatePublicationZeroSubject(
	subject publicationZeroGateSubject,
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
) error {
	if subject.Format != publicationGateSubjectFormat ||
		subject.Mode != publicationGateSubjectModeZero ||
		subject.DecisionPoint != eventing.PRDevelopmentPublicationDecisionBeforePush ||
		subject.PublicationID != publication.ID || subject.CaseID != publication.CaseID ||
		subject.ThreadID != publication.ThreadID || subject.SelectedOrdinal < 0 ||
		subject.SelectedOrdinal >= eventing.MaxPRDevelopmentThreadCases ||
		subject.PolicyRevision != policy.DecisionRevision() ||
		subject.ConversationVersion < 0 ||
		subject.ConversationVersion > eventing.MaxPRDevelopmentMessagesPerCase ||
		!validProviderRepositoryIdentity(subject.Repository) ||
		subject.AttemptID != publication.AttemptID ||
		subject.AttemptEntryID != publication.AttemptLedgerEntryID ||
		subject.AttemptEntryHash != publication.AttemptLedgerEntryHash ||
		subject.ReviewEntryID != publication.ReviewLedgerEntryID ||
		subject.ReviewEntryHash != publication.ReviewLedgerEntryHash ||
		subject.FenceOrdinal != publication.FenceOrdinal ||
		subject.FenceHash != publication.FenceHash ||
		subject.OrchestrationReceiptHash != publication.OrchestrationReceiptHash ||
		subject.CIPlanDigest != publication.CIPlanDigest ||
		subject.CIResultDigest != publication.CIResultDigest ||
		subject.TipCommit != publication.TipCommit || subject.Tree != publication.Tree ||
		subject.NoChanges != publication.NoChanges ||
		!validControllerSHA256(subject.ThreadCasesDigest) ||
		!validControllerSHA256(subject.TranscriptDigest) ||
		!validControllerSHA256(subject.AttemptEntryHash) ||
		!validControllerSHA256(subject.ReviewEntryHash) ||
		!validControllerSHA256(subject.FenceHash) ||
		!validControllerSHA256(subject.OrchestrationReceiptHash) ||
		!validControllerSHA256(subject.CIPlanDigest) ||
		!validControllerSHA256(subject.CIResultDigest) ||
		!validControllerSHA256(subject.LedgerEntriesDigest) ||
		!validControllerSHA256(subject.LedgerCheckpointsDigest) ||
		!validAttentionRevision(subject.PolicyRevision) {
		return errPublicationGateCorrupt
	}
	return nil
}

func publicationZeroSubjectRevision(canonical []byte) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(publicationGateSubjectRevisionDomain))
	_, _ = digest.Write([]byte{0})
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(canonical)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(canonical)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func (processor *PublicationGateProcessor) pinPublicationProvider(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	repository string,
	snapshot *eventing.PRDevelopmentPublicationGateContextSnapshot,
) (eventing.PRDevelopmentPublication, error) {
	if hasPublicationProviderPin(publication) {
		if err := validateCompletePublicationProviderPin(publication); err != nil {
			return publication, err
		}
		return publication, nil
	}
	if snapshot == nil {
		loaded, err := processor.loadPublicationGateContext(
			ctx,
			claim,
			publication,
			repository,
		)
		if err != nil {
			return publication, err
		}
		snapshot = &loaded
	}
	timed, err := processor.provider.ObservePublication(
		ctx,
		snapshot.Case,
		snapshot.Thread.Identity,
	)
	if err != nil {
		return publication, err
	}
	if err = ctx.Err(); err != nil {
		return publication, err
	}
	if timed.ObservedAt.IsZero() {
		return publication, errPublicationGateCorrupt
	}
	pinned, _, pinErr := processor.store.PinPRDevelopmentPublicationProvider(
		ctx,
		eventing.PRDevelopmentPublicationProviderPin{
			PublicationID: claim.ID,
			ClaimToken:    claim.ClaimToken,
			ClaimEpoch:    claim.ClaimEpoch,
			Observation:   timed.Observation,
			ObservedAt:    timed.ObservedAt,
		},
	)
	if pinErr != nil {
		current, loadErr := processor.store.GetPRDevelopmentPublication(ctx, claim.ID)
		if loadErr == nil && hasPublicationProviderPin(current) {
			if validateRecoveredPublicationProviderStage(claim, current, *snapshot) != nil {
				return publication, errPublicationGateCorrupt
			}
			return current, nil
		}
	}
	if pinErr != nil {
		return publication,
			publicationGateStoreFailure(pinErr, errPublicationGateProviderChanged)
	}
	if err = validatePublicationProviderStage(claim, pinned, timed); err != nil {
		return publication, err
	}
	return pinned, nil
}

func publicationProviderMatchesGateContext(
	observation eventing.PRDevelopmentPublicationProviderObservation,
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) bool {
	publication := snapshot.Publication
	return observation.Repository == snapshot.Case.Repository &&
		observation.PullNumber == snapshot.Case.PullNumber &&
		observation.HeadRepository == snapshot.Case.HeadRepository &&
		observation.HeadRef == snapshot.Case.HeadRef &&
		observation.HeadRepository == snapshot.OwnerSession.HeadRepository &&
		observation.HeadRef == snapshot.OwnerSession.HeadRef &&
		observation.HeadSHA == snapshot.OwnerSession.HeadSHA &&
		observation.HeadCloneURL == snapshot.OwnerSession.CloneURL &&
		observation.CurrentReviewState == snapshot.Case.CurrentReviewState &&
		observation.ReviewDigest == snapshot.OwnerSession.ReviewDigest &&
		observation.ReviewDigest == snapshot.Orchestration.ReviewDigest &&
		observation.HeadRef == publication.SourceRef &&
		observation.HeadSHA == publication.SourceCommit &&
		observation.HeadCloneURL == publication.SourceCloneURL
}

func (processor *PublicationGateProcessor) markPublicationPushReady(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
) (eventing.PRDevelopmentPublication, error) {
	ready, _, err := processor.store.MarkPRDevelopmentPublicationPushReady(
		ctx,
		eventing.PRDevelopmentPublicationMarkPushReady{
			PublicationID: claim.ID,
			ClaimToken:    claim.ClaimToken,
			ClaimEpoch:    claim.ClaimEpoch,
		},
	)
	if err != nil {
		current, loadErr := processor.store.GetPRDevelopmentPublication(ctx, claim.ID)
		if loadErr == nil && current.Status == eventing.PRDevelopmentPublicationPushReady {
			ready = current
			err = nil
		}
	}
	if err != nil {
		if errors.Is(err, eventing.ErrPRDevelopmentPublicationConflict) {
			return publication, fmt.Errorf("%w: %v", errPublicationGateLocalEvidence, err)
		}
		return publication, publicationGateStoreFailure(err, errPublicationGateLocalEvidence)
	}
	if err = validatePublicationPushReadyStage(claim, publication, ready); err != nil {
		return publication, err
	}
	return ready, nil
}

func (processor *PublicationGateProcessor) loadPublicationGateContext(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	current eventing.PRDevelopmentPublication,
	repository string,
) (eventing.PRDevelopmentPublicationGateContextSnapshot, error) {
	snapshot, err := processor.store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		claim.ID,
		claim.ClaimToken,
		claim.ClaimEpoch,
	)
	if err != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{},
			publicationGateStoreFailure(err, errPublicationGateLocalEvidence)
	}
	if snapshot.Case.Repository != repository {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, errPublicationGateCorrupt
	}
	if err = validatePublicationGateSnapshot(claim, current, snapshot); err != nil {
		return eventing.PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	return snapshot, nil
}

func (processor *PublicationGateProcessor) finishPublicationGateFailure(
	ctx context.Context,
	claim eventing.PRDevelopmentPublication,
	failure error,
) (PublicationGateProcessResult, error) {
	status, code, detail, terminal := classifyPublicationGateFailure(failure)
	if !terminal {
		return PublicationGateProcessResult{}, failure
	}
	completed, _, err := processor.store.CompletePRDevelopmentPublicationPrestart(
		ctx,
		eventing.PRDevelopmentPublicationPrestartCompletion{
			PublicationID: claim.ID,
			ClaimToken:    claim.ClaimToken,
			ClaimEpoch:    claim.ClaimEpoch,
			Status:        status,
			ErrorCode:     code,
			InternalError: detail,
		},
	)
	if err != nil {
		current, loadErr := processor.store.GetPRDevelopmentPublication(ctx, claim.ID)
		if loadErr == nil && current.Status == status && current.LastErrorCode == code {
			completed = current
			err = nil
		}
	}
	if err != nil {
		return PublicationGateProcessResult{}, fmt.Errorf(
			"record publication gate terminal outcome after %v: %w",
			failure,
			err,
		)
	}
	if !samePublicationGateIdentity(claim, completed) || completed.Status != status ||
		completed.LastErrorCode != code || completed.LastErrorDetail != detail ||
		completed.ClaimToken != "" || completed.ClaimFrom != "" ||
		completed.ClaimOwner != "" || completed.ClaimUntil != nil ||
		completed.DecisionRunID != "" || completed.EffectStartedAt != nil ||
		completed.PushRequestHash != "" || completed.CompletedAt == nil {
		return PublicationGateProcessResult{}, errPublicationGateCorrupt
	}
	return PublicationGateProcessResult{
		Disposition: PublicationGateTerminal,
		Publication: completed,
	}, nil
}

func classifyPublicationGateFailure(
	err error,
) (
	eventing.PRDevelopmentPublicationStatus,
	eventing.PRDevelopmentPublicationErrorCode,
	string,
	bool,
) {
	switch {
	case errors.Is(err, errPublicationGateRetry):
		return "", "", "", false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, eventing.ErrStaleLease):
		return "", "", "", false
	case errors.Is(err, errPublicationGateSuperseded):
		return eventing.PRDevelopmentPublicationSuperseded,
			eventing.PRDevelopmentPublicationErrorSuperseded,
			"publication local candidate was superseded before gate completion",
			true
	case errors.Is(err, errPublicationGateProviderChanged):
		return eventing.PRDevelopmentPublicationConflict,
			eventing.PRDevelopmentPublicationErrorProviderChanged,
			"provider pull request evidence changed before gate completion",
			true
	case errors.Is(err, errPublicationGateLocalEvidence):
		return eventing.PRDevelopmentPublicationConflict,
			eventing.PRDevelopmentPublicationErrorLocalEvidence,
			"local publication evidence changed before gate completion",
			true
	default:
		return "", "", "", false
	}
}

func publicationGateStoreFailure(err, conflict error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, eventing.ErrPRDevelopmentPublicationSuperseded):
		return fmt.Errorf("%w: %v", errPublicationGateSuperseded, err)
	case conflict != nil && errors.Is(err, eventing.ErrPRDevelopmentPublicationConflict):
		return fmt.Errorf("%w: %v", conflict, err)
	default:
		return err
	}
}

func validatePublicationGateClaimAuthority(publication eventing.PRDevelopmentPublication) error {
	if !validDevelopmentID(publication.ID, "pdpub_") || publication.ClaimToken == "" ||
		publication.ClaimToken != strings.TrimSpace(publication.ClaimToken) ||
		publication.ClaimEpoch < 1 {
		return ErrInvalidRequest
	}
	return nil
}

func validatePublicationGateClaim(publication eventing.PRDevelopmentPublication) error {
	if !validDevelopmentID(publication.ID, "pdpub_") || !validCaseID(publication.CaseID) ||
		!validDevelopmentID(publication.ThreadID, "pdt_") ||
		!validDevelopmentID(publication.ControllerID, "pctl_") ||
		!validRepairSessionID(publication.OwnerSessionID) ||
		!validDevelopmentID(publication.AttemptID, "pdr_") ||
		!validDevelopmentID(publication.AttemptLedgerEntryID, "pdle_") ||
		!validDevelopmentID(publication.ReviewLedgerEntryID, "pdle_") ||
		!validPublicationGateImmutableEvidence(publication) ||
		publication.Status != eventing.PRDevelopmentPublicationClaimed ||
		publication.ClaimFrom != eventing.PRDevelopmentPublicationPending ||
		publication.ClaimToken == "" || publication.ClaimToken != strings.TrimSpace(publication.ClaimToken) ||
		publication.ClaimOwner == "" || publication.ClaimOwner != strings.TrimSpace(publication.ClaimOwner) ||
		publication.ClaimEpoch < 1 || publication.Claims != int(publication.ClaimEpoch) ||
		publication.ClaimUntil == nil || publication.ClaimedAt == nil ||
		!publication.ClaimUntil.After(*publication.ClaimedAt) ||
		(publication.DecisionRunID != "" &&
			(!validDevelopmentID(publication.DecisionRunID, "wr_") ||
				publication.PolicyRevision == "" || publication.SubjectRevision == "" ||
				publication.ProviderObservationHash == "")) ||
		publication.ExpectedRemoteTip != "" ||
		publication.PushRequest != (eventing.PRDevelopmentPublicationPushRequest{}) ||
		len(publication.PushRequestJSON) != 0 || publication.PushRequestHash != "" ||
		publication.PushResult != (eventing.PRDevelopmentPublicationPushResult{}) ||
		len(publication.PushResultJSON) != 0 || publication.PushResultHash != "" ||
		publication.PushDisposition != "" || publication.WorkspaceClean || publication.LocalDrift ||
		publication.ReconciliationObservation != (eventing.PRDevelopmentPublicationRemoteObservation{}) ||
		len(publication.ReconciliationObservationJSON) != 0 ||
		publication.ReconciliationObservationHash != "" || publication.ReconciliationObservedAt != nil ||
		publication.Attempts != 0 || publication.EffectStartedAt != nil ||
		publication.CompletedAt != nil || publication.LastErrorCode != "" ||
		publication.LastErrorDetail != "" {
		return ErrInvalidRequest
	}
	if _, _, err := decodePublicationGatePolicy(publication); err != nil {
		return err
	}
	if err := validatePublicationGatePinProgression(publication); err != nil {
		return err
	}
	return nil
}

func validPublicationGateImmutableEvidence(
	publication eventing.PRDevelopmentPublication,
) bool {
	oids := []string{
		publication.SourceCommit,
		publication.SourceTree,
		publication.BaseCommit,
		publication.TipCommit,
		publication.Tree,
	}
	for _, oid := range oids {
		if !validObjectID(oid) || len(oid) != len(oids[0]) {
			return false
		}
	}
	return publication.ControllerRevision > 0 &&
		publication.ControllerRevision <= eventing.MaxPRDevelopmentControllerRevision &&
		publication.FenceOrdinal >= 0 &&
		publication.FenceOrdinal < eventing.MaxPRDevelopmentControllerFences &&
		publication.AttemptLedgerEntryKind == eventing.PRDevelopmentLedgerAttempt &&
		publication.ReviewLedgerEntryKind == eventing.PRDevelopmentLedgerReview &&
		publication.ReviewOutcome == eventing.PRDevelopmentLedgerReviewPassed &&
		publication.OrchestrationPhase == eventing.PRDevelopmentRepairOrchestrationCompleted &&
		publication.CIStatus == eventing.PRDevelopmentCIPassed &&
		validControllerSHA256(publication.FenceHash) &&
		validControllerSHA256(publication.AttemptLedgerEntryHash) &&
		validControllerSHA256(publication.ReviewLedgerEntryHash) &&
		validControllerSHA256(publication.OrchestrationReceiptHash) &&
		validControllerSHA256(publication.CIPlanDigest) &&
		validControllerSHA256(publication.CIResultDigest) &&
		publication.WorkspaceID != "" &&
		publication.WorkspaceID == strings.TrimSpace(publication.WorkspaceID) &&
		validDevelopmentID(publication.LineID, "pdln_") &&
		validHTTPSURL(publication.SourceCloneURL) && validStoredGitRef(publication.SourceRef) &&
		publication.LineVersion > 0 &&
		publication.LineVersion <= eventing.MaxPRDevelopmentControllerFences &&
		publication.MutationEpoch == publication.LineVersion &&
		publication.LineVersion == int64(publication.FenceOrdinal+1) &&
		validDevelopmentID(publication.ParkIntentID, "pdlnpark_") &&
		publication.NoChanges == (publication.BaseCommit == publication.TipCommit) &&
		publication.AttemptLedgerEntryID != publication.ReviewLedgerEntryID
}

func validatePublicationGatePinProgression(
	publication eventing.PRDevelopmentPublication,
) error {
	hasPolicy := publication.PolicyRevision != "" || len(publication.PinnedPolicy) != 0 ||
		publication.PinnedPolicyHash != ""
	hasSubject := publication.SubjectRevision != "" || len(publication.PinnedSubject) != 0 ||
		publication.PinnedSubjectHash != ""
	hasProvider := hasPublicationProviderPin(publication)
	policyComplete := publication.PolicyRevision != "" && len(publication.PinnedPolicy) != 0 &&
		publication.PinnedPolicyHash != ""
	subjectComplete := publication.SubjectRevision != "" && len(publication.PinnedSubject) != 0 &&
		publication.PinnedSubjectHash != ""
	if hasPolicy != policyComplete || hasSubject != subjectComplete ||
		hasSubject && !hasPolicy || hasProvider && !hasSubject {
		return errPublicationGateCorrupt
	}
	if hasProvider {
		return validateCompletePublicationProviderPin(publication)
	}
	return nil
}

func hasPublicationProviderPin(publication eventing.PRDevelopmentPublication) bool {
	return publication.ProviderObservation != (eventing.PRDevelopmentPublicationProviderObservation{}) ||
		publication.ProviderObservationHash != "" ||
		len(publication.ProviderObservationJSON) != 0 || publication.ProviderPinnedAt != nil ||
		publication.ProviderObservedAt != nil
}

func validateCompletePublicationProviderPin(
	publication eventing.PRDevelopmentPublication,
) error {
	if publication.ProviderObservationHash == "" ||
		!validControllerSHA256(publication.ProviderObservationHash) ||
		len(publication.ProviderObservationJSON) == 0 || publication.ProviderPinnedAt == nil ||
		publication.ProviderObservedAt == nil ||
		!publication.ProviderPinnedAt.Equal(*publication.ProviderObservedAt) {
		return errPublicationGateCorrupt
	}
	return nil
}

func validatePublicationGateSnapshot(
	claim eventing.PRDevelopmentPublication,
	current eventing.PRDevelopmentPublication,
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) error {
	publication := snapshot.Publication
	lastCaseMatches := len(snapshot.Thread.Cases) != 0 &&
		snapshot.Thread.Cases[len(snapshot.Thread.Cases)-1].CaseID == publication.CaseID &&
		snapshot.Thread.Cases[len(snapshot.Thread.Cases)-1].Ordinal == snapshot.SelectedOrdinal
	if !validClaimedPublicationGateResponse(claim, publication) ||
		publication.Claims != claim.Claims || !timesEqual(publication.ClaimedAt, claim.ClaimedAt) ||
		snapshot.Case.ID != publication.CaseID ||
		!validProviderRepositoryIdentity(snapshot.Case.Repository) || snapshot.Case.PullNumber < 1 ||
		!validProviderRepositoryIdentity(snapshot.Case.HeadRepository) ||
		snapshot.Case.HeadRef != publication.SourceRef ||
		snapshot.Case.HeadSHA != publication.SourceCommit ||
		snapshot.Thread.ID != publication.ThreadID || snapshot.Conversation.CaseID != publication.CaseID ||
		snapshot.Thread.Kind != eventing.PRDevelopmentThreadProvider ||
		snapshot.Thread.CaseCount != len(snapshot.Thread.Cases) || !lastCaseMatches ||
		snapshot.Thread.Identity.PullNumber != snapshot.Case.PullNumber ||
		!validControllerSHA256(snapshot.Thread.CasesDigest) ||
		!validPublicationGateConversation(snapshot.Conversation) ||
		!validControllerSHA256(snapshot.TranscriptDigest) || snapshot.SelectedOrdinal < 0 ||
		snapshot.SelectedOrdinal >= eventing.MaxPRDevelopmentThreadCases ||
		snapshot.AttemptEntry.ID != publication.AttemptLedgerEntryID ||
		snapshot.OwnerSession.ID != publication.OwnerSessionID ||
		snapshot.OwnerSession.CaseID != publication.CaseID ||
		snapshot.OwnerSession.HeadRepository != snapshot.Case.HeadRepository ||
		snapshot.OwnerSession.HeadRef != publication.SourceRef ||
		snapshot.OwnerSession.HeadSHA != publication.SourceCommit ||
		snapshot.OwnerSession.CloneURL != publication.SourceCloneURL ||
		!validAttentionRevision(snapshot.OwnerSession.ReviewDigest) ||
		snapshot.Orchestration.AttemptID != publication.AttemptID ||
		snapshot.Orchestration.SessionID != publication.OwnerSessionID ||
		snapshot.Orchestration.CaseID != publication.CaseID ||
		snapshot.Orchestration.ThreadID != publication.ThreadID ||
		snapshot.Orchestration.Phase != publication.OrchestrationPhase ||
		snapshot.Orchestration.ReviewDigest != snapshot.OwnerSession.ReviewDigest ||
		!validControllerSHA256(snapshot.Ledger.EntriesDigest) ||
		!validControllerSHA256(snapshot.Ledger.CheckpointsDigest) ||
		snapshot.AttemptEntry.Kind != publication.AttemptLedgerEntryKind ||
		snapshot.AttemptEntry.EntryHash != publication.AttemptLedgerEntryHash ||
		snapshot.ReviewEntry.ID != publication.ReviewLedgerEntryID ||
		snapshot.ReviewEntry.Kind != publication.ReviewLedgerEntryKind ||
		snapshot.ReviewEntry.EntryHash != publication.ReviewLedgerEntryHash ||
		snapshot.ReviewEntry.ReviewOutcome != publication.ReviewOutcome ||
		!samePublicationGatePins(current, publication) {
		return errPublicationGateCorrupt
	}
	return nil
}

func validPublicationGateConversation(
	conversation eventing.PRDevelopmentConversation,
) bool {
	if !validCaseID(conversation.CaseID) || conversation.Version < 0 ||
		conversation.Version > eventing.MaxPRDevelopmentMessagesPerCase ||
		conversation.Version != int64(len(conversation.Messages)) {
		return false
	}
	for index, message := range conversation.Messages {
		if !validMessageID(message.ID) || message.CaseID != conversation.CaseID ||
			message.Ordinal != index ||
			message.Role != eventing.PRDevelopmentMessageUser &&
				message.Role != eventing.PRDevelopmentMessageAssistant ||
			len(message.Content) > eventing.MaxPRDevelopmentMessageBytes ||
			!utf8.ValidString(message.Content) || strings.ContainsRune(message.Content, '\x00') {
			return false
		}
	}
	return true
}

func validatePublicationPolicyStage(
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
) error {
	decoded, found, err := decodePublicationGatePolicy(publication)
	if err != nil || !found || decoded.DecisionRevision() != policy.DecisionRevision() ||
		!bytes.Equal(decoded.Canonical(), policy.Canonical()) ||
		!validClaimedPublicationGateResponse(claim, publication) {
		return errPublicationGateCorrupt
	}
	return validatePublicationGatePinProgression(publication)
}

func validatePublicationSubjectStage(
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	policy sharedattention.PreparedPolicy,
	canonical []byte,
	revision string,
) error {
	_, found, err := decodePublicationZeroSubject(publication, policy)
	if err != nil || !found || publication.SubjectRevision != revision ||
		!bytes.Equal(publication.PinnedSubject, canonical) ||
		!validClaimedPublicationGateResponse(claim, publication) {
		return errPublicationGateCorrupt
	}
	return validatePublicationGatePinProgression(publication)
}

func validatePublicationProviderStage(
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	timed TimedPublicationProviderObservation,
) error {
	if !validClaimedPublicationGateResponse(claim, publication) ||
		validateCompletePublicationProviderPin(publication) != nil ||
		!reflect.DeepEqual(publication.ProviderObservation, timed.Observation) ||
		publication.ProviderPinnedAt == nil || !publication.ProviderPinnedAt.Equal(timed.ObservedAt) {
		return errPublicationGateCorrupt
	}
	return validatePublicationGatePinProgression(publication)
}

func validateRecoveredPublicationProviderStage(
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
	snapshot eventing.PRDevelopmentPublicationGateContextSnapshot,
) error {
	if !validClaimedPublicationGateResponse(claim, publication) ||
		validateCompletePublicationProviderPin(publication) != nil ||
		!publicationProviderMatchesGateContext(publication.ProviderObservation, snapshot) {
		return errPublicationGateCorrupt
	}
	return validatePublicationGatePinProgression(publication)
}

func validatePublicationPushReadyStage(
	claim eventing.PRDevelopmentPublication,
	previous eventing.PRDevelopmentPublication,
	ready eventing.PRDevelopmentPublication,
) error {
	if !samePublicationGateIdentity(claim, ready) || !samePublicationGatePins(previous, ready) ||
		ready.Status != eventing.PRDevelopmentPublicationPushReady || ready.ClaimFrom != "" ||
		ready.ClaimOwner != "" || ready.ClaimToken != "" || ready.ClaimUntil != nil ||
		ready.ClaimEpoch != claim.ClaimEpoch ||
		ready.DecisionRunID != "" || ready.EffectStartedAt != nil || ready.CompletedAt != nil ||
		ready.PushRequestHash != "" || ready.LastErrorCode != "" {
		return errPublicationGateCorrupt
	}
	return nil
}

func validClaimedPublicationGateResponse(
	claim eventing.PRDevelopmentPublication,
	publication eventing.PRDevelopmentPublication,
) bool {
	return samePublicationGateIdentity(claim, publication) && publication.ClaimToken == "" &&
		publication.Status == eventing.PRDevelopmentPublicationClaimed &&
		publication.ClaimFrom == eventing.PRDevelopmentPublicationPending &&
		publication.ClaimOwner == claim.ClaimOwner && publication.ClaimEpoch == claim.ClaimEpoch &&
		publication.Claims == claim.Claims && timesEqual(publication.ClaimedAt, claim.ClaimedAt) &&
		publication.ClaimUntil != nil && claim.ClaimUntil != nil &&
		!publication.ClaimUntil.Before(*claim.ClaimUntil) &&
		publicationGatePinsArePrefix(claim, publication)
}

func samePublicationGatePins(
	left eventing.PRDevelopmentPublication,
	right eventing.PRDevelopmentPublication,
) bool {
	return left.PolicyRevision == right.PolicyRevision &&
		bytes.Equal(left.PinnedPolicy, right.PinnedPolicy) &&
		left.PinnedPolicyHash == right.PinnedPolicyHash &&
		left.SubjectRevision == right.SubjectRevision &&
		bytes.Equal(left.PinnedSubject, right.PinnedSubject) &&
		left.PinnedSubjectHash == right.PinnedSubjectHash &&
		reflect.DeepEqual(left.ProviderObservation, right.ProviderObservation) &&
		left.ProviderObservationHash == right.ProviderObservationHash &&
		bytes.Equal(left.ProviderObservationJSON, right.ProviderObservationJSON) &&
		timesEqual(left.ProviderPinnedAt, right.ProviderPinnedAt) &&
		timesEqual(left.ProviderObservedAt, right.ProviderObservedAt)
}

func samePublicationGateIdentity(
	left eventing.PRDevelopmentPublication,
	right eventing.PRDevelopmentPublication,
) bool {
	return samePublicationGateOwnerIdentity(left, right) &&
		samePublicationGateEvidenceIdentity(left, right) &&
		samePublicationGateLineIdentity(left, right)
}

func samePublicationGateOwnerIdentity(
	left eventing.PRDevelopmentPublication,
	right eventing.PRDevelopmentPublication,
) bool {
	return left.ID == right.ID && left.CaseID == right.CaseID &&
		left.ThreadID == right.ThreadID && left.ControllerID == right.ControllerID &&
		left.ControllerRevision == right.ControllerRevision &&
		left.OwnerSessionID == right.OwnerSessionID && left.AttemptID == right.AttemptID &&
		left.FenceOrdinal == right.FenceOrdinal && left.FenceHash == right.FenceHash
}

func samePublicationGateEvidenceIdentity(
	left eventing.PRDevelopmentPublication,
	right eventing.PRDevelopmentPublication,
) bool {
	return left.AttemptLedgerEntryID == right.AttemptLedgerEntryID &&
		left.AttemptLedgerEntryKind == right.AttemptLedgerEntryKind &&
		left.AttemptLedgerEntryHash == right.AttemptLedgerEntryHash &&
		left.ReviewLedgerEntryID == right.ReviewLedgerEntryID &&
		left.ReviewLedgerEntryKind == right.ReviewLedgerEntryKind &&
		left.ReviewLedgerEntryHash == right.ReviewLedgerEntryHash &&
		left.ReviewOutcome == right.ReviewOutcome &&
		left.OrchestrationPhase == right.OrchestrationPhase &&
		left.OrchestrationReceiptHash == right.OrchestrationReceiptHash &&
		left.CIStatus == right.CIStatus && left.CIPlanDigest == right.CIPlanDigest &&
		left.CIResultDigest == right.CIResultDigest
}

func samePublicationGateLineIdentity(
	left eventing.PRDevelopmentPublication,
	right eventing.PRDevelopmentPublication,
) bool {
	return left.WorkspaceID == right.WorkspaceID && left.LineID == right.LineID &&
		left.SourceCloneURL == right.SourceCloneURL && left.SourceRef == right.SourceRef &&
		left.SourceCommit == right.SourceCommit && left.SourceTree == right.SourceTree &&
		left.LineVersion == right.LineVersion && left.MutationEpoch == right.MutationEpoch &&
		left.ParkIntentID == right.ParkIntentID && left.BaseCommit == right.BaseCommit &&
		left.TipCommit == right.TipCommit && left.Tree == right.Tree &&
		left.NoChanges == right.NoChanges
}

func timesEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func redactPublicationGateClaim(
	publication eventing.PRDevelopmentPublication,
) eventing.PRDevelopmentPublication {
	publication.ClaimToken = ""
	return publication
}
