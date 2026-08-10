package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

// TimedPublicationProviderObservation is one provider-validated publication
// preflight. The local observation instant is sampled only after the complete
// pull-request and exact-review snapshot has been validated successfully.
// Neither field is suitable for a browser or model projection.
type TimedPublicationProviderObservation struct {
	Observation eventing.PRDevelopmentPublicationProviderObservation `json:"-"`
	ObservedAt  time.Time                                            `json:"-"`
}

// TimedPublicationRemoteObservation is the minimal current source-branch
// observation used to reconcile an outcome-unknown publication. It contains
// no review, clone endpoint, credential, tool, or provider-write authority.
type TimedPublicationRemoteObservation struct {
	Observation eventing.PRDevelopmentPublicationRemoteObservation `json:"-"`
	ObservedAt  time.Time                                          `json:"-"`
}

// PublicationProviderObserver owns only a read-only, review-bearing provider
// preflight. Callers repeat this read before publication start; the durable
// store decides whether its facts are identical to the original pin.
type PublicationProviderObserver interface {
	ObservePublication(
		ctx context.Context,
		stored eventing.PRDevelopmentCase,
		expected eventing.PRDevelopmentThreadIdentity,
	) (TimedPublicationProviderObservation, error)
}

// PublicationRemoteHeadObserver owns only a read-only pull-request head read.
// It deliberately cannot inspect the mutable review occurrence or invoke a
// provider or Git write while reconciling an outcome-unknown publication.
type PublicationRemoteHeadObserver interface {
	ObservePublicationRemoteHead(
		ctx context.Context,
		stored eventing.PRDevelopmentCase,
		expected eventing.PRDevelopmentThreadIdentity,
	) (TimedPublicationRemoteObservation, error)
}

// The two adapters are deliberately distinct, unexported concrete types. A
// worker receiving the head-only interface cannot type-assert it into the
// review-bearing capability or recover the generic workflow runner.
type githubPublicationProviderObserver struct {
	verifier *GitHubVerifier
	now      func() time.Time
}

type githubPublicationRemoteHeadObserver struct {
	verifier *GitHubVerifier
	now      func() time.Time
}

var (
	_ PublicationProviderObserver   = (*githubPublicationProviderObserver)(nil)
	_ PublicationRemoteHeadObserver = (*githubPublicationRemoteHeadObserver)(nil)
)

// NewGitHubPublicationProviderObserver confines the generic workflow runner
// behind only the exact review-bearing publication preflight capability. The
// verifier configuration is copied so later caller mutation cannot widen or
// redirect an already-issued observer.
func NewGitHubPublicationProviderObserver(
	verifier *GitHubVerifier,
) PublicationProviderObserver {
	return &githubPublicationProviderObserver{verifier: copyGitHubVerifier(verifier)}
}

// NewGitHubPublicationRemoteHeadObserver confines the generic workflow runner
// behind only the exact review-independent current-head capability.
func NewGitHubPublicationRemoteHeadObserver(
	verifier *GitHubVerifier,
) PublicationRemoteHeadObserver {
	return &githubPublicationRemoteHeadObserver{verifier: copyGitHubVerifier(verifier)}
}

// ObservePublication independently verifies the current pull request and the
// exact captured review, then projects only the schema-v18 provider pin.
func (observer *githubPublicationProviderObserver) ObservePublication(
	ctx context.Context,
	stored eventing.PRDevelopmentCase,
	expected eventing.PRDevelopmentThreadIdentity,
) (TimedPublicationProviderObservation, error) {
	var verifierInput *GitHubVerifier
	if observer != nil {
		verifierInput = observer.verifier
	}
	verifier, err := publicationVerifier(ctx, verifierInput)
	if err != nil {
		return TimedPublicationProviderObservation{}, err
	}
	verified, err := verifier.VerifyCase(ctx, stored, &expected)
	if err != nil {
		return TimedPublicationProviderObservation{}, err
	}
	if !validStoredGitRef(verified.HeadRef) {
		return TimedPublicationProviderObservation{}, errors.New(
			"GitHub publication head ref is invalid",
		)
	}
	observedAt, err := publicationObservedAt(ctx, observer.now)
	if err != nil {
		return TimedPublicationProviderObservation{}, err
	}
	return TimedPublicationProviderObservation{
		Observation: eventing.PRDevelopmentPublicationProviderObservation{
			Repository:         verified.Repository,
			PullNumber:         verified.PullNumber,
			HeadRepository:     verified.HeadRepository,
			HeadRef:            verified.HeadRef,
			HeadSHA:            verified.HeadSHA,
			HeadCloneURL:       verified.HeadCloneURL,
			CurrentReviewState: verified.CurrentReviewState,
			ReviewDigest:       verified.ReviewDigest,
		},
		ObservedAt: observedAt,
	}, nil
}

