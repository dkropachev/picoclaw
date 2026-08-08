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
	"hash"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	defaultGitHubMCPServer = "github"
	defaultGitHubWebOrigin = "https://github.com"
	pullRequestReadTool    = "pull_request_read"

	providerReviewsPerPage = 100
	maxProviderReviewPages = 5
	maxProviderReviewItems = providerReviewsPerPage * maxProviderReviewPages
	// The probe uses perPage=1, so page 501 starts at the first item after the
	// five accepted 100-item pages.
	providerOverflowProbePage = maxProviderReviewItems + 1
	maxProviderJSONBytes      = 16 << 20
	maxProviderTotalBytes     = 32 << 20
	maxProviderTextBytes      = 1 << 20
	maxProviderJSONDepth      = 64
	maxProviderJSONTokens     = 1 << 20
	maxProviderReviewBody     = 64 << 10
	maxProviderRefBytes       = 1024

	providerReviewDigestDomain = "picoclaw-pr-development-review-v1"
)

var (
	errGitHubProviderEvidenceMismatch = errors.New(
		"GitHub provider identity or evidence does not match",
	)

	// ErrGitHubCaseNotActionable reports a durable case whose current pull
	// request or exact review can no longer accept development work.
	ErrGitHubCaseNotActionable = errors.New(
		"GitHub pull request development case is not actionable",
	)
	// ErrGitHubCaseDrift reports a durable case whose immutable provider
	// identity or captured review evidence no longer matches GitHub.
	ErrGitHubCaseDrift = errors.New(
		"GitHub pull request development case changed",
	)
)

// GitHubVerifier independently re-reads the current PR and exact review via
// the configured, generation-fenced workflow MCP runner.
type GitHubVerifier struct {
	Runner       workflows.ToolRunner
	Server       string
	ArtifactRoot string
	// WebOrigin is the canonical GitHub web origin used by VerifyCase to bind
	// pull, review, and credential-free clone URLs. Verify retains the capture
	// contract's existing provider checks. An empty value defaults to github.com;
	// GitHub Enterprise development callers must provide their exact HTTPS origin.
	WebOrigin string
}

// VerifiedFeedback is the bounded provider snapshot admitted to durable
// capture. It contains no checkout, credential, or provider-write capability.
type VerifiedFeedback struct {
	Repository         string
	PullNumber         int
	PullURL            string
	PullAuthor         string
	PullState          string
	PullDraft          bool
	PullMerged         bool
	BaseRepository     string
	BaseRef            string
	BaseSHA            string
	HeadRepository     string
	HeadRef            string
	HeadSHA            string
	ReviewID           string
	ReviewAuthor       string
	CurrentReviewState string
	ReviewCommitSHA    string
	ReviewSubmittedAt  time.Time
	ReviewURL          string
	Feedback           string

	// headCloneURL is retained only for the trusted VerifyCase controller.
	// The durable capture path deliberately does not persist or expose it.
	headCloneURL string
}

// VerifiedCase is the bounded current provider observation a trusted local
// development controller may pass to the exact pinned-checkout boundary. It
// contains no credential, filesystem path, model, provider-write, or browser
// capability.
type VerifiedCase struct {
	CaseID             string
	Repository         string
	PullNumber         int64
	HeadRepository     string
	HeadRef            string
	HeadSHA            string
	HeadCloneURL       string
	CurrentReviewState eventing.PRDevelopmentReviewState
	ReviewDigest       string
}

type providerPullRequest struct {
	Number  int                     `json:"number"`
	State   string                  `json:"state"`
	Draft   *bool                   `json:"draft"`
	Merged  *bool                   `json:"merged"`
	HTMLURL string                  `json:"html_url"`
	User    *providerUser           `json:"user"`
	Head    *providerPullRequestRef `json:"head"`
	Base    *providerPullRequestRef `json:"base"`
}

type providerPullRequestRef struct {
	Ref  string                   `json:"ref"`
	SHA  string                   `json:"sha"`
	Repo *providerPullRequestRepo `json:"repo"`
}

