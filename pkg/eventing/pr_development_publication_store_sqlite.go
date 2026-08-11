//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	_ PRDevelopmentPublicationReader                          = (*Store)(nil)
	_ PRDevelopmentPublicationGateClaimAuthenticator          = (*Store)(nil)
	_ PRDevelopmentPublicationPushClaimAuthenticator          = (*Store)(nil)
	_ PRDevelopmentPublicationGateContextSnapshotReader       = (*Store)(nil)
	_ PRDevelopmentPublicationPinnedGateContextSnapshotReader = (*Store)(nil)
	_ PRDevelopmentPublicationQueue                           = (*Store)(nil)
	_ PRDevelopmentPublicationPushJournal                     = (*Store)(nil)
	_ PRDevelopmentPublicationOutcomeReconciler               = (*Store)(nil)
	_ PRDevelopmentPublicationDecisionRunStore                = (*Store)(nil)
)

const (
	maxPRDevelopmentPublicationClaimLimit     = 64
	maxPRDevelopmentPublicationLease          = 24 * time.Hour
	maxPRDevelopmentPublicationObservationAge = 5 * time.Minute

	publicationPolicyHashDomain         = "picoclaw-pr-development-publication-policy-v1"
	publicationSubjectHashDomain        = "picoclaw-pr-development-publication-subject-v1"
	publicationProviderHashDomain       = "picoclaw-pr-development-publication-provider-v1"
	publicationPushRequestHashDomain    = "picoclaw-pr-development-publication-push-request-v1"
	publicationPushResultHashDomain     = "picoclaw-pr-development-publication-push-result-v1"
	publicationReconciliationHashDomain = "picoclaw-pr-development-publication-reconciliation-v1"
)

const prDevelopmentPublicationColumns = `
	id, case_id, thread_id, controller_id, controller_revision,
	owner_session_id, attempt_id, fence_ordinal, fence_hash,
	attempt_ledger_entry_id, attempt_ledger_entry_kind,
	attempt_ledger_entry_hash, review_ledger_entry_id,
	review_ledger_entry_kind, review_ledger_entry_hash, review_outcome,
	orchestration_phase, orchestration_receipt_hash, ci_status,
	ci_plan_digest, ci_result_digest, workspace_id, line_id,
	source_clone_url, source_ref, source_commit, source_tree, line_version,
	mutation_epoch, park_intent_id, base_commit, tip_commit, tree, no_changes,
	status, claim_from, claim_owner, claim_token, claim_until, claim_epoch,
	claims, claimed_at, attempts, available_at, policy_revision,
	pinned_policy_json, pinned_policy_hash, subject_revision,
	pinned_subject_json, pinned_subject_hash, provider_observation_json,
	provider_observation_hash, provider_pinned_at, provider_observed_at,
	reconciliation_observation_json, reconciliation_observation_hash,
	reconciliation_observed_at, decision_run_id, expected_remote_tip,
	push_request_json, push_request_hash, push_result_json, push_result_hash,
	push_disposition, workspace_clean, local_drift, last_error_code,
	last_error_detail, created_at, updated_at, effect_started_at, completed_at`

type prDevelopmentPublicationProviderWire struct {
	Repository         string                   `json:"repository"`
	PullNumber         int64                    `json:"pull_number"`
	HeadRepository     string                   `json:"head_repository"`
	HeadRef            string                   `json:"head_ref"`
	HeadSHA            string                   `json:"head_sha"`
	HeadCloneURL       string                   `json:"head_clone_url"`
	CurrentReviewState PRDevelopmentReviewState `json:"current_review_state"`
	ReviewDigest       string                   `json:"review_digest"`
}

type prDevelopmentPublicationRemoteObservationWire struct {
	Repository     string `json:"repository"`
	PullNumber     int64  `json:"pull_number"`
	HeadRepository string `json:"head_repository"`
	HeadRef        string `json:"head_ref"`
	HeadSHA        string `json:"head_sha"`
}

type prDevelopmentPublicationPushRequestWire struct {
	Repository            string `json:"repository"`
	SourceRef             string `json:"source_ref"`
	ExpectedSourceCommit  string `json:"expected_source_commit"`
	WorkspaceID           string `json:"workspace_id"`
	LineID                string `json:"line_id"`
	ExpectedVersion       int64  `json:"expected_version"`
	ExpectedMutationEpoch int64  `json:"expected_mutation_epoch"`
	ExpectedParkIntentID  string `json:"expected_park_intent_id"`
	ExpectedBase          string `json:"expected_base"`
	ExpectedTip           string `json:"expected_tip"`
	ExpectedTree          string `json:"expected_tree"`
	ExpectedRemoteTip     string `json:"expected_remote_tip"`
}

type prDevelopmentPublicationPushResultWire struct {
	WorkspaceID       string                                  `json:"workspace_id"`
	Version           int64                                   `json:"version"`
	MutationEpoch     int64                                   `json:"mutation_epoch"`
	ParkIntentID      string                                  `json:"park_intent_id"`
	BaseCommit        string                                  `json:"base_commit"`
	Tip               string                                  `json:"tip"`
	Tree              string                                  `json:"tree"`
	RemoteRef         string                                  `json:"remote_ref"`
	ExpectedRemoteTip string                                  `json:"expected_remote_tip"`
	RemoteTip         string                                  `json:"remote_tip"`
	Disposition       PRDevelopmentPublicationPushDisposition `json:"disposition"`
	WorkspaceClean    bool                                    `json:"workspace_clean"`
}

// GetPRDevelopmentPublication returns one complete private publication record.
func (s *Store) GetPRDevelopmentPublication(
	ctx context.Context,
	publicationID string,
) (PRDevelopmentPublication, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, err
	}
	publicationID = strings.TrimSpace(publicationID)
	if !validPrefixedHexID(publicationID, prDevelopmentPublicationIDPrefix) {
		return PRDevelopmentPublication{}, fmt.Errorf(
			"%w: invalid publication ID",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	publication, err := getPRDevelopmentPublicationByID(ctx, s.db, publicationID)
	if err != nil {
		return PRDevelopmentPublication{}, s.dbError(err)
	}
	return redactPRDevelopmentPublicationAuthority(publication), nil
}

// GetPRDevelopmentPublicationForReview resolves the unique journal occurrence
// admitted atomically with one passed local-review ledger entry.
func (s *Store) GetPRDevelopmentPublicationForReview(
	ctx context.Context,
	reviewLedgerEntryID string,
) (PRDevelopmentPublication, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, err
	}
	reviewLedgerEntryID = strings.TrimSpace(reviewLedgerEntryID)
	if !validPrefixedHexID(reviewLedgerEntryID, prDevelopmentLedgerEntryIDPrefix) {
		return PRDevelopmentPublication{}, fmt.Errorf(
			"%w: invalid publication review entry ID",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	publication, err := getPRDevelopmentPublicationByReview(
		ctx,
		s.db,
		reviewLedgerEntryID,
	)
	if err != nil {
		return PRDevelopmentPublication{}, s.dbError(err)
	}
	return redactPRDevelopmentPublicationAuthority(publication), nil
}

// AuthenticateClaimedPRDevelopmentPublicationGate proves that an exact live
// initial publication claim still owns the current publishable local candidate.
// It performs no conversation, provider, workflow, model, filesystem, or Git
// read or effect and returns only the redacted publication plus the exact case
// repository selected by the same high-water read.
func (s *Store) AuthenticateClaimedPRDevelopmentPublicationGate(
	ctx context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
) (PRDevelopmentPublicationGateAuthentication, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublicationGateAuthentication{}, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		publicationID,
		claimToken,
		claimEpoch,
	)
	if err != nil {
		return PRDevelopmentPublicationGateAuthentication{}, err
	}
	now, err := s.currentTime()
	if err != nil {
		return PRDevelopmentPublicationGateAuthentication{}, err
	}
	var authentication PRDevelopmentPublicationGateAuthentication
	err = s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			loaded, loadErr := getPRDevelopmentPublicationByID(
				ctx,
				queryer,
				publicationID,
			)
			if loadErr != nil {
				return loadErr
			}
			if claimErr := requireLivePRDevelopmentPublicationClaim(
				loaded,
				claimToken,
				claimEpoch,
				now,
			); claimErr != nil {
				return claimErr
			}
			if loaded.Status != PRDevelopmentPublicationClaimed ||
				loaded.ClaimFrom != PRDevelopmentPublicationPending {
				return ErrStaleLease
			}
			highWater, loadErr := loadCurrentPRDevelopmentPublicationHighWater(
				ctx,
				queryer,
				loaded,
			)
			if loadErr != nil {
				return loadErr
			}
			authentication = PRDevelopmentPublicationGateAuthentication{
				Publication: redactPRDevelopmentPublicationAuthority(loaded),
				Repository:  highWater.Case.Repository,
			}
			return nil
		},
	)
	if err != nil {
		return PRDevelopmentPublicationGateAuthentication{}, fmt.Errorf(
			"authenticate claimed pull request development publication gate: %w",
			s.dbError(err),
		)
	}
	return authentication, nil
}

// AuthenticateClaimedPRDevelopmentPublicationPush proves that an exact live
// push-ready publication claim still owns the current publishable local
// candidate. It performs no conversation, provider, workflow, model,
// filesystem, Git, scheduling, or mutation effect and returns only the
// redacted publication plus the exact case and immutable provider thread
// identity selected by the same high-water read.
func (s *Store) AuthenticateClaimedPRDevelopmentPublicationPush(
	ctx context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
) (PRDevelopmentPublicationPushAuthentication, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublicationPushAuthentication{}, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		publicationID,
		claimToken,
		claimEpoch,
	)
	if err != nil {
		return PRDevelopmentPublicationPushAuthentication{}, err
	}
	now, err := s.currentTime()
	if err != nil {
		return PRDevelopmentPublicationPushAuthentication{}, err
	}
	var authentication PRDevelopmentPublicationPushAuthentication
	err = s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			loaded, loadErr := getPRDevelopmentPublicationByID(
				ctx,
				queryer,
				publicationID,
			)
			if loadErr != nil {
				return s.classifyPRDevelopmentPublicationPushAuthenticationReadError(
					loadErr,
					false,
				)
			}
			if claimErr := requireLivePRDevelopmentPublicationClaim(
				loaded,
				claimToken,
				claimEpoch,
				now,
			); claimErr != nil {
				return claimErr
			}
			if loaded.Status != PRDevelopmentPublicationClaimed ||
				loaded.ClaimFrom != PRDevelopmentPublicationPushReady {
				return ErrStaleLease
			}
			highWater, loadErr := loadCurrentPRDevelopmentPublicationHighWater(
				ctx,
				queryer,
				loaded,
			)
			if loadErr != nil {
				return s.classifyPRDevelopmentPublicationPushAuthenticationReadError(
					loadErr,
					true,
				)
			}
			authentication = PRDevelopmentPublicationPushAuthentication{
				Publication:    redactPRDevelopmentPublicationAuthority(loaded),
				Case:           highWater.Case,
				ThreadIdentity: highWater.Thread.Identity,
			}
			return nil
		},
	)
	if err != nil {
		return PRDevelopmentPublicationPushAuthentication{}, fmt.Errorf(
			"authenticate claimed pull request development publication push: %w",
			s.dbError(err),
		)
	}
	return authentication, nil
}

// The authenticated high-water graph is assembled exclusively from SQLite
// reads and deterministic stored-state validators. Once classified operational
// store/driver and context failures are excluded, an unclassified read error
// is durable integrity damage rather than a transient scheduling failure.
func (s *Store) classifyPRDevelopmentPublicationPushAuthenticationReadError(
	err error,
	missingIsRecovery bool,
) error {
	err = s.dbError(err)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrPRDevelopmentPublicationConflict) ||
		errors.Is(err, ErrPRDevelopmentPublicationSuperseded) ||
		errors.Is(err, ErrStaleLease) ||
		errors.Is(err, ErrPRDevelopmentPublicationRecoveryRequired) {
		return err
	}
	if errors.Is(err, ErrNotFound) && !missingIsRecovery {
		return err
	}
	if isPRDevelopmentPublicationPushAuthenticationOperationalError(err) {
		return err
	}
	return fmt.Errorf(
		"%w: publication push high-water integrity failed: %w",
		ErrPRDevelopmentPublicationRecoveryRequired,
		err,
	)
}

func isPRDevelopmentPublicationPushAuthenticationOperationalError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrClosed) || errors.Is(err, ErrSchemaTooNew) ||
		errors.Is(err, ErrSchemaInvalid) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone) {
		return true
	}
	var sqliteError *sqlite.Error
	return errors.As(err, &sqliteError) && sqliteError != nil &&
		operationalPRDevelopmentPublicationSQLiteError(sqliteError.Code())
}

func operationalPRDevelopmentPublicationSQLiteError(code int) bool {
	switch code & 0xff {
	case sqlite3.SQLITE_PERM,
		sqlite3.SQLITE_ABORT,
		sqlite3.SQLITE_BUSY,
		sqlite3.SQLITE_LOCKED,
		sqlite3.SQLITE_NOMEM,
		sqlite3.SQLITE_READONLY,
		sqlite3.SQLITE_INTERRUPT,
		sqlite3.SQLITE_IOERR,
		sqlite3.SQLITE_FULL,
		sqlite3.SQLITE_CANTOPEN,
		sqlite3.SQLITE_PROTOCOL,
		sqlite3.SQLITE_SCHEMA,
		sqlite3.SQLITE_NOLFS,
		sqlite3.SQLITE_AUTH:
		return true
	default:
		return false
	}
}

// GetClaimedPRDevelopmentPublicationGateContextSnapshot returns the complete
// local subject for an exact live initial publication claim in one read
// transaction. It performs no provider, workflow, model, filesystem, or Git
// effect and redacts the claim bearer from the returned publication.
func (s *Store) GetClaimedPRDevelopmentPublicationGateContextSnapshot(
	ctx context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
) (PRDevelopmentPublicationGateContextSnapshot, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		publicationID,
		claimToken,
		claimEpoch,
	)
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	now, err := s.currentTime()
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	var snapshot PRDevelopmentPublicationGateContextSnapshot
	err = s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			publication, loadErr := getPRDevelopmentPublicationByID(
				ctx,
				queryer,
				publicationID,
			)
			if loadErr != nil {
				return loadErr
			}
			if claimErr := requireLivePRDevelopmentPublicationClaim(
				publication,
				claimToken,
				claimEpoch,
				now,
			); claimErr != nil {
				return claimErr
			}
			if publication.Status != PRDevelopmentPublicationClaimed ||
				publication.ClaimFrom != PRDevelopmentPublicationPending {
				return ErrStaleLease
			}
			snapshot, loadErr = loadCurrentPRDevelopmentPublicationGateContext(
				ctx,
				queryer,
				publication,
			)
			return loadErr
		},
	)
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, fmt.Errorf(
			"get claimed pull request development publication gate context: %w",
			s.dbError(err),
		)
	}
	return snapshot, nil
}

// GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot recreates the
// exact append-only conversation prefix captured by an already-pinned subject.
// The read still validates the live initial claim and current local publication
// high-water in one transaction; later conversation messages are excluded.
func (s *Store) GetClaimedPRDevelopmentPublicationPinnedGateContextSnapshot(
	ctx context.Context,
	publicationID string,
	claimToken string,
	claimEpoch int64,
	anchor PRDevelopmentPublicationGateContextAnchor,
) (PRDevelopmentPublicationGateContextSnapshot, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		publicationID,
		claimToken,
		claimEpoch,
	)
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	anchor.SubjectRevision = strings.TrimSpace(anchor.SubjectRevision)
	anchor.TranscriptDigest = strings.TrimSpace(anchor.TranscriptDigest)
	if !validReviewPolicyRevision(anchor.SubjectRevision) ||
		anchor.ConversationVersion < 0 ||
		anchor.ConversationVersion > MaxPRDevelopmentMessagesPerCase ||
		!validPRDevelopmentHex(anchor.TranscriptDigest, sha256.Size*2) {
		return PRDevelopmentPublicationGateContextSnapshot{}, fmt.Errorf(
			"%w: publication gate context anchor is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	now, err := s.currentTime()
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	var snapshot PRDevelopmentPublicationGateContextSnapshot
	err = s.withPRDevelopmentConversationReadSnapshot(
		ctx,
		func(queryer rowsQueryer) error {
			publication, loadErr := getPRDevelopmentPublicationByID(
				ctx,
				queryer,
				publicationID,
			)
			if loadErr != nil {
				return loadErr
			}
			if claimErr := requireLivePRDevelopmentPublicationClaim(
				publication,
				claimToken,
				claimEpoch,
				now,
			); claimErr != nil {
				return claimErr
			}
			if publication.Status != PRDevelopmentPublicationClaimed ||
				publication.ClaimFrom != PRDevelopmentPublicationPending {
				return ErrStaleLease
			}
			if publication.SubjectRevision == "" ||
				publication.SubjectRevision != anchor.SubjectRevision {
				return fmt.Errorf(
					"%w: publication subject pin differs from gate context anchor",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			pinnedAnchor, anchorErr := prDevelopmentPublicationGateContextAnchorFromSubject(
				publication,
			)
			if anchorErr != nil {
				return anchorErr
			}
			if pinnedAnchor != anchor {
				return fmt.Errorf(
					"%w: publication subject conversation anchor differs from requested gate context anchor",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			snapshot, loadErr = loadCurrentPRDevelopmentPublicationGateContextAtConversation(
				ctx,
				queryer,
				publication,
				pinnedAnchor,
			)
			return loadErr
		},
	)
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, fmt.Errorf(
			"get claimed pull request development pinned gate context: %w",
			s.dbError(err),
		)
	}
	return snapshot, nil
}

func prDevelopmentPublicationGateContextAnchorFromSubject(
	publication PRDevelopmentPublication,
) (PRDevelopmentPublicationGateContextAnchor, error) {
	var subject map[string]json.RawMessage
	if err := json.Unmarshal(publication.PinnedSubject, &subject); err != nil {
		return PRDevelopmentPublicationGateContextAnchor{}, fmt.Errorf(
			"%w: pinned publication subject is not a gate context object",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	conversationVersionJSON, versionPresent := subject["conversation_version"]
	transcriptDigestJSON, digestPresent := subject["transcript_digest"]
	var conversationVersion int64
	var transcriptDigest string
	if !versionPresent || !digestPresent ||
		json.Unmarshal(conversationVersionJSON, &conversationVersion) != nil ||
		json.Unmarshal(transcriptDigestJSON, &transcriptDigest) != nil ||
		conversationVersion < 0 ||
		conversationVersion > MaxPRDevelopmentMessagesPerCase ||
		!validPRDevelopmentHex(transcriptDigest, sha256.Size*2) {
		return PRDevelopmentPublicationGateContextAnchor{}, fmt.Errorf(
			"%w: pinned publication subject has an invalid gate context anchor",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	return PRDevelopmentPublicationGateContextAnchor{
		SubjectRevision:     publication.SubjectRevision,
		ConversationVersion: conversationVersion,
		TranscriptDigest:    transcriptDigest,
	}, nil
}

func redactPRDevelopmentPublicationAuthority(
	publication PRDevelopmentPublication,
) PRDevelopmentPublication {
	publication.ClaimToken = ""
	return publication
}

func getPRDevelopmentPublicationByID(
	ctx context.Context,
	queryer rowQueryer,
	publicationID string,
) (PRDevelopmentPublication, error) {
	return scanPRDevelopmentPublication(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentPublicationColumns+`
		FROM pr_development_publications
		WHERE id = ?`, publicationID))
}

func getPRDevelopmentPublicationByReview(
	ctx context.Context,
	queryer rowQueryer,
	reviewLedgerEntryID string,
) (PRDevelopmentPublication, error) {
	return scanPRDevelopmentPublication(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentPublicationColumns+`
		FROM pr_development_publications
		WHERE review_ledger_entry_id = ?`, reviewLedgerEntryID))
}

func getPRDevelopmentPublicationByRunID(
	ctx context.Context,
	queryer rowQueryer,
	runID string,
) (PRDevelopmentPublication, error) {
	return scanPRDevelopmentPublication(queryer.QueryRowContext(ctx, `
		SELECT `+prDevelopmentPublicationColumns+`
		FROM pr_development_publications
		WHERE decision_run_id = ?`, runID))
}

func scanPRDevelopmentPublication(scanner rowScanner) (PRDevelopmentPublication, error) {
	var (
		publication                           PRDevelopmentPublication
		noChanges, workspaceClean, localDrift int64
		fenceOrdinal, claims, attempts        int64
		policyJSON, subjectJSON               []byte
		providerJSON, reconciliationJSON      []byte
		requestJSON, resultJSON               []byte
		claimUntil, claimedAt                 sql.NullInt64
		providerPinnedAt, providerObservedAt  sql.NullInt64
		reconciliationObservedAt              sql.NullInt64
		effectStartedAt, completedAt          sql.NullInt64
		availableAt, createdAt, updatedAt     int64
	)
	if err := scanner.Scan(
		&publication.ID,
		&publication.CaseID,
		&publication.ThreadID,
		&publication.ControllerID,
		&publication.ControllerRevision,
		&publication.OwnerSessionID,
		&publication.AttemptID,
		&fenceOrdinal,
		&publication.FenceHash,
		&publication.AttemptLedgerEntryID,
		&publication.AttemptLedgerEntryKind,
		&publication.AttemptLedgerEntryHash,
		&publication.ReviewLedgerEntryID,
		&publication.ReviewLedgerEntryKind,
		&publication.ReviewLedgerEntryHash,
		&publication.ReviewOutcome,
		&publication.OrchestrationPhase,
		&publication.OrchestrationReceiptHash,
		&publication.CIStatus,
		&publication.CIPlanDigest,
		&publication.CIResultDigest,
		&publication.WorkspaceID,
		&publication.LineID,
		&publication.SourceCloneURL,
		&publication.SourceRef,
		&publication.SourceCommit,
		&publication.SourceTree,
		&publication.LineVersion,
		&publication.MutationEpoch,
		&publication.ParkIntentID,
		&publication.BaseCommit,
		&publication.TipCommit,
		&publication.Tree,
		&noChanges,
		&publication.Status,
		&publication.ClaimFrom,
		&publication.ClaimOwner,
		&publication.ClaimToken,
		&claimUntil,
		&publication.ClaimEpoch,
		&claims,
		&claimedAt,
		&attempts,
		&availableAt,
		&publication.PolicyRevision,
		&policyJSON,
		&publication.PinnedPolicyHash,
		&publication.SubjectRevision,
		&subjectJSON,
		&publication.PinnedSubjectHash,
		&providerJSON,
		&publication.ProviderObservationHash,
		&providerPinnedAt,
		&providerObservedAt,
		&reconciliationJSON,
		&publication.ReconciliationObservationHash,
		&reconciliationObservedAt,
		&publication.DecisionRunID,
		&publication.ExpectedRemoteTip,
		&requestJSON,
		&publication.PushRequestHash,
		&resultJSON,
		&publication.PushResultHash,
		&publication.PushDisposition,
		&workspaceClean,
		&localDrift,
		&publication.LastErrorCode,
		&publication.LastErrorDetail,
		&createdAt,
		&updatedAt,
		&effectStartedAt,
		&completedAt,
	); err != nil {
		return PRDevelopmentPublication{}, err
	}
	publication.FenceOrdinal = int(fenceOrdinal)
	publication.Claims = int(claims)
	publication.Attempts = int(attempts)
	if int64(publication.FenceOrdinal) != fenceOrdinal ||
		int64(publication.Claims) != claims || int64(publication.Attempts) != attempts ||
		(noChanges != 0 && noChanges != 1) ||
		(workspaceClean != 0 && workspaceClean != 1) ||
		(localDrift != 0 && localDrift != 1) {
		return PRDevelopmentPublication{}, errors.New(
			"stored publication integer is invalid",
		)
	}
	publication.NoChanges = noChanges == 1
	publication.WorkspaceClean = workspaceClean == 1
	publication.LocalDrift = localDrift == 1
	publication.ClaimUntil = fromNullableTime(claimUntil)
	publication.ClaimedAt = fromNullableTime(claimedAt)
	publication.ProviderPinnedAt = fromNullableTime(providerPinnedAt)
	publication.ProviderObservedAt = fromNullableTime(providerObservedAt)
	publication.ReconciliationObservedAt = fromNullableTime(reconciliationObservedAt)
	publication.EffectStartedAt = fromNullableTime(effectStartedAt)
	publication.CompletedAt = fromNullableTime(completedAt)
	publication.AvailableAt = fromDBTime(availableAt)
	publication.CreatedAt = fromDBTime(createdAt)
	publication.UpdatedAt = fromDBTime(updatedAt)
	publication.PinnedPolicy = cloneBytes(policyJSON)
	publication.PinnedSubject = cloneBytes(subjectJSON)
	publication.ProviderObservationJSON = cloneBytes(providerJSON)
	publication.ReconciliationObservationJSON = cloneBytes(reconciliationJSON)
	publication.PushRequestJSON = cloneBytes(requestJSON)
	publication.PushResultJSON = cloneBytes(resultJSON)

	var err error
	if len(providerJSON) != 0 {
		publication.ProviderObservation, err = decodePRDevelopmentPublicationProvider(providerJSON)
		if err != nil {
			return PRDevelopmentPublication{}, err
		}
	}
	if len(reconciliationJSON) != 0 {
		publication.ReconciliationObservation, err = decodePRDevelopmentPublicationRemoteObservation(
			reconciliationJSON,
		)
		if err != nil {
			return PRDevelopmentPublication{}, err
		}
	}
	if len(requestJSON) != 0 {
		publication.PushRequest, err = decodePRDevelopmentPublicationPushRequest(requestJSON)
		if err != nil {
			return PRDevelopmentPublication{}, err
		}
	}
	if len(resultJSON) != 0 {
		publication.PushResult, err = decodePRDevelopmentPublicationPushResult(resultJSON)
		if err != nil {
			return PRDevelopmentPublication{}, err
		}
	}
	if err := validateStoredPRDevelopmentPublication(publication); err != nil {
		return PRDevelopmentPublication{}, fmt.Errorf(
			"stored pull request development publication is invalid: %w",
			err,
		)
	}
	return publication, nil
}

// exactPRDevelopmentPublicationJSON validates an opaque producer-canonical
// JSON value while preserving object field order. Policy and subject consumers
// own their semantic shapes; this layer still rejects invalid UTF-8, duplicate
// keys at every depth, noncanonical string escapes, whitespace, and trailing
// values without sorting producer-owned objects.
func exactPRDevelopmentPublicationJSON(
	raw []byte,
	maximum int,
) ([]byte, error) {
	if len(raw) < 2 || len(raw) > maximum || !utf8.Valid(raw) ||
		!bytes.Equal(raw, bytes.TrimSpace(raw)) {
		return nil, ErrInvalidPRDevelopmentPublication
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var canonical bytes.Buffer
	if err := appendExactPRDevelopmentPublicationJSONValue(&canonical, decoder); err != nil ||
		!bytes.Equal(raw, canonical.Bytes()) {
		return nil, ErrInvalidPRDevelopmentPublication
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidPRDevelopmentPublication
	}
	return canonical.Bytes(), nil
}

func appendExactPRDevelopmentPublicationJSONValue(
	destination *bytes.Buffer,
	decoder *json.Decoder,
) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			destination.WriteByte('{')
			seen := make(map[string]struct{})
			first := true
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, ok := keyToken.(string)
				if keyErr != nil || !ok {
					return ErrInvalidPRDevelopmentPublication
				}
				if _, duplicate := seen[key]; duplicate {
					return ErrInvalidPRDevelopmentPublication
				}
				seen[key] = struct{}{}
				if !first {
					destination.WriteByte(',')
				}
				first = false
				encodedKey, encodeErr := json.Marshal(key)
				if encodeErr != nil {
					return encodeErr
				}
				destination.Write(encodedKey)
				destination.WriteByte(':')
				if valueErr := appendExactPRDevelopmentPublicationJSONValue(
					destination,
					decoder,
				); valueErr != nil {
					return valueErr
				}
			}
			end, endErr := decoder.Token()
			if endErr != nil || end != json.Delim('}') {
				return ErrInvalidPRDevelopmentPublication
			}
			destination.WriteByte('}')
			return nil
		case '[':
			destination.WriteByte('[')
			for index := 0; decoder.More(); index++ {
				if index > 0 {
					destination.WriteByte(',')
				}
				if valueErr := appendExactPRDevelopmentPublicationJSONValue(
					destination,
					decoder,
				); valueErr != nil {
					return valueErr
				}
			}
			end, endErr := decoder.Token()
			if endErr != nil || end != json.Delim(']') {
				return ErrInvalidPRDevelopmentPublication
			}
			destination.WriteByte(']')
			return nil
		default:
			return ErrInvalidPRDevelopmentPublication
		}
	case string:
		encoded, encodeErr := json.Marshal(value)
		if encodeErr != nil {
			return encodeErr
		}
		destination.Write(encoded)
		return nil
	case json.Number:
		destination.WriteString(value.String())
		return nil
	case bool:
		if value {
			destination.WriteString("true")
		} else {
			destination.WriteString("false")
		}
		return nil
	case nil:
		destination.WriteString("null")
		return nil
	default:
		return ErrInvalidPRDevelopmentPublication
	}
}

func hashPRDevelopmentPublicationBlob(domain string, raw []byte) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(raw)
	return hex.EncodeToString(digest.Sum(nil))
}

func encodePRDevelopmentPublicationProvider(
	observation PRDevelopmentPublicationProviderObservation,
) (PRDevelopmentPublicationProviderObservation, []byte, string, error) {
	observation.Repository = strings.TrimSpace(observation.Repository)
	observation.HeadRepository = strings.TrimSpace(observation.HeadRepository)
	observation.HeadRef = strings.TrimSpace(observation.HeadRef)
	observation.HeadSHA = strings.TrimSpace(observation.HeadSHA)
	observation.HeadCloneURL = strings.TrimSpace(observation.HeadCloneURL)
	observation.ReviewDigest = strings.TrimSpace(observation.ReviewDigest)
	if !validPRDevelopmentRepository(observation.Repository) || observation.PullNumber < 1 ||
		observation.PullNumber > maxReviewPullNumber ||
		!validPRDevelopmentRepository(observation.HeadRepository) ||
		!validPRDevelopmentGitRef(observation.HeadRef) ||
		!validPRDevelopmentHex(observation.HeadSHA, 40, 64) ||
		!validPRDevelopmentRepairCloneURL(observation.HeadCloneURL) ||
		!validPRDevelopmentRepairReviewDigest(observation.ReviewDigest) ||
		!validPRDevelopmentReviewState(observation.CurrentReviewState) {
		return PRDevelopmentPublicationProviderObservation{}, nil, "", fmt.Errorf(
			"%w: provider observation is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	wire := prDevelopmentPublicationProviderWire{
		Repository: observation.Repository, PullNumber: observation.PullNumber,
		HeadRepository: observation.HeadRepository, HeadRef: observation.HeadRef,
		HeadSHA: observation.HeadSHA, HeadCloneURL: observation.HeadCloneURL,
		CurrentReviewState: observation.CurrentReviewState,
		ReviewDigest:       observation.ReviewDigest,
	}
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > MaxPRDevelopmentPublicationProviderBytes {
		return PRDevelopmentPublicationProviderObservation{}, nil, "", fmt.Errorf(
			"%w: encode provider observation",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	return observation, raw, hashPRDevelopmentPublicationBlob(
		publicationProviderHashDomain,
		raw,
	), nil
}

func decodePRDevelopmentPublicationProvider(
	raw []byte,
) (PRDevelopmentPublicationProviderObservation, error) {
	var wire prDevelopmentPublicationProviderWire
	if err := decodeExactPRDevelopmentPublicationJSON(raw, &wire); err != nil {
		return PRDevelopmentPublicationProviderObservation{}, err
	}
	observation, canonical, _, err := encodePRDevelopmentPublicationProvider(
		PRDevelopmentPublicationProviderObservation{
			Repository: wire.Repository, PullNumber: wire.PullNumber,
			HeadRepository: wire.HeadRepository, HeadRef: wire.HeadRef,
			HeadSHA: wire.HeadSHA, HeadCloneURL: wire.HeadCloneURL,
			CurrentReviewState: wire.CurrentReviewState, ReviewDigest: wire.ReviewDigest,
		},
	)
	if err != nil || !bytes.Equal(raw, canonical) {
		return PRDevelopmentPublicationProviderObservation{}, errors.New(
			"stored publication provider observation is not canonical",
		)
	}
	return observation, nil
}

func encodePRDevelopmentPublicationRemoteObservation(
	observation PRDevelopmentPublicationRemoteObservation,
) (PRDevelopmentPublicationRemoteObservation, []byte, string, error) {
	observation.Repository = strings.TrimSpace(observation.Repository)
	observation.HeadRepository = strings.TrimSpace(observation.HeadRepository)
	observation.HeadRef = strings.TrimSpace(observation.HeadRef)
	observation.HeadSHA = strings.ToLower(strings.TrimSpace(observation.HeadSHA))
	if !validPRDevelopmentRepository(observation.Repository) || observation.PullNumber < 1 ||
		observation.PullNumber > maxReviewPullNumber ||
		!validPRDevelopmentRepository(observation.HeadRepository) ||
		!validPRDevelopmentGitRef(observation.HeadRef) ||
		!validPRDevelopmentHex(observation.HeadSHA, 40, 64) {
		return PRDevelopmentPublicationRemoteObservation{}, nil, "", fmt.Errorf(
			"%w: remote observation is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	wire := prDevelopmentPublicationRemoteObservationWire{
		Repository: observation.Repository, PullNumber: observation.PullNumber,
		HeadRepository: observation.HeadRepository, HeadRef: observation.HeadRef,
		HeadSHA: observation.HeadSHA,
	}
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > MaxPRDevelopmentPublicationProviderBytes {
		return PRDevelopmentPublicationRemoteObservation{}, nil, "", fmt.Errorf(
			"%w: remote observation exceeds its bound",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	return observation, raw, hashPRDevelopmentPublicationBlob(
		publicationReconciliationHashDomain,
		raw,
	), nil
}

func decodePRDevelopmentPublicationRemoteObservation(
	raw []byte,
) (PRDevelopmentPublicationRemoteObservation, error) {
	var wire prDevelopmentPublicationRemoteObservationWire
	if err := decodeExactPRDevelopmentPublicationJSON(raw, &wire); err != nil {
		return PRDevelopmentPublicationRemoteObservation{}, err
	}
	observation, canonical, _, err := encodePRDevelopmentPublicationRemoteObservation(
		PRDevelopmentPublicationRemoteObservation{
			Repository: wire.Repository, PullNumber: wire.PullNumber,
			HeadRepository: wire.HeadRepository, HeadRef: wire.HeadRef,
			HeadSHA: wire.HeadSHA,
		},
	)
	if err != nil || !bytes.Equal(raw, canonical) {
		return PRDevelopmentPublicationRemoteObservation{}, errors.New(
			"stored publication remote observation is not canonical",
		)
	}
	return observation, nil
}

func encodePRDevelopmentPublicationPushRequest(
	request PRDevelopmentPublicationPushRequest,
) (PRDevelopmentPublicationPushRequest, []byte, string, error) {
	request.Repository = strings.TrimSpace(request.Repository)
	request.SourceRef = strings.TrimSpace(request.SourceRef)
	request.ExpectedSourceCommit = strings.TrimSpace(request.ExpectedSourceCommit)
	request.WorkspaceID = strings.TrimSpace(request.WorkspaceID)
	request.LineID = strings.TrimSpace(request.LineID)
	request.ExpectedParkIntentID = strings.TrimSpace(request.ExpectedParkIntentID)
	request.ExpectedBase = strings.TrimSpace(request.ExpectedBase)
	request.ExpectedTip = strings.TrimSpace(request.ExpectedTip)
	request.ExpectedTree = strings.TrimSpace(request.ExpectedTree)
	request.ExpectedRemoteTip = strings.TrimSpace(request.ExpectedRemoteTip)
	if !validPRDevelopmentRepairCloneURL(request.Repository) ||
		!validPRDevelopmentGitRef(request.SourceRef) ||
		!validPRDevelopmentRepairIdentity(request.WorkspaceID, 256) ||
		!validPrefixedHexID(request.LineID, prDevelopmentLineIDPrefix) ||
		request.ExpectedVersion < 1 || request.ExpectedVersion > MaxPRDevelopmentControllerFences ||
		request.ExpectedMutationEpoch != request.ExpectedVersion ||
		!validPRDevelopmentRepairIdentity(
			request.ExpectedParkIntentID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validSameWidthPRDevelopmentOIDs(
		request.ExpectedSourceCommit,
		request.ExpectedBase,
		request.ExpectedTip,
		request.ExpectedTree,
		request.ExpectedRemoteTip,
	) {
		return PRDevelopmentPublicationPushRequest{}, nil, "", fmt.Errorf(
			"%w: push request is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	wire := prDevelopmentPublicationPushRequestWire{
		Repository: request.Repository, SourceRef: request.SourceRef,
		ExpectedSourceCommit: request.ExpectedSourceCommit,
		WorkspaceID:          request.WorkspaceID, LineID: request.LineID,
		ExpectedVersion:       request.ExpectedVersion,
		ExpectedMutationEpoch: request.ExpectedMutationEpoch,
		ExpectedParkIntentID:  request.ExpectedParkIntentID,
		ExpectedBase:          request.ExpectedBase, ExpectedTip: request.ExpectedTip,
		ExpectedTree: request.ExpectedTree, ExpectedRemoteTip: request.ExpectedRemoteTip,
	}
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > MaxPRDevelopmentPublicationRequestBytes {
		return PRDevelopmentPublicationPushRequest{}, nil, "", ErrInvalidPRDevelopmentPublication
	}
	return request, raw, hashPRDevelopmentPublicationBlob(
		publicationPushRequestHashDomain,
		raw,
	), nil
}

func decodePRDevelopmentPublicationPushRequest(
	raw []byte,
) (PRDevelopmentPublicationPushRequest, error) {
	var wire prDevelopmentPublicationPushRequestWire
	if err := decodeExactPRDevelopmentPublicationJSON(raw, &wire); err != nil {
		return PRDevelopmentPublicationPushRequest{}, err
	}
	request, canonical, _, err := encodePRDevelopmentPublicationPushRequest(
		PRDevelopmentPublicationPushRequest{
			Repository: wire.Repository, SourceRef: wire.SourceRef,
			ExpectedSourceCommit: wire.ExpectedSourceCommit,
			WorkspaceID:          wire.WorkspaceID, LineID: wire.LineID,
			ExpectedVersion:       wire.ExpectedVersion,
			ExpectedMutationEpoch: wire.ExpectedMutationEpoch,
			ExpectedParkIntentID:  wire.ExpectedParkIntentID,
			ExpectedBase:          wire.ExpectedBase, ExpectedTip: wire.ExpectedTip,
			ExpectedTree: wire.ExpectedTree, ExpectedRemoteTip: wire.ExpectedRemoteTip,
		},
	)
	if err != nil || !bytes.Equal(raw, canonical) {
		return PRDevelopmentPublicationPushRequest{}, errors.New(
			"stored publication push request is not canonical",
		)
	}
	return request, nil
}

func encodePRDevelopmentPublicationPushResult(
	result PRDevelopmentPublicationPushResult,
) (PRDevelopmentPublicationPushResult, []byte, string, error) {
	result.WorkspaceID = strings.TrimSpace(result.WorkspaceID)
	result.ParkIntentID = strings.TrimSpace(result.ParkIntentID)
	result.BaseCommit = strings.TrimSpace(result.BaseCommit)
	result.Tip = strings.TrimSpace(result.Tip)
	result.Tree = strings.TrimSpace(result.Tree)
	result.RemoteRef = strings.TrimSpace(result.RemoteRef)
	result.ExpectedRemoteTip = strings.TrimSpace(result.ExpectedRemoteTip)
	result.RemoteTip = strings.TrimSpace(result.RemoteTip)
	if !validPRDevelopmentRepairIdentity(result.WorkspaceID, 256) ||
		result.Version < 1 || result.Version > MaxPRDevelopmentControllerFences ||
		result.MutationEpoch != result.Version ||
		!validPRDevelopmentRepairIdentity(
			result.ParkIntentID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validSameWidthPRDevelopmentOIDs(
		result.BaseCommit,
		result.Tip,
		result.Tree,
		result.ExpectedRemoteTip,
		result.RemoteTip,
	) || !validPRDevelopmentPublicationPushDisposition(result.Disposition) ||
		!strings.HasPrefix(result.RemoteRef, "refs/heads/") ||
		!validPRDevelopmentGitRef(strings.TrimPrefix(result.RemoteRef, "refs/heads/")) {
		return PRDevelopmentPublicationPushResult{}, nil, "", fmt.Errorf(
			"%w: push result is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	wire := prDevelopmentPublicationPushResultWire{
		WorkspaceID: result.WorkspaceID, Version: result.Version,
		MutationEpoch: result.MutationEpoch, ParkIntentID: result.ParkIntentID,
		BaseCommit: result.BaseCommit, Tip: result.Tip, Tree: result.Tree,
		RemoteRef: result.RemoteRef, ExpectedRemoteTip: result.ExpectedRemoteTip,
		RemoteTip: result.RemoteTip, Disposition: result.Disposition,
		WorkspaceClean: result.WorkspaceClean,
	}
	raw, err := json.Marshal(wire)
	if err != nil || len(raw) > MaxPRDevelopmentPublicationResultBytes {
		return PRDevelopmentPublicationPushResult{}, nil, "", ErrInvalidPRDevelopmentPublication
	}
	return result, raw, hashPRDevelopmentPublicationBlob(
		publicationPushResultHashDomain,
		raw,
	), nil
}

func decodePRDevelopmentPublicationPushResult(
	raw []byte,
) (PRDevelopmentPublicationPushResult, error) {
	var wire prDevelopmentPublicationPushResultWire
	if err := decodeExactPRDevelopmentPublicationJSON(raw, &wire); err != nil {
		return PRDevelopmentPublicationPushResult{}, err
	}
	result, canonical, _, err := encodePRDevelopmentPublicationPushResult(
		PRDevelopmentPublicationPushResult{
			WorkspaceID: wire.WorkspaceID, Version: wire.Version,
			MutationEpoch: wire.MutationEpoch, ParkIntentID: wire.ParkIntentID,
			BaseCommit: wire.BaseCommit, Tip: wire.Tip, Tree: wire.Tree,
			RemoteRef: wire.RemoteRef, ExpectedRemoteTip: wire.ExpectedRemoteTip,
			RemoteTip: wire.RemoteTip, Disposition: wire.Disposition,
			WorkspaceClean: wire.WorkspaceClean,
		},
	)
	if err != nil || !bytes.Equal(raw, canonical) {
		return PRDevelopmentPublicationPushResult{}, errors.New(
			"stored publication push result is not canonical",
		)
	}
	return result, nil
}

func decodeExactPRDevelopmentPublicationJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidPRDevelopmentPublication
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidPRDevelopmentPublication
	}
	return nil
}

func validPRDevelopmentReviewState(state PRDevelopmentReviewState) bool {
	switch state {
	case PRDevelopmentReviewApproved,
		PRDevelopmentReviewChangesRequested,
		PRDevelopmentReviewCommented:
		return true
	default:
		return false
	}
}

func validPRDevelopmentPublicationPushDisposition(
	disposition PRDevelopmentPublicationPushDisposition,
) bool {
	switch disposition {
	case PRDevelopmentPublicationPushApplied,
		PRDevelopmentPublicationPushAlreadyCurrent,
		PRDevelopmentPublicationPushReconciled:
		return true
	default:
		return false
	}
}

func validPRDevelopmentPublicationStatus(status PRDevelopmentPublicationStatus) bool {
	switch status {
	case PRDevelopmentPublicationPending,
		PRDevelopmentPublicationClaimed,
		PRDevelopmentPublicationGateWaiting,
		PRDevelopmentPublicationPushReady,
		PRDevelopmentPublicationPushStarted,
		PRDevelopmentPublicationPublished,
		PRDevelopmentPublicationConflict,
		PRDevelopmentPublicationSuperseded,
		PRDevelopmentPublicationFailed,
		PRDevelopmentPublicationRecoveryRequired,
		PRDevelopmentPublicationOutcomeUnknown:
		return true
	default:
		return false
	}
}

func validateStoredPRDevelopmentPublication(
	publication PRDevelopmentPublication,
) error {
	if !validPRDevelopmentPublicationStatus(publication.Status) ||
		!validPrefixedHexID(publication.ID, prDevelopmentPublicationIDPrefix) ||
		!validPrefixedHexID(publication.CaseID, prDevelopmentCaseIDPrefix) ||
		!validPrefixedHexID(publication.ThreadID, prDevelopmentThreadIDPrefix) ||
		!validPrefixedHexID(publication.ControllerID, prDevelopmentControllerIDPrefix) ||
		publication.ControllerRevision < 1 ||
		publication.ControllerRevision > MaxPRDevelopmentControllerRevision ||
		!validPrefixedHexID(publication.OwnerSessionID, prDevelopmentRepairSessionIDPrefix) ||
		!validPrefixedHexID(publication.AttemptID, prDevelopmentRepairAttemptIDPrefix) ||
		publication.FenceOrdinal < 0 || publication.FenceOrdinal >= MaxPRDevelopmentControllerFences ||
		!validPRDevelopmentHex(publication.FenceHash, sha256.Size*2) ||
		!validPrefixedHexID(publication.AttemptLedgerEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		publication.AttemptLedgerEntryKind != PRDevelopmentLedgerAttempt ||
		!validPRDevelopmentHex(publication.AttemptLedgerEntryHash, sha256.Size*2) ||
		!validPrefixedHexID(publication.ReviewLedgerEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		publication.ReviewLedgerEntryKind != PRDevelopmentLedgerReview ||
		!validPRDevelopmentHex(publication.ReviewLedgerEntryHash, sha256.Size*2) ||
		publication.ReviewOutcome != PRDevelopmentLedgerReviewPassed ||
		publication.OrchestrationPhase != PRDevelopmentRepairOrchestrationCompleted ||
		!validPRDevelopmentHex(publication.OrchestrationReceiptHash, sha256.Size*2) ||
		publication.CIStatus != PRDevelopmentCIPassed ||
		!validPRDevelopmentHex(publication.CIPlanDigest, sha256.Size*2) ||
		!validPRDevelopmentHex(publication.CIResultDigest, sha256.Size*2) ||
		!validPRDevelopmentRepairIdentity(publication.WorkspaceID, 256) ||
		!validPrefixedHexID(publication.LineID, prDevelopmentLineIDPrefix) ||
		!validPRDevelopmentRepairCloneURL(publication.SourceCloneURL) ||
		!validPRDevelopmentGitRef(publication.SourceRef) ||
		publication.LineVersion < 1 ||
		publication.LineVersion > MaxPRDevelopmentControllerFences ||
		publication.MutationEpoch != publication.LineVersion ||
		publication.LineVersion != int64(publication.FenceOrdinal+1) ||
		!validPRDevelopmentRepairIdentity(
			publication.ParkIntentID,
			MaxPRDevelopmentControllerIdentityBytes,
		) || !validSameWidthPRDevelopmentOIDs(
		publication.SourceCommit,
		publication.SourceTree,
		publication.BaseCommit,
		publication.TipCommit,
		publication.Tree,
	) || publication.NoChanges != (publication.BaseCommit == publication.TipCommit) ||
		publication.AttemptLedgerEntryID == publication.ReviewLedgerEntryID ||
		publication.Claims < 0 || publication.ClaimEpoch < 0 ||
		int64(publication.Claims) != publication.ClaimEpoch || publication.Attempts < 0 ||
		validateDBTimestamp("publication availability", publication.AvailableAt) != nil ||
		validateDBTimestamp("publication creation", publication.CreatedAt) != nil ||
		validateDBTimestamp("publication update", publication.UpdatedAt) != nil ||
		publication.UpdatedAt.Before(publication.CreatedAt) ||
		publication.AvailableAt.Before(publication.CreatedAt) {
		return errors.New("stored publication immutable evidence is invalid")
	}
	for label, value := range map[string]*time.Time{
		"claim":          publication.ClaimUntil,
		"claimed":        publication.ClaimedAt,
		"provider pin":   publication.ProviderPinnedAt,
		"provider":       publication.ProviderObservedAt,
		"reconciliation": publication.ReconciliationObservedAt,
		"effect":         publication.EffectStartedAt,
		"completion":     publication.CompletedAt,
	} {
		if value != nil && validateDBTimestamp("publication "+label+" time", *value) != nil {
			return errors.New("stored publication timestamp is invalid")
		}
	}
	if publication.ClaimedAt != nil && publication.ClaimedAt.Before(publication.CreatedAt) ||
		publication.ProviderPinnedAt != nil &&
			publication.ProviderPinnedAt.Before(publication.CreatedAt) ||
		publication.ProviderObservedAt != nil && publication.ProviderPinnedAt != nil &&
			publication.ProviderObservedAt.Before(*publication.ProviderPinnedAt) ||
		publication.EffectStartedAt != nil &&
			publication.EffectStartedAt.Before(publication.CreatedAt) ||
		publication.ReconciliationObservedAt != nil &&
			(publication.EffectStartedAt == nil ||
				publication.ReconciliationObservedAt.Before(*publication.EffectStartedAt)) ||
		publication.ReconciliationObservedAt != nil &&
			(publication.CompletedAt == nil ||
				!publication.ReconciliationObservedAt.After(*publication.CompletedAt)) ||
		publication.CompletedAt != nil && publication.CompletedAt.Before(publication.CreatedAt) ||
		publication.CompletedAt != nil && publication.EffectStartedAt != nil &&
			publication.CompletedAt.Before(*publication.EffectStartedAt) {
		return errors.New("stored publication timestamp chronology is invalid")
	}
	for label, value := range map[string]*time.Time{
		"claimed":        publication.ClaimedAt,
		"provider pin":   publication.ProviderPinnedAt,
		"provider":       publication.ProviderObservedAt,
		"reconciliation": publication.ReconciliationObservedAt,
		"effect":         publication.EffectStartedAt,
		"completion":     publication.CompletedAt,
	} {
		if value != nil && value.After(publication.UpdatedAt) {
			return fmt.Errorf("stored publication %s time is after its update", label)
		}
	}

	policyPinned := publication.PolicyRevision != "" || len(publication.PinnedPolicy) != 0 ||
		publication.PinnedPolicyHash != ""
	if policyPinned {
		canonical, err := exactPRDevelopmentPublicationJSON(
			publication.PinnedPolicy,
			MaxPRDevelopmentPublicationPolicyBytes,
		)
		if err != nil || !validReviewPolicyRevision(publication.PolicyRevision) ||
			hashPRDevelopmentPublicationBlob(publicationPolicyHashDomain, canonical) !=
				publication.PinnedPolicyHash {
			return errors.New("stored publication policy pin is invalid")
		}
	}
	subjectPinned := publication.SubjectRevision != "" || len(publication.PinnedSubject) != 0 ||
		publication.PinnedSubjectHash != ""
	if subjectPinned {
		canonical, err := exactPRDevelopmentPublicationJSON(
			publication.PinnedSubject,
			MaxPRDevelopmentPublicationSubjectBytes,
		)
		if err != nil || !policyPinned || !validReviewPolicyRevision(publication.SubjectRevision) ||
			hashPRDevelopmentPublicationBlob(publicationSubjectHashDomain, canonical) !=
				publication.PinnedSubjectHash {
			return errors.New("stored publication subject pin is invalid")
		}
	}
	providerPinned := len(publication.ProviderObservationJSON) != 0 ||
		publication.ProviderObservationHash != "" || publication.ProviderPinnedAt != nil ||
		publication.ProviderObservedAt != nil
	if providerPinned {
		observation, canonical, hash, err := encodePRDevelopmentPublicationProvider(
			publication.ProviderObservation,
		)
		if err != nil || !subjectPinned ||
			!bytes.Equal(canonical, publication.ProviderObservationJSON) ||
			hash != publication.ProviderObservationHash || publication.ProviderPinnedAt == nil ||
			publication.ProviderObservedAt == nil ||
			publication.ProviderPinnedAt.After(*publication.ProviderObservedAt) ||
			observation.HeadRef != publication.SourceRef ||
			observation.HeadCloneURL != publication.SourceCloneURL {
			return errors.New("stored publication provider pin is invalid")
		}
	}
	requestPinned := publication.ExpectedRemoteTip != "" || len(publication.PushRequestJSON) != 0 ||
		publication.PushRequestHash != ""
	reconciled := len(publication.ReconciliationObservationJSON) != 0 ||
		publication.ReconciliationObservationHash != "" ||
		publication.ReconciliationObservedAt != nil
	if reconciled {
		observation, canonical, hash, err := encodePRDevelopmentPublicationRemoteObservation(
			publication.ReconciliationObservation,
		)
		if err != nil || !bytes.Equal(canonical, publication.ReconciliationObservationJSON) ||
			hash != publication.ReconciliationObservationHash ||
			publication.ReconciliationObservedAt == nil || !providerPinned ||
			observation.Repository != publication.ProviderObservation.Repository ||
			observation.PullNumber != publication.ProviderObservation.PullNumber ||
			observation.HeadRepository != publication.ProviderObservation.HeadRepository ||
			observation.HeadRef != publication.SourceRef ||
			!requestPinned || observation.HeadSHA != publication.PushRequest.ExpectedTip {
			return errors.New("stored publication reconciliation pin is invalid")
		}
	}

	if requestPinned {
		request, canonical, hash, err := encodePRDevelopmentPublicationPushRequest(
			publication.PushRequest,
		)
		if err != nil || !providerPinned || !bytes.Equal(canonical, publication.PushRequestJSON) ||
			hash != publication.PushRequestHash || request.ExpectedRemoteTip !=
			publication.ExpectedRemoteTip || request != publicationPushRequestFor(
			publication,
			publication.ExpectedRemoteTip,
		) || publication.ProviderObservation.HeadSHA != publication.ExpectedRemoteTip {
			return errors.New("stored publication push request is invalid")
		}
		if publication.EffectStartedAt == nil ||
			publication.ProviderObservedAt.After(*publication.EffectStartedAt) {
			return errors.New("stored publication provider verification is after its effect")
		}
	} else if providerPinned && !publication.ProviderPinnedAt.Equal(
		*publication.ProviderObservedAt,
	) {
		return errors.New("stored pre-effect provider verification changed from its pin")
	}
	resultPinned := len(publication.PushResultJSON) != 0 || publication.PushResultHash != "" ||
		publication.PushDisposition != "" || publication.WorkspaceClean || publication.LocalDrift
	if resultPinned {
		result, canonical, hash, err := encodePRDevelopmentPublicationPushResult(
			publication.PushResult,
		)
		if err != nil || !requestPinned || !bytes.Equal(canonical, publication.PushResultJSON) ||
			hash != publication.PushResultHash || result.Disposition != publication.PushDisposition ||
			result.WorkspaceClean != publication.WorkspaceClean ||
			(publication.LocalDrift && publication.WorkspaceClean) ||
			(!reconciled && publication.LocalDrift == publication.WorkspaceClean) ||
			(reconciled && (publication.LocalDrift || publication.WorkspaceClean)) ||
			(result.Disposition == PRDevelopmentPublicationPushApplied ||
				!reconciled && result.Disposition == PRDevelopmentPublicationPushReconciled) &&
				publication.ExpectedRemoteTip == publication.TipCommit ||
			result != publicationPushResultFor(publication, result.Disposition, result.WorkspaceClean) {
			return errors.New("stored publication push result is invalid")
		}
	}

	if publication.DecisionRunID != "" &&
		(!validPrefixedHexID(publication.DecisionRunID, "wr_") || !subjectPinned || !providerPinned) {
		return errors.New("stored publication decision run is invalid")
	}
	claimed := publication.Status == PRDevelopmentPublicationClaimed ||
		publication.Status == PRDevelopmentPublicationPushStarted
	if claimed {
		if !validPRDevelopmentPublicationClaimFrom(publication.ClaimFrom) ||
			!validPRDevelopmentRepairIdentity(publication.ClaimOwner, 256) ||
			!validPRDevelopmentRepairIdentity(publication.ClaimToken, 128) ||
			publication.ClaimUntil == nil || publication.ClaimEpoch < 1 ||
			publication.ClaimedAt == nil ||
			!publication.ClaimUntil.After(publication.UpdatedAt) {
			return errors.New("stored publication claim is invalid")
		}
	} else if publication.ClaimFrom != "" || publication.ClaimOwner != "" ||
		publication.ClaimToken != "" || publication.ClaimUntil != nil {
		return errors.New("stored publication has unexpected live claim")
	}
	if (publication.ClaimEpoch == 0) != (publication.ClaimedAt == nil) {
		return errors.New("stored publication claim history is invalid")
	}

	active := publication.Status == PRDevelopmentPublicationPending || claimed ||
		publication.Status == PRDevelopmentPublicationGateWaiting ||
		publication.Status == PRDevelopmentPublicationPushReady
	if active != (publication.CompletedAt == nil) {
		return errors.New("stored publication completion state is invalid")
	}
	effectStarted := publication.EffectStartedAt != nil
	if effectStarted != requestPinned || effectStarted != (publication.Attempts == 1) {
		return errors.New("stored publication effect state is invalid")
	}
	if !effectStarted && publication.Attempts != 0 {
		return errors.New("stored publication has attempts without an effect intent")
	}
	requiresEffect := publication.Status == PRDevelopmentPublicationPushStarted ||
		publication.Status == PRDevelopmentPublicationPublished ||
		publication.Status == PRDevelopmentPublicationOutcomeUnknown
	forbidsEffect := publication.Status == PRDevelopmentPublicationPending ||
		publication.Status == PRDevelopmentPublicationClaimed ||
		publication.Status == PRDevelopmentPublicationGateWaiting ||
		publication.Status == PRDevelopmentPublicationPushReady ||
		publication.Status == PRDevelopmentPublicationSuperseded
	if requiresEffect && !effectStarted || forbidsEffect && effectStarted {
		return errors.New("stored publication status has invalid effect shape")
	}
	if publication.Status == PRDevelopmentPublicationPushStarted &&
		publication.ClaimFrom != PRDevelopmentPublicationPushReady {
		return errors.New("stored publication push did not start from push-ready")
	}
	needsPins := publication.Status == PRDevelopmentPublicationGateWaiting ||
		publication.Status == PRDevelopmentPublicationPushReady ||
		effectStarted ||
		publication.Status == PRDevelopmentPublicationClaimed &&
			(publication.ClaimFrom == PRDevelopmentPublicationGateWaiting ||
				publication.ClaimFrom == PRDevelopmentPublicationPushReady)
	if needsPins && (!policyPinned || !subjectPinned || !providerPinned) {
		return errors.New("stored publication required pins are absent")
	}
	if publication.Status == PRDevelopmentPublicationGateWaiting && publication.DecisionRunID == "" {
		return errors.New("stored gate wait has no decision run")
	}
	if publication.Status == PRDevelopmentPublicationPublished {
		if !resultPinned || publication.LastErrorCode != "" || publication.LastErrorDetail != "" ||
			reconciled && publication.PushDisposition != PRDevelopmentPublicationPushReconciled {
			return errors.New("stored published outcome is invalid")
		}
	} else if resultPinned || reconciled {
		return errors.New("stored non-published publication has result evidence")
	}
	if terminalPRDevelopmentPublicationStatus(publication.Status) &&
		publication.Status != PRDevelopmentPublicationPublished {
		validOutcome := validPRDevelopmentPublicationPrestartOutcome(
			publication.Status,
			publication.LastErrorCode,
		)
		if effectStarted {
			validOutcome = validPRDevelopmentPublicationPoststartOutcome(
				publication.Status,
				publication.LastErrorCode,
			)
		}
		if !validOutcome {
			return errors.New("stored publication terminal error code is invalid")
		}
	} else if publication.LastErrorCode != "" || publication.LastErrorDetail != "" {
		return errors.New("stored publication has unexpected error")
	}
	return nil
}

func publicationPushRequestFor(
	publication PRDevelopmentPublication,
	expectedRemoteTip string,
) PRDevelopmentPublicationPushRequest {
	return PRDevelopmentPublicationPushRequest{
		Repository: publication.SourceCloneURL, SourceRef: publication.SourceRef,
		ExpectedSourceCommit: publication.SourceCommit, WorkspaceID: publication.WorkspaceID,
		LineID: publication.LineID, ExpectedVersion: publication.LineVersion,
		ExpectedMutationEpoch: publication.MutationEpoch,
		ExpectedParkIntentID:  publication.ParkIntentID, ExpectedBase: publication.BaseCommit,
		ExpectedTip: publication.TipCommit, ExpectedTree: publication.Tree,
		ExpectedRemoteTip: expectedRemoteTip,
	}
}

func publicationPushResultFor(
	publication PRDevelopmentPublication,
	disposition PRDevelopmentPublicationPushDisposition,
	workspaceClean bool,
) PRDevelopmentPublicationPushResult {
	return PRDevelopmentPublicationPushResult{
		WorkspaceID: publication.WorkspaceID, Version: publication.LineVersion,
		MutationEpoch: publication.MutationEpoch, ParkIntentID: publication.ParkIntentID,
		BaseCommit: publication.BaseCommit, Tip: publication.TipCommit, Tree: publication.Tree,
		RemoteRef:         "refs/heads/" + publication.SourceRef,
		ExpectedRemoteTip: publication.ExpectedRemoteTip, RemoteTip: publication.TipCommit,
		Disposition: disposition, WorkspaceClean: workspaceClean,
	}
}

// ClaimPRDevelopmentPublications leases bounded pre-effect work. Expired
// claimed rows are safely returned to their ClaimFrom phase. It deliberately
// has no authority to inspect or expire PushStarted effects.
func (s *Store) ClaimPRDevelopmentPublications(
	ctx context.Context,
	input PRDevelopmentPublicationClaimRequest,
) ([]PRDevelopmentPublication, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	worker, err := normalizePRDevelopmentPublicationIdentity(
		"publication worker label",
		input.WorkerLabel,
		256,
	)
	if err != nil || input.Lease <= 0 || input.Lease > maxPRDevelopmentPublicationLease {
		return nil, fmt.Errorf(
			"%w: publication claim is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 1
	}
	if limit > maxPRDevelopmentPublicationClaimLimit {
		limit = maxPRDevelopmentPublicationClaimLimit
	}
	claimed := make([]PRDevelopmentPublication, 0, limit)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		deadline := now.Add(input.Lease)
		if validateDBTimestamp("publication claim deadline", deadline) != nil ||
			!deadline.After(now) {
			return ErrTimestampOutOfRange
		}
		reclaimIDs, reclaimQueryErr := queryIDs(ctx, conn, `
			SELECT id
			FROM pr_development_publications
			WHERE status = 'claimed' AND claim_until <= ?
			ORDER BY claim_until ASC, claimed_at ASC, id ASC
			LIMIT ?`,
			toDBTime(now),
			limit,
		)
		if reclaimQueryErr != nil {
			return reclaimQueryErr
		}
		for _, reclaimID := range reclaimIDs {
			if _, reclaimErr := conn.ExecContext(ctx, `
				UPDATE pr_development_publications
				SET status = claim_from, claim_from = '', claim_owner = '',
					claim_token = '', claim_until = NULL, updated_at = ?
				WHERE id = ? AND status = 'claimed' AND claim_until <= ?`,
				toDBTime(now),
				reclaimID,
				toDBTime(now),
			); reclaimErr != nil {
				return reclaimErr
			}
		}
		ids, queryErr := queryIDs(ctx, conn, `
			SELECT id
			FROM pr_development_publications
			WHERE status IN ('pending', 'gate_waiting', 'push_ready')
				AND available_at <= ? AND updated_at <= ?
			ORDER BY available_at ASC, created_at ASC, id ASC
			LIMIT ?`,
			toDBTime(now),
			toDBTime(now),
			limit,
		)
		if queryErr != nil {
			return queryErr
		}
		for _, publicationID := range ids {
			token, tokenErr := newLeaseToken(worker)
			if tokenErr != nil {
				return tokenErr
			}
			result, updateErr := conn.ExecContext(ctx, `
				UPDATE pr_development_publications
				SET claim_from = status, status = 'claimed', claim_owner = ?,
					claim_token = ?, claim_until = ?, claim_epoch = claim_epoch + 1,
					claims = claims + 1, claimed_at = ?,
					updated_at = ?
				WHERE id = ? AND status IN ('pending', 'gate_waiting', 'push_ready')
					AND available_at <= ? AND updated_at <= ?`,
				worker,
				token,
				toDBTime(deadline),
				toDBTime(now),
				toDBTime(now),
				publicationID,
				toDBTime(now),
				toDBTime(now),
			)
			if updateErr != nil {
				return updateErr
			}
			rows, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				return rowsErr
			}
			if rows == 0 {
				continue
			}
			publication, loadErr := getPRDevelopmentPublicationByID(
				ctx,
				conn,
				publicationID,
			)
			if loadErr != nil {
				return loadErr
			}
			claimed = append(claimed, publication)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf(
			"claim pull request development publications: %w",
			s.dbError(err),
		)
	}
	return claimed, nil
}

// RenewPRDevelopmentPublication extends one exact live pre-effect claim.
func (s *Store) RenewPRDevelopmentPublication(
	ctx context.Context,
	input PRDevelopmentPublicationRenew,
) error {
	return s.renewPRDevelopmentPublicationClaim(
		ctx,
		input,
		PRDevelopmentPublicationClaimed,
		"renew pull request development publication",
	)
}

// RequeuePRDevelopmentPublication releases one exact live pre-effect claim to
// its database-recorded scheduling origin. It never reads mutable local
// high-water state and cannot release a started push effect.
func (s *Store) RequeuePRDevelopmentPublication(
	ctx context.Context,
	input PRDevelopmentPublicationRequeue,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	availableAt, timeErr := normalizePRDevelopmentPublicationTime(
		"publication requeue availability",
		input.AvailableAt,
	)
	if err != nil || timeErr != nil ||
		!validPRDevelopmentPublicationClaimFrom(input.ExpectedClaimFrom) {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication requeue is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	var (
		publication PRDevelopmentPublication
		requeued    bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if current.Status != PRDevelopmentPublicationClaimed {
			exactReplay := current.Status == input.ExpectedClaimFrom &&
				current.ClaimEpoch == input.ClaimEpoch &&
				current.AvailableAt.Equal(availableAt) &&
				current.ClaimFrom == "" && current.ClaimOwner == "" &&
				current.ClaimToken == "" && current.ClaimUntil == nil
			if !exactReplay {
				return fmt.Errorf(
					"%w: publication is neither claimed nor an exact requeue replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if availableAt.Before(now) {
			return fmt.Errorf(
				"%w: requeue availability predates the transition",
				ErrInvalidPRDevelopmentPublication,
			)
		}
		if current.Status != PRDevelopmentPublicationClaimed ||
			current.ClaimFrom != input.ExpectedClaimFrom {
			return fmt.Errorf(
				"%w: publication requeue origin changed",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET status = claim_from, claim_from = '', claim_owner = '',
				claim_token = '', claim_until = NULL, available_at = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed' AND claim_from = ?
				AND claim_token = ? AND claim_epoch = ? AND claim_until > ?`,
			toDBTime(availableAt),
			toDBTime(now),
			publicationID,
			input.ExpectedClaimFrom,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		requeued = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"requeue pull request development publication: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), requeued, nil
}

// RenewPRDevelopmentPublicationPush extends one exact live PushStarted claim.
// The started effect remains non-reclaimable if this lease expires.
func (s *Store) RenewPRDevelopmentPublicationPush(
	ctx context.Context,
	input PRDevelopmentPublicationRenew,
) error {
	return s.renewPRDevelopmentPublicationClaim(
		ctx,
		input,
		PRDevelopmentPublicationPushStarted,
		"renew pull request development publication push",
	)
}

func (s *Store) renewPRDevelopmentPublicationClaim(
	ctx context.Context,
	input PRDevelopmentPublicationRenew,
	requiredStatus PRDevelopmentPublicationStatus,
	operation string,
) error {
	if err := s.ready(ctx); err != nil {
		return err
	}
	if requiredStatus != PRDevelopmentPublicationClaimed &&
		requiredStatus != PRDevelopmentPublicationPushStarted {
		return fmt.Errorf(
			"%w: publication renewal phase is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	publicationID, token, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	if err != nil || input.Lease <= 0 || input.Lease > maxPRDevelopmentPublicationLease {
		return fmt.Errorf(
			"%w: publication renewal is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if timeErr := requireNonRegressingPRDevelopmentPublicationTime(
			now,
			current,
		); timeErr != nil {
			return timeErr
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			token,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != requiredStatus {
			return ErrStaleLease
		}
		deadline := now.Add(input.Lease)
		if validateDBTimestamp("publication renewal deadline", deadline) != nil ||
			!deadline.After(now) || current.ClaimUntil == nil ||
			!deadline.After(*current.ClaimUntil) {
			return fmt.Errorf(
				"%w: renewal must strictly extend the live deadline",
				ErrInvalidPRDevelopmentPublication,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET claim_until = ?, updated_at = ?
			WHERE id = ? AND status = ?
				AND claim_token = ? AND claim_epoch = ? AND claim_until > ?`,
			toDBTime(deadline),
			toDBTime(now),
			publicationID,
			requiredStatus,
			token,
			input.ClaimEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return rowsErr
		}
		if rows != 1 {
			return ErrStaleLease
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf(
			"%s: %w",
			operation,
			s.dbError(err),
		)
	}
	return nil
}

// ExpirePRDevelopmentPublicationPushes converts bounded expired PushStarted
// records to outcome_unknown. It performs no provider or Git operation.
func (s *Store) ExpirePRDevelopmentPublicationPushes(
	ctx context.Context,
	limit int,
) ([]PRDevelopmentPublication, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > maxPRDevelopmentPublicationClaimLimit {
		limit = maxPRDevelopmentPublicationClaimLimit
	}
	var expired []PRDevelopmentPublication
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		var expireErr error
		expired, expireErr = expirePRDevelopmentPublicationPushes(
			ctx,
			conn,
			now,
			limit,
		)
		return expireErr
	})
	if err != nil {
		return nil, fmt.Errorf(
			"expire pull request development publication pushes: %w",
			s.dbError(err),
		)
	}
	return expired, nil
}

func expirePRDevelopmentPublicationPushes(
	ctx context.Context,
	conn *sql.Conn,
	now time.Time,
	limit int,
) ([]PRDevelopmentPublication, error) {
	ids, err := queryIDs(ctx, conn, `
		SELECT id
		FROM pr_development_publications
		WHERE status = 'push_started' AND claim_until <= ?
		ORDER BY claim_until ASC, effect_started_at ASC, id ASC
		LIMIT ?`,
		toDBTime(now),
		limit,
	)
	if err != nil {
		return nil, err
	}
	expired := make([]PRDevelopmentPublication, 0, len(ids))
	for _, publicationID := range ids {
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET status = 'outcome_unknown', claim_from = '', claim_owner = '',
				claim_token = '', claim_until = NULL, last_error_code = ?,
				last_error_detail = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = 'push_started' AND claim_until <= ?`,
			PRDevelopmentPublicationErrorOutcomeUnknown,
			"push lease expired before an exact outcome was recorded",
			toDBTime(now),
			toDBTime(now),
			publicationID,
			toDBTime(now),
		)
		if updateErr != nil {
			return nil, updateErr
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, rowsErr
		}
		if rows == 0 {
			continue
		}
		publication, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return nil, loadErr
		}
		expired = append(expired, publication)
	}
	return expired, nil
}

type prDevelopmentPublicationUnknownOutcomeListPlan struct {
	query string
	args  []any
	limit int
}

// ListPRDevelopmentPublicationUnknownOutcomes returns one stable bounded page
// of existing outcome-unknown journal rows. It performs no claim, provider,
// Git, retry-scheduling, or reconciliation operation.
func (s *Store) ListPRDevelopmentPublicationUnknownOutcomes(
	ctx context.Context,
	filter PRDevelopmentPublicationUnknownOutcomeFilter,
) (PRDevelopmentPublicationUnknownOutcomePage, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublicationUnknownOutcomePage{}, err
	}
	plan, err := buildPRDevelopmentPublicationUnknownOutcomeListPlan(filter)
	if err != nil {
		return PRDevelopmentPublicationUnknownOutcomePage{}, err
	}
	rows, err := s.db.QueryContext(ctx, plan.query, plan.args...)
	if err != nil {
		return PRDevelopmentPublicationUnknownOutcomePage{}, fmt.Errorf(
			"list pull request development publication unknown outcomes: %w",
			s.dbError(err),
		)
	}
	defer rows.Close()

	publications := make([]PRDevelopmentPublication, 0, plan.limit+1)
	for rows.Next() {
		publication, scanErr := scanPRDevelopmentPublication(rows)
		if scanErr != nil {
			return PRDevelopmentPublicationUnknownOutcomePage{}, fmt.Errorf(
				"scan pull request development publication unknown outcome: %w",
				scanErr,
			)
		}
		if publication.Status != PRDevelopmentPublicationOutcomeUnknown ||
			publication.ClaimUntil != nil {
			return PRDevelopmentPublicationUnknownOutcomePage{}, errors.New(
				"listed pull request development publication is not an unclaimed unknown outcome",
			)
		}
		publications = append(
			publications,
			redactPRDevelopmentPublicationAuthority(publication),
		)
	}
	if err := rows.Err(); err != nil {
		return PRDevelopmentPublicationUnknownOutcomePage{}, fmt.Errorf(
			"iterate pull request development publication unknown outcomes: %w",
			s.dbError(err),
		)
	}

	page := PRDevelopmentPublicationUnknownOutcomePage{Publications: publications}
	if len(publications) > plan.limit {
		last := publications[plan.limit-1]
		page.Publications = publications[:plan.limit]
		page.Next = &PRDevelopmentPublicationUnknownOutcomeCursor{
			AvailableAt: last.AvailableAt,
			CreatedAt:   last.CreatedAt,
			ID:          last.ID,
		}
	}
	return page, nil
}

func buildPRDevelopmentPublicationUnknownOutcomeListPlan(
	filter PRDevelopmentPublicationUnknownOutcomeFilter,
) (prDevelopmentPublicationUnknownOutcomeListPlan, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 1
	}
	if limit > maxPRDevelopmentPublicationClaimLimit {
		limit = maxPRDevelopmentPublicationClaimLimit
	}

	query := `
		SELECT ` + prDevelopmentPublicationColumns + `
		FROM pr_development_publications
			INDEXED BY pr_development_publications_claimable
		WHERE status = 'outcome_unknown' AND claim_until IS NULL`
	args := make([]any, 0, 7)
	if filter.After != nil {
		cursor := *filter.After
		if !canonicalPRDevelopmentPublicationUnknownOutcomeCursorTime(cursor.AvailableAt) ||
			!canonicalPRDevelopmentPublicationUnknownOutcomeCursorTime(cursor.CreatedAt) ||
			cursor.AvailableAt.Before(cursor.CreatedAt) ||
			!validPrefixedHexID(cursor.ID, prDevelopmentPublicationIDPrefix) {
			return prDevelopmentPublicationUnknownOutcomeListPlan{}, fmt.Errorf(
				"%w: publication unknown-outcome cursor is invalid",
				ErrInvalidPRDevelopmentPublication,
			)
		}
		availableAt := toDBTime(cursor.AvailableAt)
		createdAt := toDBTime(cursor.CreatedAt)
		query += ` AND (
			available_at > ? OR
			(available_at = ? AND created_at > ?) OR
			(available_at = ? AND created_at = ? AND id > ?)
		)`
		args = append(
			args,
			availableAt,
			availableAt,
			createdAt,
			availableAt,
			createdAt,
			cursor.ID,
		)
	}
	query += `
		ORDER BY available_at ASC, claim_until ASC, created_at ASC, id ASC
		LIMIT ?`
	args = append(args, limit+1)
	return prDevelopmentPublicationUnknownOutcomeListPlan{
		query: query,
		args:  args,
		limit: limit,
	}, nil
}

func canonicalPRDevelopmentPublicationUnknownOutcomeCursorTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	return value == time.Unix(0, value.UnixNano()).UTC()
}

func normalizePRDevelopmentPublicationIdentity(
	field, value string,
	maximum int,
) (string, error) {
	value = strings.TrimSpace(value)
	if !validPRDevelopmentRepairIdentity(value, maximum) {
		return "", fmt.Errorf(
			"%w: %s is invalid",
			ErrInvalidPRDevelopmentPublication,
			field,
		)
	}
	return value, nil
}

func normalizePRDevelopmentPublicationClaim(
	publicationID, claimToken string,
	claimEpoch int64,
) (string, string, error) {
	publicationID = strings.TrimSpace(publicationID)
	claimToken = strings.TrimSpace(claimToken)
	if !validPrefixedHexID(publicationID, prDevelopmentPublicationIDPrefix) ||
		!validPRDevelopmentRepairIdentity(claimToken, 128) || claimEpoch < 1 {
		return "", "", fmt.Errorf(
			"%w: publication claim authority is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	return publicationID, claimToken, nil
}

func requireLivePRDevelopmentPublicationClaim(
	publication PRDevelopmentPublication,
	claimToken string,
	claimEpoch int64,
	now time.Time,
) error {
	if err := requireNonRegressingPRDevelopmentPublicationTime(now, publication); err != nil {
		return err
	}
	if publication.Status != PRDevelopmentPublicationClaimed &&
		publication.Status != PRDevelopmentPublicationPushStarted ||
		publication.ClaimToken != claimToken || publication.ClaimEpoch != claimEpoch ||
		publication.ClaimUntil == nil || !publication.ClaimUntil.After(now) {
		return ErrStaleLease
	}
	return nil
}

func requireNonRegressingPRDevelopmentPublicationTime(
	now time.Time,
	publication PRDevelopmentPublication,
) error {
	if now.Before(publication.UpdatedAt) {
		return fmt.Errorf(
			"%w: store clock regressed behind publication high-water time",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	return nil
}

type prDevelopmentPublicationHighWater struct {
	Case            PRDevelopmentCase
	Thread          PRDevelopmentThread
	SelectedOrdinal int
	Session         PRDevelopmentRepairSession
	Controller      PRDevelopmentController
	Fence           PRDevelopmentAttemptReviewFence
	Orchestration   PRDevelopmentRepairOrchestration
	Ledger          PRDevelopmentLedger
	AttemptEntry    PRDevelopmentLedgerEntry
	ReviewEntry     PRDevelopmentLedgerEntry
}

// loadCurrentPRDevelopmentPublicationHighWater proves that the publication's
// local candidate is still the exact latest publishable tail. It is a pure
// database read and performs no provider, workflow, model, filesystem, or Git
// effect.
func loadCurrentPRDevelopmentPublicationHighWater(
	ctx context.Context,
	queryer rowsQueryer,
	publication PRDevelopmentPublication,
) (prDevelopmentPublicationHighWater, error) {
	storedCase, err := getPRDevelopmentCaseRecord(ctx, queryer, publication.CaseID)
	if err != nil {
		return prDevelopmentPublicationHighWater{}, err
	}
	thread, err := loadPRDevelopmentThread(ctx, queryer, publication.ThreadID)
	if err != nil {
		return prDevelopmentPublicationHighWater{}, err
	}
	if thread.Kind != PRDevelopmentThreadProvider || len(thread.Cases) == 0 ||
		thread.Cases[len(thread.Cases)-1].CaseID != publication.CaseID {
		return prDevelopmentPublicationHighWater{}, fmt.Errorf(
			"%w: a newer pull request occurrence superseded the publication",
			ErrPRDevelopmentPublicationSuperseded,
		)
	}
	selectedOrdinal := thread.Cases[len(thread.Cases)-1].Ordinal
	if selectedOrdinal < 0 || selectedOrdinal >= thread.CaseCount {
		return prDevelopmentPublicationHighWater{}, fmt.Errorf(
			"%w: publication case ordinal is invalid",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	session, err := loadPRDevelopmentRepairSessionByID(
		ctx,
		queryer,
		publication.OwnerSessionID,
	)
	if err != nil {
		return prDevelopmentPublicationHighWater{}, err
	}
	if session.CaseID != publication.CaseID || len(session.Attempts) == 0 ||
		session.Attempts[len(session.Attempts)-1].ID != publication.AttemptID ||
		session.Attempts[len(session.Attempts)-1].Status != PRDevelopmentRepairCompleted ||
		activePRDevelopmentRepairAttempt(&session) != nil {
		return prDevelopmentPublicationHighWater{}, fmt.Errorf(
			"%w: publication attempt is no longer the completed owner-session tail",
			ErrPRDevelopmentPublicationSuperseded,
		)
	}
	controller, found, err := loadPRDevelopmentControllerAggregateByID(
		ctx,
		queryer,
		publication.ControllerID,
	)
	if err != nil {
		return prDevelopmentPublicationHighWater{}, err
	}
	if !found {
		return prDevelopmentPublicationHighWater{}, ErrNotFound
	}
	if controller.ThreadID != publication.ThreadID ||
		controller.OwnerSessionID != publication.OwnerSessionID ||
		controller.Revision != publication.ControllerRevision ||
		controller.Phase != PRDevelopmentControllerReady ||
		controller.CurrentAttemptID != publication.AttemptID ||
		controller.LeaseKind != "" || controller.LeaseOwner != "" ||
		controller.LeaseToken != "" || controller.LeaseUntil != nil ||
		controller.MutationReservationKey != "" ||
		controller.WorkspaceID != publication.WorkspaceID ||
		controller.LineID != publication.LineID ||
		controller.SourceCloneURL != publication.SourceCloneURL ||
		controller.SourceRef != publication.SourceRef ||
		controller.SourceCommit != publication.SourceCommit ||
		controller.SourceTree != publication.SourceTree ||
		controller.LineVersion != publication.LineVersion ||
		controller.MutationEpoch != publication.MutationEpoch ||
		controller.TipCommit != publication.TipCommit || controller.Tree != publication.Tree ||
		controller.FenceCount != publication.FenceOrdinal+1 ||
		controller.FencesDigest != publication.FenceHash {
		return prDevelopmentPublicationHighWater{}, fmt.Errorf(
			"%w: publication controller high-water changed",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	fence, found, err := loadPRDevelopmentReviewFenceByAttempt(
		ctx,
		queryer,
		publication.AttemptID,
	)
	if err != nil {
		return prDevelopmentPublicationHighWater{}, err
	}
	if !found {
		return prDevelopmentPublicationHighWater{}, ErrNotFound
	}
	if fence.ControllerID != publication.ControllerID || fence.ThreadID != publication.ThreadID ||
		fence.LineID != publication.LineID || fence.Ordinal != publication.FenceOrdinal ||
		fence.LineVersion != publication.LineVersion ||
		fence.MutationEpoch != publication.MutationEpoch ||
		fence.ParkIntentID != publication.ParkIntentID ||
		fence.BaseCommit != publication.BaseCommit || fence.TipCommit != publication.TipCommit ||
		fence.Tree != publication.Tree || fence.NoChanges != publication.NoChanges ||
		fence.FenceHash != publication.FenceHash || fence.ReviewedAt == nil {
		return prDevelopmentPublicationHighWater{}, fmt.Errorf(
			"%w: publication review fence changed",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	orchestration, found, err := loadPRDevelopmentRepairOrchestration(
		ctx,
		queryer,
		publication.AttemptID,
	)
	if err != nil {
		return prDevelopmentPublicationHighWater{}, err
	}
	if !found || orchestration.Phase != PRDevelopmentRepairOrchestrationCompleted ||
		orchestration.Validation == nil || orchestration.AttemptID != publication.AttemptID ||
		orchestration.SessionID != publication.OwnerSessionID ||
		orchestration.CaseID != publication.CaseID ||
		orchestration.ThreadID != publication.ThreadID ||
		orchestration.ControllerID != publication.ControllerID ||
		orchestration.WorkspaceID != publication.WorkspaceID ||
		orchestration.CloneURL != publication.SourceCloneURL ||
		orchestration.HeadRef != publication.SourceRef ||
		orchestration.HeadSHA != publication.SourceCommit ||
		orchestration.SourceTree != publication.SourceTree ||
		orchestration.LedgerEntryID != publication.AttemptLedgerEntryID ||
		orchestration.Validation.ReceiptHash != publication.OrchestrationReceiptHash ||
		orchestration.Validation.CIStatus != PRDevelopmentCIPassed ||
		orchestration.Validation.CIEffectivePlanDigest != publication.CIPlanDigest ||
		orchestration.Validation.CIExecutionDigest != publication.CIResultDigest ||
		orchestration.Validation.WorkspaceID != publication.WorkspaceID ||
		orchestration.Validation.LineID != publication.LineID ||
		orchestration.Validation.MutationEpoch != publication.MutationEpoch ||
		orchestration.Validation.CandidateTree != publication.Tree ||
		orchestration.Validation.NoChanges != publication.NoChanges {
		return prDevelopmentPublicationHighWater{}, fmt.Errorf(
			"%w: publication orchestration receipt changed",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	ledger, err := loadPRDevelopmentLedgerAggregate(ctx, queryer, thread)
	if err != nil {
		return prDevelopmentPublicationHighWater{}, err
	}
	if len(ledger.Entries) < 2 {
		return prDevelopmentPublicationHighWater{}, fmt.Errorf(
			"%w: publication ledger tail disappeared",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	attemptEntry := ledger.Entries[len(ledger.Entries)-2]
	reviewEntry := ledger.Entries[len(ledger.Entries)-1]
	if attemptEntry.ID != publication.AttemptLedgerEntryID ||
		attemptEntry.Kind != publication.AttemptLedgerEntryKind ||
		attemptEntry.EntryHash != publication.AttemptLedgerEntryHash ||
		attemptEntry.AttemptID != publication.AttemptID ||
		attemptEntry.CaseID != publication.CaseID ||
		attemptEntry.CaseOrdinal != selectedOrdinal ||
		attemptEntry.FenceOrdinal != publication.FenceOrdinal ||
		attemptEntry.Commit != publication.TipCommit || attemptEntry.Tree != publication.Tree ||
		attemptEntry.NoChanges != publication.NoChanges ||
		attemptEntry.CIStatus != PRDevelopmentCIPassed ||
		attemptEntry.CIPlanDigest != publication.CIPlanDigest ||
		attemptEntry.CIResultDigest != publication.CIResultDigest ||
		reviewEntry.ID != publication.ReviewLedgerEntryID ||
		reviewEntry.Kind != publication.ReviewLedgerEntryKind ||
		reviewEntry.EntryHash != publication.ReviewLedgerEntryHash ||
		reviewEntry.AttemptID != publication.AttemptID ||
		reviewEntry.CaseID != publication.CaseID ||
		reviewEntry.CaseOrdinal != selectedOrdinal ||
		reviewEntry.FenceOrdinal != publication.FenceOrdinal ||
		reviewEntry.ReviewOutcome != PRDevelopmentLedgerReviewPassed ||
		len(reviewEntry.Findings) != 0 || reviewEntry.FenceHash != publication.FenceHash ||
		reviewEntry.Ordinal != attemptEntry.Ordinal+1 ||
		reviewEntry.PreviousHash != attemptEntry.EntryHash {
		return prDevelopmentPublicationHighWater{}, fmt.Errorf(
			"%w: publication is no longer the exact passed ledger tail",
			ErrPRDevelopmentPublicationSuperseded,
		)
	}
	return prDevelopmentPublicationHighWater{
		Case: storedCase.Case, Thread: thread, SelectedOrdinal: selectedOrdinal,
		Session:    session,
		Controller: controller, Fence: fence, Orchestration: orchestration,
		Ledger: ledger, AttemptEntry: attemptEntry, ReviewEntry: reviewEntry,
	}, nil
}

func loadCurrentPRDevelopmentPublicationGateContext(
	ctx context.Context,
	queryer rowsQueryer,
	publication PRDevelopmentPublication,
) (PRDevelopmentPublicationGateContextSnapshot, error) {
	highWater, err := loadCurrentPRDevelopmentPublicationHighWater(
		ctx,
		queryer,
		publication,
	)
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	conversation, err := loadPRDevelopmentConversation(
		ctx,
		queryer,
		publication.CaseID,
	)
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	return PRDevelopmentPublicationGateContextSnapshot{
		Publication:      redactPRDevelopmentPublicationAuthority(publication),
		SelectedOrdinal:  highWater.SelectedOrdinal,
		TranscriptDigest: conversation.TranscriptDigest,
		Case:             highWater.Case,
		Thread:           highWater.Thread,
		Conversation:     conversation.Conversation,
		OwnerSession:     redactPRDevelopmentPublicationGateSession(highWater.Session),
		Controller:       highWater.Controller,
		Fence:            highWater.Fence,
		Orchestration:    highWater.Orchestration,
		Ledger:           highWater.Ledger,
		AttemptEntry:     highWater.AttemptEntry,
		ReviewEntry:      highWater.ReviewEntry,
	}, nil
}

func loadCurrentPRDevelopmentPublicationGateContextAtConversation(
	ctx context.Context,
	queryer rowsQueryer,
	publication PRDevelopmentPublication,
	anchor PRDevelopmentPublicationGateContextAnchor,
) (PRDevelopmentPublicationGateContextSnapshot, error) {
	snapshot, err := loadCurrentPRDevelopmentPublicationGateContext(
		ctx,
		queryer,
		publication,
	)
	if err != nil {
		return PRDevelopmentPublicationGateContextSnapshot{}, err
	}
	if snapshot.Conversation.Version < anchor.ConversationVersion ||
		len(snapshot.Conversation.Messages) < int(anchor.ConversationVersion) {
		return PRDevelopmentPublicationGateContextSnapshot{}, fmt.Errorf(
			"%w: publication gate conversation prefix is unavailable",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	prefixDigest := emptyPRDevelopmentTranscriptDigest()
	for _, message := range snapshot.Conversation.Messages[:anchor.ConversationVersion] {
		prefixDigest, err = extendPRDevelopmentTranscriptDigest(prefixDigest, message)
		if err != nil {
			return PRDevelopmentPublicationGateContextSnapshot{}, err
		}
	}
	if prefixDigest != anchor.TranscriptDigest {
		return PRDevelopmentPublicationGateContextSnapshot{}, fmt.Errorf(
			"%w: publication gate conversation prefix changed",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	messages := make([]PRDevelopmentMessage, int(anchor.ConversationVersion))
	copy(messages, snapshot.Conversation.Messages[:anchor.ConversationVersion])
	snapshot.Conversation.Messages = messages
	snapshot.Conversation.Version = anchor.ConversationVersion
	snapshot.TranscriptDigest = prefixDigest
	return snapshot, nil
}

func redactPRDevelopmentPublicationGateSession(
	session PRDevelopmentRepairSession,
) PRDevelopmentRepairSession {
	session.ReservationKey = ""
	session.Attempts = append([]PRDevelopmentRepairAttempt(nil), session.Attempts...)
	for index := range session.Attempts {
		session.Attempts[index].IdempotencyKey = ""
		session.Attempts[index].LeaseOwner = ""
		session.Attempts[index].LeaseToken = ""
		session.Attempts[index].LeaseUntil = nil
	}
	return session
}

func validatePRDevelopmentPublicationProviderAgainstHighWater(
	publication PRDevelopmentPublication,
	observation PRDevelopmentPublicationProviderObservation,
	highWater prDevelopmentPublicationHighWater,
) error {
	developmentCase := highWater.Case
	if observation.Repository != developmentCase.Repository ||
		observation.PullNumber != developmentCase.PullNumber ||
		observation.HeadRepository != developmentCase.HeadRepository ||
		observation.HeadRef != developmentCase.HeadRef ||
		observation.HeadRepository != highWater.Session.HeadRepository ||
		observation.HeadRef != highWater.Session.HeadRef ||
		observation.HeadSHA != highWater.Session.HeadSHA ||
		observation.HeadCloneURL != highWater.Session.CloneURL ||
		observation.CurrentReviewState != developmentCase.CurrentReviewState ||
		observation.ReviewDigest != highWater.Session.ReviewDigest ||
		observation.ReviewDigest != highWater.Orchestration.ReviewDigest ||
		observation.HeadRef != publication.SourceRef ||
		observation.HeadSHA != publication.SourceCommit ||
		observation.HeadCloneURL != publication.SourceCloneURL {
		return fmt.Errorf(
			"%w: provider observation changed from immutable publication evidence",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	return nil
}

// PinPRDevelopmentPublicationPolicy freezes one exact canonical prepared
// policy under an initial pre-effect claim.
func (s *Store) PinPRDevelopmentPublicationPolicy(
	ctx context.Context,
	input PRDevelopmentPublicationPolicyPin,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	input.PolicyRevision = strings.TrimSpace(input.PolicyRevision)
	canonical, canonicalErr := exactPRDevelopmentPublicationJSON(
		input.PinnedPolicy,
		MaxPRDevelopmentPublicationPolicyBytes,
	)
	if err != nil || canonicalErr != nil || !validReviewPolicyRevision(input.PolicyRevision) {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication policy pin is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	hash := hashPRDevelopmentPublicationBlob(publicationPolicyHashDomain, canonical)
	var (
		publication PRDevelopmentPublication
		changed     bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if current.PolicyRevision != "" {
			if current.PolicyRevision != input.PolicyRevision ||
				current.PinnedPolicyHash != hash ||
				!bytes.Equal(current.PinnedPolicy, canonical) {
				return fmt.Errorf(
					"%w: changed publication policy replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != PRDevelopmentPublicationClaimed ||
			current.ClaimFrom != PRDevelopmentPublicationPending {
			return fmt.Errorf(
				"%w: policy can only be pinned from pending",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		if _, highWaterErr := loadCurrentPRDevelopmentPublicationHighWater(
			ctx,
			conn,
			current,
		); highWaterErr != nil {
			return highWaterErr
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET policy_revision = ?, pinned_policy_json = ?,
				pinned_policy_hash = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed' AND claim_from = 'pending'
				AND claim_token = ? AND claim_epoch = ? AND claim_until > ?
				AND policy_revision = ''`,
			input.PolicyRevision,
			canonical,
			hash,
			toDBTime(now),
			publicationID,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		changed = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"pin pull request development publication policy: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), changed, nil
}

// PinPRDevelopmentPublicationSubject freezes the exact private gate subject
// after policy selection.
func (s *Store) PinPRDevelopmentPublicationSubject(
	ctx context.Context,
	input PRDevelopmentPublicationSubjectPin,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	input.PolicyRevision = strings.TrimSpace(input.PolicyRevision)
	input.SubjectRevision = strings.TrimSpace(input.SubjectRevision)
	input.ExpectedTranscriptDigest = strings.TrimSpace(input.ExpectedTranscriptDigest)
	canonical, canonicalErr := exactPRDevelopmentPublicationJSON(
		input.PinnedSubject,
		MaxPRDevelopmentPublicationSubjectBytes,
	)
	if err != nil || canonicalErr != nil || !validReviewPolicyRevision(input.PolicyRevision) ||
		!validReviewPolicyRevision(input.SubjectRevision) ||
		input.ExpectedConversationVersion < 0 ||
		input.ExpectedConversationVersion > MaxPRDevelopmentMessagesPerCase ||
		!validPRDevelopmentHex(input.ExpectedTranscriptDigest, sha256.Size*2) {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication subject pin is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	hash := hashPRDevelopmentPublicationBlob(publicationSubjectHashDomain, canonical)
	var (
		publication PRDevelopmentPublication
		changed     bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if current.SubjectRevision != "" {
			if current.PolicyRevision != input.PolicyRevision ||
				current.SubjectRevision != input.SubjectRevision ||
				current.PinnedSubjectHash != hash ||
				!bytes.Equal(current.PinnedSubject, canonical) {
				return fmt.Errorf(
					"%w: changed publication subject replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != PRDevelopmentPublicationClaimed ||
			current.ClaimFrom != PRDevelopmentPublicationPending ||
			current.PolicyRevision != input.PolicyRevision {
			return fmt.Errorf(
				"%w: subject does not match the claimed policy",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		gateContext, highWaterErr := loadCurrentPRDevelopmentPublicationGateContext(
			ctx,
			conn,
			current,
		)
		if highWaterErr != nil {
			return highWaterErr
		}
		if gateContext.Conversation.Version != input.ExpectedConversationVersion ||
			gateContext.TranscriptDigest != input.ExpectedTranscriptDigest {
			return fmt.Errorf(
				"%w: publication conversation changed before subject pin",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET subject_revision = ?, pinned_subject_json = ?,
				pinned_subject_hash = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed' AND claim_from = 'pending'
				AND claim_token = ? AND claim_epoch = ? AND claim_until > ?
				AND policy_revision = ? AND subject_revision = ''`,
			input.SubjectRevision,
			canonical,
			hash,
			toDBTime(now),
			publicationID,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
			input.PolicyRevision,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		changed = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"pin pull request development publication subject: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), changed, nil
}

// PinPRDevelopmentPublicationProvider freezes exact provider facts after the
// policy and private subject are durable.
func (s *Store) PinPRDevelopmentPublicationProvider(
	ctx context.Context,
	input PRDevelopmentPublicationProviderPin,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	observation, canonical, hash, encodeErr := encodePRDevelopmentPublicationProvider(
		input.Observation,
	)
	observedAt, timeErr := normalizePRDevelopmentPublicationTime(
		"provider observation",
		input.ObservedAt,
	)
	if err != nil || encodeErr != nil || timeErr != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication provider pin is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	var (
		publication PRDevelopmentPublication
		changed     bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if current.ProviderObservationHash != "" {
			if current.ProviderObservationHash != hash ||
				!bytes.Equal(current.ProviderObservationJSON, canonical) ||
				current.ProviderPinnedAt == nil ||
				!current.ProviderPinnedAt.Equal(observedAt) {
				return fmt.Errorf(
					"%w: changed publication provider replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if current.ClaimedAt == nil || requireFreshPRDevelopmentPublicationObservation(
			observedAt,
			now,
			*current.ClaimedAt,
		) != nil {
			return fmt.Errorf(
				"%w: provider observation is not fresh for the current claim",
				ErrInvalidPRDevelopmentPublication,
			)
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != PRDevelopmentPublicationClaimed ||
			current.ClaimFrom != PRDevelopmentPublicationPending ||
			current.PolicyRevision == "" || current.SubjectRevision == "" {
			return fmt.Errorf(
				"%w: provider pin requires policy and subject",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		highWater, highWaterErr := loadCurrentPRDevelopmentPublicationHighWater(
			ctx,
			conn,
			current,
		)
		if highWaterErr != nil {
			return highWaterErr
		}
		if providerErr := validatePRDevelopmentPublicationProviderAgainstHighWater(
			current,
			observation,
			highWater,
		); providerErr != nil {
			return providerErr
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET provider_observation_json = ?, provider_observation_hash = ?,
				provider_pinned_at = ?, provider_observed_at = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed' AND claim_from = 'pending'
				AND claim_token = ? AND claim_epoch = ? AND claim_until > ?
				AND subject_revision <> '' AND provider_observation_hash = ''`,
			canonical,
			hash,
			toDBTime(observedAt),
			toDBTime(observedAt),
			toDBTime(now),
			publicationID,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		changed = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"pin pull request development publication provider: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), changed, nil
}

func normalizePRDevelopmentPublicationTime(
	field string,
	value time.Time,
) (time.Time, error) {
	value = value.UTC()
	if err := validateDBTimestamp(field, value); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, value.UnixNano()).UTC(), nil
}

func requireFreshPRDevelopmentPublicationObservation(
	observedAt, now, causalHighWater time.Time,
) error {
	if observedAt.After(now) || !observedAt.After(causalHighWater) ||
		now.Sub(observedAt) > maxPRDevelopmentPublicationObservationAge {
		return ErrInvalidPRDevelopmentPublication
	}
	return nil
}

func requireOnePRDevelopmentPublicationRow(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf(
			"%w: publication transition lost its optimistic fence",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	return nil
}

// GetPRDevelopmentPublicationDecisionRun returns the historical exact binding
// without consulting mutable local high-water state.
func (s *Store) GetPRDevelopmentPublicationDecisionRun(
	ctx context.Context,
	key PRDevelopmentPublicationDecisionKey,
) (PRDevelopmentPublicationDecisionRunLink, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublicationDecisionRunLink{}, err
	}
	normalized, err := normalizePRDevelopmentPublicationDecisionKey(key)
	if err != nil {
		return PRDevelopmentPublicationDecisionRunLink{}, err
	}
	publication, err := getPRDevelopmentPublicationByID(ctx, s.db, normalized.PublicationID)
	if err != nil {
		return PRDevelopmentPublicationDecisionRunLink{}, s.dbError(err)
	}
	if publication.DecisionRunID == "" {
		return PRDevelopmentPublicationDecisionRunLink{}, ErrNotFound
	}
	if publicationPRDevelopmentDecisionKey(publication) != normalized {
		return PRDevelopmentPublicationDecisionRunLink{}, fmt.Errorf(
			"%w: publication decision key differs from its durable pins",
			ErrPRDevelopmentPublicationConflict,
		)
	}
	return PRDevelopmentPublicationDecisionRunLink{
		Key: normalized, RunID: publication.DecisionRunID,
	}, nil
}

// AdmitPRDevelopmentPublicationDecisionRun durably binds and invokes exactly
// one workflow creation callback under BEGIN IMMEDIATE. A historical exact
// replay returns before live-claim and mutable-high-water checks.
func (s *Store) AdmitPRDevelopmentPublicationDecisionRun(
	ctx context.Context,
	admission PRDevelopmentPublicationDecisionRunAdmission,
	create func(context.Context) error,
) (PRDevelopmentPublicationDecisionRunLink, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublicationDecisionRunLink{}, false, err
	}
	key, err := normalizePRDevelopmentPublicationDecisionKey(admission.Key)
	runID := strings.TrimSpace(admission.RunID)
	_, claimToken, claimErr := normalizePRDevelopmentPublicationClaim(
		key.PublicationID,
		admission.ClaimToken,
		admission.ClaimEpoch,
	)
	if err != nil || claimErr != nil || !validPrefixedHexID(runID, "wr_") {
		return PRDevelopmentPublicationDecisionRunLink{}, false, fmt.Errorf(
			"%w: publication decision admission is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	if create == nil {
		return PRDevelopmentPublicationDecisionRunLink{}, false, fmt.Errorf(
			"%w: workflow run create callback is required",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	var (
		link              PRDevelopmentPublicationDecisionRunLink
		existed           bool
		callbackSucceeded bool
	)
	transactionErr := s.withImmediate(ctx, func(conn *sql.Conn) error {
		publication, loadErr := getPRDevelopmentPublicationByID(ctx, conn, key.PublicationID)
		if loadErr != nil {
			return loadErr
		}
		storedKey := publicationPRDevelopmentDecisionKey(publication)
		if publication.DecisionRunID != "" {
			if storedKey != key || publication.DecisionRunID != runID {
				return fmt.Errorf(
					"%w: publication decision is already bound differently",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			link = PRDevelopmentPublicationDecisionRunLink{Key: key, RunID: runID}
			existed = true
			return nil
		}
		conflicting, findErr := getPRDevelopmentPublicationByRunID(ctx, conn, runID)
		switch {
		case findErr == nil:
			return fmt.Errorf(
				"%w: workflow run is already bound to publication %s",
				ErrPRDevelopmentPublicationConflict,
				conflicting.ID,
			)
		case !errors.Is(findErr, sql.ErrNoRows):
			return findErr
		}
		if storedKey != key || publication.PolicyRevision == "" ||
			publication.SubjectRevision == "" || publication.ProviderObservationHash == "" {
			return fmt.Errorf(
				"%w: publication decision does not match complete durable pins",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			publication,
			claimToken,
			admission.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if publication.Status != PRDevelopmentPublicationClaimed ||
			publication.ClaimFrom != PRDevelopmentPublicationPending {
			return fmt.Errorf(
				"%w: publication decision can only be admitted from pending",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		if _, highWaterErr := loadCurrentPRDevelopmentPublicationHighWater(
			ctx,
			conn,
			publication,
		); highWaterErr != nil {
			return highWaterErr
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET decision_run_id = ?, updated_at = ?
			WHERE id = ? AND decision_run_id = '' AND status = 'claimed'
				AND claim_from = 'pending' AND claim_token = ? AND claim_epoch = ?
				AND claim_until > ? AND policy_revision = ? AND subject_revision = ?
				AND provider_observation_hash = ?`,
			runID,
			toDBTime(now),
			publication.ID,
			claimToken,
			admission.ClaimEpoch,
			toDBTime(now),
			key.PolicyRevision,
			key.SubjectRevision,
			key.ProviderObservationHash,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		if callbackErr := create(ctx); callbackErr != nil {
			return callbackErr
		}
		callbackSucceeded = true
		link = PRDevelopmentPublicationDecisionRunLink{Key: key, RunID: runID}
		return nil
	})
	if transactionErr != nil {
		if callbackSucceeded {
			return PRDevelopmentPublicationDecisionRunLink{}, false, fmt.Errorf(
				"%w: %w",
				ErrPRDevelopmentPublicationAdmissionUncertain,
				s.dbError(transactionErr),
			)
		}
		return PRDevelopmentPublicationDecisionRunLink{}, false, fmt.Errorf(
			"admit pull request development publication decision: %w",
			s.dbError(transactionErr),
		)
	}
	return link, existed, nil
}

func normalizePRDevelopmentPublicationDecisionKey(
	key PRDevelopmentPublicationDecisionKey,
) (PRDevelopmentPublicationDecisionKey, error) {
	key.PublicationID = strings.TrimSpace(key.PublicationID)
	key.ReviewLedgerEntryID = strings.TrimSpace(key.ReviewLedgerEntryID)
	key.ReviewLedgerEntryHash = strings.ToLower(strings.TrimSpace(key.ReviewLedgerEntryHash))
	key.PolicyRevision = strings.TrimSpace(key.PolicyRevision)
	key.SubjectRevision = strings.TrimSpace(key.SubjectRevision)
	key.ProviderObservationHash = strings.ToLower(strings.TrimSpace(
		key.ProviderObservationHash,
	))
	if !validPrefixedHexID(key.PublicationID, prDevelopmentPublicationIDPrefix) ||
		!validPrefixedHexID(key.ReviewLedgerEntryID, prDevelopmentLedgerEntryIDPrefix) ||
		!validPRDevelopmentHex(key.ReviewLedgerEntryHash, sha256.Size*2) ||
		!validReviewPolicyRevision(key.PolicyRevision) ||
		!validReviewPolicyRevision(key.SubjectRevision) ||
		!validPRDevelopmentHex(key.ProviderObservationHash, sha256.Size*2) {
		return PRDevelopmentPublicationDecisionKey{}, fmt.Errorf(
			"%w: publication decision key is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	return key, nil
}

func publicationPRDevelopmentDecisionKey(
	publication PRDevelopmentPublication,
) PRDevelopmentPublicationDecisionKey {
	return PRDevelopmentPublicationDecisionKey{
		PublicationID:           publication.ID,
		ReviewLedgerEntryID:     publication.ReviewLedgerEntryID,
		ReviewLedgerEntryHash:   publication.ReviewLedgerEntryHash,
		PolicyRevision:          publication.PolicyRevision,
		SubjectRevision:         publication.SubjectRevision,
		ProviderObservationHash: publication.ProviderObservationHash,
	}
}

// ReleasePRDevelopmentPublicationGateWait records a durable workflow wait and
// releases scheduling authority while the retained branch remains parked.
func (s *Store) ReleasePRDevelopmentPublicationGateWait(
	ctx context.Context,
	input PRDevelopmentPublicationGateWait,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	runID := strings.TrimSpace(input.DecisionRunID)
	availableAt, timeErr := normalizePRDevelopmentPublicationTime(
		"publication gate availability",
		input.AvailableAt,
	)
	if err != nil || timeErr != nil || !validPrefixedHexID(runID, "wr_") {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication gate wait is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	var (
		publication PRDevelopmentPublication
		changed     bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if current.Status == PRDevelopmentPublicationGateWaiting {
			if current.DecisionRunID != runID || !current.AvailableAt.Equal(availableAt) {
				return fmt.Errorf(
					"%w: changed publication gate-wait replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if availableAt.Before(now) {
			return fmt.Errorf(
				"%w: gate availability predates the transition",
				ErrInvalidPRDevelopmentPublication,
			)
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != PRDevelopmentPublicationClaimed ||
			current.ClaimFrom != PRDevelopmentPublicationPending &&
				current.ClaimFrom != PRDevelopmentPublicationGateWaiting ||
			current.PolicyRevision == "" || current.SubjectRevision == "" ||
			current.ProviderObservationHash == "" || current.DecisionRunID != runID {
			return fmt.Errorf(
				"%w: gate wait is not bound to complete pins and its decision run",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		if _, highWaterErr := loadCurrentPRDevelopmentPublicationHighWater(
			ctx,
			conn,
			current,
		); highWaterErr != nil {
			return highWaterErr
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET status = 'gate_waiting', claim_from = '', claim_owner = '',
				claim_token = '', claim_until = NULL, available_at = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed'
				AND claim_from IN ('pending', 'gate_waiting')
				AND claim_token = ? AND claim_epoch = ? AND claim_until > ?
				AND decision_run_id = ?`,
			toDBTime(availableAt),
			toDBTime(now),
			publicationID,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
			runID,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		changed = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"release pull request development publication gate wait: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), changed, nil
}

// MarkPRDevelopmentPublicationPushReady records a completed gate (or zero
// gate) and releases the claim before a fresh push-start claim is acquired.
func (s *Store) MarkPRDevelopmentPublicationPushReady(
	ctx context.Context,
	input PRDevelopmentPublicationMarkPushReady,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	runID := strings.TrimSpace(input.DecisionRunID)
	if err != nil || runID != "" && !validPrefixedHexID(runID, "wr_") {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication push-ready transition is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	var (
		publication PRDevelopmentPublication
		changed     bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if current.Status == PRDevelopmentPublicationPushReady {
			if current.DecisionRunID != runID {
				return fmt.Errorf(
					"%w: changed publication push-ready replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != PRDevelopmentPublicationClaimed ||
			current.PolicyRevision == "" || current.SubjectRevision == "" ||
			current.ProviderObservationHash == "" || current.DecisionRunID != runID ||
			current.ClaimFrom != PRDevelopmentPublicationPending &&
				current.ClaimFrom != PRDevelopmentPublicationGateWaiting {
			return fmt.Errorf(
				"%w: push-ready transition differs from the durable gate",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		if current.ClaimFrom == PRDevelopmentPublicationGateWaiting && runID == "" {
			return fmt.Errorf(
				"%w: a waiting gate requires its decision run",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		if _, highWaterErr := loadCurrentPRDevelopmentPublicationHighWater(
			ctx,
			conn,
			current,
		); highWaterErr != nil {
			return highWaterErr
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET status = 'push_ready', claim_from = '', claim_owner = '',
				claim_token = '', claim_until = NULL, available_at = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed'
				AND claim_from IN ('pending', 'gate_waiting')
				AND claim_token = ? AND claim_epoch = ? AND claim_until > ?
				AND decision_run_id = ?`,
			toDBTime(now),
			toDBTime(now),
			publicationID,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
			runID,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		changed = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"mark pull request development publication push-ready: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), changed, nil
}

// CompletePRDevelopmentPublicationPrestart records a bounded terminal outcome
// while proving that no push request/effect intent was ever written.
func (s *Store) CompletePRDevelopmentPublicationPrestart(
	ctx context.Context,
	input PRDevelopmentPublicationPrestartCompletion,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	detail := strings.ReplaceAll(input.InternalError, "\x00", "\uFFFD")
	detail = s.sanitizeDetail(detail)
	if err != nil || !validPRDevelopmentPublicationPrestartOutcome(
		input.Status,
		input.ErrorCode,
	) {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication prestart completion is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	var (
		publication PRDevelopmentPublication
		changed     bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if terminalPRDevelopmentPublicationStatus(current.Status) {
			if current.PushRequestHash != "" || current.Status != input.Status ||
				current.LastErrorCode != input.ErrorCode || current.LastErrorDetail != detail {
				return fmt.Errorf(
					"%w: changed publication prestart-completion replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != PRDevelopmentPublicationClaimed || current.PushRequestHash != "" ||
			current.EffectStartedAt != nil {
			return fmt.Errorf(
				"%w: publication already crossed the effect boundary",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET status = ?, claim_from = '', claim_owner = '', claim_token = '',
				claim_until = NULL, last_error_code = ?, last_error_detail = ?,
				completed_at = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed' AND claim_token = ?
				AND claim_epoch = ? AND claim_until > ?
				AND length(push_request_json) = 0 AND effect_started_at IS NULL`,
			input.Status,
			input.ErrorCode,
			detail,
			toDBTime(now),
			toDBTime(now),
			publicationID,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		changed = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"complete pull request development publication before push: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), changed, nil
}

func validPRDevelopmentPublicationPrestartOutcome(
	status PRDevelopmentPublicationStatus,
	code PRDevelopmentPublicationErrorCode,
) bool {
	if !validPRDevelopmentPublicationErrorCode(code) {
		return false
	}
	switch status {
	case PRDevelopmentPublicationConflict:
		return code == PRDevelopmentPublicationErrorProviderChanged ||
			code == PRDevelopmentPublicationErrorLocalEvidence ||
			code == PRDevelopmentPublicationErrorPushConflict
	case PRDevelopmentPublicationSuperseded:
		return code == PRDevelopmentPublicationErrorSuperseded
	case PRDevelopmentPublicationFailed:
		return code != PRDevelopmentPublicationErrorSuperseded &&
			code != PRDevelopmentPublicationErrorRecoveryRequired &&
			code != PRDevelopmentPublicationErrorOutcomeUnknown
	case PRDevelopmentPublicationRecoveryRequired:
		return code == PRDevelopmentPublicationErrorRecoveryRequired
	default:
		return false
	}
}

// StartPRDevelopmentPublicationPush is the durable write-ahead boundary. It
// revalidates exact local high-water and a fresh repetition of the immutable
// provider pin, then persists the complete request before any Git call.
func (s *Store) StartPRDevelopmentPublicationPush(
	ctx context.Context,
	input PRDevelopmentPublicationPushStart,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	observation, observationJSON, observationHash, observationErr := encodePRDevelopmentPublicationProvider(
		input.Observation,
	)
	observedAt, observedAtErr := normalizePRDevelopmentPublicationTime(
		"publication push provider observation",
		input.ObservedAt,
	)
	request, requestJSON, requestHash, requestErr := encodePRDevelopmentPublicationPushRequest(input.Request)
	if err != nil || observationErr != nil || observedAtErr != nil || requestErr != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication push start is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	var (
		publication PRDevelopmentPublication
		started     bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		// Historical exact replay wins over current-state and claim checks. A
		// caller seeing started=false must never invoke Git again.
		if current.PushRequestHash != "" {
			if current.PushRequestHash != requestHash ||
				!bytes.Equal(current.PushRequestJSON, requestJSON) ||
				current.ProviderObservationHash != observationHash ||
				!bytes.Equal(current.ProviderObservationJSON, observationJSON) ||
				current.ProviderObservedAt == nil ||
				!current.ProviderObservedAt.Equal(observedAt) {
				return fmt.Errorf(
					"%w: changed publication push-start replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if current.ClaimedAt == nil || current.ProviderObservedAt == nil ||
			requireFreshPRDevelopmentPublicationObservation(
				observedAt,
				now,
				*current.ClaimedAt,
			) != nil || !observedAt.After(*current.ProviderObservedAt) {
			return fmt.Errorf(
				"%w: push start requires a newer provider observation",
				ErrInvalidPRDevelopmentPublication,
			)
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != PRDevelopmentPublicationClaimed ||
			current.ClaimFrom != PRDevelopmentPublicationPushReady ||
			current.PolicyRevision == "" || current.SubjectRevision == "" ||
			current.ProviderObservationHash == "" || current.Attempts != 0 {
			return fmt.Errorf(
				"%w: publication is not an unattempted push-ready claim",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		highWater, highWaterErr := loadCurrentPRDevelopmentPublicationHighWater(
			ctx,
			conn,
			current,
		)
		if highWaterErr != nil {
			return highWaterErr
		}
		if providerErr := validatePRDevelopmentPublicationProviderAgainstHighWater(
			current,
			observation,
			highWater,
		); providerErr != nil {
			return providerErr
		}
		if observationHash != current.ProviderObservationHash ||
			!bytes.Equal(observationJSON, current.ProviderObservationJSON) {
			return fmt.Errorf(
				"%w: fresh provider observation differs from its durable pin",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		expectedRequest := publicationPushRequestFor(current, observation.HeadSHA)
		if request != expectedRequest {
			return fmt.Errorf(
				"%w: push request differs from store-derived exact intent",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		result, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET status = 'push_started', provider_observed_at = ?,
				expected_remote_tip = ?, push_request_json = ?, push_request_hash = ?,
				attempts = attempts + 1, effect_started_at = ?, updated_at = ?
			WHERE id = ? AND status = 'claimed' AND claim_from = 'push_ready'
				AND claim_token = ? AND claim_epoch = ? AND claim_until > ?
				AND attempts = 0 AND length(push_request_json) = 0
				AND provider_observation_hash = ?`,
			toDBTime(observedAt),
			observation.HeadSHA,
			requestJSON,
			requestHash,
			toDBTime(now),
			toDBTime(now),
			publicationID,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
			observationHash,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(result); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		started = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"start pull request development publication push: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), started, nil
}

// FinalizePRDevelopmentPublicationPush records one exact result or bounded
// terminal post-effect failure under the still-live started claim.
func (s *Store) FinalizePRDevelopmentPublicationPush(
	ctx context.Context,
	input PRDevelopmentPublicationPushFinalize,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID, claimToken, err := normalizePRDevelopmentPublicationClaim(
		input.PublicationID,
		input.ClaimToken,
		input.ClaimEpoch,
	)
	requestHash := strings.ToLower(strings.TrimSpace(input.RequestHash))
	detail := strings.ReplaceAll(input.InternalError, "\x00", "\uFFFD")
	detail = s.sanitizeDetail(detail)
	var (
		result     PRDevelopmentPublicationPushResult
		resultJSON []byte
		resultHash string
	)
	if err != nil || !validPRDevelopmentHex(requestHash, sha256.Size*2) ||
		!validPRDevelopmentPublicationPoststartOutcome(input.Status, input.ErrorCode) {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication push finalization is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	if input.Status == PRDevelopmentPublicationPublished {
		var resultErr error
		result, resultJSON, resultHash, resultErr = encodePRDevelopmentPublicationPushResult(input.Result)
		if resultErr != nil || input.ErrorCode != "" || detail != "" ||
			input.LocalDrift == result.WorkspaceClean {
			return PRDevelopmentPublication{}, false, fmt.Errorf(
				"%w: published finalization result is invalid",
				ErrInvalidPRDevelopmentPublication,
			)
		}
	} else if input.Result != (PRDevelopmentPublicationPushResult{}) || input.LocalDrift {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: non-published finalization has result evidence",
			ErrInvalidPRDevelopmentPublication,
		)
	} else {
		resultJSON = []byte{}
	}
	var (
		publication PRDevelopmentPublication
		finalized   bool
	)
	err = s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if terminalPRDevelopmentPublicationStatus(current.Status) {
			if current.PushRequestHash != requestHash || current.Status != input.Status ||
				current.LastErrorCode != input.ErrorCode || current.LastErrorDetail != detail ||
				current.LocalDrift != input.LocalDrift ||
				!bytes.Equal(current.PushResultJSON, resultJSON) ||
				current.PushResultHash != resultHash {
				return fmt.Errorf(
					"%w: changed publication push-finalization replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if claimErr := requireLivePRDevelopmentPublicationClaim(
			current,
			claimToken,
			input.ClaimEpoch,
			now,
		); claimErr != nil {
			return claimErr
		}
		if current.Status != PRDevelopmentPublicationPushStarted ||
			current.PushRequestHash != requestHash || current.Attempts != 1 {
			return fmt.Errorf(
				"%w: finalization does not match the started push intent",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		if input.Status == PRDevelopmentPublicationPublished {
			expected := publicationPushResultFor(
				current,
				result.Disposition,
				result.WorkspaceClean,
			)
			if result != expected ||
				(result.Disposition == PRDevelopmentPublicationPushApplied ||
					result.Disposition == PRDevelopmentPublicationPushReconciled) &&
					current.ExpectedRemoteTip == current.TipCommit {
				return fmt.Errorf(
					"%w: push result differs from the durable request",
					ErrPRDevelopmentPublicationConflict,
				)
			}
		}
		resultRow, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET status = ?, claim_from = '', claim_owner = '', claim_token = '',
				claim_until = NULL, push_result_json = ?, push_result_hash = ?,
				push_disposition = ?, workspace_clean = ?, local_drift = ?,
				last_error_code = ?, last_error_detail = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = 'push_started' AND claim_token = ?
				AND claim_epoch = ? AND claim_until > ? AND push_request_hash = ?`,
			input.Status,
			resultJSON,
			resultHash,
			result.Disposition,
			boolDBValue(result.WorkspaceClean),
			boolDBValue(input.LocalDrift),
			input.ErrorCode,
			detail,
			toDBTime(now),
			toDBTime(now),
			publicationID,
			claimToken,
			input.ClaimEpoch,
			toDBTime(now),
			requestHash,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(resultRow); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		finalized = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"finalize pull request development publication push: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), finalized, nil
}

func validPRDevelopmentPublicationPoststartOutcome(
	status PRDevelopmentPublicationStatus,
	code PRDevelopmentPublicationErrorCode,
) bool {
	switch status {
	case PRDevelopmentPublicationPublished:
		return code == ""
	case PRDevelopmentPublicationConflict:
		return code == PRDevelopmentPublicationErrorProviderChanged ||
			code == PRDevelopmentPublicationErrorLocalEvidence ||
			code == PRDevelopmentPublicationErrorPushConflict
	case PRDevelopmentPublicationFailed:
		return code == PRDevelopmentPublicationErrorPushFailed ||
			code == PRDevelopmentPublicationErrorRuntimeUnavailable ||
			code == PRDevelopmentPublicationErrorInternal
	case PRDevelopmentPublicationRecoveryRequired:
		return code == PRDevelopmentPublicationErrorRecoveryRequired
	case PRDevelopmentPublicationOutcomeUnknown:
		return code == PRDevelopmentPublicationErrorOutcomeUnknown
	default:
		return false
	}
}

// ReconcilePRDevelopmentPublicationOutcome records a fresh, request-hash-bound
// head-only observation. It can prove publication, but can never reopen or
// retry the original Git effect and never overwrites the provider gate pin.
func (s *Store) ReconcilePRDevelopmentPublicationOutcome(
	ctx context.Context,
	input PRDevelopmentPublicationOutcomeReconciliation,
) (PRDevelopmentPublication, bool, error) {
	if err := s.ready(ctx); err != nil {
		return PRDevelopmentPublication{}, false, err
	}
	publicationID := strings.TrimSpace(input.PublicationID)
	requestHash := strings.ToLower(strings.TrimSpace(input.RequestHash))
	observation, observationJSON, observationHash, observationErr := encodePRDevelopmentPublicationRemoteObservation(
		input.Observation,
	)
	observedAt, observedAtErr := normalizePRDevelopmentPublicationTime(
		"publication reconciliation observation",
		input.ObservedAt,
	)
	result, resultJSON, resultHash, resultErr := encodePRDevelopmentPublicationPushResult(input.Result)
	if !validPrefixedHexID(publicationID, prDevelopmentPublicationIDPrefix) ||
		!validPRDevelopmentHex(requestHash, sha256.Size*2) || observationErr != nil ||
		observedAtErr != nil || resultErr != nil ||
		result.Disposition != PRDevelopmentPublicationPushReconciled ||
		result.WorkspaceClean {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"%w: publication outcome reconciliation is invalid",
			ErrInvalidPRDevelopmentPublication,
		)
	}
	var (
		publication PRDevelopmentPublication
		reconciled  bool
	)
	err := s.withImmediate(ctx, func(conn *sql.Conn) error {
		current, loadErr := getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		if loadErr != nil {
			return loadErr
		}
		if current.Status == PRDevelopmentPublicationPublished {
			if current.PushRequestHash != requestHash ||
				current.PushDisposition != PRDevelopmentPublicationPushReconciled ||
				current.ReconciliationObservationHash != observationHash ||
				!bytes.Equal(current.ReconciliationObservationJSON, observationJSON) ||
				current.ReconciliationObservedAt == nil ||
				!current.ReconciliationObservedAt.Equal(observedAt) ||
				current.PushResultHash != resultHash ||
				!bytes.Equal(current.PushResultJSON, resultJSON) {
				return fmt.Errorf(
					"%w: changed publication outcome-reconciliation replay",
					ErrPRDevelopmentPublicationConflict,
				)
			}
			publication = current
			return nil
		}
		if current.Status != PRDevelopmentPublicationOutcomeUnknown ||
			current.PushRequestHash != requestHash || current.EffectStartedAt == nil ||
			current.CompletedAt == nil {
			return fmt.Errorf(
				"%w: only the exact outcome-unknown request can be reconciled",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		now, clockErr := s.currentTime()
		if clockErr != nil {
			return clockErr
		}
		if timeErr := requireNonRegressingPRDevelopmentPublicationTime(
			now,
			current,
		); timeErr != nil {
			return timeErr
		}
		if current.ProviderObservedAt == nil ||
			requireFreshPRDevelopmentPublicationObservation(
				observedAt,
				now,
				*current.CompletedAt,
			) != nil || observedAt.Before(*current.ProviderObservedAt) {
			return fmt.Errorf(
				"%w: reconciliation observation is not fresh for the request",
				ErrInvalidPRDevelopmentPublication,
			)
		}
		provider := current.ProviderObservation
		if observation.Repository != provider.Repository ||
			observation.PullNumber != provider.PullNumber ||
			observation.HeadRepository != provider.HeadRepository ||
			observation.HeadRef != provider.HeadRef ||
			observation.HeadSHA != current.PushRequest.ExpectedTip {
			return fmt.Errorf(
				"%w: reconciliation observation does not prove the requested tip",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		if result != publicationPushResultFor(
			current,
			PRDevelopmentPublicationPushReconciled,
			false,
		) {
			return fmt.Errorf(
				"%w: reconciliation result differs from the durable request",
				ErrPRDevelopmentPublicationConflict,
			)
		}
		resultRow, updateErr := conn.ExecContext(ctx, `
			UPDATE pr_development_publications
			SET status = 'published', reconciliation_observation_json = ?,
				reconciliation_observation_hash = ?, reconciliation_observed_at = ?,
				push_result_json = ?, push_result_hash = ?, push_disposition = ?,
				workspace_clean = 0, local_drift = 0, last_error_code = '',
				last_error_detail = '', updated_at = ?
			WHERE id = ? AND status = 'outcome_unknown' AND push_request_hash = ?`,
			observationJSON,
			observationHash,
			toDBTime(observedAt),
			resultJSON,
			resultHash,
			PRDevelopmentPublicationPushReconciled,
			toDBTime(now),
			publicationID,
			requestHash,
		)
		if updateErr != nil {
			return updateErr
		}
		if rowErr := requireOnePRDevelopmentPublicationRow(resultRow); rowErr != nil {
			return rowErr
		}
		publication, loadErr = getPRDevelopmentPublicationByID(ctx, conn, publicationID)
		reconciled = loadErr == nil
		return loadErr
	})
	if err != nil {
		return PRDevelopmentPublication{}, false, fmt.Errorf(
			"reconcile pull request development publication outcome: %w",
			s.dbError(err),
		)
	}
	return redactPRDevelopmentPublicationAuthority(publication), reconciled, nil
}

func validPRDevelopmentPublicationClaimFrom(status PRDevelopmentPublicationStatus) bool {
	return status == PRDevelopmentPublicationPending ||
		status == PRDevelopmentPublicationGateWaiting ||
		status == PRDevelopmentPublicationPushReady
}

func terminalPRDevelopmentPublicationStatus(status PRDevelopmentPublicationStatus) bool {
	switch status {
	case PRDevelopmentPublicationPublished,
		PRDevelopmentPublicationConflict,
		PRDevelopmentPublicationSuperseded,
		PRDevelopmentPublicationFailed,
		PRDevelopmentPublicationRecoveryRequired,
		PRDevelopmentPublicationOutcomeUnknown:
		return true
	default:
		return false
	}
}

func validPRDevelopmentPublicationErrorCode(code PRDevelopmentPublicationErrorCode) bool {
	switch code {
	case PRDevelopmentPublicationErrorProviderChanged,
		PRDevelopmentPublicationErrorLocalEvidence,
		PRDevelopmentPublicationErrorGateFailed,
		PRDevelopmentPublicationErrorRuntimeUnavailable,
		PRDevelopmentPublicationErrorPushConflict,
		PRDevelopmentPublicationErrorPushFailed,
		PRDevelopmentPublicationErrorSuperseded,
		PRDevelopmentPublicationErrorRecoveryRequired,
		PRDevelopmentPublicationErrorOutcomeUnknown,
		PRDevelopmentPublicationErrorInternal:
		return true
	default:
		return false
	}
}
