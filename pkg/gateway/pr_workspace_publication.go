package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/reviews"
)

type prWorkspaceReviewPublicationRuntime struct {
	submitter reviews.Submitter
	provider  *reviews.GitHubProvider
}

func (runtime *prWorkspaceReviewPublicationRuntime) PublishReview(
	ctx context.Context,
	request prworkspace.ReviewPublicationRequest,
) (prworkspace.ReviewPublicationResult, error) {
	if runtime == nil || runtime.submitter == nil {
		return prworkspace.ReviewPublicationResult{}, errors.New("GitHub review publisher is unavailable")
	}
	submitRequest, requestErr := reviewSubmitRequest(request)
	if requestErr != nil {
		return prworkspace.ReviewPublicationResult{}, requestErr
	}
	result, err := runtime.submitter.Submit(ctx, submitRequest)
	publication := submittedReviewPublicationResult(result)
	if err != nil {
		var stageErr *reviews.SubmitStageError
		if errors.As(err, &stageErr) && stageErr.ExternalStateMayHaveChanged {
			publication.Ambiguous = true
		}
		return publication, err
	}
	if publication.ExternalID == "" ||
		!samePRWorkspaceReviewURL(publication.ExternalURL, request.Provider, publication.ExternalID) {
		publication.Ambiguous = true
		return publication, errors.New("GitHub review response omitted or mismatched its external identity")
	}
	return publication, nil
}

func (runtime *prWorkspaceReviewPublicationRuntime) ReconcileReview(
	ctx context.Context,
	request prworkspace.ReviewPublicationRequest,
) (prworkspace.ReviewPublicationResult, bool, error) {
	if runtime == nil || runtime.provider == nil || runtime.submitter == nil ||
		strings.TrimSpace(request.Marker) == "" {
		return prworkspace.ReviewPublicationResult{}, false, errors.New("GitHub review reconciler is unavailable")
	}
	match, err := runtime.findReviewByMarker(ctx, request.Provider, request.Marker)
	if err != nil {
		return prworkspace.ReviewPublicationResult{}, false, err
	}
	switch match.state {
	case reviewMarkerAbsent:
		return prworkspace.ReviewPublicationResult{}, false, nil
	case reviewMarkerSubmitted:
		return match.result, true, nil
	case reviewMarkerPending:
		// A marker-bearing pending review proves that create reached GitHub, but
		// it is not a successful publication. Complete that exact pending review
		// with a body containing every frozen finding, then re-observe COMMENTED
		// state before reporting success.
		submitRequest, requestErr := reviewSubmitRequest(request)
		if requestErr != nil {
			return prworkspace.ReviewPublicationResult{}, false, requestErr
		}
		if _, submitErr := runtime.submitter.SubmitPending(ctx, submitRequest); submitErr != nil {
			match.result.Ambiguous = true
			return match.result, false, submitErr
		}
		observed, observeErr := runtime.findReviewByMarker(ctx, request.Provider, request.Marker)
		if observeErr != nil {
			match.result.Ambiguous = true
			return match.result, false, observeErr
		}
		if observed.state != reviewMarkerSubmitted {
			match.result.Ambiguous = true
			return match.result, false, errors.New("recovered GitHub review is not yet visible as submitted COMMENTED")
		}
		return observed.result, true, nil
	default:
		return prworkspace.ReviewPublicationResult{}, false, errors.New("GitHub review marker state is invalid")
	}
}

type reviewMarkerState uint8

const (
	reviewMarkerAbsent reviewMarkerState = iota
	reviewMarkerPending
	reviewMarkerSubmitted
)

type reviewMarkerMatch struct {
	state  reviewMarkerState
	result prworkspace.ReviewPublicationResult
}