type providerPullRequestRepo struct {
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

type providerUser struct {
	Login string `json:"login"`
}

type providerReview struct {
	ID          json.Number   `json:"id"`
	State       string        `json:"state"`
	Body        string        `json:"body"`
	HTMLURL     string        `json:"html_url"`
	User        *providerUser `json:"user"`
	CommitID    string        `json:"commit_id"`
	SubmittedAt string        `json:"submitted_at"`
}

// Verify binds the signed routing identity to current provider facts. The
// review list is paginated under fixed limits and selected by its canonical
// database ID before any body is admitted.
func (v *GitHubVerifier) Verify(
	ctx context.Context,
	evidence RoutingEvidence,
) (VerifiedFeedback, error) {
	if v == nil || v.Runner == nil {
		return VerifiedFeedback{}, errors.New("GitHub verifier runner is required")
	}
	if ctx == nil {
		return VerifiedFeedback{}, errors.New("GitHub verifier context is required")
	}
	if err := ctx.Err(); err != nil {
		return VerifiedFeedback{}, err
	}
	owner, repo, ok := strings.Cut(evidence.Repository, "/")
	if !ok || owner == "" || repo == "" {
		return VerifiedFeedback{}, errors.New("GitHub verifier repository is invalid")
	}
	server := strings.TrimSpace(v.Server)
	if server == "" {
		server = defaultGitHubMCPServer
	}

	pullRaw, err := v.read(
		ctx,
		server,
		map[string]any{
			"method":     "get",
			"owner":      owner,
			"repo":       repo,
			"pullNumber": evidence.PullNumber,
		},
		maxProviderTextBytes,
	)
	if err != nil {
		return VerifiedFeedback{}, fmt.Errorf("read pull request: %w", err)
	}
	var pull providerPullRequest
	if decodeErr := decodeProviderJSON(pullRaw, &pull); decodeErr != nil {
		return VerifiedFeedback{}, fmt.Errorf("decode pull request: %w", decodeErr)
	}
	verified, err := verifyProviderPullRequest(evidence, pull)
	if err != nil {
		return VerifiedFeedback{}, err
	}

	var (
		matched       *providerReview
		totalItems    int
		totalBytes    = len(pullRaw)
		scanCompleted bool
	)
	for page := 1; page <= maxProviderReviewPages; page++ {
		if err := ctx.Err(); err != nil {
			return VerifiedFeedback{}, err
		}
		pageLimit, limitErr := remainingProviderReadLimit(totalBytes)
		if limitErr != nil {
			return VerifiedFeedback{}, limitErr
		}
		raw, readErr := v.read(
			ctx,
			server,
			map[string]any{
				"method":     "get_reviews",
				"owner":      owner,
				"repo":       repo,
				"pullNumber": evidence.PullNumber,
				"page":       page,
				"perPage":    providerReviewsPerPage,
			},
			pageLimit,
		)
		if readErr != nil {
			return VerifiedFeedback{}, fmt.Errorf("read reviews page %d: %w", page, readErr)
		}
		totalBytes += len(raw)
		if totalBytes > maxProviderTotalBytes {
			return VerifiedFeedback{}, errors.New("GitHub provider snapshot exceeds the total limit")
		}
		var reviews []providerReview
		if decodeErr := decodeProviderJSON(raw, &reviews); decodeErr != nil {
			return VerifiedFeedback{}, fmt.Errorf("decode reviews page %d: %w", page, decodeErr)
		}
		if reviews == nil || len(reviews) > providerReviewsPerPage {
			return VerifiedFeedback{}, fmt.Errorf("reviews page %d has an invalid item count", page)
		}
		totalItems += len(reviews)
		if totalItems > maxProviderReviewItems {
			return VerifiedFeedback{}, errors.New("GitHub provider review count exceeds the limit")
		}
		for index := range reviews {
			id, idErr := canonicalProviderDatabaseID(reviews[index].ID)
			if idErr != nil {
				return VerifiedFeedback{}, fmt.Errorf(
					"reviews page %d item %d has an invalid ID",
					page,
					index,
				)
			}
			if id != evidence.ReviewID {
				continue
			}
			if matched != nil {
				return VerifiedFeedback{}, errors.New("GitHub provider returned the review more than once")
			}
			copyReview := reviews[index]
			matched = &copyReview
		}
		if len(reviews) < providerReviewsPerPage {
			scanCompleted = true
			break
		}
	}
	if !scanCompleted {
		probeLimit, limitErr := remainingProviderReadLimit(totalBytes)
		if limitErr != nil {
			return VerifiedFeedback{}, limitErr
		}
		raw, readErr := v.read(
			ctx,
			server,
			map[string]any{
				"method":     "get_reviews",
				"owner":      owner,
				"repo":       repo,
				"pullNumber": evidence.PullNumber,
				"page":       providerOverflowProbePage,
				"perPage":    1,
			},
			probeLimit,
		)
		if readErr != nil {
			return VerifiedFeedback{}, fmt.Errorf("probe review scan overflow: %w", readErr)
		}
		totalBytes += len(raw)
		if totalBytes > maxProviderTotalBytes {
			return VerifiedFeedback{}, errors.New("GitHub provider snapshot exceeds the total limit")
		}
		var overflow []providerReview
		if decodeErr := decodeProviderJSON(raw, &overflow); decodeErr != nil {
			return VerifiedFeedback{}, fmt.Errorf("decode review scan overflow probe: %w", decodeErr)
		}
		if overflow == nil || len(overflow) > 1 {
			return VerifiedFeedback{}, errors.New("review scan overflow probe has an invalid item count")
		}
		if len(overflow) != 0 {
			return VerifiedFeedback{}, errors.New("GitHub provider review scan exceeds five complete pages")
		}
	}
	if matched == nil {
		return VerifiedFeedback{}, providerEvidenceMismatch(
			"exact GitHub review was not found within the bounded scan",
		)
	}
	if err := verifyProviderReview(evidence, *matched); err != nil {
		return VerifiedFeedback{}, err
	}
	verified.ReviewID = evidence.ReviewID
	verified.ReviewAuthor = matched.User.Login
	verified.CurrentReviewState = strings.ToLower(matched.State)
	verified.ReviewCommitSHA = matched.CommitID
	verified.ReviewSubmittedAt = evidence.ReviewSubmittedAt
	verified.ReviewURL = matched.HTMLURL
	verified.Feedback = matched.Body
	return verified, nil
}

// VerifyCase independently refreshes one store-validated durable case for a
// trusted local development controller. Mutable head facts are refreshed, but
// the pull target and exact captured review remain immutable fences. Draft pull
// requests remain actionable because they may still receive repair commits.
// This method performs read-only provider calls and grants no checkout or
// provider-write authority by itself.
func (v *GitHubVerifier) VerifyCase(
	ctx context.Context,
	stored eventing.PRDevelopmentCase,
) (VerifiedCase, error) {
	if ctx == nil {
		return VerifiedCase{}, errors.New("GitHub case verifier context is required")
	}
	evidence, err := routingEvidenceForStoredCase(stored)
	if err != nil {
		return VerifiedCase{}, err
	}
	if v == nil {
		return VerifiedCase{}, errors.New("GitHub verifier is required")
	}
	webOrigin, err := canonicalGitHubWebOrigin(v.WebOrigin)
	if err != nil {
		return VerifiedCase{}, fmt.Errorf("GitHub verifier web origin is invalid: %w", err)
	}
	if urlErr := verifyRoutingEvidenceURLs(evidence, webOrigin); urlErr != nil {
		if errors.Is(urlErr, errGitHubProviderEvidenceMismatch) {
			return VerifiedCase{}, fmt.Errorf("%w: %v", ErrGitHubCaseDrift, urlErr)
		}
		return VerifiedCase{}, urlErr
	}
	verified, err := v.Verify(ctx, evidence)
	if err != nil {
		if errors.Is(err, errGitHubProviderEvidenceMismatch) {
			return VerifiedCase{}, fmt.Errorf(
				"%w: %v",
				ErrGitHubCaseDrift,
				err,
			)
		}
		return VerifiedCase{}, fmt.Errorf("verify current GitHub case: %w", err)
	}
	if verified.PullState != "open" || verified.PullMerged {
		return VerifiedCase{}, ErrGitHubCaseNotActionable
	}
	if verified.CurrentReviewState != string(stored.SubmittedReviewState) {
		return VerifiedCase{}, ErrGitHubCaseNotActionable
	}
	if !strings.EqualFold(verified.BaseRepository, stored.BaseRepository) ||
		verified.BaseRef != stored.BaseRef {
		return VerifiedCase{}, fmt.Errorf(
			"%w: pull request target changed",
			ErrGitHubCaseDrift,
		)
	}
	headCloneURL, err := verifiedHeadCloneURL(
		verified.headCloneURL,
		webOrigin,
		verified.HeadRepository,
	)
	if err != nil {
		return VerifiedCase{}, fmt.Errorf(
			"%w: head clone endpoint is invalid",
			ErrGitHubCaseDrift,
		)
	}
	liveDigest := reviewSnapshotDigest(
		verified.Repository,
		int64(verified.PullNumber),
		verified.ReviewID,
		verified.ReviewAuthor,
		verified.CurrentReviewState,
		verified.ReviewCommitSHA,
		verified.ReviewSubmittedAt,
		verified.ReviewURL,
		verified.Feedback,
	)
	capturedDigest := reviewSnapshotDigest(
		stored.Repository,
		stored.PullNumber,
		stored.ReviewID,
		stored.ReviewAuthor,
		string(stored.SubmittedReviewState),
		stored.ReviewCommitSHA,
		stored.ReviewSubmittedAt,
		stored.ReviewURL,
		stored.Feedback,
	)
	if liveDigest != capturedDigest {
		return VerifiedCase{}, fmt.Errorf(
			"%w: captured review evidence changed",
			ErrGitHubCaseDrift,
		)
	}
	return VerifiedCase{
		CaseID:         stored.ID,
		Repository:     verified.Repository,
		PullNumber:     int64(verified.PullNumber),
		HeadRepository: verified.HeadRepository,
		HeadRef:        verified.HeadRef,
		HeadSHA:        verified.HeadSHA,
		HeadCloneURL:   headCloneURL,
		CurrentReviewState: eventing.PRDevelopmentReviewState(
			verified.CurrentReviewState,
		),
		ReviewDigest: liveDigest,
	}, nil
}

func routingEvidenceForStoredCase(
	stored eventing.PRDevelopmentCase,
) (RoutingEvidence, error) {
	if !validCaseID(stored.ID) ||
		stored.PullNumber <= 0 || stored.PullNumber > MaximumPullNumber ||
		(stored.PullState != eventing.PRDevelopmentPullOpen &&
			stored.PullState != eventing.PRDevelopmentPullClosed) ||
		(stored.PullMerged &&
			(stored.PullState != eventing.PRDevelopmentPullClosed || stored.PullDraft)) ||
		!validProviderRepositoryIdentity(stored.Repository) ||
		!validProviderRepositoryIdentity(stored.BaseRepository) ||
		!strings.EqualFold(stored.Repository, stored.BaseRepository) ||
		!validProviderRepositoryIdentity(stored.HeadRepository) ||
		!validHTTPSURL(stored.PullURL) ||
		!validGitHubUser(stored.PullAuthor, false) ||
		!validGitHubUser(stored.TargetUser, false) ||
		!strings.EqualFold(stored.PullAuthor, stored.TargetUser) ||
		!databaseIDPattern.MatchString(stored.ReviewID) ||
		!validNodeID(stored.TriggerReviewNodeID) ||
		!validGitHubUser(stored.ReviewAuthor, true) ||
		strings.EqualFold(stored.ReviewAuthor, stored.TargetUser) ||
		!validReviewState(string(stored.SubmittedReviewState)) ||
		!validObjectID(stored.ReviewCommitSHA) ||
		stored.ReviewSubmittedAt.IsZero() ||
		!validHTTPSURLWithFragment(stored.ReviewURL) ||
		len(stored.Feedback) > maxProviderReviewBody ||
		!utf8.ValidString(stored.Feedback) ||
		!validStoredGitRef(stored.BaseRef) || !validObjectID(stored.BaseSHA) ||
		!validStoredGitRef(stored.HeadRef) || !validObjectID(stored.HeadSHA) {
		return RoutingEvidence{}, fmt.Errorf(
			"%w: durable case identity is invalid",
			ErrGitHubCaseDrift,
		)
	}
	reviewID, err := strconv.ParseInt(stored.ReviewID, 10, 64)
	if err != nil || reviewID <= 0 || strconv.FormatInt(reviewID, 10) != stored.ReviewID {
		return RoutingEvidence{}, fmt.Errorf(
			"%w: durable review identity is invalid",
			ErrGitHubCaseDrift,
		)
	}
	_, reviewOffset := stored.ReviewSubmittedAt.Zone()
	if reviewOffset != 0 {
		return RoutingEvidence{}, fmt.Errorf(
			"%w: durable review time is invalid",
			ErrGitHubCaseDrift,
		)
	}
	if stored.CurrentReviewState != stored.SubmittedReviewState {
		return RoutingEvidence{}, ErrGitHubCaseNotActionable
	}
	if stored.PullMerged {
		return RoutingEvidence{}, ErrGitHubCaseNotActionable
	}
	return RoutingEvidence{
		Repository:        stored.Repository,
		PullNumber:        int(stored.PullNumber),
		PullURL:           stored.PullURL,
		PullAuthor:        stored.PullAuthor,
		TargetUser:        stored.TargetUser,
		ReviewID:          stored.ReviewID,
		ReviewNodeID:      stored.TriggerReviewNodeID,
		ReviewURL:         stored.ReviewURL,
		ReviewAuthor:      stored.ReviewAuthor,
		ReviewState:       string(stored.SubmittedReviewState),
		ReviewCommitSHA:   stored.ReviewCommitSHA,
		ReviewSubmittedAt: stored.ReviewSubmittedAt,
	}, nil
}

func canonicalGitHubWebOrigin(configured string) (string, error) {
	if configured == "" {
		return defaultGitHubWebOrigin, nil
	}
	if len(configured) > 4096 || !utf8.ValidString(configured) ||
		configured != strings.TrimSpace(configured) {
		return "", errors.New("web origin is not canonical text")
	}
	parsed, err := url.Parse(configured)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.Opaque != "" || parsed.User != nil || parsed.Path != "" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || parsed.Host != strings.ToLower(parsed.Host) ||
		strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New("web origin must be one lowercase canonical HTTPS origin")
	}
	hostname := parsed.Hostname()
	if !validGitHubWebHostname(hostname) {
		return "", errors.New("web origin hostname is invalid")
	}
	if port := parsed.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 || value == 443 {
			return "", errors.New("web origin port is not canonical")
		}
	}
	return configured, nil
}

