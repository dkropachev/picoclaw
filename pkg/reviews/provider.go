package reviews

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/eventing"
	picomcp "github.com/sipeed/picoclaw/pkg/mcp"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	providerReviewsPerPage           = 100
	providerThreadsPerPage           = 100
	providerMaximumPages             = 5
	providerMaximumReviews           = 500
	providerMaximumThreads           = 250
	providerMaximumComments          = 1000
	providerMaximumCommentsPerThread = 100

	providerMaximumResultBytes = 16 << 20
	providerMaximumTotalBytes  = 32 << 20
	providerMaximumPublicBytes = 4 << 20
	providerMaximumTitleBytes  = 8 << 10
	providerMaximumBodyBytes   = 64 << 10
	providerMaximumPathBytes   = 4 << 10
	providerMaximumURLBytes    = 4 << 10
	providerMaximumAuthorBytes = 256
	providerMaximumThreadID    = 1024
)

const (
	ProviderAvailabilityAvailable    = "available"
	ProviderAvailabilityPartial      = "partial"
	ProviderAvailabilityUnavailable  = "unavailable"
	ProviderAvailabilityIncompatible = "incompatible"
)

const (
	providerLimitationStatusView              = "status_view"
	providerLimitationReviewPaginationStalled = "review_history_pagination_stalled"
	providerLimitationReviewHistoryCapped     = "review_history_limit_reached"
	providerLimitationReviewPaginationOverlap = "review_history_pagination_overlap"
	providerLimitationThreadIdentity          = "thread_identity_unavailable"
	providerLimitationThreadComments          = "thread_comments_incomplete"
	providerLimitationThreadsCapped           = "thread_limit_reached"
	providerLimitationCommentsCapped          = "comment_limit_reached"
	providerLimitationPublicBytes             = "provider_snapshot_size_limit_reached"
)

var (
	ErrProviderIncompatible        = errors.New("review provider response is incompatible")
	ErrProviderMutationUnsupported = errors.New("review provider mutation is unsupported")
	ErrProviderThreadConflict      = errors.New("review provider thread changed")
	errProviderResultLimit         = errors.New("review provider result exceeds the remaining limit")
)

// ProviderCapabilities is deliberately narrower than the MCP tool surface.
type ProviderCapabilities struct {
	ThreadResolution bool `json:"thread_resolution"`
}

type ProviderPullRequest struct {
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	URL       string `json:"url"`
	Author    string `json:"author,omitempty"`
	Draft     bool   `json:"draft"`
	Merged    bool   `json:"merged"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type ProviderReview struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Body        string `json:"body,omitempty"`
	URL         string `json:"url,omitempty"`
	Author      string `json:"author,omitempty"`
	CommitID    string `json:"commit_id,omitempty"`
	SubmittedAt string `json:"submitted_at,omitempty"`
}

type ProviderThreadComment struct {
	Body      string `json:"body,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      *int   `json:"line,omitempty"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	URL       string `json:"url,omitempty"`
}

type ProviderThread struct {
	Token       string                  `json:"token,omitempty"`
	IsResolved  bool                    `json:"is_resolved"`
	IsOutdated  bool                    `json:"is_outdated"`
	IsCollapsed bool                    `json:"is_collapsed"`
	CanResolve  bool                    `json:"can_resolve"`
	TotalCount  int                     `json:"total_count"`
	Comments    []ProviderThreadComment `json:"comments"`
}

// ProviderStatus is the inexpensive PR-only projection used by portfolio
// views. It intentionally has no history/thread fields.
type ProviderStatus struct {
	Availability string               `json:"availability"`
	Connector    string               `json:"connector"`
	Repository   string               `json:"repository"`
	PullNumber   int64                `json:"pull_number"`
	PullRequest  *ProviderPullRequest `json:"pull_request,omitempty"`
	Capabilities ProviderCapabilities `json:"capabilities"`
	Limitations  []string             `json:"limitations"`
}

// ProviderSnapshot is a bounded live provider observation. Raw provider
// thread IDs are structurally absent from this browser DTO.
type ProviderSnapshot struct {
	Availability          string               `json:"availability"`
	Connector             string               `json:"connector"`
	Repository            string               `json:"repository"`
	PullNumber            int64                `json:"pull_number"`
	PullRequest           *ProviderPullRequest `json:"pull_request,omitempty"`
	Capabilities          ProviderCapabilities `json:"capabilities"`
	Reviews               []ProviderReview     `json:"reviews"`
	ReviewHistoryComplete bool                 `json:"review_history_complete"`
	ThreadsComplete       bool                 `json:"threads_complete"`
	Limitations           []string             `json:"limitations"`
	Threads               []ProviderThread     `json:"threads"`
}