func (runtime *prWorkspaceReviewPublicationRuntime) findReviewByMarker(
	ctx context.Context,
	provider prworkspace.ProviderSnapshot,
	marker string,
) (reviewMarkerMatch, error) {
	var match reviewMarkerMatch
	for page := 1; page <= reviews.MaxWorkspaceReviewHistoryPages; page++ {
		raw, err := runtime.provider.ReadWorkspaceReviewsJSON(ctx, provider.Repository, provider.PullNumber, page)
		if err != nil {
			return reviewMarkerMatch{}, err
		}
		var values []struct {
			ID       json.RawMessage `json:"id"`
			Body     string          `json:"body"`
			HTMLURL  string          `json:"html_url"`
			CommitID string          `json:"commit_id"`
			State    string          `json:"state"`
		}
		if json.Unmarshal(raw, &values) != nil || values == nil || len(values) > 100 {
			return reviewMarkerMatch{}, errors.New("GitHub review history is invalid")
		}
		for _, value := range values {
			occurrences := strings.Count(value.Body, marker)
			if occurrences == 0 {
				continue
			}
			id := githubPositiveNumericID(value.ID)
			if occurrences != 1 || match.state != reviewMarkerAbsent ||
				value.CommitID != provider.HeadSHA || id == "" ||
				!samePRWorkspaceReviewURL(value.HTMLURL, provider, id) {
				return reviewMarkerMatch{}, errors.New("GitHub review marker response is malformed or duplicated")
			}
			match.result = prworkspace.ReviewPublicationResult{
				ExternalID: id, ExternalURL: value.HTMLURL,
			}
			switch strings.ToUpper(strings.TrimSpace(value.State)) {
			case "PENDING":
				match.state = reviewMarkerPending
			case "COMMENTED":
				match.state = reviewMarkerSubmitted
			default:
				return reviewMarkerMatch{}, errors.New("GitHub review marker has an unexpected review state")
			}
		}
		if len(values) < 100 {
			return match, nil
		}
	}
	return reviewMarkerMatch{}, errors.New("GitHub review history exceeds the reconciliation bound")
}

func samePRWorkspaceReviewURL(raw string, provider prworkspace.ProviderSnapshot, reviewID string) bool {
	parsedID, idErr := strconv.ParseUint(reviewID, 10, 64)
	external, err := url.Parse(raw)
	if idErr != nil || parsedID == 0 || err != nil || external.Scheme != "https" || external.Host == "" ||
		external.User != nil ||
		external.Opaque != "" ||
		external.RawPath != "" ||
		external.RawQuery != "" ||
		external.ForceQuery ||
		external.RawFragment != "" ||
		external.Fragment != "pullrequestreview-"+reviewID {
		return false
	}
	expected, err := url.Parse(prWorkspacePullURL(provider))
	return err == nil && expected.Scheme == "https" && expected.Host != "" && expected.User == nil &&
		expected.Opaque == "" && expected.RawPath == "" && expected.RawQuery == "" && !expected.ForceQuery &&
		strings.EqualFold(external.Scheme, expected.Scheme) &&
		strings.EqualFold(external.Host, expected.Host) && external.Path == expected.Path
}

func reviewSubmitRequest(request prworkspace.ReviewPublicationRequest) (reviews.SubmitRequest, error) {
	owner, repo, ok := strings.Cut(request.Provider.Repository, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return reviews.SubmitRequest{}, prworkspace.ErrInvalid
	}
	findings := make([]reviews.SubmitFinding, 0, len(request.Findings))
	for _, finding := range request.Findings {
		findings = append(findings, reviews.SubmitFinding{
			ID: finding.ID, Title: finding.Title, File: finding.File,
			Line: finding.Line, Message: finding.Message,
		})
	}
	return reviews.SubmitRequest{
		Owner: owner, Repo: repo, PullNumber: request.Provider.PullNumber,
		HeadSHA: request.Provider.HeadSHA, Summary: request.Summary,
		Marker: request.Marker, Findings: findings,
	}, nil
}

func submittedReviewPublicationResult(result reviews.SubmitResult) prworkspace.ReviewPublicationResult {
	id, externalURL := externalIdentity(result.SubmittedReview)
	return prworkspace.ReviewPublicationResult{ExternalID: id, ExternalURL: externalURL}
}

func externalIdentity(value any) (string, string) {
	switch typed := value.(type) {
	case map[string]any:
		id := scalarExternalID(typed["id"])
		externalURL, _ := typed["html_url"].(string)
		if id != "" && (externalURL == "" || safePRWorkspaceExternalURL(externalURL)) {
			return id, externalURL
		}
		for _, nested := range typed {
			if nestedID, nestedURL := externalIdentity(nested); nestedID != "" {
				return nestedID, nestedURL
			}
		}
	case []any:
		for _, nested := range typed {
			if nestedID, nestedURL := externalIdentity(nested); nestedID != "" {
				return nestedID, nestedURL
			}
		}
	}
	return "", ""
}

func scalarExternalID(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		if typed > 0 && typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
	}
	return ""
}

func safePRWorkspaceExternalURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Opaque == ""
}

func prWorkspacePullURL(provider prworkspace.ProviderSnapshot) string {
	return fmt.Sprintf(
		"%s/%s/pull/%d",
		strings.TrimSuffix(provider.ProviderOrigin, "/"),
		provider.Repository,
		provider.PullNumber,
	)
}

var _ prworkspace.ReviewPublisher = (*prWorkspaceReviewPublicationRuntime)(nil)
