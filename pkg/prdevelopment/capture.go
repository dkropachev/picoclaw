// Package prdevelopment turns an explicitly opted-in event workflow into a
// durable, provider-verified case for feedback on the configured user's own
// pull request.
package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	// WorkflowCaptureOutput is the reserved successful-workflow output that
	// opts the installed own-PR feedback workflow into durable capture.
	WorkflowCaptureOutput = "picoclawDevelopmentCapture"
	// WorkflowCaptureVersion is the only capture contract currently accepted.
	WorkflowCaptureVersion = "v1"
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	databaseIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
)

// CaseCapturer is the durable capability required by CaptureSink.
type CaseCapturer interface {
	LookupPRDevelopmentCapture(
		ctx context.Context,
		identity eventing.PRDevelopmentCaptureIdentity,
		expectedThread *eventing.PRDevelopmentThreadIdentity,
	) (eventing.PRDevelopmentCase, bool, error)
	CapturePRDevelopmentCase(
		ctx context.Context,
		input eventing.PRDevelopmentCaptureRequest,
	) (eventing.PRDevelopmentCase, bool, error)
}

// CaptureSink verifies the exact current GitHub review and pull request before
// persisting an opted-in successful event run. Workflow outputs are only an
// opt-in marker; they are never accepted as provider evidence.
type CaptureSink struct {
	Store    CaseCapturer
	Verifier *GitHubVerifier
}