type ProviderThreadMutationRequest struct {
	CaseID string
	Token  string
	Action string
}

// GitHubProvider reuses the exact generation-fenced workflow MCP runner. The
// random token key is process-local; tokens cease to authorize anything when
// the runtime generation changes.
type GitHubProvider struct {
	Runner              workflows.ToolRunner
	Server              string
	ArtifactRoot        string
	WriteReady          bool
	tokenKey            []byte
	artifactCleanupHook func(string)
}

func NewGitHubProvider(
	runner workflows.ToolRunner,
	artifactRoot string,
	writeReady bool,
) (*GitHubProvider, error) {
	if runner == nil {
		return nil, errors.New("GitHub review provider runner is required")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("initialize GitHub review provider tokens: %w", err)
	}
	return &GitHubProvider{
		Runner: runner, ArtifactRoot: strings.TrimSpace(artifactRoot),
		WriteReady: writeReady, tokenKey: key,
	}, nil
}

func (provider *GitHubProvider) Status(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
) (ProviderStatus, error) {
	base := providerStatusBase(reviewCase)
	base.Limitations = []string{providerLimitationStatusView}
	pull, err := provider.readPull(ctx, reviewCase)
	if err != nil {
		return ProviderStatus{}, err
	}
	base.Availability = ProviderAvailabilityAvailable
	base.PullRequest = &pull
	return base, nil
}

func (provider *GitHubProvider) Snapshot(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
) (ProviderSnapshot, error) {
	if err := validateProviderContext(ctx, provider, reviewCase); err != nil {
		return ProviderSnapshot{}, err
	}
	pull, pullBytes, err := provider.readPullCounted(ctx, reviewCase)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	publicBase := providerPublicSnapshotBaseSize(reviewCase, pull) + (64 << 10)
	reviews, reviewsComplete, reviewLimits, readBytes, err := provider.readReviews(
		ctx, reviewCase, pullBytes, publicBase,
	)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	threads, threadsComplete, threadLimits, _, err := provider.readThreadsWithPublicBytes(
		ctx, reviewCase, readBytes, publicBase+providerPublicSizeReviews(reviews),
	)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	limitations := append(reviewLimits, threadLimits...)
	availability := ProviderAvailabilityAvailable
	if !reviewsComplete || !threadsComplete || len(limitations) != 0 {
		availability = ProviderAvailabilityPartial
	}
	snapshot := ProviderSnapshot{
		Availability:          availability,
		Connector:             reviewCase.Connector,
		Repository:            reviewCase.Repository,
		PullNumber:            reviewCase.PullNumber,
		PullRequest:           &pull,
		Capabilities:          ProviderCapabilities{ThreadResolution: provider.WriteReady && threadsHaveTokens(threads)},
		Reviews:               reviews,
		ReviewHistoryComplete: reviewsComplete,
		ThreadsComplete:       threadsComplete,
		Limitations:           uniqueProviderLimitations(limitations),
		Threads:               threads,
	}
	if encoded, encodeErr := json.Marshal(snapshot); encodeErr != nil ||
		len(encoded) > providerMaximumPublicBytes {
		return ProviderSnapshot{}, ErrUnavailable
	}
	return snapshot, nil
}

func (provider *GitHubProvider) MutateThread(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
	request ProviderThreadMutationRequest,
) (ProviderSnapshot, error) {
	if !provider.WriteReady {
		return ProviderSnapshot{}, ErrProviderMutationUnsupported
	}
	if request.CaseID != reviewCase.ID || !validProviderToken(request.Token) ||
		(request.Action != "resolve" && request.Action != "unresolve") {
		return ProviderSnapshot{}, ErrInvalidRequest
	}
	// Public threads intentionally omit their raw IDs. Select solely by the
	// case-bound HMAC token while repeating the bounded live scan.
	threadID, current, err := provider.findThreadID(ctx, reviewCase, request.Token)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	if threadID == "" {
		return ProviderSnapshot{}, ErrProviderThreadConflict
	}

	desired := request.Action == "resolve"
	if current != desired {
		owner, repo, _ := strings.Cut(reviewCase.Repository, "/")
		_, err = provider.run(ctx, GitHubPullRequestReviewWriteTool, map[string]any{
			"method":     request.Action + "_thread",
			"owner":      owner,
			"repo":       repo,
			"pullNumber": reviewCase.PullNumber,
			"threadId":   threadID,
		})
		if err != nil {
			if contextErr := providerContextError(ctx, err); contextErr != nil {
				return ProviderSnapshot{}, contextErr
			}
			_, reconciled, readErr := provider.findThreadID(ctx, reviewCase, request.Token)
			if readErr != nil || reconciled != desired {
				return ProviderSnapshot{}, ErrUnavailable
			}
		}
	}
	snapshot, err := provider.Snapshot(ctx, reviewCase)
	if err != nil {
		return ProviderSnapshot{}, err
	}
	reconciled := false
	for _, thread := range snapshot.Threads {
		if thread.Token == request.Token && thread.IsResolved == desired {
			reconciled = true
			break
		}
	}
	if !reconciled {
		return ProviderSnapshot{}, ErrProviderThreadConflict
	}
	return snapshot, nil
}