func validGitHubWebHostname(hostname string) bool {
	if hostname == "" {
		return false
	}
	if strings.Contains(hostname, ":") {
		return net.ParseIP(hostname) != nil
	}
	if len(hostname) > 253 || strings.HasPrefix(hostname, ".") ||
		strings.HasSuffix(hostname, ".") {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' ||
			label[len(label)-1] == '-' {
			return false
		}
		for _, char := range []byte(label) {
			if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' ||
				char == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func verifyRoutingEvidenceURLs(evidence RoutingEvidence, webOrigin string) error {
	if !validProviderRepositoryIdentity(evidence.Repository) ||
		evidence.PullNumber <= 0 || int64(evidence.PullNumber) > MaximumPullNumber {
		return errors.New("GitHub routing evidence repository or pull number is invalid")
	}
	reviewID, err := strconv.ParseInt(evidence.ReviewID, 10, 64)
	if err != nil || reviewID <= 0 || strconv.FormatInt(reviewID, 10) != evidence.ReviewID {
		return errors.New("GitHub routing evidence review ID is invalid")
	}
	pull, err := url.Parse(evidence.PullURL)
	if err != nil || pull.Opaque != "" || pull.User != nil || pull.RawPath != "" ||
		pull.RawQuery != "" || pull.ForceQuery || pull.Fragment != "" {
		return providerEvidenceMismatch("GitHub pull request URL is not canonical")
	}
	origin, err := url.Parse(webOrigin)
	if err != nil || pull.Scheme != origin.Scheme || pull.Host != origin.Host {
		return providerEvidenceMismatch("GitHub pull request URL is not on the configured web origin")
	}
	segments := strings.Split(strings.TrimPrefix(pull.Path, "/"), "/")
	if len(segments) != 4 ||
		!strings.EqualFold(segments[0]+"/"+segments[1], evidence.Repository) ||
		segments[2] != "pull" || segments[3] != strconv.Itoa(evidence.PullNumber) ||
		pull.Path != "/"+strings.Join(segments, "/") {
		return providerEvidenceMismatch("GitHub pull request URL is not canonical")
	}
	if evidence.ReviewURL != evidence.PullURL+"#pullrequestreview-"+evidence.ReviewID {
		return providerEvidenceMismatch("GitHub review URL is not canonical")
	}
	return nil
}

func verifiedHeadCloneURL(raw, webOrigin, headRepository string) (string, error) {
	if !validProviderRepositoryIdentity(headRepository) || webOrigin == "" {
		return "", errors.New("clone endpoint identity is invalid")
	}
	expected := webOrigin + "/" + headRepository + ".git"
	if raw != expected {
		return "", errors.New("clone URL is not canonical for the configured web origin")
	}
	return expected, nil
}

func validProviderRepositoryIdentity(value string) bool {
	if len(value) > MaximumRepositoryBytes || !repositoryPattern.MatchString(value) {
		return false
	}
	owner, repository, found := strings.Cut(value, "/")
	return found && owner != "." && owner != ".." &&
		repository != "." && repository != ".."
}

func providerEvidenceMismatch(message string) error {
	return fmt.Errorf("%w: %s", errGitHubProviderEvidenceMismatch, message)
}

func reviewSnapshotDigest(
	repository string,
	pullNumber int64,
	reviewID, reviewAuthor, reviewState, reviewCommitSHA string,
	reviewSubmittedAt time.Time,
	reviewURL, feedback string,
) string {
	digest := sha256.New()
	for _, part := range []string{
		providerReviewDigestDomain,
		strings.ToLower(repository),
		strconv.FormatInt(pullNumber, 10),
		reviewID,
		strings.ToLower(reviewAuthor),
		strings.ToLower(reviewState),
		reviewCommitSHA,
		reviewSubmittedAt.UTC().Format(time.RFC3339Nano),
		reviewURL,
		feedback,
	} {
		writeProviderReviewDigestPart(digest, part)
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func writeProviderReviewDigestPart(digest hash.Hash, value string) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write([]byte(value))
}

func verifyProviderPullRequest(
	evidence RoutingEvidence,
	pull providerPullRequest,
) (VerifiedFeedback, error) {
	if pull.Number != evidence.PullNumber {
		return VerifiedFeedback{}, providerEvidenceMismatch(
			"GitHub pull request number does not match the event",
		)
	}
	if !validHTTPSURL(pull.HTMLURL) {
		return VerifiedFeedback{}, errors.New("GitHub pull request URL is invalid")
	}
	if pull.HTMLURL != evidence.PullURL {
		return VerifiedFeedback{}, providerEvidenceMismatch(
			"GitHub pull request URL does not match the event",
		)
	}
	if pull.Draft == nil || pull.Merged == nil {
		return VerifiedFeedback{}, errors.New("GitHub pull request draft or merged state is incomplete")
	}
	if pull.User == nil || !validGitHubUser(pull.User.Login, false) {
		return VerifiedFeedback{}, errors.New("GitHub pull request author is invalid")
	}
	if !strings.EqualFold(pull.User.Login, evidence.PullAuthor) ||
		!strings.EqualFold(pull.User.Login, evidence.TargetUser) {
		return VerifiedFeedback{}, providerEvidenceMismatch(
			"GitHub pull request author does not match the target",
		)
	}
	state := strings.ToLower(pull.State)
	if state != "open" && state != "closed" {
		return VerifiedFeedback{}, errors.New("GitHub pull request state is invalid")
	}
	if pull.Head == nil || pull.Base == nil || pull.Head.Repo == nil || pull.Base.Repo == nil ||
		!validProviderRef(*pull.Head) || !validProviderRef(*pull.Base) {
		return VerifiedFeedback{}, errors.New("GitHub pull request branch identity is incomplete")
	}
	if !validProviderRepositoryIdentity(pull.Base.Repo.FullName) ||
		!validProviderRepositoryIdentity(pull.Head.Repo.FullName) {
		return VerifiedFeedback{}, errors.New("GitHub pull request repository identity is invalid")
	}
	if !strings.EqualFold(pull.Base.Repo.FullName, evidence.Repository) {
		return VerifiedFeedback{}, providerEvidenceMismatch(
			"GitHub pull request repository does not match the event",
		)
	}
	return VerifiedFeedback{
		Repository:     pull.Base.Repo.FullName,
		PullNumber:     pull.Number,
		PullURL:        pull.HTMLURL,
		PullAuthor:     pull.User.Login,
		PullState:      state,
		PullDraft:      *pull.Draft,
		PullMerged:     *pull.Merged,
		BaseRepository: pull.Base.Repo.FullName,
		BaseRef:        pull.Base.Ref,
		BaseSHA:        pull.Base.SHA,
		HeadRepository: pull.Head.Repo.FullName,
		HeadRef:        pull.Head.Ref,
		HeadSHA:        pull.Head.SHA,
		headCloneURL:   pull.Head.Repo.CloneURL,
	}, nil
}

func verifyProviderReview(evidence RoutingEvidence, review providerReview) error {
	id, err := canonicalProviderDatabaseID(review.ID)
	if err != nil {
		return errors.New("GitHub review ID is invalid")
	}
	if id != evidence.ReviewID {
		return providerEvidenceMismatch("GitHub review ID does not match the event")
	}
	if review.User == nil || !validGitHubUser(review.User.Login, true) {
		return errors.New("GitHub review author is invalid")
	}
	if !strings.EqualFold(review.User.Login, evidence.ReviewAuthor) ||
		strings.EqualFold(review.User.Login, evidence.TargetUser) {
		return providerEvidenceMismatch("GitHub review author does not match the event")
	}
	state := strings.ToLower(review.State)
	if !validReviewState(state) && state != "dismissed" {
		return errors.New("GitHub review state is invalid")
	}
	if state != evidence.ReviewState && state != "dismissed" {
		return providerEvidenceMismatch("GitHub review state does not match the event")
	}
	if !validObjectID(review.CommitID) {
		return errors.New("GitHub review commit is invalid")
	}
	if review.CommitID != evidence.ReviewCommitSHA {
		return providerEvidenceMismatch("GitHub review commit does not match the event")
	}
	submittedAt, err := time.Parse(time.RFC3339Nano, review.SubmittedAt)
	if err != nil {
		return errors.New("GitHub review submission time is invalid")
	}
	if !submittedAt.Equal(evidence.ReviewSubmittedAt) {
		return providerEvidenceMismatch(
			"GitHub review submission time does not match the event",
		)
	}
	if !validHTTPSURLWithFragment(review.HTMLURL) {
		return errors.New("GitHub review URL is invalid")
	}
	if review.HTMLURL != evidence.ReviewURL {
		return providerEvidenceMismatch("GitHub review URL does not match the event")
	}
	if len(review.Body) > maxProviderReviewBody || !utf8.ValidString(review.Body) {
		return errors.New("GitHub review body is invalid or too large")
	}
	return nil
}

func validProviderRef(ref providerPullRequestRef) bool {
	if ref.Ref == "" || len(ref.Ref) > maxProviderRefBytes ||
		ref.Ref != strings.TrimSpace(ref.Ref) || !utf8.ValidString(ref.Ref) ||
		!validObjectID(ref.SHA) {
		return false
	}
	for _, char := range ref.Ref {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return false
		}
	}
	return true
}

func validStoredGitRef(value string) bool {
	if value == "" || len(value) > maxProviderRefBytes ||
		value != strings.TrimSpace(value) || value == "@" ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.Contains(value, "//") || strings.Contains(value, "..") ||
		strings.Contains(value, "@{") || !utf8.ValidString(value) {
		return false
	}
	for _, char := range []byte(value) {
		if char <= ' ' || char == 0x7f || strings.ContainsRune("~^:?*[\\", rune(char)) {
			return false
		}
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") ||
			strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func canonicalProviderDatabaseID(value json.Number) (string, error) {
	raw := string(value)
	if !databaseIDPattern.MatchString(raw) {
		return "", errors.New("database ID is not canonical")
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != raw {
		return "", errors.New("database ID is out of range")
	}
	return raw, nil
}

func remainingProviderReadLimit(totalBytes int) (int, error) {
	if totalBytes < 0 || totalBytes >= maxProviderTotalBytes {
		return 0, errors.New("GitHub provider snapshot exceeds the total limit")
	}
	remaining := maxProviderTotalBytes - totalBytes
	if remaining > maxProviderJSONBytes {
		remaining = maxProviderJSONBytes
	}
	return remaining, nil
}

func (v *GitHubVerifier) read(
	ctx context.Context,
	server string,
	args map[string]any,
	limit int,
) ([]byte, error) {
	outputs, err := v.Runner.RunTool(ctx, workflows.ToolRequest{
		Name:      picomcp.CanonicalToolName(server, pullRequestReadTool),
		Args:      args,
		MCP:       true,
		MCPServer: server,
		MCPTool:   pullRequestReadTool,
	})
	if err != nil {
		return nil, err
	}
	return v.exactJSON(outputs, limit)
}

func (v *GitHubVerifier) exactJSON(outputs map[string]any, limit int) ([]byte, error) {
	if outputs == nil {
		return nil, errors.New("MCP result is missing")
	}
	if limit <= 0 || limit > maxProviderJSONBytes {
		limit = maxProviderJSONBytes
	}
	if rawTags, present := outputs["artifact_tags"]; present {
		tags, ok := rawTags.([]string)
		if !ok {
			return nil, errors.New("MCP artifact tags are invalid")
		}
		if len(tags) > 0 {
			return v.exactArtifactJSON(tags, limit)
		}
	}
	value, ok := outputs["text"].(string)
	if !ok {
		return nil, errors.New("MCP result text is missing")
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return nil, errors.New("MCP result text is invalid or too large")
	}
	raw := []byte(value)
	if !json.Valid(raw) {
		return nil, errors.New("MCP result is not exact JSON")
	}
	return raw, nil
}

func (v *GitHubVerifier) exactArtifactJSON(tags []string, limit int) ([]byte, error) {
	if len(tags) != 1 || strings.TrimSpace(v.ArtifactRoot) == "" {
		return nil, errors.New("MCP exact JSON artifact is unavailable")
	}
	artifactPath, ok := strings.CutPrefix(tags[0], "[file:")
	if !ok || !strings.HasSuffix(artifactPath, "]") {
		return nil, errors.New("MCP exact JSON artifact tag is invalid")
	}
	artifactPath = strings.TrimSuffix(artifactPath, "]")
	root, err := filepath.Abs(strings.TrimSpace(v.ArtifactRoot))
	if err != nil {
		return nil, err
	}
	artifactPath, err = filepath.Abs(artifactPath)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(artifactPath)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("MCP exact JSON artifact is not a regular file")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	resolvedPath, err := filepath.EvalSymlinks(artifactPath)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("MCP exact JSON artifact is outside the configured root")
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, err
	}
	after, statErr := file.Stat()
	if statErr != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) ||
		after.Size() > int64(limit) {
		_ = file.Close()
		return nil, errors.New("MCP exact JSON artifact is invalid or too large")
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > limit || !utf8.Valid(raw) {
		return nil, errors.New("MCP exact JSON artifact cannot be read safely")
	}
	current, currentErr := os.Lstat(resolvedPath)
	if currentErr != nil || !current.Mode().IsRegular() || !os.SameFile(after, current) {
		return nil, errors.New("MCP exact JSON artifact changed while it was read")
	}
	if err := os.Remove(resolvedPath); err != nil {
		return nil, fmt.Errorf("remove consumed MCP exact JSON artifact: %w", err)
	}
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		return nil, errors.New("MCP exact JSON artifact is not JSON")
	}
	return raw, nil
}

func decodeProviderJSON(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > maxProviderJSONBytes || !utf8.Valid(raw) {
		return errors.New("provider JSON is invalid or too large")
	}
	if err := validateProviderJSONStringEncoding(raw); err != nil {
		return err
	}
	if err := rejectDuplicateProviderJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("provider JSON contains trailing data")
		}
		return err
	}
	return nil
}

func rejectDuplicateProviderJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	tokens := 0
	var decodeValue func(int) error
	decodeValue = func(depth int) error {
		if depth > maxProviderJSONDepth {
			return errors.New("provider JSON exceeds the depth limit")
		}
		tokens++
		if tokens > maxProviderJSONTokens {
			return errors.New("provider JSON exceeds the token limit")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				tokens++
				if tokens > maxProviderJSONTokens {
					return errors.New("provider JSON exceeds the token limit")
				}
				nameToken, tokenErr := decoder.Token()
				name, ok := nameToken.(string)
				if tokenErr != nil || !ok {
					return errors.New("provider JSON object name is invalid")
				}
				if _, duplicate := seen[name]; duplicate {
					return errors.New("provider JSON contains a duplicate object name")
				}
				seen[name] = struct{}{}
				if err := decodeValue(depth + 1); err != nil {
					return err
				}
			}
			closing, closeErr := decoder.Token()
			if closeErr != nil || closing != json.Delim('}') {
				return errors.New("provider JSON object is incomplete")
			}
		case '[':
			for decoder.More() {
				if err := decodeValue(depth + 1); err != nil {
					return err
				}
			}
			closing, closeErr := decoder.Token()
			if closeErr != nil || closing != json.Delim(']') {
				return errors.New("provider JSON array is incomplete")
			}
		default:
			return errors.New("provider JSON delimiter is invalid")
		}
		return nil
	}
	if err := decodeValue(0); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("provider JSON contains trailing data")
		}
		return err
	}
	return nil
}