// ObservePublicationRemoteHead performs exactly one pull_request_read/get and
// projects only the current source-branch identity. It intentionally does not
// call VerifyCase: a changed or dismissed review and a closed or merged pull
// request must not erase evidence that an earlier push reached its desired tip.
func (observer *githubPublicationRemoteHeadObserver) ObservePublicationRemoteHead(
	ctx context.Context,
	stored eventing.PRDevelopmentCase,
	expected eventing.PRDevelopmentThreadIdentity,
) (TimedPublicationRemoteObservation, error) {
	var verifierInput *GitHubVerifier
	if observer != nil {
		verifierInput = observer.verifier
	}
	verifier, err := publicationVerifier(ctx, verifierInput)
	if err != nil {
		return TimedPublicationRemoteObservation{}, err
	}
	evidence, err := routingEvidenceForStoredCase(stored, &expected)
	if err != nil {
		return TimedPublicationRemoteObservation{}, err
	}
	webOrigin, err := publicationProviderOrigin(verifier, expected)
	if err != nil {
		return TimedPublicationRemoteObservation{}, err
	}
	if err = verifyRoutingEvidenceURLs(evidence, webOrigin); err != nil {
		if errors.Is(err, errGitHubProviderEvidenceMismatch) {
			return TimedPublicationRemoteObservation{}, fmt.Errorf(
				"%w: %v",
				ErrGitHubCaseDrift,
				err,
			)
		}
		return TimedPublicationRemoteObservation{}, err
	}
	owner, repository, found := strings.Cut(evidence.Repository, "/")
	if !found || owner == "" || repository == "" {
		return TimedPublicationRemoteObservation{}, errors.New(
			"GitHub publication repository is invalid",
		)
	}
	server := strings.TrimSpace(verifier.Server)
	if server == "" {
		server = defaultGitHubMCPServer
	}
	raw, err := verifier.read(
		ctx,
		server,
		map[string]any{
			"method":     "get",
			"owner":      owner,
			"repo":       repository,
			"pullNumber": evidence.PullNumber,
		},
		maxProviderTextBytes,
	)
	if err != nil {
		return TimedPublicationRemoteObservation{}, fmt.Errorf(
			"read current GitHub publication head: %w",
			err,
		)
	}
	var pull providerPullRequest
	if err = decodeProviderJSON(raw, &pull); err != nil {
		return TimedPublicationRemoteObservation{}, fmt.Errorf(
			"decode current GitHub publication head: %w",
			err,
		)
	}
	verified, err := verifyProviderPullRequest(evidence, pull, true)
	if err != nil {
		if errors.Is(err, errGitHubProviderEvidenceMismatch) {
			return TimedPublicationRemoteObservation{}, fmt.Errorf(
				"%w: %v",
				ErrGitHubCaseDrift,
				err,
			)
		}
		return TimedPublicationRemoteObservation{}, err
	}
	if !validStoredGitRef(verified.HeadRef) {
		return TimedPublicationRemoteObservation{}, errors.New(
			"GitHub publication head ref is invalid",
		)
	}
	observedAt, err := publicationObservedAt(ctx, observer.now)
	if err != nil {
		return TimedPublicationRemoteObservation{}, err
	}
	return TimedPublicationRemoteObservation{
		Observation: eventing.PRDevelopmentPublicationRemoteObservation{
			Repository:     verified.Repository,
			PullNumber:     int64(verified.PullNumber),
			HeadRepository: verified.HeadRepository,
			HeadRef:        verified.HeadRef,
			HeadSHA:        verified.HeadSHA,
		},
		ObservedAt: observedAt,
	}, nil
}

func publicationVerifier(
	ctx context.Context,
	verifier *GitHubVerifier,
) (*GitHubVerifier, error) {
	if verifier == nil || verifier.Runner == nil {
		return nil, errors.New("GitHub publication observer is not configured")
	}
	if ctx == nil {
		return nil, errors.New("GitHub publication observer context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return verifier, nil
}

func publicationObservedAt(
	ctx context.Context,
	configuredNow func() time.Time,
) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	now := time.Now
	if configuredNow != nil {
		now = configuredNow
	}
	observedAt := now().UTC()
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	encoded := observedAt.UnixNano()
	canonical := time.Unix(0, encoded).UTC()
	if observedAt.IsZero() || !observedAt.Equal(canonical) {
		return time.Time{}, errors.New(
			"GitHub publication observation clock is out of range",
		)
	}
	return canonical, nil
}

func copyGitHubVerifier(verifier *GitHubVerifier) *GitHubVerifier {
	if verifier == nil {
		return nil
	}
	cloned := *verifier
	return &cloned
}

func publicationProviderOrigin(
	verifier *GitHubVerifier,
	expected eventing.PRDevelopmentThreadIdentity,
) (string, error) {
	webOrigin := expected.ProviderOrigin
	if verifier.WebOrigin == "" {
		return webOrigin, nil
	}
	configured, err := canonicalGitHubWebOrigin(verifier.WebOrigin)
	if err != nil {
		return "", fmt.Errorf("GitHub verifier web origin is invalid: %w", err)
	}
	if configured != webOrigin {
		return "", fmt.Errorf(
			"%w: provider origin does not match the configured GitHub origin",
			ErrGitHubCaseDrift,
		)
	}
	return webOrigin, nil
}