func providerStatusBase(reviewCase eventing.ReviewCase) ProviderStatus {
	return ProviderStatus{
		Availability: ProviderAvailabilityUnavailable,
		Connector:    reviewCase.Connector,
		Repository:   reviewCase.Repository,
		PullNumber:   reviewCase.PullNumber,
		Capabilities: ProviderCapabilities{},
		Limitations:  []string{providerLimitationStatusView},
	}
}

func unavailableProviderSnapshot(reviewCase eventing.ReviewCase) ProviderSnapshot {
	return ProviderSnapshot{
		Availability: ProviderAvailabilityUnavailable,
		Connector:    reviewCase.Connector,
		Repository:   reviewCase.Repository,
		PullNumber:   reviewCase.PullNumber,
		Capabilities: ProviderCapabilities{},
		Reviews:      []ProviderReview{},
		Limitations:  []string{},
		Threads:      []ProviderThread{},
	}
}

func validateProviderContext(
	ctx context.Context,
	provider *GitHubProvider,
	reviewCase eventing.ReviewCase,
) error {
	if provider == nil || provider.Runner == nil || len(provider.tokenKey) != 32 {
		return ErrUnavailable
	}
	if ctx == nil {
		return ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validReviewID(reviewCase.ID, "prc_") || !validRepository(reviewCase.Repository) ||
		reviewCase.PullNumber <= 0 || strings.TrimSpace(reviewCase.Connector) == "" ||
		!validProviderPullURL(reviewCase.PullURL, reviewCase.Repository, reviewCase.PullNumber) {
		return ErrProviderIncompatible
	}
	return nil
}

type providerPullWire struct {
	Number    int64               `json:"number"`
	Title     string              `json:"title"`
	State     string              `json:"state"`
	Draft     bool                `json:"draft"`
	Merged    bool                `json:"merged"`
	HTMLURL   string              `json:"html_url"`
	User      *providerUserWire   `json:"user"`
	Base      *providerBranchWire `json:"base"`
	UpdatedAt string              `json:"updated_at"`
}

type providerUserWire struct {
	Login string `json:"login"`
}
type providerBranchWire struct {
	Repo *providerRepoWire `json:"repo"`
}
type providerRepoWire struct {
	FullName string `json:"full_name"`
}

func (provider *GitHubProvider) readPull(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
) (ProviderPullRequest, error) {
	pull, _, err := provider.readPullCounted(ctx, reviewCase)
	return pull, err
}

func (provider *GitHubProvider) readPullCounted(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
) (ProviderPullRequest, int, error) {
	if err := validateProviderContext(ctx, provider, reviewCase); err != nil {
		return ProviderPullRequest{}, 0, err
	}
	owner, repo, _ := strings.Cut(reviewCase.Repository, "/")
	raw, err := provider.readExact(ctx, map[string]any{
		"method": "get", "owner": owner, "repo": repo,
		"pullNumber": reviewCase.PullNumber,
	}, providerMaximumResultBytes)
	if err != nil {
		if contextErr := providerContextError(ctx, err); contextErr != nil {
			return ProviderPullRequest{}, 0, contextErr
		}
		if errors.Is(err, ErrProviderIncompatible) {
			return ProviderPullRequest{}, 0, ErrProviderIncompatible
		}
		return ProviderPullRequest{}, 0, ErrUnavailable
	}
	var wire providerPullWire
	if err := decodeProviderExactJSON(raw, &wire); err != nil {
		return ProviderPullRequest{}, 0, ErrProviderIncompatible
	}
	state := strings.ToLower(strings.TrimSpace(wire.State))
	author := ""
	if wire.User != nil {
		author = wire.User.Login
	}
	if wire.Number != reviewCase.PullNumber || (state != "open" && state != "closed") ||
		wire.Merged && state != "closed" || wire.Draft && state != "open" ||
		!validProviderText(wire.Title, providerMaximumTitleBytes, true) ||
		!validProviderText(author, providerMaximumAuthorBytes, false) ||
		!sameProviderPullURL(wire.HTMLURL, reviewCase.PullURL) ||
		wire.Base == nil || wire.Base.Repo == nil ||
		!strings.EqualFold(wire.Base.Repo.FullName, reviewCase.Repository) ||
		!validOptionalProviderTime(wire.UpdatedAt) {
		return ProviderPullRequest{}, 0, ErrProviderIncompatible
	}
	return ProviderPullRequest{
		Number: wire.Number, Title: wire.Title, State: state, URL: wire.HTMLURL,
		Author: author, Draft: wire.Draft, Merged: wire.Merged, UpdatedAt: wire.UpdatedAt,
	}, len(raw), nil
}

type providerReviewWire struct {
	ID          json.Number       `json:"id"`
	State       string            `json:"state"`
	Body        string            `json:"body"`
	HTMLURL     string            `json:"html_url"`
	User        *providerUserWire `json:"user"`
	CommitID    string            `json:"commit_id"`
	SubmittedAt string            `json:"submitted_at"`
}

func (provider *GitHubProvider) readReviews(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
	initialBytes int,
	initialPublicBytes int,
) ([]ProviderReview, bool, []string, int, error) {
	owner, repo, _ := strings.Cut(reviewCase.Repository, "/")
	result := make([]ProviderReview, 0)
	seenIDs := make(map[string]struct{})
	seenPages := make(map[[32]byte]struct{})
	totalBytes := initialBytes
	publicBytes := initialPublicBytes
	complete := false
	limitations := []string{}
	for page := 1; page <= providerMaximumPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, false, nil, totalBytes, err
		}
		remaining := providerMaximumTotalBytes - totalBytes
		if remaining <= 0 {
			limitations = append(limitations, providerLimitationReviewHistoryCapped)
			break
		}
		raw, err := provider.readExact(ctx, map[string]any{
			"method": "get_reviews", "owner": owner, "repo": repo,
			"pullNumber": reviewCase.PullNumber, "page": page,
			"perPage": providerReviewsPerPage,
		}, minProviderLimit(providerMaximumResultBytes, remaining))
		if err != nil {
			if contextErr := providerContextError(ctx, err); contextErr != nil {
				return nil, false, nil, totalBytes, contextErr
			}
			if errors.Is(err, errProviderResultLimit) {
				limitations = append(limitations, providerLimitationReviewHistoryCapped)
				break
			}
			if errors.Is(err, ErrProviderIncompatible) {
				return nil, false, nil, totalBytes, ErrProviderIncompatible
			}
			return nil, false, nil, totalBytes, ErrUnavailable
		}
		totalBytes += len(raw)
		if totalBytes > providerMaximumTotalBytes {
			limitations = append(limitations, providerLimitationReviewHistoryCapped)
			break
		}
		var pageItems []providerReviewWire
		if err := decodeProviderExactJSON(raw, &pageItems); err != nil || pageItems == nil || len(pageItems) > providerReviewsPerPage {
			return nil, false, nil, totalBytes, ErrProviderIncompatible
		}
		if len(pageItems) == 0 {
			complete = true
			break
		}
		fingerprint := sha256.Sum256(raw)
		if _, duplicate := seenPages[fingerprint]; duplicate {
			limitations = append(limitations, providerLimitationReviewPaginationStalled)
			break
		}
		seenPages[fingerprint] = struct{}{}
		for _, item := range pageItems {
			projected, err := projectProviderReview(item, reviewCase.PullURL)
			if err != nil {
				return nil, false, nil, totalBytes, ErrProviderIncompatible
			}
			if _, duplicate := seenIDs[projected.ID]; duplicate {
				limitations = append(limitations, providerLimitationReviewPaginationOverlap)
				continue
			}
			seenIDs[projected.ID] = struct{}{}
			size := providerPublicSizeReview(projected)
			if len(result) >= providerMaximumReviews || publicBytes+size > providerMaximumPublicBytes {
				limitations = append(limitations, providerLimitationReviewHistoryCapped)
				if publicBytes+size > providerMaximumPublicBytes {
					limitations = append(limitations, providerLimitationPublicBytes)
				}
				return result, false, uniqueProviderLimitations(limitations), totalBytes, nil
			}
			publicBytes += size
			result = append(result, projected)
		}
	}
	if !complete && len(limitations) == 0 {
		limitations = append(limitations, providerLimitationReviewHistoryCapped)
	}
	if containsProviderLimitationValue(limitations, providerLimitationReviewPaginationOverlap) {
		complete = false
	}
	return result, complete, uniqueProviderLimitations(limitations), totalBytes, nil
}