func validateProviderJSONStringEncoding(raw []byte) error {
	for index := 0; index < len(raw); index++ {
		if raw[index] != '"' {
			continue
		}
		index++
		for {
			if index >= len(raw) {
				return errors.New("provider JSON string is incomplete")
			}
			switch raw[index] {
			case '"':
				goto stringClosed
			case '\\':
				index++
				if index >= len(raw) {
					return errors.New("provider JSON escape is incomplete")
				}
				if raw[index] != 'u' {
					if !strings.ContainsRune(`"\\/bfnrt`, rune(raw[index])) {
						return errors.New("provider JSON escape is invalid")
					}
					index++
					continue
				}
				code, ok := providerJSONHexQuad(raw, index+1)
				if !ok {
					return errors.New("provider JSON Unicode escape is invalid")
				}
				index += 5
				switch {
				case code >= 0xd800 && code <= 0xdbff:
					if index+6 > len(raw) || raw[index] != '\\' || raw[index+1] != 'u' {
						return errors.New("provider JSON contains an unpaired surrogate")
					}
					low, lowOK := providerJSONHexQuad(raw, index+2)
					if !lowOK || low < 0xdc00 || low > 0xdfff {
						return errors.New("provider JSON contains an unpaired surrogate")
					}
					index += 6
				case code >= 0xdc00 && code <= 0xdfff:
					return errors.New("provider JSON contains an unpaired surrogate")
				}
				continue
			default:
				if raw[index] < 0x20 {
					return errors.New("provider JSON string contains a control byte")
				}
				_, size := utf8.DecodeRune(raw[index:])
				index += size
			}
		}
	stringClosed:
	}
	return nil
}

func providerJSONHexQuad(raw []byte, offset int) (uint16, bool) {
	if offset < 0 || offset+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, character := range raw[offset : offset+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