// CaptureSucceededEventRun implements the successful external-event run sink.
// A workflow without the reserved output is deliberately ignored.
func (s *CaptureSink) CaptureSucceededEventRun(
	ctx context.Context,
	envelope eventing.Envelope,
	dispatch eventing.Dispatch,
	run *workflows.Run,
) error {
	if s == nil || s.Store == nil || s.Verifier == nil {
		return errors.New("PR development capture is not configured")
	}
	if run == nil {
		return errors.New("PR development capture run is required")
	}
	rawMarker, present := run.Outputs[WorkflowCaptureOutput]
	if !present {
		return nil
	}
	marker, ok := rawMarker.(string)
	if !ok || marker != WorkflowCaptureVersion {
		return errors.New("PR development capture marker is invalid")
	}
	evidence, err := captureRoutingEvidence(envelope, dispatch, run)
	if err != nil {
		return err
	}
	identity := eventing.PRDevelopmentCaptureIdentity{
		EventID:          envelope.ID,
		DispatchID:       dispatch.ID,
		RunID:            run.ID,
		WorkflowRef:      run.WorkflowRef,
		WorkflowRevision: dispatch.WorkflowRevision,
		Connector:        envelope.Connector,
	}
	if evidence.RepositoryID == "" {
		if _, exists, lookupErr := s.Store.LookupPRDevelopmentCapture(
			ctx,
			identity,
			nil,
		); lookupErr != nil {
			return fmt.Errorf("reconcile PR development capture: %w", lookupErr)
		} else if exists {
			return nil
		}
		return errors.New(
			"PR development provider IDs are required for a new capture",
		)
	}
	routingOrigin, expectedThread, err := captureExpectedThreadIdentity(evidence)
	if err != nil {
		return fmt.Errorf("derive PR development thread identity: %w", err)
	}
	if _, exists, lookupErr := s.Store.LookupPRDevelopmentCapture(
		ctx,
		identity,
		&expectedThread,
	); lookupErr != nil {
		return fmt.Errorf("reconcile PR development capture: %w", lookupErr)
	} else if exists {
		return nil
	}
	configuredOrigin, err := canonicalGitHubWebOrigin(s.Verifier.WebOrigin)
	if err != nil {
		return fmt.Errorf("GitHub verifier web origin is invalid: %w", err)
	}
	if configuredOrigin != routingOrigin {
		return errors.New(
			"GitHub verifier web origin differs from authenticated routing",
		)
	}
	verified, err := s.Verifier.Verify(ctx, evidence)
	if err != nil {
		return fmt.Errorf("verify GitHub PR feedback: %w", err)
	}
	if verified.ThreadIdentity != expectedThread {
		return errors.New(
			"verified GitHub PR thread identity differs from authenticated routing",
		)
	}
	_, _, err = s.Store.CapturePRDevelopmentCase(
		ctx,
		eventing.PRDevelopmentCaptureRequest{
			Thread: verified.ThreadIdentity,
			Case: eventing.PRDevelopmentCaptureInput{
				PRDevelopmentCaptureIdentity: identity,
				Repository:                   verified.Repository,
				PullNumber:                   int64(verified.PullNumber),
				PullURL:                      verified.PullURL,
				PullAuthor:                   verified.PullAuthor,
				TargetUser:                   evidence.TargetUser,
				PullState: eventing.PRDevelopmentPullState(
					verified.PullState,
				),
				PullDraft:           verified.PullDraft,
				PullMerged:          verified.PullMerged,
				BaseRepository:      verified.BaseRepository,
				BaseRef:             verified.BaseRef,
				BaseSHA:             verified.BaseSHA,
				HeadRepository:      verified.HeadRepository,
				HeadRef:             verified.HeadRef,
				HeadSHA:             verified.HeadSHA,
				ReviewID:            verified.ReviewID,
				TriggerReviewNodeID: evidence.ReviewNodeID,
				ReviewAuthor:        verified.ReviewAuthor,
				SubmittedReviewState: eventing.PRDevelopmentReviewState(
					evidence.ReviewState,
				),
				CurrentReviewState: eventing.PRDevelopmentReviewState(
					verified.CurrentReviewState,
				),
				ReviewCommitSHA:   verified.ReviewCommitSHA,
				ReviewSubmittedAt: verified.ReviewSubmittedAt,
				ReviewURL:         verified.ReviewURL,
				Feedback:          verified.Feedback,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("persist PR development case: %w", err)
	}
	return nil
}

// captureExpectedThreadIdentity derives the retry reconciliation key and
// canonical provider origin from authenticated routing evidence carrying the
// complete current provider-identity contract. Pre-identity legacy routing is
// reconciled by exact provenance before this stricter parser is called.
func captureExpectedThreadIdentity(
	evidence RoutingEvidence,
) (string, eventing.PRDevelopmentThreadIdentity, error) {
	pull, err := url.Parse(evidence.PullURL)
	if err != nil {
		return "", eventing.PRDevelopmentThreadIdentity{}, errors.New(
			"GitHub pull request URL is invalid",
		)
	}
	providerOrigin := pull.Scheme + "://" + pull.Host
	canonicalOrigin, err := canonicalGitHubWebOrigin(providerOrigin)
	if err != nil {
		return "", eventing.PRDevelopmentThreadIdentity{}, fmt.Errorf(
			"GitHub routing origin is invalid: %w",
			err,
		)
	}
	if canonicalOrigin != providerOrigin {
		return "", eventing.PRDevelopmentThreadIdentity{}, errors.New(
			"GitHub routing origin is not canonical",
		)
	}
	if err = verifyRoutingEvidenceURLs(evidence, providerOrigin); err != nil {
		return "", eventing.PRDevelopmentThreadIdentity{}, err
	}
	if !validRoutingThreadIdentity(evidence, providerOrigin) {
		return "", eventing.PRDevelopmentThreadIdentity{}, errors.New(
			"GitHub routing evidence thread identity is invalid",
		)
	}
	return providerOrigin, eventing.PRDevelopmentThreadIdentity{
		Provider:       "github",
		ProviderOrigin: providerOrigin,
		PullAuthorID:   evidence.PullAuthorID,
		RepositoryID:   evidence.RepositoryID,
		PullRequestID:  evidence.PullRequestID,
		PullNumber:     int64(evidence.PullNumber),
	}, nil
}

// RoutingEvidence is the bounded HMAC-authenticated routing identity that a
// provider read cross-binds to current aliases, URLs, and pull number. The
// current GitHub MCP projection independently confirms PullAuthorID, but does
// not expose RepositoryID, PullRequestID, or ReviewNodeID for another exact
// comparison. The three thread IDs are all absent only on authenticated
// records admitted before that routing contract existed.
type RoutingEvidence struct {
	Repository        string
	RepositoryID      string
	PullNumber        int
	PullRequestID     string
	PullURL           string
	PullAuthor        string
	PullAuthorID      string
	TargetUser        string
	ReviewID          string
	ReviewNodeID      string
	ReviewURL         string
	ReviewAuthor      string
	ReviewState       string
	ReviewCommitSHA   string
	ReviewSubmittedAt time.Time
}

func captureRoutingEvidence(
	envelope eventing.Envelope,
	dispatch eventing.Dispatch,
	run *workflows.Run,
) (RoutingEvidence, error) {
	if envelope.Source != "github" ||
		envelope.Type != "pull_request_review.submitted" ||
		envelope.Attributes["source_authenticated"] != "true" ||
		envelope.Attributes["body_authenticated"] != "true" ||
		envelope.Attributes["provider_authenticated"] == "true" ||
		envelope.Attributes["targets_user"] != "true" ||
		envelope.Attributes["pull_request_author_is_target"] != "true" ||
		envelope.Attributes["review_author_is_target"] != "false" ||
		!containsTargetReason(envelope.Attributes["target_reason"], "review_feedback") {
		return RoutingEvidence{}, errors.New(
			"PR development capture requires authenticated own-PR review feedback",
		)
	}
	if envelope.ID == "" ||
		dispatch.ID == "" ||
		dispatch.EventID != envelope.ID ||
		dispatch.RunID != run.ID ||
		dispatch.WorkflowRef != run.WorkflowRef {
		return RoutingEvidence{}, errors.New(
			"PR development capture event, dispatch, and run identity do not match",
		)
	}
	if run.Status != workflows.RunStatusSucceeded {
		return RoutingEvidence{}, errors.New(
			"PR development capture requires a successful workflow run",
		)
	}
	if run.WorkflowRef != workflows.GitHubPRDevelopmentWorkflowRef {
		return RoutingEvidence{}, errors.New(
			"PR development capture requires the installed own-PR workflow",
		)
	}

	repository := envelope.Attributes["repository_full_name"]
	if len(repository) > 256 || !repositoryPattern.MatchString(repository) {
		return RoutingEvidence{}, errors.New("PR development repository is invalid")
	}
	pullNumber, err := strconv.Atoi(envelope.Attributes["pull_request_number"])
	if err != nil || pullNumber <= 0 || pullNumber > 1<<31-1 {
		return RoutingEvidence{}, errors.New("PR development pull number is invalid")
	}
	pullURL := envelope.Attributes["pull_request_url"]
	if !validHTTPSURL(pullURL) {
		return RoutingEvidence{}, errors.New("PR development pull URL is invalid")
	}
	pullAuthor := envelope.Attributes["pull_request_author"]
	repositoryID := envelope.Attributes["repository_database_id"]
	pullRequestID := envelope.Attributes["pull_request_id"]
	pullAuthorID := envelope.Attributes["pull_request_author_id"]
	providerIDsPresent := repositoryID != "" || pullRequestID != "" || pullAuthorID != ""
	if providerIDsPresent &&
		(repositoryID == "" || pullRequestID == "" || pullAuthorID == "") {
		return RoutingEvidence{}, errors.New(
			"PR development provider thread IDs must be present together",
		)
	}
	if providerIDsPresent {
		if repositoryID, err = captureProviderDatabaseID(repositoryID); err != nil {
			return RoutingEvidence{}, errors.New(
				"PR development repository provider ID is invalid",
			)
		}
		if pullRequestID, err = captureProviderDatabaseID(pullRequestID); err != nil {
			return RoutingEvidence{}, errors.New(
				"PR development pull request provider ID is invalid",
			)
		}
		if pullAuthorID, err = captureProviderDatabaseID(pullAuthorID); err != nil {
			return RoutingEvidence{}, errors.New(
				"PR development pull request author provider ID is invalid",
			)
		}
	}
	targetUser := envelope.Attributes["target_user"]
	reviewAuthor := envelope.Attributes["review_author"]
	if !validGitHubUser(pullAuthor, false) ||
		!validGitHubUser(targetUser, false) ||
		!strings.EqualFold(pullAuthor, targetUser) ||
		!validGitHubUser(reviewAuthor, true) ||
		strings.EqualFold(reviewAuthor, targetUser) {
		return RoutingEvidence{}, errors.New("PR development actor identity is invalid")
	}
	reviewID := envelope.Attributes["review_id"]
	reviewNumericID, reviewIDErr := strconv.ParseInt(reviewID, 10, 64)
	if !databaseIDPattern.MatchString(reviewID) ||
		reviewIDErr != nil || reviewNumericID <= 0 ||
		strconv.FormatInt(reviewNumericID, 10) != reviewID {
		return RoutingEvidence{}, errors.New("PR development review ID is invalid")
	}
	reviewNodeID := envelope.Attributes["review_node_id"]
	if !validNodeID(reviewNodeID) {
		return RoutingEvidence{}, errors.New("PR development review node ID is invalid")
	}
	reviewURL := envelope.Attributes["review_url"]
	if !validHTTPSURLWithFragment(reviewURL) {
		return RoutingEvidence{}, errors.New("PR development review URL is invalid")
	}
	reviewState := envelope.Attributes["review_state"]
	if !validReviewState(reviewState) {
		return RoutingEvidence{}, errors.New("PR development review state is invalid")
	}
	reviewCommit := envelope.Attributes["review_commit_sha"]
	if !validObjectID(reviewCommit) {
		return RoutingEvidence{}, errors.New("PR development review commit is invalid")
	}
	submittedAt, err := time.Parse(
		time.RFC3339Nano,
		envelope.Attributes["review_submitted_at"],
	)
	if err != nil ||
		submittedAt.UTC().Format(time.RFC3339Nano) !=
			envelope.Attributes["review_submitted_at"] {
		return RoutingEvidence{}, errors.New("PR development review time is invalid")
	}
	return RoutingEvidence{
		Repository:        repository,
		RepositoryID:      repositoryID,
		PullNumber:        pullNumber,
		PullRequestID:     pullRequestID,
		PullURL:           pullURL,
		PullAuthor:        pullAuthor,
		PullAuthorID:      pullAuthorID,
		TargetUser:        targetUser,
		ReviewID:          reviewID,
		ReviewNodeID:      reviewNodeID,
		ReviewURL:         reviewURL,
		ReviewAuthor:      reviewAuthor,
		ReviewState:       reviewState,
		ReviewCommitSHA:   reviewCommit,
		ReviewSubmittedAt: submittedAt.UTC(),
	}, nil
}

func captureProviderDatabaseID(value string) (string, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if !databaseIDPattern.MatchString(value) || err != nil || parsed <= 0 ||
		strconv.FormatInt(parsed, 10) != value {
		return "", errors.New("provider database ID is invalid")
	}
	return value, nil
}

func containsTargetReason(value, target string) bool {
	for _, reason := range strings.Split(value, ",") {
		if reason == target {
			return true
		}
	}
	return false
}

func validGitHubUser(value string, allowBot bool) bool {
	if allowBot {
		if base, ok := strings.CutSuffix(value, "[bot]"); ok {
			return validGitHubUser(base, false)
		}
	}
	if value == "" || len(value) > 100 || value != strings.TrimSpace(value) ||
		!utf8.ValidString(value) {
		return false
	}
	for index, char := range []byte(value) {
		if char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' ||
			char == '-' && index > 0 && index < len(value)-1 {
			continue
		}
		return false
	}
	return true
}

func validNodeID(value string) bool {
	if value == "" || len(value) > 1024 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range []byte(value) {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '-' ||
			char == '+' || char == '/' || char == '=' {
			continue
		}
		return false
	}
	return true
}

func validReviewState(value string) bool {
	switch value {
	case "approved", "changes_requested", "commented":
		return true
	default:
		return false
	}
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range []byte(value) {
		if char >= '0' && char <= '9' || char >= 'a' && char <= 'f' {
			continue
		}
		return false
	}
	return true
}

func validHTTPSURL(value string) bool {
	return validHTTPSURLShape(value, false)
}

func validHTTPSURLWithFragment(value string) bool {
	return validHTTPSURLShape(value, true)
}

func validHTTPSURLShape(value string, allowFragment bool) bool {
	if value == "" || len(value) > 4096 || value != strings.TrimSpace(value) ||
		!utf8.ValidString(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && (allowFragment || parsed.Fragment == "")
}

var _ workflows.SucceededEventRunSink = (*CaptureSink)(nil)