func projectProviderReview(wire providerReviewWire, pullURL string) (ProviderReview, error) {
	id := strings.TrimSpace(wire.ID.String())
	if !validProviderNumericID(id) || !validProviderText(wire.State, 64, true) ||
		!validProviderText(wire.Body, providerMaximumBodyBytes, false) ||
		!providerURLBelongsToPull(wire.HTMLURL, pullURL) ||
		!validProviderText(wire.CommitID, 128, false) ||
		!validOptionalProviderTime(wire.SubmittedAt) {
		return ProviderReview{}, ErrProviderIncompatible
	}
	author := ""
	if wire.User != nil {
		author = wire.User.Login
	}
	if !validProviderText(author, providerMaximumAuthorBytes, false) {
		return ProviderReview{}, ErrProviderIncompatible
	}
	return ProviderReview{ID: id, State: strings.ToLower(wire.State), Body: wire.Body,
		URL: wire.HTMLURL, Author: author, CommitID: wire.CommitID, SubmittedAt: wire.SubmittedAt}, nil
}

type providerThreadsWire struct {
	ReviewThreads []providerThreadWire `json:"review_threads"`
	TotalCount    int                  `json:"totalCount"`
	PageInfo      providerPageInfoWire `json:"pageInfo"`
}
type providerPageInfoWire struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}
type providerThreadWire struct {
	ID          string
	IsResolved  bool                        `json:"is_resolved"`
	IsOutdated  bool                        `json:"is_outdated"`
	IsCollapsed bool                        `json:"is_collapsed"`
	Comments    []providerThreadCommentWire `json:"comments"`
	TotalCount  int                         `json:"total_count"`
}
type providerThreadCommentWire struct {
	Body      string `json:"body"`
	Path      string `json:"path"`
	Line      *int   `json:"line"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	HTMLURL   string `json:"html_url"`
}

func (wire *providerThreadWire) UnmarshalJSON(raw []byte) error {
	type alias providerThreadWire
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	var decoded alias
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	var lower, upper string
	if value, ok := fields["id"]; ok && json.Unmarshal(value, &lower) != nil {
		return ErrProviderIncompatible
	}
	if value, ok := fields["ID"]; ok && json.Unmarshal(value, &upper) != nil {
		return ErrProviderIncompatible
	}
	if lower != "" && upper != "" && lower != upper {
		return ErrProviderIncompatible
	}
	decoded.ID = lower
	if decoded.ID == "" {
		decoded.ID = upper
	}
	*wire = providerThreadWire(decoded)
	return nil
}

func (provider *GitHubProvider) readThreads(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
	initialBytes int,
) ([]ProviderThread, bool, []string, int, error) {
	return provider.readThreadsWithPublicBytes(ctx, reviewCase, initialBytes, 0)
}

func (provider *GitHubProvider) readThreadsWithPublicBytes(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
	initialBytes int,
	initialPublicBytes int,
) ([]ProviderThread, bool, []string, int, error) {
	public, raw, complete, limitations, totalBytes, err := provider.scanThreads(
		ctx, reviewCase, initialBytes, initialPublicBytes,
	)
	_ = raw
	return public, complete, limitations, totalBytes, err
}

func (provider *GitHubProvider) scanThreads(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
	initialBytes int,
	initialPublicBytes int,
) ([]ProviderThread, []providerThreadWire, bool, []string, int, error) {
	owner, repo, _ := strings.Cut(reviewCase.Repository, "/")
	result := make([]ProviderThread, 0)
	rawResult := make([]providerThreadWire, 0)
	limitations := []string{}
	complete := false
	after := ""
	seenCursors := make(map[string]struct{})
	totalBytes := initialBytes
	totalComments := 0
	publicBytes := initialPublicBytes
	contentIncomplete := false
	seenThreadIDs := make(map[string]struct{})
	expectedTotal := -1
	for page := 1; page <= providerMaximumPages; page++ {
		args := map[string]any{"method": "get_review_comments", "owner": owner, "repo": repo,
			"pullNumber": reviewCase.PullNumber, "perPage": providerThreadsPerPage}
		if after != "" {
			args["after"] = after
		}
		remaining := providerMaximumTotalBytes - totalBytes
		if remaining <= 0 {
			limitations = append(limitations, providerLimitationThreadsCapped)
			break
		}
		raw, err := provider.readExact(
			ctx, args, minProviderLimit(providerMaximumResultBytes, remaining),
		)
		if err != nil {
			if contextErr := providerContextError(ctx, err); contextErr != nil {
				return nil, nil, false, nil, totalBytes, contextErr
			}
			if errors.Is(err, errProviderResultLimit) {
				limitations = append(limitations, providerLimitationThreadsCapped)
				break
			}
			if errors.Is(err, ErrProviderIncompatible) {
				return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
			}
			return nil, nil, false, nil, totalBytes, ErrUnavailable
		}
		totalBytes += len(raw)
		if totalBytes > providerMaximumTotalBytes {
			limitations = append(limitations, providerLimitationThreadsCapped)
			break
		}
		var pageWire providerThreadsWire
		if err := decodeProviderExactJSON(raw, &pageWire); err != nil || pageWire.ReviewThreads == nil ||
			len(pageWire.ReviewThreads) > providerThreadsPerPage || pageWire.TotalCount < 0 {
			return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
		}
		if expectedTotal == -1 {
			expectedTotal = pageWire.TotalCount
		} else if expectedTotal != pageWire.TotalCount {
			return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
		}
		for _, thread := range pageWire.ReviewThreads {
			if len(result) >= providerMaximumThreads {
				limitations = append(limitations, providerLimitationThreadsCapped)
				return result, rawResult, false, uniqueProviderLimitations(limitations), totalBytes, nil
			}
			if len(thread.Comments) > providerMaximumCommentsPerThread {
				return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
			}
			if thread.TotalCount < len(thread.Comments) {
				return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
			}
			if thread.ID != "" {
				if _, duplicate := seenThreadIDs[thread.ID]; duplicate {
					return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
				}
				seenThreadIDs[thread.ID] = struct{}{}
			}
			if totalComments+len(thread.Comments) > providerMaximumComments {
				remainingComments := providerMaximumComments - totalComments
				if remainingComments <= 0 {
					limitations = append(limitations, providerLimitationCommentsCapped)
					return result, rawResult, false, uniqueProviderLimitations(limitations), totalBytes, nil
				}
				thread.Comments = thread.Comments[:remainingComments]
				limitations = append(limitations, providerLimitationCommentsCapped)
				contentIncomplete = true
			}
			projected, projectionLimits, err := provider.projectThread(
				reviewCase.ID, reviewCase.PullURL, thread,
			)
			if err != nil {
				return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
			}
			limitations = append(limitations, projectionLimits...)
			if containsProviderLimitationValue(projectionLimits, providerLimitationThreadComments) {
				contentIncomplete = true
			}
			size := providerPublicSizeThread(projected)
			if publicBytes+size > providerMaximumPublicBytes {
				limitations = append(limitations, providerLimitationPublicBytes)
				return result, rawResult, false, uniqueProviderLimitations(limitations), totalBytes, nil
			}
			publicBytes += size
			totalComments += len(projected.Comments)
			result = append(result, projected)
			thread.Comments = nil
			rawResult = append(rawResult, thread)
			if len(rawResult) > expectedTotal {
				return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
			}
		}
		if !pageWire.PageInfo.HasNextPage {
			complete = true
			break
		}
		next := strings.TrimSpace(pageWire.PageInfo.EndCursor)
		if next == "" {
			return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
		}
		if _, exists := seenCursors[next]; exists {
			limitations = append(limitations, providerLimitationThreadsCapped)
			break
		}
		seenCursors[next] = struct{}{}
		after = next
	}
	if !complete && len(limitations) == 0 {
		limitations = append(limitations, providerLimitationThreadsCapped)
	}
	if complete && expectedTotal != len(rawResult) {
		if expectedTotal < len(rawResult) {
			return nil, nil, false, nil, totalBytes, ErrProviderIncompatible
		}
		complete = false
		limitations = append(limitations, providerLimitationThreadsCapped)
	}
	return result, rawResult, complete && !contentIncomplete, uniqueProviderLimitations(limitations), totalBytes, nil
}

func (provider *GitHubProvider) projectThread(
	caseID string,
	pullURL string,
	wire providerThreadWire,
) (ProviderThread, []string, error) {
	limitations := []string{}
	thread := ProviderThread{IsResolved: wire.IsResolved, IsOutdated: wire.IsOutdated,
		IsCollapsed: wire.IsCollapsed, TotalCount: wire.TotalCount, Comments: make([]ProviderThreadComment, 0, len(wire.Comments))}
	if wire.TotalCount < 0 || wire.TotalCount < len(wire.Comments) {
		return ProviderThread{}, nil, ErrProviderIncompatible
	}
	if wire.TotalCount > len(wire.Comments) {
		limitations = append(limitations, providerLimitationThreadComments)
	}
	if validProviderThreadID(wire.ID) {
		thread.Token = provider.threadToken(caseID, wire.ID)
		thread.CanResolve = provider.WriteReady
	} else {
		limitations = append(limitations, providerLimitationThreadIdentity)
	}
	for _, comment := range wire.Comments {
		if !validProviderText(comment.Body, providerMaximumBodyBytes, false) ||
			!validProviderText(comment.Path, providerMaximumPathBytes, false) ||
			!validProviderText(comment.Author, providerMaximumAuthorBytes, false) ||
			!validOptionalProviderTime(comment.CreatedAt) || !validOptionalProviderTime(comment.UpdatedAt) ||
			!providerURLBelongsToPull(comment.HTMLURL, pullURL) || comment.Line != nil && *comment.Line <= 0 {
			return ProviderThread{}, nil, ErrProviderIncompatible
		}
		line := comment.Line
		if line != nil {
			copied := *line
			line = &copied
		}
		thread.Comments = append(thread.Comments, ProviderThreadComment{Body: comment.Body, Path: comment.Path,
			Line: line, Author: comment.Author, CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt, URL: comment.HTMLURL})
	}
	return thread, limitations, nil
}

func (provider *GitHubProvider) findThreadID(
	ctx context.Context,
	reviewCase eventing.ReviewCase,
	token string,
) (string, bool, error) {
	_, rawThreads, _, _, _, err := provider.scanThreads(ctx, reviewCase, 0, 0)
	if err != nil {
		return "", false, err
	}
	for _, thread := range rawThreads {
		if validProviderThreadID(thread.ID) && hmac.Equal([]byte(provider.threadToken(reviewCase.ID, thread.ID)), []byte(token)) {
			return thread.ID, thread.IsResolved, nil
		}
	}
	return "", false, nil
}

func (provider *GitHubProvider) threadToken(caseID, threadID string) string {
	mac := hmac.New(sha256.New, provider.tokenKey)
	_, _ = mac.Write([]byte("picoclaw-review-thread-token-v1\x00" + caseID + "\x00" + threadID))
	return "rtt_" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (provider *GitHubProvider) readExact(ctx context.Context, args map[string]any, limit int) ([]byte, error) {
	outputs, err := provider.run(ctx, GitHubPullRequestReadTool, args)
	if err != nil {
		return nil, err
	}
	return provider.exactJSON(outputs, limit)
}

func (provider *GitHubProvider) run(ctx context.Context, tool string, args map[string]any) (map[string]any, error) {
	server := strings.TrimSpace(provider.Server)
	if server == "" {
		server = DefaultGitHubMCPServer
	}
	return provider.Runner.RunTool(ctx, workflows.ToolRequest{Name: picomcp.CanonicalToolName(server, tool), Args: args,
		MCP: true, MCPServer: server, MCPTool: tool})
}

func (provider *GitHubProvider) exactJSON(outputs map[string]any, limit int) ([]byte, error) {
	if outputs == nil || limit <= 0 || limit > providerMaximumResultBytes {
		return nil, errors.New("provider result is invalid")
	}
	if rawTags, present := outputs["artifact_tags"]; present {
		tags, ok := rawTags.([]string)
		if !ok {
			return nil, errors.New("provider artifact tags are invalid")
		}
		if len(tags) > 0 {
			return provider.exactArtifactJSON(tags, limit)
		}
	}
	value, ok := outputs["text"].(string)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return nil, errors.New("provider result is not bounded exact JSON")
	}
	if len(value) > limit {
		return nil, errProviderResultLimit
	}
	if !utf8.ValidString(value) || !json.Valid([]byte(value)) {
		return nil, ErrProviderIncompatible
	}
	return []byte(value), nil
}

func (provider *GitHubProvider) exactArtifactJSON(
	tags []string,
	limit int,
) (raw []byte, returnErr error) {
	if len(tags) != 1 || provider.ArtifactRoot == "" {
		return nil, errors.New("provider exact JSON artifact unavailable")
	}
	rawPath, ok := strings.CutPrefix(tags[0], "[file:")
	if !ok || !strings.HasSuffix(rawPath, "]") {
		return nil, errors.New("invalid provider artifact tag")
	}
	rawPath = strings.TrimSuffix(rawPath, "]")
	artifact, err := acquireProviderArtifact(
		provider.ArtifactRoot,
		rawPath,
		provider.artifactCleanupHook,
	)
	if artifact != nil {
		defer func() {
			cleanupErr := artifact.Consume()
			if cleanupErr != nil {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}()
	}
	if err != nil {
		return nil, err
	}
	if artifact == nil || artifact.File == nil {
		return nil, errors.New("provider artifact cannot be opened safely")
	}
	if artifact.Size > int64(limit) {
		return nil, errProviderResultLimit
	}
	var readErr error
	raw, readErr = io.ReadAll(io.LimitReader(artifact.File, int64(limit)+1))
	closeErr := artifact.File.Close()
	artifact.File = nil
	if readErr != nil || closeErr != nil {
		return nil, errors.New("provider artifact cannot be read safely")
	}
	if len(raw) > limit {
		return nil, errProviderResultLimit
	}
	if !utf8.Valid(raw) {
		return nil, ErrProviderIncompatible
	}
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		return nil, ErrProviderIncompatible
	}
	return raw, nil
}

type providerArtifact struct {
	File    *os.File
	Size    int64
	consume func() error
}

func (artifact *providerArtifact) Consume() error {
	if artifact == nil {
		return nil
	}
	var closeErr error
	if artifact.File != nil {
		closeErr = artifact.File.Close()
		artifact.File = nil
	}
	var cleanupErr error
	if artifact.consume != nil {
		cleanupErr = artifact.consume()
		artifact.consume = nil
	}
	return errors.Join(closeErr, cleanupErr)
}

func decodeProviderExactJSON(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > providerMaximumResultBytes || !utf8.Valid(raw) || !json.Valid(raw) {
		return ErrProviderIncompatible
	}
	if err := rejectDuplicateReviewJSONKeys(raw); err != nil {
		return ErrProviderIncompatible
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrProviderIncompatible
	}
	return nil
}

func validProviderPullURL(raw, repository string, pullNumber int64) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	wantPath := "/" + repository + "/pull/" + strconv.FormatInt(pullNumber, 10)
	return strings.EqualFold(strings.TrimSuffix(parsed.Path, "/"), wantPath)
}

func sameProviderPullURL(actual, expected string) bool {
	a, errA := url.Parse(actual)
	e, errE := url.Parse(expected)
	return errA == nil && errE == nil && a.Scheme == "https" && e.Scheme == "https" && a.User == nil && e.User == nil && a.RawQuery == "" && a.Fragment == "" &&
		strings.EqualFold(a.Host, e.Host) && strings.EqualFold(strings.TrimSuffix(a.Path, "/"), strings.TrimSuffix(e.Path, "/"))
}

func validOptionalProviderURL(raw string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > providerMaximumURLBytes || !utf8.ValidString(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func providerURLBelongsToPull(raw string, pullURL string) bool {
	if raw == "" {
		return true
	}
	if len(raw) > providerMaximumURLBytes || !utf8.ValidString(raw) {
		return false
	}
	actual, actualErr := url.Parse(raw)
	pull, pullErr := url.Parse(pullURL)
	return actualErr == nil && pullErr == nil &&
		actual.Scheme == "https" && pull.Scheme == "https" &&
		actual.User == nil && pull.User == nil &&
		actual.RawQuery == "" && pull.RawQuery == "" &&
		strings.EqualFold(actual.Host, pull.Host) &&
		strings.EqualFold(strings.TrimSuffix(actual.Path, "/"), strings.TrimSuffix(pull.Path, "/"))
}

func validOptionalProviderTime(raw string) bool {
	if raw == "" {
		return true
	}
	_, err := time.Parse(time.RFC3339, raw)
	return err == nil
}
func validProviderText(raw string, maximum int, required bool) bool {
	return (!required || raw != "") && len(raw) <= maximum && utf8.ValidString(raw) && !strings.ContainsRune(raw, 0)
}
func validProviderNumericID(raw string) bool {
	if raw == "" || len(raw) > 32 {
		return false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return false
		}
	}
	return raw != "0"
}
func validProviderThreadID(raw string) bool {
	return raw != "" && len(raw) <= providerMaximumThreadID && utf8.ValidString(raw) && raw == strings.TrimSpace(raw) && !strings.ContainsRune(raw, 0)
}
func validProviderToken(raw string) bool {
	if !strings.HasPrefix(raw, "rtt_") || len(raw) > 128 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, "rtt_"))
	return err == nil && len(decoded) == sha256.Size
}
func threadsHaveTokens(threads []ProviderThread) bool {
	for _, thread := range threads {
		if thread.Token != "" {
			return true
		}
	}
	return false
}
func uniqueProviderLimitations(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func containsProviderLimitationValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func providerPublicSizeReview(value ProviderReview) int {
	raw, _ := json.Marshal(value)
	return len(raw) + 1
}
func providerPublicSizeReviews(values []ProviderReview) int {
	total := 0
	for _, value := range values {
		total += providerPublicSizeReview(value)
	}
	return total
}
func providerPublicSizeThread(value ProviderThread) int {
	raw, _ := json.Marshal(value)
	return len(raw) + 1
}

func providerPublicSnapshotBaseSize(
	reviewCase eventing.ReviewCase,
	pull ProviderPullRequest,
) int {
	base := ProviderSnapshot{
		Availability: ProviderAvailabilityPartial,
		Connector:    reviewCase.Connector,
		Repository:   reviewCase.Repository,
		PullNumber:   reviewCase.PullNumber,
		PullRequest:  &pull,
		Capabilities: ProviderCapabilities{},
		Reviews:      []ProviderReview{},
		Limitations:  []string{},
		Threads:      []ProviderThread{},
	}
	raw, _ := json.Marshal(base)
	return len(raw)
}

func minProviderLimit(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func providerContextError(ctx context.Context, err error) error {
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}
